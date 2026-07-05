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
  @callback argv(prompt :: String.t(), opts :: keyword()) :: {exe :: String.t(), args :: [String.t()]}

  @optional_callbacks [argv: 2]

  @doc """
  The model-egress proxy env for a harness (05 R1): when
  `config :mitos, :model_proxy_url` is set (e.g. `http://127.0.0.1:4648`), the
  harness's model base URL is redirected to the edge proxy so the model key is
  attached there (Vault), never in the sandbox. Returns `%{}` when unset
  (spec-clean default).
  """
  def model_proxy_env do
    reverse =
      case Application.get_env(:mitos, :model_proxy_url) do
        nil -> %{}
        url -> %{"ANTHROPIC_BASE_URL" => "#{url}/anthropic", "OPENAI_BASE_URL" => "#{url}/openai"}
      end

    # The MITM form is universal: route HTTPS through the edge + trust its CA.
    # `ca_cert_path/0` reads the mitos CA the TlsMitm proxy signs leaves with.
    mitm =
      case Application.get_env(:mitos, :mitm_proxy_url) do
        nil -> %{}
        url ->
          ca = Mitos.Broker.CertAuthority.ca_cert_path()

          %{
            "HTTPS_PROXY" => url,
            "HTTP_PROXY" => url,
            "SSL_CERT_FILE" => ca,
            "REQUESTS_CA_BUNDLE" => ca,
            "NODE_EXTRA_CA_CERTS" => ca,
            "GIT_SSL_CAINFO" => ca
          }
      end

    Map.merge(reverse, mitm)
  end
end
