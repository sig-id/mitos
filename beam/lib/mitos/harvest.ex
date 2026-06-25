defmodule Mitos.Harvest do
  @moduledoc """
  Local harvest collector for the compute-plane boundary.

  Harvest reads the sandbox/workspace outputs from outside the harness:
  workspace diff, RESULT artifact, and registered artifacts. It may write
  copies/metadata into the run trace directory, but it does not mutate the
  workspace.
  """

  @default_result_path "RESULT"

  @type result :: %{
          diff: map(),
          result: map(),
          artifacts: [map()]
        }

  @spec collect(keyword() | map()) :: {:ok, result()} | {:error, term()}
  def collect(opts) when is_list(opts), do: opts |> Map.new() |> collect()

  def collect(%{} = opts) do
    cwd = value(opts, :cwd)
    trace_dir = value(opts, :trace_dir)
    schema_root = value(opts, :schema_root)
    launch_spec = value(opts, :launch_spec, %{})

    with {:ok, diff} <- collect_diff(cwd, trace_dir),
         {:ok, result} <- collect_result(cwd, launch_spec, diff, schema_root),
         {:ok, artifacts} <- collect_artifacts(trace_dir, cwd) do
      {:ok, %{diff: diff, result: result, artifacts: artifacts}}
    end
  end

  @doc "Compact harvest payload suitable for canonical EffectEvents."
  def effect_payload(%{diff: diff, result: result, artifacts: artifacts}) do
    diff = normalize(diff)
    artifacts = normalize(artifacts)
    result = result |> normalize() |> content_address_result_from_artifacts(artifacts)

    %{
      "diff" => Map.drop(diff, ["patch"]),
      "result" => content_address_result_evidence(result, nil, diff),
      "artifacts" => artifacts
    }
  end

  defp collect_diff(cwd, trace_dir) do
    {patch, files} =
      if git_worktree?(cwd) do
        {git_output(cwd, ["diff", "--binary", "--"]), changed_files(cwd)}
      else
        {"", []}
      end

    public_patch = Mitos.Trace.redact_public_text(patch)
    sha = sha256(public_patch)

    write_trace_artifact(trace_dir, "diff.patch", public_patch)
    write_trace_artifact(trace_dir, "files_changed.json", Jason.encode!(files))

    {:ok,
     %{
       "path" => "diff.patch",
       "sha256" => sha,
       "bytes" => byte_size(public_patch),
       "files_changed" => files,
       "empty?" => public_patch == "",
       "patch" => public_patch
     }}
  end

  defp collect_result(cwd, launch_spec, diff, schema_root) do
    path = result_path(launch_spec)

    result =
      case read_workspace_file(cwd, path) do
        {:ok, bytes} ->
          case Jason.decode(bytes) do
            {:ok, %{} = decoded} ->
              decoded
              |> normalize()
              |> Map.put_new("path", path)
              |> Map.put_new("sha256", sha256(bytes))
              |> Map.put_new("synthetic", false)

            {:ok, _other} ->
              synthetic_result("RESULT is not a JSON object", "result_invalid", diff)

            {:error, error} ->
              synthetic_result(
                "RESULT is invalid JSON: #{inspect(error)}",
                "result_invalid",
                diff
              )
          end

        {:error, :missing} ->
          synthetic_result("RESULT artifact missing", "result_missing", diff)

        {:error, reason} ->
          synthetic_result(
            "RESULT artifact unavailable: #{inspect(reason)}",
            "result_unavailable",
            diff
          )
      end

    result =
      result
      |> validate_result_schema(cwd, launch_spec, diff, schema_root)
      |> attach_declared_evidence(cwd, launch_spec)
      |> content_address_result_evidence(cwd, diff)

    {:ok, result}
  end

  @doc false
  def validate_result_schema(result, cwd, launch_spec, diff),
    do: validate_result_schema(result, cwd, launch_spec, diff, nil)

  @doc false
  def validate_result_schema(
        %{"synthetic" => true} = result,
        _cwd,
        _launch_spec,
        _diff,
        _schema_root
      ),
      do: result

  def validate_result_schema(result, cwd, launch_spec, diff, schema_root)
      when is_map(result) and is_map(launch_spec) do
    launch_spec
    |> result_schema()
    |> load_result_schema(schema_roots(cwd, launch_spec, schema_root))
    |> case do
      :none ->
        result

      {:ok, schema} ->
        case validate_json_schema(result, schema) do
          :ok ->
            result

          {:error, reason} ->
            synthetic_result(
              "RESULT does not match result.schema: #{reason}",
              "result_schema_invalid",
              diff
            )
            |> Map.put("schema_error", reason)
        end

      {:error, reason} ->
        synthetic_result(
          "RESULT schema unavailable: #{inspect(reason)}",
          "result_schema_unavailable",
          diff
        )
        |> Map.put("schema_error", inspect(reason))
    end
  end

  def validate_result_schema(result, _cwd, _launch_spec, _diff, _schema_root), do: result

  defp collect_artifacts(nil, _cwd), do: {:ok, []}

  defp collect_artifacts(trace_dir, cwd) do
    artifacts =
      trace_dir
      |> Path.join("events.jsonl")
      |> read_jsonl()
      |> Enum.flat_map(&artifact_from_event/1)
      |> Enum.map(&content_address_artifact(&1, cwd))
      |> Enum.uniq_by(fn artifact ->
        {artifact["kind"], artifact["path"], artifact["request_id"], artifact["artifact"]}
      end)

    {:ok, artifacts}
  end

  defp synthetic_result(summary, need, diff) do
    %{
      "signal" => "unknown",
      "confidence" => 0.0,
      "summary" => summary,
      "metrics" => %{},
      "evidence" => [
        %{
          "kind" => "diff",
          "path" => diff["path"],
          "sha256" => diff["sha256"]
        }
      ],
      "needs" => [need],
      "synthetic" => true
    }
  end

  defp artifact_from_event(%{"kind" => "effect", "type" => "artifact_registered", "ref" => ref})
       when is_map(ref) do
    [Map.delete(ref, "request_id")]
  end

  defp artifact_from_event(%{"event" => %{"type" => "ARTIFACT_REGISTERED"} = event}) do
    [Map.delete(event, "type")]
  end

  defp artifact_from_event(%{"type" => "ARTIFACT_REGISTERED"} = event) do
    [Map.delete(event, "type")]
  end

  defp artifact_from_event(_event), do: []

  defp content_address_artifact(artifact, cwd) do
    artifact = normalize(artifact)

    case {cwd, artifact["path"]} do
      {cwd, path} when is_binary(cwd) and is_binary(path) ->
        with {:ok, bytes} <- read_workspace_file(cwd, path) do
          artifact
          |> Map.put_new("sha256", sha256(bytes))
          |> Map.put_new("bytes", byte_size(bytes))
        else
          _ -> artifact
        end

      _ ->
        artifact
    end
  end

  defp result_path(launch_spec) do
    launch_spec
    |> value(:result, %{})
    |> case do
      result when is_map(result) ->
        value(result, :path, @default_result_path)

      _ ->
        @default_result_path
    end
  end

  defp result_schema(launch_spec) do
    launch_spec
    |> value(:result, %{})
    |> case do
      %{} = result -> value(result, :schema)
      _ -> nil
    end
  end

  defp schema_roots(cwd, launch_spec, schema_root) do
    result = value(launch_spec, :result, %{})

    [
      schema_root,
      value(result, :schema_root),
      value(result, :schema_base),
      value(launch_spec, :schema_root),
      value(launch_spec, :package_root),
      value(launch_spec, :capability_root),
      value(launch_spec, :manifest_dir),
      cwd
    ]
    |> Enum.filter(&(is_binary(&1) and &1 != ""))
    |> Enum.map(&Path.expand/1)
    |> Enum.uniq()
  end

  defp load_result_schema(nil, _roots), do: :none
  defp load_result_schema("", _roots), do: :none
  defp load_result_schema(%{} = schema, _roots), do: {:ok, normalize(schema)}

  defp load_result_schema(path, roots) when is_binary(path) do
    Enum.find_value(roots, {:error, :schema_unavailable}, fn root ->
      with {:ok, bytes} <- read_workspace_file(root, path),
           {:ok, %{} = schema} <- Jason.decode(bytes) do
        {:ok, normalize(schema)}
      else
        {:ok, _other} -> {:error, :schema_not_object}
        {:error, %Jason.DecodeError{} = error} -> {:error, error}
        _ -> nil
      end
    end)
  end

  defp load_result_schema(_schema, _roots), do: {:error, :invalid_schema}

  defp validate_json_schema(value, %{} = schema), do: validate_json_schema(value, schema, [])

  defp validate_json_schema(value, schema, path) do
    with :ok <- validate_enum(value, schema, path),
         :ok <- validate_type(value, schema, path),
         :ok <- validate_required(value, schema, path),
         :ok <- validate_properties(value, schema, path),
         :ok <- validate_items(value, schema, path),
         :ok <- validate_numeric_bounds(value, schema, path) do
      :ok
    end
  end

  defp validate_enum(value, schema, path) do
    case value(schema, "enum") do
      values when is_list(values) ->
        if value in values do
          :ok
        else
          {:error,
           "#{format_schema_path(path)} must be one of #{Enum.map_join(values, ", ", &inspect/1)}"}
        end

      _ ->
        :ok
    end
  end

  defp validate_type(value, schema, path) do
    case value(schema, "type") do
      nil ->
        :ok

      types when is_list(types) ->
        if Enum.any?(types, &type_matches?(value, &1)) do
          :ok
        else
          {:error, "#{format_schema_path(path)} must match type #{Enum.join(types, " or ")}"}
        end

      type when is_binary(type) ->
        if type_matches?(value, type) do
          :ok
        else
          {:error, "#{format_schema_path(path)} must be #{type}"}
        end

      _ ->
        {:error, "#{format_schema_path(path)} has invalid schema type"}
    end
  end

  defp validate_required(%{} = value, schema, path) do
    case value(schema, "required") do
      required when is_list(required) ->
        required
        |> Enum.filter(&is_binary/1)
        |> Enum.find_value(:ok, fn key ->
          if Map.has_key?(value, key) do
            nil
          else
            {:error, "#{format_schema_path(path ++ [key])} is required"}
          end
        end)

      _ ->
        :ok
    end
  end

  defp validate_required(_value, _schema, _path), do: :ok

  defp validate_properties(%{} = value, schema, path) do
    case value(schema, "properties") do
      %{} = properties ->
        Enum.reduce_while(properties, :ok, fn {key, property_schema}, :ok ->
          if Map.has_key?(value, key) and is_map(property_schema) do
            case validate_json_schema(Map.fetch!(value, key), property_schema, path ++ [key]) do
              :ok -> {:cont, :ok}
              {:error, reason} -> {:halt, {:error, reason}}
            end
          else
            {:cont, :ok}
          end
        end)

      _ ->
        :ok
    end
  end

  defp validate_properties(_value, _schema, _path), do: :ok

  defp validate_items(value, schema, path) when is_list(value) do
    case value(schema, "items") do
      %{} = item_schema ->
        value
        |> Enum.with_index()
        |> Enum.reduce_while(:ok, fn {item, index}, :ok ->
          case validate_json_schema(item, item_schema, path ++ [index]) do
            :ok -> {:cont, :ok}
            {:error, reason} -> {:halt, {:error, reason}}
          end
        end)

      _ ->
        :ok
    end
  end

  defp validate_items(_value, _schema, _path), do: :ok

  defp validate_numeric_bounds(value, schema, path) when is_number(value) do
    cond do
      is_number(value(schema, "minimum")) and value < value(schema, "minimum") ->
        {:error, "#{format_schema_path(path)} must be >= #{value(schema, "minimum")}"}

      is_number(value(schema, "maximum")) and value > value(schema, "maximum") ->
        {:error, "#{format_schema_path(path)} must be <= #{value(schema, "maximum")}"}

      true ->
        :ok
    end
  end

  defp validate_numeric_bounds(_value, _schema, _path), do: :ok

  defp type_matches?(value, "object"), do: is_map(value)
  defp type_matches?(value, "array"), do: is_list(value)
  defp type_matches?(value, "string"), do: is_binary(value)
  defp type_matches?(value, "number"), do: is_number(value)
  defp type_matches?(value, "integer"), do: is_integer(value)
  defp type_matches?(value, "boolean"), do: is_boolean(value)
  defp type_matches?(nil, "null"), do: true
  defp type_matches?(_value, _type), do: false

  defp format_schema_path([]), do: "RESULT"

  defp format_schema_path(path) do
    "RESULT." <>
      Enum.map_join(path, ".", fn
        index when is_integer(index) -> Integer.to_string(index)
        key -> to_string(key)
      end)
  end

  defp attach_declared_evidence(result, cwd, launch_spec) do
    declared = declared_evidence_paths(launch_spec)

    if declared == [] do
      result
    else
      {evidence, missing} =
        Enum.map_reduce(declared, [], fn path, missing ->
          case evidence_artifact(cwd, path) do
            {:ok, artifact} ->
              {artifact, missing}

            {:error, reason} ->
              {%{"kind" => "file", "path" => path, "missing" => true}, [{path, reason} | missing]}
          end
        end)

      result
      |> Map.put("evidence", merge_evidence(result["evidence"], evidence))
      |> maybe_mark_missing_evidence(Enum.reverse(missing))
    end
  end

  defp declared_evidence_paths(launch_spec) do
    launch_spec
    |> value(:result, %{})
    |> case do
      %{} = result -> value(result, :evidence, [])
      _ -> []
    end
    |> case do
      paths when is_list(paths) ->
        paths
        |> Enum.filter(&is_binary/1)
        |> Enum.reject(&(&1 == ""))
        |> Enum.uniq()

      _ ->
        []
    end
  end

  defp evidence_artifact(cwd, path) when is_binary(cwd) and is_binary(path) do
    with {:ok, bytes} <- read_workspace_file(cwd, path) do
      {:ok,
       %{
         "kind" => "file",
         "path" => path,
         "sha256" => sha256(bytes),
         "bytes" => byte_size(bytes)
       }}
    else
      {:error, reason} -> {:error, reason}
      _ -> {:error, :missing}
    end
  end

  defp evidence_artifact(_cwd, _path), do: {:error, :missing}

  defp merge_evidence(existing, declared) do
    existing
    |> evidence_list()
    |> Kernel.++(declared)
    |> Enum.uniq_by(fn
      %{"path" => path, "kind" => kind} -> {kind, path}
      %{"artifact" => artifact, "kind" => kind} -> {kind, artifact}
      value -> value
    end)
  end

  defp evidence_list(list) when is_list(list), do: Enum.filter(list, &is_map/1)
  defp evidence_list(_other), do: []

  defp content_address_result_evidence(%{"synthetic" => true} = result, _cwd, _diff), do: result

  defp content_address_result_evidence(%{} = result, cwd, diff) do
    evidence = evidence_list(result["evidence"])

    if evidence == [] do
      result
    else
      {evidence, missing} =
        evidence
        |> Enum.with_index()
        |> Enum.map_reduce([], fn {artifact, index}, missing ->
          content_address_evidence_entry(artifact, cwd, index, missing)
        end)

      result
      |> Map.put("evidence", evidence)
      |> maybe_mark_missing_evidence(Enum.reverse(missing))
      |> maybe_synthetic_unknown(diff, missing)
    end
  end

  defp content_address_result_evidence(result, _cwd, _diff), do: result

  defp content_address_result_from_artifacts(%{"synthetic" => true} = result, _artifacts),
    do: result

  defp content_address_result_from_artifacts(%{} = result, artifacts) do
    cond do
      is_binary(result["sha256"]) and result["sha256"] != "" ->
        result

      is_binary(result_path = result["path"] || result["artifact"]) ->
        case artifact_sha256(artifacts, result_path) do
          sha256 when is_binary(sha256) -> Map.put(result, "sha256", sha256)
          _missing -> result
        end

      true ->
        result
    end
  end

  defp content_address_result_from_artifacts(result, _artifacts), do: result

  defp artifact_sha256(artifacts, result_path) do
    artifacts
    |> List.wrap()
    |> Enum.find_value(fn
      %{} = artifact ->
        path = artifact["path"] || artifact["artifact"]
        sha256 = artifact["sha256"]

        if path == result_path and is_binary(sha256) and sha256 != "" do
          sha256
        end

      _artifact ->
        nil
    end)
  end

  defp content_address_evidence_entry(%{} = artifact, cwd, index, missing) do
    artifact = normalize(artifact)
    path = artifact["path"]

    cond do
      is_binary(path) and path != "" and is_binary(cwd) ->
        case evidence_artifact(cwd, path) do
          {:ok, addressed} ->
            artifact =
              artifact
              |> Map.put_new("kind", addressed["kind"])
              |> Map.merge(Map.take(addressed, ["sha256", "bytes"]))

            {artifact, missing}

          {:error, reason} ->
            missing_artifact = artifact |> Map.put("missing", true) |> Map.put_new("path", path)
            {missing_artifact, [{evidence_ref(artifact, index), reason} | missing]}
        end

      valid_sha256?(artifact["sha256"]) ->
        {artifact, missing}

      true ->
        {artifact, [{evidence_ref(artifact, index), :missing_sha256} | missing]}
    end
  end

  defp valid_sha256?("sha256:" <> digest), do: digest != ""
  defp valid_sha256?(_value), do: false

  defp evidence_ref(%{"path" => path}, _index) when is_binary(path) and path != "", do: path

  defp evidence_ref(%{"artifact" => artifact}, _index)
       when is_binary(artifact) and artifact != "", do: artifact

  defp evidence_ref(_artifact, index), do: "evidence[#{index}]"

  defp maybe_mark_missing_evidence(result, []), do: result

  defp maybe_mark_missing_evidence(result, missing) do
    missing = Enum.map(missing, &missing_ref/1)

    result
    |> Map.put("synthetic", true)
    |> Map.update("needs", ["evidence_missing"], fn needs ->
      needs
      |> List.wrap()
      |> Kernel.++(["evidence_missing"])
      |> Enum.uniq()
    end)
    |> Map.update("evidence_missing", missing, fn existing ->
      existing
      |> List.wrap()
      |> Kernel.++(missing)
      |> Enum.uniq()
    end)
  end

  defp maybe_synthetic_unknown(result, _diff, []), do: result

  defp maybe_synthetic_unknown(result, diff, _missing) do
    result
    |> Map.put("signal", "unknown")
    |> Map.put_new("confidence", 0.0)
    |> Map.put_new("summary", "RESULT evidence is not content-addressed")
    |> Map.update("evidence", [], fn evidence ->
      merge_evidence(evidence, [
        %{"kind" => "diff", "path" => diff["path"], "sha256" => diff["sha256"]}
      ])
    end)
  end

  defp missing_ref({ref, _reason}), do: ref
  defp missing_ref(ref), do: ref

  defp git_worktree?(cwd) when is_binary(cwd) do
    trusted_workspace_dir?(cwd) and
      match?(
        {"true\n", 0},
        System.cmd("git", ["-C", cwd, "rev-parse", "--is-inside-work-tree"],
          stderr_to_stdout: true
        )
      )
  end

  defp git_worktree?(_cwd), do: false

  defp trusted_workspace_dir?(path) do
    path = Path.expand(path)

    with {:ok, %{type: :directory}} <- File.lstat(path),
         true <- no_symlink_absolute_path?(path) do
      true
    else
      _ -> false
    end
  end

  defp git_output(cwd, args) do
    case System.cmd("git", ["-C", cwd | args], stderr_to_stdout: true) do
      {output, 0} -> output
      {_output, _status} -> ""
    end
  end

  defp changed_files(cwd) do
    tracked =
      cwd
      |> git_output(["diff", "--name-only", "--"])
      |> lines()

    untracked =
      cwd
      |> git_output(["ls-files", "--others", "--exclude-standard"])
      |> lines()

    (tracked ++ untracked)
    |> Enum.uniq()
    |> Enum.sort()
  end

  defp read_workspace_file(cwd, relative_path) when is_binary(cwd) and is_binary(relative_path) do
    with {:ok, path} <- workspace_path(cwd, relative_path),
         true <- no_symlink_path?(path, cwd),
         {:ok, bytes} <- read_regular_file(path) do
      {:ok, bytes}
    else
      false -> {:error, :symlink}
      {:error, reason} -> {:error, reason}
      _ -> {:error, :missing}
    end
  end

  defp read_workspace_file(_cwd, _relative_path), do: {:error, :missing}

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, %{type: :symlink}} -> {:error, :symlink}
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
    end
  end

  defp workspace_path(cwd, relative_path) do
    path = Path.expand(relative_path, cwd)
    root = Path.expand(cwd)

    if path == root or String.starts_with?(path, root <> "/") do
      {:ok, path}
    else
      {:error, :escape}
    end
  end

  defp no_symlink_path?(path, root) do
    root = Path.expand(root)
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
          {:error, reason} -> {:halt, {:error, reason}}
        end
      end)
      |> case do
        false -> false
        {:error, reason} -> {:error, reason}
        _last_path -> true
      end
    else
      false -> false
      {:error, reason} -> {:error, reason}
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

  defp read_jsonl(path) do
    case read_regular_file(path) do
      {:ok, raw} ->
        raw
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          case Jason.decode(line) do
            {:ok, %{} = event} -> [event]
            _ -> []
          end
        end)

      {:error, _} ->
        []
    end
  end

  defp write_trace_artifact(nil, _path, _bytes), do: :ok

  defp write_trace_artifact(trace_dir, path, bytes) do
    root = Path.expand(trace_dir)
    target = Path.expand(path, root)

    unless target == root or String.starts_with?(target, root <> "/") do
      raise ArgumentError, "trace artifact path escapes trace directory"
    end

    case {File.lstat(root), no_symlink_absolute_path?(root)} do
      {{:ok, %{type: :directory}}, true} ->
        write_regular_trace_file!(target, bytes)

      {{:ok, %{type: :symlink}}, _} ->
        raise ArgumentError, "trace directory must not be a symlink"

      {{:ok, %{type: :directory}}, false} ->
        raise ArgumentError, "trace directory must not contain symlinks"

      {{:ok, stat}, _} ->
        raise ArgumentError, "trace directory must be a directory: #{inspect(stat.type)}"

      {{:error, reason}, _} ->
        raise File.Error, reason: reason, action: "inspect trace directory", path: root
    end
  end

  defp write_regular_trace_file!(path, bytes) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} ->
        File.rm!(path)
        write_new_trace_file!(path, bytes)

      {:error, :enoent} ->
        write_new_trace_file!(path, bytes)

      {:ok, %{type: :symlink}} ->
        raise ArgumentError, "trace artifact must not be a symlink"

      {:ok, stat} ->
        raise ArgumentError, "trace artifact must be regular: #{inspect(stat.type)}"

      {:error, reason} ->
        raise File.Error, reason: reason, action: "inspect trace artifact", path: path
    end
  end

  defp write_new_trace_file!(path, bytes) do
    case File.open(path, [:write, :exclusive], fn io -> IO.binwrite(io, bytes) end) do
      {:ok, :ok} ->
        :ok

      {:ok, {:error, reason}} ->
        raise File.Error, reason: reason, action: "write trace artifact", path: path

      {:error, reason} ->
        raise File.Error, reason: reason, action: "create trace artifact", path: path
    end
  end

  defp sha256(bytes) when is_binary(bytes) do
    digest =
      :crypto.hash(:sha256, bytes)
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end

  defp lines(output) do
    output
    |> String.split("\n", trim: true)
    |> Enum.reject(&(&1 == ""))
  end

  defp value(map, key, default \\ nil)

  defp value(%{} = map, key, default),
    do: Map.get(map, key, Map.get(map, to_string(key), default))

  defp value(_other, _key, default), do: default

  defp normalize(%{} = map),
    do: Map.new(map, fn {key, val} -> {to_string(key), normalize(val)} end)

  defp normalize(list) when is_list(list), do: Enum.map(list, &normalize/1)
  defp normalize(value), do: value
end
