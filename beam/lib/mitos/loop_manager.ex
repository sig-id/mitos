defmodule Mitos.LoopManager do
  @moduledoc """
  The intra-step deterministic FSM (04 §4). Drives the iterations of ONE
  capability the orchestrator opened; composes each iteration's context
  deterministically (04 §4.2), watches deterministic signals (04 §4.3), and
  emits Effects `caused_by` the opening Decision.

  Eight signals now, and the split between them matters (`16 §2`). The first six
  are computed at **iteration boundaries**. The last two are not, and that is
  the point:

    * `blocked_on_human` runs off the quiescence clock (`14 §2`), independently
      of the loop. An interactive harness parked at its own permission prompt
      never returns, so no iteration boundary is ever reached and not one of the
      other six can fire. Without this signal the run hangs until a person
      notices, and every unattended claim dies there.
    * `predicate_stall` runs off the acceptance predicate (`16 §3.2`), because
      `no_progress` is diff-based and an agent that keeps editing always looks
      busy.

  The ladder is bounded by restart intensity (`Mitos.Supervision`, `16 §4`), not
  by the per-signal counters alone — those are first responses, and a run can
  otherwise retry forever by rotating which signal trips.
  """

  alias Mitos.Observation.{Clock, Session}

  @doc """
  Run the iterations of one capability step.

    * `provider`/`handle` — the prepared sandbox (05 §2)
    * `prompt` — iteration-1 context, composed by the caller (original intent, verbatim)
    * `env` — non-secret launch env (lease token, model id, …)
    * `append` — fun(fields) that appends an Effect via the run's single writer
    * `trace` — optional path; the normalized harness stream is persisted there
      as JSONL. This is the **inner zoom** of the two-zoom watch (11 §4): a
      human observability surface, never an orchestrator input (04 §3.1).
    * `on_blocked` — fun(window) called once per quiet window when `T_block`
      elapses (`16 §2`); the caller raises the decision card.

  Returns `%{iterations, tokens, usd, wall_ms, ok, events, signals}`.
  """
  def run_step(%{provider: provider, handle: handle, prompt: prompt, env: env, append: append} = step) do
    max_iterations = step[:max_iterations] || config(:max_iterations, 1)
    t0 = System.monotonic_time(:millisecond)

    {:ok, clock} = start_clock(step, append)
    session = start_session(step, clock)

    try do
      result =
        Enum.reduce_while(1..max_iterations, initial_acc(), fn i, acc ->
          append.(type: :iteration_started, payload: %{"iteration" => i})

          {events, iter} = iterate(provider, handle, next_ctx(prompt, acc, i), env, clock)
          persist_trace(step[:trace], i, events)

          append.(
            type: :iteration_finished,
            payload: %{
              "iteration" => i,
              "tokens" => iter.tokens,
              "usd" => iter.usd,
              "ok" => iter.ok
            }
          )

          acc = accumulate(acc, i, iter, events, step)

          cond do
            # 04 §4.1: success criteria met / harness final_result ⇒ done.
            iter.ok ->
              {:halt, acc}

            acc.signals != [] ->
              Enum.each(acc.signals, fn s ->
                append.(type: :escalation_raised, payload: %{"signal" => to_string(s), "iteration" => i})
              end)

              {:halt, acc}

            i == max_iterations ->
              {:halt, acc}

            true ->
              {:cont, acc}
          end
        end)

      result
      |> Map.put(:wall_ms, System.monotonic_time(:millisecond) - t0)
      |> Map.put(:session_events, session_events(session))
    after
      stop(session)
      stop(clock)
    end
  end

  @doc """
  The deterministic signals (04 §4.3), computed from Effects + workspace diffs +
  the token meter. No model call — that is the whole point of the FSM half of
  the control model.
  """
  def signals(acc, step) do
    []
    |> add(:no_progress, no_progress?(acc, step))
    |> add(:repeated_error, repeated_error?(acc, step))
    |> add(:oscillation, oscillation?(acc))
    |> add(:turn_exhaustion, turn_exhaustion?(acc))
    |> add(:budget_burn, budget_burn?(acc, step))
    |> add(:predicate_stall, predicate_stall?(acc, step))
  end

  # --- observation lifecycle ---

  # The clock is the step's heartbeat: every consumer of a quiescence edge
  # (snapshot timing, steer window, blocked_on_human) reads this one process.
  defp start_clock(step, append) do
    Clock.start_link(
      run_id: step[:run_id],
      stage_id: step[:stage_id],
      thresholds: step[:thresholds] || %{},
      on_effect: append,
      on_block: step[:on_blocked]
    )
  end

  # C2 (14 §1.1): where the adapter declares a session artifact, tail it. In a
  # headless run this is redundant with the stream; in an interactive one it is
  # the only structured channel there is.
  defp start_session(step, clock) do
    with mod when not is_nil(mod) <- harness_mod(step),
         sandbox when is_map(sandbox) <- sandbox_view(step),
         {:ok, pid} <-
           Session.start_link(
             harness_mod: mod,
             sandbox: sandbox,
             clock: clock,
             run_id: step[:run_id],
             stage_id: step[:stage_id]
           ) do
      pid
    else
      _ -> nil
    end
  end

  defp session_events(nil), do: []

  defp session_events(session) do
    Session.sync(session)
  catch
    :exit, _ -> []
  end

  defp stop(nil), do: :ok

  defp stop(pid) do
    if Process.alive?(pid), do: GenServer.stop(pid, :normal, 1_000)
    :ok
  catch
    :exit, _ -> :ok
  end

  defp harness_mod(step) do
    get_in(step, [:handle, Access.key(:spec, %{}), Access.key(:harness_mod)])
  rescue
    _ -> nil
  end

  defp sandbox_view(step) do
    case step[:handle] do
      %{workspace: ws, home: home} -> %{workspace: ws, home: home}
      _ -> nil
    end
  end

  # --- iteration ---

  defp initial_acc,
    do: %{iterations: 0, tokens: 0, usd: 0.0, ok: false, events: [], signals: [], history: []}

  # One harness invocation, stream drained to completion (05 §2 stream). Every
  # normalized event is activity: it resets the quiescence window, which is what
  # keeps a thinking harness from reading as blocked (14 §2).
  defp iterate(provider, handle, prompt, env, clock) do
    case provider.start(handle, [prompt], env) do
      {:ok, session} ->
        events =
          provider.stream(session)
          |> Stream.each(fn e -> bump(clock, e) end)
          |> Enum.to_list()

        usage =
          events
          |> Enum.filter(&(&1.type == :token_usage))
          |> Enum.reduce(%{tokens: 0, usd: 0.0}, fn e, acc ->
            %{tokens: acc.tokens + (e[:tokens] || 0), usd: acc.usd + (e[:usd] || 0.0)}
          end)

        final = Enum.find(events, &(&1.type == :final_result))

        {events,
         %{
           tokens: usage.tokens,
           usd: usage.usd,
           ok: (final && final[:ok]) || false,
           final: final
         }}

      {:error, reason} ->
        {[%{type: :status, status: {:launch_failed, reason}}],
         %{tokens: 0, usd: 0.0, ok: false, final: nil}}
    end
  end

  defp bump(clock, %{type: :tool_use} = e), do: Clock.bump(clock, :exec, e[:name] && [e.name, inspect(e[:input])])
  defp bump(clock, _e), do: Clock.bump(clock, :session, 1)

  defp accumulate(acc, i, iter, events, step) do
    acc = %{
      acc
      | iterations: i,
        tokens: acc.tokens + iter.tokens,
        usd: acc.usd + iter.usd,
        ok: iter.ok,
        events: acc.events ++ events,
        history: acc.history ++ [snapshot_of(iter, events, step)]
    }

    %{acc | signals: signals(acc, step)}
  end

  # The per-iteration read the signals are computed from: what changed, what
  # broke, and what the predicate said.
  defp snapshot_of(iter, events, step) do
    %{
      ok: iter.ok,
      final: iter.final,
      errors: Enum.map(error_events(events), &error_class/1),
      diff_sha: step[:diff_sha] && step.diff_sha.(),
      score: step[:score] && step.score.(),
      tokens: iter.tokens,
      usd: iter.usd
    }
  end

  defp error_events(events) do
    Enum.filter(events, fn e ->
      e.type == :tool_result and error_text?(e[:content])
    end) ++ Enum.filter(events, &(&1.type == :stderr))
  end

  defp error_text?(content) when is_binary(content),
    do: Regex.match?(~r/\b(error|exception|traceback|failed)\b/i, content)

  defp error_text?(_), do: false

  defp error_class(%{type: :stderr, line: line}), do: String.slice(to_string(line), 0, 120)
  defp error_class(%{content: c}) when is_binary(c), do: String.slice(c, 0, 120)
  defp error_class(_), do: "unknown"

  # --- signal predicates (04 §4.3) ---

  defp no_progress?(acc, step) do
    n = step[:no_progress_n] || config(:no_progress_n, 3)
    recent = Enum.take(acc.history, -n)

    length(recent) >= n and
      Enum.all?(recent, &(&1.diff_sha != nil)) and
      recent |> Enum.map(& &1.diff_sha) |> Enum.uniq() |> length() == 1
  end

  defp repeated_error?(acc, step) do
    m = step[:repeated_error_m] || config(:repeated_error_m, 2)
    recent = acc.history |> Enum.take(-m) |> Enum.map(& &1.errors)

    length(recent) >= m and
      Enum.all?(recent, &(&1 != [])) and
      recent |> Enum.map(&List.first/1) |> Enum.uniq() |> length() == 1
  end

  # An edit/revert cycle: the workspace returns to a state it already held.
  defp oscillation?(acc) do
    shas = acc.history |> Enum.map(& &1.diff_sha) |> Enum.reject(&is_nil/1)
    length(shas) >= 3 and length(Enum.uniq(shas)) < length(shas) and List.last(shas) in Enum.drop(shas, -2)
  end

  defp turn_exhaustion?(acc) do
    case List.last(acc.history) do
      %{final: %{subtype: "error_max_turns"}} -> true
      %{final: %{turns: t}} when is_integer(t) and t > 0 -> false
      _ -> false
    end
  end

  defp budget_burn?(acc, step) do
    case step[:remaining] do
      %{usd: remaining} when is_number(remaining) -> acc.usd > remaining
      _ -> false
    end
  end

  # 16 §2: the predicate has not moved for K segments, whatever the diff volume
  # or the narration says. This is the only progress claim that cannot be
  # produced by editing in circles.
  defp predicate_stall?(acc, step) do
    k = step[:predicate_stall_k] || config(:predicate_stall_k, 3)
    scores = acc.history |> Enum.map(& &1.score) |> Enum.reject(&is_nil/1) |> Enum.take(-k)
    epsilon = step[:predicate_epsilon] || config(:predicate_epsilon, 1.0e-9)

    length(scores) >= k and
      scores |> Enum.chunk_every(2, 1, :discard) |> Enum.all?(fn [a, b] -> abs(b - a) <= epsilon end)
  end

  defp add(list, _signal, false), do: list
  defp add(list, signal, true), do: list ++ [signal]

  # 04 §4.2: next_ctx = original_intent (verbatim, never summarized away)
  #        ⊕ running trace ⊕ last result. Iteration 1 is the intent alone;
  # compaction is conditional and out of MVP scope.
  defp next_ctx(prompt, _acc, 1), do: prompt

  defp next_ctx(prompt, acc, _i) do
    trace =
      acc.events
      |> Enum.filter(&(&1.type == :assistant_text))
      |> Enum.map_join("\n", & &1[:text])
      |> String.slice(0, 4000)

    prompt <> "\n\n## Previous iteration trace (continue where it left off)\n" <> trace
  end

  defp config(key, default) do
    Application.get_env(:mitos, :loop_manager, []) |> Keyword.get(key, default)
  end

  # --- inner trace persistence (11 §4) ---

  defp persist_trace(nil, _i, _events), do: :ok

  defp persist_trace(path, iteration, events) do
    File.mkdir_p!(Path.dirname(path))

    lines =
      Enum.map(events, fn e ->
        e
        |> Map.put(:iteration, iteration)
        |> sanitize()
        |> Mitos.JSON.encode!()
        |> Kernel.<>("\n")
      end)

    File.write!(path, lines, [:append])
  end

  # Normalized events may carry non-JSON terms (e.g. an exit-status tuple).
  defp sanitize(%{} = m), do: Map.new(m, fn {k, v} -> {sanitize(k), sanitize(v)} end)
  defp sanitize(l) when is_list(l), do: Enum.map(l, &sanitize/1)
  defp sanitize(t) when is_tuple(t), do: inspect(t)
  defp sanitize(other), do: other
end
