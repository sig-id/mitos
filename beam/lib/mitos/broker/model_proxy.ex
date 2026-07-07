defmodule Mitos.Broker.ModelProxy do
  @moduledoc """
  The host-side model-egress proxy (05 R1) — the edge a harness's model-API
  traffic routes through so the model credential is attached HERE and never
  enters the sandbox (01 §8.5). This is the broker Nucleus's
  `--credential-broker IP:PORT` points at (Nucleus injects `HTTP_PROXY`/
  `HTTPS_PROXY` for you), and the endpoint a harness base-URL override points at.

  Routing shape (reverse / base-URL override — no TLS interception, 05 R1): the
  harness's model base URL is set to `http://<proxy>/<dest>`; the `dest` segment
  maps to a real upstream (`openai`→api.openai.com, `anthropic`→api.anthropic.com,
  `zai`→open.bigmodel.cn, …). The proxy attaches the Vault key for
  `(scope: "model", dest: <upstream>)` and forwards over TLS to the real API,
  **streaming** the (SSE) response back chunk-by-chunk. The model key lives only
  in the Vault on the control plane; the sandbox sends an uncredentialed request.

  The forward is a raw TLS connection (`:ssl.connect` → send the HTTP request →
  decode the upstream's response framing → forward raw bytes as Plug chunks), NOT
  `:httpc`: `:httpc`'s async `stream: :self` mode silently fails to deliver its
  `{http, _}` messages under the running OTP supervision tree, and sync `:httpc`
  buffers the whole body (breaking SSE clients). The raw-TLS form decodes the
  upstream's `Transfer-Encoding: chunked` (or `Content-Length`) into raw bytes so
  the response terminates at its frame end (not by waiting for a keep-alive
  connection to close) and an SSE stream lands intact.
  """
  use Plug.Router

  plug(:match)
  plug(:dispatch)

  get "/health" do
    send_resp(conn, 200, Mitos.JSON.encode!(%{status: "ok", mode: "model_proxy"}))
  end

  # Reverse form: /<dest>/<path...>  e.g. POST /openai/v1/chat/completions
  match _ do
    {dest, rest} = route(conn.path_info)

    with upstream when not is_nil(upstream) <- upstream_for(dest),
         {:ok, body, conn} <- Plug.Conn.read_body(conn) do
      forward(conn, upstream, rest, body)
    else
      nil ->
        send_resp(conn, 404, Mitos.JSON.encode!(%{error: "unknown_model_dest", dest: dest}))

      {:more, _, conn} ->
        send_resp(conn, 413, Mitos.JSON.encode!(%{error: "request_too_large"}))
    end
  end

  # --- public helpers (unit-tested) ---

  @doc "Resolve a dest alias to a real upstream host. Delegates to `ModelCredentials` (single source of truth, shared with Vault seeding)."
  def upstream_for(dest), do: Mitos.Broker.ModelCredentials.upstream_for(dest)

  @doc "The outbound request headers: Host fixed to the upstream, harness headers kept, Vault cred attached."
  def outbound_headers(conn, upstream) do
    kept =
      conn.req_headers
      |> Enum.filter(fn {k, _} ->
        # req_headers keys are binaries (lowercase); compare to binaries, not atoms.
        k in ~w(content-type accept user-agent anthropic-version anthropic-beta)
      end)
      |> Enum.map(fn {k, v} -> {String.to_charlist(k), String.to_charlist(v)} end)

    [{~c"host", String.to_charlist(upstream)} | kept]
    |> attach_credential(Mitos.Vault.get("model", upstream))
    |> drop_hop_by_conn()
  end

  @doc "Build the upstream URL for `(upstream, rest_path)`."
  def upstream_url(upstream, rest), do: "https://#{upstream}/#{rest}"

  defp attach_credential(headers, nil), do: headers

  defp attach_credential(headers, %{secret: secret}),
    do: [{~c"authorization", String.to_charlist("Bearer " <> secret)} | headers]

  defp drop_hop_by_conn(headers) do
    Enum.reject(headers, fn {k, _} ->
      k in ~w(proxy-authorization proxy-connection transfer-encoding connection keep-alive content-length)c
    end)
  end

  # --- forward (live-upstream path): raw TLS so the response STREAMS ---

  defp forward(conn, upstream, rest, body) do
    path = "/" <> rest
    headers = outbound_headers(conn, upstream)
    method = conn.method
    request = build_request(method, path, headers, body)

    # HTTPS upstreams need SNI for the handshake. Upstream cert verification is
    # intentionally off (the proxy is the trust boundary — it allow-lists
    # upstreams via :model_upstreams and holds the Vault key); wildcard provider
    # certs make per-host verification fragile. TODO(05 R1): re-enable
    # :verify_peer once wildcard matching is verified per upstream.
    ssl_opts = [
      server_name_indication: String.to_charlist(upstream),
      verify: :verify_none,
      mode: :binary,
      active: false
    ]

    case :ssl.connect(String.to_charlist(upstream), 443, ssl_opts, 30_000) do
      {:ok, up} ->
        try do
          :ok = :ssl.send(up, request)
          stream_response(conn, up)
        after
          :ssl.close(up)
        end

      {:error, reason} ->
        send_resp(
          conn,
          502,
          Mitos.JSON.encode!(%{error: "upstream_failed", reason: inspect(reason)})
        )
    end
  end

  # Build a raw HTTP/1.1 request. `headers` are charlist {k,v} tuples.
  defp build_request(method, path, headers, body) do
    header_lines = Enum.map_join(headers, "", fn {k, v} -> "#{k}: #{v}\r\n" end)
    # Content-Length reflects the body we send (no Transfer-Encoding outbound).
    clen = "Content-Length: #{byte_size(body)}\r\n"
    "#{method} #{path} HTTP/1.1\r\n#{header_lines}#{clen}\r\n#{body}"
  end

  # Read the response head, send_chunked the status + content-type, then forward
  # the body with correct framing (decoded to raw bytes so Plug re-chunks for the
  # client — no double framing — and the response ends at the upstream's frame end).
  defp stream_response(conn, up) do
    case read_head(up, <<"">>) do
      {:ok, head_blob, extra} ->
        {status, headers, _body_start} = parse_head(head_blob)

        conn =
          conn
          |> put_resp_content_type(headers["content-type"] || "application/octet-stream")
          |> send_chunked(status)

        stream_framed(conn, up, extra, headers)

      {:error, reason} ->
        send_resp(
          conn,
          502,
          Mitos.JSON.encode!(%{error: "upstream_read_failed", reason: inspect(reason)})
        )
    end
  end

  # Read until the end-of-headers marker. Returns the head + any body bytes that
  # arrived in the same recv (pipelined).
  defp read_head(up, acc) do
    case :binary.match(acc, "\r\n\r\n") do
      {pos, 4} ->
        head = binary_part(acc, 0, pos + 4)
        extra = binary_part(acc, pos + 4, byte_size(acc) - (pos + 4))
        {:ok, head, extra}

      :nomatch ->
        case :ssl.recv(up, 0, 30_000) do
          {:ok, data} -> read_head(up, acc <> data)
          {:error, _} = e -> e
        end
    end
  end

  defp parse_head(head_blob) do
    [head | _] = :binary.split(head_blob, "\r\n\r\n")
    [status_line | header_lines] = String.split(head, "\r\n", trim: true)
    status = status_line |> String.split(" ") |> Enum.at(1) |> String.to_integer()

    headers =
      Map.new(header_lines, fn line ->
        [k | rest] = String.split(line, ":", parts: 2)
        {String.downcase(String.trim(k)), String.trim(Enum.join(rest))}
      end)

    {status, headers, nil}
  end

  # Forward the upstream body decoded into raw bytes. Dispatch on framing:
  # chunked (decode <hex>\r\n<data>\r\n frames until 0), content-length (read N),
  # or read-until-close (identity).
  defp stream_framed(conn, up, buf, headers) do
    cond do
      headers["transfer-encoding"] != nil and
          String.contains?(headers["transfer-encoding"], "chunked") ->
        stream_chunked(conn, up, buf)

      headers["content-length"] != nil ->
        stream_content_length(conn, up, buf, String.to_integer(headers["content-length"]))

      true ->
        stream_until_close(conn, up, buf)
    end
  end

  # Transfer-Encoding: chunked.
  defp stream_chunked(conn, up, buf) do
    case take_chunk_size(buf) do
      {:ok, 0, _rest} ->
        conn

      {:ok, size, rest} ->
        with {:ok, data, after_data} <- take_exact(up, rest, size),
             {:ok, _crlf, after_frame} <- take_exact(up, after_data, 2),
             {:ok, conn} <- chunk_once(conn, data) do
          stream_chunked(conn, up, after_frame)
        else
          _ -> conn
        end

      :need_size ->
        case :ssl.recv(up, 0, 30_000) do
          {:ok, data} -> stream_chunked(conn, up, buf <> data)
          {:error, _} -> conn
        end
    end
  end

  # Content-Length.
  defp stream_content_length(conn, up, buf, remaining) do
    cond do
      remaining <= 0 ->
        conn

      byte_size(buf) > 0 ->
        {data, rest} = split_at(buf, remaining)

        case chunk_once(conn, data) do
          {:ok, conn} -> stream_content_length(conn, up, rest, remaining - byte_size(data))
          {:error, _} -> conn
        end

      true ->
        case :ssl.recv(up, 0, 30_000) do
          {:ok, data} -> stream_content_length(conn, up, data, remaining)
          {:error, _} -> conn
        end
    end
  end

  # No framing — read until the upstream closes.
  defp stream_until_close(conn, up, buf) do
    if byte_size(buf) > 0 do
      case chunk_once(conn, buf) do
        {:ok, conn} -> stream_until_close(conn, up, <<"">>)
        {:error, _} -> conn
      end
    else
      case :ssl.recv(up, 0, 60_000) do
        {:ok, data} ->
          case chunk_once(conn, data) do
            {:ok, conn} -> stream_until_close(conn, up, <<"">>)
            {:error, _} -> conn
          end

        {:error, :closed} ->
          conn

        {:error, _} ->
          conn
      end
    end
  end

  # Read exactly `n` bytes from `buf`, draining `up` as needed.
  defp take_exact(up, buf, n) do
    if byte_size(buf) >= n do
      <<data::binary-size(n), rest::binary>> = buf
      {:ok, data, rest}
    else
      case :ssl.recv(up, 0, 30_000) do
        {:ok, more} -> take_exact(up, buf <> more, n)
        {:error, _} = e -> e
      end
    end
  end

  # Parse a chunked frame's hex size line from the buffer.
  defp take_chunk_size(buf) do
    case :binary.match(buf, "\r\n") do
      {pos, 2} ->
        size_line = binary_part(buf, 0, pos)
        rest = binary_part(buf, pos + 2, byte_size(buf) - (pos + 2))
        # discard chunk extensions (;...)
        hex = size_line |> String.split(";") |> hd() |> String.trim()
        {:ok, String.to_integer(hex, 16), rest}

      :nomatch ->
        :need_size
    end
  end

  defp split_at(buf, n) do
    if byte_size(buf) <= n do
      {buf, <<"">>}
    else
      <<data::binary-size(n), rest::binary>> = buf
      {data, rest}
    end
  end

  defp chunk_once(_conn, <<>>), do: {:ok, nil}
  defp chunk_once(conn, data), do: Plug.Conn.chunk(conn, data)

  defp route([dest | rest]), do: {dest, Enum.join(rest, "/")}
  defp route(_), do: {nil, ""}
end
