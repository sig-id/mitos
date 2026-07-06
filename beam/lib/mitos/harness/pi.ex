defmodule Mitos.Harness.Pi do
  @moduledoc """
  The pi adapter (01 §8.6) — **structured** observation via `pi -p --mode json`.
  pi emits a session/turn protocol (`session`, `agent_start`, `turn_start`,
  `message_start`/`message_update`/`message_end`, `turn_end`) where assistant
  content streams through `assistantMessageEvent` parts (`text_start`,
  `text_delta`, tool events). Final content + usage land on `message_end`.

  Hermeticity (05 §6): HOME + the pi data/session dir redirected into the
  sandbox so no cross-run session or auto-loaded config leaks in; the model key
  rides the broker's forward proxy on a hosted deployment. Local T1 dev MAY
  passthrough env vars via `config :mitos, :harness_passthrough_env`.
  """
  @behaviour Mitos.Harness
  alias Mitos.Harness.PortAdapter

  @impl true
  def argv(prompt, opts) do
    args = ["-p", "--mode", "json"] ++ model_args(opts) ++ [prompt]
    {"pi", args}
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
      # pi's session store + project data live under this; redirect it hermetic.
      "PI_DATA_DIR" => Path.join(sandbox.home, ".pi"),
      "PATH" => System.get_env("PATH", "/usr/local/bin:/usr/bin:/bin")
    }
  end

  @impl true
  def capabilities,
    do: %{structured_output: true, mcp_tools?: true, sessions?: true, effort_levels: ["low", "medium", "high"]}

  # --- pi JSON → normalized events ---

  def normalize(line) do
    case Mitos.JSON.decode(line) do
      {:ok, %{"type" => "message_update", "assistantMessageEvent" => ae}} ->
        message_update_events(ae)

      {:ok, %{"type" => "message_end", "message" => %{"role" => "assistant"} = m}} ->
        text_events(m["content"]) ++
          usage_events(m["usage"]) ++
          [%{type: :final_result, ok: true, subtype: m["stopReason"] || "stop"}]

      {:ok, %{"type" => "turn_end"}} ->
        []

      {:ok, %{"type" => other}} when other in ["session", "agent_start", "turn_start"] ->
        [%{type: :status, status: other}]

      _ ->
        []
    end
  end

  # Streaming assistant parts. text_delta carries the incremental text; tool
  # events carry tool invocations. We emit per-delta text (the loop-manager
  # folds them) and tool_use/tool_result where present.
  defp message_update_events(%{"type" => "text_delta", "delta" => delta}) when is_binary(delta),
    do: [%{type: :assistant_text, text: delta}]

  defp message_update_events(%{"type" => "text_start", "partial" => %{"content" => content}}),
    do: text_events(content)

  defp message_update_events(%{"type" => "tool_" <> _, "tool" => tool} = ae) do
    case ae["type"] do
      t when t in ["tool_start", "tool_call"] ->
        [%{type: :tool_use, name: tool["name"], input: tool["input"]}]

      t when t in ["tool_end", "tool_result"] ->
        [%{type: :tool_result, content: tool["output"], tool_use_id: tool["id"]}]

      _ ->
        []
    end
  end

  defp message_update_events(_), do: []

  defp text_events(content) do
    for %{"type" => "text", "text" => t} <- content || [], do: %{type: :assistant_text, text: t}
  end

  defp usage_events(nil), do: []

  defp usage_events(%{} = u) do
    tokens = u["totalTokens"] || u["output"] || 0
    cost = get_in(u, ["cost", "total"]) || 0.0
    if tokens > 0, do: [%{type: :token_usage, tokens: tokens, usd: cost}], else: []
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
end
