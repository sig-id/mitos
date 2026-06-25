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

      {:ok, pid} = Mitos.SessionSupervisor.start_session(opts)
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
  @max_terminal_cols 512
  @max_terminal_rows 512
  @max_signal_number 64
  @max_shutdown_grace_ms 60_000
  @max_sidecar_frame_bytes 16 * 1024 * 1024
  @max_stdin_bytes @max_sidecar_frame_bytes - 1024
  @max_authority_response_bytes 1 * 1024 * 1024
  @max_steer_text_bytes 1 * 1024 * 1024
  @max_request_id_bytes 4 * 1024

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
    :schema_root,
    :opening_decision_seq,
    :latest_final_output,
    :pre_hook_results,
    :post_hook_results,
    :authority,
    :project_id,
    backend: :unknown,
    token_usage_entries: [],
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

  @doc false
  def max_stdin_bytes, do: @max_stdin_bytes

  @doc false
  def max_authority_response_bytes, do: @max_authority_response_bytes

  @doc false
  def max_steer_text_bytes, do: @max_steer_text_bytes

  @doc false
  def max_sidecar_frame_bytes, do: @max_sidecar_frame_bytes

  def write_stdin(server, bytes) do
    case normalize_stdin(bytes) do
      {:ok, bytes} -> GenServer.cast(server, {:write_stdin, bytes})
      {:error, reason} -> {:error, reason}
    end
  end

  @steer_no_payload_interactions [:approve, :deny, :interrupt]
  @steer_text_interactions [:answer, :prompt]

  @doc """
  Steer the interactive agent without knowing its keymap: the sidecar
  translates the interaction into the byte sequence the provider's TUI
  expects. `:answer`/`:prompt` take `text:`, `:select_option` takes `option:`.

      Mitos.Session.steer(pid, :approve)
      Mitos.Session.steer(pid, :prompt, text: "now run the tests")
  """
  def steer(server, interaction, opts \\ []) do
    case normalize_steer(interaction, opts) do
      {:ok, interaction, opts} -> GenServer.cast(server, {:steer, interaction, opts})
      {:error, reason} -> {:error, reason}
    end
  end

  @doc "Reply to a question the agent asked through its input box."
  def answer(server, text), do: steer(server, :answer, text: text)

  @doc "Send a new instruction to the agent's input box."
  def prompt(server, text), do: steer(server, :prompt, text: text)

  def resize(server, cols, rows) do
    case normalize_session_size(cols, rows) do
      {:ok, cols, rows} -> GenServer.cast(server, {:resize, cols, rows})
      {:error, reason} -> {:error, reason}
    end
  end

  def signal(server, signum) do
    case normalize_signal(signum) do
      {:ok, signum} -> GenServer.cast(server, {:signal, signum})
      {:error, reason} -> {:error, reason}
    end
  end

  def resolve(server, request_id, decision, payload \\ %{}) do
    case normalize_authority_resolution(request_id, decision, payload) do
      {:ok, request_id, decision, payload} ->
        GenServer.cast(server, {:resolve, request_id, decision, payload})

      {:error, reason} ->
        {:error, reason}
    end
  end

  def authorize(server, action_id, token),
    do: resolve(server, action_id, :authorize, %{"token" => token})

  def deny(server, action_id, reason),
    do: resolve(server, action_id, :deny, %{"reason" => reason})

  @doc """
  Request a structured snapshot. Returns `{:ok, snapshot}` (the parsed
  msgpack map from the sidecar) or `{:error, reason}`. Synchronous: blocks
  until the next SNAPSHOT frame arrives.
  """
  def snapshot(server, timeout \\ 5_000), do: GenServer.call(server, :snapshot, timeout)

  @doc "Harvest workspace diff, RESULT, and registered artifacts for a session."
  def harvest(server, timeout \\ 5_000), do: GenServer.call(server, :harvest, timeout)

  def shutdown(server, grace_ms \\ 100) do
    case normalize_shutdown_grace_ms(grace_ms) do
      {:ok, grace_ms} -> GenServer.cast(server, {:shutdown, grace_ms})
      {:error, reason} -> {:error, reason}
    end
  end

  def session_id(server), do: GenServer.call(server, :session_id)

  # -- GenServer ------------------------------------------------------------

  @impl true
  def init(opts) do
    Process.flag(:trap_exit, true)

    with :ok <- validate_session_cwd(opts[:cwd]),
         {:ok, env} <- normalize_session_env(opts[:env]),
         {:ok, cols, rows} <-
           normalize_session_size(Keyword.get(opts, :cols, 80), Keyword.get(opts, :rows, 24)),
         {:ok, idle_ms} <- normalize_session_idle_ms(Keyword.get(opts, :idle_ms, 250)) do
      state = %__MODULE__{
        session_id: Keyword.fetch!(opts, :session_id),
        sidecar_path: Keyword.fetch!(opts, :sidecar_path),
        launch_spec: opts[:launch_spec],
        argv: opts[:argv],
        cwd: opts[:cwd],
        env: env,
        cols: cols,
        rows: rows,
        idle_ms: idle_ms,
        trace: opts[:trace],
        schema_root: opts[:schema_root],
        opening_decision_seq: opts[:opening_decision_seq],
        project_id: Keyword.get(opts, :project_id, "default")
      }

      case validate_open_control_frame(state) do
        :ok ->
          state = maybe_start_authority(state, opts)

          case validate_open_control_frame(state) do
            :ok ->
              state =
                if state.trace,
                  do: state,
                  else: %{state | trace: Mitos.Trace.open!(trace_options(state, opts))}

              {:ok, state, {:continue, :open_port}}

            {:error, reason} ->
              stop_authority(state)
              {:stop, reason}
          end

        {:error, reason} ->
          {:stop, reason}
      end
    else
      {:error, reason} -> {:stop, reason}
    end
  end

  @impl true
  def handle_continue(:open_port, state) do
    {state, pre_hook_results} = run_hooks(state, "pre")
    state = %{state | pre_hook_results: pre_hook_results}

    if Mitos.Hooks.blocked?(pre_hook_results) do
      block_pre_launch(state, pre_hook_results)
    else
      open_sidecar(state)
    end
  end

  defp open_sidecar(state) do
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

  def handle_info({:subrun_timeout, parent_task_id, child_task_id}, state) do
    {:noreply, timeout_pending_subrun(parent_task_id, child_task_id, state)}
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

  def handle_call(:harvest, _from, state) do
    {state, harvest} = harvest_state(state)
    {:reply, {:ok, harvest}, state}
  end

  @impl true
  def handle_cast({:write_stdin, bytes}, state) do
    with {:ok, bytes} <- normalize_stdin(bytes) do
      send_control(state.port, Protocol.input(bytes))
    end

    {:noreply, state}
  end

  def handle_cast({:steer, interaction, opts}, state) do
    with {:ok, interaction, opts} <- normalize_steer(interaction, opts) do
      send_control(state.port, Protocol.steer(interaction, opts))
    end

    {:noreply, state}
  end

  def handle_cast({:resize, cols, rows}, state) do
    case normalize_session_size(cols, rows) do
      {:ok, cols, rows} ->
        send_control(state.port, Protocol.resize(cols, rows))
        {:noreply, %{state | cols: cols, rows: rows}}

      {:error, _reason} ->
        {:noreply, state}
    end
  end

  def handle_cast({:signal, signum}, state) do
    with {:ok, signum} <- normalize_signal(signum) do
      send_control(state.port, Protocol.signal(signum))
    end

    {:noreply, state}
  end

  def handle_cast({:authority_request, %Request{} = request}, state) do
    {state, request} = register_pending_request(request, state)
    state = maybe_apply_broker_policy(request, state)
    {:noreply, state}
  end

  def handle_cast({:resolve, request_id, decision, payload}, state) do
    state =
      case normalize_authority_resolution(request_id, decision, payload) do
        {:ok, request_id, decision, payload} ->
          resolve_pending_request(
            %Verdict{id: request_id, decision: decision, payload: payload},
            state
          )

        {:error, _reason} ->
          state
      end

    {:noreply, state}
  end

  def handle_cast({:shutdown, grace_ms}, state) do
    with {:ok, grace_ms} <- normalize_shutdown_grace_ms(grace_ms),
         true <- not is_nil(state.port) do
      send_control(state.port, Protocol.shutdown(grace_ms))
    end

    {:noreply, state}
  end

  @impl true
  def terminate(_reason, state) do
    Mitos.KeyVault.revoke_run(state.session_id)

    stop_authority(state)

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

  defp stop_authority(%{authority: nil}), do: :ok

  defp stop_authority(%{authority: authority}) do
    try do
      Mitos.Authority.stop(authority)
    catch
      :exit, _ -> :ok
    end
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

  defp validate_open_control_frame(state) do
    state
    |> open_command()
    |> Protocol.encode_control()
    |> IO.iodata_length()
    |> case do
      bytes when bytes <= @max_sidecar_frame_bytes -> :ok
      _bytes -> {:error, {:invalid_open_control, :too_large}}
    end
  rescue
    _exception -> {:error, {:invalid_open_control, :invalid}}
  end

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
          resolve_pending_request(
            %Verdict{id: request.id, decision: decision, payload: payload},
            state
          )
      end

    {:noreply, state}
  end

  defp handle_event(%{"type" => "RUN_STARTED"} = event, state) do
    state =
      state
      |> trace_event(event)
      |> trace_effect("capability_started", %{
        "ref" => %{
          "provider" => event["provider"],
          "adapter" => event["adapter"],
          "transport" => event["transport"]
        },
        "payload" =>
          %{
            "harness" => event["adapter"],
            "transport" => event["transport"]
          }
          |> put_present(
            "resolved_skill",
            resolved_skill_effect(launch_value(state.launch_spec, "resolved_skill"))
          )
      })
      |> lease_credentials()

    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, state}
  end

  defp handle_event(%{"type" => "FINAL_OUTPUT", "text" => text} = event, state)
       when is_binary(text) do
    state = trace_event(state, event)
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, %{state | latest_final_output: text}}
  end

  defp handle_event(%{"type" => "STRUCTURED_OUTPUT"} = event, state) do
    source_sequence = trace_sequence(state)

    state =
      state
      |> trace_event(event)
      |> collect_structured_token_usage(event, source_sequence)

    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, state}
  end

  defp handle_event(%{"type" => type} = event, state) when type in ["exit", "PROCESS_EXITED"] do
    # The sidecar reports the agent's exit *and then* exits itself; we don't
    # have to wait for the Port `:exit_status` to come around. Stopping here
    # keeps the Session lifetime tight against the agent process lifetime.
    {state, post_hook_results} = run_hooks(state, "post")
    state = maybe_trace_post_hook_verification(state, post_hook_results)
    {state, harvest} = harvest_state(state)
    verification = hook_verification(post_hook_results)

    state =
      state
      |> trace_event(event)
      |> flush_token_usage()
      |> trace_effect("capability_finished", %{
        "ref" => %{"result" => harvest.result["path"] || "RESULT"},
        "payload" => %{
          "status" => exit_status(event),
          "exit_code" => event["code"],
          "signal" => event["signal"],
          "final_output" => state.latest_final_output,
          "harvest" => Mitos.Harvest.effect_payload(harvest),
          "result" => harvest.result,
          "verification" => verification,
          "hooks" => %{
            "pre" => Enum.map(state.pre_hook_results || [], &Mitos.Hooks.effect_payload/1),
            "post" => Enum.map(post_hook_results, &Mitos.Hooks.effect_payload/1)
          }
        }
      })
      |> revoke_credentials()

    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    state = reply_snapshot(state, {:error, :exited})
    {:stop, :normal, %{state | post_hook_results: post_hook_results, exited?: true}}
  end

  defp handle_event(%{"type" => type} = event, state) when type in ["ERROR", "error"] do
    Logger.warning("mitos session #{inspect(state.session_id)} sidecar error: #{inspect(event)}")

    state =
      state
      |> trace_event(event)
      |> maybe_trace_launch_failure(event)

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
      |> maybe_trace_authority_request_effect(request)

    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {state, request}
  end

  defp put_pending(
         state,
         %Request{id: request_id, origin: :authority_socket, from: from} = request
       )
       when not is_nil(from) do
    {pid, _tag} = from
    monitor_ref = Process.monitor(pid)

    timer_ref =
      Process.send_after(
        self(),
        {:authority_request_timeout, request_id},
        @authority_socket_timeout_ms
      )

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
        state = maybe_trace_authority_verdict_effect(state, request, verdict, reply_payload)
        Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
        state

      :error ->
        state
    end
  end

  defp complete_request(
         %Request{origin: :terminal_scrape} = request,
         %Verdict{decision: :authorize} = verdict,
         state
       ) do
    token = Map.get(verdict.payload, "token", Map.get(verdict.payload, :token, ""))
    send_control(state.port, Protocol.authorize(request.id, token, origin: request.origin))
    {state, verdict.payload}
  end

  defp complete_request(
         %Request{origin: :terminal_scrape} = request,
         %Verdict{decision: :deny} = verdict,
         state
       ) do
    reason =
      verdict.reason ||
        Map.get(verdict.payload, "reason", Map.get(verdict.payload, :reason, "denied"))

    send_control(state.port, Protocol.deny(request.id, reason, origin: request.origin))
    {state, Map.put(verdict.payload, "reason", reason)}
  end

  defp complete_request(
         %Request{origin: :authority_socket} = request,
         %Verdict{decision: :deny} = verdict,
         state
       ) do
    reason =
      verdict.reason ||
        Map.get(verdict.payload, "reason", Map.get(verdict.payload, :reason, "denied"))

    reply = %{
      "ok" => true,
      "request_id" => request.id,
      "decision" => "deny",
      "reason" => reason,
      "payload" => %{}
    }

    state =
      if request.method == "tool_broker" do
        trace_effect(state, "egress_denied", %{
          "ref" => egress_ref(request),
          "payload" => %{"reason" => reason}
        })
      else
        state
      end

    GenServer.reply(request.from, reply)

    {state, reply["payload"]}
  end

  defp complete_request(
         %Request{origin: :authority_socket} = request,
         %Verdict{decision: :authorize} = verdict,
         state
       ) do
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

  defp authorized_socket_payload(%Request{method: "approval"}, payload, state),
    do: {state, payload}

  defp authorized_socket_payload(%Request{method: "tool_broker"} = request, payload, state),
    do: execute_tool_broker(%{request | context: merge_context(request.context, payload)}, state)

  defp authorized_socket_payload(%Request{method: "memory_fetch"} = request, _payload, state),
    do: memory_fetch(request, state)

  defp authorized_socket_payload(%Request{method: "memory_put"} = request, _payload, state),
    do: memory_put(request, state)

  defp authorized_socket_payload(%Request{method: "subrun_spawn"} = request, _payload, state),
    do: subrun_spawn(request, state)

  defp authorized_socket_payload(%Request{method: "subtask_return"} = request, _payload, state),
    do: subtask_return(request, state)

  defp authorized_socket_payload(%Request{method: "task_emit"} = request, _payload, state),
    do: task_emit(request, state)

  defp authorized_socket_payload(
         %Request{method: "artifact_register"} = request,
         _payload,
         state
       ),
       do: artifact_register(request, state)

  defp timeout_pending_request(request_id, state) do
    case Map.fetch(state.pending, request_id) do
      {:ok, %Request{origin: :authority_socket, from: from} = request} ->
        GenServer.reply(from, %{"ok" => false, "error" => "timeout", "request_id" => request_id})

        state
        |> trace_authority_timeout_effects(request)
        |> cleanup_pending_request(request_id)

      {:ok, _request} ->
        state

      :error ->
        state
    end
  end

  defp timeout_pending_subrun(_parent_task_id, _child_task_id, %{trace: nil} = state), do: state

  defp timeout_pending_subrun(parent_task_id, child_task_id, state) do
    events =
      state.trace.run_id
      |> Mitos.Trace.read_events(base_dir: trace_base_dir(state.trace))

    pool = Mitos.WorkPool.fold_events(events)
    invocation = get_in(pool, ["invocations", "#{parent_task_id}->#{child_task_id}"])

    if invocation && invocation["status"] == "requested" do
      trace =
        Mitos.WorkPool.append_subtask_timeout(
          state.trace,
          parent_task_id,
          child_task_id,
          caused_by: state.opening_decision_seq || 0,
          timeout_ms: invocation["timeout_ms"]
        )

      %{state | trace: trace}
    else
      state
    end
  end

  defp trace_base_dir(%Mitos.Trace{dir: dir}) do
    dir
    |> Path.dirname()
    |> Path.dirname()
  rescue
    _ -> Mitos.Trace.base_dir()
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
    producer_request_id =
      Map.get(
        event,
        "producer_request_id",
        Map.get(event, "request_id", Map.get(event, "action_id"))
      )

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
      "payload" => Mitos.Trace.redact_public_value(payload)
    }
    |> maybe_put_event("reason", Mitos.Trace.redact_public_text(verdict.reason))
  end

  defp auto_decision(%{"auto_decision" => decision} = event),
    do: parse_auto_decision(decision, event)

  defp auto_decision(%{"decision" => decision, "decided_by" => _} = event),
    do: parse_auto_decision(decision, event)

  defp auto_decision(_event), do: nil

  defp parse_auto_decision(decision, event)
       when decision in ["authorize", "allow", :authorize, :allow] do
    {:authorize,
     %{
       "decided_by" => Map.get(event, "decided_by", "policy"),
       "fast_path" => Map.get(event, "fast_path", false)
     }}
  end

  defp parse_auto_decision(decision, event) when decision in ["deny", "block", :deny, :block] do
    {:deny,
     %{
       "decided_by" => Map.get(event, "decided_by", "policy"),
       "fast_path" => Map.get(event, "fast_path", false)
     }}
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
    cwd = validate_broker_cwd!(context_get(context, "cwd", state.cwd || File.cwd!()))

    {cmd, args, command_text} =
      case context_get(context, "argv") do
        [cmd | args] = argv when is_binary(cmd) ->
          {cmd, Enum.map(args, &to_string/1), Enum.join(argv, " ")}

        _ ->
          command = context_get(context, "command") || context_get(context, "cmd")

          if not is_binary(command) or command == "",
            do: raise(ArgumentError, "tool_broker requires context.command or context.argv")

          {"/bin/sh", ["-c", command], command}
      end

    egress_ref = egress_ref(request)

    state =
      trace_effect(state, "egress_attempted", %{
        "ref" => egress_ref,
        "payload" => %{
          "command" => command_text,
          "cwd" => cwd,
          "origin" => origin_string(request.origin)
        }
      })

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

    state =
      state
      |> trace_effect("egress_allowed", %{
        "ref" => egress_ref,
        "payload" => %{
          "status" => exit_status,
          "stdout_bytes" => byte_size(output),
          "stderr_bytes" => 0
        }
      })
      |> trace_effect("egress_completed", %{
        "ref" => egress_ref,
        "payload" => %{
          "status" => exit_status,
          "stdout_bytes" => byte_size(output),
          "stderr_bytes" => 0
        }
      })
      |> trace_effect("tool_brokered", %{
        "ref" => %{"request_id" => request.id, "method" => request.method},
        "payload" => %{
          "command" => command_text,
          "cwd" => cwd,
          "exit_status" => exit_status,
          "stdout_bytes" => byte_size(output)
        }
      })

    {state, payload}
  end

  defp validate_broker_cwd!(cwd) when is_binary(cwd) and cwd != "" do
    path = Path.expand(cwd)

    with {:ok, %{type: :directory}} <- File.lstat(path),
         true <- no_symlink_absolute_path?(path) do
      path
    else
      {:ok, %{type: :symlink}} ->
        raise ArgumentError, "tool_broker cwd must not be a symlink"

      {:ok, _stat} ->
        raise ArgumentError, "tool_broker cwd must be a directory"

      {:error, reason} ->
        raise ArgumentError, "tool_broker cwd is unavailable: #{inspect(reason)}"

      false ->
        raise ArgumentError, "tool_broker cwd must not contain symlinks"
    end
  end

  defp validate_broker_cwd!(_cwd),
    do: raise(ArgumentError, "tool_broker cwd must be a non-empty string")

  defp validate_session_cwd(nil), do: :ok

  defp validate_session_cwd(cwd) when is_binary(cwd) do
    path = Path.expand(cwd)

    with {:ok, %{type: :directory}} <- File.lstat(path),
         true <- no_symlink_absolute_path?(path) do
      :ok
    else
      {:ok, %{type: :symlink}} -> {:error, {:invalid_cwd, :symlink}}
      {:ok, _stat} -> {:error, {:invalid_cwd, :not_directory}}
      {:error, reason} -> {:error, {:invalid_cwd, reason}}
      false -> {:error, {:invalid_cwd, :symlink}}
    end
  end

  defp validate_session_cwd(_cwd), do: {:error, {:invalid_cwd, :invalid}}

  defp normalize_session_size(cols, rows) do
    with {:ok, cols} <- normalize_terminal_dimension(cols, :cols, @max_terminal_cols),
         {:ok, rows} <- normalize_terminal_dimension(rows, :rows, @max_terminal_rows) do
      {:ok, cols, rows}
    end
  end

  defp normalize_terminal_dimension(value, _field, max)
       when is_integer(value) and value >= 1 and value <= max,
       do: {:ok, value}

  defp normalize_terminal_dimension(value, field, _max) when is_integer(value) and value < 1,
    do: {:error, {:invalid_size, {field, :too_small}}}

  defp normalize_terminal_dimension(value, field, max) when is_integer(value) and value > max,
    do: {:error, {:invalid_size, {field, :too_large}}}

  defp normalize_terminal_dimension(_value, field, _max),
    do: {:error, {:invalid_size, {field, :invalid}}}

  defp normalize_session_idle_ms(value) when is_integer(value) and value >= 0 and value <= 60_000,
    do: {:ok, value}

  defp normalize_session_idle_ms(value) when is_integer(value) and value < 0,
    do: {:error, {:invalid_idle_ms, :too_small}}

  defp normalize_session_idle_ms(value) when is_integer(value) and value > 60_000,
    do: {:error, {:invalid_idle_ms, :too_large}}

  defp normalize_session_idle_ms(_value), do: {:error, {:invalid_idle_ms, :invalid}}

  defp normalize_stdin(bytes) when is_binary(bytes) and byte_size(bytes) <= @max_stdin_bytes,
    do: {:ok, bytes}

  defp normalize_stdin(bytes) when is_binary(bytes),
    do: {:error, {:invalid_stdin, :too_large}}

  defp normalize_stdin(_bytes), do: {:error, {:invalid_stdin, :invalid}}

  defp normalize_authority_resolution(request_id, decision, payload) do
    with {:ok, request_id} <- normalize_authority_request_id(request_id),
         {:ok, decision} <- normalize_authority_decision(decision),
         {:ok, payload} <- normalize_authority_payload(decision, payload) do
      {:ok, request_id, decision, payload}
    end
  end

  defp normalize_authority_request_id(request_id) when is_binary(request_id) do
    cond do
      request_id == "" ->
        {:error, {:invalid_authority_resolution, {:request_id, :empty}}}

      byte_size(request_id) > @max_request_id_bytes ->
        {:error, {:invalid_authority_resolution, {:request_id, :too_large}}}

      String.contains?(request_id, <<0>>) ->
        {:error, {:invalid_authority_resolution, {:request_id, :nul}}}

      true ->
        {:ok, request_id}
    end
  end

  defp normalize_authority_request_id(_request_id),
    do: {:error, {:invalid_authority_resolution, {:request_id, :invalid}}}

  defp normalize_authority_decision(decision) when decision in [:authorize, :deny],
    do: {:ok, decision}

  defp normalize_authority_decision(_decision),
    do: {:error, {:invalid_authority_resolution, {:decision, :invalid}}}

  defp normalize_authority_payload(decision, payload) when is_map(payload) do
    case decision do
      :authorize ->
        case fetch_payload_value(payload, "token") do
          {:ok, token} ->
            with {:ok, token} <- normalize_authority_response_text(token, :token) do
              {:ok, put_payload_value(payload, "token", token)}
            end

          :error ->
            {:ok, payload}
        end

      :deny ->
        case fetch_payload_value(payload, "reason") do
          {:ok, reason} ->
            with {:ok, reason} <- normalize_authority_response_text(reason, :reason) do
              {:ok, put_payload_value(payload, "reason", reason)}
            end

          :error ->
            {:ok, payload}
        end
    end
  end

  defp normalize_authority_payload(_decision, _payload),
    do: {:error, {:invalid_authority_resolution, {:payload, :invalid}}}

  defp fetch_payload_value(payload, key) do
    cond do
      Map.has_key?(payload, key) ->
        {:ok, Map.fetch!(payload, key)}

      Map.has_key?(payload, String.to_atom(key)) ->
        {:ok, Map.fetch!(payload, String.to_atom(key))}

      true ->
        :error
    end
  end

  defp put_payload_value(payload, key, value) do
    payload
    |> Map.delete(String.to_atom(key))
    |> Map.put(key, value)
  end

  defp normalize_authority_response_text(value, field) when is_binary(value) do
    cond do
      byte_size(value) > @max_authority_response_bytes ->
        {:error, {:invalid_authority_resolution, {field, :too_large}}}

      String.contains?(value, <<0>>) ->
        {:error, {:invalid_authority_resolution, {field, :nul}}}

      true ->
        {:ok, value}
    end
  end

  defp normalize_authority_response_text(_value, field),
    do: {:error, {:invalid_authority_resolution, {field, :invalid}}}

  defp normalize_steer(interaction, opts) do
    with :ok <- validate_steer_opts(opts) do
      normalize_steer_interaction(interaction, opts)
    end
  end

  defp validate_steer_opts(opts) when is_list(opts) do
    if Enum.all?(opts, &match?({_key, _value}, &1)) do
      :ok
    else
      {:error, {:invalid_steer, :invalid_options}}
    end
  end

  defp validate_steer_opts(_opts), do: {:error, {:invalid_steer, :invalid_options}}

  defp normalize_steer_interaction(interaction, _opts)
       when interaction in @steer_no_payload_interactions,
       do: {:ok, interaction, []}

  defp normalize_steer_interaction(interaction, opts)
       when interaction in @steer_text_interactions do
    case fetch_steer_opt(opts, :text) do
      {:ok, text} ->
        with {:ok, text} <- normalize_steer_text(text) do
          {:ok, interaction, [text: text]}
        end

      :error ->
        {:error, {:invalid_steer, {:text, :required}}}
    end
  end

  defp normalize_steer_interaction(:select_option, opts) do
    case fetch_steer_opt(opts, :option) do
      {:ok, option} ->
        with {:ok, option} <- normalize_steer_option(option) do
          {:ok, :select_option, [option: option]}
        end

      :error ->
        {:error, {:invalid_steer, {:option, :required}}}
    end
  end

  defp normalize_steer_interaction(_interaction, _opts),
    do: {:error, {:invalid_steer, :invalid_interaction}}

  defp fetch_steer_opt(opts, key) do
    case Enum.find(opts, fn {opt_key, _value} -> opt_key == key end) do
      {^key, value} -> {:ok, value}
      nil -> :error
    end
  end

  defp normalize_steer_text(text) when is_binary(text) do
    cond do
      byte_size(text) > @max_steer_text_bytes -> {:error, {:invalid_steer, {:text, :too_large}}}
      not String.valid?(text) -> {:error, {:invalid_steer, {:text, :invalid}}}
      String.contains?(text, <<0>>) -> {:error, {:invalid_steer, {:text, :nul}}}
      String.trim(text) == "" -> {:error, {:invalid_steer, {:text, :empty}}}
      true -> {:ok, text}
    end
  end

  defp normalize_steer_text(_text), do: {:error, {:invalid_steer, {:text, :invalid}}}

  defp normalize_steer_option(value) when is_integer(value) and value >= 1 and value <= 9,
    do: {:ok, value}

  defp normalize_steer_option(value) when is_integer(value) and value < 1,
    do: {:error, {:invalid_steer, {:option, :too_small}}}

  defp normalize_steer_option(value) when is_integer(value) and value > 9,
    do: {:error, {:invalid_steer, {:option, :too_large}}}

  defp normalize_steer_option(_value),
    do: {:error, {:invalid_steer, {:option, :invalid}}}

  defp normalize_signal(value)
       when is_integer(value) and value >= 1 and value <= @max_signal_number,
       do: {:ok, value}

  defp normalize_signal(value) when is_integer(value) and value < 1,
    do: {:error, {:invalid_signal, :too_small}}

  defp normalize_signal(value) when is_integer(value) and value > @max_signal_number,
    do: {:error, {:invalid_signal, :too_large}}

  defp normalize_signal(_value), do: {:error, {:invalid_signal, :invalid}}

  defp normalize_shutdown_grace_ms(value)
       when is_integer(value) and value >= 0 and value <= @max_shutdown_grace_ms,
       do: {:ok, value}

  defp normalize_shutdown_grace_ms(value) when is_integer(value) and value < 0,
    do: {:error, {:invalid_shutdown_grace_ms, :too_small}}

  defp normalize_shutdown_grace_ms(value)
       when is_integer(value) and value > @max_shutdown_grace_ms,
       do: {:error, {:invalid_shutdown_grace_ms, :too_large}}

  defp normalize_shutdown_grace_ms(_value),
    do: {:error, {:invalid_shutdown_grace_ms, :invalid}}

  defp normalize_session_env(nil), do: {:ok, nil}
  defp normalize_session_env([]), do: {:ok, %{}}

  defp normalize_session_env(%{} = env) do
    env
    |> Enum.reduce_while({:ok, %{}}, fn {key, value}, {:ok, acc} ->
      case normalize_env_pair(key, value) do
        {:ok, name, value} -> {:cont, {:ok, Map.put(acc, name, value)}}
        {:error, reason} -> {:halt, {:error, {:invalid_env, reason}}}
      end
    end)
  end

  defp normalize_session_env(env) when is_list(env) do
    env
    |> Enum.reduce_while({:ok, %{}}, fn entry, {:ok, acc} ->
      case normalize_env_entry(entry) do
        {:ok, name, value} -> {:cont, {:ok, Map.put(acc, name, value)}}
        {:error, reason} -> {:halt, {:error, {:invalid_env, reason}}}
      end
    end)
  end

  defp normalize_session_env(_env), do: {:error, {:invalid_env, :invalid}}

  defp normalize_env_entry({key, value}), do: normalize_env_pair(key, value)

  defp normalize_env_entry(%{"name" => key, "value" => value}),
    do: normalize_env_pair(key, value)

  defp normalize_env_entry(%{name: key, value: value}), do: normalize_env_pair(key, value)
  defp normalize_env_entry(_entry), do: {:error, :invalid_entry}

  defp normalize_env_pair(key, value) do
    with {:ok, name} <- env_string(key),
         {:ok, value} <- env_string(value),
         :ok <- validate_env_name(name),
         :ok <- validate_env_value(name, value) do
      {:ok, name, value}
    end
  end

  defp env_string(value) do
    {:ok, to_string(value)}
  rescue
    Elixir.Protocol.UndefinedError -> {:error, :invalid_entry}
  end

  defp validate_env_name(""), do: {:error, :empty_name}

  defp validate_env_name(name) do
    cond do
      String.contains?(name, <<0>>) -> {:error, {:invalid_name, name}}
      String.contains?(name, "=") -> {:error, {:invalid_name, name}}
      true -> :ok
    end
  end

  defp validate_env_value(name, value) do
    if String.contains?(value, <<0>>) do
      {:error, {:invalid_value, name}}
    else
      :ok
    end
  end

  defp memory_fetch(%Request{} = request, state) do
    context = context_map(request.context)
    key = memory_key!(context)

    payload =
      case read_memory_file(state, key) do
        {:ok, bytes} -> %{"key" => key, "found" => true, "bytes" => bytes}
        {:error, :enoent} -> %{"key" => key, "found" => false, "bytes" => nil}
        {:error, :symlink} -> raise ArgumentError, "memory file must not be a symlink"
        {:error, reason} -> raise File.Error, reason: reason, action: "read memory file"
      end

    {state, payload}
  end

  defp memory_put(%Request{} = request, state) do
    context = context_map(request.context)
    key = memory_key!(context)
    bytes = context_get(context, "bytes", context_get(context, "value", ""))
    if not is_binary(bytes), do: raise(ArgumentError, "memory_put requires string bytes/value")

    path = memory_path!(state, key)
    ensure_memory_directory!(state)
    write_memory_file!(path, bytes)

    payload = %{"key" => key, "bytes" => byte_size(bytes)}

    state =
      trace_effect(state, "memory_written", %{
        "ref" => %{"request_id" => request.id, "key" => key},
        "payload" => payload
      })

    {state, payload}
  end

  defp artifact_register(%Request{} = request, state) do
    context = context_map(request.context)

    path =
      context_get(context, "path") ||
        raise ArgumentError, "artifact_register requires context.path"

    kind = context_get(context, "kind", "artifact")

    artifact = %{
      "type" => "ARTIFACT_REGISTERED",
      "request_id" => request.id,
      "kind" => kind,
      "path" => path,
      "metadata" => context_get(context, "metadata", %{})
    }

    state =
      state
      |> trace_event(artifact)
      |> trace_effect("artifact_registered", %{
        "ref" => Map.put(Map.delete(artifact, "type"), "request_id", request.id),
        "payload" => %{}
      })

    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, artifact}})

    {state, Map.delete(artifact, "type")}
  end

  defp subrun_spawn(%Request{}, %{trace: nil}) do
    raise ArgumentError, "subrun_spawn requires an open trace"
  end

  defp subrun_spawn(%Request{} = request, state) do
    context = context_map(request.context)
    parent_task_id = subrun_parent_task_id(context, state)
    child_task = subrun_child_task(context, state, request)
    return_to = subrun_return_to(request, context, state)
    timeout_ms = optional_integer(context_get(context, "timeout_ms"))

    {trace, admitted_child_task, admission} =
      Mitos.WorkPool.append_invoke(
        state.trace,
        parent_task_id,
        child_task,
        caused_by: state.opening_decision_seq || 0,
        return_to: return_to,
        timeout_ms: timeout_ms,
        max_depth: optional_integer(context_get(context, "max_depth")) || 3,
        budget_cap: context_get(context, "budget_cap", %{}),
        dedupe_key: context_get(context, "dedupe_key"),
        allowed_capabilities:
          context_get(context, "allowed_capabilities") ||
            context_get(context, "allowlist") ||
            context_get(context, "policy_allowlist")
      )

    if admitted_child_task && is_integer(timeout_ms) && timeout_ms > 0 do
      Process.send_after(self(), {:subrun_timeout, parent_task_id, child_task["id"]}, timeout_ms)
    end

    {%{state | trace: trace},
     %{
       "parent_task_id" => parent_task_id,
       "child_task_id" => child_task["id"],
       "pool" => child_task["pool"],
       "status" => if(admitted_child_task, do: "queued", else: admission["status"]),
       "return_to" => return_to,
       "admission" => admission
     }}
  end

  defp task_emit(%Request{}, %{trace: nil}) do
    raise ArgumentError, "task_emit requires an open trace"
  end

  defp task_emit(%Request{} = request, state) do
    context = context_map(request.context)
    parent_task_id = subrun_parent_task_id(context, state)
    task = subrun_child_task(context, state, request)

    {trace, emitted_task} =
      Mitos.WorkPool.append_emitted_task(
        state.trace,
        parent_task_id,
        task,
        caused_by: state.opening_decision_seq || 0,
        emitted_by: "authority:#{request.id || "task_emit"}",
        max_depth: optional_integer(context_get(context, "max_depth")) || 3,
        budget_cap: context_get(context, "budget_cap", %{}),
        dedupe_key: context_get(context, "dedupe_key")
      )

    reply =
      if emitted_task do
        %{
          "parent_task_id" => parent_task_id,
          "task_id" => emitted_task["id"],
          "pool" => emitted_task["pool"],
          "status" => "queued"
        }
      else
        %{
          "parent_task_id" => parent_task_id,
          "task_id" => task["id"],
          "status" => "not_enqueued"
        }
      end

    {%{state | trace: trace}, reply}
  end

  defp subrun_parent_task_id(context, state) do
    context
    |> context_get(
      "parent_task_id",
      context_get(context, "parent_task", "run:#{state.session_id}")
    )
    |> required_task_string!("subrun_spawn requires context.parent_task_id")
  end

  defp subrun_child_task(context, state, request) do
    raw_task = context_get(context, "task", context_get(context, "task_spec", %{}))
    task_attrs = subrun_task_attrs(raw_task)
    task_payload = context_map(context_get(task_attrs, "payload", %{}))

    payload =
      task_payload
      |> put_present(
        "capability_ref",
        context_get(context, "capability_ref", context_get(context, "capability"))
      )
      |> put_present("inputs", context_get(context, "inputs"))
      |> put_present("args", context_get(context, "args"))

    task_attrs
    |> Map.put("id", subrun_child_task_id(context, task_attrs, request))
    |> Map.put("pool", optional_task_string(context_get(context, "pool"), "calls"))
    |> Map.put("tenant", subrun_tenant(context, state))
    |> Map.put("payload", payload)
    |> put_present("priority", optional_integer(context_get(context, "priority")))
    |> put_present("budget", context_get(context, "budget", context_get(task_attrs, "budget")))
    |> put_present("deps", context_get(context, "deps", context_get(task_attrs, "deps")))
    |> put_present(
      "pipeline",
      context_get(context, "pipeline", context_get(task_attrs, "pipeline"))
    )
    |> put_present(
      "external",
      context_get(context, "external", context_get(task_attrs, "external"))
    )
  end

  defp subrun_task_attrs(%{} = attrs), do: stringify_keys(attrs)

  defp subrun_task_attrs(task) when is_binary(task) do
    %{"payload" => %{"intent" => task}}
  end

  defp subrun_task_attrs(_task), do: %{}

  defp subrun_child_task_id(context, task_attrs, request) do
    context
    |> context_get(
      "child_task_id",
      context_get(task_attrs, "id", "subrun:#{request.id || System.unique_integer([:positive])}")
    )
    |> required_task_string!("subrun_spawn requires a child task id")
  end

  defp subrun_tenant(context, state) do
    case context_get(context, "tenant") do
      %{} = tenant ->
        stringify_keys(tenant)

      _ ->
        %{
          "org" => (state.trace && state.trace.org_id) || "default",
          "project" => (state.trace && state.trace.project_id) || state.project_id || "default"
        }
    end
  end

  defp subrun_return_to(%Request{} = request, context, state) do
    case context_get(context, "return_to") do
      %{} = return_to ->
        stringify_keys(return_to)

      nil ->
        %{
          "channel" => "authority_socket",
          "request_id" => request.id,
          "run_id" => state.session_id
        }

      value ->
        %{"channel" => to_string(value), "request_id" => request.id, "run_id" => state.session_id}
    end
  end

  defp subtask_return(%Request{}, %{trace: nil}) do
    raise ArgumentError, "subtask_return requires an open trace"
  end

  defp subtask_return(%Request{} = request, state) do
    context = context_map(request.context)

    parent_task_id =
      context
      |> context_get("parent_task_id", context_get(context, "parent_task"))
      |> required_task_string!("subtask_return requires context.parent_task_id")

    child_task_id =
      context
      |> context_get("child_task_id", context_get(context, "child_task"))
      |> required_task_string!("subtask_return requires context.child_task_id")

    result = context_get(context, "result", context_get(context, "payload", %{}))

    caused_by = state.opening_decision_seq || 0

    trace =
      state.trace
      |> Mitos.WorkPool.append_status(child_task_id, :done,
        caused_by: caused_by,
        result: result
      )
      |> Mitos.WorkPool.append_subtask_return(
        parent_task_id,
        child_task_id,
        result,
        caused_by: caused_by
      )

    {%{state | trace: trace},
     %{
       "parent_task_id" => parent_task_id,
       "child_task_id" => child_task_id,
       "status" => "returned",
       "result" => result
     }}
  end

  defp memory_key!(context) do
    key =
      context_get(context, "key") || raise ArgumentError, "memory request requires context.key"

    cond do
      not is_binary(key) ->
        raise ArgumentError, "memory key must be a string"

      key == "" or String.contains?(key, ["..", "/", "\\"]) ->
        raise ArgumentError, "memory key must be a project-local filename"

      true ->
        key
    end
  end

  defp read_memory_file(state, key) do
    base = memory_base!(state)
    path = memory_path!(state, key)

    with true <- no_symlink_path?(path, base),
         {:ok, bytes} <- read_regular_file(path) do
      {:ok, bytes}
    else
      false -> {:error, :symlink}
      {:error, reason} -> {:error, reason}
    end
  end

  defp ensure_memory_directory!(%{trace: %{dir: dir}} = state) do
    ensure_descendant_directory!(dir, memory_base!(state), "memory directory")
  end

  defp ensure_memory_directory!(_state),
    do: raise(ArgumentError, "memory methods require an open trace")

  defp write_memory_file!(path, bytes) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} ->
        File.rm!(path)
        write_new_memory_file!(path, bytes)

      {:error, :enoent} ->
        write_new_memory_file!(path, bytes)

      {:ok, %{type: :symlink}} ->
        raise ArgumentError, "memory file must not be a symlink"

      {:ok, stat} ->
        raise ArgumentError, "memory file must be regular: #{inspect(stat.type)}"

      {:error, reason} ->
        raise File.Error, reason: reason, action: "inspect memory file", path: path
    end
  end

  defp write_new_memory_file!(path, bytes) do
    case File.open(path, [:write, :exclusive], fn io -> IO.binwrite(io, bytes) end) do
      {:ok, :ok} ->
        :ok

      {:ok, {:error, reason}} ->
        raise File.Error, reason: reason, action: "write memory file", path: path

      {:error, reason} ->
        raise File.Error, reason: reason, action: "create memory file", path: path
    end
  end

  defp memory_path!(state, key) do
    base = memory_base!(state)
    path = Path.expand(Path.join(base, key))
    base = Path.expand(base)

    if String.starts_with?(path, base <> "/") or path == base do
      path
    else
      raise ArgumentError, "memory key escapes project scope"
    end
  end

  defp memory_base!(%{trace: %{dir: dir}, project_id: project_id}) do
    safe_project = Regex.replace(~r/[^A-Za-z0-9_.-]/, project_id, "_")
    Path.join([dir, "artifacts", "memory", safe_project])
  end

  defp memory_base!(_state), do: raise(ArgumentError, "memory methods require an open trace")

  defp ensure_descendant_directory!(root, path, label) do
    root = Path.expand(root)
    path = Path.expand(path)

    unless path == root or String.starts_with?(path, root <> "/") do
      raise ArgumentError, "#{label} escapes root"
    end

    parts = if path == root, do: [], else: Path.split(Path.relative_to(path, root))

    with {:ok, %{type: :directory}} <- File.lstat(root) do
      Enum.reduce(parts, root, fn part, current ->
        candidate = Path.join(current, part)

        case File.lstat(candidate) do
          {:ok, %{type: :directory}} ->
            candidate

          {:error, :enoent} ->
            mkdir_regular!(candidate, label)
            candidate

          {:ok, %{type: :symlink}} ->
            raise ArgumentError, "#{label} must not contain symlinks"

          {:ok, stat} ->
            raise ArgumentError, "#{label} component must be a directory: #{inspect(stat.type)}"

          {:error, reason} ->
            raise File.Error, reason: reason, action: "inspect #{label}", path: candidate
        end
      end)

      :ok
    else
      {:ok, %{type: :symlink}} ->
        raise ArgumentError, "#{label} root must not be a symlink"

      {:ok, stat} ->
        raise ArgumentError, "#{label} root must be a directory: #{inspect(stat.type)}"

      {:error, reason} ->
        raise File.Error, reason: reason, action: "inspect #{label} root", path: root
    end
  end

  defp mkdir_regular!(path, label) do
    case File.mkdir(path) do
      :ok ->
        :ok

      {:error, :eexist} ->
        unless match?({:ok, %{type: :directory}}, File.lstat(path)) do
          raise ArgumentError, "#{label} component must be a directory"
        end

      {:error, reason} ->
        raise File.Error, reason: reason, action: "create #{label}", path: path
    end
  end

  defp no_symlink_path?(path, root) do
    root = Path.expand(root)
    path = Path.expand(path)

    unless path == root or String.starts_with?(path, root <> "/") do
      raise ArgumentError, "memory path escapes root"
    end

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

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, %{type: :symlink}} -> {:error, :symlink}
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
    end
  end

  defp context_map(%{} = context), do: context
  defp context_map(context) when is_binary(context), do: %{"text" => context}
  defp context_map(_context), do: %{}

  defp context_get(context, key, default \\ nil) do
    Map.get(context, key, Map.get(context, String.to_atom(key), default))
  end

  defp stringify_keys(%{} = map) do
    Map.new(map, fn {key, value} -> {to_string(key), value} end)
  end

  defp put_present(map, _key, nil), do: map
  defp put_present(map, _key, ""), do: map
  defp put_present(map, key, value), do: Map.put(map, key, value)

  defp required_task_string!(nil, message), do: raise(ArgumentError, message)
  defp required_task_string!("", message), do: raise(ArgumentError, message)
  defp required_task_string!(value, _message) when is_binary(value), do: value
  defp required_task_string!(value, _message), do: to_string(value)

  defp optional_task_string(nil, default), do: default
  defp optional_task_string("", default), do: default
  defp optional_task_string(value, _default) when is_binary(value), do: value
  defp optional_task_string(value, _default), do: to_string(value)

  defp optional_integer(nil), do: nil
  defp optional_integer(value) when is_integer(value), do: value

  defp optional_integer(value) when is_binary(value) do
    case Integer.parse(value) do
      {integer, ""} -> integer
      _ -> nil
    end
  end

  defp optional_integer(_value), do: nil

  defp egress_ref(%Request{} = request) do
    context = context_map(request.context)

    %{
      "request_id" => request.id,
      "method" => request.method,
      "dest" =>
        context_get(context, "dest", context_get(context, "destination", "local:tool_broker")),
      "credential_id" =>
        context_get(context, "credential_id", context_get(context, "credential")),
      "seller_id" => context_get(context, "seller_id"),
      "policy_rule" => context_get(context, "policy_rule"),
      "nonce" => context_get(context, "nonce")
    }
    |> Enum.reject(fn {_key, value} -> is_nil(value) or value == "" end)
    |> Map.new()
  end

  defp merge_context(context, payload) when is_map(payload),
    do: Map.merge(context_map(context), context_map(payload))

  defp merge_context(context, _payload), do: context_map(context)

  defp maybe_apply_broker_policy(
         %Request{origin: :authority_socket, method: "tool_broker"} = request,
         state
       ) do
    case configured_egress_policy(state.launch_spec) do
      :unset ->
        resolve_broker_policy(request, [], state)

      policy when is_list(policy) ->
        resolve_broker_policy(request, policy, state)
    end
  end

  defp maybe_apply_broker_policy(_request, state), do: state

  defp resolve_broker_policy(%Request{} = request, policy, state) do
    dest = request_dest(request)

    case Enum.find(policy, &policy_matches_dest?(&1, dest)) do
      nil ->
        resolve_pending_request(
          %Verdict{
            id: request.id,
            decision: :deny,
            payload: %{"reason" => "egress policy denied", "dest" => dest}
          },
          state
        )

      rule ->
        authorize_broker_rule(request, rule, dest, state)
    end
  end

  defp authorize_broker_rule(%Request{} = request, rule, dest, state) do
    cond do
      truthy?(context_get(rule, "requires_approval", false)) ->
        case policy_payload(rule, dest, state) do
          {:ok, payload} ->
            enrich_pending_broker_request(request, payload, state)

          {:error, reason} ->
            resolve_pending_request(
              %Verdict{
                id: request.id,
                decision: :deny,
                payload: %{"reason" => reason, "dest" => dest}
              },
              state
            )
        end

      true ->
        case policy_payload(rule, dest, state) do
          {:ok, payload} ->
            resolve_pending_request(
              %Verdict{
                id: request.id,
                decision: :authorize,
                payload: payload
              },
              state
            )

          {:error, reason} ->
            resolve_pending_request(
              %Verdict{
                id: request.id,
                decision: :deny,
                payload: %{"reason" => reason, "dest" => dest}
              },
              state
            )
        end
    end
  end

  defp enrich_pending_broker_request(%Request{id: request_id}, payload, state)
       when is_map(payload) do
    case Map.fetch(state.pending, request_id) do
      {:ok, %Request{} = request} ->
        request = %{request | context: merge_context(request.context, payload)}
        %{state | pending: Map.put(state.pending, request_id, request)}

      :error ->
        state
    end
  end

  defp request_dest(%Request{} = request) do
    context = context_map(request.context)
    context_get(context, "dest", context_get(context, "destination", "local:tool_broker"))
  end

  defp policy_matches_dest?(rule, dest) when is_map(rule) do
    rule_dest = context_get(rule, "dest", context_get(rule, "destination"))
    rule_dest in [dest, "*"]
  end

  defp policy_matches_dest?(_rule, _dest), do: false

  defp policy_payload(rule, dest, state) do
    seller_id = launch_seller_id(state.launch_spec)

    payload =
      %{
        "dest" => dest,
        "credential_id" => context_get(rule, "credential_id", context_get(rule, "credential")),
        "seller_id" => seller_id,
        "policy_rule" => context_get(rule, "id", context_get(rule, "dest"))
      }
      |> Enum.reject(fn {_key, value} -> is_nil(value) or value == "" end)
      |> Map.new()

    case Mitos.KeyVault.resolve(payload["credential_id"], dest,
           seller_id: seller_id,
           run_id: state.session_id
         ) do
      {:ok, nil} -> {:ok, payload}
      {:ok, credential_ref} -> {:ok, Map.put(payload, "credential_ref", credential_ref)}
      {:error, reason} -> {:error, reason}
    end
  end

  defp launch_seller_id(%{} = launch_spec) do
    Map.get(launch_spec, :seller_id, Map.get(launch_spec, "seller_id"))
  end

  defp launch_seller_id(_launch_spec), do: nil

  defp launch_value(%{} = launch_spec, key) do
    Map.get(launch_spec, key, Map.get(launch_spec, String.to_atom(key)))
  end

  defp launch_value(_launch_spec, _key), do: nil

  defp resolved_skill_effect(%{} = resolved_skill) do
    Map.take(resolved_skill, ["path", "source", "sha256"])
  end

  defp resolved_skill_effect(_resolved_skill), do: nil

  defp lease_credentials(state) do
    _ = Mitos.KeyVault.lease_run(state.session_id, state.launch_spec || %{})
    state
  end

  defp revoke_credentials(state) do
    _ = Mitos.KeyVault.revoke_run(state.session_id)
    state
  end

  defp configured_egress_policy(%{} = launch_spec) do
    top_level_policy =
      Map.get(launch_spec, :egress_policy, Map.get(launch_spec, "egress_policy", :unset))

    case top_level_policy do
      :unset ->
        launch_spec
        |> Map.get(:policy, Map.get(launch_spec, "policy", %{}))
        |> nested_egress_policy()

      policy ->
        policy
    end
  end

  defp configured_egress_policy(_launch_spec), do: :unset

  defp nested_egress_policy(%{} = policy),
    do: Map.get(policy, :egress_policy, Map.get(policy, "egress_policy", :unset))

  defp nested_egress_policy(_policy), do: :unset

  defp truthy?(value), do: value in [true, "true", 1, "1"]

  defp trace_options(state, opts) do
    [
      run_id: state.session_id,
      launch_spec: state.launch_spec
    ]
    |> maybe_put_option(:base_dir, Keyword.get(opts, :trace_base_dir))
    |> maybe_put_option(:project_id, Keyword.get(opts, :project_id))
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

  defp run_hooks(state, phase) do
    results =
      Mitos.Hooks.run(phase, state.launch_spec,
        cwd: state.cwd || File.cwd!(),
        env: state.env
      )

    state = Enum.reduce(results, state, &trace_hook_result(&2, &1))
    {state, results}
  end

  defp trace_hook_result(state, result) do
    state
    |> trace_effect("hook_executed", %{
      "ref" => %{"phase" => result["phase"], "name" => result["name"]},
      "payload" => Mitos.Hooks.effect_payload(result)
    })
    |> maybe_trace_commit_hook(result)
  end

  defp maybe_trace_commit_hook(state, %{"name" => "commit", "success?" => true} = result) do
    trace_effect(state, "commit", %{
      "ref" =>
        %{
          "hook" => result["name"],
          "sha" => git_output(state.cwd, ["rev-parse", "HEAD"])
        }
        |> reject_blank_values(),
      "payload" => %{
        "phase" => result["phase"],
        "files" =>
          git_lines(state.cwd, ["diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"])
      }
    })
  end

  defp maybe_trace_commit_hook(state, _result), do: state

  defp block_pre_launch(state, pre_hook_results) do
    failed = Mitos.Hooks.failed(pre_hook_results)

    event = %{
      "type" => "HOOK_BLOCKED",
      "phase" => "pre",
      "failed_hooks" => Enum.map(failed, & &1["name"])
    }

    state =
      state
      |> trace_event(event)
      |> trace_effect("capability_finished", %{
        "ref" => %{"error" => "pre_hook_blocked"},
        "payload" => %{
          "status" => "failed",
          "reason" => "pre_hook_blocked",
          "hooks" => %{
            "pre" => Enum.map(pre_hook_results, &Mitos.Hooks.effect_payload/1),
            "post" => []
          },
          "verification" => %{
            "verified" => false,
            "failed_hooks" => Enum.map(failed, & &1["name"])
          }
        }
      })

    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:stop, :normal, %{state | exited?: true}}
  end

  defp maybe_trace_post_hook_verification(state, post_hook_results) do
    failed = Mitos.Hooks.failed(post_hook_results)

    if Mitos.Hooks.blocked?(post_hook_results) do
      trace_effect(state, "verification_recorded", %{
        "ref" => %{"phase" => "post", "tier" => "deterministic_hook"},
        "payload" => %{
          "tier" => "deterministic_hook",
          "verdict" => "unverified",
          "failed_hooks" => Enum.map(failed, & &1["name"])
        }
      })
    else
      state
    end
  end

  defp hook_verification(post_hook_results) do
    failed = Mitos.Hooks.failed(post_hook_results)

    %{
      "verified" => not Mitos.Hooks.blocked?(post_hook_results),
      "failed_hooks" => Enum.map(failed, & &1["name"])
    }
  end

  defp harvest_state(state) do
    {:ok, harvest} =
      Mitos.Harvest.collect(%{
        cwd: state.cwd,
        trace_dir: trace_dir(state),
        schema_root: state.schema_root,
        launch_spec: state.launch_spec || %{}
      })

    {state, harvest}
  end

  defp trace_dir(%{trace: %{dir: dir}}), do: dir
  defp trace_dir(_state), do: nil

  defp git_lines(cwd, args) do
    cwd
    |> git_output(args)
    |> case do
      nil -> []
      output -> output |> String.split("\n", trim: true) |> Enum.reject(&(&1 == ""))
    end
  end

  defp git_output(cwd, args) when is_binary(cwd) do
    case System.cmd("git", ["-C", cwd | args], stderr_to_stdout: true) do
      {output, 0} -> String.trim(output)
      _ -> nil
    end
  rescue
    _ -> nil
  end

  defp git_output(_cwd, _args), do: nil

  defp reject_blank_values(map) do
    map
    |> Enum.reject(fn {_key, value} -> is_nil(value) or value == "" end)
    |> Map.new()
  end

  defp trace_effect(%{opening_decision_seq: nil} = state, _type, _attrs), do: state
  defp trace_effect(%{trace: nil} = state, _type, _attrs), do: state

  defp trace_effect(state, type, attrs) do
    attrs =
      attrs
      |> Map.put("type", type)
      |> Map.put("caused_by", state.opening_decision_seq)

    %{state | trace: Mitos.Trace.append_effect(state.trace, attrs)}
  end

  defp collect_structured_token_usage(state, %{"bytes" => raw_bytes} = event, source_sequence) do
    case structured_bytes(raw_bytes) do
      bytes when is_binary(bytes) ->
        entries =
          bytes
          |> token_usage_events_from_jsonl()
          |> Enum.map(fn usage_event ->
            {token_usage_ref(usage_event, event, source_sequence),
             token_usage_payload(usage_event)}
          end)

        %{state | token_usage_entries: state.token_usage_entries ++ entries}

      nil ->
        state
    end
  end

  defp collect_structured_token_usage(state, _event, _source_sequence), do: state

  defp flush_token_usage(%{token_usage_entries: []} = state), do: state

  defp flush_token_usage(state) do
    state
    |> trace_effect("budget_charged", %{
      "ref" => aggregate_token_usage_ref(state.token_usage_entries),
      "payload" => aggregate_token_usage_payload(state.token_usage_entries)
    })
    |> then(&%{&1 | token_usage_entries: []})
  end

  defp structured_bytes(%Msgpax.Bin{data: bytes}) when is_binary(bytes), do: bytes
  defp structured_bytes(bytes) when is_binary(bytes), do: bytes
  defp structured_bytes(_bytes), do: nil

  defp token_usage_events_from_jsonl(bytes) do
    bytes
    |> String.split(["\n", "\r"], trim: true)
    |> Enum.flat_map(fn line ->
      case Jason.decode(line) do
        {:ok, %{} = event} ->
          event = stringify(event)

          if String.downcase(to_string(event["type"] || "")) == "token_usage" do
            [event]
          else
            []
          end

        _other ->
          []
      end
    end)
  end

  defp token_usage_ref(usage_event, source_event, source_sequence) do
    %{
      "source_sequence" => source_sequence,
      "source_type" => source_event["type"],
      "stream" => source_event["stream"],
      "provider" => usage_event["provider"],
      "model" => usage_event["model"]
    }
    |> reject_blank_values()
  end

  defp token_usage_payload(event) do
    usage =
      case event["payload"] do
        %{} = payload -> stringify(payload)
        _other -> event
      end

    input_tokens = numeric(usage["input_tokens"] || usage["prompt_tokens"])
    output_tokens = numeric(usage["output_tokens"] || usage["completion_tokens"])
    cached_tokens = numeric(usage["cached_tokens"] || usage["cache_read_tokens"])

    tokens =
      numeric(
        usage["tokens"] ||
          usage["total_tokens"] ||
          input_tokens + output_tokens + cached_tokens
      )

    %{
      "billing_basis" => "provider_token_usage",
      "tokens" => tokens,
      "input_tokens" => input_tokens,
      "output_tokens" => output_tokens,
      "cached_tokens" => cached_tokens,
      "usd" => numeric(usage["usd"] || usage["cost_usd"] || map_value(usage["cost"], "usd")),
      "wall_ms" => numeric(usage["wall_ms"])
    }
  end

  defp aggregate_token_usage_ref(usage_entries) do
    refs = Enum.map(usage_entries, fn {ref, _payload} -> ref end)

    %{
      "source_type" => common_ref(refs, "source_type") || "STRUCTURED_OUTPUT",
      "source_sequences" => Enum.flat_map(refs, &List.wrap(&1["source_sequence"])),
      "stream" => common_ref(refs, "stream"),
      "provider" => common_ref(refs, "provider"),
      "model" => common_ref(refs, "model")
    }
    |> Enum.reject(fn {_key, value} -> value in [nil, "", []] end)
    |> Map.new()
  end

  defp aggregate_token_usage_payload(usage_entries) do
    payloads = Enum.map(usage_entries, fn {_ref, payload} -> payload end)

    %{
      "billing_basis" => "provider_token_usage",
      "tokens" => sum_payload(payloads, "tokens"),
      "input_tokens" => sum_payload(payloads, "input_tokens"),
      "output_tokens" => sum_payload(payloads, "output_tokens"),
      "cached_tokens" => sum_payload(payloads, "cached_tokens"),
      "usd" => sum_payload(payloads, "usd"),
      "wall_ms" => sum_payload(payloads, "wall_ms")
    }
  end

  defp common_ref(refs, key) do
    values =
      refs
      |> Enum.map(& &1[key])
      |> Enum.reject(&(&1 in [nil, ""]))
      |> Enum.uniq()

    case values do
      [value] -> value
      _ -> nil
    end
  end

  defp sum_payload(payloads, key), do: Enum.sum(Enum.map(payloads, &numeric(&1[key])))

  defp trace_sequence(%{trace: %{seq: seq}}) when is_integer(seq), do: seq
  defp trace_sequence(_state), do: nil

  defp stringify(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify(value)} end)

  defp stringify(list) when is_list(list), do: Enum.map(list, &stringify/1)
  defp stringify(value), do: value

  defp map_value(%{} = map, key), do: Map.get(map, key, Map.get(map, String.to_atom(key)))
  defp map_value(_map, _key), do: nil

  defp numeric(value) when is_integer(value) or is_float(value), do: value

  defp numeric(value) when is_binary(value) do
    case Float.parse(value) do
      {number, ""} -> number
      _ -> 0
    end
  end

  defp numeric(_value), do: 0

  defp trace_authority_timeout_effects(state, %Request{origin: :authority_socket} = request) do
    state =
      trace_effect(state, "approval_resolved", %{
        "ref" => %{"request_id" => request.id, "method" => request.method},
        "payload" => %{
          "decision" => "deny",
          "reason" => "timeout",
          "result" => %{"error" => "timeout"}
        }
      })

    if request.method == "tool_broker" do
      trace_effect(state, "egress_denied", %{
        "ref" => egress_ref(request),
        "payload" => %{"reason" => "approval timeout"}
      })
    else
      state
    end
  end

  defp trace_authority_timeout_effects(state, _request), do: state

  defp maybe_trace_launch_failure(state, %{"kind" => "launch"} = event) do
    state
    |> trace_effect("capability_finished", %{
      "ref" => %{"error" => event["kind"]},
      "payload" => %{
        "status" => "failed",
        "detail" => event["detail"]
      }
    })
    |> revoke_credentials()
  end

  defp maybe_trace_launch_failure(state, _event), do: state

  defp maybe_trace_authority_request_effect(
         state,
         %Request{origin: :authority_socket} = request
       ) do
    trace_effect(state, "approval_requested", %{
      "ref" => %{"request_id" => request.id, "method" => request.method},
      "payload" => %{
        "action" => request.action,
        "context" => request.context
      }
    })
  end

  defp maybe_trace_authority_request_effect(state, _request), do: state

  defp maybe_trace_authority_verdict_effect(
         state,
         %Request{origin: :authority_socket} = request,
         %Verdict{} = verdict,
         payload
       ) do
    trace_effect(state, "approval_resolved", %{
      "ref" => %{"request_id" => request.id, "method" => request.method},
      "payload" => %{
        "decision" => decision_string(verdict.decision),
        "reason" => verdict.reason,
        "result" => payload
      }
    })
  end

  defp maybe_trace_authority_verdict_effect(state, _request, _verdict, _payload), do: state

  defp exit_status(%{"code" => 0}), do: "completed"
  defp exit_status(%{"code" => code}) when is_integer(code), do: "failed"
  defp exit_status(%{"signal" => signal}) when is_integer(signal), do: "failed"
  defp exit_status(_event), do: "interrupted"

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
  defp encode_env(%{} = env) when map_size(env) == 0, do: default_env()
  defp encode_env(%{} = env), do: Map.merge(default_env(), env)

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
