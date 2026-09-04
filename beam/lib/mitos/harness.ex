defmodule Mitos.Harness do
  @moduledoc """
  The harness adapter contract (01 §8.6, elaborated 03 §5 / 05 §5.2).

  Mitos wraps coding-agent harnesses and never hard-codes one (01 §1.4). The
  adapter owns launching the harness process inside a prepared sandbox,
  normalizing its output into the one event stream the loop-manager consumes,
  steering, and **hermeticity** — redirecting/neutralizing the harness's native
  cross-run memory (HOME, config, session store, auto-loaded project memory)
  unless the capability opts into reuse (05 §6).

  Normalized events are maps with a `:type` of:

      :assistant_text | :tool_use | :tool_result | :token_usage | :status | :final_result

  This supersedes the old PTY/session-log screen-scraping path; PTY is a
  fallback codec, out of MVP scope (01 §8.6, 09 §2).
  """

  @type session :: term()
  @type event :: %{required(:type) => atom(), optional(atom()) => term()}

  @doc """
  Launch the harness inside the sandbox with a composed prompt. `sandbox` is
  the provider's prepared layout (workspace/cap/skill/home paths + broker
  endpoint/token). `env` MUST carry non-secret config only (05 §5.1).
  """
  @callback launch(sandbox :: map(), prompt :: String.t(), opts :: keyword()) ::
              {:ok, session} | {:error, term()}

  @doc "The normalized event stream for one harness invocation (consumed to completion per iteration)."
  @callback events(session) :: Enumerable.t()

  @doc "Steer: approve | answer | interrupt | select | prompt (05 §5.2, mitos-initiated)."
  @callback steer(session, command :: term()) :: :ok | {:error, term()}

  @doc "Structured final result of the invocation (after events/1 is drained)."
  @callback result(session) :: map()

  @doc """
  The hermetic environment for this harness (05 §6): HOME + config redirected
  to ephemeral sandbox paths, no ambient credentials. Providers merge this into
  the process env at start.
  """
  @callback hermetic_env(sandbox :: map()) :: %{String.t() => String.t()}

  @doc "Adapter descriptor: %{structured_output, mcp_tools?, sessions?, effort_levels, ...}."
  @callback capabilities() :: map()

  @doc """
  The non-interactive argv for this harness given a prompt + opts, WITHOUT
  spawning: `{exe, args}`. Lets a `SandboxProvider` (e.g. Nucleus) run the CLI
  as the inner command of its own sandbox while the adapter still owns
  normalization of the passthrough stream (01 §8.6).
  """
  @callback argv(prompt :: String.t(), opts :: keyword()) ::
              {exe :: String.t(), args :: [String.t()]}

  @doc """
  The **observation matrix** (14 §4): what can be observed about this harness.
  The substrate computes the observable set from this and refuses configurations
  it cannot observe — it never probes and never assumes.

      %{
        structured_output : :stream | :artifact | :none,
        session_artifacts : %{path_template, format, schema_version, tail} | nil,
        interactive       : boolean,
        modal_classifier  : module | nil,
        pty_control       : boolean,
        resume            : :session_id | :none,
        think_time_p95_ms : integer          # seeds T_block (14 §2.1)
      }
  """
  @callback observation() :: map

  @doc """
  Normalize one line of the harness's session artifact into the `01 §8.6`
  vocabulary (C2, 14 §1.1). A session record is a different shape from a
  headless stdout frame even where they rhyme, so the adapter owns both.
  """
  @callback normalize_session_line(line :: String.t()) :: [map]

  @optional_callbacks [argv: 2, normalize_session_line: 1]

  @doc """
  The matrix for an adapter, with defaults filled in. An adapter that predates
  `14` declares nothing and is treated as headless-only — the conservative
  reading, since claiming an unverified channel is how a run goes blind.
  """
  def observation(mod) do
    declared =
      try do
        mod.observation()
      rescue
        UndefinedFunctionError -> %{}
      end

    Map.merge(
      %{
        structured_output: :stream,
        session_artifacts: nil,
        interactive: false,
        modal_classifier: nil,
        pty_control: false,
        resume: :none,
        think_time_p95_ms: 30_000
      },
      declared
    )
  end

  @doc """
  Which channels (14 §1) are available for this adapter, and whether the
  resulting posture may run unattended (16 §6).

  A harness observable only through C1+C3 may be watched by a human but MUST NOT
  be granted an unattended horizon, and MUST NOT serve a Market run — a buyer's
  proof cannot be built from a screen (14 §4).
  """
  def channels(mod) do
    o = observation(mod)
    c2 = o.structured_output == :stream or match?(%{}, o.session_artifacts)
    c3 = o.interactive and o.pty_control

    %{
      c1: true,
      c2: c2,
      c3: c3,
      unattended_ok: c2,
      market_ok: c2,
      reason: if(c2, do: nil, else: :no_structured_channel)
    }
  end

  @doc """
  `T_block` for this harness (14 §2.1). Derived from observed think-time, never
  guessed: a harness that legitimately thinks for minutes must not be reported
  as blocked on a human at 45 seconds.
  """
  def block_threshold(mod) do
    o = observation(mod)
    max(o.think_time_p95_ms * 2, Mitos.Observation.Clock.thresholds().block)
  end

  @doc """
  The model-egress proxy env for a harness (05 R1): the harness's model base
  URL is redirected to the edge proxy so the model key is attached there
  (Vault), never in the sandbox. Each proxy's URL is an explicit `_url` if set,
  otherwise **derived from its enabled `_port`** — so starting `ModelProxy`
  (`:model_proxy_port`) or `TlsMitm` (`:mitm_proxy_port`) auto-wires the harness
  env. Returns `%{}` when neither URL nor port is set (spec-clean default).
  """
  def model_proxy_env do
    # Reverse-proxy form (base-URL override — no TLS interception). NOTE: pi
    # ignores ANTHROPIC_BASE_URL; the pi adapter writes a models.json baseUrl
    # override instead (it DOES respect that). Other harnesses read these env
    # vars directly. Either way, the Vault key is attached at ModelProxy.
    reverse =
      case reverse_proxy_url() do
        nil -> %{}
        url -> %{"ANTHROPIC_BASE_URL" => "#{url}/anthropic", "OPENAI_BASE_URL" => "#{url}/openai"}
      end

    # The MITM form is universal (any HTTPS client, no base-URL override needed):
    # route HTTPS through the edge + trust its CA. `ca_cert_path/0` reads the
    # mitos CA the TlsMitm proxy signs leaves with. NO_PROXY keeps loopback off
    # the MITM so reverse-proxy base-URL traffic (→ 127.0.0.1:<model_proxy_port>)
    # goes direct when both forms are active.
    mitm =
      case mitm_proxy_url() do
        nil ->
          %{}

        url ->
          ca = Mitos.Broker.CertAuthority.ca_cert_path()

          %{
            "HTTPS_PROXY" => url,
            "HTTP_PROXY" => url,
            "NO_PROXY" => "127.0.0.1,localhost",
            "no_proxy" => "127.0.0.1,localhost",
            "SSL_CERT_FILE" => ca,
            "REQUESTS_CA_BUNDLE" => ca,
            "NODE_EXTRA_CA_CERTS" => ca,
            "GIT_SSL_CAINFO" => ca
          }
      end

    Map.merge(reverse, mitm)
  end

  @doc "The resolved reverse-proxy base URL (loopback + :model_proxy_port), or nil if disabled."
  def reverse_proxy_url, do: proxy_url(:model_proxy_url, :model_proxy_port)

  @doc "The resolved TLS-MITM proxy URL (loopback + :mitm_proxy_port), or nil if disabled."
  def mitm_proxy_url, do: proxy_url(:mitm_proxy_url, :mitm_proxy_port)

  # Explicit `_url` wins; otherwise derive from the enabled `_port` (loopback —
  # both proxies bind to {127,0,0,1}). nil when neither is set / port is 0.
  defp proxy_url(url_key, port_key) do
    case Application.get_env(:mitos, url_key) do
      nil ->
        case Application.get_env(:mitos, port_key, 0) do
          port when is_integer(port) and port > 0 -> "http://127.0.0.1:#{port}"
          _ -> nil
        end

      url ->
        url
    end
  end
end
