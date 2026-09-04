defmodule Mitos.GC do
  @moduledoc """
  Reclaiming what a run leaves behind.

  `Provider.Local.stop/1` keeps the sandbox "for audit" and nothing ever removes
  it, so every run permanently costs a copy of its workspace — plus, since `16`,
  a checkpoint archive and an acceptance verify dir *per stage*. On a real repo
  that is how a disk fills: not one large thing, but the same tree kept N times
  for runs that finished weeks ago.

  Two kinds of leftovers, deliberately distinguished, because only one of them
  is evidence:

  | Reclaimable | Evidence — never touched |
  |---|---|
  | `sandboxes/<org>/<run>/` — the working copy | `runs/<org>/<run>/events.jsonl` — the log IS the run (`01 §4`) |
  | `runs/**/checkpoints/` — archived trees (`16 §3.1`) | `runs/**/artifacts/` — content-addressed harvest, what proof reads (`08 §5`) |
  | `runs/**/acceptance/` — verify dirs (`16 §3.2`) | `runs/**/trace/`, `spool/` — the inner zoom and the raw terminal record (`11 §4`, `14 §3.1`) |
  | `runs/**/rewind/` — restored trees | `config.json` — what `resume/1` needs (`04 §7`) |

  Everything in the right column is small, content-addressed, or irreplaceable;
  everything in the left column is a copy that can be made again. Removing a
  sandbox therefore costs nothing a reader of the log could have wanted — with
  one exception this module honours: **the wreckage of a recent failure is worth
  keeping**, so an unfinished run is reaped only once it is old, or on `--all`.

  A run whose process still holds its writer is never a candidate, at any age.
  """

  @default_keep_days 7

  @reclaimable_run_subdirs ~w(checkpoints acceptance rewind)

  @type state :: :live | :terminal | :unfinished | :orphan

  @doc "Root under which every sandbox lives. Nothing outside it is ever removed."
  def sandbox_root, do: Path.join(Mitos.data_dir(), "sandboxes")

  @doc """
  What is on disk, and whether it may go.

  Options: `all: true` (ignore age), `older_than: days`.
  """
  def scan(opts \\ []) do
    keep_days = opts[:older_than] || config(:keep_days, @default_keep_days)
    all? = !!opts[:all]

    sandboxes(keep_days, all?) ++ run_bulk(keep_days, all?)
  end

  @doc """
  Reclaim. Returns `%{removed, kept, bytes, dry_run}`; with `dry_run: true`
  nothing is deleted and `removed` is what would have been.
  """
  def sweep(opts \\ []) do
    entries = scan(opts)
    {reapable, kept} = Enum.split_with(entries, & &1.reapable?)

    removed =
      if opts[:dry_run] do
        reapable
      else
        Enum.filter(reapable, &remove(&1.path))
      end

    %{
      removed: removed,
      kept: kept,
      bytes: Enum.reduce(removed, 0, &(&1.bytes + &2)),
      dry_run: !!opts[:dry_run]
    }
  end

  @doc """
  Reap one run's sandbox after it finishes.

  Default is successful runs only: a failed run's sandbox is the first thing you
  want to look at, and reclaiming it to save disk would trade the cheap resource
  for the expensive one. Off entirely with `config :mitos, :gc, reap_on_finish: false`.
  """
  def reap_after_run(run_id, status) do
    cond do
      not config(:reap_on_finish, true) -> :skipped
      status != :finished -> :kept
      true -> if remove(sandbox_path(run_id)), do: :reaped, else: :kept
    end
  end

  @doc "Human-readable byte size."
  def human(bytes), do: Mitos.Workspace.human(bytes)

  @doc "A `mitos gc` report."
  def report(%{removed: removed, kept: kept, bytes: bytes, dry_run: dry?}) do
    verb = if dry?, do: "would reclaim", else: "reclaimed"

    lines =
      Enum.map_join(Enum.sort_by(removed, &(-&1.bytes)), "\n", fn e ->
        "  #{String.pad_trailing(human(e.bytes), 9)} #{e.kind}  #{e.run_id}  (#{e.reason})"
      end)

    held =
      kept
      |> Enum.filter(&(&1.state == :live))
      |> Enum.map_join("\n", &"  held: #{&1.run_id} is still running")

    [
      "#{verb} #{human(bytes)} across #{length(removed)} item(s)",
      lines,
      held,
      if(kept == [], do: nil, else: "  #{length(kept)} kept")
    ]
    |> Enum.reject(&(&1 in [nil, ""]))
    |> Enum.join("\n")
  end

  # --- scanning ---

  defp sandboxes(keep_days, all?) do
    sandbox_root()
    |> Path.join("*/*")
    |> Path.wildcard()
    |> Enum.filter(&File.dir?/1)
    |> Enum.map(fn path ->
      run_id = run_id_of(path)
      state = state_of(run_id)

      entry(%{
        kind: "sandbox",
        run_id: run_id,
        path: path,
        state: state,
        age_days: age_days(path)
      }, keep_days, all?)
    end)
  end

  # The per-stage copies `16` introduced. They live under the run dir beside the
  # evidence, so they are enumerated by name rather than by sweeping the dir —
  # a glob that reached `artifacts/` would be deleting proof.
  defp run_bulk(keep_days, all?) do
    Mitos.data_dir()
    |> Path.join("runs/*/*")
    |> Path.wildcard()
    |> Enum.filter(&File.dir?/1)
    |> Enum.flat_map(fn run_dir ->
      run_id = run_id_of(run_dir)
      state = state_of(run_id)

      for sub <- @reclaimable_run_subdirs,
          path = Path.join(run_dir, sub),
          File.dir?(path) do
        entry(%{
          kind: sub,
          run_id: run_id,
          path: path,
          state: state,
          age_days: age_days(path)
        }, keep_days, all?)
      end
    end)
  end

  defp entry(base, keep_days, all?) do
    {reapable?, reason} = verdict(base.state, base.age_days, keep_days, all?)

    base
    |> Map.put(:bytes, dir_bytes(base.path))
    |> Map.put(:reapable?, reapable?)
    |> Map.put(:reason, reason)
  end

  # A run still holding its writer is off limits at any age — deleting the
  # workspace out from under a running stage is the one mistake this module
  # could make that the log could not recover from.
  defp verdict(:live, _age, _keep, _all), do: {false, "run is live"}

  # No log at all: nothing references it and nothing can.
  defp verdict(:orphan, _age, _keep, _all), do: {true, "orphan, no event log"}

  defp verdict(state, age, keep, all?) do
    cond do
      all? -> {true, "--all"}
      age >= keep -> {true, "#{state}, #{age}d old"}
      state == :terminal -> {false, "terminal but only #{age}d old"}
      # The wreckage of a recent failure is worth keeping.
      true -> {false, "unfinished and recent — kept for inspection"}
    end
  end

  defp state_of(run_id) do
    cond do
      Mitos.Log.writer_held?(run_id) -> :live
      not Mitos.Log.exists?(run_id) -> :orphan
      terminal?(run_id) -> :terminal
      true -> :unfinished
    end
  end

  defp terminal?(run_id) do
    run_id
    |> Mitos.Log.read()
    |> Enum.any?(&match?(%Mitos.Event.Effect{type: :run_finished}, &1))
  rescue
    _ -> false
  end

  # "<data_dir>/sandboxes/<org>/<run>" and "<data_dir>/runs/<org>/<run>" both
  # carry the run id in their last two segments.
  defp run_id_of(path) do
    path |> Path.split() |> Enum.take(-2) |> Path.join()
  end

  defp sandbox_path(run_id), do: Path.join(sandbox_root(), run_id)

  defp age_days(path) do
    case File.stat(path, time: :posix) do
      {:ok, %{mtime: m}} -> div(System.os_time(:second) - m, 86_400)
      _ -> 0
    end
  end

  defp dir_bytes(path) do
    path
    |> Path.join("**")
    |> Path.wildcard(match_dot: true)
    |> Enum.reduce(0, fn f, acc ->
      case File.stat(f) do
        {:ok, %{type: :regular, size: n}} -> acc + n
        _ -> acc
      end
    end)
  end

  # The one guard that matters: never remove anything outside the data dir, so a
  # malformed run id can't turn a reap into a delete of the operator's tree —
  # which, in `:in_place` mode (`05 §5.1`), is a real directory on their disk.
  defp remove(path) do
    root = Path.expand(Mitos.data_dir())
    full = Path.expand(path)

    if String.starts_with?(full, root <> "/") and File.dir?(full) do
      File.rm_rf!(full)
      true
    else
      false
    end
  end

  defp config(key, default),
    do: Application.get_env(:mitos, :gc, []) |> Keyword.get(key, default)
end
