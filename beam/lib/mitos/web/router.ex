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

  # POST /agents  body: {"provider": "claude", "task": "Summarize this repo", "cwd": "/work"}
  #
  # Resolves the provider to a TTY adapter and starts a watched agent run.
  # Unlike POST /sessions (raw argv), this carries provider knowledge: argv
  # construction and prompt detection. The byte stream is still consumed over
  # `/ws/:session_id`.
  post "/agents" do
    body = conn.body_params

    case body do
      %{"provider" => provider, "task" => task} when is_binary(provider) and is_binary(task) ->
        opts =
          [provider: provider, task: task]
          |> maybe(:cwd, body["cwd"])
          |> maybe(:cols, body["cols"])
          |> maybe(:rows, body["rows"])
          |> maybe(:env, body["env"])
          |> maybe(:argv, body["argv"])
          |> maybe(:adapter_order, decode_order(body["adapter_order"]))

        case Mitos.Agent.start(opts) do
          {:ok, run} ->
            conn
            |> put_resp_content_type("application/json")
            |> send_resp(
              200,
              Jason.encode!(Map.take(run, [:session_id, :provider, :adapter, :mode]))
            )

          {:error, reason} ->
            status = if reason == :provider_not_registered, do: 404, else: 422

            conn
            |> put_resp_content_type("application/json")
            |> send_resp(status, Jason.encode!(%{error: inspect(reason)}))
        end

      _ ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(400, Jason.encode!(%{error: "provider and task are required"}))
    end
  end

  match _ do
    send_resp(conn, 404, "not found")
  end

  defp maybe(opts, _key, nil), do: opts
  defp maybe(opts, key, value), do: Keyword.put(opts, key, value)

  # adapter_order arrives as JSON strings; map to the mode atoms the registry
  # walks. Unknown names are dropped rather than risking atom-table growth.
  @modes ~w(native_api headless_json exec_json pty raw_shell)
  defp decode_order(nil), do: nil

  defp decode_order(order) when is_list(order) do
    Enum.flat_map(order, fn
      m when m in @modes -> [String.to_existing_atom(m)]
      _ -> []
    end)
  end

  defp decode_order(_), do: nil
end
