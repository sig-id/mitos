defmodule Mitos.Orchestrator.Policy.Agentic do
  @moduledoc """
  The agentic (LLM-backed) orchestrator policy (04 §3). It composes a decision
  prompt from the folded state + intent + budget, asks ONE model call (04 §3.4:
  one call → one Decision), and maps the response to a Decision. On any model
  outage, unparseable response, or budget exhaustion it **degrades to the
  deterministic `Linear` policy** — the run never blocks on a model (04 §3.4).

  This is the policy behind the same `Mitos.Orchestrator.Policy` seam as
  `Linear`; a run selects it via `Run.Config.policy`. The model client is
  pluggable (`config :mitos, :orchestrator_model`, default `Model.Stub` ⇒
  Linear-equivalent); tests inject a fake client to drive non-linear decisions
  (retry-after-fix, escalate-on-stuck).

  Non-linear behaviors the model owns (vs Linear's strict order):

    * a finished stage reporting a non-ship signal ⇒ the model may `retry` or
      `insert` a fix step instead of advancing;
    * repeated failure ⇒ `escalate` or `abort`;
    * a stuck stage ⇒ `switch` model/harness or `escalate`.

  Budget is enforced by the substrate regardless of the model's choice (the run
  server refuses an exhausted-budget Decision, 04 §3.4), so this policy only
  *proposes*.
  """
  @behaviour Mitos.Orchestrator.Policy

  alias Mitos.Event.Decision
  alias Mitos.Orchestrator.Policy.Linear

  @impl true
  def decide(ctx) do
    cond do
      exhausted?(ctx.remaining) ->
        # Hard floor; the model is not consulted on a spent budget.
        Linear.decide(ctx)

      true ->
        case call_model(ctx) do
          {:ok, decision_map} ->
            build_decision(ctx, decision_map)

          {:error, _reason} ->
            # Degrade to deterministic — never block a run on a model outage.
            Linear.decide(ctx)
        end
    end
  end

  # --- model call ---

  defp call_model(ctx) do
    model().complete(compose_prompt(ctx), model_opts(ctx))
  end

  defp model, do: Application.get_env(:mitos, :orchestrator_model, Mitos.Orchestrator.Model.Stub)

  defp model_opts(ctx),
    do: [model: get_in(ctx, [:config, :orchestrator_model_name])]

  defp compose_prompt(ctx) do
    stages_summary =
      (ctx.intent.stages |> Enum.map(&"- #{&1.name}: #{&1.intent}") |> Enum.join("\n")) ||
        "no declared stages"

    state_summary =
      ctx.state.stages
      |> Enum.map(fn {id, s} ->
        signal = s.result && s.result["signal"]
        "  - #{id}: #{s.status}" <> if(signal, do: " (signal=#{signal})", else: "")
      end)
      |> Enum.join("\n")

    """
    PIPELINE INTENT:
    #{ctx.intent.goal}

    STAGES:
    #{stages_summary}

    CURRENT STATE (fold of the event log):
    status: #{ctx.state.status}
    stages:
    #{state_summary}
    budget remaining: #{inspect(ctx.remaining)}

    Decide the single next action.
    """
    |> String.trim()
  end

  # --- map model decision → Decision struct ---

  defp build_decision(ctx, %{"action" => action} = m) do
    target = m["target"]
    params = Map.get(m, "params", %{}) |> normalize_keys()
    rationale = m["rationale"] || "agentic: #{action}"

    valid_actions = ~w(advance insert retry switch skip escalate finish abort)

    if action in valid_actions do
      Decision.new(
        actor: "orchestrator",
        type: String.to_atom(action),
        target: target,
        params: Map.merge(default_params(action, ctx, target), params),
        inputs_hash: inputs_hash(ctx),
        rationale: rationale,
        budget: budget_fields(ctx)
      )
    else
      Linear.decide(ctx)
    end
  end

  defp build_decision(ctx, _), do: Linear.decide(ctx)

  # An `advance` with no target picks the next unfinished stage (Linear-equivalent).
  defp default_params("advance", ctx, nil) do
    case next_stage(ctx) do
      nil -> %{}
      stage -> %{"capability" => stage, "intent" => stage_intent(ctx, stage)}
    end
  end

  defp default_params("advance", _ctx, target), do: %{"capability" => target}

  defp default_params("finish", ctx, _target) do
    %{"result_ref" => last_finished(ctx), "success_criteria_met" => true}
  end

  defp default_params("abort", _ctx, _target), do: %{"cause" => :agentic, "detail" => "model aborted"}

  defp default_params(_action, _ctx, _target), do: %{}

  defp next_stage(ctx) do
    done = for {id, %{status: s}} <- ctx.state.stages, s in [:finished], into: MapSet.new(), do: id
    skipped = for %Decision{type: :skip, target: t} <- ctx.state.decision_log, t, into: MapSet.new(), do: t
    (ctx.intent.stages |> Enum.map(& &1.name)) |> Enum.find(&(&1 not in MapSet.union(done, skipped)))
  end

  defp stage_intent(ctx, name) do
    case Enum.find(ctx.intent.stages, &(&1.name == name)) do
      %{intent: i} -> i
      nil -> ""
    end
  end

  defp last_finished(ctx) do
    ctx.state.stage_order
    |> Enum.reverse()
    |> Enum.find_value(fn id ->
      case ctx.state.stages[id] do
        %{status: :finished} -> "stage:#{id}"
        _ -> nil
      end
    end)
  end

  defp normalize_keys(%{} = m), do: Map.new(m, fn {k, v} -> {to_string(k), v} end)
  defp normalize_keys(_), do: %{}

  defp exhausted?(remaining) when map_size(remaining) == 0, do: false
  defp exhausted?(remaining), do: Enum.any?(remaining, fn {_axis, v} -> v <= 0 end)

  defp inputs_hash(ctx) do
    Mitos.sha256(Mitos.canonical_json(%{last_seq: ctx.state.last_seq, intent: ctx.intent.sha256}))
  end

  defp budget_fields(ctx) do
    %{
      tokens: ctx.budget["tokens"] || 0,
      usd: ctx.budget["usd"] || 0.0,
      wall_ms: ctx.budget["wall_ms"] || 0,
      remaining: Map.new(ctx.remaining, fn {k, v} -> {to_string(k), v} end)
    }
  end
end
