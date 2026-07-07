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
    # Path R (reverse proxy, 05 R1): pi ignores ANTHROPIC_BASE_URL, so to route
    # its model calls through ModelProxy we write a models.json provider
    # baseUrl override + a placeholder apiKey into the hermetic .pi. The
    # placeholder satisfies pi's startup auth check; ModelProxy strips it and
    # attaches the real Vault key. No-op under pi_dev_auth (operator's real
    # ~/.pi), with the reverse proxy disabled, or with nothing seeded.
    prepare_proxy_models(sandbox)

    {exe, args} = argv(prompt, opts)

    env =
      hermetic_env(sandbox)
      |> Map.merge(placeholder_env_for_mitm())
      |> Map.merge(passthrough_env())
      |> Map.merge(Mitos.Harness.model_proxy_env())

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
    # pi refuses to start without a key present ("No API key found"). Two ways
    # that key reaches pi without entering the sandbox as a real secret:
    #   • pi_dev_auth (local T1 dev): point pi at the operator's real
    #     ~/.pi/agent so it reads their auth.json and boots with their key.
    #   • the edge proxy (default + prod): prepare_proxy_models/1 writes a
    #     models.json with a placeholder apiKey at <sandbox>/.pi/agent — pi boots
    #     on the placeholder and its calls route through ModelProxy/TlsMitm,
    #     which strip it and attach the real Vault key.
    #
    # pi locates that dir via PI_CODING_AGENT_DIR (from
    # `${APP_NAME.toUpperCase()}_CODING_AGENT_DIR`), defaulting to
    # $HOME/<configDir>/agent. It does NOT read PI_DATA_DIR — that was a no-op,
    # so in dev pi fell back to the empty sandbox $HOME/.pi/agent and died with
    # "No API key found". Point PI_CODING_AGENT_DIR explicitly in both modes:
    # the operator's agent dir in dev, the sandbox agent dir (where
    # prepare_proxy_models writes models.json) otherwise.
    agent_dir =
      if Application.get_env(:mitos, :pi_dev_auth, false) do
        Path.expand("~/.pi/agent")
      else
        Path.join(sandbox.home, ".pi/agent")
      end

    %{
      "HOME" => sandbox.home,
      "XDG_CONFIG_HOME" => Path.join(sandbox.home, ".config"),
      "XDG_CACHE_HOME" => Path.join(sandbox.home, ".cache"),
      "XDG_DATA_HOME" => Path.join([sandbox.home, ".local", "share"]),
      "PI_CODING_AGENT_DIR" => agent_dir,
      "PATH" => System.get_env("PATH", "/usr/local/bin:/usr/bin:/bin")
    }
  end

  @impl true
  def capabilities,
    do: %{
      structured_output: true,
      mcp_tools?: true,
      sessions?: true,
      effort_levels: ["low", "medium", "high"]
    }

  # --- pi JSON → normalized events ---

  def normalize(line) do
    case Mitos.JSON.decode(line) do
      {:ok, %{} = frame} -> normalize_frame(frame)
      _ -> []
    end
  end

  @doc """
  Normalize an already-decoded pi protocol frame (a map) into events.

  Public so the trace read-model can re-hydrate legacy trace files (whose raw
  pi frames were persisted verbatim as `:stderr` blobs by the pre-fix adapter)
  through the same mapping used for a live run.

  pi's session/turn protocol (captured from `pi -p --mode json` over glm-5.2):

    * `session` / `agent_start` / `turn_start`   → status (turn_start opens a turn)
    * `message_update` `thinking_delta`           → assistant text (reasoning)
    * `message_update` `text_delta`               → assistant text (streamed answer)
    * `message_update` other (toolcall_*, *_start/_end) → ignored; the parsed
      tool call + result arrive on the `tool_execution_*` frames below, which
      carry the full arguments/output rather than the streaming partials.
    * `tool_execution_start` {toolName, args}     → tool_use
    * `tool_execution_end` {toolName, result, isError} → tool_result
    * `turn_end` {message: assistant w/ usage + stopReason + content}
        → assistant text (from content text parts), token_usage, final_result
    * `message_start` / `message_end` / `turn_end`'s thinking parts / etc. → ignored

  Older pi builds (and some providers) put the assistant message on
  `message_end` instead; we still honor that as a fallback so the adapter
  doesn't go silent on a protocol shift.
  """
  def normalize_frame(%{"type" => "message_update", "assistantMessageEvent" => ae}) do
    message_update_events(ae)
  end

  def normalize_frame(%{"type" => "tool_execution_start", "toolName" => name, "args" => args} = f) do
    [%{type: :tool_use, name: name, input: args || %{}, tool_use_id: f["toolCallId"]}]
  end

  def normalize_frame(%{"type" => "tool_execution_end"} = f) do
    [%{type: :tool_result, content: tool_result_text(f["result"]), tool_use_id: f["toolCallId"],
       name: f["toolName"], isError: f["isError"] == true}]
  end

  def normalize_frame(%{"type" => "turn_end", "message" => %{} = m}) do
    text_parts(m["content"]) ++
      usage_events(m["usage"]) ++
      [%{type: :final_result, ok: true, subtype: m["stopReason"] || "stop"}]
  end

  # Fallback for pi builds that finalize the assistant message on message_end
  # rather than turn_end.
  def normalize_frame(%{"type" => "message_end", "message" => %{"role" => "assistant"} = m}) do
    text_parts(m["content"]) ++
      usage_events(m["usage"]) ++
      [%{type: :final_result, ok: true, subtype: m["stopReason"] || "stop"}]
  end

  def normalize_frame(%{"type" => other}) when other in ["session", "agent_start", "turn_start"] do
    [%{type: :status, status: other}]
  end

  def normalize_frame(_), do: []

  # Streaming assistant parts. Reasoning (glm/Claude thinking) and answer text
  # stream as *_delta; we emit per-delta and let Runs.trace coalesce a run into
  # one block. The streaming toolcall_* partials are ignored — tool_execution_*
  # carries the parsed call and its result.
  defp message_update_events(%{"type" => "thinking_delta", "delta" => delta}) when is_binary(delta),
    do: [%{type: :assistant_text, text: delta, thinking: true}]

  defp message_update_events(%{"type" => "text_delta", "delta" => delta}) when is_binary(delta),
    do: [%{type: :assistant_text, text: delta}]

  defp message_update_events(_), do: []

  # Extract the answer prose from a finalized assistant message. We take only
  # `text` parts (NOT `thinking` — reasoning already streamed via
  # thinking_delta, and echoing it here would duplicate).
  defp text_parts(content) do
    for %{"type" => "text", "text" => t} <- content || [], do: %{type: :assistant_text, text: t}
  end

  # pi ships tool results as %{content: [%{type: "text", text: ...}, ...]}; join
  # the text parts so the trace shows the output the way the model saw it.
  defp tool_result_text(%{"content" => content}) do
    for %{"type" => "text", "text" => t} <- content || [], into: "", do: t
  end

  defp tool_result_text(other), do: other

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

  # --- edge-proxy wiring (Path R reverse, Path M MITM) ---

  # Sentinel that satisfies pi's startup auth check. ModelProxy (reverse) drops
  # every credential header it doesn't keep, and TlsMitm strips x-api-key /
  # api-key / Authorization, so this never reaches an upstream — the real key is
  # attached at the edge from the Vault.
  @placeholder_key "via-mitos-edge"

  @doc """
  Write a models.json into the sandbox's hermetic .pi so pi routes its model
  calls through the reverse proxy (Path R, 05 R1). Idempotent no-op under
  pi_dev_auth, with the reverse proxy off, or with no seeded providers. Public
  so the wiring is testable without spawning pi.
  """
  def prepare_proxy_models(sandbox) do
    with false <- Application.get_env(:mitos, :pi_dev_auth, false),
         base when is_binary(base) <- Mitos.Harness.reverse_proxy_url(),
         [_ | _] = providers <- Mitos.Broker.ModelCredentials.seeded_providers() do
      path = Path.join([sandbox.home, ".pi", "agent", "models.json"])
      File.mkdir_p!(Path.dirname(path))
      File.write!(path, Mitos.JSON.encode!(proxy_models(base, providers)) <> "\n")
      :ok
    else
      _ -> :ok
    end
  end

  # pi reads ~/.pi/agent/models.json (PI_CODING_AGENT_DIR, which we set to
  # <sandbox>/.pi/agent) and DOES respect a provider baseUrl override there
  # (unlike the ANTHROPIC_BASE_URL env var). Pointing each seeded provider at
  # `<reverse_url>/<dest><prefix>` routes its calls through ModelProxy; the
  # placeholder apiKey boots pi. The prefix reconstructs providers whose real
  # base carries a path prefix (zai → /api/paas/v4).
  defp proxy_models(base, providers) do
    %{
      "providers" =>
        Map.new(providers, fn p ->
          prefix =
            case Mitos.Broker.ModelCredentials.upstream_base(p) do
              {_host, pref} -> pref || ""
              nil -> ""
            end

          {p, %{"baseUrl" => "#{base}/#{p}#{prefix}", "apiKey" => @placeholder_key}}
        end)
    }
  end

  # Path M fallback: if the MITM proxy is engaged but the reverse proxy is NOT
  # (so no models.json was written), inject a placeholder key so pi boots. Its
  # calls then go direct to the upstream over HTTPS, intercepted by TlsMitm,
  # which strips the placeholder and attaches the Vault key. No-op when the
  # reverse proxy is engaged (models.json boots pi), under pi_dev_auth, or with
  # MITM disabled. Merged before passthrough so a real passthrough key wins.
  defp placeholder_env_for_mitm do
    cond do
      Application.get_env(:mitos, :pi_dev_auth, false) -> %{}
      is_nil(Mitos.Harness.mitm_proxy_url()) -> %{}
      not is_nil(Mitos.Harness.reverse_proxy_url()) -> %{}
      true -> %{"ANTHROPIC_API_KEY" => @placeholder_key, "OPENAI_API_KEY" => @placeholder_key}
    end
  end
end
