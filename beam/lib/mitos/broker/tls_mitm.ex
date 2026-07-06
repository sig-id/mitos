defmodule Mitos.Broker.TlsMitm do
  @moduledoc """
  The TLS-intercepting model-egress proxy (05 R1). The harness's HTTPS model
  calls (e.g. pi → `https://open.bigmodel.cn`) route here via `HTTPS_PROXY`
  (Nucleus injects this for `--credential-broker`; the Local provider sets it).

    1. accept `CONNECT host:443`,
    2. terminate TLS with a per-host leaf signed by the mitos CA
       (`Mitos.Broker.CertAuthority`) — the harness trusts the CA via the injected
       `SSL_CERT_FILE`/`NODE_EXTRA_CA_CERTS`,
    3. read the decrypted request, inject `Authorization: Bearer <key>` from the
       `Vault` (`scope "model"`, `dest: host`),
    4. forward over TLS to the real `host:443`, stream the (SSE) response back.

  The model key never enters the sandbox and never appears on the log; it lives
  only at this edge. Universal — needs no per-harness base-URL override.
  """
  use Task, restart: :permanent

  def child_spec(port: port) when is_integer(port) and port > 0 do
    %{id: __MODULE__, start: {__MODULE__, :start_link, [port]}, type: :worker, restart: :permanent}
  end

  def child_spec(_), do: :ignore

  def start_link(port) do
    Task.start_link(fn -> accept_loop(port) end)
  end

  defp accept_loop(port) do
    # Line mode for parsing the CONNECT request; the socket switches to raw
    # before the TLS handshake. (Mirrors the standalone probe that works.)
    {:ok, listen} = :gen_tcp.listen(port, [:binary, active: false, reuseaddr: true, packet: :line])
    loop(listen)
  end

  defp loop(listen) do
    # Accept and serve in the SAME process: :ssl.handshake requires the calling
    # process to own the socket.
    spawn(fn ->
      case :gen_tcp.accept(listen) do
        {:ok, sock} -> serve(sock)
        {:error, _} -> :ok
      end
    end)

    loop(listen)
  end

  defp serve(sock) do
    case read_request_line(sock) do
      {:connect, host, port} ->
        :ok = read_headers(sock)
        {cert, key} = Mitos.Broker.CertAuthority.leaf(host)
        :inet.setopts(sock, [packet: :raw, active: false])
        :gen_tcp.send(sock, "HTTP/1.1 200 Connection Established\r\n\r\n")
        tls_serve(sock, host, port, cert, key)

      {:http, method, url} ->
        :ok = read_headers(sock)
        forward_plain(sock, method, url)

      :closed ->
        :ok
    end
  after
    close(sock)
  end

  defp read_request_line(sock) do
    case :gen_tcp.recv(sock, 0, 15_000) do
      {:ok, line} ->
        case String.trim(line) do
          "CONNECT " <> rest ->
            [target | _] = String.split(rest, " ")

            [host, port] =
              case String.split(target, ":") do
                [h, p] -> [h, p]
                [h] -> [h, "443"]
              end

            {:connect, host, String.to_integer(port)}

          other ->
            [method, url | _] = String.split(other, " ")
            {:http, method, url}
        end

      {:error, _} ->
        :closed
    end
  end

  defp read_headers(sock) do
    case :gen_tcp.recv(sock, 0, 15_000) do
      {:ok, "\r\n"} -> :ok
      {:ok, "\n"} -> :ok
      {:ok, _} -> read_headers(sock)
      {:error, _} -> :ok
    end
  end

  defp tls_serve(sock, host, port, cert, key) do
    opts = [
      certfile: String.to_charlist(cert),
      keyfile: String.to_charlist(key),
      verify: :verify_none,
      active: false
    ]

    case :ssl.handshake(sock, opts) do
      {:ok, tls} ->
        tls_request_loop(tls, host, port)
        :ssl.close(tls)

      {:error, reason} ->
        require Logger
        Logger.error("tls_mitm handshake failed for #{host}:#{port}: #{inspect(reason)}")
        :ok
    end
  end

  defp tls_request_loop(tls, host, port) do
    with {:ok, req} <- read_tls_request(tls),
         {method, path, headers, body} <- parse_http(req) do
      forward_tls(tls, host, port, method, path, credentialed_headers(headers, host), body)
      tls_request_loop(tls, host, port)
    else
      _ -> :ok
    end
  end

  defp read_tls_request(tls, buf \\ <<>>) do
    case :ssl.recv(tls, 0, 30_000) do
      {:ok, data} ->
        buf = buf <> data

        case :binary.split(buf, "\r\n\r\n") do
          [head, body] ->
            headers = for line <- :binary.split(head, "\r\n", [:global]), line != "", do: line
            clen = content_length(headers)
            need = clen - byte_size(body)

            if need > 0 do
              case drain_body(tls, body, need) do
                {:ok, full} -> {:ok, head <> "\r\n\r\n" <> full}
                other -> other
              end
            else
              {:ok, buf}
            end

          _ ->
            if byte_size(buf) > 1_000_000, do: {:error, :too_large}, else: read_tls_request(tls, buf)
        end

      {:error, _} = e ->
        e
    end
  end

  defp drain_body(tls, body, need) do
    if need <= 0 do
      {:ok, body}
    else
      case :ssl.recv(tls, need, 30_000) do
        {:ok, data} -> {:ok, body <> data}
        {:error, _} = e -> e
      end
    end
  end

  defp parse_http(req) do
    [head, body] = :binary.split(req, "\r\n\r\n")
    [request_line | header_lines] = :binary.split(head, "\r\n", [:global])
    [method, path | _] = String.split(request_line, " ")
    headers = Enum.reject(header_lines, &(&1 == ""))
    {method, path, headers, body}
  end

  @doc """
  The outbound request headers with the Vault model credential injected for
  `host` (any existing Authorization is replaced). Public for unit testing.
  """
  def credentialed_headers(headers, host) do
    headers = Enum.reject(headers, &String.starts_with?(&1, "Authorization:"))

    case Mitos.Vault.get("model", host) do
      %{secret: secret} -> ["Authorization: Bearer #{secret}" | headers]
      _ -> headers
    end
  end

  defp forward_tls(client_tls, host, port, method, path, headers, body) do
    request = "#{method} #{path} HTTP/1.1\r\n#{Enum.join(headers, "\r\n")}\r\n\r\n#{body}"

    case :ssl.connect(String.to_charlist(host), port, [
           verify: :verify_peer,
           customize_hostname_check: [],
           active: false,
           packet: :raw,
           timeout: 30_000
         ], 30_000) do
      {:ok, up} ->
        :ssl.send(up, request)
        pipe(up, client_tls)
        :ssl.close(up)

      {:error, _} ->
        :ssl.send(client_tls, "HTTP/1.1 502 Bad Gateway\r\ncontent-length: 0\r\n\r\n")
    end
  end

  defp pipe(up, client) do
    case :ssl.recv(up, 0, 300_000) do
      {:ok, data} -> :ssl.send(client, data); pipe(up, client)
      {:error, :closed} -> :ok
      {:error, _} -> :ok
    end
  end

  defp forward_plain(sock, method, url) do
    uri = URI.parse(url)
    host = uri.host
    injected = credentialed_headers([], host)
    request = "#{method} #{uri.path}#{uri.query && "?" <> uri.query} HTTP/1.1\r\n#{Enum.join(injected, "\r\n")}\r\n\r\n"

    case :gen_tcp.connect(String.to_charlist(host), uri.port || 80, [active: false, packet: :raw], 15_000) do
      {:ok, up} -> :gen_tcp.send(up, request); pipe_tcp(up, sock)
      {:error, _} -> :ok
    end
  end

  defp pipe_tcp(up, client) do
    case :gen_tcp.recv(up, 0, 300_000) do
      {:ok, data} -> :gen_tcp.send(client, data)
      _ -> :ok
    end
  end

  defp close(sock), do: :gen_tcp.close(sock)

  defp content_length(headers) do
    Enum.find_value(headers, 0, fn h ->
      case String.downcase(h) do
        "content-length: " <> n -> String.to_integer(String.trim(n))
        _ -> nil
      end
    end)
  end
end
