defmodule Mitos.CapabilityRuntime do
  @moduledoc """
  Local materialization for the capability pillars before a harness starts.

  The BEAM control plane owns the capability manifest, trace directory, and
  hooks. For local T0/T1 execution this module prepares a per-run runtime root
  containing the resolved SKILL, copied TOOL executables/manifests, and copied
  DATA refs. Remote/T2 providers receive the manifest in the LaunchSpec instead.
  """

  @runtime_dir "runtime"

  @doc """
  Prepare local runtime assets and return updated agent options.

  No-op for workloads without a capability and for T2 capabilities, where local
  materialization would violate the compute-plane trust posture.
  """
  def prepare_agent_options(%Mitos.Workload{capability: nil}, agent_opts, _opts)
      when is_list(agent_opts),
      do: {:ok, agent_opts}

  def prepare_agent_options(
        %Mitos.Workload{capability: %Mitos.Capability{trust_tier: "T2"}},
        agent_opts,
        _opts
      )
      when is_list(agent_opts),
      do: {:ok, agent_opts}

  def prepare_agent_options(%Mitos.Workload{} = workload, agent_opts, opts)
      when is_list(agent_opts) and is_list(opts) do
    session_id = Keyword.fetch!(agent_opts, :session_id)
    runtime_root = runtime_root(session_id, agent_opts, opts)
    workspace = workspace_path(workload, runtime_root, agent_opts)
    skill_dir = Path.join(runtime_root, "skill")
    tools_dir = Path.join(runtime_root, "tools")
    data_dir = Path.join(runtime_root, "data")

    with :ok <- mkdir_runtime_dirs(runtime_root, workspace, [skill_dir, tools_dir, data_dir]),
         {:ok, skill} <- materialize_skill(workload, agent_opts, skill_dir),
         {:ok, tools} <- materialize_tools(workload, tools_dir),
         {:ok, data} <- materialize_data(workload, data_dir),
         :ok <- lock_runtime_pillars([skill_dir, tools_dir, data_dir]) do
      runtime = %{
        "root" => runtime_root,
        "workspace" => workspace,
        "skill_dir" => skill_dir,
        "tools_dir" => tools_dir,
        "data_dir" => data_dir,
        "skill" => skill,
        "tools" => tools,
        "data" => data
      }

      env =
        agent_opts
        |> Keyword.get(:env)
        |> runtime_env(runtime)

      agent_opts =
        agent_opts
        |> Keyword.put(:cwd, workspace)
        |> Keyword.put(:env, env)
        |> Keyword.put(:capability_runtime, runtime)

      {:ok, agent_opts}
    end
  end

  defp runtime_root(session_id, agent_opts, opts) do
    Keyword.get(opts, :runtime_root) ||
      Path.join([
        Keyword.get(agent_opts, :trace_base_dir) || Mitos.Trace.base_dir(),
        ".runs",
        session_id,
        @runtime_dir
      ])
  end

  defp workspace_path(%Mitos.Workload{cwd: cwd}, _runtime_root, _agent_opts)
       when is_binary(cwd) and cwd != "",
       do: Path.expand(cwd)

  defp workspace_path(_workload, runtime_root, _agent_opts),
    do: Path.join(runtime_root, "workspace")

  defp mkdir_runtime_dirs(runtime_root, workspace, pillar_dirs) do
    with :ok <- mkdir_regular_p(runtime_root),
         :ok <- mkdir_workspace(workspace, runtime_root) do
      pillar_dirs
      |> Enum.reduce_while(:ok, fn path, :ok ->
        case mkdir_regular_p(path) do
          :ok -> {:cont, :ok}
          {:error, reason} -> {:halt, {:error, reason}}
        end
      end)
    end
  end

  defp mkdir_workspace(workspace, _runtime_root), do: mkdir_regular_p(workspace)

  defp mkdir_regular_p(path) do
    path = Path.expand(path)
    parent = Path.dirname(path)

    cond do
      path == parent ->
        ensure_existing_regular_dir(path)

      true ->
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

  defp inside_root?(path, root) do
    path = Path.expand(path)
    root = Path.expand(root)
    path == root or String.starts_with?(path, root <> "/")
  end

  defp materialize_skill(_workload, agent_opts, skill_dir) do
    case Keyword.get(agent_opts, :resolved_skill) do
      %{} = resolved ->
        body = map_value(resolved, "body")
        target = Path.join(skill_dir, "resolved.md")

        if is_binary(body) do
          write_runtime_file!(target, body)
        end

        {:ok,
         %{
           "path" => target,
           "source" => map_value(resolved, "source"),
           "sha256" => map_value(resolved, "sha256")
         }
         |> reject_blank_values()}

      _none ->
        {:ok, %{}}
    end
  rescue
    exception -> {:error, {:skill_materialize_failed, Exception.message(exception)}}
  end

  defp materialize_tools(
         %Mitos.Workload{capability: %Mitos.Capability{tools: tools}} = workload,
         tools_dir
       )
       when is_list(tools) do
    tools =
      tools
      |> Enum.map(&stringify/1)
      |> Enum.map(fn tool ->
        case materialize_tool_exec(workload, tool, tools_dir) do
          {:ok, exec_path} ->
            tool
            |> Map.put("runtime_exec", exec_path)
            |> reject_blank_values()

          :no_exec ->
            tool
        end
      end)

    manifest = Path.join(tools_dir, "tools.json")
    write_runtime_file!(manifest, Jason.encode!(%{"items" => tools}))

    {:ok, %{"manifest" => manifest, "items" => tools}}
  rescue
    exception -> {:error, {:tools_materialize_failed, Exception.message(exception)}}
  catch
    {:tool_error, reason} -> {:error, reason}
  end

  defp materialize_tool_exec(workload, %{"exec" => exec} = tool, tools_dir)
       when is_binary(exec) and exec != "" do
    case package_file(workload.package_root, exec) do
      {:ok, source} ->
        case File.lstat(source) do
          {:ok, %{type: :regular}} ->
            target = Path.join(tools_dir, Path.basename(exec))

            case copy_runtime_file(source, target) do
              :ok ->
                chmod_like_source(source, target)
                {:ok, target}

              {:error, reason} ->
                throw({:tool_error, {:tool_exec_unavailable, tool["name"], exec, reason}})
            end

          {:ok, %{type: :symlink}} ->
            throw({:tool_error, {:tool_exec_unavailable, tool["name"], exec, :symlink}})

          {:ok, _stat} ->
            throw({:tool_error, {:tool_exec_missing, tool["name"], exec}})

          {:error, reason} ->
            throw({:tool_error, {:tool_exec_unavailable, tool["name"], exec, reason}})
        end

      {:error, reason} ->
        throw({:tool_error, {:tool_exec_unavailable, tool["name"], exec, reason}})
    end
  end

  defp materialize_tool_exec(_workload, _tool, _tools_dir), do: :no_exec

  defp materialize_data(
         %Mitos.Workload{capability: %Mitos.Capability{data: data}} = workload,
         data_dir
       )
       when is_map(data) do
    data = stringify(data)

    case data_source(data) do
      nil ->
        ref_path = Path.join(data_dir, "data_ref.json")
        write_runtime_file!(ref_path, Jason.encode!(data))
        {:ok, Map.merge(data, %{"path" => data_dir, "ref" => ref_path})}

      source_ref ->
        with {:ok, source} <- data_source_path(workload, source_ref),
             :ok <- copy_data_source(source_ref, source, data_dir) do
          {:ok, Map.merge(data, %{"path" => data_dir, "source" => source_ref})}
        end
    end
  rescue
    exception -> {:error, {:data_materialize_failed, Exception.message(exception)}}
  end

  defp materialize_data(_workload, data_dir), do: {:ok, %{"path" => data_dir}}

  defp data_source(data) do
    Enum.find_value(["path", "bind"], fn key ->
      value = data[key]
      if is_binary(value) and value != "", do: value
    end)
  end

  defp data_source_path(workload, source_ref) do
    case package_file(workload.package_root, source_ref) do
      {:ok, source} -> {:ok, source}
      {:error, reason} -> {:error, {:data_unavailable, source_ref, reason}}
    end
  end

  defp copy_data_source(source_ref, source, target_dir) do
    case File.lstat(source) do
      {:ok, %{type: :directory}} ->
        copy_data_directory(source_ref, source, target_dir, "")

      {:ok, %{type: :regular}} ->
        case copy_runtime_file(source, Path.join(target_dir, Path.basename(source))) do
          :ok -> :ok
          {:error, reason} -> {:error, {:data_copy_failed, Path.basename(source), reason}}
        end

      {:ok, %{type: :symlink}} ->
        {:error, {:data_unavailable, source_ref, :symlink}}

      {:ok, _stat} ->
        {:error, {:data_unavailable, source_ref, :missing}}

      {:error, :enoent} ->
        {:error, {:data_unavailable, source_ref, :missing}}

      {:error, reason} ->
        {:error, {:data_unavailable, source_ref, reason}}
    end
  end

  defp copy_data_directory(source_ref, source, target_dir, relative) do
    case File.ls(source) do
      {:ok, entries} ->
        Enum.reduce_while(entries, :ok, fn entry, :ok ->
          from = Path.join(source, entry)
          to = Path.join(target_dir, entry)
          child_relative = relative_path(relative, entry)

          case copy_data_entry(source_ref, from, to, child_relative) do
            :ok -> {:cont, :ok}
            {:error, reason} -> {:halt, {:error, reason}}
          end
        end)

      {:error, reason} ->
        {:error, {:data_unavailable, source_ref, reason}}
    end
  end

  defp copy_data_entry(source_ref, from, to, relative) do
    case File.lstat(from) do
      {:ok, %{type: :directory}} ->
        with :ok <- mkdir_regular_p(to) do
          copy_data_directory(source_ref, from, to, relative)
        else
          {:error, {:mkdir_failed, _path, reason}} ->
            {:error, {:data_copy_failed, relative, reason}}

          {:error, reason} ->
            {:error, {:data_copy_failed, relative, reason}}
        end

      {:ok, %{type: :regular}} ->
        case copy_runtime_file(from, to) do
          :ok -> :ok
          {:error, reason} -> {:error, {:data_copy_failed, relative, reason}}
        end

      {:ok, %{type: :symlink}} ->
        {:error, {:data_copy_failed, relative, :symlink}}

      {:ok, _stat} ->
        {:error, {:data_copy_failed, relative, :unsupported_file_type}}

      {:error, reason} ->
        {:error, {:data_copy_failed, relative, reason}}
    end
  end

  defp relative_path("", entry), do: entry
  defp relative_path(parent, entry), do: Path.join(parent, entry)

  defp package_file(package_root, path) when is_binary(package_root) and is_binary(path) do
    root = Path.expand(package_root)

    expanded =
      if Path.type(path) == :absolute do
        Path.expand(path)
      else
        Path.expand(path, root)
      end

    cond do
      not inside_root?(expanded, root) ->
        {:error, :path_escape}

      not source_parent_without_symlinks?(expanded, root) ->
        {:error, :path_symlink}

      true ->
        {:ok, expanded}
    end
  end

  defp package_file(_package_root, _path), do: {:error, :missing_package_root}

  defp source_parent_without_symlinks?(path, root) do
    path =
      if Path.expand(path) == Path.expand(root) do
        path
      else
        Path.dirname(path)
      end

    no_symlink_path?(path, root)
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
          {:error, :enoent} -> {:halt, true}
          {:error, _reason} -> {:halt, false}
        end
      end)
      |> case do
        false -> false
        true -> true
        _last_path -> true
      end
    else
      {:error, :enoent} -> true
      _ -> false
    end
  end

  defp copy_runtime_file(source, target) do
    with {:ok, bytes} <- read_regular_file(source),
         :ok <- write_runtime_file(target, bytes) do
      :ok
    end
  end

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, %{type: :symlink}} -> {:error, :symlink}
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
    end
  end

  defp write_runtime_file!(path, bytes) do
    case write_runtime_file(path, bytes) do
      :ok ->
        :ok

      {:error, reason} ->
        raise ArgumentError, "runtime file write failed at #{path}: #{inspect(reason)}"
    end
  end

  defp write_runtime_file(path, bytes) when is_binary(bytes) do
    parent = Path.dirname(path)

    with {:ok, %{type: :directory}} <- File.lstat(parent),
         :ok <- ensure_runtime_file_replaceable(path) do
      write_new_runtime_file(path, bytes)
    else
      {:ok, %{type: :symlink}} -> {:error, :parent_symlink}
      {:ok, _stat} -> {:error, :parent_not_directory}
      {:error, reason} -> {:error, reason}
    end
  end

  defp ensure_runtime_file_replaceable(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} ->
        File.rm(path)

      {:ok, %{type: :symlink}} ->
        {:error, :symlink}

      {:ok, _stat} ->
        {:error, :not_regular}

      {:error, :enoent} ->
        :ok

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp write_new_runtime_file(path, bytes) do
    case File.open(path, [:write, :exclusive], fn io -> IO.binwrite(io, bytes) end) do
      {:ok, :ok} -> :ok
      {:error, reason} -> {:error, reason}
    end
  end

  defp runtime_env(base_env, runtime) do
    base_env
    |> env_map()
    |> Map.merge(%{
      "MITOS_CAPABILITY_ROOT" => runtime["root"],
      "MITOS_WORKSPACE_DIR" => runtime["workspace"],
      "MITOS_SKILL_DIR" => runtime["skill_dir"],
      "MITOS_RESOLVED_SKILL" => get_in(runtime, ["skill", "path"]),
      "MITOS_SKILL_SHA256" => get_in(runtime, ["skill", "sha256"]),
      "MITOS_TOOLS_DIR" => runtime["tools_dir"],
      "MITOS_TOOLS_MANIFEST" => get_in(runtime, ["tools", "manifest"]),
      "MITOS_DATA_DIR" => runtime["data_dir"]
    })
    |> reject_blank_values()
    |> prepend_path(runtime["tools_dir"])
  end

  defp env_map(nil), do: %{}

  defp env_map(%{} = env) do
    Map.new(env, fn {key, value} -> {to_string(key), to_string(value)} end)
  end

  defp env_map(env) when is_list(env) do
    Enum.flat_map(env, fn
      {key, value} -> [{to_string(key), to_string(value)}]
      %{"name" => key, "value" => value} -> [{to_string(key), to_string(value)}]
      %{name: key, value: value} -> [{to_string(key), to_string(value)}]
      _ -> []
    end)
    |> Map.new()
  end

  defp env_map(_env), do: %{}

  defp prepend_path(env, tools_dir) do
    existing = env["PATH"] || System.get_env("PATH") || ""
    Map.put(env, "PATH", Enum.join(Enum.reject([tools_dir, existing], &(&1 in [nil, ""])), ":"))
  end

  defp chmod_like_source(source, target) do
    case File.lstat(source) do
      {:ok, %{mode: mode}} -> File.chmod(target, mode)
      {:error, _reason} -> :ok
    end
  end

  defp lock_runtime_pillars(paths) do
    paths
    |> Enum.reduce_while(:ok, fn path, :ok ->
      case read_only_tree(path) do
        :ok -> {:cont, :ok}
        {:error, reason} -> {:halt, {:error, {:runtime_lock_failed, path, reason}}}
      end
    end)
  end

  defp read_only_tree(path) do
    case File.lstat(path) do
      {:ok, %{type: :directory}} ->
        with {:ok, entries} <- File.ls(path),
             :ok <-
               Enum.reduce_while(entries, :ok, fn entry, :ok ->
                 case read_only_tree(Path.join(path, entry)) do
                   :ok -> {:cont, :ok}
                   {:error, reason} -> {:halt, {:error, reason}}
                 end
               end) do
          File.chmod(path, 0o555)
        end

      {:ok, %{type: :regular}} ->
        File.chmod(path, read_only_file_mode(path))

      {:ok, %{type: :symlink}} ->
        {:error, {:symlink, path}}

      {:ok, _stat} ->
        :ok

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp read_only_file_mode(path) do
    case File.lstat(path) do
      {:ok, %{mode: mode}} when Bitwise.band(mode, 0o111) != 0 -> 0o555
      _other -> 0o444
    end
  end

  defp map_value(%{} = map, key), do: Map.get(map, key, Map.get(map, String.to_atom(key)))
  defp map_value(_map, _key), do: nil

  defp stringify(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify(value)} end)

  defp stringify(list) when is_list(list), do: Enum.map(list, &stringify/1)
  defp stringify(value), do: value

  defp reject_blank_values(map) do
    map
    |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
    |> Map.new()
  end
end
