defmodule Mitos.CrashTest do
  @moduledoc """
  The crash test that 01 §4 / 04 §7 declare MUST pass:

      seq 41  Decision  advance(target=stage:test)   ✔ on log
      seq 42  Effect    capability_started            ✘ never committed  ← crash here

  Recovery sees the Decision with no Effect. It does not guess and does not
  re-decide — it re-drives the Decision. Because the Decision is total and the
  Effect is not, the action simply runs again.
  """
  use ExUnit.Case, async: true

  alias Mitos.Log
  alias Mitos.Event.{Decision, Effect}
  alias Mitos.Run.State

  defp run_id, do: "test/crash_" <> Mitos.id()

  test "a Decision with no committed Effect is the pending recovery tail" do
    rid = run_id()
    {:ok, w, []} = Log.open_writer(rid)

    # A committed step: advance + its Effect.
    {d0, w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :advance, target: "plan"))
    {_, w} = Log.append(w, Effect.new(run_id: rid, type: :capability_started, caused_by: d0.seq))

    # The crash: a Decision is appended, its Effect never is. Simulate by
    # appending the Decision then "dying" (dropping the writer without the Effect).
    {d1, w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :advance, target: "test"))
    Log.close(w)

    # Recovery: reopen and fold the committed log.
    {:ok, _w2, committed} = Log.open_writer(rid)
    st = State.fold(rid, committed)

    # d0 is committed (has an Effect); d1 is pending (no Effect references it).
    pending = State.pending_decisions(st)
    assert Enum.map(pending, & &1.seq) == [d1.seq]
    assert hd(pending).type == :advance
    assert hd(pending).target == "test"

    # The recovery does not re-decide; it re-drives d1. No Effect references d1
    # yet — it is the tail to re-drive (04 §7.1 idempotency).
    refute Enum.any?(committed, fn e -> match?(%Effect{}, e) and e.caused_by == d1.seq end)
  end

  test "next seq continues monotonically after reopen (no torn pointer)" do
    rid = run_id()
    {:ok, w, []} = Log.open_writer(rid)
    {_, w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :advance, target: "a"))
    {_, w} = Log.append(w, Effect.new(run_id: rid, type: :capability_started, caused_by: 0))
    Log.close(w)

    # Reopen: next_seq is computed from the immutable log, not a mutable pointer.
    {:ok, w2, committed} = Log.open_writer(rid)
    assert length(committed) == 2
    {e, _w2} = Log.append(w2, Effect.new(run_id: rid, type: :capability_finished, caused_by: 0))
    assert e.seq == 2
  end

  test "re-driving a pending Decision converges: one started Effect wins" do
    rid = run_id()
    {:ok, w, []} = Log.open_writer(rid)
    {d, w} = Log.append(w, Decision.new(run_id: rid, actor: "orchestrator", type: :advance, target: "test"))
    Log.close(w)

    # Recovery drives d once: append the Effect it was missing.
    {:ok, w2, _committed} = Log.open_writer(rid)
    {_, _w2} = Log.append(w2, Effect.new(run_id: rid, type: :capability_started, caused_by: d.seq, ref: %{"idem" => "#{rid}:#{d.seq}"}))

    st = State.fold(rid)
    started = Enum.filter(st.effects, &(&1.type == :capability_started and &1.caused_by == d.seq))
    assert length(started) == 1
    assert State.pending_decisions(st) == []
  end
end
