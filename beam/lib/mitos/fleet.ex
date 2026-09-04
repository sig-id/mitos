defmodule Mitos.Fleet do
  @moduledoc """
  The fleet supervisor (`16 §5`) — oversight of *other* runs, from evidence
  only.

  Two structural choices carry the whole value, and both are enforced here
  rather than trusted:

    * **Independence.** `evidence/1` is the only view a supervisor gets:
      Decisions, Effects, diffs, predicate verdicts, evidence refs. It cannot
      return `assistant_text`, the inner trace, the raw spool, or `rationale`.
      `04 §3.1` already forbids the orchestrator from reading its own run's
      narration; this extends the rule one level up, and the reason is not
      tidiness — a supervisor that reads a worker's narration inherits its
      delusions and rubber-stamps them.
    * **A restricted move set.** `Mitos.Event.Decision` refuses
      `advance | insert | skip | finish` from `actor: "supervisor"` (`16 §5.2`).
      A supervisor that can declare another run finished can declare it
      finished wrongly.

  A supervisor is a capability running in the substrate, so it is sandboxed,
  metered, and replayable like any other — mitos supervising itself with its own
  machinery.
  """

  alias Mitos.Event.Decision
  alias Mitos.Run.State

  @doc """
  The evidence view of a run. Everything a supervisor may see, and nothing else.
  """
  def evidence(run_id) when is_binary(run_id), do: evidence(State.fold(run_id))

  def evidence(%State{} = st) do
    %{
      run_id: st.run_id,
      status: st.status,
      blocked_on_human: st.blocked_on_human,
      spent: st.spent,
      budget: st.budget,
      stages:
        Enum.map(st.stage_order, fn id ->
          s = st.stages[id]

          %{
            id: id,
            status: s.status,
            capability_ref: s.capability_ref,
            # The SOFT signal only — a label, never the summary text, which is
            # the worker's narration about itself.
            result_signal: s.result && s.result["signal"],
            acceptance: s.acceptance,
            score: s.score,
            delta: s.delta,
            tampered: s.tampered,
            evidence_refs: Enum.map(s.evidence || [], &(&1["artifact"] || &1["path"]))
          }
        end),
      checkpoints:
        Enum.map(st.checkpoints, &%{id: &1.checkpoint_id, seq: &1.seq, green: &1.green, verdict: &1.predicate.verdict}),
      decisions: %{
        # Types and causality — the shape of the policy trace, not its prose.
        trace: Enum.map(Enum.reverse(st.decision_log), &%{seq: &1.seq, actor: &1.actor, type: &1.type, target: &1.target}),
        open: map_size(st.decisions.open),
        resolved: length(st.decisions.resolved),
        defaulted: length(st.decisions.defaulted)
      },
      signals: signals_seen(st),
      last_seq: st.last_seq
    }
  end

  @doc """
  Runs a supervisor should look at, with why. Deliberately cheap and
  deterministic: the decision to *look* must not itself need a model.
  """
  def attention(run_ids) do
    run_ids
    |> Enum.map(&evidence/1)
    |> Enum.map(&{&1, reasons(&1)})
    |> Enum.reject(fn {_e, reasons} -> reasons == [] end)
    |> Enum.sort_by(fn {_e, reasons} -> -length(reasons) end)
    |> Enum.map(fn {e, reasons} -> %{run_id: e.run_id, reasons: reasons, evidence: e} end)
  end

  defp reasons(e) do
    []
    |> then(&if e.blocked_on_human, do: &1 ++ [:blocked_on_human], else: &1)
    |> then(&if Enum.any?(e.stages, fn s -> s.tampered end), do: &1 ++ [:oracle_tampered], else: &1)
    |> then(&if Enum.any?(e.stages, fn s -> s.acceptance == "fail" end), do: &1 ++ [:acceptance_failed], else: &1)
    # Only when an oracle actually ran and never passed. A capability that
    # declares no predicate has no green checkpoints by construction (16 §3.2)
    # and is already handled by its zero unattended grant — flagging it here
    # would drown the real signal in noise.
    |> then(&if verified_but_never_green?(e), do: &1 ++ [:no_green_checkpoint], else: &1)
    |> then(&if :predicate_stall in e.signals, do: &1 ++ [:predicate_stall], else: &1)
  end

  defp verified_but_never_green?(e) do
    Enum.any?(e.checkpoints, &(&1.verdict not in [:skipped, nil])) and
      not Enum.any?(e.checkpoints, & &1.green)
  end

  defp signals_seen(%State{effects: effects}) do
    effects
    |> Enum.filter(&(&1.type == :escalation_raised))
    |> Enum.map(&(&1.payload["signal"] && String.to_atom(&1.payload["signal"])))
    |> Enum.reject(&is_nil/1)
    |> Enum.uniq()
  end

  @doc """
  Act on another run. `type` MUST be in the supervisor's restricted set;
  `Decision.new/1` refuses anything else, so a bug here cannot become a
  supervisor finishing someone else's run.
  """
  def act(run_id, type, params \\ %{}, opts \\ []) do
    evidence = evidence(run_id)

    decision =
      Decision.new(
        actor: "supervisor",
        type: type,
        target: opts[:target],
        params: Map.new(params),
        # Replay integrity (01 §8.1): the hash is over the EVIDENCE the
        # supervisor saw, which is the only state it was allowed to see.
        inputs_hash: Mitos.sha256(Mitos.canonical_json(sanitize(evidence))),
        rationale: opts[:rationale] || "supervisor: #{type}",
        budget: opts[:budget] || %{}
      )

    Mitos.Run.append_decision(run_id, decision)
  end

  @doc "Supervisor-permitted Decision types (16 §5.2)."
  def permitted, do: Decision.supervisor_types()

  defp sanitize(%{} = m), do: Map.new(m, fn {k, v} -> {to_string(k), sanitize(v)} end)
  defp sanitize(l) when is_list(l), do: Enum.map(l, &sanitize/1)
  defp sanitize(a) when is_atom(a) and not is_boolean(a) and not is_nil(a), do: to_string(a)
  defp sanitize(other), do: other
end
