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

  alias Mitos.Authority.Request

  defstruct [
    :run_id,
    :project_id,
    :token,
    :socket_path,
    :listen_socket,
    :acceptor,
    :session,
    :token_ttl_ms,
    tokens: %{}
  ]

  @request_methods ~w(tool_broker approval artifact_register memory_fetch memory_put subrun_spawn subtask_return task_emit)
  @socket_timeout_ms 5_000
  @default_token_ttl_ms 60 * 60 * 1_000

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts)
  end

  def launch_spec(server), do: GenServer.call(server, :launch_spec)

  def attenuate_token(server, parent_token, opts \\ []),
    do: GenServer.call(server, {:attenuate_token, parent_token, opts})

  def stop(server), do: GenServer.stop(server, :normal)

  @impl true
  def init(opts) do
    run_id = Keyword.fetch!(opts, :run_id)
    project_id = Keyword.get(opts, :project_id, "default")
    socket_path = Keyword.get_lazy(opts, :socket_path, fn -> default_socket_path(run_id) end)
    token = Keyword.get_lazy(opts, :token, &mint_token/0)
    token_ttl_ms = Keyword.get(opts, :token_ttl_ms, @default_token_ttl_ms)
    expires_at_ms = System.system_time(:millisecond) + token_ttl_ms
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
       session: session,
       token_ttl_ms: token_ttl_ms,
       tokens: %{
         token => %{
           run_id: run_id,
           project_id: project_id,
           methods: MapSet.new(@request_methods),
           expires_at_ms: expires_at_ms
         }
       }
     }}
  end

  @impl true
  def handle_call(:launch_spec, _from, state) do
    scope = Map.fetch!(state.tokens, state.token)

    {:reply,
     %{
       socket_path: state.socket_path,
       token: state.token,
       project_id: state.project_id,
       run_id: state.run_id,
       methods: @request_methods,
       expires_at_ms: scope.expires_at_ms
     }, state}
  end

  def handle_call({:attenuate_token, parent_token, opts}, _from, state) do
    {reply, state} = attenuate(parent_token, opts, state)
    {:reply, reply, state}
  end

  def handle_call({:request, request}, from, state) do
    case handle_request(request, from, state) do
      {:reply, reply} ->
        {:reply, reply, state}

      :noreply ->
        {:noreply, state}
    end
  end

  @impl true
  def terminate(_reason, state) do
    if state.listen_socket, do: :gen_tcp.close(state.listen_socket)
    if state.acceptor, do: Process.exit(state.acceptor, :normal)
    if state.socket_path, do: File.rm(state.socket_path)
    :ok
  end

  defp handle_request(%{"token" => token} = request, from, state) do
    method = Map.get(request, "method", "tool_broker")

    with :ok <- known_method(method),
         {:ok, scope} <- verify_token(token, state),
         :ok <- token_allows_method(scope, method) do
      GenServer.cast(
        state.session,
        {:authority_request,
         %Request{
           method: method,
           action: Map.get(request, "action", method),
           context: normalize_context(Map.get(request, "context", %{})),
           origin: :authority_socket,
           from: from
         }}
      )

      :noreply
    else
      {:error, error} -> {:reply, %{"ok" => false, "error" => error}}
    end
  end

  defp handle_request(_request, _from, _state),
    do: {:reply, %{"ok" => false, "error" => "missing_token"}}

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
        :gen_tcp.send(socket, Jason.encode!(%{"ok" => false, "error" => "timeout"}) <> "\n")
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

  defp request_reply({:ok, request}, owner) do
    GenServer.call(owner, {:request, request}, @socket_timeout_ms)
  catch
    :exit, {:timeout, _} -> %{"ok" => false, "error" => "timeout"}
    :exit, _ -> %{"ok" => false, "error" => "authority_unavailable"}
  end

  defp request_reply({:error, error}, _owner), do: %{"ok" => false, "error" => error}

  defp attenuate(parent_token, opts, state) do
    with {:ok, parent_scope} <- verify_token(parent_token, state),
         {:ok, methods} <- attenuated_methods(opts, parent_scope),
         {:ok, project_id} <- attenuated_project(opts, parent_scope) do
      ttl_ms = Keyword.get(opts, :ttl_ms, state.token_ttl_ms)
      expires_at_ms = min(parent_scope.expires_at_ms, System.system_time(:millisecond) + ttl_ms)
      child_token = mint_token()

      child_scope = %{
        run_id: parent_scope.run_id,
        project_id: project_id,
        methods: methods,
        expires_at_ms: expires_at_ms
      }

      reply =
        {:ok,
         %{
           token: child_token,
           run_id: child_scope.run_id,
           project_id: child_scope.project_id,
           methods: MapSet.to_list(child_scope.methods),
           expires_at_ms: child_scope.expires_at_ms
         }}

      {reply, %{state | tokens: Map.put(state.tokens, child_token, child_scope)}}
    else
      {:error, reason} -> {{:error, reason}, state}
    end
  end

  defp attenuated_methods(opts, parent_scope) do
    requested =
      opts
      |> Keyword.get(:methods, MapSet.to_list(parent_scope.methods))
      |> Enum.map(&to_string/1)
      |> MapSet.new()

    cond do
      MapSet.size(requested) == 0 ->
        {:error, :empty_method_set}

      not MapSet.subset?(requested, parent_scope.methods) ->
        {:error, :scope_escalation}

      true ->
        {:ok, requested}
    end
  end

  defp attenuated_project(opts, parent_scope) do
    project_id = Keyword.get(opts, :project_id, parent_scope.project_id)

    if project_id == parent_scope.project_id do
      {:ok, project_id}
    else
      {:error, :scope_escalation}
    end
  end

  defp known_method(method) when method in @request_methods, do: :ok
  defp known_method(_method), do: {:error, "unknown_method"}

  defp verify_token(token, state) do
    now_ms = System.system_time(:millisecond)

    case Map.fetch(state.tokens, token) do
      {:ok, %{expires_at_ms: expires_at_ms} = scope} when expires_at_ms > now_ms -> {:ok, scope}
      {:ok, _expired} -> {:error, "unauthorized"}
      :error -> {:error, "unauthorized"}
    end
  end

  defp token_allows_method(scope, method) do
    if MapSet.member?(scope.methods, method) do
      :ok
    else
      {:error, "unauthorized"}
    end
  end

  defp normalize_context(%{} = context), do: context
  defp normalize_context(context) when is_binary(context), do: context
  defp normalize_context(context), do: %{"value" => context}

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
