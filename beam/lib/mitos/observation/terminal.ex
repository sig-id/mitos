defmodule Mitos.Observation.Terminal do
  @moduledoc """
  Channel C3 (14 §1, §3.1) — the terminal, re-admitted under three separations.

    1. **Parse continuously, materialize on demand.** Every byte is spooled and
       fed to `Mitos.Observation.Vt`, which emits nothing. `snapshot/2` is the
       only way a grid becomes visible, and it refuses unless the parser is at a
       consistent boundary *and* the session is at a quiescence edge.
    2. **A bounded question.** The grid goes to a modal classifier
       (`Mitos.Observation.Classifier`), never to open-ended extraction. Run
       semantics come from C1/C2 (14 §1).
    3. **Control is a verified transaction** — `Mitos.Steer`, not this module.

  The property the whole channel rests on: a snapshot is a **pure function of
  the spool prefix** `[0, pty_offset)`. `replay/2` recomputes it from the
  spooled artifact and any recorded `digest` verifies, so a screen read is
  evidence rather than an unrepeatable observation. That is what makes C3
  compose with audit, proof, and replay — the objection that banned it before.

  This module owns the BEAM side of the seam. Whoever owns the PTY bytes (the
  Zig engine locally, a provider stream remotely — `01 §7`) calls `feed/2`.
  """

  use GenServer

  alias Mitos.Observation.{Clock, Vt}

  defmodule Grid do
    @moduledoc "A materialized, digested screen read (14 §3.1)."

    @enforce_keys [:rows, :cols, :lines, :cursor, :alt_screen, :pty_offset, :digest]
    defstruct rows: 24,
              cols: 80,
              lines: [],
              cursor: %{row: 0, col: 0, visible: true},
              alt_screen: false,
              stable_at: nil,
              pty_offset: 0,
              digest: nil

    @doc "The grid as one string — what a classifier reads."
    def text(%__MODULE__{lines: lines}), do: Enum.join(lines, "\n")

    @doc "Non-blank lines, in order."
    def content(%__MODULE__{lines: lines}), do: Enum.reject(lines, &(&1 == ""))
  end

  # --- client ---

  def start_link(opts), do: GenServer.start_link(__MODULE__, opts, Keyword.take(opts, [:name]))

  @doc "Feed raw PTY bytes. Continuous, cheap, and never parsed for control."
  def feed(term, bytes), do: GenServer.call(term, {:feed, bytes})

  @doc """
  Materialize a digested grid. Returns `{:error, :unstable}` when the parser
  holds a partial escape or UTF-8 sequence, and `{:error, :busy}` when the
  session has not been quiet for `T_snap` — the two states in which a read
  would be a torn frame rather than a document (14 §3.1).

  Options:

    * `wait: ms` — retry until a readable grid appears, instead of failing fast.
    * `require_content: true` — treat a blank grid as not-yet-readable
      (`{:error, :blank}`). A session that has produced no bytes is quiet from
      the moment it starts, which is the right answer for `blocked_on_human`
      (`16 §2`) and the wrong one for a classifier: there is no modal state on
      an empty screen.
    * `differs_from: digest` — treat a grid identical to `digest` as
      not-yet-readable (`{:error, :unchanged}`). After a steer writes, the
      terminal is quiet until the response arrives, so without this "no response
      yet" and "the response changed nothing" are the same read (`14 §3.3`).
    * `min_quiet_ms`, `unguarded` — see `14 §3.1`.

  **The wait happens in the caller, never in the terminal process.** Blocking
  inside the server would queue the `feed/2` calls carrying the very bytes the
  wait is waiting for — the parser could not advance, so the wait could never
  succeed.
  """
  def snapshot(term, opts \\ []) do
    case opts[:wait] do
      nil -> GenServer.call(term, {:snapshot, opts})
      ms -> await_snapshot(term, opts, System.monotonic_time(:millisecond) + ms)
    end
  end

  defp await_snapshot(term, opts, deadline) do
    case GenServer.call(term, {:snapshot, Keyword.delete(opts, :wait)}) do
      {:ok, grid} ->
        {:ok, grid}

      {:error, reason} ->
        if System.monotonic_time(:millisecond) >= deadline do
          {:error, reason}
        else
          Process.sleep(10)
          await_snapshot(term, opts, deadline)
        end
    end
  end

  @doc "Spool path for this session (the raw, redacted, append-only artifact)."
  def spool_path(term), do: GenServer.call(term, :spool_path)

  @doc "Bytes consumed so far."
  def offset(term), do: GenServer.call(term, :offset)

  @doc """
  Recompute a grid from a spooled artifact — the replay side of the purity
  property (14 §3.1). `upto` bounds the prefix; omit it for the whole spool.
  A `digest` recorded at snapshot time MUST match the one returned here.
  """
  def replay(spool_path, opts \\ []) do
    with {:ok, bin} <- File.read(spool_path) do
      prefix =
        case opts[:upto] do
          nil -> bin
          n -> binary_part(bin, 0, min(n, byte_size(bin)))
        end

      vt =
        Vt.new(opts[:rows] || default_rows(), opts[:cols] || default_cols())
        |> Vt.feed(prefix)

      {:ok, grid(vt, nil)}
    end
  end

  @doc """
  A grid from an already-fed parser. Used by contract fixtures (`14 §5`) and by
  `replay/2`; both need the grid without a live session, and neither should have
  to reach into the parser struct to build one.
  """
  def replay_from_vt(%Vt{} = vt), do: {:ok, grid(vt, nil)}

  # --- server ---

  @impl true
  def init(opts) do
    spool = opts[:spool] || default_spool(opts)
    File.mkdir_p!(Path.dirname(spool))
    {:ok, device} = :file.open(String.to_charlist(spool), [:append, :raw, :binary])

    {:ok,
     %{
       run_id: opts[:run_id],
       stage_id: opts[:stage_id],
       spool: spool,
       device: device,
       clock: opts[:clock],
       thresholds: Clock.thresholds(opts[:thresholds] || %{}),
       redact: opts[:redact] || (&__MODULE__.default_redact/1),
       vt: Vt.new(opts[:rows] || default_rows(), opts[:cols] || default_cols())
     }}
  end

  @impl true
  def handle_call({:feed, bytes}, _from, st) do
    # First-pass redaction happens on the way to the spool (05 §5.2): the raw
    # artifact is evidence and outlives the run, so a secret must never land in
    # it. The parser sees the same redacted bytes, which keeps replay pure.
    bytes = st.redact.(bytes)
    :ok = :file.write(st.device, bytes)
    if st.clock, do: Clock.bump(st.clock, :pty, byte_size(bytes))
    {:reply, :ok, %{st | vt: Vt.feed(st.vt, bytes)}}
  end

  def handle_call({:snapshot, opts}, _from, st) do
    {:reply, do_snapshot(st, opts), st}
  end

  def handle_call(:spool_path, _from, st), do: {:reply, st.spool, st}
  def handle_call(:offset, _from, st), do: {:reply, st.vt.offset, st}

  @impl true
  def terminate(_reason, st), do: :file.close(st.device)

  defp do_snapshot(st, opts) do
    with :ok <- check_quiet(st, opts),
         true <- Vt.stable?(st.vt) or {:error, :unstable},
         grid = grid(st.vt, Mitos.ts()),
         :ok <- check_content(grid, opts) do
      {:ok, grid}
    else
      {:error, _} = err -> err
      false -> {:error, :unstable}
    end
  end

  defp check_content(grid, opts) do
    cond do
      opts[:require_content] && Grid.content(grid) == [] -> {:error, :blank}
      opts[:differs_from] && grid.digest == opts[:differs_from] -> {:error, :unchanged}
      true -> :ok
    end
  end

  # No clock attached means no way to know the repaint finished; a caller that
  # wants an unguarded read must say so explicitly rather than get one by
  # default (14 §3.1).
  defp check_quiet(%{clock: nil}, opts), do: if(opts[:unguarded], do: :ok, else: {:error, :no_clock})

  defp check_quiet(st, opts) do
    min_quiet = opts[:min_quiet_ms] || st.thresholds.snap
    if Clock.quiet?(st.clock, min_quiet), do: :ok, else: {:error, :busy}
  end

  defp grid(vt, stable_at) do
    {row, col} = vt.cursor

    %Grid{
      rows: vt.rows,
      cols: vt.cols,
      lines: Vt.render(vt),
      cursor: %{row: row, col: col, visible: vt.cursor_visible},
      alt_screen: vt.alt_screen,
      stable_at: stable_at,
      pty_offset: vt.offset,
      digest: Vt.digest(vt)
    }
  end

  @doc false
  # Conservative first-pass redaction: obvious bearer/API-key shapes never reach
  # the durable spool. The authority story does not depend on this (no secrets
  # enter the sandbox at all — 05 §5.1); it is defence for the artifact.
  def default_redact(bytes) do
    bytes
    |> String.replace(~r/\b(sk-[A-Za-z0-9_\-]{16,})/, "sk-<redacted>")
    |> String.replace(~r/\b(gh[pousr]_[A-Za-z0-9]{16,})/, "gh_<redacted>")
    |> String.replace(~r/(?i)\b(authorization:\s*bearer\s+)\S+/, "\\1<redacted>")
  end

  defp default_spool(opts) do
    Path.join([Mitos.run_dir(opts[:run_id] || "unknown"), "spool", "#{opts[:stage_id] || "session"}.pty"])
  end

  defp default_rows, do: observation(:rows, 24)
  defp default_cols, do: observation(:cols, 80)

  defp observation(key, default),
    do: Application.get_env(:mitos, :observation, []) |> Keyword.get(key, default)
end
