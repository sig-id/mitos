defmodule Mitos.Session do
  @moduledoc """
  One BEAM process per agent session. Owns a `mitos-term` OS process via a
  Port and bridges between it and Phoenix subscribers (browser WS clients,
  prompt detectors, the orchestration brain).

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
      {:mitos, session_id, {:event, %{...}}}     # title/bell/idle/exit/error
      {:mitos, session_id, {:snapshot, %{...}}}  # on-demand grid snapshot

  New subscribers get a `{:mitos, session_id, {:replay, bytes}}` on attach to
  catch up the current screen.
  """
  use GenServer
  require Logger

  alias Mitos.Sidecar.Protocol

  # 256 KiB of replayable PTY output per session. Browser tabs that connect
  # late get this much history. Larger than xterm.js's own buffer; the brain
  # can ask for `snapshot` for a structured view.
  @scrollback_cap 256 * 1024

  defstruct [
    :session_id,
    :port,
    :sidecar_path,
    :argv,
    :cwd,
    :env,
    :cols,
    :rows,
    :idle_ms,
    :snapshot_waiter,
    backend: :unknown,
    scrollback: <<>>,
    exited?: false
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
    state = %__MODULE__{
      session_id: Keyword.fetch!(opts, :session_id),
      sidecar_path: Keyword.fetch!(opts, :sidecar_path),
      argv: Keyword.fetch!(opts, :argv),
      cwd: opts[:cwd],
      env: opts[:env],
      cols: Keyword.get(opts, :cols, 80),
      rows: Keyword.get(opts, :rows, 24),
      idle_ms: Keyword.get(opts, :idle_ms, 250)
    }

    Process.flag(:trap_exit, true)
    {:ok, state, {:continue, :open_port}}
  end

  @impl true
  def handle_continue(:open_port, state) do
    port =
      Port.open({:spawn_executable, state.sidecar_path},
        # packet: 4 → 4-byte big-endian length frame, exactly what the sidecar
        # speaks. :binary keeps payloads as binaries (not charlists).
        # :exit_status forwards the OS exit code as a message when the
        # sidecar dies, so we can mirror it as an `:exit` event.
        [:binary, :exit_status, packet: 4]
      )

    send_control(port, Protocol.hello())

    send_control(
      port,
      Protocol.open(state.argv,
        cwd: state.cwd,
        env: encode_env(state.env),
        cols: state.cols,
        rows: state.rows,
        idle_ms: state.idle_ms
      )
    )

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
        state = reply_snapshot(state, {:ok, snap})
        Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:snapshot, snap}})
        {:noreply, state}

      {:unknown, tag, _} ->
        Logger.warning("mitos session #{inspect(state.session_id)} got unknown frame tag 0x#{Integer.to_string(tag, 16)}")
        {:noreply, state}
    end
  end

  # The sidecar exited (the binary itself, not the agent inside it). Treat
  # this as terminal — the supervisor decides whether to restart us.
  def handle_info({port, {:exit_status, code}}, %{port: port} = state) do
    Logger.info("mitos session #{inspect(state.session_id)} sidecar exited code=#{code}")
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, %{"type" => "sidecar_exit", "code" => code}}})

    state = reply_snapshot(state, {:error, :sidecar_exited})
    {:stop, :normal, %{state | port: nil, exited?: true}}
  end

  def handle_info({:EXIT, port, reason}, %{port: port} = state) do
    state = reply_snapshot(state, {:error, :sidecar_exited})
    {:stop, reason, %{state | port: nil, exited?: true}}
  end

  def handle_info(_other, state), do: {:noreply, state}

  @impl true
  def handle_call(:session_id, _from, state), do: {:reply, state.session_id, state}

  def handle_call(:replay, _from, state), do: {:reply, state.scrollback, state}

  def handle_call(:snapshot, from, state) do
    case state.snapshot_waiter do
      nil ->
        send_control(state.port, Protocol.snapshot_cmd())
        {:noreply, %{state | snapshot_waiter: from}}

      _ ->
        {:reply, {:error, :snapshot_in_flight}, state}
    end
  end

  @impl true
  def handle_cast({:write_stdin, bytes}, state) do
    send_control(state.port, Protocol.write_stdin(bytes))
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

  def handle_cast({:shutdown, grace_ms}, state) do
    if state.port, do: send_control(state.port, Protocol.shutdown(grace_ms))
    {:noreply, state}
  end

  @impl true
  def terminate(_reason, state) do
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
  end

  defp handle_event(%{"type" => "hello", "backend" => backend} = event, state) do
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, %{state | backend: String.to_atom(backend)}}
  end

  defp handle_event(%{"type" => "exit"} = event, state) do
    # The sidecar reports the agent's exit *and then* exits itself; we don't
    # have to wait for the Port `:exit_status` to come around. Stopping here
    # keeps the Session lifetime tight against the agent process lifetime.
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    state = reply_snapshot(state, {:error, :exited})
    {:stop, :normal, %{state | exited?: true}}
  end

  defp handle_event(%{"type" => "error"} = event, state) do
    Logger.warning("mitos session #{inspect(state.session_id)} sidecar error: #{inspect(event)}")
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, state}
  end

  defp handle_event(event, state) do
    Mitos.PubSub.broadcast(state.session_id, {:mitos, state.session_id, {:event, event}})
    {:noreply, state}
  end

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
  # agents emit ANSI color (matches the webpty fix in src/web/webpty.zig).
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
