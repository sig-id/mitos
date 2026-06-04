defmodule Mitos.Session do
  @moduledoc """
  One BEAM process per agent session. Owns a `mitos-term` OS process via a
  Port and bridges between it and Phoenix subscribers (browser WS clients and
  the orchestration brain).

  Lifetime is tied to the Port: when the Session goes away the Port closes,
  the sidecar sees EOF on stdin and exits — no socket cleanup, no zombies.
  When the sidecar dies the Port emits `:exit_status`/`{:EXIT, port, reason}`
  and we stop with that reason; our supervisor restarts us if configured to.

  Public API (call from any process):

      {:ok, pid} = Mitos.Orchestrator.start_session(opts)
      Mitos.Session.write_stdin(pid, "ls\\n")
      Mitos.Session.resize(pid, 100, 30)
      {:ok, snap} = Mitos.Session.snapshot(pid)
      Mitos.Session.shutdown(pid)

  Subscribers receive messages from `Mitos.PubSub`:

      {:mitos, session_id, {:raw, bytes}}        # opaque PTY bytes
      {:mitos, session_id, {:event, %{...}}}     # typed engine events
      {:mitos, session_id, {:prompt, event}}     # semantic prompt events
      {:mitos, session_id, {:snapshot, %{...}}}  # on-demand grid snapshot

  New subscribers get a `{:mitos, session_id, {:replay, bytes}}` on attach to
  catch up the current screen.
  """
  use GenServer
  require Logger

  alias Mitos.Authority.Request
  alias Mitos.Authority.Verdict
  alias Mitos.Sidecar.Protocol

  # 256 KiB of replayable PTY output per session. Browser tabs that connect
  # late get this much history. Larger than xterm.js's own buffer; the brain
  # can ask for `snapshot` for a structured view.
  @scrollback_cap 256 * 1024
  @authority_socket_timeout_ms 4_750

  defstruct [
    :session_id,
    :port,
    :sidecar_path,
    :launch_spec,
    :argv,
    :cwd,
    :env,
    :cols,
    :rows,
    :idle_ms,
    :snapshot_waiter,
    :trace,
    :authority,
    :project_id,
    backend: :unknown,
    scrollback: <<>>,
    exited?: false,
    pending: %{},
    pending_timers: %{},
    pending_monitors: %{},
    next_request_id: 1
  ]

  # -- public API -----------------------------------------------------------

  def child_spec(opts) do
    %{
      id: {__MODULE__, opts[:session_id]},
      start: {__MODULE__, :start_link, [opts]},
      restart: :transient,
      shutdown: 5_000
    }
  end

  def start_link(opts) do
    session_id = Keyword.fetch!(opts, :session_id)
    GenServer.start_link(__MODULE__, opts, name: via(session_id))
  end

  @doc "Address a Session GenServer by session id."
  def via(session_id), do: {:via, Registry, {Mitos.SessionRegistry, session_id}}

  def write_stdin(server, bytes) when is_binary(bytes),
    do: GenServer.cast(server, {:write_stdin, bytes})

  def resize(server, cols, rows) when is_integer(cols) and is_integer(rows),
    do: GenServer.cast(server, {:resize, cols, rows})

  def signal(server, signum) when is_integer(signum),
    do: GenServer.cast(server, {:signal, signum})

  def resolve(server, request_id, decision, payload \\ %{})
      when is_binary(request_id) and decision in [:authorize, :deny] and is_map(payload),
      do: GenServer.cast(server, {:resolve, request_id, decision, payload})

  def authorize(server, action_id, token) when is_binary(action_id) and is_binary(token),
    do: resolve(server, action_id, :authorize, %{"token" => token})

  def deny(server, action_id, reason) when is_binary(action_id) and is_binary(reason),
    do: resolve(server, action_id, :deny, %{"reason" => reason})

  @doc """
  Request a structured snapshot. Returns `{:ok, snapshot}` (the parsed
  msgpack map from the sidecar) or `{:error, reason}`. Synchronous: blocks
  until the next SNAPSHOT frame arrives.
  """
  def snapshot(server, timeout \\ 5_000), do: GenServer.call(server, :snapshot, timeout)

  def shutdown(server, grace_ms \\ 100), do: GenServer.cast(server, {:shutdown, grace_ms})

  def session_id(server), do: GenServer.call(server, :session_id)

  # -- GenServer ------------------------------------------------------------

  @impl true
  def init(opts) do
    Process.flag(:trap_exit, true)

    state = %__MODULE__{
      session_id: Keyword.fetch!(opts, :session_id),
      sidecar_path: Keyword.fetch!(opts, :sidecar_path),
      launch_spec: opts[:launch_spec],
      argv: opts[:argv],
      cwd: opts[:cwd],
      env: opts[:env],
      cols: Keyword.get(opts, :cols, 80),
      rows: Keyword.get(opts, :rows, 24),
      idle_ms: Keyword.get(opts, :idle_ms, 250),
      project_id: Keyword.get(opts, :project_id, "default")
    }

    state = %{state | trace: Mitos.Trace.open!(trace_options(state, opts))}
    state = maybe_start_authority(state, opts)

    {:ok, state, {:continue, :open_port}}
  end

  @impl true
  def handle_continue(:open_port, state) do
    port =
      Port.open(
        {:spawn_executable, state.sidecar_path},
        # packet: 4 → 4-byte big-endian length frame, exactly what the sidecar
        # speaks. :binary keeps payloads as binaries (not charlists).
        # :exit_status forwards the OS exit code as a message when the
        # sidecar dies, so we can mirror it as an `:exit` event.
        [:binary, :exit_status, packet: 4]
      )

    send_control(port, Protocol.hello())

    send_control(port, open_command(state))

    {:noreply, %{state | port: port}}
  end

  # Frames from the sidecar arrive as one message per frame thanks to
  # `packet: 4`. We dispatch by stream tag and act accordingly.
  @impl true
  def handle_info({port, {:data, payload}}, %{port: port} = state) do
    case Protocol.decode_frame(payload) do
      {:raw, bytes} ->
        Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:raw, bytes}})
        {:noreply, append_scrollback(state, bytes)}

      {:event, event} ->
        handle_event(event, state)

      {:snapshot, snap} ->
        state = trace_snapshot(state, snap)
        state = reply_snapshot(state, {:ok, snap})
        Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:snapshot, snap}})
        {:noreply, state}

      {:unknown, tag, _} ->
        Logger.warning(
          "mitos session #{inspect(state.session_id)} got unknown frame tag 0x#{Integer.to_string(tag, 16)}"
        )

        {:noreply, state}
    end
  end

  # The sidecar exited (the binary itself, not the agent inside it). Treat
  # this as terminal — the supervisor decides whether to restart us.
  def handle_info({port, {:exit_status, code}}, %{port: port} = state) do
    Logger.info("mitos session #{inspect(state.session_id)} sidecar exited code=#{code}")
    event = %{"type" => "sidecar_exit", "code" => code}
    state = trace_event(state, event)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})

    state = reply_snapshot(state, {:error, :sidecar_exited})
    {:stop, :normal, %{state | port: nil, exited?: true}}
  end

  def handle_info({:EXIT, port, reason}, %{port: port} = state) do
    state = reply_snapshot(state, {:error, :sidecar_exited})
    {:stop, reason, %{state | port: nil, exited?: true}}
  end

  def handle_info({:authority_event, event}, state) do
    handle_event(event, state)
  end

  def handle_info({:authority_request_timeout, request_id}, state) do
    {:noreply, timeout_pending_request(request_id, state)}
  end

  def handle_info({:DOWN, ref, :process, _pid, _reason}, state) do
    {:noreply, drop_pending_monitor(ref, state)}
  end

  def handle_info(_other, state), do: {:noreply, state}

  @impl true
  def handle_call(:session_id, _from, state), do: {:reply, state.session_id, state}

  def handle_call(:replay, _from, state), do: {:reply, state.scrollback, state}

  def handle_call(:snapshot, from, state) do
    case state.snapshot_waiter do
      nil ->
        send_control(state.port, Protocol.snapshot_request())
        {:noreply, %{state | snapshot_waiter: from}}

      _ ->
        {:reply, {:error, :snapshot_in_flight}, state}
    end
  end

  @impl true
  def handle_cast({:write_stdin, bytes}, state) do
    send_control(state.port, Protocol.input(bytes))
    {:noreply, state}
  end

  def handle_cast({:resize, cols, rows}, state) do
    send_control(state.port, Protocol.resize(cols, rows))
    {:noreply, %{state | cols: cols, rows: rows}}
  end

  def handle_cast({:signal, signum}, state) do
    send_control(state.port, Protocol.signal(signum))
    {:noreply, state}
  end

  def handle_cast({:authority_request, %Request{} = request}, state) do
    {state, _request} = register_pending_request(request, state)
    {:noreply, state}
  end

  def handle_cast({:resolve, request_id, decision, payload}, state) do
    {:noreply, resolve_pending_request(%Verdict{id: request_id, decision: decision, payload: payload}, state)}
  end

  def handle_cast({:shutdown, grace_ms}, state) do
    if state.port, do: send_control(state.port, Protocol.shutdown(grace_ms))
    {:noreply, state}
  end

  @impl true
  def terminate(_reason, state) do
    if state.authority do
      try do
        Mitos.Authority.stop(state.authority)
      catch
        :exit, _ -> :ok
      end
    end

    if state.port && not state.exited? do
      try do
        send_control(state.port, Protocol.shutdown(50))
      rescue
        _ -> :ok
      end
    end

    :ok
  end

  # -- helpers --------------------------------------------------------------

  defp send_control(nil, _command), do: :ok

  defp send_control(port, command) do
    Port.command(port, Protocol.encode_control(command))
  rescue
    ArgumentError -> :ok
  end

  defp open_command(%{launch_spec: %{} = launch_spec} = state) do
    Protocol.open_launch(launch_spec,
      cwd: state.cwd,
      env: encode_env(state.env),
      cols: state.cols,
      rows: state.rows,
      idle_ms: state.idle_ms
    )
  end

  defp open_command(%{argv: [_ | _] = argv} = state) do
    Protocol.open(argv,
      cwd: state.cwd,
      env: encode_env(state.env),
      cols: state.cols,
      rows: state.rows,
      idle_ms: state.idle_ms
    )
  end

  defp open_command(_state), do: raise(ArgumentError, "Session requires :launch_spec or :argv")

  defp handle_event(%{"type" => "hello", "backend" => backend} = event, state) do
    state = trace_event(state, event)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, %{state | backend: String.to_atom(backend)}}
  end

  defp handle_event(%{"type" => "PROMPT_DETECTED"} = event, state) do
    state = trace_event(state, event)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})

    if prompt = prompt_event(event) do
      Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:prompt, prompt}})
    end

    {:noreply, state}
  end

  defp handle_event(%{"type" => "ACTION_REQUESTED"} = event, state) do
    {request, event} = request_from_scrape_event(event)
    {state, request} = register_pending_request(request, state, event)

    state =
      case auto_decision(event) do
        nil ->
          state

        {decision, payload} ->
          resolve_pending_request(%Verdict{id: request.id, decision: decision, payload: payload}, state)
      end

    {:noreply, state}
  end

  defp handle_event(%{"type" => "exit"} = event, state) do
    # The sidecar reports the agent's exit *and then* exits itself; we don't
    # have to wait for the Port `:exit_status` to come around. Stopping here
    # keeps the Session lifetime tight against the agent process lifetime.
    state = trace_event(state, event)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    state = reply_snapshot(state, {:error, :exited})
    {:stop, :normal, %{state | exited?: true}}
  end

  defp handle_event(%{"type" => type} = event, state) when type in ["ERROR", "error"] do
    Logger.warning("mitos session #{inspect(state.session_id)} sidecar error: #{inspect(event)}")
    state = trace_event(state, event)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, state}
  end

  defp handle_event(event, state) do
    state = trace_event(state, event)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, state}
  end

  defp register_pending_request(%Request{} = request, state, event \\ nil) do
    {request_id, state} = allocate_request_id(state)
    request = %{request | id: request_id}
    event = event || event_from_request(request)
    event = canonical_request_event(event, request)

    state =
      state
      |> put_pending(request)
      |> trace_event(event)

    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {state, request}
  end

  defp put_pending(state, %Request{id: request_id, origin: :authority_socket, from: from} = request)
       when not is_nil(from) do
    {pid, _tag} = from
    monitor_ref = Process.monitor(pid)
    timer_ref = Process.send_after(self(), {:authority_request_timeout, request_id}, @authority_socket_timeout_ms)

    %{
      state
      | pending: Map.put(state.pending, request_id, request),
        pending_timers: Map.put(state.pending_timers, request_id, timer_ref),
        pending_monitors: Map.put(state.pending_monitors, monitor_ref, request_id)
    }
  end

  defp put_pending(state, %Request{id: request_id} = request) do
    %{state | pending: Map.put(state.pending, request_id, request)}
  end

  defp resolve_pending_request(%Verdict{id: request_id} = verdict, state) do
    case Map.fetch(state.pending, request_id) do
      {:ok, request} ->
        state = cleanup_pending_request(request_id, state)
        {state, reply_payload} = complete_request(request, verdict, state)
        event = verdict_event(request, verdict, reply_payload)
        state = trace_event(state, event)
        Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
        state

      :error ->
        state
    end
  end

  defp complete_request(%Request{origin: :terminal_scrape} = request, %Verdict{decision: :authorize} = verdict, state) do
    token = Map.get(verdict.payload, "token", Map.get(verdict.payload, :token, ""))
    send_control(state.port, Protocol.authorize(request.id, token, origin: request.origin))
    {state, verdict.payload}
  end

  defp complete_request(%Request{origin: :terminal_scrape} = request, %Verdict{decision: :deny} = verdict, state) do
    reason = verdict.reason || Map.get(verdict.payload, "reason", Map.get(verdict.payload, :reason, "denied"))
    send_control(state.port, Protocol.deny(request.id, reason, origin: request.origin))
    {state, Map.put(verdict.payload, "reason", reason)}
  end

  defp complete_request(%Request{origin: :authority_socket} = request, %Verdict{decision: :deny} = verdict, state) do
    reason = verdict.reason || Map.get(verdict.payload, "reason", Map.get(verdict.payload, :reason, "denied"))
    reply = %{"ok" => true, "request_id" => request.id, "decision" => "deny", "reason" => reason, "payload" => %{}}
    GenServer.reply(request.from, reply)
    {state, reply["payload"]}
  end

  defp complete_request(%Request{origin: :authority_socket} = request, %Verdict{decision: :authorize} = verdict, state) do
    {state, payload} = authorized_socket_payload(request, verdict.payload, state)

    reply = %{
      "ok" => true,
      "request_id" => request.id,
      "decision" => "authorize",
      "payload" => payload
    }

    GenServer.reply(request.from, reply)
    {state, payload}
  rescue
    exception ->
      payload = %{"error" => Exception.message(exception)}

      GenServer.reply(request.from, %{
        "ok" => false,
        "request_id" => request.id,
        "error" => "request_failed",
        "detail" => Exception.message(exception)
      })

      {state, payload}
  end

  defp authorized_socket_payload(%Request{method: "approval"}, payload, state), do: {state, payload}
  defp authorized_socket_payload(%Request{method: "tool_broker"} = request, _payload, state), do: execute_tool_broker(request, state)
  defp authorized_socket_payload(%Request{method: "memory_fetch"} = request, _payload, state), do: memory_fetch(request, state)
  defp authorized_socket_payload(%Request{method: "memory_put"} = request, _payload, state), do: memory_put(request, state)
  defp authorized_socket_payload(%Request{method: "artifact_register"} = request, _payload, state), do: artifact_register(request, state)

  defp timeout_pending_request(request_id, state) do
    case Map.fetch(state.pending, request_id) do
      {:ok, %Request{origin: :authority_socket, from: from}} ->
        GenServer.reply(from, %{"ok" => false, "error" => "timeout", "request_id" => request_id})
        cleanup_pending_request(request_id, state)

      {:ok, _request} ->
        state

      :error ->
        state
    end
  end

  defp drop_pending_monitor(ref, state) do
    case Map.pop(state.pending_monitors, ref) do
      {nil, _monitors} ->
        state

      {request_id, monitors} ->
        state
        |> Map.put(:pending_monitors, monitors)
        |> cleanup_pending_request(request_id, demonitor?: false)
    end
  end

  defp cleanup_pending_request(request_id, state, opts \\ []) do
    demonitor? = Keyword.get(opts, :demonitor?, true)

    state =
      case Map.pop(state.pending_timers, request_id) do
        {nil, timers} ->
          %{state | pending_timers: timers}

        {timer_ref, timers} ->
          Process.cancel_timer(timer_ref)
          %{state | pending_timers: timers}
      end

    state =
      if demonitor? do
        {monitor_ref, monitors} = pop_monitor_for_request(state.pending_monitors, request_id)
        if monitor_ref, do: Process.demonitor(monitor_ref, [:flush])
        %{state | pending_monitors: monitors}
      else
        state
      end

    %{state | pending: Map.delete(state.pending, request_id)}
  end

  defp pop_monitor_for_request(monitors, request_id) do
    Enum.reduce_while(monitors, {nil, monitors}, fn {ref, id}, _acc ->
      if id == request_id do
        {:halt, {ref, Map.delete(monitors, ref)}}
      else
        {:cont, {nil, monitors}}
      end
    end)
  end

  defp allocate_request_id(state) do
    request_id = "request-#{state.next_request_id}"
    {request_id, %{state | next_request_id: state.next_request_id + 1}}
  end

  defp request_from_scrape_event(event) do
    context = Map.get(event, "context_json", Map.get(event, "context", %{}))
    origin = origin_atom(Map.get(event, "origin", "terminal_scrape"))

    request = %Request{
      id: Map.get(event, "request_id", Map.get(event, "action_id")),
      method: Map.get(event, "method", "approval"),
      action: Map.get(event, "action", "terminal_input"),
      context: context,
      origin: origin,
      from: nil
    }

    {request, event}
  end

  defp event_from_request(%Request{} = request) do
    %{
      "type" => "ACTION_REQUESTED",
      "request_id" => request.id,
      "action_id" => request.id,
      "action" => request.action,
      "context" => context_to_text(request.context),
      "context_json" => request.context,
      "origin" => origin_string(request.origin),
      "source" => origin_string(request.origin),
      "method" => request.method
    }
  end

  defp canonical_request_event(event, %Request{} = request) do
    producer_request_id = Map.get(event, "producer_request_id", Map.get(event, "request_id", Map.get(event, "action_id")))

    event
    |> Map.put("request_id", request.id)
    |> Map.put("action_id", request.id)
    |> Map.put("origin", origin_string(request.origin))
    |> Map.put("source", Map.get(event, "source", origin_string(request.origin)))
    |> Map.put("method", request.method)
    |> Map.put("action", request.action)
    |> Map.put("context", context_to_text(request.context))
    |> Map.put("context_json", request.context)
    |> maybe_put_event("producer_request_id", producer_request_id)
  end

  defp verdict_event(%Request{} = request, %Verdict{} = verdict, payload) do
    %{
      "type" => "AUTHORITY_VERDICT",
      "request_id" => request.id,
      "action_id" => request.id,
      "origin" => origin_string(request.origin),
      "method" => request.method,
      "action" => request.action,
      "decision" => decision_string(verdict.decision),
      "payload" => payload
    }
    |> maybe_put_event("reason", verdict.reason)
  end

  defp auto_decision(%{"auto_decision" => decision} = event), do: parse_auto_decision(decision, event)
  defp auto_decision(%{"decision" => decision, "decided_by" => _} = event), do: parse_auto_decision(decision, event)
  defp auto_decision(_event), do: nil

  defp parse_auto_decision(decision, event) when decision in ["authorize", "allow", :authorize, :allow] do
    {:authorize, %{"decided_by" => Map.get(event, "decided_by", "policy"), "fast_path" => Map.get(event, "fast_path", false)}}
  end

  defp parse_auto_decision(decision, event) when decision in ["deny", "block", :deny, :block] do
    {:deny, %{"decided_by" => Map.get(event, "decided_by", "policy"), "fast_path" => Map.get(event, "fast_path", false)}}
  end

  defp parse_auto_decision(_decision, _event), do: nil

  defp origin_atom(:terminal_scrape), do: :terminal_scrape
  defp origin_atom(:authority_socket), do: :authority_socket
  defp origin_atom(:codec), do: :codec
  defp origin_atom("authority_socket"), do: :authority_socket
  defp origin_atom("codec"), do: :codec
  defp origin_atom(_), do: :terminal_scrape

  defp origin_string(:terminal_scrape), do: "terminal_scrape"
  defp origin_string(:authority_socket), do: "authority_socket"
  defp origin_string(:codec), do: "codec"

  defp decision_string(:authorize), do: "authorize"
  defp decision_string(:deny), do: "deny"

  defp maybe_put_event(map, _key, nil), do: map
  defp maybe_put_event(map, _key, ""), do: map
  defp maybe_put_event(map, key, value), do: Map.put(map, key, value)

  defp context_to_text(context) when is_binary(context), do: context
  defp context_to_text(context), do: Jason.encode!(context)

  defp execute_tool_broker(%Request{} = request, state) do
    context = context_map(request.context)
    cwd = context_get(context, "cwd", state.cwd || File.cwd!())

    {cmd, args, command_text} =
      case context_get(context, "argv") do
        [cmd | args] = argv when is_binary(cmd) ->
          {cmd, Enum.map(args, &to_string/1), Enum.join(argv, " ")}

        _ ->
          command = context_get(context, "command") || context_get(context, "cmd")
          if not is_binary(command) or command == "", do: raise(ArgumentError, "tool_broker requires context.command or context.argv")
          {"/bin/sh", ["-c", command], command}
      end

    {output, exit_status} = System.cmd(cmd, args, cd: cwd, stderr_to_stdout: true)

    payload = %{
      "command" => command_text,
      "cwd" => cwd,
      "stdout" => output,
      "stderr" => "",
      "exit_status" => exit_status
    }

    state =
      if state.trace do
        event =
          Map.merge(payload, %{
            "type" => "TOOL_BROKER_COMMAND",
            "request_id" => request.id,
            "origin" => origin_string(request.origin)
          })

        %{state | trace: Mitos.Trace.append_command(state.trace, event)}
      else
        state
      end

    {state, payload}
  end

  defp memory_fetch(%Request{} = request, state) do
    context = context_map(request.context)
    key = memory_key!(context)
    path = memory_path!(state, key)

    payload =
      case File.read(path) do
        {:ok, bytes} -> %{"key" => key, "found" => true, "bytes" => bytes}
        {:error, :enoent} -> %{"key" => key, "found" => false, "bytes" => nil}
        {:error, reason} -> raise File.Error, reason: reason, action: "read file", path: path
      end

    {state, payload}
  end

  defp memory_put(%Request{} = request, state) do
    context = context_map(request.context)
    key = memory_key!(context)
    bytes = context_get(context, "bytes", context_get(context, "value", ""))
    if not is_binary(bytes), do: raise(ArgumentError, "memory_put requires string bytes/value")

    path = memory_path!(state, key)
    File.mkdir_p!(Path.dirname(path))
    File.write!(path, bytes)

    {state, %{"key" => key, "bytes" => byte_size(bytes)}}
  end

  defp artifact_register(%Request{} = request, state) do
    context = context_map(request.context)
    path = context_get(context, "path") || raise ArgumentError, "artifact_register requires context.path"
    kind = context_get(context, "kind", "artifact")

    artifact = %{
      "type" => "ARTIFACT_REGISTERED",
      "request_id" => request.id,
      "kind" => kind,
      "path" => path,
      "metadata" => context_get(context, "metadata", %{})
    }

    state = trace_event(state, artifact)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, artifact}})

    {state, Map.delete(artifact, "type")}
  end

  defp memory_key!(context) do
    key = context_get(context, "key") || raise ArgumentError, "memory request requires context.key"

    cond do
      not is_binary(key) ->
        raise ArgumentError, "memory key must be a string"

      key == "" or String.contains?(key, ["..", "/", "\\"]) ->
        raise ArgumentError, "memory key must be a project-local filename"

      true ->
        key
    end
  end

  defp memory_path!(%{trace: %{dir: dir}, project_id: project_id}, key) do
    safe_project = Regex.replace(~r/[^A-Za-z0-9_.-]/, project_id, "_")
    base = Path.join([dir, "artifacts", "memory", safe_project])
    path = Path.expand(Path.join(base, key))
    base = Path.expand(base)

    if String.starts_with?(path, base <> "/") or path == base do
      path
    else
      raise ArgumentError, "memory key escapes project scope"
    end
  end

  defp memory_path!(_state, _key), do: raise(ArgumentError, "memory methods require an open trace")

  defp context_map(%{} = context), do: context
  defp context_map(context) when is_binary(context), do: %{"text" => context}
  defp context_map(_context), do: %{}

  defp context_get(context, key, default \\ nil) do
    Map.get(context, key, Map.get(context, String.to_atom(key), default))
  end

  defp trace_options(state, opts) do
    [
      run_id: state.session_id,
      launch_spec: state.launch_spec
    ]
    |> maybe_put_option(:base_dir, Keyword.get(opts, :trace_base_dir))
  end

  defp maybe_put_option(opts, _key, nil), do: opts
  defp maybe_put_option(opts, key, value), do: Keyword.put(opts, key, value)

  defp maybe_start_authority(%{launch_spec: nil} = state, _opts), do: state

  defp maybe_start_authority(state, opts) do
    if launch_spec_has_authority?(state.launch_spec) do
      state
    else
      {:ok, authority} =
        Mitos.Authority.start_link(
          run_id: state.session_id,
          session: self(),
          project_id: Keyword.get(opts, :project_id, "default")
        )

      authority_spec = Mitos.Authority.launch_spec(authority)

      %{
        state
        | authority: authority,
          launch_spec: Map.put(state.launch_spec, :authority, authority_spec)
      }
    end
  end

  defp launch_spec_has_authority?(%{} = launch_spec) do
    Map.has_key?(launch_spec, :authority) or Map.has_key?(launch_spec, "authority")
  end

  defp trace_event(%{trace: nil} = state, _event), do: state

  defp trace_event(state, event) do
    %{state | trace: Mitos.Trace.append_event(state.trace, event)}
  end

  defp trace_snapshot(%{trace: nil} = state, _snapshot), do: state

  defp trace_snapshot(state, snapshot) do
    %{state | trace: Mitos.Trace.append_snapshot(state.trace, snapshot)}
  end

  defp prompt_event(%{"prompt_kind" => "approval_requested", "text" => text} = event) do
    risk =
      event
      |> Map.get("risk", "medium")
      |> risk_atom()

    {:approval_requested, %{text: text, risk: risk, action: nil}}
  end

  defp prompt_event(%{"prompt_kind" => "question_asked", "text" => text}),
    do: {:question_asked, %{text: text}}

  defp prompt_event(%{"prompt_kind" => "final_answer_candidate", "text" => text}),
    do: {:final_answer_candidate, String.trim(text)}

  defp prompt_event(%{"prompt_kind" => "error_detected", "text" => text}),
    do: {:error_detected, String.trim(text)}

  defp prompt_event(%{"prompt_kind" => "idle_detected"}), do: :idle_detected
  defp prompt_event(_event), do: nil

  defp risk_atom("low"), do: :low
  defp risk_atom("medium"), do: :medium
  defp risk_atom("high"), do: :high
  defp risk_atom(_), do: :medium

  defp append_scrollback(state, bytes) do
    new = state.scrollback <> bytes

    new =
      if byte_size(new) > @scrollback_cap do
        drop = byte_size(new) - @scrollback_cap
        binary_part(new, drop, byte_size(new) - drop)
      else
        new
      end

    %{state | scrollback: new}
  end

  defp reply_snapshot(%{snapshot_waiter: nil} = state, _), do: state

  defp reply_snapshot(%{snapshot_waiter: waiter} = state, reply) do
    GenServer.reply(waiter, reply)
    %{state | snapshot_waiter: nil}
  end

  # Pass-through env vars the agent almost always needs. Anything else stays
  # out unless the caller asks for it — we don't want to leak the BEAM's own
  # env (BINDIR, RELEASE_*, ERL_*, ...) into the agent's process.
  @inherit_env_keys ~w(PATH HOME USER SHELL LANG LC_ALL LC_CTYPE TZ)

  defp encode_env(nil), do: default_env()
  defp encode_env([]), do: default_env()

  defp encode_env(env) when is_list(env) do
    base = default_env()
    overlay = Enum.into(env, %{}, fn {k, v} -> {to_string(k), to_string(v)} end)
    Map.merge(base, overlay)
  end

  defp encode_env(env) when is_map(env), do: encode_env(Map.to_list(env))

  # Inherit the small whitelist plus force a known-good TERM/COLORTERM so
  # providers emit ANSI color consistently under mitos-term.
  defp default_env do
    base =
      for key <- @inherit_env_keys,
          value = System.get_env(key),
          is_binary(value),
          into: %{},
          do: {key, value}

    base
    |> Map.put("TERM", "xterm-256color")
    |> Map.put("COLORTERM", "truecolor")
  end

  @doc """
  Drop-replay snapshot of the current scrollback. Used by new subscribers to
  catch up. Returns binary (possibly empty).
  """
  def replay(server), do: GenServer.call(server, :replay)
end
