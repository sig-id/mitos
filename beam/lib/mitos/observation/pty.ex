defmodule Mitos.Observation.Pty do
  @moduledoc """
  A local PTY byte source for channel C3 (`14 §3`).

  `01 §7` assigns local transports to the engine: it owns the pty, the parser
  feed, and the raw spool. This module is the BEAM-side realization of that
  seam — enough of a transport to drive `Mitos.Observation.Terminal` and
  `Mitos.Steer` against a real interactive process, so the channel is
  demonstrable and testable without the external engine.

  The pty itself comes from `script(1)`, which allocates one, relays the child's
  output to its stdout, and relays its own stdin into the pty. That gives both
  halves the design needs: a byte stream to spool and parse, and a write channel
  for a verified steer.

  It is a **process that owns its port and drains it continuously**, which is
  not an implementation detail: a steer blocks its caller while it waits for the
  after-edge (`14 §3.3`), so if the caller were also the pump, the bytes proving
  the transition arrived would be the bytes the caller is not reading. The port
  owner drains; everyone else asks the `Terminal`.

  **This is not the structured-observation path.** A process under a pty writes
  a *display*, so its stdout is not parseable as a protocol — which is the whole
  reason `14 §1` puts run semantics on C1/C2 and leaves C3 the single bounded
  question of modal state. A caller that opens a pty gets its semantics from the
  session-artifact tail (`Mitos.Observation.Session`), never from these bytes.
  """

  use GenServer

  alias Mitos.Observation.Terminal

  # --- client ---

  @doc """
  Launch `exe` with `args` under a pty, wiring its bytes into a `Terminal`.

  Options: `run_id`, `stage_id`, `clock`, `env`, `cwd`, `rows`, `cols`,
  `thresholds`, `terminal` (attach to an existing one), `on_exit`.

  Returns `{:error, :pty_unavailable}` where `script(1)` is absent — a caller
  must degrade to C1+C2 rather than assume a terminal exists (`14 §4`).
  """
  def open(exe, args, opts \\ []) do
    case find_script() do
      {:ok, _} -> GenServer.start_link(__MODULE__, {exe, args, opts})
      {:error, reason} -> {:error, reason}
    end
  end

  @doc "The `Terminal` this session feeds — where snapshots come from."
  def terminal(pty), do: GenServer.call(pty, :terminal)

  @doc """
  The write half — a plain byte write to the pty master. Stable in a way output
  parsing is not, which is why `14 §3.3` puts control here and semantics
  elsewhere. Prefer `Mitos.Steer`: a bare write is the fire-and-forget keystroke
  the design exists to avoid.
  """
  def write(pty, bytes), do: GenServer.call(pty, {:write, bytes})

  @doc "A `write` function shaped for `Mitos.Steer.apply/1`."
  def writer(pty), do: fn bytes -> write(pty, bytes) end

  @doc "Exit status of the child, or `nil` while it runs."
  def exit_status(pty), do: GenServer.call(pty, :exit_status)

  @doc "Bytes read from the pty so far."
  def bytes_read(pty), do: GenServer.call(pty, :bytes_read)

  def close(pty) do
    if Process.alive?(pty), do: GenServer.stop(pty, :normal, 5_000)
    :ok
  catch
    :exit, _ -> :ok
  end

  @doc "Is a local pty transport available on this host?"
  def available?, do: match?({:ok, _}, find_script())

  @doc """
  A steer context for this session (`14 §3.3`), ready for `Mitos.Steer.apply/1`.
  """
  def steer_ctx(pty, command, opts \\ []) do
    %{
      terminal: terminal(pty),
      clock: opts[:clock],
      classifier: opts[:classifier],
      command: command,
      stage_id: opts[:stage_id],
      thresholds: opts[:thresholds] || %{},
      settle_ms: opts[:settle_ms],
      append: opts[:append],
      caused_by: opts[:caused_by],
      write: writer(pty)
    }
  end

  # --- server ---

  @impl true
  def init({exe, args, opts}) do
    {:ok, script} = find_script()
    {:ok, terminal} = terminal_for(opts)
    command = Enum.map_join([exe | args], " ", &shell_quote/1)

    port =
      Port.open({:spawn_executable, script}, [
        :binary,
        :exit_status,
        :hide,
        args: script_args(command),
        cd: opts[:cwd] || File.cwd!(),
        env: port_env(opts[:env] || %{})
      ])

    {:ok,
     %{
       port: port,
       terminal: terminal,
       own_terminal?: is_nil(opts[:terminal]),
       on_exit: opts[:on_exit],
       exit_status: nil,
       bytes: 0
     }}
  end

  @impl true
  def handle_call(:terminal, _from, st), do: {:reply, st.terminal, st}
  def handle_call(:exit_status, _from, st), do: {:reply, st.exit_status, st}
  def handle_call(:bytes_read, _from, st), do: {:reply, st.bytes, st}

  def handle_call({:write, bytes}, _from, st) do
    reply =
      try do
        Port.command(st.port, bytes)
        :ok
      catch
        :error, :badarg -> {:error, :closed}
      end

    {:reply, reply, st}
  end

  @impl true
  def handle_info({port, {:data, data}}, %{port: port} = st) do
    # Every chunk both spools and advances the parser, and bumps the quiescence
    # clock through the Terminal (14 §2). Nothing is interpreted here.
    Terminal.feed(st.terminal, data)
    {:noreply, %{st | bytes: st.bytes + byte_size(data)}}
  end

  def handle_info({port, {:exit_status, status}}, %{port: port} = st) do
    if st.on_exit, do: st.on_exit.(status)
    {:noreply, %{st | exit_status: status}}
  end

  def handle_info(_msg, st), do: {:noreply, st}

  @impl true
  def terminate(_reason, st) do
    if Port.info(st.port), do: Port.close(st.port)
    if st.own_terminal? and Process.alive?(st.terminal), do: GenServer.stop(st.terminal, :normal, 1_000)
    :ok
  catch
    _, _ -> :ok
  end

  # --- internals ---

  defp find_script do
    case System.find_executable("script") do
      nil -> {:error, :pty_unavailable}
      path -> {:ok, path}
    end
  end

  # util-linux: `script -q -e -c CMD /dev/null`. BSD/macOS: `script -q /dev/null CMD...`.
  defp script_args(command) do
    if :os.type() == {:unix, :darwin} do
      ["-q", "/dev/null", "/bin/sh", "-c", command]
    else
      ["-q", "-e", "-c", command, "/dev/null"]
    end
  end

  defp terminal_for(opts) do
    case opts[:terminal] do
      nil ->
        Terminal.start_link(
          run_id: opts[:run_id],
          stage_id: opts[:stage_id],
          clock: opts[:clock],
          rows: opts[:rows],
          cols: opts[:cols],
          thresholds: opts[:thresholds] || %{}
        )

      pid ->
        {:ok, pid}
    end
  end

  defp port_env(env) do
    env
    |> Map.put_new("TERM", "xterm-256color")
    |> Enum.map(fn
      {k, nil} -> {String.to_charlist(k), false}
      {k, v} -> {String.to_charlist(k), String.to_charlist(to_string(v))}
    end)
  end

  defp shell_quote(arg) do
    "'" <> String.replace(to_string(arg), "'", "'\\''") <> "'"
  end
end
