defmodule Mitos.Run.State do
  @moduledoc """
  `state(t) = fold(events[0..t])` (01 §4). The folded read model the
  orchestrator reads each turn (04 §3.1) — results, evidence refs, Effect
  summaries — and the source for resume/replay (04 §7), metering (08 §4), and
  the proof/report render (11 §5).

  There is no separately-stored run state; this struct is always a pure function
  of the log.
  """

  alias Mitos.Event.{Decision, Effect}

  defstruct run_id: nil,
            # :pending | :running | :finished | :aborted | :parked
            status: :pending,
            events: [],
            decisions: [],
            effects: [],
            # stage_id/capability_ref => stage read model (below)
            stages: %{},
            # order stages were opened
            stage_order: [],
            # last budget seen on a Decision (01 §8.1 budget carried on every decision)
            budget: %{tokens: 0, usd: 0.0, wall_ms: 0, remaining: %{}},
            # spent so far, folded from budget_charged Effects
            spent: %{tokens: 0, usd: 0.0, wall_ms: 0},
            # I/O-plane deliveries (10 §5.3), folded from delivery_* Effects —
            # consumed here so no Effect subtype is silently ignored (01 §4).
            deliveries: %{sent: [], failed: []},
            result_ref: nil,
            last_seq: -1

  @doc "Fold a run's committed log into state."
  def fold(run_id) when is_binary(run_id), do: fold(run_id, Mitos.Log.read(run_id))

  def fold(run_id, events) do
    base = %__MODULE__{run_id: run_id, events: events}
    Enum.reduce(events, base, &apply_event/2)
  end

  defp apply_event(%Decision{} = d, st) do
    st = %{st | decisions: [d | st.decisions], last_seq: d.seq, budget: budget_of(d, st)}
    apply_decision(d, st)
  end

  defp apply_event(%Effect{} = e, st) do
    st = %{st | effects: [e | st.effects], last_seq: e.seq}
    apply_effect(e, st)
  end

  defp budget_of(%Decision{budget: b}, _st) when is_map(b) and map_size(b) > 0,
    do: normalize_budget(b)

  defp budget_of(_d, st), do: st.budget

  defp normalize_budget(b) do
    %{
      tokens: b["tokens"] || b[:tokens] || 0,
      usd: b["usd"] || b[:usd] || 0.0,
      wall_ms: b["wall_ms"] || b[:wall_ms] || 0,
      remaining: b["remaining"] || b[:remaining] || %{}
    }
  end

  # --- decision effects on state ---

  defp apply_decision(%Decision{type: t, target: target} = d, st)
       when t in [:advance, :insert, :retry] do
    stage_id = target || "stage_#{d.seq}"
    stage = Map.get(st.stages, stage_id, new_stage(stage_id))
    stage = %{stage | status: :opened, opened_by: d.seq, capability_ref: target}
    order = if stage_id in st.stage_order, do: st.stage_order, else: st.stage_order ++ [stage_id]
    %{st | stages: Map.put(st.stages, stage_id, stage), stage_order: order, status: :running}
  end

  defp apply_decision(%Decision{type: :finish, params: p}, st) do
    %{st | status: :finished, result_ref: p["result_ref"] || p[:result_ref]}
  end

  defp apply_decision(%Decision{type: :abort}, st), do: %{st | status: :aborted}

  defp apply_decision(%Decision{type: :request_approval}, st), do: %{st | status: :parked}

  defp apply_decision(_d, st), do: st

  # --- effect effects on state ---

  defp apply_effect(%Effect{type: :run_started}, st), do: %{st | status: :running}

  defp apply_effect(%Effect{type: :capability_finished} = e, st) do
    stage_id = e.ref["stage_id"] || e.ref["capability_ref"]
    update_stage(st, stage_id, fn s ->
      %{s | status: :finished, result: e.payload["result"], evidence: e.payload["evidence"] || []}
    end)
  end

  defp apply_effect(%Effect{type: :budget_charged} = e, st) do
    p = e.payload
    spent = st.spent

    %{
      st
      | spent: %{
          tokens: spent.tokens + (p["tokens"] || 0),
          usd: spent.usd + (p["usd"] || 0.0),
          wall_ms: spent.wall_ms + (p["wall_ms"] || 0)
        }
    }
  end

  defp apply_effect(%Effect{type: :approval_resolved}, st),
    do: if(st.status == :parked, do: %{st | status: :running}, else: st)

  # I/O-plane deliveries (10 §5.3) — consumed so the read model is complete.
  defp apply_effect(%Effect{type: :delivery_sent} = e, st) do
    entry = %{sink: e.ref["sink"], dest: e.ref["dest"], receipt: e.ref["receipt"], ts: e.ts}
    %{st | deliveries: %{st.deliveries | sent: st.deliveries.sent ++ [entry]}}
  end

  defp apply_effect(%Effect{type: :delivery_failed} = e, st) do
    entry = %{sink: e.ref["sink"], dest: e.ref["dest"], reason: e.payload["reason"], ts: e.ts}
    %{st | deliveries: %{st.deliveries | failed: st.deliveries.failed ++ [entry]}}
  end

  # delivery_attempted / signal_received / task_enqueued (and the rest) are
  # I/O-plane Effects that don't change execution state; they are deliberately
  # no-ops on the control-plane read model (consumed, not ignored — 01 §4).
  defp apply_effect(_e, st), do: st

  defp new_stage(stage_id),
    do: %{
      id: stage_id,
      status: :new,
      opened_by: nil,
      capability_ref: nil,
      result: nil,
      evidence: []
    }

  defp update_stage(st, nil, _f), do: st

  defp update_stage(st, stage_id, f) do
    stage = Map.get(st.stages, stage_id, new_stage(stage_id))
    order = if stage_id in st.stage_order, do: st.stage_order, else: st.stage_order ++ [stage_id]
    %{st | stages: Map.put(st.stages, stage_id, f.(stage)), stage_order: order}
  end

  @doc "Is the run in a terminal state? (04 §3.4 `terminal(state)`)"
  def terminal?(%__MODULE__{status: s}), do: s in [:finished, :aborted]

  @doc """
  Decisions with no Effect referencing them (04 §7): the crash-recovery tail.
  A Decision is committed once at least one Effect has `caused_by == seq`.
  Terminal Decisions (finish/abort) are self-committing (they mutate status).
  """
  def pending_decisions(%__MODULE__{} = st) do
    caused = st.effects |> Enum.map(& &1.caused_by) |> MapSet.new()

    st.decisions
    |> Enum.reject(fn d ->
      d.type in [:finish, :abort, :skip] or MapSet.member?(caused, d.seq)
    end)
    |> Enum.sort_by(& &1.seq)
  end
end
