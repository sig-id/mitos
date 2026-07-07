defmodule Mitos.Broker.ModelCredentials do
  @moduledoc """
  The single source for the model-provider → upstream-host mapping AND the
  boot-time seeding of the Vault (01 §8.5, 05 R1). The real model key NEVER
  enters a sandbox; it lives only in the `Vault` on the control plane and is
  attached to outbound model calls by `ModelProxy` (reverse) or `TlsMitm`
  (TLS intercept).

  ## Seeding sources (priority order)

    * `config :mitos, :app_manifest` — path to a deploy-provided JSON file
      (production). Shape `{"model": {"<upstream-host>": "<secret>"}}` (or the
      flat `{"<upstream-host>": "<secret>"}`). The deployment (app.manifest)
      owns the keys; this process never logs them.
    * `~/.pi/agent/auth.json` when `config :mitos, :pi_dev_auth` — the
      operator's pi auth (local T1 dev). `api_key` credentials are mapped
      provider→upstream and seeded; OAuth creds are skipped (token-lived,
      refresh is pi's job).

  Seeding is idempotent and additive; re-running `seed/0` refreshes the same
  `(scope: "model", dest)` entries.
  """
  use GenServer
  require Logger

  # The canonical provider/dest alias → real upstream host. Operator-extensible
  # via `config :mitos, :model_upstreams` (merged in upstream_for/1). Kept here
  # so ModelProxy (reverse) and credential seeding agree on one truth.
  @providers %{
    "openai" => {"api.openai.com", ""},
    "anthropic" => {"api.anthropic.com", ""},
    "google" => {"generativelanguage.googleapis.com", ""},
    "gemini" => {"generativelanguage.googleapis.com", ""},
    "zai" => {"open.bigmodel.cn", "/api/paas/v4"},
    "deepseek" => {"api.deepseek.com", ""},
    "groq" => {"api.groq.com", ""},
    "mistral" => {"api.mistral.ai", ""},
    "xai" => {"api.x.ai", ""}
  }

  @scope "model"

  ## GenServer boilerplate — exists so `seed/0` runs synchronously at boot
  ## (init blocks until the Vault is populated), before any run can start.

  def start_link(_opts), do: GenServer.start_link(__MODULE__, [], name: __MODULE__)

  @impl true
  def init(_) do
    {from, count} = seed()
    Logger.info("[model_credentials] #{describe_seed(count, from)}")
    {:ok, %{from: from, count: count}}
  end

  # `from` is a descriptor tuple ({:app_manifest, path} / {:pi_auth, path}) or
  # :none — never stringify it directly (String.Chars isn't implemented for
  # tuples); inspect/1 keeps the log safe and readable.
  defp describe_seed(0, _from),
    do: "no model credentials seeded (no app_manifest, pi_dev_auth off, or empty source)"

  defp describe_seed(count, from) when count > 0,
    do: "seeded #{count} model credential(s) from #{inspect(from)}"

  ## Public API

  @doc "Resolve a provider/dest alias to its real upstream HOST (operator-extensible via :model_upstreams). Used for Vault keying + TLS SNI."
  def upstream_for(provider) when is_binary(provider) do
    case provider_upstreams()[provider] do
      {host, _prefix} -> host
      nil -> nil
    end
  end

  @doc "`{host, path_prefix}` for a provider, or nil. The prefix reconstructs the full upstream URL through the reverse proxy."
  def upstream_base(provider) when is_binary(provider), do: provider_upstreams()[provider]

  @doc "The full provider→upstream map (built-in + operator `:model_upstreams`). Each value is `{host, path_prefix}`."
  def provider_upstreams,
    do: Map.merge(@providers, Application.get_env(:mitos, :model_upstreams, %{}))

  @doc """
  Providers (aliases) for which the Vault currently holds a model credential —
  i.e. the providers a harness may route through the reverse proxy. Used by the
  pi adapter to write per-provider `baseUrl` overrides into `models.json`.
  """
  def seeded_providers do
    for {provider, {host, _prefix}} <- provider_upstreams(),
        Mitos.Vault.get(@scope, host) != nil,
        do: provider
  end

  @doc """
  Seed the Vault from the configured source. Returns `{source_descriptor, count}`.
  Safe to call repeatedly. Public so tests and a reseed endpoint can invoke it.
  """
  def seed do
    case seed_from_manifest() do
      {_, n} = result when n > 0 -> result
      _ -> seed_from_pi_auth()
    end
  end

  # --- app.manifest (production) ---

  defp seed_from_manifest do
    case Application.get_env(:mitos, :app_manifest) do
      nil ->
        {:none, 0}

      path ->
        case read_json(path) do
          {:ok, map} ->
            model = map["model"] || map

            count =
              Enum.reduce(model, 0, fn
                {upstream, secret}, acc
                when is_binary(upstream) and is_binary(secret) and secret != "" ->
                  Mitos.Vault.put(@scope, upstream, secret)
                  acc + 1

                _, acc ->
                  acc
              end)

            {{:app_manifest, path}, count}

          {:error, reason} ->
            Logger.warning(
              "[model_credentials] app_manifest at #{path} unreadable: #{inspect(reason)}"
            )

            {{:app_manifest, path}, 0}
        end
    end
  end

  # --- ~/.pi/agent/auth.json (local T1 dev via pi_dev_auth) ---

  defp seed_from_pi_auth do
    if Application.get_env(:mitos, :pi_dev_auth, false) do
      path =
        Application.get_env(:mitos, :pi_auth_path) || Path.expand("~/.pi/agent/auth.json")

      case read_json(path) do
        {:ok, map} when is_map(map) ->
          count =
            Enum.reduce(map, 0, fn
              # api_key: {type, key} → map provider→upstream, seed. OAuth etc. skipped.
              {provider, %{"type" => "api_key", "key" => key}}, acc
              when is_binary(key) and key != "" ->
                case upstream_for(provider) do
                  nil ->
                    acc

                  upstream ->
                    Mitos.Vault.put(@scope, upstream, key)
                    acc + 1
                end

              _, acc ->
                acc
            end)

          {{:pi_auth, path}, count}

        {:error, reason} ->
          Logger.warning("[model_credentials] pi auth at #{path} unreadable: #{inspect(reason)}")
          {{:pi_auth, path}, 0}
      end
    else
      {:none, 0}
    end
  end

  defp read_json(path) do
    case File.read(path) do
      {:ok, body} -> {:ok, Mitos.JSON.decode!(body)}
      {:error, _} = e -> e
    end
  end
end
