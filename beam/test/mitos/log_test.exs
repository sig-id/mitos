defmodule Mitos.LogTest do
  use ExUnit.Case, async: true

  alias Mitos.Log
  alias Mitos.Event.{Decision, Effect}
  alias Mitos.Run.State

  defp run_id, do: "test/log_" <> Mitos.id()

  test "append assigns monotonic seq and reads back in order" do
    rid = run_id()
    {:ok, w, []} = Log.open_writer(rid)

    {d0, w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :advance, target: "s1"))
    {e0, w} = Log.append(w, Effect.new(run_id: rid, type: :capability_started, caused_by: d0.seq))
    {d1, _w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :finish))

    assert d0.seq == 0
    assert e0.seq == 1
    assert d1.seq == 2

    events = Log.read(rid)
    assert Enum.map(events, & &1.seq) == [0, 1, 2]
    assert [%Decision{}, %Effect{}, %Decision{}] = events
  end

  test "events survive a JSONL round-trip with fields intact" do
    rid = run_id()
    {:ok, w, []} = Log.open_writer(rid)

    {_d, _w} =
      Log.append(
        w,
        Decision.new(
          run_id: rid,
          actor: "loop_manager",
          type: :switch,
          target: "s1",
          params: %{"model" => "opus", "effort" => "high"},
          rationale: "escalating effort",
          budget: %{"usd" => 8, "remaining" => %{"usd" => 5}}
        )
      )

    [d] = Log.read(rid)
    assert d.actor == "loop_manager"
    assert d.type == :switch
    assert d.params["model"] == "opus"
    assert d.rationale == "escalating effort"
    assert d.budget["remaining"]["usd"] == 5
  end

  test "fold reconstructs run state = fold(events) (01 §4)" do
    rid = run_id()
    {:ok, w, []} = Log.open_writer(rid)

    {_, w} = Log.append(w, Effect.new(run_id: rid, type: :run_started))
    {d1, w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :advance, target: "planner"))
    {_, w} = Log.append(w, Effect.new(run_id: rid, type: :capability_started, caused_by: d1.seq, ref: %{"stage_id" => "planner"}))

    {_, w} =
      Log.append(
        w,
        Effect.new(
          run_id: rid,
          type: :capability_finished,
          caused_by: d1.seq,
          ref: %{"stage_id" => "planner"},
          payload: %{"result" => %{"signal" => "done"}, "evidence" => []}
        )
      )

    {_, w} = Log.append(w, Effect.new(run_id: rid, type: :budget_charged, caused_by: d1.seq, payload: %{"usd" => 1.5, "tokens" => 1000}))
    {_, _w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :finish, params: %{"result_ref" => "art://r"}))

    st = State.fold(rid)
    assert st.status == :finished
    assert st.result_ref == "art://r"
    assert st.stages["planner"].status == :finished
    assert st.stages["planner"].result["signal"] == "done"
    assert st.spent.usd == 1.5
    assert st.spent.tokens == 1000
    assert State.terminal?(st)
  end
end
