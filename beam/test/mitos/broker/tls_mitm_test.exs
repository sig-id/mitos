defmodule Mitos.Broker.TlsMitmTest do
  @moduledoc """
  05 R1: the TLS-MITM proxy terminates the harness's HTTPS, injects the Vault
  model key, and re-encrypts to the real upstream. Verified layers: (1) the CA
  signs leaves that validate against it; (2) a raw TLS client CONNECTs through
  the proxy and the CA-trusted handshake completes the MITM path; (3) the
  injected credential is built from the Vault and replaces a caller Authorization.
  """
  use ExUnit.Case, async: false

  alias Mitos.Broker.{CertAuthority, TlsMitm}

  @host "localhost"

  setup do
    Mitos.Vault.clear()
    Application.ensure_started(:ssl)

    unless Process.whereis(CertAuthority), do: start_supervised!({CertAuthority, []})

    # Pre-warm the leaf so the CONNECT handler hits the cache (no openssl latency
    # racing the TLS handshake).
    _leaf = CertAuthority.leaf(@host)

    # a free port for this test run (avoids any cross-run collision)
    {:ok, probe} = :gen_tcp.listen(0, [:binary, active: false, reuseaddr: true])
    {:ok, port} = :inet.port(probe)
    :gen_tcp.close(probe)

    {:ok, pid} = TlsMitm.start_link(port)

    on_exit(fn ->
      ref = Process.monitor(pid)
      Process.exit(pid, :shutdown)

      receive do
        {:DOWN, ^ref, _, _, _} -> :ok
      after
        500 -> :ok
      end

      Mitos.Vault.clear()
    end)

    {:ok, ca: CertAuthority.ca_cert_path(), port: port}
  end

  test "the CA signs a leaf that validates against it", %{ca: ca_pem} do
    {leaf_pem, _} = CertAuthority.leaf(@host)
    {out, 0} = System.cmd("openssl", ["verify", "-CAfile", ca_pem, leaf_pem], stderr_to_stdout: true)
    assert out =~ "OK"
  end

  test "a CONNECT through the proxy completes the CA-trusted MITM path", %{ca: ca_pem, port: port} do
    # The first-contact path can race the leaf gen under suite load; a single
    # retry exercises the (now cached) leaf and confirms the interception.
    assert connect_and_intercept(ca_pem, port) == :ok
  end

  defp connect_and_intercept(ca_pem, port) do
    case try_connect(ca_pem, port) do
      :ok -> :ok
      {:error, _} -> try_connect(ca_pem, port)
    end
  end

  defp try_connect(ca_pem, port) do
    with {:ok, s} <- :gen_tcp.connect(~c"127.0.0.1", port, [:binary, active: false, packet: :raw]),
         :ok <- :gen_tcp.send(s, "CONNECT #{@host}:443 HTTP/1.1\r\nHost: #{@host}\r\n\r\n"),
         resp when is_binary(resp) <- drain_until(s, "\r\n\r\n"),
         true <- String.starts_with?(resp, "HTTP/1.1 200"),
         {:ok, tls} <-
           :ssl.connect(s, [
             verify: :verify_peer,
             cacertfile: String.to_charlist(ca_pem),
             server_name_indication: String.to_charlist(@host),
             active: false
           ], 8_000),
         :ok <- :ssl.send(tls, "GET / HTTP/1.1\r\nHost: #{@host}\r\n\r\n"),
         {:ok, resp} <- :ssl.recv(tls, 0, 5_000) do
      :ssl.close(tls)
      if resp =~ "502", do: :ok, else: {:error, {:bad_resp, resp}}
    else
      _ -> {:error, :handshake_failed}
    end
  end

  defp drain_until(s, marker, acc \\ "") do
    if String.contains?(acc, marker) do
      acc
    else
      case :gen_tcp.recv(s, 0, 5_000) do
        {:ok, data} -> drain_until(s, marker, acc <> data)
        {:error, _} -> acc
      end
    end
  end

  test "credentialed_headers injects the Vault key and replaces a caller Authorization" do
    assert TlsMitm.credentialed_headers(["Host: x", "Accept: */*"], "open.bigmodel.cn") ==
             ["Host: x", "Accept: */*"]

    Mitos.Vault.put("model", "open.bigmodel.cn", "sk-edge")

    headers =
      TlsMitm.credentialed_headers(["Host: x", "Authorization: Bearer caller-attempt"], "open.bigmodel.cn")

    assert Enum.any?(headers, &(&1 == "Authorization: Bearer sk-edge"))
    refute Enum.any?(headers, &(&1 == "Authorization: Bearer caller-attempt"))
  end

  # A harness may auth with x-api-key (Anthropic) / api-key (OpenAI) rather than
  # Authorization: Bearer. The placeholder/boot key a sandbox sends under those
  # names must be stripped too, or it leaks upstream and 401s — the real key is
  # injected from the Vault, case-insensitively across all the conventions.
  test "credentialed_headers strips x-api-key / api-key placeholders before injecting the Vault key" do
    Mitos.Vault.put("model", "api.anthropic.com", "sk-edge")

    headers =
      TlsMitm.credentialed_headers(
        [
          "Host: api.anthropic.com",
          "x-api-key: via-mitos-edge",
          "api-key: via-mitos-edge",
          "X-Api-Key: via-mitos-edge",
          "Authorization: Bearer via-mitos-edge",
          "anthropic-version: 2023-06-01"
        ],
        "api.anthropic.com"
      )

    assert Enum.any?(headers, &(&1 == "Authorization: Bearer sk-edge"))

    refute Enum.any?(headers, &String.starts_with?(&1, "x-api-key:"))
    refute Enum.any?(headers, &String.starts_with?(&1, "api-key:"))
    refute Enum.any?(headers, &String.starts_with?(&1, "X-Api-Key:"))
    refute Enum.any?(headers, &String.starts_with?(&1, "Authorization: Bearer via-mitos-edge"))

    # non-credential headers survive
    assert Enum.any?(headers, &(&1 == "anthropic-version: 2023-06-01"))
  end
end
