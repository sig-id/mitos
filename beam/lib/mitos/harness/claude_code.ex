defmodule Mitos.Harness.ClaudeCode do
  @moduledoc """
  The Claude Code adapter (01 §8.6) — observed on the three channels of `14 §1`.

  `launch/3` runs the headless path (`-p --output-format stream-json`), which is
  what a Market run wants: no TUI, no prompts, a machine-readable stream. A
  Studio run wants the opposite — the operator's own harness, unmodified — and
  gets the same normalized vocabulary from `normalize_session_line/1` tailing
  the CLI's own session record (C2), whose location mitos already controls
  because `hermetic_env/1` points `CLAUDE_CONFIG_DIR` into the sandbox.

  The terminal (C3) contributes modal state only, never semantics, and only as
  a digested snapshot at a quiescence edge (`14 §3`). `observation/0` declares
  all of this; the substrate reads that matrix and never probes.

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
  def argv(prompt, opts) do
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

    {"claude", args}
  end

  @impl true
  def launch(sandbox, prompt, opts) do
    exe = System.find_executable("claude")

    if is_nil(exe) do
      {:error, :claude_cli_not_found}
    else
      {_exe, args} = argv(prompt, opts)

      env =
        Map.merge(
          hermetic_env(sandbox),
          Map.merge(passthrough_env(), Mitos.Harness.model_proxy_env())
        )

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
      effort_levels: ["low", "medium", "high"],
      observation: observation()
    }
  end

  @impl true
  def observation do
    %{
      # Headless (`-p --output-format stream-json`) for a Market run...
      structured_output: :stream,
      # ...and, for a Studio run against an UNMODIFIED interactive session, a
      # tail of the CLI's own session record. `hermetic_env/1` sets
      # CLAUDE_CONFIG_DIR into the sandbox (05 §6), so mitos already knows where
      # that record lands — the whole of 14 §1.1 in one line.
      session_artifacts: %{
        path_template: "{home}/.claude/projects/{project_slug}/*.jsonl",
        format: :jsonl,
        schema_version: "1",
        tail: true
      },
      interactive: true,
      modal_classifier: Mitos.Observation.Classifier.Heuristic,
      pty_control: true,
      resume: :session_id,
      think_time_p95_ms: 60_000
    }
  end

  @impl true
  # The session record shares the assistant/user/result framing of the headless
  # stream, so one normalizer serves both sources (14 §1.1). Where a future
  # generation diverges, the contract test (14 §5) catches it before a user does.
  def normalize_session_line(line), do: normalize(line)

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
