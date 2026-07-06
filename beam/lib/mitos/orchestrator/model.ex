defmodule Mitos.Orchestrator.Model do
  @moduledoc """
  The orchestrator's model client seam (04 §3.4: one model call → one Decision).
  An agentic `Mitos.Orchestrator.Policy` implementation composes a decision
  prompt from the folded state + intent and asks a model for the next action;
  this behaviour abstracts the transport so the substrate is model-agnostic.

  A client returns a parsed decision map:

      %{"action" => "advance|insert|retry|switch|skip|escalate|finish|abort",
        "target" => stage_id | nil,
        "params" => %{...},        # optional, action-specific
        "rationale" => "..."}

  or `{:error, reason}` — the agentic policy falls back to deterministic
  `Linear` on any error (never blocks a run on a model outage).
  """

  @callback complete(prompt :: String.t(), opts :: keyword()) ::
              {:ok, map()} | {:error, term()}
end

defmodule Mitos.Orchestrator.Model.Stub do
  @moduledoc """
  The default model client: returns `:no_model_configured` so an agentic policy
  degrades to the deterministic `Linear` fallback. Wire a real client
  (`Model.HTTP`, or your own) via `config :mitos, :orchestrator_model`.
  """
  @behaviour Mitos.Orchestrator.Model
  @impl true
  def complete(_prompt, _opts), do: {:error, :no_model_configured}
end

defmodule Mitos.Orchestrator.Model.HTTP do
  @moduledoc """
  An OpenAI-compatible chat-completions model client (04 §3.4). Config:

      config :mitos, :orchestrator_model, Mitos.Orchestrator.Model.HTTP
      config :mitos, :orchestrator_model_url, "https://api.openai.com/v1/chat/completions"
      config :mitos, :orchestrator_model_key, {:system, "OPENAI_API_KEY"}   # or a literal

  The key is read HERE (control plane); it never enters a sandbox. The model is
  asked for a single JSON decision object; this client parses it and returns the
  map. A hosted deployment instead routes this call through the broker's forward
  proxy (05 R1) so the key attaches at the edge.
  """
  @behaviour Mitos.Orchestrator.Model

  @impl true
  def complete(prompt, opts) do
    url = config(:orchestrator_model_url, "https://api.openai.com/v1/chat/completions")
    model = opts[:model] || config(:orchestrator_model_name, "gpt-4o-mini")

    body =
      Mitos.JSON.encode!(%{
        model: model,
        messages: [
          %{role: "system", content: system_instruction()},
          %{role: "user", content: prompt}
        ],
        temperature: 0,
        response_format: %{type: "json_object"}
      })

    headers = [
      {~c"content-type", ~c"application/json"},
      {~c"authorization", String.to_charlist("Bearer " <> key())}
    ]

    case :httpc.request(:post, {String.to_charlist(url), headers, ~c"application/json", body}, [{:timeout, 60_000}], body_format: :binary) do
      {:ok, {{_, 200, _}, _resp_headers, resp_body}} ->
        parse(resp_body)

      {:ok, {{_, status, _}, _resp_headers, resp_body}} ->
        {:error, {:http, status, resp_body}}

      {:error, reason} ->
        {:error, {:transport, reason}}
    end
  end

  defp system_instruction do
    "You are the mitos orchestrator. Given the run state and the pipeline intent, " <>
      "decide the SINGLE next action. Respond with JSON: " <>
      ~s({"action": "advance|insert|retry|switch|skip|escalate|finish|abort", ) <>
      ~s("target": <stage_id or null>, "params": {}, "rationale": "..."}. ) <>
      "Use 'finish' when the goal is met, 'abort' on unrecoverable failure or budget exhaustion."
  end

  defp parse(resp_body) do
    with {:ok, %{"choices" => [%{"message" => %{"content" => content}} | _]}} <-
           Mitos.JSON.decode(resp_body),
         {:ok, json_str} <- extract_json(content),
         {:ok, decision} <- Mitos.JSON.decode(json_str) do
      {:ok, decision}
    else
      _ -> {:error, :bad_model_response}
    end
  end

  # The model may wrap JSON in prose or fences; pull the first {...} block.
  defp extract_json(content) do
    case Regex.run(~r/\{[\s\S]*\}/, content) do
      [json] -> {:ok, json}
      _ -> {:error, :no_json}
    end
  end

  defp key do
    case config(:orchestrator_model_key, nil) do
      {:system, var} -> System.get_env(var) || ""
      k when is_binary(k) -> k
      nil -> ""
    end
  end

  defp config(k, default), do: Application.get_env(:mitos, k, default)
end
