defmodule Mitos.Autonomy do
  @moduledoc """
  The metrics that say whether any of `16` works, and the horizon grant that
  says whether a run may be left alone (`16 §6`).

  All of it is computed from the existing log, which is the point: the product
  can be steered before most of it is built.

  | metric | definition | direction |
  |---|---|---|
  | autonomy ratio | productive wall-clock / human decisions raised | up |
  | unattended horizon | time to the first *required* decision (no default) | up |
  | decision precision | fraction of cards NOT resolved as their default | up |
  | rewind depth | segments discarded per accepted change | down |
  | green rate | green checkpoints / checkpoints taken | up |

  **Decision precision is the self-tuning one.** A card that is always resolved
  as its default should not have been raised: the rule that raised it is noise
  and should be deleted, or its floor raised (`15 §7`). That is why
  `decision_defaulted` is kept distinct from `approval_resolved` on the log
  forever (`15 §4`) — collapsing them would destroy the only signal that tells
  you which interruptions to stop making.
  """

  alias Mitos.Event.Effect
  alias Mitos.Run.State

  @doc "Metrics for one run."
  def metrics(run_id) when is_binary(run_id), do: metrics(State.fold(run_id))

  def metrics(%State{} = st) do
    raised = count(st, :approval_requested)
    defaulted = count(st, :decision_defaulted)
    resolved = count(st, :approval_resolved)
    human_resolved = max(resolved - defaulted, 0)
    wall = st.spent.wall_ms

    %{
      run_id: st.run_id,
      decisions_raised: raised,
      decisions_by_human: human_resolved,
      decisions_defaulted: defaulted,
      # Productive wall-clock per human decision. No decisions raised means the
      # run was fully autonomous — reported as `:infinity` rather than a
      # division by zero dressed up as a number.
      autonomy_ratio: if(human_resolved == 0, do: :infinity, else: wall / human_resolved),
      unattended_horizon_ms: unattended_horizon(st),
      decision_precision: precision(resolved, defaulted),
      rewind_depth: count(st, :rewound),
      green_rate: green_rate(st),
      checkpoints: length(st.checkpoints),
      wall_ms: wall
    }
  end

  @doc """
  Fleet metrics over many runs. `autonomy_ratio` aggregates as total wall over
  total human decisions, not as a mean of ratios — a mean of ratios lets one
  short fully-autonomous run hide a fleet that interrupts constantly.
  """
  def fleet(run_ids) do
    all = Enum.map(run_ids, &metrics/1)
    wall = Enum.reduce(all, 0, &(&1.wall_ms + &2))
    human = Enum.reduce(all, 0, &(&1.decisions_by_human + &2))
    raised = Enum.reduce(all, 0, &(&1.decisions_raised + &2))
    defaulted = Enum.reduce(all, 0, &(&1.decisions_defaulted + &2))
    horizons = all |> Enum.map(& &1.unattended_horizon_ms) |> Enum.reject(&is_nil/1)

    %{
      runs: length(all),
      autonomy_ratio: if(human == 0, do: :infinity, else: wall / human),
      unattended_horizon_p50_ms: p50(horizons),
      decision_precision: precision(raised, defaulted),
      rewind_depth: Enum.reduce(all, 0, &(&1.rewind_depth + &2)),
      green_rate: mean(Enum.map(all, & &1.green_rate)),
      per_run: all
    }
  end

  @doc """
  May this run be left alone, and for how long (`16 §6`)?

  The horizon is a **grant**, not only a measurement. A harness observable only
  through C1+C3 (`14 §4`), or a capability with no machine-checkable predicate
  (`16 §3.2`), is granted **zero** — it may be watched, not left.
  """
  def grant(manifests, harness_mods, opts \\ []) do
    manifests = List.wrap(manifests)
    harness_mods = List.wrap(harness_mods)

    reasons =
      []
      |> add(:no_acceptance_predicate, Enum.any?(manifests, &(not Mitos.Acceptance.unattended_ok?(&1))))
      |> add(:no_structured_channel, Enum.any?(harness_mods, &(not Mitos.Harness.channels(&1).unattended_ok)))
      |> add(:harness_quarantined, Enum.any?(harness_mods, &quarantined?/1))

    if reasons == [] do
      {:ok, opts[:horizon_ms] || default_horizon()}
    else
      {:zero, reasons}
    end
  end

  @doc """
  Quarantine an adapter whose contract test failed against upstream (`14 §5`).
  Studio drops the run's unattended horizon to zero; Market refuses to schedule
  it. The reason a fleet went quiet is then on the record, not a mystery.
  """
  def quarantine(harness_mod, reason) do
    :persistent_term.put({__MODULE__, :quarantine, harness_mod}, reason)
  end

  def unquarantine(harness_mod), do: :persistent_term.erase({__MODULE__, :quarantine, harness_mod})

  def quarantined?(harness_mod),
    do: :persistent_term.get({__MODULE__, :quarantine, harness_mod}, nil) != nil

  def quarantine_reason(harness_mod),
    do: :persistent_term.get({__MODULE__, :quarantine, harness_mod}, nil)

  # --- internals ---

  # Time from run start to the first card that had NO default — the first point
  # at which the run genuinely could not continue without a person. Cards that
  # defaulted are not the end of a horizon: silence advanced them.
  defp unattended_horizon(%State{events: events}) do
    start = Enum.find(events, &match?(%Effect{type: :run_started}, &1))

    required =
      Enum.find(events, fn
        %Effect{type: :approval_requested, payload: p} -> is_nil(p["default"])
        _ -> false
      end)

    case {start, required} do
      {%{ts: t0}, %{ts: t1}} -> diff_ms(t0, t1)
      {%{ts: t0}, nil} -> last_ts(events) && diff_ms(t0, last_ts(events))
      _ -> nil
    end
  end

  defp last_ts(events), do: events |> List.last() |> then(&(&1 && &1.ts))

  defp diff_ms(t0, t1) do
    with {:ok, a, _} <- DateTime.from_iso8601(to_string(t0)),
         {:ok, b, _} <- DateTime.from_iso8601(to_string(t1)) do
      DateTime.diff(b, a, :millisecond)
    else
      _ -> nil
    end
  end

  defp precision(0, _defaulted), do: nil
  defp precision(resolved, defaulted), do: (resolved - defaulted) / resolved

  defp green_rate(%State{checkpoints: []}), do: nil

  defp green_rate(%State{checkpoints: cps}),
    do: Enum.count(cps, & &1.green) / length(cps)

  defp count(%State{effects: effects}, type), do: Enum.count(effects, &(&1.type == type))

  defp p50([]), do: nil

  defp p50(values) do
    sorted = Enum.sort(values)
    Enum.at(sorted, div(length(sorted), 2))
  end

  defp mean([]), do: nil

  defp mean(values) do
    present = Enum.reject(values, &is_nil/1)
    if present == [], do: nil, else: Enum.sum(present) / length(present)
  end

  defp add(list, _reason, false), do: list
  defp add(list, reason, true), do: list ++ [reason]

  defp default_horizon,
    do: Application.get_env(:mitos, :autonomy, []) |> Keyword.get(:horizon_ms, 8 * 60 * 60 * 1000)
end
