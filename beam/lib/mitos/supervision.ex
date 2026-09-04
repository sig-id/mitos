defmodule Mitos.Supervision do
  @moduledoc """
  Restart intensity (`16 §4`) — the outer bound the escalation ladder was
  missing.

  `04 §4.3`'s per-signal counters (N=3, M=2, K=2) are *first responses*. Alone
  they let a run retry indefinitely by rotating which signal trips: three
  `no_progress` climbs, then two `repeated_error` climbs, then two
  `verify_fail` climbs, and nothing has ever counted the total. The bound is
  the supervision formalism the BEAM already implements and has run at decade
  scale:

      max_restarts within max_seconds  ->  else escalate

  The mapping to OTP is exact, which is why it is the right formalism rather
  than an analogy (`16 §4`):

  | OTP | here |
  |---|---|
  | supervisor never inspects the failed child's internals | the supervisor reads evidence, never the worker's context |
  | `one_for_one` / `one_for_all` | `rewind{scope: step}` / `rewind{scope: tail}` |
  | restart intensity | restarts before a human is worth interrupting |
  | restart from a known-good `init` | rewind to the last **green** checkpoint |
  | shutdown escalates to the parent | intensity exhausted -> `escalate` -> a card |

  Intensity is enforced per **step** and per **run**; exhaustion at either level
  escalates.
  """

  @strategies [:retry_step, :rewind_to_green, :rewind_tail, :escalate]

  defstruct max_restarts: 3,
            max_seconds: 3600,
            strategy: :rewind_to_green,
            # monotonic-second stamps of restarts inside the window
            restarts: %{},
            total: []

  @type t :: %__MODULE__{}

  def strategies, do: @strategies

  @doc "Build from config, with per-capability overrides."
  def new(opts \\ []) do
    base = Application.get_env(:mitos, :supervision, [])

    %__MODULE__{
      max_restarts: opts[:max_restarts] || base[:max_restarts] || 3,
      max_seconds: opts[:max_seconds] || base[:max_seconds] || 3600,
      strategy: opts[:strategy] || base[:strategy] || :rewind_to_green
    }
  end

  @doc """
  Record a restart of `key` (a stage id). Returns `{:ok, sup}` while intensity
  remains, or `{:exceeded, level, sup}` where `level` is `:step | :run` — the
  level that ran out first, because they escalate for different reasons: a step
  that keeps failing is a wrong approach, a run that keeps failing is a wrong
  brief.
  """
  def restart(%__MODULE__{} = sup, key, now \\ nil) do
    now = now || System.monotonic_time(:second)
    sup = prune(sup, now)

    per_step = Map.get(sup.restarts, key, [])
    sup = %{sup | restarts: Map.put(sup.restarts, key, [now | per_step]), total: [now | sup.total]}

    cond do
      length(Map.fetch!(sup.restarts, key)) > sup.max_restarts -> {:exceeded, :step, sup}
      length(sup.total) > sup.max_restarts * 3 -> {:exceeded, :run, sup}
      true -> {:ok, sup}
    end
  end

  @doc "Restarts of `key` still inside the window."
  def count(%__MODULE__{} = sup, key, now \\ nil) do
    now = now || System.monotonic_time(:second)
    sup |> prune(now) |> Map.fetch!(:restarts) |> Map.get(key, []) |> length()
  end

  @doc """
  What to do about a tripped signal, given remaining intensity. The signal
  chooses the *shape* of the recovery; intensity chooses whether there is one
  left at all (`16 §4`).
  """
  def next_move(%__MODULE__{} = sup, signal, key, now \\ nil) do
    case restart(sup, key, now) do
      {:exceeded, level, sup} ->
        {{:escalate, {:intensity_exhausted, level}}, sup}

      {:ok, sup} ->
        {move_for(sup.strategy, signal), sup}
    end
  end

  # `predicate_stall` and `oscillation` mean the current state is bad, not that
  # the last action was: repairing forward from them is exactly the failure
  # 16 §0 warns about, so they always rewind regardless of strategy.
  defp move_for(_strategy, signal) when signal in [:predicate_stall, :oscillation],
    do: {:rewind, :step}

  defp move_for(:retry_step, _signal), do: {:retry, :step}
  defp move_for(:rewind_to_green, _signal), do: {:rewind, :step}
  defp move_for(:rewind_tail, _signal), do: {:rewind, :tail}
  defp move_for(:escalate, signal), do: {:escalate, signal}

  defp prune(%__MODULE__{} = sup, now) do
    cutoff = now - sup.max_seconds
    keep = &Enum.filter(&1, fn t -> t > cutoff end)

    %{
      sup
      | restarts: Map.new(sup.restarts, fn {k, v} -> {k, keep.(v)} end),
        total: keep.(sup.total)
    }
  end
end
