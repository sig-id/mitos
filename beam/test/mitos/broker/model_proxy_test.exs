defmodule Mitos.Broker.ModelProxyTest do
  @moduledoc """
  05 R1: the model-egress proxy routes a harness's model calls to the real
  upstream and attaches the credential at the edge (Vault), never in the sandbox.
  We unit-test the routing + credential logic (the live SSE forward needs a real
  upstream, as for any TLS reverse proxy).
  """
  use ExUnit.Case, async: true

  alias Mitos.Broker.ModelProxy

  setup do
    Mitos.Vault.clear()
    on_exit(&Mitos.Vault.clear/0)
    :ok
  end

  test "upstream_for resolves dest aliases (operator-extensible)" do
    assert ModelProxy.upstream_for("openai") == "api.openai.com"
    assert ModelProxy.upstream_for("anthropic") == "api.anthropic.com"
    assert ModelProxy.upstream_for("zai") == "open.bigmodel.cn"
    assert ModelProxy.upstream_for("nope") == nil
  end

  test "outbound_headers attaches the Vault credential for (model, upstream); omits it when absent" do
    conn = Plug.Test.conn(:post, "/openai/v1/chat/completions", "{}")
    # no credential registered ⇒ no authorization header
    refute Enum.any?(
             ModelProxy.outbound_headers(conn, "api.openai.com"),
             &match?({~c"authorization", _}, &1)
           )

    # register a model key at the edge ⇒ the proxy attaches it; the sandbox never saw it
    Mitos.Vault.put("model", "api.openai.com", "sk-edge-secret")
    headers = ModelProxy.outbound_headers(conn, "api.openai.com")
    assert {~c"authorization", auth} = List.keyfind(headers, ~c"authorization", 0)
    assert to_string(auth) == "Bearer sk-edge-secret"
    # Host is fixed to the real upstream
    assert {~c"host", ~c"api.openai.com"} = List.keyfind(headers, ~c"host", 0)
  end

  test "upstream_url builds the TLS forward URL" do
    assert ModelProxy.upstream_url("api.openai.com", "v1/chat/completions") ==
             "https://api.openai.com/v1/chat/completions"
  end

  test "model_proxy_env redirects harness base URLs to the edge, deriving the URL from the enabled port" do
    # Explicit URL wins.
    Application.put_env(:mitos, :model_proxy_url, "http://127.0.0.1:4648")
    Application.put_env(:mitos, :model_proxy_port, 0)
    env = Mitos.Harness.model_proxy_env()
    assert env["ANTHROPIC_BASE_URL"] == "http://127.0.0.1:4648/anthropic"
    assert env["OPENAI_BASE_URL"] == "http://127.0.0.1:4648/openai"

    # No explicit URL, but the port is enabled → URL derived from it (loopback)
    # so a running ModelProxy is auto-wired without a second config knob.
    Application.delete_env(:mitos, :model_proxy_url)
    Application.put_env(:mitos, :model_proxy_port, 4648)
    env = Mitos.Harness.model_proxy_env()
    assert env["ANTHROPIC_BASE_URL"] == "http://127.0.0.1:4648/anthropic"

    # Neither URL nor an enabled port → spec-clean empty.
    Application.put_env(:mitos, :model_proxy_port, 0)
    assert Mitos.Harness.model_proxy_env() == %{}
  after
    Application.delete_env(:mitos, :model_proxy_url)
    # restore the test-isolated port (config/test.exs)
    Application.put_env(:mitos, :model_proxy_port, 4748)
  end
end
