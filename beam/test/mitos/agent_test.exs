defmodule Mitos.AgentTest do
  use ExUnit.Case, async: false

  @moduletag :integration

  setup_all do
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

    unless File.exists?(binary) do
      flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
    end

    :ok
  end

  test "fake agent run: launch spec → session → engine prompt event" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "hello",
        session_id: session_id,
        cols: 80,
        rows: 24,
        # Short idle so the watcher snapshots while the fake stand-in is still
        # alive (its script sleeps 0.3s after printing the final marker).
        idle_ms: 40,
        trace_base_dir: trace_base_dir
      )

    on_exit(fn ->
      try do
        Mitos.SessionSupervisor.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    assert run.provider == "fake"
    assert run.adapter == nil
    assert run.mode == :engine
    assert run.transport_order == ["stdio-json", "pty"]
    assert run.session_id == session_id

    # The sidecar comes up and the agent's output flows through.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "hello"}}}, 5_000

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "RUN_STARTED",
                       "provider" => "fake",
                       "adapter" => "fake.headless",
                       "transport" => "stdio-json"
                     }}},
                   5_000

    # The engine's vt-screen codec emits semantic prompt events directly.
    assert_receive {:mitos, ^session_id, {:prompt, {:final_answer_candidate, "hello"}}}, 5_000

    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)
    assert [%{"kind" => "decision", "seq" => decision_seq, "type" => "advance"} | _] = events

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_started",
               "caused_by" => ^decision_seq
             } ->
               true

             _ ->
               false
           end)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_finished",
               "caused_by" => ^decision_seq,
               "payload" => %{
                 "status" => "completed",
                 "final_output" => "FAKE_FINAL: hello",
                 "result" => %{"signal" => "unknown", "synthetic" => true}
               }
             } ->
               true

             _ ->
               false
           end)

    assert Mitos.Trace.fold_events(events)["pending_decisions"] == []
  end

  test "refuses launch when the decision budget exceeds remaining budget" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-budget-abort")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)

    assert {:error, {:budget_exceeded, abort}} =
             Mitos.Agent.start(
               provider: "fake",
               task: "should not launch",
               session_id: session_id,
               trace_base_dir: trace_base_dir,
               budget: %{
                 "tokens" => 0,
                 "usd" => "2.00",
                 "wall_ms" => 0,
                 "remaining" => %{"usd" => "1.00"}
               }
             )

    assert abort["type"] == "abort"
    assert abort["params"]["cause"] == "budget"

    assert Mitos.Trace.read_events(session_id, base_dir: trace_base_dir) == [abort]
    assert Registry.lookup(Mitos.SessionRegistry, session_id) == []
  end

  test "unknown provider is reported by the engine, not a BEAM registry" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    Mitos.PubSub.subscribe(session_id)

    assert {:ok, run} =
             Mitos.Agent.start(provider: "does-not-exist", task: "x", session_id: session_id)

    on_exit(fn ->
      try do
        Mitos.SessionSupervisor.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    assert run.provider == "does-not-exist"

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ERROR",
                       "kind" => "launch",
                       "detail" => "ProviderNotRegistered"
                     }}},
                   5_000
  end

  test "generic agent finish effect is emitted from harvested RESULT and workspace diff" do
    session_id = "agent-harvest-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-harvest-trace")
    workspace = unique_tmp_dir("agent-harvest-workspace")
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    on_exit(fn -> File.rm_rf!(workspace) end)

    File.mkdir_p!(workspace)
    git!(workspace, ["init"])
    git!(workspace, ["config", "user.email", "test@example.invalid"])
    git!(workspace, ["config", "user.name", "Mitos Test"])
    File.write!(Path.join(workspace, "tracked.txt"), "base\n")
    git!(workspace, ["add", "tracked.txt"])
    git!(workspace, ["commit", "-m", "base"])

    result_json =
      Jason.encode!(%{
        signal: "ship",
        confidence: 0.8,
        summary: "harvested",
        evidence: []
      })

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "generic",
        task: "",
        session_id: session_id,
        transport_order: ["pty"],
        argv: [
          "/bin/sh",
          "-c",
          "printf 'changed\\n' > tracked.txt; printf '%s' #{sh_quote(result_json)} > RESULT"
        ],
        cwd: workspace,
        sidecar_path: binary,
        idle_ms: 30,
        trace_base_dir: trace_base_dir,
        result: %{path: "RESULT"}
      )

    on_exit(fn -> stop_session(session_id) end)

    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_finished",
               "payload" => %{
                 "status" => "completed",
                 "result" => %{"signal" => "ship", "summary" => "harvested", "path" => "RESULT"},
                 "harvest" => %{
                   "diff" => %{"files_changed" => ["RESULT", "tracked.txt"]},
                   "result" => %{"signal" => "ship"}
                 }
               }
             } ->
               true

             _ ->
               false
           end)

    run_dir = Path.join([trace_base_dir, ".runs", session_id])
    assert File.read!(Path.join(run_dir, "diff.patch")) =~ "+changed"

    assert Jason.decode!(File.read!(Path.join(run_dir, "files_changed.json"))) == [
             "RESULT",
             "tracked.txt"
           ]
  end

  test "pre hooks run before the generic harness starts" do
    session_id = "agent-pre-hook-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-pre-hook-trace")
    workspace = unique_tmp_dir("agent-pre-hook-workspace")
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "generic",
        task: "",
        session_id: session_id,
        transport_order: ["pty"],
        argv: [
          "/bin/sh",
          "-c",
          "test -f pre.txt && printf '{\"signal\":\"ship\",\"summary\":\"pre-ok\"}' > RESULT"
        ],
        cwd: workspace,
        sidecar_path: binary,
        idle_ms: 30,
        trace_base_dir: trace_base_dir,
        result: %{path: "RESULT"},
        hooks: %{
          pre: [
            %{
              name: "prepare",
              run: ["/bin/sh", "-c", "printf prepared > pre.txt"],
              on_fail: "block"
            }
          ]
        }
      )

    on_exit(fn -> stop_session(session_id) end)
    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)
    assert File.read!(Path.join(workspace, "pre.txt")) == "prepared"

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)
    pre_hook_seq = effect_seq!(events, "hook_executed", %{"phase" => "pre", "name" => "prepare"})
    started_seq = effect_seq!(events, "capability_started")
    assert pre_hook_seq < started_seq

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_finished",
               "payload" => %{"result" => %{"signal" => "ship", "summary" => "pre-ok"}}
             } ->
               true

             _ ->
               false
           end)
  end

  test "blocking pre hook stops before harness launch" do
    session_id = "agent-pre-block-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-pre-block-trace")
    workspace = unique_tmp_dir("agent-pre-block-workspace")
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "generic",
        task: "",
        session_id: session_id,
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "printf should-not-run > ran.txt"],
        cwd: workspace,
        sidecar_path: binary,
        idle_ms: 30,
        trace_base_dir: trace_base_dir,
        hooks: %{pre: [%{name: "gate", run: ["/bin/sh", "-c", "exit 7"], on_fail: "block"}]}
      )

    on_exit(fn -> stop_session(session_id) end)
    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)
    refute File.exists?(Path.join(workspace, "ran.txt"))

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)
    refute Enum.any?(events, &(get_in(&1, ["event", "type"]) == "RUN_STARTED"))

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_finished",
               "payload" => %{
                 "status" => "failed",
                 "reason" => "pre_hook_blocked",
                 "verification" => %{"verified" => false, "failed_hooks" => ["gate"]}
               }
             } ->
               true

             _ ->
               false
           end)
  end

  test "non-blocking pre hook failures do not stop harness launch" do
    session_id = "agent-pre-warn-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-pre-warn-trace")
    workspace = unique_tmp_dir("agent-pre-warn-workspace")
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "generic",
        task: "",
        session_id: session_id,
        transport_order: ["pty"],
        argv: [
          "/bin/sh",
          "-c",
          "test -f prepared.txt && printf '{\"signal\":\"ship\",\"summary\":\"warn-ok\"}' > RESULT"
        ],
        cwd: workspace,
        sidecar_path: binary,
        idle_ms: 30,
        trace_base_dir: trace_base_dir,
        result: %{path: "RESULT"},
        hooks: %{
          pre: [
            %{name: "warn-gate", run: ["/bin/sh", "-c", "exit 5"], on_fail: "warn"},
            %{name: "skip-gate", run: ["/bin/sh", "-c", "exit 6"], on_fail: "skip"},
            %{
              name: "prepare",
              run: ["/bin/sh", "-c", "printf prepared > prepared.txt"],
              on_fail: "block"
            }
          ]
        }
      )

    on_exit(fn -> stop_session(session_id) end)
    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)
    refute Enum.any?(events, &(get_in(&1, ["event", "type"]) == "HOOK_BLOCKED"))
    assert File.read!(Path.join(workspace, "prepared.txt")) == "prepared"

    assert_hook_blocked?(events, "pre", "warn-gate", false)
    assert_hook_blocked?(events, "pre", "skip-gate", false)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_finished",
               "payload" => %{
                 "status" => "completed",
                 "result" => %{"signal" => "ship", "summary" => "warn-ok"}
               }
             } ->
               true

             _ ->
               false
           end)
  end

  test "blocking post hook marks harvest unverified but preserves result" do
    session_id = "agent-post-block-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-post-block-trace")
    workspace = unique_tmp_dir("agent-post-block-workspace")
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "generic",
        task: "",
        session_id: session_id,
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "printf '{\"signal\":\"ship\",\"summary\":\"done\"}' > RESULT"],
        cwd: workspace,
        sidecar_path: binary,
        idle_ms: 30,
        trace_base_dir: trace_base_dir,
        result: %{path: "RESULT"},
        hooks: %{post: [%{name: "gate", run: ["/bin/sh", "-c", "exit 9"], on_fail: "block"}]}
      )

    on_exit(fn -> stop_session(session_id) end)
    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "verification_recorded",
               "payload" => %{
                 "tier" => "deterministic_hook",
                 "verdict" => "unverified",
                 "failed_hooks" => ["gate"]
               }
             } ->
               true

             _ ->
               false
           end)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_finished",
               "payload" => %{
                 "status" => "completed",
                 "result" => %{"signal" => "ship", "summary" => "done"},
                 "verification" => %{"verified" => false, "failed_hooks" => ["gate"]}
               }
             } ->
               true

             _ ->
               false
           end)
  end

  test "non-blocking post hook failures preserve verified harvest" do
    session_id = "agent-post-warn-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("agent-post-warn-trace")
    workspace = unique_tmp_dir("agent-post-warn-workspace")
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "generic",
        task: "",
        session_id: session_id,
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "printf '{\"signal\":\"ship\",\"summary\":\"done\"}' > RESULT"],
        cwd: workspace,
        sidecar_path: binary,
        idle_ms: 30,
        trace_base_dir: trace_base_dir,
        result: %{path: "RESULT"},
        hooks: %{post: [%{name: "warn-gate", run: ["/bin/sh", "-c", "exit 9"], on_fail: "warn"}]}
      )

    on_exit(fn -> stop_session(session_id) end)
    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)
    refute Enum.any?(events, &(Map.get(&1, "type") == "verification_recorded"))
    assert_hook_blocked?(events, "post", "warn-gate", false)

    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "capability_finished",
               "payload" => %{
                 "status" => "completed",
                 "result" => %{"signal" => "ship", "summary" => "done"},
                 "verification" => %{"verified" => true, "failed_hooks" => ["warn-gate"]}
               }
             } ->
               true

             _ ->
               false
           end)
  end

  test "approval prompts bubble up as action requests" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    Mitos.PubSub.subscribe(session_id)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "FAKE_APPROVAL: Run tests? [y/N]",
        session_id: session_id,
        transport_order: ["stdio-json"]
      )

    on_exit(fn ->
      try do
        Mitos.SessionSupervisor.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ACTION_REQUESTED",
                       "action" => "terminal_input",
                       "origin" => "terminal_scrape",
                       "action_id" => action_id,
                       "request_id" => request_id,
                       "producer_request_id" => producer_request_id,
                       "context" => context
                     }}},
                   5_000

    assert action_id == request_id
    assert action_id =~ "request-"
    assert producer_request_id =~ "terminal-scrape-"
    assert context =~ "FAKE_APPROVAL"
  end

  test "terminal scrape verdicts route back to the sidecar with canonical ids" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "Run tests? [y/N]",
        session_id: session_id,
        transport_order: ["pty"],
        idle_ms: 50
      )

    on_exit(fn -> stop_session(session_id) end)

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ACTION_REQUESTED",
                       "origin" => "terminal_scrape",
                       "request_id" => request_id
                     }}},
                   5_000

    Mitos.Session.authorize(run.pid, request_id, "y\n")

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "STRUCTURED_OUTPUT",
                       "kind" => "authorization",
                       "origin" => "terminal_scrape",
                       "request_id" => ^request_id,
                       "action_id" => ^request_id,
                       "decision" => "allow",
                       "token" => "y\n"
                     }}},
                   5_000
  end

  test "fast-path approvals are persisted as requested and decided" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("fast-path-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "FAKE_APPROVAL: Run tests? [y/N]",
        session_id: session_id,
        transport_order: ["stdio-json"],
        trace_base_dir: trace_base_dir,
        policy: %{tier2_fast_path_rules: ["Run tests?"]}
      )

    on_exit(fn -> stop_session(session_id) end)

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ACTION_REQUESTED",
                       "request_id" => request_id,
                       "fast_path" => true,
                       "auto_decision" => "authorize"
                     }}},
                   5_000

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "AUTHORITY_VERDICT",
                       "request_id" => ^request_id,
                       "decision" => "authorize",
                       "payload" => %{"fast_path" => true}
                     }}},
                   5_000

    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)

    approvals = File.read!(Path.join([trace_base_dir, ".runs", session_id, "approvals.jsonl"]))
    assert approvals =~ ~s("type":"approval_requested")
    assert approvals =~ ~s("type":"approval_decision")
    assert approvals =~ request_id
    assert approvals =~ ~s("decision":"authorize")
  end

  defp stop_session(session_id) do
    try do
      Mitos.SessionSupervisor.stop_session(session_id)
    catch
      :exit, _ -> :ok
    end
  end

  defp assert_eventually(deadline_ms, fun) do
    deadline = System.monotonic_time(:millisecond) + deadline_ms

    Stream.unfold(:start, fn _ ->
      if fun.() do
        nil
      else
        if System.monotonic_time(:millisecond) > deadline do
          nil
        else
          Process.sleep(10)
          {:tick, :tick}
        end
      end
    end)
    |> Enum.to_list()

    assert fun.(), "condition did not become true within #{deadline_ms}ms"
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end

  defp git!(cwd, args) do
    case System.cmd("git", ["-C", cwd | args], stderr_to_stdout: true) do
      {_output, 0} -> :ok
      {output, status} -> flunk("git #{Enum.join(args, " ")} failed with #{status}: #{output}")
    end
  end

  defp sh_quote(value) do
    "'" <> String.replace(value, "'", "'\"'\"'") <> "'"
  end

  defp effect_seq!(events, type, ref_match \\ %{}) do
    case Enum.find(events, fn
           %{"kind" => "effect", "type" => ^type, "ref" => ref} ->
             Enum.all?(ref_match, fn {key, value} -> ref[key] == value end)

           %{"kind" => "effect", "type" => ^type} ->
             map_size(ref_match) == 0

           _ ->
             false
         end) do
      %{"seq" => seq} -> seq
      nil -> flunk("missing effect #{type} #{inspect(ref_match)}")
    end
  end

  defp assert_hook_blocked?(events, phase, name, blocked?) do
    assert Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "hook_executed",
               "ref" => %{"phase" => ^phase, "name" => ^name},
               "payload" => %{"blocked?" => ^blocked?}
             } ->
               true

             _ ->
               false
           end)
  end
end
