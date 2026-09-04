defmodule Mitos.Log do
  @moduledoc """
  The append-only event log — the single source of truth (01 §4, 04 §6).

      state(t) = fold(events[0..t])          # no separately-stored run state
      resume   = replay to the last committed Effect, then re-invoke orchestrator

  Every event is durably appended (fsync) BEFORE its effect is executed — this is
  what makes the crash test pass (04 §3.4/§7): a Decision on the log with no
  committed Effect is simply re-driven on recovery.

  Seq is monotonic per run and assigned by a **single writer** (04 §11). The
  `Writer` struct carries the next seq + an open file device; the Run server owns
  exactly one and is the only caller of `append/2`, so the log stays a single
  totally-ordered stream even under concurrent stages.
  """

  alias Mitos.Event
  alias Mitos.Event.{Decision, Effect}

  defmodule Writer do
    @moduledoc false
    @enforce_keys [:run_id, :device, :next_seq]
    defstruct [:run_id, :device, :next_seq]
  end

  def path(run_id), do: Path.join(Mitos.run_dir(run_id), "events.jsonl")

  @doc """
  Open (or reopen, for resume) the log for a run. On reopen, `next_seq` is
  computed from the existing committed events so replay is idempotent.
  """
  def open_writer(run_id) do
    File.mkdir_p!(Mitos.run_dir(run_id))
    p = path(run_id)
    existing = if File.exists?(p), do: read(run_id), else: []
    next_seq = (existing |> Enum.map(& &1.seq) |> Enum.max(fn -> -1 end)) + 1
    {:ok, device} = :file.open(String.to_charlist(p), [:append, :raw, :binary])
    {:ok, %Writer{run_id: run_id, device: device, next_seq: next_seq}, existing}
  end

  @doc """
  Append an event, stamping the next `seq` and `ts` if absent. Durable before
  return. Returns `{stamped_event, writer'}`.
  """
  def append(%Writer{} = w, %mod{} = event) when mod in [Decision, Effect] do
    event = %{event | seq: w.next_seq, run_id: w.run_id}
    event = if is_nil(event.ts), do: %{event | ts: Mitos.ts()}, else: event
    line = Event.to_line(event)
    :ok = :file.write(w.device, line)
    :ok = :file.datasync(w.device)
    {event, %{w | next_seq: w.next_seq + 1}}
  end

  def close(%Writer{device: device}), do: :file.close(device)

  @doc "Read all events for a run, ordered by seq (ascending)."
  def read(run_id) do
    p = path(run_id)

    case File.read(p) do
      {:ok, ""} ->
        []

      {:ok, bin} ->
        bin
        |> String.split("\n", trim: true)
        |> Enum.map(&Event.from_line/1)
        |> Enum.sort_by(& &1.seq)

      {:error, :enoent} ->
        []
    end
  end

  @doc """
  Append when **no run process holds the writer** — the run has finished or
  parked and released it (cf. `Mitos.IO.deliver_for/1`).

  This does not weaken the single-writer invariant (`01 §4`): it is used only
  once the owning process is gone, and callers serialize per run so a concurrent
  resume cannot interleave. It exists because a decision resolved after a run
  parks is exactly the event the log must not lose — `decision_precision`
  (`16 §6`) is computed from those records.
  """
  def append_detached(run_id, %mod{} = event) when mod in [Decision, Effect] do
    :global.trans({__MODULE__, {:detached, run_id}}, fn ->
      {:ok, writer, _} = open_writer(run_id)

      try do
        {stamped, _writer} = append(writer, event)
        {:ok, stamped}
      after
        close(writer)
      end
    end)
  end

  @doc "Is a run process currently holding this run's writer?"
  def writer_held?(run_id) do
    case Registry.lookup(Mitos.Run.Registry, run_id) do
      [] -> false
      _ -> true
    end
  end

  @doc "Does a log exist (has at least one committed event) for this run?"
  def exists?(run_id), do: File.exists?(path(run_id))
end
