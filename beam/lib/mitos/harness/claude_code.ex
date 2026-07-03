defmodule Mitos.Harness.ClaudeCode do
  @moduledoc """
  The Claude Code adapter (01 §8.6) — **structured** observation via
  `claude -p --output-format stream-json`, normalized into the one event
  stream. No PTY, no session-log tailing (09 §1 discards that path); the CLI's
  own structured output is the observation channel, re-specced per harness
  generation.

  Hermeticity (05 §6): `HOME` and `CLAUDE_CONFIG_DIR` are redirected into the
  sandbox so no cross-run config, session store, or auto-loaded project memory
  (CLAUDE.md) leaks in; the skill and composed context arrive only via the
  prompt and the injected `/skill`, `/ctx` mounts.

  Model-provider auth: per 01 §8.5 no secrets enter the sandbox env. A hosted
  deployment forces the CLI's API traffic through the broker's forward proxy
  (05 R1) which attaches the key at the edge. For local T1 dev an operator MAY
  allowlist passthrough env vars via `config :mitos, :harness_passthrough_env`
  — empty by default, so the default posture stays spec-clean.
  """
  @behaviour Mitos.Harness

  @impl true
  def launch(sandbox, prompt, opts) do
    exe = System.find_executable("claude")

    if is_nil(exe) do
      {:error, :claude_cli_not_found}
    else
      args =
        [
          "-p",
          prompt,
          "--output-format",
          "stream-json",
          "--verbose",
          "--max-turns",
          to_string(Keyword.get(opts, :max_turns, 30)),
          "--dangerously-skip-permissions"
        ] ++ model_args(opts)

      env = Map.merge(hermetic_env(sandbox), passthrough_env())

      port =
        Port.open({:spawn_executable, exe}, [
          :binary,
          :exit_status,
          :hide,
          args: args,
          cd: sandbox.workspace,
          # full ambient clear + overrides (05 §6); `false` unsets a var in ports
          env:
            Enum.map(Mitos.Hermetic.env(env), fn
              {k, nil} -> {String.to_charlist(k), false}
              {k, v} -> {String.to_charlist(k), String.to_charlist(v)}
            end)
        ])

      {:ok, %{port: port, sandbox: sandbox}}
    end
  end

  @impl true
  def events(%{port: port}) do
    Stream.resource(
      fn -> {port, ""} end,
      fn
        :halt ->
          {:halt, :closed}

        {port, buf} ->
          receive do
            {^port, {:data, data}} ->
              {lines, rest} = split_lines(buf <> data)
              {Enum.flat_map(lines, &normalize/1), {port, rest}}

            {^port, {:exit_status, status}} ->
              tail = if buf == "", do: [], else: normalize(buf)
              {tail ++ [%{type: :status, status: {:exited, status}}], :halt}
          end
      end,
      fn
        :closed -> :ok
        {port, _} -> safe_close(port)
      end
    )
  end

  @impl true
  def steer(%{port: port}, :interrupt), do: safe_close(port)
  # -p (print) mode is one-shot; other steer commands need an interactive
  # transport (PTY fallback, out of MVP scope).
  def steer(_session, _command), do: {:error, :unsupported}

  @impl true
  def result(%{sandbox: sandbox}) do
    # The harness-agnostic result channel is the RESULT artifact (03 §7).
    Mitos.Capability.Result.harvest(sandbox.workspace)
  end

  @impl true
  def hermetic_env(sandbox) do
    %{
      "HOME" => sandbox.home,
      "CLAUDE_CONFIG_DIR" => Path.join(sandbox.home, ".claude"),
      "XDG_CONFIG_HOME" => Path.join(sandbox.home, ".config"),
      "XDG_CACHE_HOME" => Path.join(sandbox.home, ".cache"),
      "PATH" => System.get_env("PATH", "/usr/local/bin:/usr/bin:/bin"),
      "DISABLE_TELEMETRY" => "1",
      "DISABLE_AUTOUPDATER" => "1"
    }
  end

  @impl true
  def capabilities do
    %{
      structured_output: true,
      mcp_tools?: true,
      sessions?: true,
      effort_levels: ["low", "medium", "high"]
    }
  end

  # --- stream-json → normalized events (01 §8.6) ---

  defp normalize(line) do
    case Mitos.JSON.decode(line) do
      {:ok, %{"type" => "assistant", "message" => msg}} ->
        content_events(msg["content"] || []) ++ usage_events(msg["usage"])

      {:ok, %{"type" => "user", "message" => msg}} ->
        for %{"type" => "tool_result"} = c <- msg["content"] || [] do
          %{type: :tool_result, content: c["content"], tool_use_id: c["tool_use_id"]}
        end

      {:ok, %{"type" => "result"} = r} ->
        usage_events(r["usage"], r["total_cost_usd"]) ++
          [
            %{
              type: :final_result,
              ok: r["subtype"] == "success",
              subtype: r["subtype"],
              text: r["result"],
              turns: r["num_turns"]
            }
          ]

      {:ok, %{"type" => "system"} = s} ->
        [%{type: :status, status: s["subtype"] || "system"}]

      _ ->
        []
    end
  end

  defp content_events(content) do
    Enum.flat_map(content, fn
      %{"type" => "text", "text" => t} -> [%{type: :assistant_text, text: t}]
      %{"type" => "tool_use"} = tu -> [%{type: :tool_use, name: tu["name"], input: tu["input"]}]
      _ -> []
    end)
  end

  defp usage_events(usage, usd \\ nil)
  defp usage_events(nil, nil), do: []

  defp usage_events(usage, usd) do
    usage = usage || %{}
    tokens = (usage["input_tokens"] || 0) + (usage["output_tokens"] || 0)
    if tokens > 0 or usd, do: [%{type: :token_usage, tokens: tokens, usd: usd || 0.0}], else: []
  end

  defp model_args(opts) do
    case Keyword.get(opts, :model) do
      nil -> []
      model -> ["--model", model]
    end
  end

  defp passthrough_env do
    Application.get_env(:mitos, :harness_passthrough_env, [])
    |> Enum.reduce(%{}, fn var, acc ->
      case System.get_env(var) do
        nil -> acc
        val -> Map.put(acc, var, val)
      end
    end)
  end

  defp split_lines(buf) do
    parts = String.split(buf, "\n")
    {rest, lines} = List.pop_at(parts, -1)
    {Enum.reject(lines, &(&1 == "")), rest || ""}
  end

  defp safe_close(port) do
    if Port.info(port), do: Port.close(port)
    :ok
  catch
    :error, :badarg -> :ok
  end
end
