defmodule Mitos.Authority do
  @moduledoc """
  Run-scoped authority/callback plane exposed over a Unix-domain socket.

  Agents receive the returned `authority` map in their `LaunchSpec` and use
  `OD_*` env vars injected by the engine to call back into the control plane.
  The wire shape is intentionally small for now: one line-delimited JSON
  request per line, including the run-scoped token.
  """

  use GenServer
  require Logger

  defstruct [
    :run_id,
    :project_id,
    :token,
    :socket_path,
    :listen_socket,
    :acceptor,
    :session,
    next_action_id: 1
  ]

  @request_methods ~w(tool_broker approval artifact_register memory_fetch subrun_spawn)

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts)
  end

  def launch_spec(server), do: GenServer.call(server, :launch_spec)
  def stop(server), do: GenServer.stop(server, :normal)

  @impl true
  def init(opts) do
    run_id = Keyword.fetch!(opts, :run_id)
    project_id = Keyword.get(opts, :project_id, "default")
    socket_path = Keyword.get_lazy(opts, :socket_path, fn -> default_socket_path(run_id) end)
    token = Keyword.get_lazy(opts, :token, &mint_token/0)
    session = Keyword.fetch!(opts, :session)

    File.rm(socket_path)

    {:ok, listen_socket} =
      :gen_tcp.listen(0, [
        :binary,
        packet: :line,
        active: false,
        ip: {:local, socket_path}
      ])

    owner = self()
    {:ok, acceptor} = Task.start_link(fn -> accept_loop(listen_socket, owner) end)

    {:ok,
     %__MODULE__{
       run_id: run_id,
       project_id: project_id,
       token: token,
       socket_path: socket_path,
       listen_socket: listen_socket,
       acceptor: acceptor,
       session: session
     }}
  end

  @impl true
  def handle_call(:launch_spec, _from, state) do
    {:reply,
     %{
       socket_path: state.socket_path,
       token: state.token,
       project_id: state.project_id,
       run_id: state.run_id
     }, state}
  end

  def handle_call({:request, request}, _from, state) do
    {reply, state} = handle_request(request, state)
    {:reply, reply, state}
  end

  @impl true
  def terminate(_reason, state) do
    if state.listen_socket, do: :gen_tcp.close(state.listen_socket)
    if state.acceptor, do: Process.exit(state.acceptor, :normal)
    if state.socket_path, do: File.rm(state.socket_path)
    :ok
  end

  defp handle_request(%{"token" => token} = request, %{token: token} = state) do
    method = Map.get(request, "method", "tool_broker")

    cond do
      method not in @request_methods ->
        {%{"ok" => false, "error" => "unknown_method"}, state}

      method in ["artifact_register", "memory_fetch", "subrun_spawn"] ->
        {%{"ok" => false, "error" => "not_implemented", "method" => method}, state}

      true ->
        action_id = "authority-#{state.next_action_id}"
        context = Map.get(request, "context", %{})

        event = %{
          "type" => "ACTION_REQUESTED",
          "action_id" => action_id,
          "action" => Map.get(request, "action", method),
          "context" => context_to_text(context),
          "context_json" => context,
          "source" => "authority",
          "method" => method
        }

        send(state.session, {:authority_event, event})

        {%{"ok" => true, "action_id" => action_id, "status" => "requested"},
         %{state | next_action_id: state.next_action_id + 1}}
    end
  end

  defp handle_request(%{"token" => _bad_token}, state),
    do: {%{"ok" => false, "error" => "unauthorized"}, state}

  defp handle_request(_request, state), do: {%{"ok" => false, "error" => "missing_token"}, state}

  defp accept_loop(listen_socket, owner) do
    case :gen_tcp.accept(listen_socket) do
      {:ok, socket} ->
        handle_socket(socket, owner)
        accept_loop(listen_socket, owner)

      {:error, :closed} ->
        :ok

      {:error, reason} ->
        Logger.warning("authority accept failed: #{inspect(reason)}")
        accept_loop(listen_socket, owner)
    end
  end

  defp handle_socket(socket, owner) do
    case :gen_tcp.recv(socket, 0, 5_000) do
      {:ok, line} ->
        reply =
          line
          |> String.trim()
          |> decode_request()
          |> request_reply(owner)

        :gen_tcp.send(socket, Jason.encode!(reply) <> "\n")
        handle_socket(socket, owner)

      {:error, :closed} ->
        :gen_tcp.close(socket)

      {:error, :timeout} ->
        :gen_tcp.close(socket)

      {:error, reason} ->
        Logger.warning("authority recv failed: #{inspect(reason)}")
        :gen_tcp.close(socket)
    end
  end

  defp decode_request(""), do: {:error, "empty_request"}

  defp decode_request(line) do
    case Jason.decode(line) do
      {:ok, %{} = request} -> {:ok, request}
      {:ok, _} -> {:error, "request_must_be_object"}
      {:error, _} -> {:error, "invalid_json"}
    end
  end

  defp request_reply({:ok, request}, owner), do: GenServer.call(owner, {:request, request}, 5_000)
  defp request_reply({:error, error}, _owner), do: %{"ok" => false, "error" => error}

  defp context_to_text(context) when is_binary(context), do: context
  defp context_to_text(context), do: Jason.encode!(context)

  defp default_socket_path(run_id) do
    safe_id = Regex.replace(~r/[^A-Za-z0-9_.-]/, run_id, "_")
    Path.join(System.tmp_dir!(), "mitos-authority-#{safe_id}.sock")
  end

  defp mint_token do
    32
    |> :crypto.strong_rand_bytes()
    |> Base.url_encode64(padding: false)
  end
end
