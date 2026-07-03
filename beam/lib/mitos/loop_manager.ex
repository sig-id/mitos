defmodule Mitos.LoopManager do
  @moduledoc """
  The intra-step deterministic FSM (04 §4). Drives the iterations of ONE
  capability the orchestrator opened; composes each iteration's context
  deterministically (04 §4.2), watches deterministic signals (04 §4.3), and
  emits Effects `caused_by` the opening Decision.

  MVP posture (09 §3): **one iteration, no ESCALATE-CHOICE model call** — the
  signal computation and the ladder seam are here, but with
  `max_iterations: 1` a step is a single harness invocation followed by
  harvest. The `switch`/`escalate` Decision emission plugs into the same
  `append` funnel when the ladder goes live.
  """

  @doc """
  Run the iterations of one capability step.

    * `provider`/`handle` — the prepared sandbox (05 §2)
    * `prompt` — iteration-1 context, composed by the caller (original intent, verbatim)
    * `env` — non-secret launch env (lease token, model id, …)
    * `append` — fun(fields) that appends an Effect via the run's single writer
    * `trace` — optional path; the normalized harness stream is persisted there
      as JSONL. This is the **inner zoom** of the two-zoom watch (11 §4): a
      human observability surface, never an orchestrator input (04 §3.1).

  Returns `%{iterations, tokens, usd, wall_ms, ok, events}`.
  """
  def run_step(%{provider: provider, handle: handle, prompt: prompt, env: env, append: append} = step) do
    max_iterations = step[:max_iterations] || config(:max_iterations, 1)
    t0 = System.monotonic_time(:millisecond)

    result =
      Enum.reduce_while(1..max_iterations, %{iterations: 0, tokens: 0, usd: 0.0, ok: false, events: []}, fn i, acc ->
        append.(type: :iteration_started, payload: %{"iteration" => i})

        {events, iter} = iterate(provider, handle, next_ctx(prompt, acc, i), env)
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

        acc = %{
          acc
          | iterations: i,
            tokens: acc.tokens + iter.tokens,
            usd: acc.usd + iter.usd,
            ok: iter.ok,
            events: acc.events ++ events
        }

        # 04 §4.1: success criteria met / harness final_result ⇒ done. The
        # escalation signals (§4.3) would gate the other exits; with the MVP's
        # single iteration the reduce ends either way.
        if iter.ok or i == max_iterations do
          {:halt, acc}
        else
          {:cont, acc}
        end
      end)

    Map.put(result, :wall_ms, System.monotonic_time(:millisecond) - t0)
  end

  # One harness invocation, stream drained to completion (05 §2 stream).
  defp iterate(provider, handle, prompt, env) do
    case provider.start(handle, [prompt], env) do
      {:ok, session} ->
        events = provider.stream(session) |> Enum.to_list()

        usage =
          events
          |> Enum.filter(&(&1.type == :token_usage))
          |> Enum.reduce(%{tokens: 0, usd: 0.0}, fn e, acc ->
            %{tokens: acc.tokens + (e[:tokens] || 0), usd: acc.usd + (e[:usd] || 0.0)}
          end)

        final = Enum.find(events, &(&1.type == :final_result))

        {events, %{tokens: usage.tokens, usd: usage.usd, ok: (final && final[:ok]) || false}}

      {:error, reason} ->
        {[%{type: :status, status: {:launch_failed, reason}}],
         %{tokens: 0, usd: 0.0, ok: false}}
    end
  end

  # 04 §4.2: next_ctx = original_intent (verbatim, never summarized away)
  #        ⊕ running trace ⊕ last result. Iteration 1 is the intent alone;
  # compaction is conditional and out of MVP scope (context never grows past
  # one iteration here).
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
  defp sanitize(%{} = m), do: Map.new(m, fn {k, v} -> {k, sanitize(v)} end)
  defp sanitize(l) when is_list(l), do: Enum.map(l, &sanitize/1)
  defp sanitize(t) when is_tuple(t), do: inspect(t)
  defp sanitize(other), do: other
end
