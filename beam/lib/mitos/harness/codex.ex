defmodule Mitos.Harness.Codex do
  @moduledoc """
  The Codex (OpenAI) adapter (01 §8.6) — **structured** observation via
  `codex exec --json`, normalized into the one event stream. Codex emits a
  Responses-API-shaped JSONL protocol (`thread.started`, `turn.started`,
  `message`, `function_call`, `function_call_output`, `turn.completed`).

  Hermeticity (05 §6): HOME + config redirected into the sandbox; the model
  key never enters the env on a hosted deployment (it rides the broker's
  forward proxy, 05 R1). For local T1 dev an operator MAY passthrough env vars
  via `config :mitos, :harness_passthrough_env`.
  """
  @behaviour Mitos.Harness
  alias Mitos.Harness.PortAdapter

  @impl true
  def argv(prompt, opts) do
    args =
      [
        "exec",
        "--json",
        "-s",
        Keyword.get(opts, :sandbox_mode, "workspace-write"),
        # mitos is the sandbox boundary (05); codex's own sandbox is redundant.
        "--dangerously-bypass-approvals-and-sandbox"
      ] ++ model_args(opts) ++ [prompt]

    {"codex", args}
  end

  @impl true
  def launch(sandbox, prompt, opts) do
    {exe, args} = argv(prompt, opts)
    env = Map.merge(hermetic_env(sandbox), Map.merge(passthrough_env(), Mitos.Harness.model_proxy_env()))
    PortAdapter.launch(exe, args, sandbox.workspace, Mitos.Hermetic.env(env))
  end

  @impl true
  def events(session), do: PortAdapter.events(session, &normalize/1)

  @impl true
  def steer(%{port: port}, :interrupt), do: PortAdapter.safe_close(port)
  def steer(_session, _command), do: {:error, :unsupported}

  @impl true
  def result(%{sandbox: sandbox}),
    do: Mitos.Capability.Result.harvest(sandbox.workspace)

  @impl true
  def hermetic_env(sandbox) do
    %{
      "HOME" => sandbox.home,
      "XDG_CONFIG_HOME" => Path.join(sandbox.home, ".config"),
      "XDG_CACHE_HOME" => Path.join(sandbox.home, ".cache"),
      "XDG_DATA_HOME" => Path.join([sandbox.home, ".local", "share"]),
      "PATH" => System.get_env("PATH", "/usr/local/bin:/usr/bin:/bin"),
      "OPENAI_LOG" => "error"
    }
  end

  @impl true
  def capabilities,
    do: %{structured_output: true, mcp_tools?: false, sessions?: true, effort_levels: ["minimal", "low", "medium", "high"]}

  # --- codex JSONL → normalized events ---

  def normalize(line) do
    case Mitos.JSON.decode(line) do
      {:ok, %{"type" => "message", "role" => role} = m} ->
        content_events(role, m["content"]) ++ usage_events(m["usage"])

      {:ok, %{"type" => "function_call", "name" => name, "arguments" => args}} ->
        [%{type: :tool_use, name: name, input: decode_args(args)}]

      {:ok, %{"type" => "function_call_output", "output" => out}} ->
        [%{type: :tool_result, content: out}]

      {:ok, %{"type" => "turn.completed"} = m} ->
        usage_events(m["usage"]) ++
          [%{type: :final_result, ok: true, subtype: "success", turns: m["turns"]}]

      {:ok, %{"type" => "turn.failed", "error" => err}} ->
        [%{type: :final_result, ok: false, subtype: "error", error: err["message"]}]

      {:ok, %{"type" => "error", "message" => msg}} ->
        [%{type: :status, status: {:error, msg}}]

      {:ok, %{"type" => other}} when other in ["thread.started", "turn.started"] ->
        [%{type: :status, status: other}]

      _ ->
        []
    end
  end

  defp content_events("assistant", content),
    do: for(%{"type" => "text", "text" => t} <- content || [], do: %{type: :assistant_text, text: t})

  defp content_events(_, _), do: []

  defp decode_args(args) when is_binary(args) do
    case Mitos.JSON.decode(args) do
      {:ok, m} -> m
      _ -> args
    end
  end

  defp decode_args(args), do: args

  defp usage_events(nil), do: []

  defp usage_events(%{} = u) do
    tokens = (u["input_tokens"] || 0) + (u["output_tokens"] || u["output"] || 0)
    if tokens > 0, do: [%{type: :token_usage, tokens: tokens, usd: u["cost"] || 0.0}], else: []
  end

  defp model_args(opts) do
    case Keyword.get(opts, :model) do
      nil -> []
      model -> ["-m", model]
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
end
