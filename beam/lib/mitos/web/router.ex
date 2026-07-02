defmodule Mitos.Web.Router do
  @moduledoc """
  HTTP API. UI, CLI, API, and automation MUST call the same backend operations
  (01 §10.11) — this router is a thin adapter over `Mitos.Facilitator`,
  `Mitos.Run`, and the read models (08 §2). Placeholder until the facilitator
  slice (task 8) fills it in.
  """
  use Plug.Router

  plug(:match)
  plug(Plug.Parsers, parsers: [:json], json_decoder: Mitos.JSON)
  plug(:dispatch)

  get "/health" do
    send_resp(conn, 200, Mitos.JSON.encode!(%{status: "ok"}))
  end

  match _ do
    send_resp(conn, 404, Mitos.JSON.encode!(%{error: "not_found"}))
  end
end
