defmodule Mitos.Http do
  @moduledoc """
  Minimal control-plane HTTP client for I/O-plane egress (sink delivery).

  Uses raw sockets (`:gen_tcp` / `:ssl`) with no external dependency and no
  reliance on OTP's `:httpc` — which is deliberately avoided so control-plane
  egress is robust across OTP builds (some inets versions misbehave on the
  connection-failure path). This mirrors the in-sandbox broker's raw-socket
  forwarder (`Mitos.Web.Router`), but is a clean POST client for sink delivery
  that needs robust TLS and a sane error surface, not byte-level header control.

  Only POST is needed by sinks today. The response body is returned as a binary.
  Requests are bounded by a timeout; any network/protocol failure — including a
  raised internal error — is converted to `{:error, reason}` so a sink delivery
  can never crash a run.

  Secrets travel via the control-plane vault, never in sink URLs: URLs with
  userinfo or credential-bearing query parameters are rejected.
  """

  @default_timeout_ms 5_000

  @doc """
  POST `body` (binary or iolist) to `url` with the given headers.

  Returns:
    * `{:ok, %{status: integer, headers: [{name, value}], body: binary}}`
    * `{:error, reason}` (network/protocol failure)

  `content-type` defaults to `application/json` when not supplied.
  """
  def post(url, body, headers \\ [], opts \\ [])

  def post(url, body, headers, opts) when is_binary(url) do
    try do
      do_post(url, body, headers, opts)
    rescue
      error -> {:error, Exception.message(error)}
    catch
      :exit, reason -> {:error, {:exit, reason}}
      kind, reason -> {:error, {kind, reason}}
    end
  end

  defp do_post(url, body, headers, opts) do
    with {:ok, uri} <- safe_uri(url) do
      timeout = Keyword.get(opts, :timeout_ms, @default_timeout_ms)
      body_binary = IO.iodata_to_binary(body)
      content_type = content_type_header(headers, "application/json")
      request = build_request(uri, content_type, body_binary, headers)

      with {:ok, transport, socket} <- connect(uri, timeout),
           :ok <- send_all(transport, socket, request),
           {:ok, status, resp_headers, resp_body} <- read_response(transport, socket, timeout) do
        {:ok,
         %{
           status: status,
           headers: resp_headers,
           body: resp_body
         }}
      end
    end
  end

  defp build_request(uri, content_type, body, headers) do
    user_headers =
      headers
      |> Enum.flat_map(&normalize_header/1)
      |> Enum.reject(fn {name, _value} -> String.downcase(name) == "content-type" end)

    [
      "POST ",
      request_target(uri),
      " HTTP/1.1\r\n",
      "host: ",
      host_header(uri),
      "\r\n",
      "content-type: ",
      content_type,
      "\r\n",
      "content-length: ",
      Integer.to_string(byte_size(body)),
      "\r\n",
      Enum.map(user_headers, fn {name, value} -> [name, ": ", value, "\r\n"] end),
      "connection: close\r\n",
      "\r\n",
      body
    ]
  end

  defp normalize_header({name, value}) when is_binary(name) and is_binary(value),
    do: [{String.downcase(name), value}]

  defp normalize_header({name, value}) when is_atom(name) and is_binary(value),
    do: [{name |> Atom.to_string() |> String.downcase(), value}]

  defp normalize_header(_other), do: []

  defp request_target(%URI{path: path, query: query}) do
    path = if path in [nil, ""], do: "/", else: path
    if query in [nil, ""], do: path, else: path <> "?" <> query
  end

  defp host_header(%URI{host: host, port: port, scheme: scheme}) do
    if port == nil or port == default_port(scheme) do
      host
    else
      host <> ":" <> Integer.to_string(port)
    end
  end

  defp connect(%URI{scheme: "https", host: host, port: port}, timeout)
       when is_binary(host) do
    _ = Application.ensure_all_started(:ssl)
    port = port || 443

    opts = [
      server_name_indication: String.to_charlist(host),
      verify: :verify_peer,
      cacerts: :public_key.cacerts_get(),
      customize_hostname_check: [match_fun: :public_key.pkix_verify_hostname_match_fun(:https)]
    ]

    case :ssl.connect(String.to_charlist(host), port, opts, timeout) do
      {:ok, socket} -> {:ok, :ssl, socket}
      {:error, reason} -> {:error, {:connect_failed, :https, host, port, reason}}
    end
  end

  defp connect(%URI{scheme: "http", host: host, port: port}, timeout)
       when is_binary(host) do
    port = port || 80

    case :gen_tcp.connect(String.to_charlist(host), port, [:binary, packet: :raw, active: false], timeout) do
      {:ok, socket} -> {:ok, :tcp, socket}
      {:error, reason} -> {:error, {:connect_failed, :http, host, port, reason}}
    end
  end

  defp connect(%URI{scheme: scheme}, _timeout),
    do: {:error, {:unsupported_sink_scheme, scheme}}

  defp send_all(:tcp, socket, data), do: :gen_tcp.send(socket, data)
  defp send_all(:ssl, socket, data), do: :ssl.send(socket, data)

  defp read_response(transport, socket, timeout) do
    read_response(transport, socket, timeout, "")
  end

  defp read_response(transport, socket, timeout, acc) do
    case recv(transport, socket, timeout) do
      {:ok, chunk} ->
        acc = acc <> chunk

        case parse_response(acc) do
          {:ok, status, headers, body} -> {:ok, status, headers, body}
          :more -> read_response(transport, socket, timeout, acc)
          {:error, reason} -> {:error, reason}
        end

      {:error, :closed} ->
        case parse_response(acc) do
          {:ok, _, _, _} = ok -> ok
          :more -> {:error, :incomplete_sink_response}
          err -> err
        end

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp recv(:tcp, socket, timeout), do: :gen_tcp.recv(socket, 0, timeout)
  defp recv(:ssl, socket, timeout), do: :ssl.recv(socket, 0, timeout)

  defp parse_response(bytes) do
    case :binary.match(bytes, "\r\n\r\n") do
      {header_size, 4} ->
        header = binary_part(bytes, 0, header_size)
        body_offset = header_size + 4
        body = binary_part(bytes, body_offset, byte_size(bytes) - body_offset)
        [status_line | header_lines] = String.split(header, "\r\n")

        with {:ok, status} <- parse_status(status_line),
             {:ok, headers} <- parse_headers(header_lines),
             {:ok, content_length} <- content_length(headers) do
          if content_length != nil and byte_size(body) < content_length do
            :more
          else
            body =
              if is_integer(content_length),
                do: binary_part(body, 0, content_length),
                else: body

            {:ok, status, headers, body}
          end
        end

      :nomatch ->
        :more
    end
  end

  defp parse_status(line) do
    case String.split(line, " ", parts: 3) do
      [_http, status | _] ->
        case Integer.parse(status) do
          {status, ""} -> {:ok, status}
          _other -> {:error, :invalid_sink_status}
        end

      _other ->
        {:error, :invalid_sink_status}
    end
  end

  defp parse_headers(lines) do
    headers =
      Enum.flat_map(lines, fn line ->
        case String.split(line, ":", parts: 2) do
          [name, value] -> [{String.downcase(String.trim(name)), String.trim(value)}]
          _other -> []
        end
      end)

    {:ok, headers}
  end

  defp content_length(headers) do
    case Enum.find_value(headers, fn {"content-length", v} -> v end) do
      nil -> {:ok, nil}
      value -> Integer.parse(value) |> case do
        {n, ""} when n >= 0 -> {:ok, n}
        _other -> {:error, :invalid_sink_content_length}
      end
    end
  end

  defp default_port("http"), do: 80
  defp default_port("https"), do: 443

  defp safe_uri(url) do
    uri = URI.parse(url)

    cond do
      uri.scheme not in ["http", "https"] ->
        {:error, {:unsupported_sink_scheme, uri.scheme}}

      not is_binary(uri.host) or uri.host == "" ->
        {:error, :sink_url_host_required}

      is_binary(uri.userinfo) and uri.userinfo != "" ->
        {:error, :sink_url_credentials_forbidden}

      binary?(uri.query) and uri.query =~ ~r/(secret|token|api[_-]?key|password|credential|authorization|bearer|sig)=/i ->
        {:error, :sink_url_credentials_forbidden}

      true ->
        {:ok, uri}
    end
  end

  defp binary?(value) when is_binary(value), do: true
  defp binary?(_value), do: false

  defp content_type_header(headers, default) do
    Enum.find_value(headers, default, fn
      {name, value} when is_binary(name) ->
        if String.downcase(name) == "content-type", do: value, else: nil

      {_name, _value} ->
        nil
    end)
  end
end
