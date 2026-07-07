defmodule Mitos.Harness.PiProxyTest do
  @moduledoc """
  05 R1 / Path R: the pi adapter writes a models.json into the hermetic .pi so
  pi (which ignores ANTHROPIC_BASE_URL) routes its model calls through the
  reverse proxy. The placeholder apiKey boots pi; ModelProxy strips it and
  attaches the real Vault key. Verifies the wiring conditions without spawning pi.
  """
  use ExUnit.Case, async: false

  alias Mitos.Harness.Pi

  setup do
    Mitos.Vault.clear()
    keys = [:pi_dev_auth, :model_proxy_url, :model_proxy_port, :model_upstreams]
    prev = Map.new(keys, fn k -> {k, Application.get_env(:mitos, k)} end)

    on_exit(fn ->
      Enum.each(prev, fn
        {k, nil} -> Application.delete_env(:mitos, k)
        {k, v} -> Application.put_env(:mitos, k, v)
      end)

      Mitos.Vault.clear()
    end)

    :ok
  end

  defp sandbox(home), do: %{home: home, workspace: home}

  test "writes models.json with per-provider baseUrl overrides when the reverse proxy is engaged" do
    Application.put_env(:mitos, :model_proxy_port, 4648)
    Application.delete_env(:mitos, :model_proxy_url)
    Application.delete_env(:mitos, :pi_dev_auth)
    # a model credential is seeded at the edge → its provider is routable
    Mitos.Vault.put("model", "open.bigmodel.cn", "sk-real-zai")

    home = Path.join(System.tmp_dir!(), "mitos-pi-proxy-#{System.unique_integer([:positive])}")
    Pi.prepare_proxy_models(sandbox(home))

    models = home |> Path.join(".pi/agent/models.json") |> File.read!() |> Mitos.JSON.decode!()

    # zai (→ open.bigmodel.cn) is the seeded provider; its calls route through
    # the reverse proxy on 4648, with a placeholder apiKey that ModelProxy strips.
    assert %{"baseUrl" => base, "apiKey" => key} = models["providers"]["zai"]
    # zai carries the /api/paas/v4 path prefix so pi's appended path reconstructs
    # the full upstream URL through the reverse proxy.
    assert base == "http://127.0.0.1:4648/zai/api/paas/v4"
    assert key == "via-mitos-edge"
  end

  test "no-op under pi_dev_auth (operator's real ~/.pi is used directly)" do
    Application.put_env(:mitos, :model_proxy_port, 4648)
    Application.put_env(:mitos, :pi_dev_auth, true)
    Mitos.Vault.put("model", "open.bigmodel.cn", "sk-real-zai")

    home = Path.join(System.tmp_dir!(), "mitos-pi-proxy-#{System.unique_integer([:positive])}")
    assert Pi.prepare_proxy_models(sandbox(home)) == :ok
    refute File.exists?(Path.join(home, ".pi/agent/models.json"))
  end

  test "no-op when the reverse proxy is disabled" do
    Application.put_env(:mitos, :model_proxy_port, 0)
    Application.delete_env(:mitos, :model_proxy_url)
    Mitos.Vault.put("model", "open.bigmodel.cn", "sk-real-zai")

    home = Path.join(System.tmp_dir!(), "mitos-pi-proxy-#{System.unique_integer([:positive])}")
    assert Pi.prepare_proxy_models(sandbox(home)) == :ok
    refute File.exists?(Path.join(home, ".pi/agent/models.json"))
  end

  test "no-op when nothing is seeded (no provider to route)" do
    Application.put_env(:mitos, :model_proxy_port, 4648)
    # Vault empty → seeded_providers/0 is []
    home = Path.join(System.tmp_dir!(), "mitos-pi-proxy-#{System.unique_integer([:positive])}")
    assert Pi.prepare_proxy_models(sandbox(home)) == :ok
    refute File.exists?(Path.join(home, ".pi/agent/models.json"))
  end
end
