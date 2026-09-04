defmodule Mitos.GCTest do
  @moduledoc """
  Reclaiming leftovers (`Mitos.GC`).

  The distinction these pin is the whole point: a sandbox and a checkpoint
  archive are *copies*, and the log, artifacts, trace and spool are *evidence*.
  A collector that blurred them would trade disk for the one thing that cannot
  be regenerated.
  """
  use ExUnit.Case, async: false

  alias Mitos.{Fixtures, GC, Run}

  describe "what it will and will not remove" do
    test "an orphan sandbox with no event log always goes" do
      path = sandbox("local", "run_orphan_" <> Mitos.id(), 4_000)

      entry = find(GC.scan(), path)
      assert entry.state == :orphan
      assert entry.reapable?
      assert entry.reason =~ "orphan"

      assert %{removed: removed} = GC.sweep()
      assert Enum.any?(removed, &(&1.path == path))
      refute File.exists?(path)
    end

    test "a live run's sandbox is never a candidate, at any age" do
      run_id = live_run()
      path = sandbox_for(run_id, 1_000)
      backdate(path, 400)

      entry = find(GC.scan(all: true), path)
      assert entry.state == :live
      refute entry.reapable?
      assert entry.reason =~ "live"

      # Even --all leaves it: deleting the workspace out from under a running
      # stage is the one mistake the log could not recover from.
      GC.sweep(all: true)
      assert File.exists?(path)
    end

    test "a recent failure is kept for inspection; an old one is not" do
      run_id = unfinished_run()
      path = sandbox_for(run_id, 1_000)

      entry = find(GC.scan(), path)
      assert entry.state == :unfinished
      refute entry.reapable?
      assert entry.reason =~ "kept for inspection"

      backdate(path, 30)
      aged = find(GC.scan(), path)
      assert aged.reapable?
      assert aged.reason =~ "30d old"
    end

    test "--all ignores age but not liveness" do
      run_id = unfinished_run()
      path = sandbox_for(run_id, 1_000)

      refute find(GC.scan(), path).reapable?
      assert find(GC.scan(all: true), path).reapable?
    end
  end

  describe "evidence is never touched" do
    test "the log, artifacts, trace and spool survive a sweep that takes the copies" do
      run_id = "local/run_ev_" <> Mitos.id()
      run_dir = Mitos.run_dir(run_id)

      # Evidence.
      File.mkdir_p!(Path.join(run_dir, "artifacts"))
      File.write!(Path.join(run_dir, "artifacts/abc123"), "harvested diff")
      File.mkdir_p!(Path.join(run_dir, "trace"))
      File.write!(Path.join(run_dir, "trace/stage.jsonl"), "{}\n")
      File.mkdir_p!(Path.join(run_dir, "spool"))
      File.write!(Path.join(run_dir, "spool/stage.pty"), "bytes")
      File.write!(Path.join(run_dir, "config.json"), "{}")

      # Copies (16 §3.1/§3.2).
      fill(Path.join(run_dir, "checkpoints/cp1/workspace"), 2_000)
      fill(Path.join(run_dir, "acceptance/stage/x"), 2_000)
      fill(Path.join(run_dir, "rewind/cp1"), 2_000)

      GC.sweep(all: true)

      refute File.dir?(Path.join(run_dir, "checkpoints"))
      refute File.dir?(Path.join(run_dir, "acceptance"))
      refute File.dir?(Path.join(run_dir, "rewind"))

      assert File.read!(Path.join(run_dir, "artifacts/abc123")) == "harvested diff"
      assert File.exists?(Path.join(run_dir, "trace/stage.jsonl"))
      assert File.exists?(Path.join(run_dir, "spool/stage.pty"))
      assert File.exists?(Path.join(run_dir, "config.json"))
    end
  end

  describe "the dry run" do
    test "reports what would go without touching it" do
      path = sandbox("local", "run_dry_" <> Mitos.id(), 3_000)

      assert %{dry_run: true, removed: removed, bytes: bytes} = GC.sweep(dry_run: true)
      assert Enum.any?(removed, &(&1.path == path))
      assert bytes > 0
      assert File.exists?(path)

      assert GC.report(GC.sweep(dry_run: true)) =~ "would reclaim"
    end
  end

  describe "reaping on finish" do
    test "a successful run's sandbox goes; a failed run's is kept" do
      prev = Application.get_env(:mitos, :gc, [])
      Application.put_env(:mitos, :gc, Keyword.put(prev, :reap_on_finish, true))
      on_exit(fn -> Application.put_env(:mitos, :gc, prev) end)

      done = "local/run_done_" <> Mitos.id()
      failed = "local/run_failed_" <> Mitos.id()
      done_path = sandbox_for(done, 500)
      failed_path = sandbox_for(failed, 500)

      assert GC.reap_after_run(done, :finished) == :reaped
      refute File.exists?(done_path)

      # Reclaiming a failure's sandbox trades the cheap resource for the
      # expensive one — you want the wreckage.
      assert GC.reap_after_run(failed, :aborted) == :kept
      assert File.exists?(failed_path)
    end

    test "a real run cleans up after itself" do
      # The suite keeps sandboxes (config/test.exs) because other tests assert
      # on their internals; this one is about the production default.
      prev = Application.get_env(:mitos, :gc, [])
      Application.put_env(:mitos, :gc, Keyword.put(prev, :reap_on_finish, true))
      on_exit(fn -> Application.put_env(:mitos, :gc, prev) end)

      {:ok, run_id} =
        Run.start_run(
          tenant: %{"org" => "test", "project" => "gc"},
          pipeline: Fixtures.pipeline(["work"]),
          capabilities: %{"work" => Fixtures.capability("work")},
          workspace: Fixtures.workspace(%{"README.md" => "x"})
        )

      state = Run.await(run_id)
      assert state.status == :finished

      # The evidence outlives the copy (05 §5.4).
      refute File.exists?(Path.join([Mitos.data_dir(), "sandboxes", run_id]))
      assert File.exists?(Mitos.Log.path(run_id))
      assert state.stages["work"].result["signal"] == "ship"
    end
  end

  test "nothing outside the data dir can be removed" do
    outside = Path.join(System.tmp_dir!(), "mitos-gc-outside-" <> Mitos.id())
    File.mkdir_p!(outside)
    File.write!(Path.join(outside, "precious"), "operator's own tree")

    # The guard that matters in `:in_place` mode, where a "workspace" is a real
    # directory on the operator's disk (05 §5.1).
    GC.sweep(all: true)
    assert File.exists?(Path.join(outside, "precious"))
  end

  # --- helpers ---

  defp sandbox(org, run, bytes), do: fill(Path.join([GC.sandbox_root(), org, run]), bytes)

  defp sandbox_for(run_id, bytes), do: fill(Path.join(GC.sandbox_root(), run_id), bytes)

  defp fill(path, bytes) do
    File.mkdir_p!(path)
    File.write!(Path.join(path, "blob"), String.duplicate("x", bytes))
    path
  end

  defp backdate(path, days) do
    past = System.os_time(:second) - days * 86_400
    File.touch!(path, past)
    path
  end

  defp live_run do
    run_id = "local/run_live_" <> Mitos.id()
    {:ok, _} = Run.start_run(run_id: run_id, pipeline: "# Pipeline: p\n", capabilities: %{}, policy: HoldingPolicy)
    await_live(run_id)
    run_id
  end

  defp unfinished_run do
    run_id = "local/run_unfin_" <> Mitos.id()
    File.mkdir_p!(Mitos.run_dir(run_id))

    File.write!(
      Mitos.Log.path(run_id),
      Mitos.Event.to_line(%Mitos.Event.Effect{type: :run_started, run_id: run_id, seq: 0, ts: Mitos.ts()})
    )

    run_id
  end

  defp await_live(run_id, tries \\ 100) do
    cond do
      Mitos.Log.writer_held?(run_id) -> :ok
      tries == 0 -> :timeout
      true ->
        Process.sleep(10)
        await_live(run_id, tries - 1)
    end
  end

  defp find(entries, path), do: Enum.find(entries, &(&1.path == path))
end

defmodule HoldingPolicy do
  @moduledoc false
  # Parks and stays parked, so the run process keeps holding its writer for the
  # duration of a test that needs a genuinely live run.
  @behaviour Mitos.Orchestrator.Policy

  @impl true
  def decide(_ctx) do
    Mitos.Event.Decision.new(
      actor: "orchestrator",
      type: :request_approval,
      params: %{"question" => "hold", "blocking" => true},
      inputs_hash: "hold",
      rationale: "test policy holds the run open",
      budget: %{}
    )
  end
end
