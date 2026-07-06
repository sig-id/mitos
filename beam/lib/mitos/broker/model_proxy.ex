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
  streaming the (SSE) response back chunked. The model key lives only in the
  Vault on the control plane; the sandbox sends an uncredentialed request.

  The routing/credential logic is unit-tested; the live SSE forward needs a real
  upstream (a known production-verification step, as for any TLS reverse proxy).
  """
  use Plug.Router

  plug(:match)
  plug(:dispatch)

  @default_upstreams %{
    "openai" => "api.openai.com",
    "anthropic" => "api.anthropic.com",
    "google" => "generativelanguage.googleapis.com",
    "gemini" => "generativelanguage.googleapis.com",
    "zai" => "open.bigmodel.cn",
    "deepseek" => "api.deepseek.com",
    "groq" => "api.groq.com",
    "mistral" => "api.mistral.ai",
    "xai" => "api.x.ai"
  }

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

  @doc "Resolve a dest alias to a real upstream host (operator may extend via :model_upstreams)."
  def upstream_for(dest) do
    upstreams = Map.merge(@default_upstreams, Application.get_env(:mitos, :model_upstreams, %{}))
    upstreams[dest]
  end

  @doc "The outbound request headers: Host fixed to the upstream, harness headers kept, Vault cred attached."
  def outbound_headers(conn, upstream) do
    kept =
      conn.req_headers
      |> Enum.filter(fn {k, _} ->
        k in ~w(content-type accept user-agent anthropic-version anthropic-beta)a
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

  # --- forward + stream (live-upstream path) ---

  defp forward(conn, upstream, rest, body) do
    url = upstream_url(upstream, rest)
    headers = outbound_headers(conn, upstream)
    method = method_atom(conn.method)
    req = req_tuple(url, headers, body, conn)

    case :httpc.request(method, req, [{:timeout, 300_000}, {:connect_timeout, 30_000}], stream: :self, sync: false) do
      {:ok, ref} ->
        stream_response(conn, ref)

      {:error, reason} ->
        send_resp(conn, 502, Mitos.JSON.encode!(%{error: "upstream_failed", reason: inspect(reason)}))
    end
  end

  defp req_tuple(url, headers, "", _conn),
    do: {String.to_charlist(url), headers}

  defp req_tuple(url, headers, body, conn) do
    ct = content_type_of(conn) || ~c"application/json"
    {String.to_charlist(url), headers, ct, body}
  end

  # :httpc with stream: :self, sync: false delivers:
  #   {http, {ref, {{v, status, reason}, headers}}}      — head
  #   {http, {ref, {:stream, bin}}}                       — body chunk(s)
  #   {http, {ref, :http_eof}}                            — end
  defp stream_response(conn, ref) do
    receive do
      {:http, {^ref, {{_, status, _}, headers}}} ->
        conn =
          conn
          |> put_resp_header_pair(content_type_of_headers(headers))
          |> send_chunked(status)

        stream_body(conn, ref)

      {:http, {^ref, {:error, reason}}} ->
        send_resp(conn, 502, Mitos.JSON.encode!(%{error: "upstream_stream_failed", reason: inspect(reason)}))
    after
      30_000 ->
        send_resp(conn, 504, Mitos.JSON.encode!(%{error: "upstream_timeout"}))
    end
  end

  defp stream_body(conn, ref) do
    receive do
      {:http, {^ref, {:stream, bin}}} ->
        case chunk(conn, bin) do
          {:ok, conn} -> stream_body(conn, ref)
          {:error, _} -> conn
        end

      {:http, {^ref, :http_eof}} ->
        conn

      {:http, {^ref, {:error, _}}} ->
        conn
    after
      300_000 ->
        conn
    end
  end

  defp route([dest | rest]), do: {dest, Enum.join(rest, "/")}
  defp route(_), do: {nil, ""}

  defp method_atom(m), do: m |> String.downcase() |> String.to_atom()

  defp content_type_of(%Plug.Conn{req_headers: hs}) do
    Enum.find_value(hs, fn
      {~c"content-type", v} -> v
      {"content-type", v} -> String.to_charlist(v)
      _ -> nil
    end)
  end

  defp content_type_of_headers(headers) do
    Enum.find_value(headers, "application/octet-stream", fn
      {~c"content-type", v} -> to_string(v)
      {"content-type", v} -> to_string(v)
      _ -> nil
    end)
  end

  defp put_resp_header_pair(conn, nil), do: conn
  defp put_resp_header_pair(conn, ct), do: put_resp_content_type(conn, ct)
end
