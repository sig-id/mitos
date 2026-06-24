defmodule Mitos.AppRunner do
  @moduledoc """
  Runs a local agentic application entrypoint through the backend engine.

  This is the executable bridge for `spec/12_APPLICATION.md`: load an app
  manifest, validate entrypoint inputs, map the entrypoint to a WorkPool Task,
  compile the bundled markdown pipeline into ordered capability steps, and run
  those steps via `Mitos.PipelineRunner`.
  """

  alias Mitos.AppManifest
  alias Mitos.IOPlane
  alias Mitos.Trace
  alias Mitos.WorkPool

  @id_pattern ~r/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/
  @path_ref_prefixes ["./", "app/", "apps/", "capabilities/", "workloads/"]
  @reserved_app_files ~w(app.json app.yaml app.yml bindings.json)

  @doc "Run one entrypoint from a local app directory or manifest path."
  @spec run(String.t(), String.t(), map(), keyword()) :: {:ok, map()} | {:error, term()}
  def run(path_or_dir, entrypoint_id, inputs, opts \\ [])

  def run(path_or_dir, entrypoint_id, inputs, opts)
      when is_binary(path_or_dir) and is_binary(entrypoint_id) and is_map(inputs) do
    with {:ok, app} <- AppManifest.load(path_or_dir),
         {:ok, task} <- AppManifest.task_for_entrypoint(app, entrypoint_id, inputs, opts),
         {:ok, pipeline} <- compile_task_pipeline(app, task) do
      trace = start_app_trace(app, task, pipeline, opts)

      case Mitos.PipelineRunner.execute_definition(
             pipeline,
             pipeline_bindings(task["payload"]["inputs"]),
             pipeline_opts(app, task, opts)
           ) do
        {:ok, pipeline_result} ->
          trace =
            WorkPool.append_status(trace, task["id"], :done,
              caused_by: 0,
              run_id: pipeline_result["execution_id"],
              result: pipeline_result
            )

          terminal_seq = trace.seq - 1

          {trace, deliveries} =
            IOPlane.append_deliveries(
              trace,
              app,
              task,
              pipeline_result,
              Keyword.put(opts, :caused_by, terminal_seq)
            )

          {:ok, app_response(app, task, trace.run_id, pipeline_result, deliveries)}

        {:error, reason} ->
          _trace =
            WorkPool.append_status(trace, task["id"], :failed,
              caused_by: 0,
              reason: inspect(reason)
            )

          {:error, {:app_run_failed, trace.run_id, reason}}
      end
    end
  end

  def run(_path_or_dir, _entrypoint_id, _inputs, _opts),
    do: {:error, {:invalid_application_input, "inputs must be an object"}}

  defp compile_task_pipeline(%AppManifest{} = app, %{} = task) do
    with {:ok, pipeline_meta} <- app_pipeline(app, task["pipeline"]),
         {:ok, markdown} <- read_pipeline_markdown(app, pipeline_meta),
         {:ok, workflow} <- workflow_from_markdown(app, markdown) do
      {:ok,
       %{
         "id" => pipeline_meta["id"],
         "pipeline_id" => "app:#{app.app_id}:#{pipeline_meta["id"]}",
         "workflow" => workflow
       }}
    end
  end

  defp app_pipeline(%AppManifest{} = app, pipeline_id) do
    case Enum.find(app.pipelines, &(&1["id"] == pipeline_id)) do
      nil -> {:error, {:invalid_application, "entrypoint pipeline is unavailable"}}
      %{} = pipeline -> {:ok, pipeline}
    end
  end

  defp read_pipeline_markdown(%AppManifest{root: root}, %{"file" => file})
       when is_binary(root) and is_binary(file) do
    path = Path.expand(file, root)

    with true <- inside_root?(path, root),
         {:ok, raw} <- File.read(path) do
      {:ok, raw}
    else
      false ->
        {:error, {:invalid_application, "pipeline file escapes the application root"}}

      {:error, reason} ->
        {:error, {:invalid_application, "could not read pipeline file: #{inspect(reason)}"}}
    end
  end

  defp read_pipeline_markdown(_app, _pipeline),
    do: {:error, {:invalid_application, "application root is required"}}

  defp workflow_from_markdown(%AppManifest{} = app, markdown) when is_binary(markdown) do
    aliases = capability_aliases(app)
    steps = markdown_steps(markdown)

    steps =
      cond do
        steps != [] ->
          steps

        map_size(aliases) == 1 ->
          [single_capability_step(aliases)]

        true ->
          []
      end

    cond do
      steps == [] ->
        {:error, {:invalid_pipeline, "pipeline has no executable steps"}}

      unknown_step_alias(steps, aliases) ->
        {:error, {:invalid_pipeline, "pipeline references unknown capability alias"}}

      true ->
        {:ok,
         %{
           "goal" => markdown_goal(markdown),
           "allowed_capabilities" => Map.keys(aliases),
           "steps" => normalize_step_ids(steps)
         }}
    end
  end

  defp capability_aliases(%AppManifest{} = app) do
    Map.new(app.capabilities, fn %{"as" => alias, "ref" => ref} -> {alias, ref} end)
  end

  defp markdown_steps(markdown) do
    markdown
    |> String.split("\n")
    |> stage_lines()
    |> Enum.flat_map(&parse_step_line/1)
  end

  defp stage_lines(lines) do
    {stage_lines, saw_stages?} =
      Enum.reduce(lines, {[], false, false}, fn line, {acc, in_stages?, saw_stages?} ->
        cond do
          heading?(line, "stages") ->
            {acc, true, true}

          in_stages? and String.starts_with?(String.trim_leading(line), "#") ->
            {acc, false, saw_stages?}

          in_stages? ->
            {[line | acc], in_stages?, saw_stages?}

          true ->
            {acc, in_stages?, saw_stages?}
        end
      end)
      |> then(fn {acc, _in_stages?, saw_stages?} -> {Enum.reverse(acc), saw_stages?} end)

    if saw_stages?, do: stage_lines, else: lines
  end

  defp heading?(line, name) do
    line = String.trim(line)

    String.starts_with?(line, "#") and
      line
      |> String.trim_leading("#")
      |> String.trim()
      |> String.downcase()
      |> String.starts_with?(name)
  end

  defp parse_step_line(line) do
    line = String.trim(line)

    cond do
      captures =
          Regex.named_captures(
            ~r/^(?:\d+[.)]|[-*])\s+\*\*(?<alias>[A-Za-z0-9_-]+)\*\*(?:\s*[—-]\s*(?<notes>.*))?$/u,
            line
          ) ->
        alias = captures["alias"]

        [
          %{"name" => alias, "capability_id" => alias}
          |> maybe_put("notes", clean_notes(captures["notes"]))
        ]

      captures =
          Regex.named_captures(
            ~r/^(?:\d+[.)]|[-*])\s+(?<name>.+?)\s+\[(?<alias>[A-Za-z0-9_-]+)\](?:\s*[—-]\s*(?<notes>.*))?$/u,
            line
          ) ->
        [
          %{"name" => String.trim(captures["name"]), "capability_id" => captures["alias"]}
          |> maybe_put("notes", clean_notes(captures["notes"]))
        ]

      true ->
        []
    end
  end

  defp clean_notes(nil), do: nil

  defp clean_notes(notes) do
    notes = String.trim(notes)
    if notes == "", do: nil, else: notes
  end

  defp single_capability_step(aliases) do
    alias = aliases |> Map.keys() |> hd()
    %{"name" => alias, "capability_id" => alias}
  end

  defp unknown_step_alias(steps, aliases) do
    Enum.find(steps, fn step -> not Map.has_key?(aliases, step["capability_id"]) end)
  end

  defp normalize_step_ids(steps) do
    steps
    |> Enum.with_index(1)
    |> Enum.map(fn {step, index} ->
      Map.put(step, "id", step_id(step["capability_id"], index))
    end)
  end

  defp step_id(alias, index) do
    if index == 1, do: alias, else: "#{alias}-#{index}"
  end

  defp markdown_goal(markdown) do
    Regex.run(~r/^Goal:\s*(.+)$/mi, markdown, capture: :all_but_first)
    |> case do
      [goal] -> String.trim(goal)
      _ -> "Run application pipeline"
    end
  end

  defp start_app_trace(app, task, pipeline, opts) do
    run_id =
      Keyword.get(opts, :run_id) ||
        "app-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)

    trace =
      [run_id: run_id, launch_spec: app_launch_spec(app, task, pipeline)]
      |> maybe_kw(:base_dir, Keyword.get(opts, :trace_base_dir))
      |> Trace.open!()

    trace
    |> IOPlane.append_signal_received(app, task, opts)
    |> WorkPool.append_enqueue(task, caused_by: 0)
    |> WorkPool.append_status(task["id"], :scheduled, caused_by: 0, run_id: run_id)
    |> WorkPool.append_status(task["id"], :running, caused_by: 0, run_id: run_id)
  end

  defp app_launch_spec(app, task, pipeline) do
    %{
      provider: "app",
      app_id: app.app_id,
      app_name: app.name,
      entrypoint: task["provenance"]["entrypoint"],
      pipeline: pipeline["id"],
      pipeline_id: pipeline["pipeline_id"]
    }
  end

  defp pipeline_opts(app, task, opts) do
    opts
    |> Keyword.put(:task_id, task["id"])
    |> Keyword.put(:pool, task["pool"])
    |> Keyword.put(:tenant, task["tenant"])
    |> Keyword.put(:budget, task["budget"])
    |> Keyword.put(:provenance, task["provenance"])
    |> Keyword.put(:pipeline_execution_id, "pipe-" <> task["id"])
    |> Keyword.put(:capability_resolver, fn alias -> resolve_capability(app, alias) end)
  end

  defp resolve_capability(%AppManifest{} = app, alias) when is_binary(alias) do
    aliases = capability_aliases(app)

    case Map.fetch(aliases, alias) do
      {:ok, ref} -> resolve_capability_ref(app, alias, ref)
      :error -> {:error, {:unknown_capability_alias, alias}}
    end
  end

  defp resolve_capability_ref(app, alias, ref)
       when is_binary(ref) and ref != "" do
    cond do
      capability_id_ref?(ref) ->
        resolve_catalog_capability_ref(ref)

      oci_digest_ref?(ref) ->
        resolve_catalog_oci_ref(ref)

      String.starts_with?(ref, "oci://") ->
        {:error, {:unsupported_app_capability_ref, ref}}

      path_ref?(ref) ->
        :error

      Regex.match?(@id_pattern, ref) ->
        Mitos.Workload.fetch(ref)

      true ->
        :error
    end
    |> case do
      :error -> resolve_local_capability_ref(app, alias, ref)
      other -> other
    end
  end

  defp resolve_capability_ref(_app, _alias, ref),
    do: {:error, {:invalid_application, "capability ref is invalid: #{inspect(ref)}"}}

  defp resolve_local_capability_ref(%AppManifest{root: root}, alias, ref) when is_binary(root) do
    with :ok <- safe_relative_path(ref),
         path <- Path.expand(ref, root),
         true <- inside_root?(path, root),
         {:ok, workload_path} <- workload_path(path, alias),
         {:ok, raw} <- File.read(workload_path),
         {:ok, json} <- Jason.decode(strip_json_comments(raw)),
         {:ok, workload} <-
           Mitos.Workload.from_json(alias, json, package_root: Path.dirname(workload_path)) do
      {:ok, workload}
    else
      false ->
        {:error, {:invalid_application, "capability ref escapes the application root"}}

      {:error, %Jason.DecodeError{} = error} ->
        {:error,
         {:invalid_application, "bad capability workload JSON: #{Exception.message(error)}"}}

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp resolve_local_capability_ref(_app, _alias, ref),
    do: {:error, {:unsupported_app_capability_ref, ref}}

  defp resolve_catalog_capability_ref(ref) do
    Mitos.Workload.list()
    |> Enum.find(fn
      %Mitos.Workload{capability: %Mitos.Capability{id: ^ref}} -> true
      _workload -> false
    end)
    |> case do
      nil -> {:error, {:unknown_capability_ref, ref}}
      %Mitos.Workload{} = workload -> {:ok, workload}
    end
  end

  defp resolve_catalog_oci_ref(ref) do
    Mitos.Workload.list()
    |> Enum.find(fn
      %Mitos.Workload{capability: %Mitos.Capability{image: ^ref}} -> true
      _workload -> false
    end)
    |> case do
      nil -> {:error, {:unknown_capability_ref, ref}}
      %Mitos.Workload{} = workload -> {:ok, workload}
    end
  end

  defp capability_id_ref?(ref), do: String.starts_with?(ref, "cap:sha256:")

  defp oci_digest_ref?(ref),
    do: String.starts_with?(ref, "oci://") and String.contains?(ref, "@sha256:")

  defp path_ref?(ref) do
    String.ends_with?(ref, ".json") or String.contains?(ref, "/") or
      Enum.any?(@path_ref_prefixes, &String.starts_with?(ref, &1))
  end

  defp safe_relative_path(path) do
    cond do
      Path.type(path) != :relative ->
        {:error, {:invalid_application, "capability refs must be relative paths or workload ids"}}

      ".." in Path.split(path) ->
        {:error, {:invalid_application, "capability refs must not contain .."}}

      reserved_app_file?(path) ->
        {:error,
         {:invalid_application, "capability refs must not point at application control files"}}

      true ->
        :ok
    end
  end

  defp reserved_app_file?(path) do
    case Path.split(path) do
      [file] -> file in @reserved_app_files
      _parts -> false
    end
  end

  defp workload_path(path, alias) do
    cond do
      File.regular?(path) ->
        {:ok, path}

      File.dir?(path) ->
        [Path.join(path, "workload.json"), Path.join(path, alias <> ".json")]
        |> Enum.find(&File.regular?/1)
        |> case do
          nil -> {:error, {:invalid_application, "capability ref does not contain workload.json"}}
          workload_path -> {:ok, workload_path}
        end

      true ->
        {:error, {:invalid_application, "capability ref is unavailable"}}
    end
  end

  defp strip_json_comments(raw) do
    raw
    |> String.split("\n")
    |> Enum.reject(&String.starts_with?(String.trim_leading(&1), "//"))
    |> Enum.join("\n")
  end

  defp app_response(app, task, app_run_id, pipeline_result, deliveries) do
    %{
      "app_id" => app.app_id,
      "app_name" => app.name,
      "app_run_id" => app_run_id,
      "task_id" => task["id"],
      "entrypoint" => task["provenance"]["entrypoint"],
      "pipeline_id" => task["pipeline"],
      "status" => pipeline_result["status"],
      "deliverable_summary" => pipeline_result["deliverable_summary"],
      "deliveries" => deliveries,
      "pipeline" => pipeline_result,
      "evidence" =>
        pipeline_result["evidence"]
        |> Map.put("app_run_id", app_run_id)
        |> Map.put("app_id", app.app_id)
    }
  end

  defp inside_root?(path, root) do
    root = Path.expand(root)
    path = Path.expand(path)
    path == root or String.starts_with?(path, root <> "/")
  end

  defp maybe_kw(opts, _key, nil), do: opts
  defp maybe_kw(opts, key, value), do: Keyword.put(opts, key, value)

  defp pipeline_bindings(inputs) when is_map(inputs) do
    Map.new(inputs, fn {key, value} -> {to_string(key), binding_value(value)} end)
  end

  defp binding_value(value) when is_binary(value), do: value
  defp binding_value(nil), do: ""
  defp binding_value(value) when is_number(value) or is_boolean(value), do: to_string(value)
  defp binding_value(value), do: Jason.encode!(value)

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)
end
