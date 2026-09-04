defmodule Mitos.Harness.Opencode do
  @moduledoc """
  The opencode adapter (01 §8.6) — **structured** observation via
  `opencode run --format json`. opencode emits part-shaped events
  (`step_start`, `text`, `tool_call`, `tool_result`, `step_finish`) where each
  carries a `part` with the content + per-step token/cost accounting.

  Hermeticity (05 §6): HOME + XDG redirected into the sandbox; the model key
  rides the broker's forward proxy on a hosted deployment. Local T1 dev MAY
  passthrough env vars via `config :mitos, :harness_passthrough_env`.
  """
  @behaviour Mitos.Harness
  alias Mitos.Harness.PortAdapter

  @impl true
  def argv(prompt, opts) do
    args =
      ["run", "--format", "json"] ++ model_args(opts) ++ variant_args(opts) ++ [prompt]

    {"opencode", args}
  end

  @impl true
  def launch(sandbox, prompt, opts) do
    {exe, args} = argv(prompt, opts)

    env =
      Map.merge(
        hermetic_env(sandbox),
        Map.merge(passthrough_env(), Mitos.Harness.model_proxy_env())
      )

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
      "PATH" => System.get_env("PATH", "/usr/local/bin:/usr/bin:/bin")
    }
  end

  @impl true
  def capabilities,
    do: %{
      structured_output: true,
      mcp_tools?: true,
      sessions?: true,
      effort_levels: ["minimal", "low", "high", "max"]
    }

  @impl true
  def observation do
    %{
      structured_output: :stream,
      session_artifacts: nil,
      interactive: true,
      modal_classifier: Mitos.Observation.Classifier.Heuristic,
      pty_control: true,
      resume: :none,
      think_time_p95_ms: 45_000
    }
  end

  # --- opencode JSON → normalized events ---

  def normalize(line) do
    case Mitos.JSON.decode(line) do
      {:ok, %{"type" => "text", "part" => part}} ->
        [%{type: :assistant_text, text: part["text"]}]

      {:ok, %{"type" => "tool_call", "part" => part}} ->
        [
          %{
            type: :tool_use,
            name: part["tool"] || part["name"],
            input: part["input"] || part["args"]
          }
        ]

      {:ok, %{"type" => "tool_result", "part" => part}} ->
        [%{type: :tool_result, content: part["output"], tool_use_id: part["id"]}]

      {:ok, %{"type" => "step_finish", "part" => part}} ->
        tokens = get_in(part, ["tokens", "total"]) || 0
        cost = get_in(part, ["cost"]) || 0.0
        events = if tokens > 0, do: [%{type: :token_usage, tokens: tokens, usd: cost}], else: []

        ok = part["reason"] != "error"
        events ++ [%{type: :final_result, ok: ok, subtype: part["reason"], turns: 1}]

      {:ok, %{"type" => "error", "error" => err}} ->
        [%{type: :status, status: {:error, err["message"] || err}}]

      {:ok, %{"type" => other}} when other in ["step_start", "session_start", "session_finish"] ->
        [%{type: :status, status: other}]

      _ ->
        []
    end
  end

  defp model_args(opts) do
    case Keyword.get(opts, :model) do
      nil -> []
      model -> ["--model", model]
    end
  end

  defp variant_args(opts) do
    case Keyword.get(opts, :effort) do
      nil -> []
      variant -> ["--variant", to_string(variant)]
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
