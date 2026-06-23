defmodule Mitos.TraceTest do
  use ExUnit.Case, async: true

  test "writes manifest and JSONL events with binary-safe normalization" do
    base_dir = unique_tmp_dir("trace")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace =
      Mitos.Trace.open!(
        run_id: "run-1",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "hello"}
      )

    trace =
      Mitos.Trace.append_event(trace, %{
        "type" => "STRUCTURED_OUTPUT",
        "bytes" => Msgpax.Bin.new(<<255, 0>>)
      })

    run_dir = Path.join([base_dir, ".runs", "run-1"])
    assert File.read!(Path.join(run_dir, "run.yaml")) =~ "provider: fake"

    [line] =
      run_dir
      |> Path.join("events.jsonl")
      |> File.read!()
      |> String.split("\n", trim: true)

    assert %{
             "run_id" => "run-1",
             "sequence" => 0,
             "event" => %{
               "type" => "STRUCTURED_OUTPUT",
               "bytes" => %{"encoding" => "base64", "data" => "/wA="}
             }
           } = Jason.decode!(line)

    assert trace.seq == 1
  end

  test "appends canonical Decision and Effect events" do
    base_dir = unique_tmp_dir("decision-effect")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-de", base_dir: base_dir)

    trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "stage:test",
        params: %{"capability" => "cap:test"},
        inputs_hash: "sha256:state",
        rationale: "run the test capability",
        budget: %{"tokens" => 1_000, "usd" => 0, "wall_ms" => 10_000, "remaining" => %{}}
      })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_started",
        caused_by: 0,
        ref: %{"sandbox" => "local"},
        payload: %{"harness" => "fake"}
      })

    assert trace.seq == 2

    [decision, effect] = Mitos.Trace.read_events("run-de", base_dir: base_dir)

    assert %{
             "kind" => "decision",
             "run_id" => "run-de",
             "seq" => 0,
             "actor" => "orchestrator",
             "type" => "advance",
             "target" => "stage:test",
             "params" => %{"capability" => "cap:test"},
             "inputs_hash" => "sha256:state",
             "rationale" => "run the test capability",
             "budget" => %{"tokens" => 1000}
           } = decision

    assert DateTime.from_iso8601(decision["ts"]) |> elem(0) == :ok

    assert %{
             "kind" => "effect",
             "run_id" => "run-de",
             "seq" => 1,
             "type" => "capability_started",
             "caused_by" => 0,
             "ref" => %{"sandbox" => "local"},
             "payload" => %{"harness" => "fake"}
           } = effect
  end

  test "refuses decisions whose grant exceeds remaining budget" do
    base_dir = unique_tmp_dir("budget-abort")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-budget-abort", base_dir: base_dir)

    assert {:abort, trace, abort} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:expensive",
               params: %{"capability" => "cap:expensive"},
               inputs_hash: "sha256:state",
               rationale: "try an expensive step",
               budget: %{
                 "tokens" => 100,
                 "usd" => "2.50",
                 "wall_ms" => 20_000,
                 "remaining" => %{"tokens" => 100, "usd" => "1.00", "wall_ms" => 20_000}
               }
             })

    assert trace.seq == 1
    assert abort["type"] == "abort"
    assert abort["actor"] == "orchestrator"
    assert abort["target"] == nil
    assert abort["params"]["cause"] == "budget"
    assert abort["params"]["refused_decision"]["type"] == "advance"

    assert [%{"dimension" => "usd", "requested" => 2.5, "remaining" => 1.0}] =
             abort["params"]["violations"]

    assert [logged] = Mitos.Trace.read_events("run-budget-abort", base_dir: base_dir)
    assert logged == abort
    assert Mitos.Trace.fold_events([logged])["terminal?"]
  end

  test "rejects budget charges that exceed the causing decision budget" do
    base_dir = unique_tmp_dir("budget-charge-limit")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-budget-charge-limit", base_dir: base_dir)

    assert {:ok, trace, %{"seq" => decision_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:metered",
               params: %{},
               inputs_hash: "sha256:state",
               rationale: "grant a one dollar budget slice",
               budget: %{
                 "tokens" => 100,
                 "usd" => "1.00",
                 "wall_ms" => 10_000,
                 "remaining" => %{"tokens" => 100, "usd" => "1.00", "wall_ms" => 10_000}
               }
             })

    assert_raise ArgumentError, ~r/budget charge would exceed decision budget/, fn ->
      Mitos.Trace.append_effect(trace, %{
        type: "budget_charged",
        caused_by: decision_seq,
        ref: %{"provider" => "fake", "source_sequence" => 1},
        payload: %{"tokens" => 10, "usd" => "1.10", "wall_ms" => 500}
      })
    end

    events = Mitos.Trace.read_events("run-budget-charge-limit", base_dir: base_dir)
    assert Enum.count(events, &(Map.get(&1, "type") == "budget_charged")) == 0
  end

  test "fold exposes charge totals and current remaining budget" do
    base_dir = unique_tmp_dir("budget-fold")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-budget-fold", base_dir: base_dir)

    assert {:ok, trace, %{"seq" => decision_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:metered",
               params: %{},
               inputs_hash: "sha256:state",
               rationale: "grant a metered budget slice",
               budget: %{
                 "tokens" => 100,
                 "usd" => "1.00",
                 "wall_ms" => 10_000,
                 "remaining" => %{"tokens" => 100, "usd" => "1.00", "wall_ms" => 10_000}
               }
             })

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "budget_charged",
        caused_by: decision_seq,
        ref: %{"provider" => "fake"},
        payload: %{"tokens" => 10, "usd" => "0.60", "wall_ms" => 500}
      })

    state = Mitos.Trace.fold_run("run-budget-fold", base_dir: base_dir)

    assert state["budget"]["charged"] == %{"tokens" => 10, "usd" => 0.6, "wall_ms" => 500}
    assert state["budget"]["current_spent"] == state["budget"]["charged"]
    assert state["budget"]["remaining"] == %{"tokens" => 90, "usd" => 0.4, "wall_ms" => 9500}
    assert state["budget"]["charged_by_decision"][decision_seq] == state["budget"]["charged"]
  end

  test "folded state excludes pending tail decisions from state and budget" do
    base_dir = unique_tmp_dir("pending-tail-fold")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-pending-tail-fold", base_dir: base_dir)

    assert {:ok, trace, %{"seq" => decision_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:committed",
               params: %{},
               inputs_hash: "sha256:committed",
               rationale: "committed launch",
               budget: %{"tokens" => 10, "usd" => "1.00", "wall_ms" => 1_000, "remaining" => %{}}
             })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_started",
        caused_by: decision_seq,
        ref: %{"sandbox" => "sandbox-1"},
        payload: %{}
      })

    assert {:ok, _trace, %{"seq" => pending_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "loop_manager",
               type: "switch",
               target: "stage:committed",
               params: %{"model" => "stronger"},
               inputs_hash: "sha256:pending",
               rationale: "pending switch",
               budget: %{
                 "tokens" => 999,
                 "usd" => "999.00",
                 "wall_ms" => 999_000,
                 "remaining" => %{}
               }
             })

    state = Mitos.Trace.fold_run("run-pending-tail-fold", base_dir: base_dir)

    assert Enum.map(state["decisions"], & &1["seq"]) == [decision_seq]
    assert Enum.map(state["all_decisions"], & &1["seq"]) == [decision_seq, pending_seq]
    assert [%{"seq" => ^pending_seq, "type" => "switch"}] = state["pending_decisions"]
    refute state["terminal?"]
    assert state["budget"]["current"]["usd"] == "1.00"
  end

  test "idempotent re-driven effects are not appended twice" do
    base_dir = unique_tmp_dir("effect-idempotency")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-effect-idempotency", base_dir: base_dir)

    assert {:ok, trace, %{"seq" => decision_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:idempotent",
               params: %{},
               inputs_hash: "sha256:state",
               rationale: "exercise idempotent effect append",
               budget: %{
                 "tokens" => 100,
                 "usd" => "2.00",
                 "wall_ms" => 10_000,
                 "remaining" => %{}
               }
             })

    effects = [
      %{
        type: "capability_started",
        caused_by: decision_seq,
        ref: %{"sandbox" => "sandbox-1"},
        payload: %{}
      },
      %{
        type: "egress_attempted",
        caused_by: decision_seq,
        ref: %{"dest" => "api.example.test", "nonce" => "egress-1"},
        payload: %{}
      },
      %{
        type: "egress_allowed",
        caused_by: decision_seq,
        ref: %{"dest" => "api.example.test", "nonce" => "egress-1"},
        payload: %{}
      },
      %{
        type: "egress_completed",
        caused_by: decision_seq,
        ref: %{"dest" => "api.example.test", "nonce" => "egress-1"},
        payload: %{}
      },
      %{
        type: "artifact_registered",
        caused_by: decision_seq,
        ref: %{"path" => "RESULT", "sha256" => "sha256:result"},
        payload: %{}
      },
      %{
        type: "commit",
        caused_by: decision_seq,
        ref: %{"sha" => "abc123"},
        payload: %{}
      },
      %{
        type: "budget_charged",
        caused_by: decision_seq,
        ref: %{"source_sequence" => 7, "provider" => "fake"},
        payload: %{"tokens" => 5, "usd" => "0.10", "wall_ms" => 100}
      }
    ]

    trace =
      Enum.reduce(effects ++ effects, trace, fn effect, trace ->
        Mitos.Trace.append_effect(trace, effect)
      end)

    assert {:ok, trace, %{"seq" => second_decision_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:idempotent-2",
               params: %{},
               inputs_hash: "sha256:state-2",
               rationale: "exercise a separate budget charge decision",
               budget: %{
                 "tokens" => 100,
                 "usd" => "2.00",
                 "wall_ms" => 10_000,
                 "remaining" => %{}
               }
             })

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "budget_charged",
        caused_by: second_decision_seq,
        ref: %{"source_sequence" => 8, "provider" => "fake"},
        payload: %{"tokens" => 5, "usd" => "0.10", "wall_ms" => 100}
      })

    assert trace.seq == 10
    events = Mitos.Trace.read_events("run-effect-idempotency", base_dir: base_dir)

    assert Enum.count(events, &(Map.get(&1, "type") == "capability_started")) == 1
    assert Enum.count(events, &(Map.get(&1, "type") == "egress_allowed")) == 1
    assert Enum.count(events, &(Map.get(&1, "type") == "egress_attempted")) == 1
    assert Enum.count(events, &(Map.get(&1, "type") == "egress_completed")) == 1
    assert Enum.count(events, &(Map.get(&1, "type") == "artifact_registered")) == 1
    assert Enum.count(events, &(Map.get(&1, "type") == "commit")) == 1
    assert Enum.count(events, &(Map.get(&1, "type") == "budget_charged")) == 2
  end

  test "open preserves existing log records and resumes sequence" do
    base_dir = unique_tmp_dir("append-only-open")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace =
      Mitos.Trace.open!(
        run_id: "run-append",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "first"}
      )

    _trace = Mitos.Trace.append_event(trace, %{"type" => "hello"})

    reopened =
      Mitos.Trace.open!(
        run_id: "run-append",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "second"}
      )

    assert reopened.seq == 1
    reopened = Mitos.Trace.append_event(reopened, %{"type" => "next"})

    assert reopened.seq == 2
    assert [first, second] = Mitos.Trace.read_events("run-append", base_dir: base_dir)
    assert first["sequence"] == 0
    assert first["event"]["type"] == "hello"
    assert second["sequence"] == 1
    assert second["event"]["type"] == "next"

    manifest = File.read!(Path.join([base_dir, ".runs", "run-append", "run.yaml"]))
    assert manifest =~ "task: first"
    refute manifest =~ "task: second"
  end

  test "stamps manifest and event records with trace tenant context" do
    base_dir = unique_tmp_dir("tenant-context")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace =
      Mitos.Trace.open!(
        run_id: "run-tenant",
        base_dir: base_dir,
        org_id: "org-a",
        project_id: "proj-a",
        actor_id: "user-a",
        launch_spec: %{"provider" => "fake", "task" => "hello"}
      )

    trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "stage:test",
        params: %{},
        inputs_hash: "sha256:state",
        rationale: "launch tenant-scoped work",
        budget: %{"tokens" => 10, "usd" => 0, "wall_ms" => 1000, "remaining" => %{}}
      })

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_started",
        caused_by: 0,
        ref: %{},
        payload: %{}
      })

    run_dir = Path.join([base_dir, ".runs", "run-tenant"])
    manifest = File.read!(Path.join(run_dir, "run.yaml"))

    assert manifest =~ "org_id: org-a"
    assert manifest =~ "project_id: proj-a"
    assert manifest =~ "actor_id: user-a"

    [decision, effect] = Mitos.Trace.read_events("run-tenant", base_dir: base_dir)

    assert decision["tenant"] == %{
             "org_id" => "org-a",
             "project_id" => "proj-a",
             "actor_id" => "user-a"
           }

    assert effect["tenant"] == decision["tenant"]

    reopened = Mitos.Trace.open!(run_id: "run-tenant", base_dir: base_dir)
    assert reopened.org_id == "org-a"
    assert reopened.project_id == "proj-a"
    assert reopened.actor_id == "user-a"
  end

  test "fold marks decisions without committed effects as pending for re-drive" do
    base_dir = unique_tmp_dir("crash-fold")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-crash", base_dir: base_dir)

    trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "stage:test",
        params: %{},
        inputs_hash: "sha256:before",
        rationale: "open the stage before executing it",
        budget: %{"tokens" => 10, "usd" => 0, "wall_ms" => 1000, "remaining" => %{}}
      })

    state = Mitos.Trace.fold_run("run-crash", base_dir: base_dir)

    assert state["last_committed_effect_seq"] == -1
    assert [%{"seq" => 0, "type" => "advance"}] = state["pending_decisions"]
    refute state["terminal?"]

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_started",
        caused_by: 0,
        ref: %{"handle" => "sandbox-1"},
        payload: %{}
      })

    state = Mitos.Trace.fold_run("run-crash", base_dir: base_dir)

    assert state["last_committed_effect_seq"] == 1
    assert state["pending_decisions"] == []
    assert %{0 => [%{"type" => "capability_started"}]} = state["effects_by_decision"]
  end

  test "fold marks egress attempts without completion as approval-gated replay" do
    base_dir = unique_tmp_dir("egress-attempt-fold")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-egress-attempt-fold", base_dir: base_dir)

    assert {:ok, trace, %{"seq" => decision_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:broker",
               params: %{},
               inputs_hash: "sha256:before-egress",
               rationale: "open a brokered egress step",
               budget: %{"tokens" => 10, "usd" => 0, "wall_ms" => 1000, "remaining" => %{}}
             })

    ref = %{"dest" => "api.example.test", "nonce" => "egress-1"}

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "egress_attempted",
        caused_by: decision_seq,
        ref: ref,
        payload: %{"method" => "POST"}
      })

    state = Mitos.Trace.fold_run("run-egress-attempt-fold", base_dir: base_dir)

    assert state["pending_decisions"] == []
    assert state["egress_replay_requires_approval?"]
    assert [%{"type" => "egress_attempted", "ref" => ^ref}] = state["incomplete_egress_attempts"]

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "egress_completed",
        caused_by: decision_seq,
        ref: ref,
        payload: %{"status" => 200}
      })

    state = Mitos.Trace.fold_run("run-egress-attempt-fold", base_dir: base_dir)

    refute state["egress_replay_requires_approval?"]
    assert state["incomplete_egress_attempts"] == []
  end

  test "recovery re-drives pending decisions without re-deciding" do
    base_dir = unique_tmp_dir("crash-recover")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-redrive", base_dir: base_dir)

    _trace =
      Mitos.Trace.append_decision(trace, %{
        actor: "orchestrator",
        type: "advance",
        target: "stage:test",
        params: %{"capability" => "cap:test"},
        inputs_hash: "sha256:before",
        rationale: "open the stage before executing it",
        budget: %{"tokens" => 10, "usd" => 0, "wall_ms" => 1000, "remaining" => %{}}
      })

    assert {:ok, recovery} =
             Mitos.Trace.recover_run("run-redrive", [base_dir: base_dir], fn decision ->
               send(self(), {:redrive, decision})

               {:ok,
                %{
                  type: "capability_started",
                  ref: %{"sandbox" => "sandbox-1"},
                  payload: %{"harness" => "fake"}
                }}
             end)

    assert_receive {:redrive, %{"seq" => 0, "type" => "advance"}}
    assert [%{type: "capability_started"}] = recovery.redriven_effects

    [decision, effect] = Mitos.Trace.read_events("run-redrive", base_dir: base_dir)

    assert decision["kind"] == "decision"

    assert %{
             "kind" => "effect",
             "type" => "capability_started",
             "caused_by" => 0,
             "ref" => %{"sandbox" => "sandbox-1"}
           } = effect

    assert recovery.state["pending_decisions"] == []

    assert {:ok, second_recovery} =
             Mitos.Trace.recover_run("run-redrive", [base_dir: base_dir], fn _decision ->
               flunk("committed decisions should not be re-driven")
             end)

    assert second_recovery.pending_decisions == []
    assert second_recovery.redriven_effects == []
  end

  test "recovery refuses incomplete egress replay until explicitly approved" do
    base_dir = unique_tmp_dir("egress-replay-approval")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = Mitos.Trace.open!(run_id: "run-egress-replay-approval", base_dir: base_dir)

    assert {:ok, trace, %{"seq" => decision_seq}} =
             Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "advance",
               target: "stage:broker",
               params: %{},
               inputs_hash: "sha256:before-egress",
               rationale: "open a brokered egress step",
               budget: %{"tokens" => 10, "usd" => 0, "wall_ms" => 1000, "remaining" => %{}}
             })

    ref = %{"dest" => "api.example.test", "nonce" => "egress-1"}

    _trace =
      Mitos.Trace.append_effect(trace, %{
        type: "egress_attempted",
        caused_by: decision_seq,
        ref: ref,
        payload: %{"method" => "POST"}
      })

    assert {:error, {:egress_replay_requires_approval, [attempt]}} =
             Mitos.Trace.recover_run(
               "run-egress-replay-approval",
               [base_dir: base_dir],
               fn _decision -> flunk("incomplete egress must block automatic replay") end
             )

    assert attempt["caused_by"] == decision_seq
    assert attempt["ref"] == ref

    assert {:ok, recovery} =
             Mitos.Trace.recover_run(
               "run-egress-replay-approval",
               [base_dir: base_dir, allow_incomplete_egress_replay: true],
               fn decision ->
                 send(self(), {:redrive_after_approval, decision})

                 {:ok,
                  [
                    %{type: "egress_attempted", ref: ref, payload: %{"method" => "POST"}},
                    %{type: "egress_completed", ref: ref, payload: %{"status" => 200}}
                  ]}
               end
             )

    assert_receive {:redrive_after_approval, %{"seq" => ^decision_seq}}
    assert [%{type: "egress_attempted"}, %{type: "egress_completed"}] = recovery.redriven_effects
    refute recovery.state["egress_replay_requires_approval?"]

    events = Mitos.Trace.read_events("run-egress-replay-approval", base_dir: base_dir)

    assert Enum.count(events, &(Map.get(&1, "type") == "egress_attempted")) == 1
    assert Enum.count(events, &(Map.get(&1, "type") == "egress_completed")) == 1
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
