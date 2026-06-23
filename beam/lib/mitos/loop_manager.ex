defmodule Mitos.LoopManager do
  @moduledoc """
  Deterministic intra-step loop manager.

  This is deliberately not an agent. It watches one capability step, emits
  iteration/control Effects, and only emits a `loop_manager` Decision when a
  deterministic signal requires climbing the local ladder or escalating back to
  the orchestrator.
  """

  alias Mitos.Trace

  @default_budget %{"tokens" => 0, "usd" => 0, "wall_ms" => 0, "remaining" => %{}}
  @default_ladder [:effort, :model, :harness]
  @default_thresholds %{
    no_progress: 3,
    repeated_error: 2,
    verify_fail: 2,
    context_ratio: 0.85
  }

  defstruct [
    :opening_decision_seq,
    :target,
    :last_result_state,
    budget: @default_budget,
    iteration: 0,
    no_progress_streak: 0,
    error_class: nil,
    error_streak: 0,
    verify_fail_streak: 0,
    climb_history: [],
    compacted: false
  ]

  @type t :: %__MODULE__{}

  @doc "Create loop state for one opened capability step."
  def new(opts) when is_list(opts) do
    %__MODULE__{
      opening_decision_seq: Keyword.fetch!(opts, :opening_decision_seq),
      target: Keyword.get(opts, :target),
      budget: Keyword.get(opts, :budget, @default_budget),
      climb_history: Keyword.get(opts, :climb_history, [])
    }
  end

  @doc """
  Advance the FSM and append the resulting Effects/Decision to `trace`.

  Returns `{action, state, trace}` where `action` is one of `:continue`,
  `:compact_context`, `:switch`, `:escalate`, or `:done`.
  """
  def advance(%Trace{} = trace, %__MODULE__{} = state, observation, opts \\ []) do
    {action, state, plan} = plan(state, observation, opts)
    trace = append_opening_effects(trace, state.opening_decision_seq, plan.effects)

    {trace, budget_aborted?} =
      case plan do
        %{decision: nil} ->
          {trace, false}

        %{decision: decision, decision_effect: decision_effect} ->
          case Trace.append_decision_checked(trace, decision) do
            {:ok, trace, %{"seq" => decision_seq}} ->
              {Trace.append_effect(trace, Map.put(decision_effect, :caused_by, decision_seq)),
               false}

            {:abort, trace, _decision} ->
              {trace, true}
          end
      end

    action =
      if budget_aborted? do
        :abort
      else
        action
      end

    {action, state, trace}
  end

  @doc "Plan the FSM transition without writing to the trace."
  def plan(%__MODULE__{} = state, observation, opts \\ []) do
    thresholds = Map.merge(@default_thresholds, Map.new(Keyword.get(opts, :thresholds, %{})))
    ladder = Keyword.get(opts, :ladder, @default_ladder)
    observation = normalize_observation(observation)

    state = update_streaks(state, observation)
    iteration_effect = iteration_finished_effect(state, observation)

    cond do
      truthy?(observation["done?"]) or truthy?(observation["final_result"]) ->
        {:done, state, %{effects: [iteration_effect], decision: nil, decision_effect: nil}}

      budget_burn?(observation) ->
        escalate(state, observation, :budget_burn, [iteration_effect])

      context_near_limit?(observation, thresholds) ->
        compact(state, observation, [iteration_effect])

      state.no_progress_streak >= thresholds.no_progress and not state.compacted ->
        compact(state, observation, [iteration_effect])

      signal = tripped_signal(state, thresholds) ->
        climb_or_escalate(state, observation, signal, ladder, [iteration_effect])

      true ->
        {:continue, state, %{effects: [iteration_effect], decision: nil, decision_effect: nil}}
    end
  end

  defp append_opening_effects(trace, opening_decision_seq, effects) do
    Enum.reduce(effects, trace, fn effect, trace ->
      Trace.append_effect(trace, Map.put(effect, :caused_by, opening_decision_seq))
    end)
  end

  defp update_streaks(%__MODULE__{} = state, observation) do
    result_state = observation["result_state"]
    progress? = truthy?(observation["diff?"]) or truthy?(observation["artifact?"])
    no_progress? = not progress? and result_state == state.last_result_state
    error_class = observation["error_class"]

    %{
      state
      | iteration: state.iteration + 1,
        last_result_state: result_state || state.last_result_state,
        no_progress_streak: streak(no_progress?, state.no_progress_streak),
        error_class: error_class,
        error_streak: error_streak(state, error_class),
        verify_fail_streak:
          streak(observation["verify_status"] == "fail", state.verify_fail_streak)
    }
  end

  defp iteration_finished_effect(state, observation) do
    %{
      type: "iteration_finished",
      ref: %{"iteration" => state.iteration, "target" => state.target},
      payload: %{
        "diff" => truthy?(observation["diff?"]),
        "artifact" => truthy?(observation["artifact?"]),
        "result_state" => observation["result_state"],
        "error_class" => observation["error_class"],
        "verify_status" => observation["verify_status"]
      }
    }
  end

  defp compact(state, observation, effects) do
    effect = %{
      type: "context_compacted",
      ref: %{"iteration" => state.iteration, "target" => state.target},
      payload: %{
        "signal" => "no_progress",
        "pre_tokens" => observation["context_tokens"],
        "limit_tokens" => observation["context_limit"]
      }
    }

    {:compact_context, %{state | compacted: true},
     %{effects: effects ++ [effect], decision: nil, decision_effect: nil}}
  end

  defp climb_or_escalate(state, observation, signal, ladder, effects) do
    if length(state.climb_history) >= length(ladder) do
      escalate(state, observation, signal, effects)
    else
      rung = Enum.at(ladder, length(state.climb_history))
      history = state.climb_history ++ [to_string(rung)]

      decision = %{
        actor: "loop_manager",
        type: "switch",
        target: state.target,
        params:
          %{"signal" => to_string(signal), "climb_history" => history}
          |> Map.merge(rung_params(rung)),
        inputs_hash: inputs_hash(state, observation, signal, rung),
        rationale: "Loop-manager climbed #{rung} after #{signal}",
        budget: state.budget
      }

      decision_effect = %{
        type: "harness_switched",
        ref: %{"target" => state.target, "rung" => to_string(rung)},
        payload: %{"signal" => to_string(signal), "climb_history" => history}
      }

      {:switch, %{state | climb_history: history},
       %{effects: effects, decision: decision, decision_effect: decision_effect}}
    end
  end

  defp escalate(state, observation, signal, effects) do
    decision = %{
      actor: "loop_manager",
      type: "escalate",
      target: state.target,
      params: %{"signal" => to_string(signal), "climb_history" => state.climb_history},
      inputs_hash: inputs_hash(state, observation, signal, :escalate),
      rationale: "Loop-manager escalated #{signal} to the orchestrator",
      budget: state.budget
    }

    decision_effect = %{
      type: "escalation_raised",
      ref: %{"target" => state.target},
      payload: %{"signal" => to_string(signal), "climb_history" => state.climb_history}
    }

    {:escalate, state, %{effects: effects, decision: decision, decision_effect: decision_effect}}
  end

  defp tripped_signal(state, thresholds) do
    cond do
      state.error_streak >= thresholds.repeated_error -> :repeated_error
      state.verify_fail_streak >= thresholds.verify_fail -> :verify_fail
      state.no_progress_streak >= thresholds.no_progress -> :no_progress
      true -> nil
    end
  end

  defp context_near_limit?(%{"context_tokens" => tokens, "context_limit" => limit}, thresholds)
       when is_number(tokens) and is_number(limit) and limit > 0 do
    tokens / limit >= thresholds.context_ratio
  end

  defp context_near_limit?(_observation, _thresholds), do: false

  defp budget_burn?(%{"budget_burn?" => value}), do: truthy?(value)
  defp budget_burn?(_observation), do: false

  defp rung_params(:effort), do: %{"effort" => "higher"}
  defp rung_params(:model), do: %{"model" => "stronger"}
  defp rung_params(:harness), do: %{"harness" => "alternate"}
  defp rung_params(rung), do: %{"rung" => to_string(rung)}

  defp streak(true, count), do: count + 1
  defp streak(false, _count), do: 0

  defp error_streak(state, nil),
    do: if(is_nil(state.error_class), do: state.error_streak, else: 0)

  defp error_streak(%{error_class: same, error_streak: count}, same), do: count + 1
  defp error_streak(_state, _error_class), do: 1

  defp inputs_hash(state, observation, signal, rung) do
    payload = %{
      target: state.target,
      iteration: state.iteration,
      signal: signal,
      rung: rung,
      climb_history: state.climb_history,
      observation: observation
    }

    digest =
      :crypto.hash(:sha256, canonical_json(payload))
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end

  defp normalize_observation(%{} = observation) do
    Map.new(observation, fn {key, value} -> {to_string(key), value} end)
  end

  defp normalize_observation(_observation), do: %{}

  defp truthy?(value), do: value in [true, "true", 1, "1"]

  defp canonical_json(%{} = map) do
    body =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map_join(",", fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> body <> "}"
  end

  defp canonical_json(list) when is_list(list),
    do: "[" <> Enum.map_join(list, ",", &canonical_json/1) <> "]"

  defp canonical_json(value), do: Jason.encode!(value)
end
