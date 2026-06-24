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

    with :ok <- mkdirs([runtime_root, workspace, skill_dir, tools_dir, data_dir]),
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

  defp mkdirs(paths) do
    Enum.reduce_while(paths, :ok, fn path, :ok ->
      case File.mkdir_p(path) do
        :ok -> {:cont, :ok}
        {:error, reason} -> {:halt, {:error, {:mkdir_failed, path, reason}}}
      end
    end)
  end

  defp materialize_skill(_workload, agent_opts, skill_dir) do
    case Keyword.get(agent_opts, :resolved_skill) do
      %{} = resolved ->
        body = map_value(resolved, "body")
        target = Path.join(skill_dir, "resolved.md")

        if is_binary(body) do
          File.write!(target, body)
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
    File.write!(manifest, Jason.encode!(%{"items" => tools}))

    {:ok, %{"manifest" => manifest, "items" => tools}}
  rescue
    exception -> {:error, {:tools_materialize_failed, Exception.message(exception)}}
  catch
    {:tool_error, reason} -> {:error, reason}
  end

  defp materialize_tool_exec(workload, %{"exec" => exec} = tool, tools_dir)
       when is_binary(exec) and exec != "" do
    with {:ok, source} <- package_file(workload.package_root, exec),
         true <- File.regular?(source) do
      target = Path.join(tools_dir, Path.basename(exec))
      File.cp!(source, target)
      chmod_like_source(source, target)
      {:ok, target}
    else
      false ->
        throw({:tool_error, {:tool_exec_missing, tool["name"], exec}})

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
        File.write!(ref_path, Jason.encode!(data))
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
    cond do
      File.dir?(source) ->
        case File.ls(source) do
          {:ok, entries} ->
            Enum.reduce_while(entries, :ok, fn entry, :ok ->
              from = Path.join(source, entry)
              to = Path.join(target_dir, entry)

              case File.cp_r(from, to) do
                {:ok, _files} -> {:cont, :ok}
                {:error, reason, _file} -> {:halt, {:error, {:data_copy_failed, entry, reason}}}
              end
            end)

          {:error, reason} ->
            {:error, {:data_unavailable, source_ref, reason}}
        end

      File.regular?(source) ->
        case File.cp(source, Path.join(target_dir, Path.basename(source))) do
          :ok -> :ok
          {:error, reason} -> {:error, {:data_copy_failed, Path.basename(source), reason}}
        end

      true ->
        {:error, {:data_unavailable, source_ref, :missing}}
    end
  end

  defp package_file(package_root, path) when is_binary(package_root) and is_binary(path) do
    root = Path.expand(package_root)

    expanded =
      if Path.type(path) == :absolute do
        Path.expand(path)
      else
        Path.expand(path, root)
      end

    if expanded == root or String.starts_with?(expanded, root <> "/") do
      {:ok, expanded}
    else
      {:error, :path_escape}
    end
  end

  defp package_file(_package_root, _path), do: {:error, :missing_package_root}

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
    case File.stat(source) do
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
    cond do
      File.dir?(path) ->
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

      File.regular?(path) ->
        File.chmod(path, read_only_file_mode(path))

      true ->
        :ok
    end
  end

  defp read_only_file_mode(path) do
    case File.stat(path) do
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
