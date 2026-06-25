defmodule Mitos.SandboxProvider.LocalSidecar do
  @moduledoc """
  Local engine-backed sandbox provider.

  The provider starts one `Mitos.Session`, which owns one `mitos-term` sidecar
  process and presents the current PTY/stdio-json local execution path behind
  the sandbox-provider seam.
  """

  @behaviour Mitos.SandboxProvider

  @impl true
  def prepare(%{} = launch_spec), do: {:ok, launch_spec}

  @impl true
  def start(%{} = handle, argv, env) when is_list(argv) do
    session_id = field(handle, :session_id) || field(handle, :run_id) || random_id()
    provider = field(handle, :provider) || if(argv == [], do: nil, else: "generic")
    task = field(handle, :task, "")

    if provider in [nil, ""] do
      {:error, :missing_provider}
    else
      Mitos.PubSub.subscribe(session_id)

      opts =
        [
          provider: provider,
          task: task,
          session_id: session_id,
          transport_order: field(handle, :transport_order, ["stdio-json", "pty"])
        ]
        |> maybe(:argv, non_empty(argv) || field(handle, :argv))
        |> maybe(:cwd, field(handle, :cwd))
        |> maybe(:env, merged_env(field(handle, :env), env))
        |> maybe(:cols, field(handle, :cols))
        |> maybe(:rows, field(handle, :rows))
        |> maybe(:idle_ms, field(handle, :idle_ms))
        |> maybe(:sidecar_path, field(handle, :sidecar_path))
        |> maybe(:trace_base_dir, field(handle, :trace_base_dir))
        |> maybe(:schema_root, field(handle, :schema_root))
        |> maybe(:project_id, field(handle, :project_id))
        |> maybe(:org_id, field(handle, :org_id))
        |> maybe(:actor_id, field(handle, :actor_id))
        |> maybe(:budget, field(handle, :budget))
        |> maybe(:capability_ref, field(handle, :capability_ref))
        |> maybe(:seller_id, field(handle, :seller_id))
        |> maybe(:trust_tier, field(handle, :trust_tier))
        |> maybe(:image, field(handle, :image))
        |> maybe(:data, field(handle, :data))
        |> maybe(:harnesses, field(handle, :harnesses))
        |> maybe(:skill, field(handle, :skill))
        |> maybe(:skill_adapt, field(handle, :skill_adapt))
        |> maybe(:resolved_skill, field(handle, :resolved_skill))
        |> maybe(:tools, field(handle, :tools))
        |> maybe(:resources, field(handle, :resources))
        |> maybe(:hooks, field(handle, :hooks))
        |> maybe(:policy, field(handle, :policy))
        |> maybe(:egress_policy, field(handle, :egress_policy))
        |> maybe(:result, field(handle, :result))
        |> maybe(:authority, field(handle, :authority))
        |> maybe(:knowledge, field(handle, :knowledge))
        |> maybe(:capability_runtime, field(handle, :capability_runtime))

      case Mitos.Agent.start(opts) do
        {:ok, run} ->
          {:ok,
           %{
             pid: run.pid,
             session_id: run.session_id,
             cwd: Keyword.get(opts, :cwd),
             trace_dir: trace_dir(run.session_id, Keyword.get(opts, :trace_base_dir)),
             launch_spec: handle,
             stream_owner: self()
           }}

        {:error, reason} ->
          {:error, reason}
      end
    end
  end

  def start(_handle, _argv, _env), do: {:error, :invalid_launch_handle}

  @impl true
  def stream(%{session_id: session_id} = session) when is_binary(session_id) do
    Stream.resource(
      fn -> Map.put(session, :done?, false) end,
      &next_stream_event/1,
      fn _session -> :ok end
    )
  end

  def stream(_session), do: []

  defp next_stream_event(%{done?: true} = session), do: {:halt, session}

  defp next_stream_event(%{session_id: session_id} = session) do
    receive do
      {:mitos, ^session_id, {:event, event}} ->
        event = stringify(event)
        {[event], %{session | done?: terminal_event?(event)}}

      {:mitos, ^session_id, {:raw, bytes}} when is_binary(bytes) ->
        {[%{"type" => "OUTPUT", "bytes" => bytes}], session}

      {:mitos, ^session_id, {:prompt, prompt}} ->
        {[%{"type" => "PROMPT", "prompt" => inspect(prompt)}], session}

      {:mitos, ^session_id, {:snapshot, snapshot}} ->
        {[%{"type" => "SNAPSHOT", "snapshot" => stringify(snapshot)}], session}
    after
      stream_timeout_ms(session) ->
        {
          [%{"type" => "ERROR", "kind" => "stream", "detail" => "timeout"}],
          %{session | done?: true}
        }
    end
  end

  @impl true
  def control(%{pid: pid}, command) when is_pid(pid), do: control(pid, command)

  def control(session, {:write_stdin, bytes}) when is_binary(bytes) do
    Mitos.Session.write_stdin(session, bytes)
  end

  def control(session, {:steer, interaction, opts}) when is_list(opts) do
    Mitos.Session.steer(session, interaction, opts)
  end

  def control(session, {:resize, cols, rows}) when is_integer(cols) and is_integer(rows) do
    Mitos.Session.resize(session, cols, rows)
  end

  def control(session, {:signal, signum}) when is_integer(signum) do
    Mitos.Session.signal(session, signum)
  end

  def control(session, {:shutdown, grace_ms}) when is_integer(grace_ms) do
    Mitos.Session.shutdown(session, grace_ms)
  end

  def control(_session, _command), do: {:error, :unsupported_control}

  @impl true
  def snapshot(%{pid: pid}) when is_pid(pid), do: snapshot(pid)

  def snapshot(session), do: Mitos.Session.snapshot(session)

  @impl true
  def harvest(%{pid: pid} = session) when is_pid(pid) do
    if Process.alive?(pid) do
      Mitos.Session.harvest(pid)
    else
      session
      |> Map.take([:cwd, :trace_dir, :launch_spec])
      |> Mitos.Harvest.collect()
    end
  catch
    :exit, _reason ->
      session
      |> Map.take([:cwd, :trace_dir, :launch_spec])
      |> Mitos.Harvest.collect()
  end

  def harvest(session) when is_pid(session), do: Mitos.Session.harvest(session)

  def harvest(%{} = session), do: Mitos.Harvest.collect(session)

  def harvest(opts) when is_list(opts), do: Mitos.Harvest.collect(opts)

  def harvest(_session), do: Mitos.Harvest.collect(%{})

  @impl true
  def stop(%{pid: pid, session_id: session_id}) when is_pid(pid) do
    if Process.alive?(pid) do
      Mitos.Session.shutdown(pid)
    else
      stop_registered_session(session_id)
    end

    :ok
  end

  def stop(session) do
    Mitos.Session.shutdown(session)
    :ok
  catch
    :exit, _reason -> :ok
  end

  @impl true
  def capabilities do
    %{
      isolation: :local_process,
      remote?: false,
      egress: :broker,
      data_mount: :bind,
      snapshot?: true,
      net_modes: [:host]
    }
  end

  @impl true
  def child_spec(opts), do: Mitos.Session.child_spec(opts)

  defp terminal_event?(%{"type" => type}) when type in ["exit", "PROCESS_EXITED", "sidecar_exit"],
    do: true

  defp terminal_event?(_event), do: false

  defp stream_timeout_ms(session) do
    case field(session, :stream_timeout_ms, 120_000) do
      timeout when is_integer(timeout) and timeout > 0 -> timeout
      _ -> 120_000
    end
  end

  defp merged_env(nil, nil), do: nil
  defp merged_env(env, nil), do: env
  defp merged_env(nil, env), do: env

  defp merged_env(left, right) do
    Map.merge(env_map(left), env_map(right))
  end

  defp env_map(%{} = env), do: Map.new(env, fn {key, value} -> {to_string(key), value} end)

  defp env_map(env) when is_list(env) do
    env
    |> Enum.flat_map(fn
      {key, value} -> [{to_string(key), value}]
      %{"name" => key, "value" => value} -> [{to_string(key), value}]
      %{name: key, value: value} -> [{to_string(key), value}]
      _ -> []
    end)
    |> Map.new()
  end

  defp env_map(_env), do: %{}

  defp non_empty([]), do: nil
  defp non_empty(value), do: value

  defp maybe(opts, _key, nil), do: opts
  defp maybe(opts, _key, []), do: opts
  defp maybe(opts, key, value), do: Keyword.put(opts, key, value)

  defp field(map, key, default \\ nil)

  defp field(%{} = map, key, default) when is_atom(key) do
    string_key = Atom.to_string(key)
    Map.get(map, key, Map.get(map, string_key, default))
  end

  defp field(_map, _key, default), do: default

  defp trace_dir(session_id, base_dir) do
    Path.join([base_dir || Mitos.Trace.base_dir(), ".runs", session_id])
  end

  defp stop_registered_session(session_id) when is_binary(session_id) do
    case Mitos.SessionSupervisor.stop_session(session_id) do
      :ok -> :ok
      {:error, :not_found} -> :ok
      _other -> :ok
    end
  catch
    :exit, _reason -> :ok
  end

  defp stop_registered_session(_session_id), do: :ok

  defp random_id do
    "local-" <> (:crypto.strong_rand_bytes(8) |> Base.url_encode64(padding: false))
  end

  defp stringify(%Msgpax.Bin{data: data}), do: data

  defp stringify(%{} = map) when not is_struct(map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify(value)} end)

  defp stringify(list) when is_list(list), do: Enum.map(list, &stringify/1)
  defp stringify(value), do: value
end
