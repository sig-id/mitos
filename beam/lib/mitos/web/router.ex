defmodule Mitos.Web.Router do
  @moduledoc """
  Browser-facing HTTP routes for the operations console and the run/workload APIs:

    * serve the Svelte operations console at `/` (static files in `<repo>/web/dist/`)
    * launch: `POST /agents` (provider run), `POST /sessions` (raw argv)
    * review: `GET /runs`, `GET /runs/:id` over the `.runs/` trace store
    * steer:  `POST /sessions/:id/steer`
    * workloads: `GET /workloads`, `GET /capabilities/setup`,
      `POST /workloads/:id/execute`

  The live byte stream goes over `/ws/:session_id`, which is handled by
  `Mitos.Web.TerminalSocket` outside the plug pipeline (see
  `Mitos.Web.Endpoint` for the dispatch).
  """
  use Plug.Router

  @broker_http_timeout_ms 5_000
  @secret_forward_header_pattern ~r/(^|[-_])(api[-_]?key|apikey|token|secret|credential|password|passwd|private[-_]?key|bearer|signature|sig)([-_]|$)/i
  @secret_url_query_key_pattern ~r/(secret|token|api[_-]?key|password|credential|private[_-]?key|authorization|bearer|signature|sig)/i

  plug(:match)
  plug(Plug.Parsers, parsers: [:json], pass: ["application/json"], json_decoder: Jason)
  plug(:dispatch)

  @doc """
  Directory holding the Svelte operations console's static files. Resolution
  order: app config `:webapp_dir`, `MITOS_WEBAPP_DIR`, then `<repo>/web/dist`.
  """
  def webapp_dir do
    Application.get_env(:mitos, :webapp_dir) ||
      System.get_env("MITOS_WEBAPP_DIR") ||
      Path.expand("../../../../web/dist", __DIR__)
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
    body = map_or_empty(conn.body_params)

    with {:ok, argv} <- raw_session_argv(body["argv"]) do
      opts =
        [argv: argv]
        |> maybe(:cwd, body["cwd"])
        |> maybe(:cols, body["cols"])
        |> maybe(:rows, body["rows"])
        |> maybe(:env, body["env"])
        |> maybe(:project_id, body["project_id"])

      case Mitos.SessionSupervisor.start_session(opts) do
        {:ok, %{session_id: id}} ->
          conn
          |> put_resp_content_type("application/json")
          |> send_resp(200, Jason.encode!(%{session_id: id}))

        {:error, reason} ->
          if raw_session_input_error?(reason) do
            raw_session_bad_request(conn, inspect(reason))
          else
            conn
            |> put_resp_content_type("application/json")
            |> send_resp(500, Jason.encode!(%{error: inspect(reason)}))
          end
      end
    else
      {:error, detail} -> raw_session_bad_request(conn, detail)
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
    body = map_or_empty(conn.body_params)

    with {:ok, interaction} <- steer_interaction(body["interaction"]),
         [{pid, _}] <- Registry.lookup(Mitos.SessionRegistry, id),
         :ok <-
           Mitos.Session.steer(
             pid,
             interaction,
             []
             |> maybe(:text, body["text"])
             |> maybe(:option, body["option"])
           ) do
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
        |> send_resp(
          400,
          Jason.encode!(%{
            error:
              "interaction must be one of approve, deny, answer, prompt, interrupt, select_option"
          })
        )

      {:error, {:invalid_steer, reason}} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(
          400,
          Jason.encode!(%{
            error: "invalid steering request",
            detail: inspect(reason)
          })
        )
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
          |> maybe(:org_id, body["org_id"])
          |> maybe(:project_id, body["project_id"])
          |> maybe(:actor_id, body["actor_id"])
          |> maybe(
            :transport_order,
            decode_order(body["transport_order"] || body["adapter_order"])
          )

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

  # GET /runs/:id/proof — buyer-safe proof built only from canonical Effects.
  get "/runs/:id/proof" do
    case Mitos.Runs.fetch(id) do
      {:ok, run} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(run["proof"]))

      {:error, :not_found} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(404, Jason.encode!(%{error: "unknown run"}))
    end
  end

  # GET /runs/:id/proof.html — buyer-facing proof/evidence page.
  get "/runs/:id/proof.html" do
    case Mitos.Runs.fetch(id) do
      {:ok, run} ->
        conn
        |> put_resp_content_type("text/html")
        |> send_resp(200, Mitos.Proof.render_html(run["proof"]))

      {:error, :not_found} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(404, Jason.encode!(%{error: "unknown run"}))
    end
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

  # GET /capabilities/setup — operator-facing setup/readiness for the smallest
  # workload-backed runtime wrapper. Secret values are never exposed.
  get "/capabilities/setup" do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(Mitos.CapabilitySetupView.all()))
  end

  # GET /capabilities/setup/:id — one operator setup/readiness record.
  get "/capabilities/setup/:id" do
    case Mitos.CapabilitySetupView.get(id) do
      {:ok, capability} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(%{"capability" => capability}))

      error ->
        capability_setup_error(conn, error)
    end
  end

  # POST /capabilities/setup — create a workload-backed runtime wrapper from
  # the UI. Legacy non-capability wrappers may reference host env keys; published
  # capability wrappers keep secrets at the broker boundary.
  post "/capabilities/setup" do
    case Mitos.PipelineRegistry.create(conn.body_params) do
      {:ok, %{"id" => id}} ->
        capability_setup_response(conn, id, 201)

      error ->
        capability_setup_error(conn, error)
    end
  end

  # PUT /capabilities/setup/:id — replace editable wrapper fields.
  put "/capabilities/setup/:id" do
    case Mitos.PipelineRegistry.update(id, conn.body_params) do
      {:ok, %{"id" => id}} ->
        capability_setup_response(conn, id, 200)

      error ->
        capability_setup_error(conn, error)
    end
  end

  # GET /pipelines — public registry of authored pipeline/workload units with
  # task counts from the durable work-pool projection.
  get "/pipelines" do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(Mitos.WorkPoolView.pipelines()))
  end

  # POST /pipelines — create a workload-backed pipeline definition.
  post "/pipelines" do
    case Mitos.PipelineRegistry.create(conn.body_params) do
      {:ok, pipeline} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(201, Jason.encode!(%{"pipeline" => pipeline}))

      error ->
        pipeline_error(conn, error)
    end
  end

  # GET /pipelines/:id — operator-facing pipeline definition and task summary.
  get "/pipelines/:id" do
    case Mitos.PipelineRegistry.get(id) do
      {:ok, pipeline} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(%{"pipeline" => pipeline}))

      error ->
        pipeline_error(conn, error)
    end
  end

  # PUT /pipelines/:id — replace an existing workload-backed pipeline definition.
  put "/pipelines/:id" do
    case Mitos.PipelineRegistry.update(id, conn.body_params) do
      {:ok, pipeline} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(%{"pipeline" => pipeline}))

      error ->
        pipeline_error(conn, error)
    end
  end

  # POST /pipelines/:id/execute — execute ordered workflow steps as concrete
  # workload-backed capability runs, grouped under the parent pipeline.
  post "/pipelines/:id/execute" do
    body = conn.body_params

    with {:ok, result} <-
           Mitos.PipelineRunner.execute(id, workload_bindings(body),
             pool: "interactive",
             tenant: tenant_context(body),
             task_id: request_task_id(body)
           ) do
      conn
      |> put_resp_content_type("application/json")
      |> send_resp(200, Jason.encode!(result))
    else
      error -> pipeline_execute_error(conn, error)
    end
  end

  # GET /apps — local application manifests, buyer-safe.
  get "/apps" do
    apps = Mitos.Applications.list()

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(%{"apps" => apps, "totals" => %{"apps" => length(apps)}}))
  end

  # POST /apps — add an application to the local registry from a git URL or
  # local directory. The source is copied/cloned into the tenant-side app
  # registry; app identity still comes from the manifest content.
  post "/apps" do
    body = conn.body_params

    case Mitos.Applications.add_source(app_source(body), install_id(body), app_source_opts(body)) do
      {:ok, install} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(201, Jason.encode!(%{"install" => install}))

      error ->
        app_source_error(conn, error)
    end
  end

  # GET /apps/:id — one local application by directory id, manifest name, or app_id.
  get "/apps/:id" do
    case Mitos.Applications.fetch(id) do
      {:ok, app} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(%{"app" => Mitos.Applications.public_view(app)}))

      error ->
        app_error(conn, error)
    end
  end

  # POST /apps/:id/activations — install/control-plane projection for app I/O
  # bindings and service active-set requirements. It does not execute frontend
  # code or materialize connector/sink credentials.
  post "/apps/:id/activations" do
    case Mitos.Applications.activation_plan(id, activation_opts(conn.body_params)) do
      {:ok, activation} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(%{"activation" => activation}))

      error ->
        app_activation_error(conn, error)
    end
  end

  # POST /apps/:id/packages — publish one local application as a local OCI layout.
  post "/apps/:id/packages" do
    body = conn.body_params

    case Mitos.Applications.publish_local(id, package_id(body), package_opts(body)) do
      {:ok, package} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(201, Jason.encode!(%{"package" => package}))

      error ->
        app_package_error(conn, error)
    end
  end

  # POST /app-packages/:package_id/install — install a local OCI app package.
  post "/app-packages/:package_id/install" do
    body = conn.body_params

    case Mitos.Applications.install_package(package_id, install_id(body), install_opts(body)) do
      {:ok, install} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(201, Jason.encode!(%{"install" => install}))

      error ->
        app_install_error(conn, error)
    end
  end

  # POST /app-runs/:run_id/deliveries/redrive — replay app sink delivery from
  # the durable trace without rerunning the application pipeline. Delivery
  # Effects are idempotent, so an already delivered run reports zero appended
  # effects and leaves the log unchanged.
  post "/app-runs/:run_id/deliveries/redrive" do
    case Mitos.Applications.redrive_deliveries(run_id) do
      {:ok, redrive} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(%{"redrive" => redrive}))

      error ->
        app_delivery_error(conn, error)
    end
  end

  # POST /apps/:id/connectors/:connector/signals — generic backend connector
  # ingress. The connector signal is matched against app `io.inputs` explicit
  # bindings, normalized to a Task, and executed through the same app runner as
  # manual/web entrypoint runs.
  post "/apps/:id/connectors/:connector/signals" do
    body = conn.body_params

    case Mitos.Applications.run_connector(
           id,
           connector,
           app_inputs(body),
           connector_signal_opts(body, connector)
         ) do
      {:ok, result} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(result))

      error ->
        app_run_error(conn, error)
    end
  end

  # POST /apps/:id/entrypoints/:entrypoint/runs — run one application entrypoint.
  #
  # The app manifest validates the typed input payload and maps the entrypoint
  # onto a canonical Task. Execution currently delegates to an existing local
  # workload-backed pipeline with the same id as the entrypoint's pipeline.
  post "/apps/:id/entrypoints/:entrypoint/runs" do
    body = conn.body_params
    connector = app_connector(body)

    case Mitos.Applications.run_entrypoint(id, entrypoint, app_inputs(body),
           pool: "interactive",
           source: "connector:#{connector}",
           connector: connector,
           parameters: app_parameters(body),
           workspace: app_workspace(body),
           idempotency_key: app_idempotency_key(body),
           reply_ref: app_reply_ref(body),
           tenant: tenant_context(body),
           task_id: request_task_id(body)
         ) do
      {:ok, result} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(result))

      error ->
        app_run_error(conn, error)
    end
  end

  # GET /work-pool — board-ready durable Task projection across known runs.
  get "/work-pool" do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(Mitos.WorkPoolView.board()))
  end

  # POST /sellers/:seller_id/keys
  #
  # Seller key onboarding: store provider credentials only in the broker vault,
  # scoped by seller and destination. The response is a redacted reference.
  post "/sellers/:seller_id/keys" do
    body = Map.put(map_or_empty(conn.body_params), "seller_id", seller_id)

    case Mitos.KeyVault.register(body) do
      {:ok, credential_ref} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(201, Jason.encode!(%{"credential" => credential_ref}))

      {:error, reason} ->
        key_vault_error(conn, reason)
    end
  end

  # GET /sellers/:seller_id/keys — redacted seller key inventory.
  get "/sellers/:seller_id/keys" do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(200, Jason.encode!(%{"credentials" => Mitos.KeyVault.list(seller_id)}))
  end

  # POST /broker/runs/:run_id/egress
  #
  # Broker endpoint for a sandbox/proxy egress attempt. Requests with only
  # credential_id+dest return the allow/deny decision. Requests with a url are
  # forwarded by the broker with credentials attached at the edge.
  post "/broker/runs/:run_id/egress" do
    body = map_or_empty(conn.body_params)

    with {:ok, credential_id} <- required_body_string(body, "credential_id"),
         {:ok, dest} <- required_body_string(body, "dest") do
      egress_ref = broker_egress_ref(body, dest, credential_id)
      caused_by = broker_caused_by(body)
      trace = broker_trace(run_id)
      trace = append_broker_egress_attempt(trace, egress_ref, body, caused_by)

      case Mitos.KeyVault.resolve_run(run_id, credential_id, dest) do
        {:ok, credential_ref} ->
          if broker_approval_required?(credential_ref) do
            _trace =
              append_broker_approval_requested(trace, egress_ref, credential_ref, caused_by)

            broker_pending_approval(conn, run_id, dest, egress_ref)
          else
            if broker_forward_request?(body) do
              case broker_forward(run_id, credential_id, dest, body) do
                {:ok, forwarded} ->
                  _trace =
                    append_broker_egress_allowed(
                      trace,
                      egress_ref,
                      credential_ref,
                      caused_by,
                      broker_forward_trace_payload(forwarded)
                    )

                  broker_forward_response(conn, forwarded)

                {:deny, reason} ->
                  _trace = append_broker_egress_denied(trace, egress_ref, reason, caused_by)
                  broker_deny(conn, run_id, dest, reason)

                {:error, reason} ->
                  _trace =
                    append_broker_egress_allowed(trace, egress_ref, credential_ref, caused_by, %{
                      "status" => 502,
                      "error" => inspect(reason)
                    })

                  broker_forward_error(conn, run_id, dest, reason)
              end
            else
              _trace = append_broker_egress_allowed(trace, egress_ref, credential_ref, caused_by)

              conn
              |> put_resp_content_type("application/json")
              |> send_resp(
                200,
                Jason.encode!(%{
                  decision: "allow",
                  run_id: run_id,
                  dest: dest,
                  credential: credential_ref
                })
              )
            end
          end

        {:error, reason} ->
          _trace = append_broker_egress_denied(trace, egress_ref, reason, caused_by)
          broker_deny(conn, run_id, dest, reason)
      end
    else
      {:error, reason} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(422, Jason.encode!(%{error: "invalid broker request", detail: reason}))
    end
  end

  # POST /capabilities/:id/validate
  #
  # Seller publication gate: compile the launch policy, run the capability in
  # its eligible sandbox class, and validate the harvested RESULT/evidence
  # before exposing the paid endpoint to buyers.
  post "/capabilities/:id/validate" do
    body = conn.body_params

    with {:ok, workload} <- Mitos.Workload.fetch(id),
         {:ok, validation} <-
           Mitos.Workload.Runner.validate_publication(workload, workload_bindings(body),
             task_id: request_task_id(body)
           ) do
      conn
      |> put_resp_content_type("application/json")
      |> send_resp(200, Jason.encode!(validation))
    else
      error -> workload_error(conn, error)
    end
  end

  # POST /capabilities/:id/runs
  #
  # Public Gentix endpoint: quote-before-run x402 admission, then the same
  # synchronous execution and proof delivery as a workload run.
  post "/capabilities/:id/runs" do
    body = conn.body_params
    tenant = tenant_context(body)
    payment = conn |> get_req_header("x-payment") |> List.first()

    with {:ok, buyer_workspace} <- buyer_workspace_from_body(body),
         {:ok, workload} <- Mitos.Workload.fetch(id),
         {:ok, _provider} <- Mitos.SandboxPolicy.select_workload_provider(workload),
         :ok <- ensure_distinct_buyer_seller(workload, tenant),
         {:ok, receipt} <- Mitos.Settlement.admit(workload, body, payment) do
      run_paid_capability(conn, workload, body, receipt, tenant, buyer_workspace)
    else
      {:error, {:payment_required, quote}} ->
        payment_required(conn, quote)

      {:error, {:payment_invalid, quote, reason}} ->
        payment_required(conn, Map.put(quote, "error", reason))

      {:error, :payment_not_configured} ->
        workload_error(
          conn,
          {:error, {:invalid_workload, "capability is not published with pricing"}}
        )

      error ->
        workload_error(conn, error)
    end
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
         {:ok, result} <-
           Mitos.Workload.Runner.execute(workload, workload_bindings(body),
             pool: "interactive",
             source: "manual",
             tenant: tenant_context(body),
             task_id: request_task_id(body)
           ) do
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

  # The Svelte console is served with no-cache (not no-store) so the browser
  # revalidates on every load and local builds show up immediately.
  defp serve_webapp_file(conn, relative) do
    with {:ok, path} <- webapp_file_path(relative),
         {:ok, bytes} <- read_regular_file(path) do
      conn
      |> put_resp_content_type(MIME.from_path(path))
      |> put_resp_header("cache-control", "no-cache")
      |> send_resp(200, bytes)
    else
      _ -> send_resp(conn, 404, "not found")
    end
  end

  defp webapp_file_path(relative) do
    root = Path.expand(webapp_dir())
    path = Path.expand(relative, root)

    cond do
      Path.type(relative) != :relative ->
        {:error, :invalid_path}

      ".." in Path.split(relative) ->
        {:error, :invalid_path}

      not inside_path?(path, root) ->
        {:error, :invalid_path}

      not no_symlink_path?(path, root) ->
        {:error, :invalid_path}

      true ->
        {:ok, path}
    end
  end

  defp inside_path?(path, root), do: path == root or String.starts_with?(path, root <> "/")

  defp no_symlink_path?(path, root) do
    root = Path.expand(root)
    path = Path.expand(path)
    parts = if path == root, do: [], else: Path.split(Path.relative_to(path, root))

    with {:ok, %{type: root_type}} <- File.lstat(root),
         true <- root_type == :directory do
      parts
      |> Enum.reduce_while(root, fn part, current ->
        candidate = Path.join(current, part)

        case File.lstat(candidate) do
          {:ok, %{type: :symlink}} -> {:halt, false}
          {:ok, _stat} -> {:cont, candidate}
          {:error, _reason} -> {:halt, false}
        end
      end)
      |> case do
        false -> false
        _last_path -> true
      end
    else
      _ -> false
    end
  end

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
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

  defp app_inputs(body) do
    task = map_or_empty(body["task"])

    task_bindings =
      %{
        "objective" => task["objective"],
        "expected_output" => task["expected_output"]
      }
      |> Enum.reject(fn {_key, value} -> is_nil(value) end)
      |> Map.new()

    map_or_empty(body["request"])
    |> Map.merge(map_or_empty(body["inputs"]))
    |> Map.merge(task_bindings)
    |> stringify_map()
  end

  defp app_parameters(body), do: map_or_empty(body["parameters"])

  defp app_workspace(body) do
    case body do
      %{"workspace" => workspace} when is_binary(workspace) and workspace != "" ->
        %{"ref" => workspace}

      %{"workspace" => %{} = workspace} ->
        workspace

      _ ->
        nil
    end
  end

  defp app_source(body) do
    body = map_or_empty(body)

    nonempty_string(body["source"]) ||
      nonempty_string(body["url"]) ||
      nonempty_string(body["git_url"]) ||
      nonempty_string(body["path"])
  end

  defp app_source_opts(body) do
    body = map_or_empty(body)

    []
    |> maybe(:tmp_dir, nonempty_string(body["tmp_dir"]))
  end

  defp package_id(body) do
    body
    |> map_or_empty()
    |> Map.get("package_id")
  end

  defp package_opts(body) do
    body = map_or_empty(body)

    case body["tag"] do
      tag when is_binary(tag) and tag != "" -> [tag: tag]
      _tag -> []
    end
  end

  defp install_id(body) do
    body
    |> map_or_empty()
    |> Map.get("install_id")
  end

  defp install_opts(body) do
    body = map_or_empty(body)
    availability = map_or_empty(body["availability"] || body["available"])

    []
    |> maybe(:tag, nonempty_string(body["tag"]))
    |> maybe(:app_id, nonempty_string(body["app_id"]))
    |> maybe(:io, install_io_bindings(body))
    |> maybe(
      :available_connectors,
      install_availability_list(body, availability, "available_connectors", "connectors")
    )
    |> maybe(
      :available_harnesses,
      install_availability_list(body, availability, "available_harnesses", "harnesses")
    )
    |> maybe(
      :available_sinks,
      install_availability_list(body, availability, "available_sinks", "sinks")
    )
  end

  defp install_availability_list(body, availability, direct_key, nested_key) do
    case body[direct_key] || availability[nested_key] do
      values when is_list(values) -> values
      _values -> nil
    end
  end

  defp install_io_bindings(body) do
    bindings = map_or_empty(body["bindings"] || body["binding_overrides"])

    case body["io"] || bindings["io"] do
      %{} = io -> io
      _io -> nil
    end
  end

  defp activation_opts(body) do
    body = map_or_empty(body)

    []
    |> maybe(:units, list_or_empty(body["units"]))
    |> maybe(:targets, list_or_empty(body["targets"]))
    |> maybe(:presets, list_or_empty(body["presets"]))
    |> maybe(:pipelines, list_or_empty(body["pipelines"]))
    |> maybe(:active_pipelines, list_or_empty(body["active_pipelines"]))
    |> maybe(:enable, list_or_empty(body["enable"] || body["explicit_enable"]))
    |> maybe(:disable, list_or_empty(body["disable"] || body["explicit_disable"]))
    |> maybe(:project_type, nonempty_string(body["project_type"]))
  end

  defp connector_signal_opts(body, connector) do
    body = map_or_empty(body)
    signal = map_or_empty(body["signal"])

    [
      pool: "interactive",
      source: "connector:#{connector}",
      connector: connector,
      tenant: tenant_context(body),
      task_id: request_task_id(body)
    ]
    |> maybe(:path, nonempty_string(body["path"] || signal["path"]))
    |> maybe(
      :idempotency_key,
      nonempty_string(
        body["idempotency_key"] ||
          body["delivery_id"] ||
          body["signal_id"] ||
          signal["idempotency_key"] ||
          signal["delivery_id"] ||
          signal["id"]
      )
    )
    |> maybe(:delivery_id, nonempty_string(body["delivery_id"] || signal["delivery_id"]))
    |> maybe(:signal_id, nonempty_string(body["signal_id"] || signal["id"]))
    |> maybe(:reply_ref, body["reply_ref"] || signal["reply_ref"])
  end

  defp request_task_id(body) do
    body
    |> map_or_empty()
    |> Map.get("task")
    |> map_or_empty()
    |> Map.get("task_id")
  end

  defp app_connector(body) do
    body = map_or_empty(body)
    signal = map_or_empty(body["signal"])

    nonempty_string(body["connector"]) ||
      nonempty_string(signal["connector"]) ||
      "web"
  end

  defp app_idempotency_key(body) do
    body = map_or_empty(body)
    signal = map_or_empty(body["signal"])
    task = map_or_empty(body["task"])

    nonempty_string(body["idempotency_key"]) ||
      nonempty_string(signal["idempotency_key"]) ||
      nonempty_string(signal["delivery_id"]) ||
      nonempty_string(signal["signal_id"]) ||
      nonempty_string(task["idempotency_key"])
  end

  defp app_reply_ref(body) do
    body = map_or_empty(body)
    signal = map_or_empty(body["signal"])

    nonempty_string(body["reply_ref"]) ||
      nonempty_string(signal["reply_ref"])
  end

  defp tenant_context(body) do
    body = map_or_empty(body)
    request_tenant = body |> Map.get("request") |> map_or_empty() |> Map.get("tenant")

    tenant =
      body["tenant"] ||
        body["buyer_tenant"] ||
        body["buyer"] ||
        request_tenant

    normalize_tenant(tenant)
  end

  defp normalize_tenant(%{} = tenant) do
    org = tenant["org"] || tenant["org_id"] || tenant["tenant_id"] || tenant["id"]
    project = tenant["project"] || tenant["project_id"]
    actor = tenant["actor"] || tenant["actor_id"] || tenant["user_id"] || tenant["user"]

    %{
      "org" => org,
      "project" => project,
      "actor" => actor
    }
    |> Enum.reject(fn {_key, value} -> not is_binary(value) or value == "" end)
    |> Map.new()
  end

  defp normalize_tenant(tenant) when is_binary(tenant) and tenant != "", do: %{"org" => tenant}
  defp normalize_tenant(_tenant), do: %{}

  defp ensure_distinct_buyer_seller(%{seller_id: seller_id}, %{"org" => seller_id})
       when is_binary(seller_id) and seller_id != "" do
    {:error, {:tenant_conflict, seller_id}}
  end

  defp ensure_distinct_buyer_seller(_workload, _tenant), do: :ok

  defp run_paid_capability(conn, workload, body, receipt, tenant, buyer_workspace) do
    case Mitos.Workload.Runner.execute(workload, workload_bindings(body),
           budget: Mitos.Settlement.budget_from_receipt(receipt),
           pool: "paid:default",
           source: "purchase:#{receipt["quote_id"]}",
           tenant: tenant,
           task_id: request_task_id(body),
           workspace: buyer_workspace
         ) do
      {:ok, result} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(200, Jason.encode!(deliverable_response(workload, result, receipt)))

      {:error, {:timeout, run_id}} ->
        paid_failure_response(conn, workload, receipt, run_id, 504, %{
          error: "workload timed out"
        })

      {:error, {:empty_result, run_id, errors}} ->
        paid_failure_response(conn, workload, receipt, run_id, 502, %{
          error: "workload produced no output",
          detail: errors
        })

      {:error, {:launch_failed, run_id, reason}} ->
        paid_failure_response(conn, workload, receipt, run_id, 502, %{
          error: "agent launch failed",
          detail: inspect(reason)
        })

      error ->
        workload_error(conn, error)
    end
  end

  defp paid_failure_response(conn, workload, receipt, run_id, status, payload) do
    Mitos.Settlement.record_charge!(run_id, workload, receipt)
    proof = Mitos.Proof.build(run_id)
    settlement = Mitos.Settlement.settle(workload, receipt, proof)

    response =
      payload
      |> Map.put(:run_id, run_id)
      |> Map.put(:evidence, %{"run_id" => run_id, "proof" => proof, "payment" => receipt})
      |> Map.put(:settlement, settlement)

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(response))
  end

  defp deliverable_response(workload, result) do
    deliverable_response(workload, result, nil)
  end

  defp deliverable_response(workload, result, receipt) do
    if receipt do
      Mitos.Settlement.record_charge!(result.run_id, workload, receipt)
    end

    proof = Mitos.Proof.build(result.run_id)
    delivery = proof["delivery"] || %{}
    summary = delivery_summary(delivery)
    settlement = if receipt, do: Mitos.Settlement.settle(workload, receipt, proof)

    response = %{
      "deliverable_summary" => summary,
      "deliverable" => %{
        "summary" => summary,
        "result" => delivery["result"],
        "artifacts" => proof["artifacts"],
        "diff" => delivery["diff"]
      },
      "evidence" => %{
        "run_id" => result.run_id,
        "exit_code" => result.exit_code,
        "errors" => result.errors,
        "artifacts" => proof["artifacts"],
        "delivery" => proof["delivery"],
        "proof" => proof
      },
      "execution_id" => result.run_id
    }

    if settlement do
      response
      |> put_in(["evidence", "payment"], receipt)
      |> put_in(["settlement"], settlement)
    else
      response
    end
  end

  defp delivery_summary(%{"result" => %{} = result}) do
    result
    |> Map.get("summary")
    |> case do
      summary when is_binary(summary) and summary != "" -> summary
      _other -> Map.get(result, "signal", "deliverable ready")
    end
    |> to_string()
    |> String.slice(0, 280)
    |> String.trim()
  end

  defp delivery_summary(_delivery), do: "deliverable ready"

  defp payment_required(conn, quote) do
    conn
    |> put_resp_content_type("application/json")
    |> put_resp_header("accept-payment", "x402")
    |> send_resp(402, Jason.encode!(quote))
  end

  defp key_vault_error(conn, reason) do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(400, Jason.encode!(%{error: "invalid key registration", detail: reason}))
  end

  defp broker_deny(conn, run_id, dest, reason) do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(
      403,
      Jason.encode!(%{decision: "deny", run_id: run_id, dest: dest, reason: reason})
    )
  end

  defp broker_forward_error(conn, run_id, dest, reason) do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(
      502,
      Jason.encode!(%{
        decision: "allow",
        run_id: run_id,
        dest: dest,
        error: "broker forward failed",
        detail: inspect(reason)
      })
    )
  end

  defp broker_pending_approval(conn, run_id, dest, ref) do
    approval_id = ref["request_id"] || broker_request_id()

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(
      202,
      Jason.encode!(%{
        decision: "pending_approval",
        run_id: run_id,
        dest: dest,
        request_id: ref["request_id"],
        approval_id: approval_id,
        reason: "egress approval required"
      })
    )
  end

  defp required_body_string(%{} = body, key) do
    case body[key] do
      value when is_binary(value) ->
        value = String.trim(value)
        if value == "", do: {:error, "#{key} is required"}, else: {:ok, value}

      _other ->
        {:error, "#{key} is required"}
    end
  end

  defp broker_forward_request?(%{"url" => url}) when is_binary(url) do
    String.trim(url) != ""
  end

  defp broker_forward_request?(_body), do: false

  defp broker_approval_required?(%{"requires_approval" => true}), do: true
  defp broker_approval_required?(_credential_ref), do: false

  defp broker_forward(run_id, credential_id, dest, body) do
    with {:ok, uri} <- broker_forward_uri(body, dest),
         {:ok, %{"headers" => attachment_headers, "redaction_values" => redaction_values}} <-
           Mitos.KeyVault.resolve_run_attachment(run_id, credential_id, dest),
         {:ok, request_body, body_content_type} <- broker_forward_body(body),
         {:ok, method} <- broker_forward_method(body),
         {:ok, headers} <- broker_forward_headers(body, attachment_headers, body_content_type),
         {:ok, response} <- broker_http_request(uri, method, headers, request_body) do
      {:ok,
       response
       |> Map.put(:request_bytes, byte_size(request_body))
       |> Map.put(:body, redact_values(response.body, redaction_values))}
    end
  end

  defp broker_forward_uri(%{"url" => url}, dest) when is_binary(url) do
    uri = URI.parse(String.trim(url))

    cond do
      uri.scheme not in ["http", "https"] ->
        {:error, {:unsupported_broker_forward_scheme, uri.scheme}}

      not is_binary(uri.host) or uri.host == "" ->
        {:error, :broker_forward_url_host_required}

      credentialed_broker_forward_url?(uri) ->
        {:deny, "broker forward URL must not contain credentials"}

      sensitive_broker_forward_query?(uri.query) ->
        {:deny, "broker forward URL must not contain credential query parameters"}

      broker_destination(uri) != dest ->
        {:deny, "broker forward destination mismatch"}

      true ->
        {:ok, uri}
    end
  end

  defp broker_forward_uri(_body, _dest), do: {:error, :broker_forward_url_required}

  defp broker_destination(%URI{scheme: scheme, host: host, port: port}) do
    if port in [nil, broker_default_port(scheme)] do
      host
    else
      host <> ":" <> Integer.to_string(port)
    end
  end

  defp broker_forward_body(%{"body_base64" => body}) when is_binary(body) do
    case Base.decode64(body) do
      {:ok, decoded} -> {:ok, decoded, nil}
      :error -> {:error, :invalid_broker_forward_body_base64}
    end
  end

  defp broker_forward_body(%{"body" => body}) when is_binary(body), do: {:ok, body, nil}

  defp broker_forward_body(%{"body" => body}) when is_map(body) or is_list(body) do
    {:ok, Jason.encode!(body), "application/json"}
  end

  defp broker_forward_body(_body), do: {:ok, "", nil}

  defp broker_forward_method(%{"method" => method}) when is_binary(method) do
    method =
      method
      |> String.trim()
      |> String.upcase()

    if method in ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"] do
      {:ok, method}
    else
      {:error, {:unsupported_broker_forward_method, method}}
    end
  end

  defp broker_forward_method(%{"body" => body}) when body not in [nil, ""], do: {:ok, "POST"}

  defp broker_forward_method(%{"body_base64" => body}) when body not in [nil, ""],
    do: {:ok, "POST"}

  defp broker_forward_method(_body), do: {:ok, "GET"}

  defp broker_forward_headers(body, attachment_headers, body_content_type) do
    headers =
      body
      |> Map.get("headers", %{})
      |> normalize_forward_headers()
      |> reject_forward_headers()
      |> reject_secret_forward_headers()
      |> reject_credential_headers(attachment_headers)
      |> maybe_put_content_type(body_content_type)
      |> Kernel.++(attachment_headers)

    if Enum.all?(headers, fn {name, value} ->
         valid_header_name?(name) and valid_header_value?(value)
       end) do
      {:ok, headers}
    else
      {:error, :invalid_broker_forward_header}
    end
  end

  defp normalize_forward_headers(%{} = headers) do
    Enum.flat_map(headers, fn
      {name, value} when is_binary(name) and is_binary(value) ->
        [{String.downcase(name), value}]

      {name, value} when is_atom(name) and is_binary(value) ->
        [{name |> Atom.to_string() |> String.downcase(), value}]

      _other ->
        []
    end)
  end

  defp normalize_forward_headers(headers) when is_list(headers) do
    Enum.flat_map(headers, fn
      [name, value] when is_binary(name) and is_binary(value) ->
        [{String.downcase(name), value}]

      {name, value} when is_binary(name) and is_binary(value) ->
        [{String.downcase(name), value}]

      %{"name" => name, "value" => value} when is_binary(name) and is_binary(value) ->
        [{String.downcase(name), value}]

      _other ->
        []
    end)
  end

  defp normalize_forward_headers(_headers), do: []

  defp reject_forward_headers(headers) do
    rejected =
      MapSet.new([
        "connection",
        "content-length",
        "host",
        "keep-alive",
        "proxy-authenticate",
        "proxy-authorization",
        "te",
        "trailer",
        "transfer-encoding",
        "upgrade"
      ])

    Enum.reject(headers, fn {name, _value} -> MapSet.member?(rejected, name) end)
  end

  defp reject_secret_forward_headers(headers) do
    rejected = MapSet.new(["authorization", "proxy-authorization", "cookie", "set-cookie"])

    Enum.reject(headers, fn {name, _value} ->
      MapSet.member?(rejected, name) or Regex.match?(@secret_forward_header_pattern, name)
    end)
  end

  defp reject_credential_headers(headers, attachment_headers) do
    credential_headers =
      attachment_headers
      |> Enum.map(fn {name, _value} -> String.downcase(name) end)
      |> MapSet.new()

    Enum.reject(headers, fn {name, _value} -> MapSet.member?(credential_headers, name) end)
  end

  defp maybe_put_content_type(headers, nil), do: headers

  defp maybe_put_content_type(headers, content_type) do
    if Enum.any?(headers, fn {name, _value} -> name == "content-type" end) do
      headers
    else
      [{"content-type", content_type} | headers]
    end
  end

  defp valid_header_name?(name) when is_binary(name) do
    String.match?(name, ~r/^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/)
  end

  defp valid_header_name?(_name), do: false

  defp valid_header_value?(value) when is_binary(value) do
    not String.contains?(value, ["\r", "\n", <<0>>])
  end

  defp valid_header_value?(_value), do: false

  defp broker_http_request(uri, method, headers, body) do
    case broker_connect(uri) do
      {:ok, socket, transport} ->
        try do
          with :ok <- broker_send_request(socket, transport, uri, method, headers, body),
               {:ok, status, response_headers, response_body} <-
                 broker_read_response(socket, transport) do
            {:ok,
             %{
               status: status,
               headers: response_headers,
               body: response_body,
               response_bytes: byte_size(response_body)
             }}
          end
        after
          broker_close(socket, transport)
        end

      {:error, reason} ->
        {:error, {:broker_forward_unavailable, reason}}
    end
  end

  defp broker_connect(%URI{scheme: scheme, host: host} = uri)
       when scheme in ["http", "https"] and is_binary(host) do
    port = uri.port || broker_default_port(scheme)

    if scheme == "https" do
      _ = Application.ensure_all_started(:ssl)

      case :ssl.connect(
             String.to_charlist(host),
             port,
             [:binary, active: false],
             @broker_http_timeout_ms
           ) do
        {:ok, socket} -> {:ok, socket, :ssl}
        {:error, reason} -> {:error, reason}
      end
    else
      case :gen_tcp.connect(
             String.to_charlist(host),
             port,
             [:binary, packet: :raw, active: false],
             @broker_http_timeout_ms
           ) do
        {:ok, socket} -> {:ok, socket, :tcp}
        {:error, reason} -> {:error, reason}
      end
    end
  end

  defp broker_connect(%URI{scheme: scheme}), do: {:error, {:unsupported_scheme, scheme}}

  defp broker_default_port("http"), do: 80
  defp broker_default_port("https"), do: 443

  defp broker_send_request(socket, transport, uri, method, headers, body) do
    request = [
      method,
      " ",
      broker_request_target(uri),
      " HTTP/1.1\r\n",
      "host: ",
      broker_host_header(uri),
      "\r\n",
      broker_header_lines(headers),
      "content-length: ",
      Integer.to_string(byte_size(body)),
      "\r\n",
      "connection: close\r\n",
      "\r\n",
      body
    ]

    broker_send_data(socket, transport, request)
  end

  defp broker_request_target(%URI{path: nil, query: nil}), do: "/"
  defp broker_request_target(%URI{path: "", query: nil}), do: "/"
  defp broker_request_target(%URI{path: nil, query: query}), do: "/?" <> query
  defp broker_request_target(%URI{path: "", query: query}), do: "/?" <> query
  defp broker_request_target(%URI{path: path, query: nil}), do: path
  defp broker_request_target(%URI{path: path, query: query}), do: path <> "?" <> query

  defp broker_host_header(%URI{scheme: scheme, host: host, port: port}) do
    if port in [nil, broker_default_port(scheme)] do
      host
    else
      host <> ":" <> Integer.to_string(port)
    end
  end

  defp broker_header_lines(headers) do
    Enum.map(headers, fn {name, value} -> [name, ": ", value, "\r\n"] end)
  end

  defp broker_read_response(socket, transport, acc \\ "") do
    case broker_recv_data(socket, transport) do
      {:ok, chunk} ->
        acc = acc <> chunk

        case broker_parse_response(acc, false) do
          {:ok, status, headers, body} -> {:ok, status, headers, body}
          :more -> broker_read_response(socket, transport, acc)
          {:error, reason} -> {:error, reason}
        end

      {:error, :closed} ->
        case broker_parse_response(acc, true) do
          :more -> {:error, :incomplete_broker_forward_response}
          parsed -> parsed
        end

      {:error, reason} ->
        {:error, {:broker_forward_unavailable, reason}}
    end
  end

  defp broker_parse_response(bytes, closed?) do
    case :binary.match(bytes, "\r\n\r\n") do
      {header_size, 4} ->
        header = binary_part(bytes, 0, header_size)
        body_offset = header_size + 4
        body = binary_part(bytes, body_offset, byte_size(bytes) - body_offset)
        [status_line | header_lines] = String.split(header, "\r\n")

        with {:ok, status} <- broker_status_code(status_line),
             {:ok, headers} <- broker_response_headers(header_lines),
             {:ok, content_length} <- broker_content_length(headers),
             true <- content_length != nil or closed?,
             true <- content_length == nil or byte_size(body) >= content_length do
          body =
            if is_integer(content_length) do
              binary_part(body, 0, content_length)
            else
              body
            end

          {:ok, status, headers, body}
        else
          false -> :more
          {:error, reason} -> {:error, reason}
        end

      :nomatch ->
        :more
    end
  end

  defp broker_status_code(status_line) do
    case String.split(status_line, " ", parts: 3) do
      [_http, status | _] ->
        case Integer.parse(status) do
          {status, ""} -> {:ok, status}
          _error -> {:error, :invalid_broker_forward_status}
        end

      _other ->
        {:error, :invalid_broker_forward_status}
    end
  end

  defp broker_response_headers(header_lines) do
    headers =
      Enum.flat_map(header_lines, fn line ->
        case String.split(line, ":", parts: 2) do
          [name, value] -> [{String.downcase(name), String.trim(value)}]
          _other -> []
        end
      end)

    {:ok, headers}
  end

  defp broker_content_length(headers) do
    headers
    |> Enum.find_value(fn
      {"content-length", value} -> value
      _other -> nil
    end)
    |> case do
      nil ->
        {:ok, nil}

      value ->
        case Integer.parse(value) do
          {length, ""} when length >= 0 -> {:ok, length}
          _other -> {:error, :invalid_broker_forward_content_length}
        end
    end
  end

  defp broker_send_data(socket, :tcp, request), do: :gen_tcp.send(socket, request)
  defp broker_send_data(socket, :ssl, request), do: :ssl.send(socket, request)

  defp broker_recv_data(socket, :tcp), do: :gen_tcp.recv(socket, 0, @broker_http_timeout_ms)
  defp broker_recv_data(socket, :ssl), do: :ssl.recv(socket, 0, @broker_http_timeout_ms)

  defp broker_close(socket, :tcp), do: :gen_tcp.close(socket)
  defp broker_close(socket, :ssl), do: :ssl.close(socket)

  defp broker_forward_trace_payload(%{
         status: status,
         request_bytes: request_bytes,
         response_bytes: response_bytes
       }) do
    %{
      "status" => status,
      "request_bytes" => request_bytes,
      "response_bytes" => response_bytes
    }
  end

  defp broker_forward_response(conn, %{status: status, headers: headers, body: body}) do
    content_type =
      Enum.find_value(headers, "application/octet-stream", fn
        {"content-type", value} -> value
        _other -> nil
      end)

    conn
    |> put_resp_content_type(content_type)
    |> send_resp(status, body)
  end

  defp redact_values(value, redaction_values) when is_binary(value) do
    Enum.reduce(redaction_values, value, fn secret, acc ->
      String.replace(acc, secret, "[redacted]")
    end)
  end

  defp redact_values(value, _redaction_values), do: value

  defp broker_trace(run_id), do: Mitos.Trace.open!(run_id: run_id)

  defp broker_egress_ref(body, dest, credential_id) do
    %{
      "dest" => dest,
      "credential_id" => credential_id,
      "request_id" => body["request_id"] || body["nonce"] || broker_request_id(),
      "method" => body["method"],
      "policy_rule" => body["policy_rule"]
    }
    |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
    |> Map.new()
  end

  defp append_broker_egress_attempt(trace, ref, body, caused_by) do
    Mitos.Trace.append_effect(trace, %{
      type: "egress_attempted",
      caused_by: caused_by,
      ref: ref,
      payload:
        %{
          "origin" => "broker_http",
          "method" => body["method"],
          "url" => public_broker_url(body["url"]),
          "request_bytes" => broker_trace_request_bytes(body)
        }
        |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
        |> Map.new()
    })
  end

  defp public_broker_url(url) when is_binary(url) do
    uri = URI.parse(url)

    if is_binary(uri.scheme) and is_binary(uri.host) do
      uri
      |> Map.put(:userinfo, nil)
      |> Map.put(:query, public_broker_url_query(uri.query))
      |> URI.to_string()
    else
      url
    end
  end

  defp public_broker_url(url), do: url

  defp credentialed_broker_forward_url?(%URI{userinfo: userinfo})
       when is_binary(userinfo) and userinfo != "",
       do: true

  defp credentialed_broker_forward_url?(_uri), do: false

  defp sensitive_broker_forward_query?(nil), do: false

  defp sensitive_broker_forward_query?(query) when is_binary(query) do
    query
    |> String.split("&", trim: false)
    |> Enum.any?(&sensitive_broker_query_pair?/1)
  end

  defp sensitive_broker_forward_query?(_query), do: false

  defp public_broker_url_query(nil), do: nil

  defp public_broker_url_query(query) do
    query
    |> String.split("&", trim: false)
    |> Enum.map(&public_broker_url_query_pair/1)
    |> Enum.join("&")
  end

  defp public_broker_url_query_pair(pair) do
    [key | _rest] = String.split(pair, "=", parts: 2)

    if sensitive_broker_url_query_key?(safe_form_decode(key)) do
      key <> "=[redacted]"
    else
      pair
    end
  end

  defp sensitive_broker_query_pair?(pair) do
    [key | _rest] = String.split(pair, "=", parts: 2)
    sensitive_broker_url_query_key?(safe_form_decode(key))
  end

  defp sensitive_broker_url_query_key?(key) do
    Regex.match?(@secret_url_query_key_pattern, to_string(key))
  end

  defp safe_form_decode(value) do
    URI.decode_www_form(value)
  rescue
    ArgumentError -> value
  end

  defp broker_trace_request_bytes(%{"body_base64" => encoded}) when is_binary(encoded) do
    case Base.decode64(encoded) do
      {:ok, decoded} -> byte_size(decoded)
      :error -> 0
    end
  end

  defp broker_trace_request_bytes(%{"body" => body}) when is_binary(body), do: byte_size(body)

  defp broker_trace_request_bytes(%{"body" => body}) when is_map(body) or is_list(body) do
    body
    |> Jason.encode!()
    |> byte_size()
  end

  defp broker_trace_request_bytes(_body), do: 0

  defp append_broker_egress_allowed(trace, ref, credential_ref, caused_by, meta \\ %{}) do
    status = Map.get(meta, "status", 200)
    response_bytes = Map.get(meta, "response_bytes", 0)

    allowed_payload =
      %{
        "decision" => "allow",
        "status" => status,
        "credential" => credential_ref,
        "request_bytes" => Map.get(meta, "request_bytes"),
        "response_bytes" => response_bytes,
        "error" => Map.get(meta, "error")
      }
      |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
      |> Map.new()

    completed_payload =
      %{
        "status" => status,
        "response_bytes" => response_bytes,
        "error" => Map.get(meta, "error")
      }
      |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
      |> Map.new()

    trace
    |> Mitos.Trace.append_effect(%{
      type: "egress_allowed",
      caused_by: caused_by,
      ref: ref,
      payload: allowed_payload
    })
    |> Mitos.Trace.append_effect(%{
      type: "egress_completed",
      caused_by: caused_by,
      ref: ref,
      payload: completed_payload
    })
  end

  defp append_broker_egress_denied(trace, ref, reason, caused_by) do
    Mitos.Trace.append_effect(trace, %{
      type: "egress_denied",
      caused_by: caused_by,
      ref: ref,
      payload: %{"decision" => "deny", "status" => 403, "reason" => reason}
    })
  end

  defp append_broker_approval_requested(trace, ref, credential_ref, caused_by) do
    approval_id = ref["request_id"] || broker_request_id()

    Mitos.Trace.append_effect(trace, %{
      type: "approval_requested",
      caused_by: caused_by,
      ref: Map.put(ref, "approval_id", approval_id),
      payload: %{
        "kind" => "egress",
        "decision" => "pending",
        "reason" => "egress approval required",
        "dest" => ref["dest"],
        "credential" => credential_ref
      }
    })
  end

  defp broker_caused_by(%{"caused_by" => caused_by}) when is_integer(caused_by), do: caused_by
  defp broker_caused_by(_body), do: 0

  defp broker_request_id do
    "broker-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)
  end

  defp capability_setup_response(conn, id, status) do
    case Mitos.CapabilitySetupView.get(id) do
      {:ok, capability} ->
        conn
        |> put_resp_content_type("application/json")
        |> send_resp(status, Jason.encode!(%{"capability" => capability}))

      error ->
        capability_setup_error(conn, error)
    end
  end

  defp capability_setup_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "capability setup not found"}}

        {:error, {:conflict, id}} ->
          {409, %{error: "capability setup already exists", id: id}}

        {:error, {:invalid_pipeline, detail}} ->
          {422, %{error: "invalid capability setup", detail: detail}}

        {:error, {:invalid_workload, detail}} ->
          {422, %{error: "invalid capability setup", detail: detail}}

        {:error, reason} ->
          {500, %{error: "capability setup failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp pipeline_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "pipeline not found"}}

        {:error, {:conflict, id}} ->
          {409, %{error: "pipeline already exists", id: id}}

        {:error, {:invalid_pipeline, detail}} ->
          {422, %{error: "invalid pipeline definition", detail: detail}}

        {:error, {:invalid_workload, detail}} ->
          {422, %{error: "invalid pipeline definition", detail: detail}}

        {:error, reason} ->
          {500, %{error: "pipeline registry failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp pipeline_execute_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "pipeline not found"}}

        {:error, {:invalid_pipeline, detail}} ->
          {422, %{error: "invalid pipeline definition", detail: detail}}

        {:error,
         {:app_run_failed, app_run_id,
          {:pipeline_step_failed, step_id, capability_id, {:unknown_capability_ref, ref}}}} ->
          {404,
           %{
             error: "application capability ref not found",
             app_run_id: app_run_id,
             step_id: step_id,
             capability_id: capability_id,
             ref: ref
           }}

        {:error,
         {:app_run_failed, app_run_id,
          {:pipeline_step_failed, step_id, capability_id, {:unsupported_app_capability_ref, ref}}}} ->
          {422,
           %{
             error: "unsupported application capability ref",
             app_run_id: app_run_id,
             step_id: step_id,
             capability_id: capability_id,
             ref: ref
           }}

        {:error,
         {:app_run_failed, app_run_id, {:pipeline_step_failed, step_id, capability_id, reason}}} ->
          {502,
           %{
             error: "pipeline step failed",
             app_run_id: app_run_id,
             step_id: step_id,
             capability_id: capability_id,
             detail: inspect(reason)
           }}

        {:error, {:app_run_failed, app_run_id, reason}} ->
          {500,
           %{
             error: "application run failed",
             app_run_id: app_run_id,
             detail: inspect(reason)
           }}

        {:error, {:pipeline_step_failed, step_id, capability_id, reason}} ->
          {502,
           %{
             error: "pipeline step failed",
             step_id: step_id,
             capability_id: capability_id,
             detail: inspect(reason)
           }}

        {:error, reason} ->
          {500, %{error: "pipeline execution failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp app_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "app not found"}}

        {:error, {:application_not_found, path}} ->
          {404, %{error: "app not found", path: path}}

        {:error, {:invalid_application, detail}} ->
          {422, %{error: "invalid application manifest", detail: detail}}

        {:error, {:unsupported_manifest_format, detail}} ->
          {422, %{error: "unsupported application manifest format", detail: detail}}

        {:error, reason} ->
          {500, %{error: "application registry failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp app_source_error(conn, error) do
    {status, payload} =
      case error do
        {:error, {:application_source_not_found, source}} ->
          {404, %{error: "application source not found", source: source}}

        {:error, {:application_source_conflict, install_id}} ->
          {409, %{error: "application already installed", install_id: install_id}}

        {:error, {:invalid_application_source, detail}} ->
          {422, %{error: "invalid application source", detail: detail}}

        {:error, {:application_not_found, path}} ->
          {404, %{error: "app not found", path: path}}

        {:error, {:invalid_application, detail}} ->
          {422, %{error: "invalid application manifest", detail: detail}}

        {:error, {:unsupported_manifest_format, detail}} ->
          {422, %{error: "unsupported application manifest format", detail: detail}}

        {:error, reason} ->
          {500, %{error: "application install failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp app_activation_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "app not found"}}

        {:error, {:application_not_found, path}} ->
          {404, %{error: "app not found", path: path}}

        {:error, {:invalid_application, detail}} ->
          {422, %{error: "invalid application manifest", detail: detail}}

        {:error, {:unsupported_manifest_format, detail}} ->
          {422, %{error: "unsupported application manifest format", detail: detail}}

        {:error, {:invalid_application_activation, detail}} ->
          {422, %{error: "invalid application activation", detail: detail}}

        {:error, {:application_activation_failed, detail}} ->
          {422, %{error: "application activation failed", detail: detail}}

        {:error, {:application_activation_conflict, ids}} ->
          {409, %{error: "application activation conflict", services: ids}}

        {:error, reason} ->
          {500, %{error: "application activation failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp app_run_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "app or pipeline not found"}}

        {:error, {:unknown_entrypoint, entrypoint}} ->
          {404, %{error: "entrypoint not found", entrypoint: entrypoint}}

        {:error, {:invalid_application_input, detail}} ->
          {422, %{error: "invalid application input", detail: detail}}

        {:error, {:application_connector_not_found, connector}} ->
          {404, %{error: "application connector not found", connector: connector}}

        {:error, {:invalid_application_io, detail}} ->
          {422, %{error: "invalid application io", detail: detail}}

        {:error, {:unsupported_application_selection, mode}} ->
          {422, %{error: "unsupported application selection", mode: mode}}

        {:error, {:invalid_application, detail}} ->
          {422, %{error: "invalid application manifest", detail: detail}}

        {:error, {:invalid_pipeline, detail}} ->
          {422, %{error: "invalid pipeline definition", detail: detail}}

        {:error,
         {:app_run_failed, app_run_id,
          {:pipeline_step_failed, step_id, capability_id, {:unknown_capability_ref, ref}}}} ->
          {404,
           %{
             error: "application capability ref not found",
             app_run_id: app_run_id,
             step_id: step_id,
             capability_id: capability_id,
             ref: ref
           }}

        {:error,
         {:app_run_failed, app_run_id,
          {:pipeline_step_failed, step_id, capability_id, {:unsupported_app_capability_ref, ref}}}} ->
          {422,
           %{
             error: "unsupported application capability ref",
             app_run_id: app_run_id,
             step_id: step_id,
             capability_id: capability_id,
             ref: ref
           }}

        {:error,
         {:app_run_failed, app_run_id, {:pipeline_step_failed, step_id, capability_id, reason}}} ->
          {502,
           %{
             error: "pipeline step failed",
             app_run_id: app_run_id,
             step_id: step_id,
             capability_id: capability_id,
             detail: inspect(reason)
           }}

        {:error, {:app_run_failed, app_run_id, reason}} ->
          {500,
           %{
             error: "application run failed",
             app_run_id: app_run_id,
             detail: inspect(reason)
           }}

        {:error, {:pipeline_step_failed, step_id, capability_id, reason}} ->
          {502,
           %{
             error: "pipeline step failed",
             step_id: step_id,
             capability_id: capability_id,
             detail: inspect(reason)
           }}

        {:error, reason} ->
          {500, %{error: "application run failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp app_delivery_error(conn, error) do
    {status, payload} =
      case error do
        {:error, {:application_run_not_found, app_run_id}} ->
          {404, %{error: "application run not found", app_run_id: app_run_id}}

        {:error, {:application_run_not_completed, task_id}} ->
          {409, %{error: "application run not completed", task_id: task_id}}

        {:error, {:application_run_not_deliverable, detail}} ->
          {422, %{error: "application run not deliverable", detail: detail}}

        {:error, {:invalid_application_run, detail}} ->
          {422, %{error: "invalid application run", detail: detail}}

        {:error, :not_found} ->
          {404, %{error: "app not found"}}

        {:error, {:invalid_application, detail}} ->
          {422, %{error: "invalid application manifest", detail: detail}}

        {:error, reason} ->
          {500, %{error: "application delivery failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp app_package_error(conn, error) do
    {status, payload} =
      case error do
        {:error, :not_found} ->
          {404, %{error: "app not found"}}

        {:error, {:application_not_found, path}} ->
          {404, %{error: "app not found", path: path}}

        {:error, {:invalid_application, detail}} ->
          {422, %{error: "invalid application manifest", detail: detail}}

        {:error, {:unsupported_manifest_format, detail}} ->
          {422, %{error: "unsupported application manifest format", detail: detail}}

        {:error, {:invalid_application_package, detail}} ->
          {422, %{error: "invalid application package", detail: detail}}

        {:error, reason} ->
          {500, %{error: "application package failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
  end

  defp app_install_error(conn, error) do
    {status, payload} =
      case error do
        {:error, {:application_package_not_found, package_id}} ->
          {404, %{error: "application package not found", package_id: package_id}}

        {:error, {:invalid_application, detail}} ->
          {422, %{error: "invalid application manifest", detail: detail}}

        {:error, {:unsupported_manifest_format, detail}} ->
          {422, %{error: "unsupported application manifest format", detail: detail}}

        {:error, {:invalid_application_package, detail}} ->
          {422, %{error: "invalid application package", detail: detail}}

        {:error, reason} ->
          {500, %{error: "application install failed", detail: inspect(reason)}}
      end

    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Jason.encode!(payload))
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

        {:error, {:tenant_conflict, seller_id}} ->
          {422, %{error: "buyer and seller tenants must be distinct", seller_id: seller_id}}

        {:error, {:invalid_request, detail}} ->
          {422, %{error: "invalid request", detail: detail}}

        {:error, :payment_not_configured} ->
          {422, %{error: "capability is not published with pricing"}}

        {:error, {:launch_failed, reason}} ->
          {502, %{error: "agent launch failed", detail: inspect(reason)}}

        {:error, {:launch_failed, run_id, reason}} ->
          {502, %{error: "agent launch failed", run_id: run_id, detail: inspect(reason)}}

        {:error, {:publication_validation_failed, validation}} ->
          {422, validation}

        {:error, {:sandbox_provider_ineligible, tier, detail}} ->
          {503,
           %{
             error: "no eligible sandbox provider",
             trust_tier: tier,
             detail: detail
           }}

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

  defp raw_session_argv([command | _rest] = argv) do
    cond do
      not Enum.all?(argv, &is_binary/1) ->
        {:error, "argv entries must be strings"}

      command == "" ->
        {:error, "argv command must not be empty"}

      Enum.any?(argv, &String.contains?(&1, <<0>>)) ->
        {:error, "argv entries must not contain NUL"}

      true ->
        {:ok, argv}
    end
  end

  defp raw_session_argv(_argv), do: {:error, "argv must be a non-empty list of strings"}

  defp raw_session_input_error?({:invalid_env, _detail}), do: true
  defp raw_session_input_error?({:invalid_cwd, _detail}), do: true
  defp raw_session_input_error?({:invalid_argv, _detail}), do: true
  defp raw_session_input_error?({:invalid_size, _detail}), do: true
  defp raw_session_input_error?({:invalid_idle_ms, _detail}), do: true
  defp raw_session_input_error?({:invalid_open_control, _detail}), do: true
  defp raw_session_input_error?(_reason), do: false

  defp raw_session_bad_request(conn, detail) do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(400, Jason.encode!(%{error: "invalid session request", detail: detail}))
  end

  defp list_or_empty(values) when is_list(values), do: values
  defp list_or_empty(_values), do: []

  defp buyer_workspace_from_body(body) do
    body = map_or_empty(body)
    request = body |> Map.get("request") |> map_or_empty()

    workspace =
      body["workspace"] ||
        body["buyer_workspace"] ||
        body["buyer_data"] ||
        body["data_snapshot"] ||
        request["workspace"] ||
        request["buyer_data"]

    normalize_buyer_workspace(workspace)
  end

  defp normalize_buyer_workspace(nil), do: {:ok, nil}
  defp normalize_buyer_workspace(""), do: {:ok, nil}

  defp normalize_buyer_workspace(snapshot) when is_binary(snapshot) do
    {:ok, %{"snapshot" => snapshot, "mount" => "/workspace", "ro" => true}}
  end

  defp normalize_buyer_workspace(%{} = workspace) do
    workspace = stringify_map(workspace)
    mount = string_or(workspace["mount"], "/workspace")

    cond do
      Enum.any?(["path", "bind", "cwd"], &Map.has_key?(workspace, &1)) ->
        {:error,
         {:invalid_request,
          "buyer workspace must use a provider snapshot or volume, not host path/bind/cwd"}}

      workspace["ro"] == false ->
        {:error, {:invalid_request, "buyer workspace must be read-only"}}

      not is_binary(workspace["snapshot"] || workspace["volume"]) ->
        {:error, {:invalid_request, "buyer workspace requires snapshot or volume"}}

      mount != "/workspace" ->
        {:error, {:invalid_request, "buyer workspace must mount at /workspace"}}

      true ->
        {:ok,
         %{
           "mount" => mount,
           "ro" => true
         }
         |> maybe_put("snapshot", workspace["snapshot"])
         |> maybe_put("volume", workspace["volume"])
         |> maybe_put("sha256", workspace["sha256"])}
    end
  end

  defp normalize_buyer_workspace(_workspace),
    do: {:error, {:invalid_request, "buyer workspace must be an object or snapshot string"}}

  defp binding_value(value) when is_binary(value), do: value
  defp binding_value(nil), do: ""
  defp binding_value(value) when is_number(value) or is_boolean(value), do: to_string(value)
  defp binding_value(value), do: Jason.encode!(value)

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default

  defp stringify_map(%{} = map) do
    Map.new(map, fn {key, value} -> {to_string(key), value} end)
  end

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)

  defp maybe(opts, _key, nil), do: opts
  defp maybe(opts, key, value), do: Keyword.put(opts, key, value)

  defp nonempty_string(value) when is_binary(value) do
    value = String.trim(value)
    if value == "", do: nil, else: value
  end

  defp nonempty_string(_value), do: nil

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
