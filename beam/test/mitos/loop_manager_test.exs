defmodule Mitos.LoopManagerTest do
  use ExUnit.Case, async: true

  test "compacts context before climbing on repeated no-progress iterations" do
    base_dir = unique_tmp_dir("loop-compact")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-loop-compact")
    state = Mitos.LoopManager.new(opening_decision_seq: 0, target: "stage:test")
    observation = %{diff?: false, artifact?: false, result_state: nil}

    {:continue, state, trace} = Mitos.LoopManager.advance(trace, state, observation)
    {:continue, state, trace} = Mitos.LoopManager.advance(trace, state, observation)
    {:compact_context, state, _trace} = Mitos.LoopManager.advance(trace, state, observation)

    assert state.compacted

    events = Mitos.Trace.read_events("run-loop-compact", base_dir: base_dir)
    effects = Enum.filter(events, &(Map.get(&1, "kind") == "effect"))

    assert Enum.count(effects, &(Map.get(&1, "type") == "iteration_finished")) == 3

    assert %{
             "type" => "context_compacted",
             "caused_by" => 0,
             "payload" => %{"signal" => "no_progress"}
           } = List.last(effects)

    refute Enum.any?(events, &(Map.get(&1, "actor") == "loop_manager"))
  end

  test "repeated deterministic error emits loop-manager switch decision and effect" do
    base_dir = unique_tmp_dir("loop-switch")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-loop-switch")
    state = Mitos.LoopManager.new(opening_decision_seq: 0, target: "stage:test")
    observation = %{error_class: "exec:1"}

    {:continue, state, trace} = Mitos.LoopManager.advance(trace, state, observation)
    {:switch, state, _trace} = Mitos.LoopManager.advance(trace, state, observation)

    assert state.climb_history == ["effort"]

    events = Mitos.Trace.read_events("run-loop-switch", base_dir: base_dir)
    switch = Enum.find(events, &(Map.get(&1, "kind") == "decision" and &1["type"] == "switch"))

    assert %{
             "actor" => "loop_manager",
             "target" => "stage:test",
             "params" => %{
               "signal" => "repeated_error",
               "effort" => "higher",
               "climb_history" => ["effort"]
             }
           } = switch

    assert %{
             "kind" => "effect",
             "type" => "harness_switched",
             "caused_by" => switch_seq,
             "payload" => %{"signal" => "repeated_error", "climb_history" => ["effort"]}
           } =
             Enum.find(
               events,
               &(Map.get(&1, "kind") == "effect" and &1["type"] == "harness_switched")
             )

    assert switch_seq == switch["seq"]
  end

  test "over-budget loop-manager switch emits budget abort without switch effect" do
    base_dir = unique_tmp_dir("loop-budget-abort")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-loop-budget-abort")

    state =
      Mitos.LoopManager.new(
        opening_decision_seq: 0,
        target: "stage:test",
        budget: %{
          "tokens" => 100,
          "usd" => "2.00",
          "wall_ms" => 1000,
          "remaining" => %{"tokens" => 100, "usd" => "1.00", "wall_ms" => 1000}
        }
      )

    observation = %{error_class: "exec:1"}

    {:continue, state, trace} = Mitos.LoopManager.advance(trace, state, observation)
    {:abort, _state, _trace} = Mitos.LoopManager.advance(trace, state, observation)

    events = Mitos.Trace.read_events("run-loop-budget-abort", base_dir: base_dir)

    assert %{
             "kind" => "decision",
             "type" => "abort",
             "params" => %{
               "cause" => "budget",
               "refused_decision" => %{"type" => "switch"}
             }
           } = Enum.find(events, &(Map.get(&1, "kind") == "decision" and &1["type"] == "abort"))

    refute Enum.any?(
             events,
             &(Map.get(&1, "kind") == "effect" and &1["type"] == "harness_switched")
           )
  end

  test "exhausted local ladder emits escalate decision and effect" do
    base_dir = unique_tmp_dir("loop-escalate")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-loop-escalate")

    state =
      Mitos.LoopManager.new(
        opening_decision_seq: 0,
        target: "stage:test",
        climb_history: ["effort", "model", "harness"]
      )

    observation = %{error_class: "exec:1"}

    {:continue, state, trace} = Mitos.LoopManager.advance(trace, state, observation)
    {:escalate, _state, _trace} = Mitos.LoopManager.advance(trace, state, observation)

    events = Mitos.Trace.read_events("run-loop-escalate", base_dir: base_dir)

    escalate =
      Enum.find(events, &(Map.get(&1, "kind") == "decision" and &1["type"] == "escalate"))

    assert %{
             "actor" => "loop_manager",
             "params" => %{
               "signal" => "repeated_error",
               "climb_history" => ["effort", "model", "harness"]
             }
           } = escalate

    assert %{
             "type" => "escalation_raised",
             "caused_by" => escalate_seq,
             "payload" => %{"signal" => "repeated_error"}
           } =
             Enum.find(
               events,
               &(Map.get(&1, "kind") == "effect" and &1["type"] == "escalation_raised")
             )

    assert escalate_seq == escalate["seq"]
  end

  defp opened_trace(base_dir, run_id) do
    trace =
      Mitos.Trace.open!(
        run_id: run_id,
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "loop"}
      )

    Mitos.Trace.append_decision(trace, %{
      actor: "orchestrator",
      type: "advance",
      target: "stage:test",
      params: %{},
      inputs_hash: "sha256:state",
      rationale: "open loop-managed capability",
      budget: %{"tokens" => 100, "usd" => 1, "wall_ms" => 1000, "remaining" => %{}}
    })
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
