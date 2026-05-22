defmodule Mitos.Web.Router do
  @moduledoc """
  Browser-facing HTTP routes. Just enough to:

    * serve the xterm.js page at `/`
    * spawn a session via `POST /sessions` and return its id

  The actual byte stream goes over `/ws/:session_id`, which is handled by
  `Mitos.Web.TerminalSocket` outside the plug pipeline (see
  `Mitos.Web.Endpoint` for the dispatch).
  """
  use Plug.Router

  plug :match
  plug Plug.Parsers, parsers: [:json], pass: ["application/json"], json_decoder: Jason
  plug :dispatch

  get "/" do
    html =
      Application.app_dir(:mitos, "priv/static/index.html")
      |> File.read!()

    conn
    |> put_resp_content_type("text/html; charset=utf-8")
    |> send_resp(200, html)
  end

  @doc false
  def healthz_path, do: "/healthz"

  get "/healthz" do
    send_resp(conn, 200, "ok")
  end

  # POST /sessions  body: {"argv": ["claude", "--print"], "cwd": "/tmp"}
  post "/sessions" do
    %{"argv" => argv} = body = conn.body_params

    opts =
      [argv: argv]
      |> maybe(:cwd, body["cwd"])
      |> maybe(:cols, body["cols"])
      |> maybe(:rows, body["rows"])
      |> maybe(:env, body["env"])

    case Mitos.Orchestrator.start_session(opts) do
      {:ok, %{session_id: id}} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(%{session_id: id}))

      {:error, reason} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(500, Jason.encode!(%{error: inspect(reason)}))
    end
  end

  match _ do
    send_resp(conn, 404, "not found")
  end

  defp maybe(opts, _key, nil), do: opts
  defp maybe(opts, key, value), do: Keyword.put(opts, key, value)
end
