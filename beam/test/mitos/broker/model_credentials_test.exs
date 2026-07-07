defmodule Mitos.Broker.ModelCredentialsTest do
  @moduledoc """
  05 R1: the Vault is seeded at boot with model credentials from a deploy-provided
  app.manifest (prod) or the operator's pi auth.json (dev). The real key never
  enters a sandbox; it lives only in the Vault and is attached at the proxy edge.
  """
  use ExUnit.Case, async: false

  alias Mitos.Broker.ModelCredentials

  setup do
    Mitos.Vault.clear()
    # snapshot the env keys this module reads so each test is hermetic.
    keys = [:app_manifest, :pi_dev_auth, :pi_auth_path, :model_upstreams]

    prev =
      Map.new(keys, fn k ->
        {k, Application.get_env(:mitos, k)}
      end)

    on_exit(fn ->
      Enum.each(prev, fn
        {k, nil} -> Application.delete_env(:mitos, k)
        {k, v} -> Application.put_env(:mitos, k, v)
      end)

      Mitos.Vault.clear()
    end)

    :ok
  end

  test "upstream_for resolves built-in providers and operator overrides; nil for unknown" do
    assert ModelCredentials.upstream_for("zai") == "open.bigmodel.cn"
    assert ModelCredentials.upstream_for("anthropic") == "api.anthropic.com"
    assert ModelCredentials.upstream_for("nope") == nil

    Application.put_env(:mitos, :model_upstreams, %{"custom" => {"api.custom.example", ""}})
    assert ModelCredentials.upstream_for("custom") == "api.custom.example"
    assert ModelCredentials.upstream_base("custom") == {"api.custom.example", ""}
  end

  test "seed loads model keys from an app.manifest (production path)" do
    manifest = """
    {
      "model": {
        "api.anthropic.com": "sk-prod-ant",
        "open.bigmodel.cn": "sk-prod-zai"
      }
    }
    """

    path = Path.join(System.tmp_dir!(), "mitos-manifest-#{System.unique_integer([:positive])}.json")
    File.write!(path, manifest)
    Application.put_env(:mitos, :app_manifest, path)

    {source, count} = ModelCredentials.seed()
    assert count == 2
    assert match?({:app_manifest, ^path}, source)

    assert Mitos.Vault.get("model", "api.anthropic.com").secret == "sk-prod-ant"
    assert Mitos.Vault.get("model", "open.bigmodel.cn").secret == "sk-prod-zai"

    # seeded_providers reports the providers whose upstreams are now credentialed
    providers = ModelCredentials.seeded_providers() |> Enum.sort()
    assert providers == ["anthropic", "zai"]
  end

  test "seed loads api_key credentials from pi auth.json (dev path via pi_dev_auth)" do
    auth = """
    {
      "zai": {"type": "api_key", "key": "sk-dev-zai"},
      "anthropic": {"type": "oauth", "accessToken": "tok", "expires": 9999999999999},
      "bogus": {"type": "api_key", "key": "sk-dev-bogus"}
    }
    """

    path = Path.join(System.tmp_dir!(), "mitos-pi-auth-#{System.unique_integer([:positive])}.json")
    File.write!(path, auth)

    Application.put_env(:mitos, :pi_dev_auth, true)
    Application.put_env(:mitos, :pi_auth_path, path)

    {source, count} = ModelCredentials.seed()
    # zai maps to open.bigmodel.cn; bogus is an unknown provider (skipped);
    # the oauth cred is skipped (token-lived).
    assert count == 1
    assert match?({:pi_auth, ^path}, source)
    assert Mitos.Vault.get("model", "open.bigmodel.cn").secret == "sk-dev-zai"
    assert Mitos.Vault.get("model", "api.anthropic.com") == nil
    assert ModelCredentials.seeded_providers() == ["zai"]
  end

  test "seed is a no-op (and silent) when no source is configured" do
    {source, count} = ModelCredentials.seed()
    assert count == 0
    assert source == :none
    assert ModelCredentials.seeded_providers() == []
  end

  test "app_manifest takes priority over pi_dev_auth" do
    manifest_path =
      Path.join(System.tmp_dir!(), "mitos-manifest-#{System.unique_integer([:positive])}.json")

    File.write!(manifest_path, ~s<{"model": {"api.anthropic.com": "sk-prod"}}>)
    auth_path = Path.join(System.tmp_dir!(), "mitos-pi-auth-#{System.unique_integer([:positive])}.json")
    File.write!(auth_path, ~s<{"zai": {"type": "api_key", "key": "sk-dev-zai"}}>)

    Application.put_env(:mitos, :app_manifest, manifest_path)
    Application.put_env(:mitos, :pi_dev_auth, true)
    Application.put_env(:mitos, :pi_auth_path, auth_path)

    {source, count} = ModelCredentials.seed()
    assert match?({:app_manifest, ^manifest_path}, source)
    # only the manifest's entry is loaded; pi auth is not consulted
    assert count == 1
    assert Mitos.Vault.get("model", "api.anthropic.com").secret == "sk-prod"
    assert Mitos.Vault.get("model", "open.bigmodel.cn") == nil
  end
end
