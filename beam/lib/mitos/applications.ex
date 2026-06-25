defmodule Mitos.Applications do
  @moduledoc """
  Local registry and execution facade for agentic application manifests.

  Applications are loaded from subdirectories of `:apps_dir` (or
  `MITOS_APPS_DIR`). Each subdirectory contains an `app.json`, `app.yaml`, or
  `app.yml` manifest. Execution delegates to the existing workload-backed
  `Mitos.PipelineRunner` when an entrypoint names a locally executable pipeline.
  """

  @id_pattern ~r/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/

  @doc "Directory containing local application directories."
  def dir do
    Application.get_env(:mitos, :apps_dir) ||
      System.get_env("MITOS_APPS_DIR") ||
      Path.expand("../../../apps", __DIR__)
  end

  @doc "Directory containing local OCI image-layout application packages."
  def packages_dir do
    Application.get_env(:mitos, :app_packages_dir) ||
      System.get_env("MITOS_APP_PACKAGES_DIR") ||
      Path.join(dir(), ".packages")
  end

  @doc """
  Add an application to the local registry from a directory or git URL.

  Local directories are copied into the configured application registry. Git
  URLs are cloned into a temporary directory first, then installed through the
  same path so manifest validation and identity are identical for both sources.
  """
  def add_source(source, install_id \\ nil, opts \\ [])

  def add_source(source, install_id, opts) when is_binary(source) and is_list(opts) do
    source = String.trim(source)

    with {:ok, source} <- source_string(source),
         {:ok, kind, materialized_source} <- materialize_source(source, opts) do
      {source_dir, cleanup} = materialized_source

      try do
        with {:ok, app} <- Mitos.AppManifest.load(source_dir),
             {:ok, install_id} <- source_install_id(install_id, app, source),
             app_dir <- Path.join(dir(), install_id),
             :ok <- ensure_install_target_available(app_dir),
             :ok <- ensure_app_registry_dir(),
             {:ok, _files} <- copy_app_source(source_dir, app_dir),
             {:ok, installed_app} <- Mitos.AppManifest.load(app_dir) do
          {:ok,
           %{
             "install_id" => install_id,
             "app_dir" => Path.expand(app_dir),
             "source" => source,
             "source_kind" => kind,
             "app_id" => installed_app.app_id,
             "app" => public_view(installed_app, local_id: install_id)
           }}
        end
      after
        cleanup.()
      end
    end
  end

  def add_source(_source, _install_id, _opts),
    do: {:error, {:invalid_application_source, "source must be a string"}}

  @doc "List loadable local applications, sorted by name."
  def list do
    app_dirs()
    |> Enum.flat_map(fn app_dir ->
      case Mitos.AppManifest.load(app_dir) do
        {:ok, app} -> [public_view(app, local_id: Path.basename(app_dir))]
        {:error, _reason} -> []
      end
    end)
    |> Enum.sort_by(&{&1["name"], &1["version"]})
  end

  @doc "Fetch one application by local directory id, manifest name, or app_id."
  def fetch(id) when is_binary(id) do
    cond do
      safe_id?(id) ->
        case trusted_app_registry_dir() do
          {:ok, root} ->
            app_dir = Path.join(root, id)

            if regular_dir?(app_dir) do
              Mitos.AppManifest.load(app_dir)
            else
              find_loaded(id)
            end

          {:error, _reason} ->
            {:error, :not_found}
        end

      true ->
        find_loaded(id)
    end
  end

  def fetch(_id), do: {:error, :not_found}

  @doc "Buyer-safe view of an application manifest."
  def public_view(%Mitos.AppManifest{} = app, opts \\ []) do
    %{
      "id" => Keyword.get(opts, :local_id) || app.name,
      "app_id" => app.app_id,
      "name" => app.name,
      "version" => app.version,
      "entrypoints" =>
        Enum.map(app.entrypoints, fn entrypoint ->
          Map.take(entrypoint, ~w(id pipeline title description workspace inputs defaults))
        end),
      "pipelines" => Enum.map(app.pipelines, &Map.take(&1, ~w(id file sha256 requires))),
      "capabilities" => Enum.map(app.capabilities, &Map.take(&1, ~w(as ref))),
      "io" => app.io,
      "services" => app.services,
      "requirements" => app.requirements
    }
    |> maybe_put("description", app.description)
    |> maybe_put("authors", app.authors)
    |> maybe_put("license", app.license)
  end

  @doc "Run a local app entrypoint through an executable pipeline with the same id."
  def run_entrypoint(app_id, entrypoint_id, inputs, opts \\ [])

  def run_entrypoint(app_id, entrypoint_id, inputs, opts)
      when is_binary(app_id) and is_binary(entrypoint_id) and is_map(inputs) do
    with {:ok, app_dir} <- app_dir(app_id),
         {:ok, app} <- Mitos.AppManifest.load(app_dir),
         {:ok, result} <- Mitos.AppRunner.run(app_dir, entrypoint_id, inputs, opts) do
      {:ok,
       result
       |> Map.put("app_id", app.app_id)
       |> Map.put("app", public_view(app, local_id: local_id(app_id, app)))
       |> Map.put("entrypoint", entrypoint_id)}
    end
  end

  def run_entrypoint(_app_id, _entrypoint_id, _inputs, _opts),
    do: {:error, {:invalid_application_input, "inputs must be an object"}}

  @doc "Run an application from a connector signal by selecting an explicit I/O binding."
  def run_connector(app_id, connector, inputs, opts \\ [])

  def run_connector(app_id, connector, inputs, opts)
      when is_binary(app_id) and is_binary(connector) and is_map(inputs) and is_list(opts) do
    with {:ok, app_dir} <- app_dir(app_id),
         {:ok, app} <- Mitos.AppManifest.load(app_dir),
         {:ok, binding} <- select_input_binding(app, connector, Keyword.get(opts, :path)),
         {:ok, entrypoint_id} <- entrypoint_for_binding(app, binding) do
      run_opts = connector_run_opts(connector, binding, opts)

      case duplicate_connector_result(app, binding, connector, entrypoint_id, run_opts) do
        {:ok, result} ->
          {:ok, enrich_connector_result(result, app_id, app, entrypoint_id, connector, binding)}

        :not_found ->
          with {:ok, result} <- Mitos.AppRunner.run(app_dir, entrypoint_id, inputs, run_opts) do
            {:ok, enrich_connector_result(result, app_id, app, entrypoint_id, connector, binding)}
          end
      end
    end
  end

  def run_connector(_app_id, _connector, _inputs, _opts),
    do: {:error, {:invalid_application_input, "inputs must be an object"}}

  @doc "Re-drive sink deliveries for a completed application run without rerunning it."
  def redrive_deliveries(app_run_id, opts \\ [])

  def redrive_deliveries(app_run_id, opts) when is_binary(app_run_id) and is_list(opts) do
    trace_opts = trace_opts(app_run_id, opts)
    events = Mitos.Trace.read_events(app_run_id, trace_read_opts(opts))

    with :ok <- ensure_app_run_events(app_run_id, events),
         {:ok, signal} <- app_signal_event(events),
         {:ok, app_id} <- signal_app_id(signal),
         {:ok, app_dir} <- app_dir(app_id),
         {:ok, app} <- Mitos.AppManifest.load(app_dir),
         {:ok, task} <- redrive_task(events, signal),
         {:ok, done} <- redrive_task_done(events, task["id"]),
         {:ok, pipeline_result} <- redrive_pipeline_result(done) do
      trace = Mitos.Trace.open!(trace_opts)
      before_seq = trace.seq
      before_sent = delivery_sent_count(events, task["id"])

      {trace, _deliveries} =
        Mitos.IOPlane.append_deliveries(trace, app, task, pipeline_result, caused_by: done["seq"])

      after_events = Mitos.Trace.read_events(app_run_id, trace_read_opts(opts))
      after_sent = delivery_sent_count(after_events, task["id"])
      effects_appended = trace.seq - before_seq

      {:ok,
       %{
         "app_run_id" => app_run_id,
         "app_id" => app.app_id,
         "task_id" => task["id"],
         "entrypoint" => get_in(task, ["provenance", "entrypoint"]),
         "pipeline_id" => task["pipeline"],
         "status" => "redriven",
         "idempotent?" => effects_appended == 0,
         "effects_appended" => effects_appended,
         "new_deliveries" => max(after_sent - before_sent, 0),
         "deliveries" => deliveries_from_events(after_events, task["id"])
       }
       |> drop_nil()}
    end
  end

  def redrive_deliveries(_app_run_id, _opts),
    do: {:error, {:invalid_application_run, "app_run_id must be a string"}}

  @doc """
  Project an application activation into service-runtime and I/O binding state.

  This is an install/control-plane projection: it resolves the app as a service
  target and returns declared or default connector/sink bindings. It never
  materializes connector credentials, which remain tenant-side.
  """
  def activation_plan(app_id, opts \\ [])

  def activation_plan(app_id, opts) when is_binary(app_id) and is_list(opts) do
    with {:ok, app_dir} <- app_dir(app_id),
         {:ok, app} <- Mitos.AppManifest.load(app_dir),
         {:ok, units} <- normalize_service_units(Keyword.get(opts, :units, [])),
         :ok <- ensure_required_services_present(app, units),
         {:ok, active_set} <- resolve_app_active_set(app, units, opts) do
      local_id = local_id(app_id, app)

      {:ok,
       %{
         "activation_id" => "act:" <> app.app_id,
         "app_id" => app.app_id,
         "app" => public_view(app, local_id: local_id),
         "services" => %{
           "target" => app_target(app),
           "requires" => app_required_services(app),
           "wants" => app_wanted_services(app),
           "pipeline_requires" => pipeline_service_requirements(app),
           "missing_wants" => missing_services(app_wanted_services(app), units),
           "active_set" => active_set
         },
         "io" => io_projection(app)
       }}
    end
  end

  def activation_plan(_app_id, _opts),
    do: {:error, {:invalid_application_activation, "app_id must be a string"}}

  @doc "Publish one local application into the configured local OCI-layout package directory."
  def publish_local(app_id, package_id \\ nil, opts \\ [])

  def publish_local(app_id, package_id, opts) when is_binary(app_id) and is_list(opts) do
    with {:ok, app_dir} <- app_dir(app_id),
         {:ok, app} <- Mitos.AppManifest.load(app_dir, apply_bindings: false),
         {:ok, package_id} <- package_id(package_id, app),
         package_dir <- Path.join(packages_dir(), package_id),
         {:ok, package} <-
           Mitos.AppPackage.publish_local(app_dir, package_dir,
             tag: Keyword.get(opts, :tag, app.name)
           ) do
      {:ok,
       package
       |> Map.put("package_id", package_id)
       |> Map.put("app", public_view(app, local_id: local_id(app_id, app)))}
    end
  end

  def publish_local(_app_id, _package_id, _opts),
    do: {:error, {:invalid_application_package, "app_id must be a string"}}

  @doc "Install one local OCI-layout application package into the local application directory."
  def install_package(package_id, install_id \\ nil, opts \\ [])

  def install_package(package_id, install_id, opts)
      when is_binary(package_id) and is_list(opts) do
    with {:ok, package_id} <- package_dir_id(package_id),
         {:ok, install_id} <- install_id(install_id, package_id),
         {:ok, packages_root} <- trusted_packages_dir(),
         package_dir <- Path.join(packages_root, package_id),
         true <- regular_dir?(package_dir),
         app_dir <- Path.join(dir(), install_id),
         {:ok, install} <- Mitos.AppPackage.install_local(package_dir, app_dir, opts),
         {:ok, app} <- Mitos.AppManifest.load(app_dir) do
      {:ok,
       install
       |> Map.put("package_id", package_id)
       |> Map.put("install_id", install_id)
       |> Map.put("app", public_view(app, local_id: install_id))}
    else
      false ->
        {:error, {:application_package_not_found, package_id}}

      {:error, :untrusted_directory} ->
        {:error, {:application_package_not_found, package_id}}

      {:error, _reason} = error ->
        error
    end
  end

  def install_package(_package_id, _install_id, _opts),
    do: {:error, {:invalid_application_package, "package_id must be a string"}}

  defp source_string(""), do: {:error, {:invalid_application_source, "source is required"}}
  defp source_string(source), do: {:ok, source}

  defp materialize_source(source, opts) do
    local_path = local_source_path(source)

    cond do
      regular_dir?(local_path) and no_symlink_absolute_path?(local_path) ->
        {:ok, "directory", {local_path, fn -> :ok end}}

      regular_dir?(local_path) or symlink_path?(local_path) ->
        {:error, {:invalid_application_source, "source directory must not contain symlinks"}}

      git_source?(source) ->
        clone_git_source(source, opts)

      true ->
        {:error, {:application_source_not_found, source}}
    end
  end

  defp local_source_path("file://" <> _rest = source) do
    source
    |> URI.parse()
    |> Map.get(:path, "")
    |> URI.decode()
    |> Path.expand()
  end

  defp local_source_path(source), do: Path.expand(source)

  defp git_source?(source) do
    String.starts_with?(source, ["https://", "http://", "ssh://", "git://", "git@"]) or
      String.ends_with?(source, ".git")
  end

  defp clone_git_source(source, opts) do
    tmp_root =
      Keyword.get(opts, :tmp_dir) ||
        Path.join(System.tmp_dir!(), "mitos-app-source-#{unique_suffix()}")

    app_dir = Path.join(tmp_root, "app")

    with :ok <- mkdir_regular_p(tmp_root),
         {output, 0} <-
           System.cmd("git", ["clone", "--depth", "1", "--", source, app_dir],
             stderr_to_stdout: true
           ) do
      _ = output
      {:ok, "git", {app_dir, fn -> File.rm_rf(tmp_root) end}}
    else
      {output, status} when is_binary(output) and is_integer(status) ->
        File.rm_rf(tmp_root)
        {:error, {:invalid_application_source, "git clone failed with status #{status}"}}

      {:error, reason} ->
        File.rm_rf(tmp_root)
        {:error, {:invalid_application_source, "git clone failed: #{inspect(reason)}"}}
    end
  end

  defp unique_suffix do
    Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)
  end

  defp source_install_id(nil, app, source) do
    [source_basename(source), app.name]
    |> Enum.find_value(fn candidate ->
      candidate = string_or(candidate, nil)
      if candidate && safe_id?(candidate), do: candidate
    end)
    |> case do
      nil -> {:error, {:invalid_application_source, "install_id must be provided"}}
      install_id -> {:ok, install_id}
    end
  end

  defp source_install_id(install_id, _app, _source) when is_binary(install_id) do
    install_id = String.trim(install_id)

    if safe_id?(install_id) do
      {:ok, install_id}
    else
      {:error, {:invalid_application_source, "install_id must be a safe identifier"}}
    end
  end

  defp source_install_id(_install_id, _app, _source),
    do: {:error, {:invalid_application_source, "install_id must be a string"}}

  defp source_basename(source) do
    source
    |> String.trim_trailing("/")
    |> Path.basename()
    |> String.replace_suffix(".git", "")
  end

  defp ensure_install_target_available(app_dir) do
    case File.lstat(app_dir) do
      {:error, :enoent} -> :ok
      {:error, _reason} -> :ok
      {:ok, _stat} -> {:error, {:application_source_conflict, Path.basename(app_dir)}}
    end
  end

  defp ensure_app_registry_dir do
    case mkdir_regular_p(dir()) do
      :ok ->
        :ok

      {:error, reason} ->
        {:error,
         {:invalid_application_source, "could not create app registry: #{inspect(reason)}"}}
    end
  end

  defp copy_app_source(source_dir, app_dir) do
    case copy_app_source_tree(source_dir, source_dir, app_dir) do
      :ok ->
        {:ok, [app_dir]}

      {:error, reason} ->
        File.rm_rf(app_dir)
        {:error, reason}
    end
  end

  defp copy_app_source_tree(root, path, target) do
    case File.lstat(path) do
      {:ok, %{type: :directory}} ->
        with :ok <- mkdir_regular_p(target),
             {:ok, entries} <- File.ls(path) do
          Enum.reduce_while(entries, :ok, fn entry, :ok ->
            case copy_app_source_tree(root, Path.join(path, entry), Path.join(target, entry)) do
              :ok -> {:cont, :ok}
              {:error, _reason} = error -> {:halt, error}
            end
          end)
        else
          {:error, {:mkdir_failed, failed_path, reason}} ->
            {:error,
             {:invalid_application_source, "copy failed at #{failed_path}: #{inspect(reason)}"}}

          {:error, reason} ->
            {:error,
             {:invalid_application_source,
              "could not inspect source tree at #{source_relative_path(root, path)}: #{inspect(reason)}"}}
        end

      {:ok, %{type: :regular}} ->
        copy_app_source_file(root, path, target)

      {:ok, %{type: :symlink}} ->
        {:error,
         {:invalid_application_source,
          "source tree must not contain symlinks: #{source_relative_path(root, path)}"}}

      {:ok, stat} ->
        {:error,
         {:invalid_application_source,
          "source tree entry must be a regular file or directory: #{source_relative_path(root, path)} (#{inspect(stat.type)})"}}

      {:error, reason} ->
        {:error,
         {:invalid_application_source,
          "could not inspect source tree at #{source_relative_path(root, path)}: #{inspect(reason)}"}}
    end
  end

  defp copy_app_source_file(root, path, target) do
    relative = source_relative_path(root, path)

    with {:ok, bytes} <- read_stable_regular_source(path),
         :ok <- write_new_regular_target(target, bytes) do
      :ok
    else
      {:error, reason} ->
        {:error, {:invalid_application_source, "copy failed at #{relative}: #{inspect(reason)}"}}
    end
  end

  defp read_stable_regular_source(path) do
    with {:ok, before_stat = %{type: :regular}} <- File.lstat(path),
         {:ok, bytes} <- File.read(path),
         {:ok, after_stat = %{type: :regular}} <- File.lstat(path),
         true <- stable_file_read?(before_stat, after_stat, byte_size(bytes)) do
      {:ok, bytes}
    else
      {:ok, %{type: :symlink}} -> {:error, :symlink}
      {:ok, _stat} -> {:error, :not_regular}
      false -> {:error, :changed_during_copy}
      {:error, reason} -> {:error, reason}
    end
  end

  defp stable_file_read?(before_stat, after_stat, byte_size) do
    before_stat.size == byte_size and
      before_stat.size == after_stat.size and
      before_stat.inode == after_stat.inode and
      before_stat.mtime == after_stat.mtime
  end

  defp write_new_regular_target(target, bytes) do
    case File.open(target, [:write, :exclusive], fn io -> IO.binwrite(io, bytes) end) do
      {:ok, :ok} -> :ok
      {:error, reason} -> {:error, reason}
    end
  end

  defp source_relative_path(root, path) do
    case Path.relative_to(path, root) do
      "." -> Path.basename(root)
      relative -> relative
    end
  end

  defp select_input_binding(app, connector, path) do
    connector = String.trim(connector)

    candidates =
      app
      |> input_bindings()
      |> Enum.filter(&(&1["connector"] == connector))
      |> Enum.filter(&path_matches?(&1, path))

    case candidates do
      [binding] ->
        with :ok <- ensure_explicit_selection(binding) do
          {:ok, binding}
        end

      [] ->
        {:error, {:application_connector_not_found, connector}}

      _many ->
        {:error, {:invalid_application_io, "input binding is ambiguous"}}
    end
  end

  defp input_bindings(app) do
    case list_or(get_in(app.io, ["inputs"])) do
      [] -> default_io_inputs(app)
      bindings -> bindings
    end
  end

  defp path_matches?(_binding, nil), do: true
  defp path_matches?(_binding, ""), do: true

  defp path_matches?(%{"path" => path}, requested) when is_binary(path) and path != "",
    do: path == requested

  defp path_matches?(%{"connector" => "webhook"}, requested) when is_binary(requested),
    do: false

  defp path_matches?(_binding, _requested), do: true

  defp ensure_explicit_selection(%{"select" => %{"mode" => "smart"}}),
    do: {:error, {:unsupported_application_selection, "smart"}}

  defp ensure_explicit_selection(%{"select" => %{"mode" => mode}})
       when is_binary(mode) and mode not in ["", "explicit"],
       do: {:error, {:unsupported_application_selection, mode}}

  defp ensure_explicit_selection(_binding), do: :ok

  defp entrypoint_for_binding(app, binding) do
    select = map_or(binding["select"], %{})
    entrypoint_id = string_or(binding["entrypoint"], nil)
    pipeline_id = string_or(select["pipeline"], nil)

    cond do
      entrypoint_id ->
        ensure_binding_entrypoint(app, entrypoint_id, pipeline_id)

      pipeline_id ->
        entrypoint_for_pipeline(app, pipeline_id)

      true ->
        {:error,
         {:invalid_application_io, "input binding requires entrypoint or select.pipeline"}}
    end
  end

  defp ensure_binding_entrypoint(app, entrypoint_id, nil) do
    if Enum.any?(app.entrypoints, &(&1["id"] == entrypoint_id)) do
      {:ok, entrypoint_id}
    else
      {:error, {:invalid_application_io, "input binding references unknown entrypoint"}}
    end
  end

  defp ensure_binding_entrypoint(app, entrypoint_id, pipeline_id) do
    case Enum.find(app.entrypoints, &(&1["id"] == entrypoint_id)) do
      %{"pipeline" => ^pipeline_id} ->
        {:ok, entrypoint_id}

      %{} ->
        {:error, {:invalid_application_io, "input binding pipeline does not match entrypoint"}}

      nil ->
        {:error, {:invalid_application_io, "input binding references unknown entrypoint"}}
    end
  end

  defp entrypoint_for_pipeline(app, pipeline_id) do
    app.entrypoints
    |> Enum.filter(&(&1["pipeline"] == pipeline_id))
    |> case do
      [%{"id" => entrypoint_id}] ->
        {:ok, entrypoint_id}

      [] ->
        {:error, {:invalid_application_io, "input binding references unknown pipeline"}}

      _many ->
        {:error, {:invalid_application_io, "input binding pipeline is ambiguous"}}
    end
  end

  defp connector_run_opts(connector, binding, opts) do
    opts
    |> Keyword.put(:connector, connector)
    |> Keyword.put(:source, "connector:#{connector}")
    |> maybe_kw(:idempotency_key, connector_idempotency_key(opts))
    |> maybe_kw(:reply_ref, Keyword.get(opts, :reply_ref) || binding["reply_ref"])
  end

  defp duplicate_connector_result(app, binding, connector, entrypoint_id, opts) do
    case opts |> Keyword.get(:idempotency_key) |> string_or(nil) do
      nil ->
        :not_found

      idempotency_key ->
        duplicate_trace_events(opts)
        |> Enum.find_value(fn event ->
          if duplicate_signal_event?(event, app, connector, entrypoint_id, idempotency_key, opts) do
            event
          end
        end)
        |> case do
          nil -> :not_found
          signal -> {:ok, duplicate_trace_response(app, binding, entrypoint_id, signal, opts)}
        end
    end
  end

  defp duplicate_trace_events(opts) do
    case Keyword.get(opts, :base_dir) || Keyword.get(opts, :trace_base_dir) do
      nil ->
        Mitos.Runs.all_events(per_run_limit: :all)

      base_dir ->
        base_dir
        |> Path.join(".runs")
        |> trace_run_ids()
        |> Enum.flat_map(&Mitos.Trace.read_events(&1, base_dir: base_dir))
    end
  end

  defp trace_run_ids(runs_dir) do
    case File.ls(runs_dir) do
      {:ok, entries} ->
        entries
        |> Enum.filter(&regular_dir?(Path.join(runs_dir, &1)))

      {:error, _reason} ->
        []
    end
  end

  defp duplicate_signal_event?(
         %{
           "kind" => "effect",
           "type" => "signal_received",
           "ref" => %{
             "app_id" => app_id,
             "connector" => connector,
             "entrypoint" => entrypoint_id,
             "idempotency_key" => idempotency_key
           }
         } = event,
         app,
         connector,
         entrypoint_id,
         idempotency_key,
         opts
       ),
       do: app_id == app.app_id and trace_tenant_matches?(event, app, opts)

  defp duplicate_signal_event?(_event, _app, _connector, _entrypoint_id, _idempotency_key, _opts),
    do: false

  defp trace_tenant_matches?(%{"tenant" => %{} = tenant}, app, opts) do
    tenant_scope(tenant) == tenant_scope(expected_trace_tenant(app, opts))
  end

  defp trace_tenant_matches?(_event, app, opts) do
    tenant_scope(expected_trace_tenant(app, opts)) == tenant_scope(default_trace_tenant())
  end

  defp expected_trace_tenant(app, opts) do
    if Keyword.has_key?(opts, :tenant) do
      expected_trace_tenant(Keyword.get(opts, :tenant))
    else
      expected_trace_tenant(%{"org" => "default", "project" => app.name})
    end
  end

  defp expected_trace_tenant(%{} = tenant) do
    default_trace_tenant()
    |> Map.put("org_id", tenant_value(tenant, ["org", "org_id", :org, :org_id]) || "default")
    |> Map.put(
      "project_id",
      tenant_value(tenant, ["project", "project_id", :project, :project_id]) || "default"
    )
    |> Map.put(
      "actor_id",
      tenant_value(tenant, ["actor", "actor_id", :actor, :actor_id]) || "system"
    )
  end

  defp expected_trace_tenant(_tenant), do: default_trace_tenant()

  defp default_trace_tenant do
    %{"org_id" => "default", "project_id" => "default", "actor_id" => "system"}
  end

  defp tenant_scope(%{} = tenant) do
    {tenant["org_id"], tenant["project_id"]}
  end

  defp tenant_value(tenant, keys) do
    keys
    |> Enum.find_value(fn key ->
      case Map.get(tenant, key) do
        value when is_binary(value) and value != "" -> value
        _other -> nil
      end
    end)
  end

  defp duplicate_trace_response(app, binding, entrypoint_id, signal, opts) do
    run_id = signal["run_id"]
    task_id = get_in(signal, ["ref", "task_id"])

    events =
      if is_binary(run_id), do: Mitos.Trace.read_events(run_id, trace_read_opts(opts)), else: []

    task = task_from_events(events, task_id) || %{}
    status_event = task_status_event(events, task_id)
    pipeline_result = get_in(status_event, ["payload", "result"]) || %{}
    deliveries = deliveries_from_events(events, task_id)
    pipeline_id = task["pipeline"] || pipeline_result["pipeline_id"] || binding_pipeline(binding)

    %{
      "app_id" => app.app_id,
      "app_name" => app.name,
      "app_run_id" => run_id,
      "task_id" => task_id,
      "entrypoint" => entrypoint_id,
      "pipeline_id" => pipeline_id,
      "status" => duplicate_status(pipeline_result, status_event),
      "deliverable_summary" => pipeline_result["deliverable_summary"],
      "deliveries" => deliveries,
      "pipeline" => pipeline_result,
      "evidence" =>
        pipeline_result
        |> Map.get("evidence", %{})
        |> map_or(%{})
        |> Map.put("app_run_id", run_id)
        |> Map.put("app_id", app.app_id),
      "duplicate?" => true
    }
    |> drop_nil()
  end

  defp task_from_events(events, task_id) do
    Enum.find_value(events, fn
      %{
        "kind" => "effect",
        "type" => "task_enqueued",
        "payload" => %{"task" => %{"id" => ^task_id} = task}
      } ->
        task

      _event ->
        nil
    end)
  end

  defp task_status_event(events, task_id) do
    events
    |> Enum.reverse()
    |> Enum.find(fn
      %{
        "kind" => "effect",
        "type" => type,
        "ref" => %{"task_id" => ^task_id}
      }
      when type in ["task_done", "task_failed", "task_running", "task_scheduled"] ->
        true

      _event ->
        false
    end)
  end

  defp deliveries_from_events(events, task_id) do
    events
    |> Enum.flat_map(fn
      %{
        "kind" => "effect",
        "type" => "delivery_sent",
        "ref" => %{"task_id" => ^task_id},
        "payload" => payload
      } ->
        [
          %{
            "receipt" => payload["receipt"],
            "payload" => payload["rendered"]
          }
          |> drop_nil()
        ]

      _event ->
        []
    end)
  end

  defp delivery_sent_count(events, task_id) do
    Enum.count(events, fn
      %{
        "kind" => "effect",
        "type" => "delivery_sent",
        "ref" => %{"task_id" => ^task_id}
      } ->
        true

      _event ->
        false
    end)
  end

  defp ensure_app_run_events(_app_run_id, [_event | _rest]), do: :ok

  defp ensure_app_run_events(app_run_id, _events),
    do: {:error, {:application_run_not_found, app_run_id}}

  defp app_signal_event(events) do
    events
    |> Enum.find(fn
      %{
        "kind" => "effect",
        "type" => "signal_received",
        "ref" => %{"app_id" => app_id, "task_id" => task_id}
      }
      when is_binary(app_id) and is_binary(task_id) ->
        true

      _event ->
        false
    end)
    |> case do
      nil -> {:error, {:application_run_not_deliverable, "run has no app ingress signal"}}
      signal -> {:ok, signal}
    end
  end

  defp signal_app_id(%{"ref" => %{"app_id" => app_id}}) when is_binary(app_id),
    do: {:ok, app_id}

  defp signal_app_id(_signal),
    do: {:error, {:application_run_not_deliverable, "run signal has no app_id"}}

  defp redrive_task(events, signal) do
    task_id = get_in(signal, ["ref", "task_id"])

    case is_binary(task_id) && task_from_events(events, task_id) do
      %{} = task -> {:ok, task}
      _missing -> {:error, {:application_run_not_deliverable, "run has no app task"}}
    end
  end

  defp redrive_task_done(events, task_id) do
    events
    |> Enum.reverse()
    |> Enum.find(fn
      %{"kind" => "effect", "type" => "task_done", "ref" => %{"task_id" => ^task_id}} ->
        true

      _event ->
        false
    end)
    |> case do
      nil -> {:error, {:application_run_not_completed, task_id}}
      done -> {:ok, done}
    end
  end

  defp redrive_pipeline_result(%{"payload" => %{"result" => %{} = result}}), do: {:ok, result}

  defp redrive_pipeline_result(_done),
    do: {:error, {:application_run_not_deliverable, "task_done has no pipeline result"}}

  defp duplicate_status(%{"status" => status}, _status_event) when is_binary(status), do: status
  defp duplicate_status(_pipeline_result, %{"payload" => %{"status" => "done"}}), do: "completed"
  defp duplicate_status(_pipeline_result, %{"payload" => %{"status" => status}}), do: status
  defp duplicate_status(_pipeline_result, _status_event), do: "accepted"

  defp binding_pipeline(%{"select" => %{"pipeline" => pipeline}}) when is_binary(pipeline),
    do: pipeline

  defp binding_pipeline(_binding), do: nil

  defp enrich_connector_result(result, app_id, app, entrypoint_id, connector, binding) do
    result
    |> Map.put("app_id", app.app_id)
    |> Map.put("app", public_view(app, local_id: local_id(app_id, app)))
    |> Map.put("entrypoint", entrypoint_id)
    |> Map.put("connector", connector)
    |> Map.put("io_binding", public_io_binding(binding))
  end

  defp connector_idempotency_key(opts) do
    Keyword.get(opts, :idempotency_key) ||
      Keyword.get(opts, :delivery_id) ||
      Keyword.get(opts, :signal_id)
  end

  defp public_io_binding(%{} = binding) do
    Mitos.IOPlane.public_binding(binding)
  end

  defp resolve_app_active_set(app, units, opts) do
    app_target = app_target(app)

    config =
      %{
        "units" => units,
        "targets" => normalize_maps(Keyword.get(opts, :targets, [])) ++ [app_target],
        "presets" => normalize_maps(Keyword.get(opts, :presets, [])),
        "pipelines" =>
          normalize_maps(Keyword.get(opts, :pipelines, [])) ++ app_pipeline_targets(app),
        "active_targets" => [app_target["id"]],
        "active_pipelines" =>
          app_pipeline_ids(app) ++ string_list(Keyword.get(opts, :active_pipelines)),
        "project_type" => string_or(Keyword.get(opts, :project_type), nil),
        "enable" => string_list(Keyword.get(opts, :enable)),
        "disable" => string_list(Keyword.get(opts, :disable))
      }

    case Mitos.ServiceRuntime.resolve_active_set(config) do
      {:ok, active_set} ->
        {:ok, active_set}

      {:error, {:activation_conflict, ids}} ->
        {:error, {:application_activation_conflict, ids}}

      {:error, reason} ->
        {:error, {:application_activation_failed, inspect(reason)}}
    end
  end

  defp app_target(app) do
    %{
      "id" => app_target_id(app),
      "description" => "Application #{app.name}",
      "scope" => "project",
      "requires" => app_required_services(app),
      "wants" => app_wanted_services(app)
    }
  end

  defp app_target_id(app), do: app.app_id

  defp app_pipeline_targets(app) do
    Enum.map(app.pipelines, fn pipeline ->
      %{
        "id" => pipeline["id"],
        "requires" => map_or(pipeline["requires"], %{})
      }
    end)
  end

  defp app_pipeline_ids(app), do: Enum.map(app.pipelines, & &1["id"])

  defp ensure_required_services_present(app, units) do
    required = app_required_services(app) ++ pipeline_required_services(app)
    missing = missing_services(required, units)

    case missing do
      [] ->
        :ok

      [service_id | _rest] ->
        {:error, {:application_activation_failed, "missing required service #{service_id}"}}
    end
  end

  defp app_required_services(app), do: string_list(get_in(app.services, ["requires"]))
  defp app_wanted_services(app), do: string_list(get_in(app.services, ["wants"]))

  defp pipeline_service_requirements(app) do
    app.pipelines
    |> Enum.map(fn pipeline ->
      %{
        "pipeline" => pipeline["id"],
        "services" => string_list(get_in(pipeline, ["requires", "services"])),
        "targets" => string_list(get_in(pipeline, ["requires", "targets"]))
      }
    end)
    |> Enum.reject(fn requirement ->
      requirement["services"] == [] and requirement["targets"] == []
    end)
  end

  defp pipeline_required_services(app) do
    app
    |> pipeline_service_requirements()
    |> Enum.flat_map(& &1["services"])
  end

  defp missing_services(ids, units) do
    unit_ids = units |> Enum.map(& &1["id"]) |> MapSet.new()

    ids
    |> Enum.uniq()
    |> Enum.reject(&MapSet.member?(unit_ids, &1))
  end

  defp io_projection(app) do
    inputs = list_or(get_in(app.io, ["inputs"]))
    outputs = list_or(get_in(app.io, ["outputs"]))

    if inputs == [] and outputs == [] do
      %{
        "inputs" => default_io_inputs(app),
        "outputs" => default_io_outputs(app),
        "defaults_applied?" => true,
        "credential_scope" => "tenant_control_plane"
      }
    else
      %{
        "inputs" => inputs,
        "outputs" => outputs,
        "defaults_applied?" => false,
        "credential_scope" => "tenant_control_plane"
      }
    end
  end

  defp default_io_inputs(app) do
    app.entrypoints
    |> List.first()
    |> case do
      nil ->
        []

      entrypoint ->
        for connector <- ["manual", "web"] do
          %{
            "connector" => connector,
            "entrypoint" => entrypoint["id"],
            "select" => %{"mode" => "explicit", "pipeline" => entrypoint["pipeline"]},
            "default?" => true
          }
        end
    end
  end

  defp default_io_outputs(app) do
    app.entrypoints
    |> List.first()
    |> case do
      nil ->
        []

      entrypoint ->
        [
          %{
            "sink" => "web",
            "entrypoint" => entrypoint["id"],
            "on" => "run.done",
            "render" => "result.summary + artifacts + proof_excerpt",
            "default?" => true
          }
        ]
    end
  end

  defp normalize_service_units(values) do
    values
    |> list_or()
    |> Enum.reduce_while({:ok, []}, fn value, {:ok, acc} ->
      value = stringify_value(value)

      try do
        case Mitos.ServiceRuntime.unit(value) do
          {:error, {:invalid_service_unit, detail}} ->
            {:halt, {:error, {:invalid_application_activation, detail}}}

          {:error, reason} ->
            {:halt, {:error, {:invalid_application_activation, inspect(reason)}}}

          %{} = unit ->
            {:cont, {:ok, acc ++ [unit]}}
        end
      rescue
        error ->
          {:halt, {:error, {:invalid_application_activation, Exception.message(error)}}}
      end
    end)
  end

  defp normalize_maps(values) do
    values
    |> list_or()
    |> Enum.flat_map(fn
      %{} = value -> [stringify_value(value)]
      _value -> []
    end)
  end

  defp app_dirs do
    case trusted_app_registry_dir() do
      {:ok, root} -> app_dirs(root)
      {:error, _reason} -> []
    end
  end

  defp app_dirs(root) do
    case File.ls(root) do
      {:ok, entries} ->
        entries
        |> Enum.map(&Path.join(root, &1))
        |> Enum.filter(&regular_dir?/1)

      {:error, _reason} ->
        []
    end
  end

  defp find_loaded(id) do
    app_dirs()
    |> Enum.find_value({:error, :not_found}, fn app_dir ->
      with {:ok, app} <- Mitos.AppManifest.load(app_dir),
           true <- id in [app.name, app.app_id] do
        {:ok, app}
      else
        _other -> nil
      end
    end)
  end

  defp app_dir(id) do
    cond do
      safe_id?(id) ->
        case trusted_app_registry_dir() do
          {:ok, root} ->
            path = Path.join(root, id)

            if regular_dir?(path) do
              {:ok, path}
            else
              find_app_dir(id)
            end

          {:error, _reason} ->
            {:error, :not_found}
        end

      true ->
        find_app_dir(id)
    end
  end

  defp find_app_dir(id) do
    app_dirs()
    |> Enum.find_value({:error, :not_found}, fn app_dir ->
      with {:ok, app} <- Mitos.AppManifest.load(app_dir),
           true <- id in [app.name, app.app_id] do
        {:ok, app_dir}
      else
        _other -> nil
      end
    end)
  end

  defp safe_id?(id), do: Regex.match?(@id_pattern, id)

  defp regular_dir?(path), do: match?({:ok, %{type: :directory}}, File.lstat(path))
  defp symlink_path?(path), do: match?({:ok, %{type: :symlink}}, File.lstat(path))

  defp trusted_app_registry_dir, do: trusted_directory(dir())

  defp trusted_packages_dir, do: trusted_directory(packages_dir())

  defp trusted_directory(path) do
    path = Path.expand(path)

    with true <- regular_dir?(path),
         true <- no_symlink_absolute_path?(path) do
      {:ok, path}
    else
      _ -> {:error, :untrusted_directory}
    end
  end

  defp no_symlink_absolute_path?(path) do
    root = Path.expand("/")
    path = Path.expand(path)
    parts = if path == root, do: [], else: Path.split(Path.relative_to(path, root))

    with {:ok, %{type: root_type}} <- File.lstat(root),
         true <- root_type != :symlink do
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

  defp mkdir_regular_p(path) do
    path = Path.expand(path)
    parent = Path.dirname(path)

    if path == parent do
      ensure_existing_regular_dir(path)
    else
      with :ok <- mkdir_regular_p(parent) do
        ensure_regular_dir(path)
      end
    end
  end

  defp ensure_existing_regular_dir(path) do
    case File.lstat(path) do
      {:ok, %{type: :directory}} -> :ok
      {:ok, %{type: :symlink}} -> {:error, {:mkdir_failed, path, :symlink}}
      {:ok, _stat} -> {:error, {:mkdir_failed, path, :not_directory}}
      {:error, reason} -> {:error, {:mkdir_failed, path, reason}}
    end
  end

  defp ensure_regular_dir(path) do
    case File.lstat(path) do
      {:ok, %{type: :directory}} ->
        :ok

      {:ok, %{type: :symlink}} ->
        {:error, {:mkdir_failed, path, :symlink}}

      {:ok, _stat} ->
        {:error, {:mkdir_failed, path, :not_directory}}

      {:error, :enoent} ->
        case File.mkdir(path) do
          :ok -> :ok
          {:error, :eexist} -> ensure_regular_dir(path)
          {:error, reason} -> {:error, {:mkdir_failed, path, reason}}
        end

      {:error, reason} ->
        {:error, {:mkdir_failed, path, reason}}
    end
  end

  defp package_id(nil, app), do: {:ok, app.name}

  defp package_id(package_id, _app) when is_binary(package_id) do
    package_id = String.trim(package_id)

    if safe_id?(package_id) do
      {:ok, package_id}
    else
      {:error, {:invalid_application_package, "package_id must be a safe identifier"}}
    end
  end

  defp package_id(_package_id, _app),
    do: {:error, {:invalid_application_package, "package_id must be a string"}}

  defp package_dir_id(package_id) when is_binary(package_id) do
    package_id = String.trim(package_id)

    if safe_id?(package_id) do
      {:ok, package_id}
    else
      {:error, {:invalid_application_package, "package_id must be a safe identifier"}}
    end
  end

  defp install_id(nil, package_id), do: {:ok, package_id}

  defp install_id(install_id, _package_id) when is_binary(install_id) do
    install_id = String.trim(install_id)

    if safe_id?(install_id) do
      {:ok, install_id}
    else
      {:error, {:invalid_application_package, "install_id must be a safe identifier"}}
    end
  end

  defp install_id(_install_id, _package_id),
    do: {:error, {:invalid_application_package, "install_id must be a string"}}

  defp local_id(request_id, app) do
    if request_id in [app.name, app.app_id], do: app.name, else: request_id
  end

  defp trace_opts(app_run_id, opts) do
    [run_id: app_run_id]
    |> maybe_kw(:base_dir, Keyword.get(opts, :base_dir) || Keyword.get(opts, :trace_base_dir))
  end

  defp trace_read_opts(opts) do
    []
    |> maybe_kw(:base_dir, Keyword.get(opts, :base_dir) || Keyword.get(opts, :trace_base_dir))
  end

  defp map_or(%{} = map, _default), do: stringify_value(map)
  defp map_or(_value, default), do: default

  defp list_or(values) when is_list(values), do: Enum.map(values, &stringify_value/1)
  defp list_or(_values), do: []

  defp string_list(values) when is_list(values) do
    values
    |> Enum.filter(&is_binary/1)
    |> Enum.map(&String.trim/1)
    |> Enum.reject(&(&1 == ""))
  end

  defp string_list(_values), do: []

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default

  defp stringify_value(%{} = map) do
    Map.new(map, fn {key, value} -> {to_string(key), stringify_value(value)} end)
  end

  defp stringify_value(values) when is_list(values), do: Enum.map(values, &stringify_value/1)
  defp stringify_value(value), do: value

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, _key, []), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)

  defp maybe_kw(opts, _key, nil), do: opts
  defp maybe_kw(opts, key, value), do: Keyword.put(opts, key, value)

  defp drop_nil(map), do: Enum.reject(map, fn {_key, value} -> is_nil(value) end) |> Map.new()
end
