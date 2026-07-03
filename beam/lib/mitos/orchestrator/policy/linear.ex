defmodule Mitos.Orchestrator.Policy.Linear do
  @moduledoc """
  The MVP orchestrator policy (09 §3): a **single linear pipeline** — advance
  through the declared stages in order, `skip` a stage whose capability is not
  in the run's catalog, `finish` when every stage is done, `abort` when the
  budget is exhausted. No insert/switch branching, no model call.

  This is a deterministic stand-in behind the same `Mitos.Orchestrator.Policy`
  seam an agentic orchestrator fills later — the Decision vocabulary, the
  fold-state input, and the append-before-execute loop (04 §3.4) are already
  the real thing.
  """
  @behaviour Mitos.Orchestrator.Policy

  alias Mitos.Event.Decision

  @impl true
  def decide(ctx) do
    stages = stages(ctx)
    done = finished_or_skipped(ctx.state)
    next = Enum.find(stages, fn s -> s.name not in done end)

    cond do
      is_nil(next) ->
        finish(ctx, stages)

      exhausted?(ctx.remaining) ->
        abort(ctx, :budget, "budget exhausted before stage #{next.name}")

      not Map.has_key?(ctx.capabilities, next.name) ->
        decision(ctx, :skip, next.name, %{reason: "no such capability in run catalog"},
          rationale: "stage #{next.name} names no known capability; skipping"
        )

      true ->
        decision(
          ctx,
          :advance,
          next.name,
          %{capability: next.name, intent: next.intent},
          rationale: "linear order: #{next.name} is the first unfinished stage"
        )
    end
  end

  # Fall back to the run catalog when the intent declares no stages — a paid
  # single-capability run gets a generated one-stage pipeline either way (08 §9).
  defp stages(ctx) do
    case ctx.intent.stages do
      [] -> ctx.capabilities |> Map.keys() |> Enum.sort() |> Enum.map(&%{name: &1, intent: ""})
      stages -> stages
    end
  end

  defp finished_or_skipped(state) do
    finished =
      for {id, %{status: :finished}} <- state.stages, into: MapSet.new(), do: id

    skipped =
      for %Decision{type: :skip, target: t} <- state.decisions,
          not is_nil(t),
          into: MapSet.new(),
          do: t

    MapSet.union(finished, skipped)
  end

  defp finish(ctx, stages) do
    last =
      stages
      |> Enum.reverse()
      |> Enum.find_value(fn s ->
        case ctx.state.stages[s.name] do
          %{status: :finished} -> s.name
          _ -> nil
        end
      end)

    met = success_criteria_met?(ctx.state, stages)

    decision(ctx, :finish, nil, %{result_ref: last && "stage:#{last}", success_criteria_met: met},
      rationale: "all stages finished or skipped"
    )
  end

  defp abort(ctx, cause, detail) do
    decision(ctx, :abort, nil, %{cause: cause, detail: detail}, rationale: detail)
  end

  # A soft read of soft results (01 §5): every finished stage reporting a
  # non-blocking signal counts as met. The signal never drives control flow
  # beyond this advisory bit — it lands in finish.params for the record.
  defp success_criteria_met?(state, stages) do
    stages
    |> Enum.map(&state.stages[&1.name])
    |> Enum.filter(& &1)
    |> Enum.all?(fn stage ->
      signal = stage.result && stage.result["signal"]
      signal not in ["block", "blocked", "error"]
    end)
  end

  # `remaining` carries only the axes the run declared a ceiling for (04 §8.1:
  # omitted budget ⇒ tenant/plan default, MVP: unconstrained). Any declared
  # axis at or below zero is exhaustion.
  defp exhausted?(remaining) when map_size(remaining) == 0, do: false
  defp exhausted?(remaining), do: Enum.any?(remaining, fn {_axis, v} -> v <= 0 end)

  defp decision(ctx, type, target, params, opts) do
    Decision.new(
      actor: "orchestrator",
      type: type,
      target: target,
      params: params,
      inputs_hash: inputs_hash(ctx),
      rationale: Keyword.get(opts, :rationale, ""),
      budget: budget_fields(ctx)
    )
  end

  # Hash of the state the decision was taken on (01 §8.1 replay integrity):
  # the fold position + the intent identity pin what this decision saw.
  defp inputs_hash(ctx) do
    Mitos.sha256(
      Mitos.canonical_json(%{
        last_seq: ctx.state.last_seq,
        intent: ctx.intent.sha256
      })
    )
  end

  # Budget carried on EVERY decision (04 §5).
  defp budget_fields(ctx) do
    %{
      tokens: ctx.budget["tokens"] || 0,
      usd: ctx.budget["usd"] || 0.0,
      wall_ms: ctx.budget["wall_ms"] || 0,
      remaining: Map.new(ctx.remaining, fn {k, v} -> {to_string(k), v} end)
    }
  end
end
