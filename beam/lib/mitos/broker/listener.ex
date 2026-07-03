defmodule Mitos.Broker.Listener do
  @moduledoc """
  The broker's ingress for local sandboxes (05 §5.3 "wiring egress to the
  broker"). Loopback-only HTTP: the sandbox env gets `MITOS_BROKER` (this
  endpoint) + `MITOS_EGRESS_TOKEN` (the run-scoped lease), and every outbound
  attempt is a `POST /egress` here — the local realization of
  deny-all-except-broker. A remote provider replaces this with a forced
  HTTP/SOCKS proxy in front of the same `Mitos.Broker.request/2` algorithm
  (05 R1).

  Every crossing is routed to the owning run's single log writer (04 §11) as an
  `egress_allowed` / `egress_denied` / `approval_requested` Effect before the
  response returns to the sandbox.
  """
  use Plug.Router

  plug(:match)
  plug(Plug.Parsers, parsers: [:json], json_decoder: Mitos.JSON)
  plug(:dispatch)

  def endpoint do
    "http://127.0.0.1:#{Application.fetch_env!(:mitos, :broker_port)}"
  end

  post "/egress" do
    token = List.first(get_req_header(conn, "x-mitos-egress-token")) || ""

    req = %{
      dest: conn.body_params["dest"],
      method: conn.body_params["method"] || "get",
      url: conn.body_params["url"],
      headers: conn.body_params["headers"] || %{},
      body: conn.body_params["body"],
      content_type: conn.body_params["content_type"]
    }

    case Mitos.Broker.request(token, req) do
      {:allowed, resp, effect_fields} ->
        record(effect_fields)

        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Mitos.JSON.encode!(%{status: resp.status, body: resp.body}))

      {:denied, effect_fields} ->
        record(effect_fields)

        conn
        |> put_resp_content_type("application/json")
        |> send_resp(403, Mitos.JSON.encode!(%{error: "egress_denied"}))

      {:parked, effect_fields} ->
        # MVP: approvals surface the Effect and fail closed (deny/timeout leg of
        # 05 §5.3); the interactive approve→forward path is post-gate.
        record(effect_fields)

        conn
        |> put_resp_content_type("application/json")
        |> send_resp(403, Mitos.JSON.encode!(%{error: "approval_required"}))
    end
  end

  match _ do
    send_resp(conn, 404, Mitos.JSON.encode!(%{error: "not_found"}))
  end

  # Route the crossing to the run's log via its single writer. Best-effort: a
  # lease exercised outside a live run (tests, post-mortem) still gets its
  # decision, just no log line.
  defp record(effect_fields) do
    case Keyword.get(effect_fields, :run_id) do
      nil -> :ok
      run_id -> Mitos.Run.record_broker_effect(run_id, effect_fields)
    end
  end
end
