defmodule Mitos.Web.TerminalSocket do
  @moduledoc """
  Cowboy WebSocket handler bridging a browser xterm.js terminal to one
  `Mitos.Session`.

  Wire contract (per direction):

  Server -> Browser
    * binary frame  = raw PTY output; xterm.js calls `term.write(bytes)`
    * text frame    = JSON `{"type":"event",  "event":  %{...}}`
                          or `{"type":"snapshot","snapshot":%{...}}`

  Browser -> Server
    * binary frame  = keystrokes / paste, forwarded verbatim to `write_stdin`
    * text frame    = JSON command, currently:
                      `{"type":"resize", "cols":n, "rows":m}`

  All RAW forwarding is opaque — xterm.js is the VT parser. EVENT/SNAPSHOT
  go as text so the page's JS can branch without sniffing bytes. (This is the
  *display* path; the orchestration brain consumes the structured streams
  directly from the Session via PubSub, not via this WS.)
  """
  @behaviour :cowboy_websocket

  require Logger

  @impl true
  def init(req, _state) do
    session_id = :cowboy_req.binding(:session_id, req)

    case session_id do
      :undefined -> {:cowboy_websocket, req, %{error: :no_session_id}}
      id -> {:cowboy_websocket, req, %{session_id: id}}
    end
  end

  @impl true
  def websocket_init(%{error: :no_session_id} = state) do
    {[{:close, 1008, "missing session id"}], state}
  end

  def websocket_init(%{session_id: session_id} = state) do
    case Registry.lookup(Mitos.SessionRegistry, session_id) do
      [{pid, _}] ->
        Mitos.PubSub.subscribe(session_id)
        replay = Mitos.Session.replay(pid)

        frames = if byte_size(replay) > 0, do: [{:binary, replay}], else: []
        {frames, Map.put(state, :session_pid, pid)}

      [] ->
        {[{:close, 1008, "unknown session"}], state}
    end
  end

  # -- inbound (browser -> server) -----------------------------------------

  @impl true
  def websocket_handle({:binary, bytes}, state) do
    Mitos.Session.write_stdin(state.session_pid, bytes)
    {[], state}
  end

  def websocket_handle({:text, text}, state) do
    case Jason.decode(text) do
      {:ok, %{"type" => "resize", "cols" => cols, "rows" => rows}}
      when is_integer(cols) and is_integer(rows) and cols > 0 and rows > 0 ->
        Mitos.Session.resize(state.session_pid, cols, rows)
        {[], state}

      {:ok, %{"type" => "stdin", "bytes" => bytes}} when is_binary(bytes) ->
        Mitos.Session.write_stdin(state.session_pid, bytes)
        {[], state}

      {:ok, %{"type" => "signal", "signum" => signum}} when is_integer(signum) ->
        Mitos.Session.signal(state.session_pid, signum)
        {[], state}

      _ ->
        {[], state}
    end
  end

  def websocket_handle({:ping, payload}, state), do: {[{:pong, payload}], state}
  def websocket_handle(_frame, state), do: {[], state}

  # -- outbound (PubSub -> browser) ----------------------------------------

  @impl true
  def websocket_info({:mitos, session_id, payload}, %{session_id: session_id} = state) do
    case payload do
      {:raw, bytes} -> {[{:binary, bytes}], state}
      {:event, event} -> {[{:text, Jason.encode!(%{type: "event", event: event})}], state}
      {:snapshot, snap} -> {[{:text, Jason.encode!(%{type: "snapshot", snapshot: snap})}], state}
      _ -> {[], state}
    end
  end

  def websocket_info(_msg, state), do: {[], state}

  @impl true
  def terminate(_reason, _req, _state), do: :ok
end
