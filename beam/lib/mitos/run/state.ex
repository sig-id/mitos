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
            decision_log: [],
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
            # 16 §3.1 — segment boundaries, newest last. `green` is the
            # acceptance verdict and nothing else.
            checkpoints: [],
            # 15 — cards raised on this run, and how each was answered. The
            # human/default split is preserved forever because
            # `decision_precision` (16 §6) is computed from it.
            decisions: %{open: %{}, resolved: [], defaulted: [], rejected: []},
            # 14 §2 — the last quiescence edge seen, per level.
            quiescence: %{},
            # 16 §2 — set while a card is open with no default (fail-secure park).
            blocked_on_human: false,
            result_ref: nil,
            last_seq: -1

  @doc "Fold a run's committed log into state."
  def fold(run_id) when is_binary(run_id), do: fold(run_id, Mitos.Log.read(run_id))

  def fold(run_id, events) do
    base = %__MODULE__{run_id: run_id, events: events}
    Enum.reduce(events, base, &apply_event/2)
  end

  defp apply_event(%Decision{} = d, st) do
    st = %{st | decision_log: [d | st.decision_log], last_seq: d.seq, budget: budget_of(d, st)}
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

  # 16 §3.3: rewind APPENDS. The abandoned segment stays on the record — that is
  # what makes attribution (16 §6.1) possible at all — and the targeted stage is
  # re-opened rather than rewritten.
  defp apply_decision(%Decision{type: :rewind, params: p}, st) do
    stage_id = p["stage_id"] || p[:stage_id]

    st
    |> Map.put(:status, :running)
    |> then(fn s ->
      if stage_id, do: update_stage(s, stage_id, &%{&1 | status: :opened, result: nil, evidence: []}), else: s
    end)
  end

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

  defp apply_effect(%Effect{type: :checkpoint_taken} = e, st),
    do: %{st | checkpoints: st.checkpoints ++ [Mitos.Checkpoint.from_effect(e)]}

  defp apply_effect(%Effect{type: :acceptance_recorded} = e, st) do
    verdict = e.payload["verdict"]

    update_stage(st, e.ref["stage_id"], fn s ->
      Map.merge(s, %{
        acceptance: verdict,
        score: e.payload["score"],
        delta: e.payload["delta"],
        tampered: !!e.payload["tampered"]
      })
    end)
  end

  defp apply_effect(%Effect{type: :quiescent} = e, st),
    do: %{st | quiescence: Map.put(st.quiescence, e.ref["level"], e.payload["for_ms"])}

  defp apply_effect(%Effect{type: :approval_requested} = e, st) do
    card = Mitos.Decision.Card.from_payload(st.run_id, e.payload, e.caused_by)

    %{
      st
      | decisions: %{st.decisions | open: Map.put(st.decisions.open, card.decision_id, card)},
        # A card with no default parks indefinitely (15 §4) — that is what
        # "blocked on a human" means to every reader of this fold.
        blocked_on_human: st.blocked_on_human or is_nil(card.default)
    }
  end

  defp apply_effect(%Effect{type: :decision_defaulted} = e, st),
    do: %{st | decisions: %{st.decisions | defaulted: st.decisions.defaulted ++ [e.payload]}}

  defp apply_effect(%Effect{type: :decision_rejected} = e, st),
    do: %{st | decisions: %{st.decisions | rejected: st.decisions.rejected ++ [e.payload]}}

  defp apply_effect(%Effect{type: :approval_resolved} = e, st) do
    id = e.ref["decision_id"] || e.payload["decision_id"]
    open = Map.delete(st.decisions.open, id)

    st = %{
      st
      | decisions: %{st.decisions | open: open, resolved: st.decisions.resolved ++ [e.payload]},
        blocked_on_human: Enum.any?(Map.values(open), &is_nil(&1.default))
    }

    if st.status == :parked, do: %{st | status: :running}, else: st
  end

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
      evidence: [],
      # 16 §3.2 — the HARD verdict, kept beside the soft `result` and never
      # merged into it: they answer different questions.
      acceptance: nil,
      score: nil,
      delta: nil,
      tampered: false
    }

  defp update_stage(st, nil, _f), do: st

  defp update_stage(st, stage_id, f) do
    stage = Map.get(st.stages, stage_id, new_stage(stage_id))
    order = if stage_id in st.stage_order, do: st.stage_order, else: st.stage_order ++ [stage_id]
    %{st | stages: Map.put(st.stages, stage_id, f.(stage)), stage_order: order}
  end

  @doc "The last green checkpoint (16 §3.3's default rewind target)."
  def last_green(%__MODULE__{checkpoints: cps}, seq \\ nil),
    do: Mitos.Checkpoint.last_green(cps, seq)

  @doc "A checkpoint by id."
  def checkpoint(%__MODULE__{checkpoints: cps}, id),
    do: Enum.find(cps, &(&1.checkpoint_id == id))

  @doc """
  Is this run currently parked on a human with no default to advance it?
  `15 §4`'s fail-secure state, read straight off the fold.
  """
  def blocked?(%__MODULE__{blocked_on_human: b}), do: b

  @doc "Is the run in a terminal state? (04 §3.4 `terminal(state)`)"
  def terminal?(%__MODULE__{status: s}), do: s in [:finished, :aborted]

  @doc """
  Decisions with no Effect referencing them (04 §7): the crash-recovery tail.
  A Decision is committed once at least one Effect has `caused_by == seq`.
  Terminal Decisions (finish/abort) are self-committing (they mutate status).
  """
  def pending_decisions(%__MODULE__{} = st) do
    caused = st.effects |> Enum.map(& &1.caused_by) |> MapSet.new()

    st.decision_log
    |> Enum.reject(fn d ->
      d.type in [:finish, :abort, :skip] or MapSet.member?(caused, d.seq)
    end)
    |> Enum.sort_by(& &1.seq)
  end
end
