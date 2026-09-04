defmodule Mitos.EvalTest do
  @moduledoc """
  Replay-as-eval (`16 §6.1`) and the interrupt distribution (`02 §6.1`).

  The point of both: no token is spent to answer either question. They are
  deterministic recomputations over a log that already exists, which is what
  makes "measure before you build" a week of work rather than a quarter.
  """
  use ExUnit.Case, async: false

  alias Mitos.Eval
  alias Mitos.Event.Effect

  test "flags a rule change that would now ask a human LESS" do
    run_id = run_with_cards()

    # Recorded as `internal` with a workspace-relative path. Today's policy
    # classifies a path outside a declared workspace as `recoverable` — a
    # tightening, which is safe. The direction is what the gate cares about.
    result = Eval.replay(run_id)
    assert result.cards == 2

    directions = Enum.map(result.divergences, & &1.direction)
    refute :loosened in directions
  end

  test "a corpus replay is a CI gate, not a report" do
    run_id = run_with_cards()
    result = Eval.replay_all([run_id], strict: true)

    assert result.runs == 1
    assert result.ok
    assert Eval.report(result) =~ "decision points"
  end

  test "buckets interrupts into what they say to build next" do
    run_id = run_with_cards()
    d = Eval.interrupt_distribution([run_id])

    assert d.total == 2
    # One carried a default (silence would have advanced it), one did not.
    assert d.buckets[:approve_the_boring] == 1
    assert d.buckets[:genuine_judgment] == 1
    assert d.implication in [:build_a_gate, :mixed]
  end

  test "says nothing rather than guessing when there is no data" do
    assert %{total: 0, implication: :no_data} = Eval.interrupt_distribution([])
  end

  defp run_with_cards do
    run_id = "test/" <> Mitos.id("run")
    {:ok, _} = Mitos.Run.start_run(run_id: run_id, pipeline: "# Pipeline: p\n", capabilities: %{})
    Mitos.Run.await(run_id)

    # A reversible card: a default, on a clock.
    append(run_id, %{
      type: :approval_requested,
      ref: %{"decision_id" => "d1"},
      payload: %{
        "decision_id" => "d1",
        "kind" => "approve",
        "reversibility" => "internal",
        "question" => "Write src/main.ex?",
        "default" => "approve",
        "ttl_ms" => 60_000,
        "blast_radius" => %{"paths" => ["src/main.ex"], "destinations" => [], "spend" => nil},
        "context" => %{"attempted" => "edit", "blocked_by" => nil, "evidence_refs" => []}
      }
    })

    # An irreversible one: no default, parks.
    append(run_id, %{
      type: :approval_requested,
      ref: %{"decision_id" => "d2"},
      payload: %{
        "decision_id" => "d2",
        "kind" => "approve",
        "reversibility" => "external",
        "question" => "Publish the package?",
        "default" => nil,
        "ttl_ms" => nil,
        "blast_radius" => %{"paths" => [], "destinations" => ["registry.npmjs.org"], "spend" => nil},
        "context" => %{"attempted" => "publish", "blocked_by" => nil, "evidence_refs" => []}
      }
    })

    run_id
  end

  defp append(run_id, fields) do
    if Mitos.Log.writer_held?(run_id) do
      Mitos.Run.append_effect(run_id, fields)
    else
      Mitos.Log.append_detached(run_id, Effect.new(fields))
    end
  end
end
