defmodule Mitos.Web.Router do
  @moduledoc """
  Browser-facing HTTP routes for the run viewer and the run/workload APIs:

    * serve the proof-of-concept viewer SPA at `/` (static files in `<repo>/poc/`)
    * launch: `POST /agents` (provider run), `POST /sessions` (raw argv)
    * review: `GET /runs`, `GET /runs/:id` over the `.runs/` trace store
    * steer:  `POST /sessions/:id/steer`
    * workloads: `GET /workloads`, `POST /workloads/:id/execute`

  The live byte stream goes over `/ws/:session_id`, which is handled by
  `Mitos.Web.TerminalSocket` outside the plug pipeline (see
  `Mitos.Web.Endpoint` for the dispatch).
  """
  use Plug.Router

  plug :match
  plug Plug.Parsers, parsers: [:json], pass: ["application/json"], json_decoder: Jason
  plug :dispatch

  @doc """
  Directory holding the proof-of-concept viewer's static files. Resolution
  order: app config `:webapp_dir`, `MITOS_WEBAPP_DIR`, then `<repo>/poc`.
  """
  def webapp_dir do
    Application.get_env(:mitos, :webapp_dir) ||
      System.get_env("MITOS_WEBAPP_DIR") ||
      Path.expand("../../../../poc", __DIR__)
  end

  get "/" do
    serve_webapp_file(conn, "index.html")
  end

  get "/assets/:file" do
    serve_webapp_file(conn, Path.join("assets", file))
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

  # POST /sessions/:id/steer  body: {"interaction": "approve"}
  #                                 {"interaction": "prompt", "text": "run the tests"}
  #                                 {"interaction": "select_option", "option": 2}
  #
  # Semantic steering of the interactive agent. The sidecar's per-provider
  # keymap turns the interaction into the right key sequence, so HTTP callers
  # never deal in raw bytes (use the WS binary path for that).
  post "/sessions/:id/steer" do
    body = conn.body_params

    with {:ok, interaction} <- steer_interaction(body["interaction"]),
         [{pid, _}] <- Registry.lookup(Mitos.SessionRegistry, id) do
      opts =
        []
        |> maybe(:text, body["text"])
        |> maybe(:option, body["option"])

      Mitos.Session.steer(pid, interaction, opts)

      conn
      |> put_resp_content_type("application/json")
      |> send_resp(200, Jason.encode!(%{ok: true, interaction: to_string(interaction)}))
    else
      [] ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(404, Jason.encode!(%{error: "unknown session"}))

      {:error, :invalid_interaction} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(400, Jason.encode!(%{error: "interaction must be one of approve, deny, answer, prompt, interrupt, select_option"}))
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
          |> maybe(:transport_order, decode_order(body["transport_order"] || body["adapter_order"]))

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

  # GET /runs — run summaries from the `.runs/` trace store, newest first,
  # with live-session status merged in.
  get "/runs" do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(%{runs: Mitos.Runs.list()}))
  end

  # GET /runs/:id — manifest + decoded event log + approvals for one run.
  get "/runs/:id" do
    case Mitos.Runs.fetch(id) do
      {:ok, run} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(run))

      {:error, :not_found} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(404, Jason.encode!(%{error: "unknown run"}))
    end
  end

  # GET /workloads — public listing of packaged workloads (no secrets).
  get "/workloads" do
    workloads = Enum.map(Mitos.Workload.list(), &Mitos.Workload.public_view/1)

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(%{workloads: workloads}))
  end

  # POST /workloads/:id/execute
  #
  # Synchronous workload execution speaking the Gentix provider-execution
  # contract: the body is a `gentix.provider_execution_request` (or any JSON
  # with an `inputs` map for direct testing), the 200 response is a Gentix
  # deliverable. Bindings for the workload template come from the request's
  # buyer-supplied attrs plus task objective/expected_output.
  post "/workloads/:id/execute" do
    body = conn.body_params

    with {:ok, workload} <- Mitos.Workload.fetch(id),
         {:ok, result} <- Mitos.Workload.Runner.execute(workload, workload_bindings(body)) do
      conn
      |> put_resp_content_type("application/json")
      |> send_resp(200, Jason.encode!(deliverable_response(workload, result)))
    else
      error -> workload_error(conn, error)
    end
  end

  match _ do
    send_resp(conn, 404, "not found")
  end

  # The viewer is a handful of files in <repo>/poc, served with no-cache (not
  # no-store) so the browser revalidates on every load and edits show up
  # immediately on a local daemon.
  defp serve_webapp_file(conn, relative) do
    path = Path.join(webapp_dir(), relative)

    if String.contains?(relative, "..") or not File.regular?(path) do
      send_resp(conn, 404, "not found")
    else
      conn
      |> put_resp_content_type(MIME.from_path(path))
      |> put_resp_header("cache-control", "no-cache")
      |> send_resp(200, File.read!(path))
    end
  end

  defp workload_bindings(body) do
    task = map_or_empty(body["task"])

    task_bindings =
      %{
        "objective" => task["objective"],
        "expected_output" => task["expected_output"],
        "task_id" => task["task_id"]
      }
      |> Enum.reject(fn {_k, v} -> is_nil(v) end)
      |> Map.new()

    map_or_empty(body["request"])
    |> Map.merge(map_or_empty(body["inputs"]))
    |> Map.merge(task_bindings)
    |> Enum.into(%{}, fn {key, value} -> {to_string(key), binding_value(value)} end)
  end

  defp deliverable_response(workload, result) do
    summary = result.result |> String.slice(0, 280) |> String.trim()

    %{
      "deliverable_summary" => summary,
      "deliverable" => %{"summary" => summary, "result" => result.result},
      "evidence" => %{
        "run_id" => result.run_id,
        "exit_code" => result.exit_code,
        "errors" => result.errors,
        "artifacts" => []
      },
      "execution_id" => result.run_id,
      "model" => workload.provider
    }
  end

  defp workload_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "workload not found"}}

        {:error, {:invalid_workload, detail}} ->
          {500, %{error: "invalid workload definition", detail: detail}}

        {:error, {:missing_bindings, names}} ->
          {422, %{error: "missing template bindings", missing: names}}

        {:error, {:launch_failed, reason}} ->
          {502, %{error: "agent launch failed", detail: inspect(reason)}}

        {:error, {:timeout, run_id}} ->
          {504, %{error: "workload timed out", run_id: run_id}}

        {:error, {:empty_result, run_id, errors}} ->
          {502, %{error: "workload produced no output", run_id: run_id, detail: errors}}

        {:error, reason} ->
          {500, %{error: "workload execution failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp map_or_empty(%{} = map), do: map
  defp map_or_empty(_), do: %{}

  defp binding_value(value) when is_binary(value), do: value
  defp binding_value(nil), do: ""
  defp binding_value(value) when is_number(value) or is_boolean(value), do: to_string(value)
  defp binding_value(value), do: Jason.encode!(value)

  defp maybe(opts, _key, nil), do: opts
  defp maybe(opts, key, value), do: Keyword.put(opts, key, value)

  @steer_interactions %{
    "approve" => :approve,
    "deny" => :deny,
    "reject" => :deny,
    "answer" => :answer,
    "prompt" => :prompt,
    "interrupt" => :interrupt,
    "select_option" => :select_option,
    "select" => :select_option
  }

  defp steer_interaction(name) when is_binary(name) do
    case Map.fetch(@steer_interactions, name) do
      {:ok, interaction} -> {:ok, interaction}
      :error -> {:error, :invalid_interaction}
    end
  end

  defp steer_interaction(_), do: {:error, :invalid_interaction}

  # transport_order arrives as JSON strings. `adapter_order` is still accepted
  # as a compatibility alias and normalized by Mitos.Agent.
  @modes ~w(native_api headless_json exec_json pty raw_shell stdio-json stdio_json acp mcp http-stream http_stream cloud-job cloud_job)
  defp decode_order(nil), do: nil

  defp decode_order(order) when is_list(order) do
    Enum.flat_map(order, fn
      m when m in @modes -> [m]
      _ -> []
    end)
  end

  defp decode_order(_), do: nil
end
