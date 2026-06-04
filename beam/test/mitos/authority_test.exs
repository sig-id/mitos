defmodule Mitos.AuthorityTest do
  use ExUnit.Case, async: false

  alias Mitos.Authority.Request

  defmodule FakeSession do
    use GenServer

    def start_link(parent), do: GenServer.start_link(__MODULE__, parent)

    @impl true
    def init(parent), do: {:ok, parent}

    @impl true
    def handle_cast({:authority_request, %Request{} = request}, parent) do
      send(parent, {:authority_request, request})
      {:noreply, parent}
    end
  end

  test "parks run-scoped socket requests until the session replies" do
    run_id = "auth-" <> Integer.to_string(System.unique_integer([:positive]))
    {:ok, session} = FakeSession.start_link(self())

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: run_id,
        project_id: "proj",
        session: session
      )

    on_exit(fn -> stop_authority(authority) end)

    spec = Mitos.Authority.launch_spec(authority)
    assert spec.run_id == run_id
    assert spec.project_id == "proj"
    assert is_binary(spec.socket_path)
    assert is_binary(spec.token)
    assert "approval" in spec.methods
    assert is_integer(spec.expires_at_ms)

    unauthorized =
      authority_request(spec.socket_path, %{
        "method" => "approval",
        "token" => "wrong"
      })

    assert unauthorized == %{"ok" => false, "error" => "unauthorized"}

    task =
      Task.async(fn ->
        authority_request(spec.socket_path, %{
          "method" => "approval",
          "token" => spec.token,
          "action" => "run_command",
          "context" => %{"command" => "mix test"}
        })
      end)

    assert_receive {:authority_request,
                    %Request{
                      id: nil,
                      origin: :authority_socket,
                      method: "approval",
                      action: "run_command",
                      context: %{"command" => "mix test"},
                      from: from
                    }},
                   1_000

    GenServer.reply(from, %{
      "ok" => true,
      "request_id" => "request-1",
      "decision" => "authorize",
      "payload" => %{"approved_by" => "test"}
    })

    assert Task.await(task, 1_000) == %{
             "ok" => true,
             "request_id" => "request-1",
             "decision" => "authorize",
             "payload" => %{"approved_by" => "test"}
           }
  end

  test "attenuated child tokens cannot exceed parent method scope" do
    {:ok, session} = FakeSession.start_link(self())

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: "attenuate-" <> Integer.to_string(System.unique_integer([:positive])),
        project_id: "proj",
        session: session
      )

    on_exit(fn -> stop_authority(authority) end)

    spec = Mitos.Authority.launch_spec(authority)
    assert {:ok, child} = Mitos.Authority.attenuate_token(authority, spec.token, methods: ["approval"])
    assert child.project_id == "proj"
    assert child.methods == ["approval"]

    assert {:error, :scope_escalation} =
             Mitos.Authority.attenuate_token(authority, child.token, methods: ["approval", "memory_fetch"])

    reply =
      authority_request(spec.socket_path, %{
        "method" => "memory_fetch",
        "token" => child.token,
        "context" => %{"key" => "notes"}
      })

    assert reply == %{"ok" => false, "error" => "unauthorized"}
  end

  test "socket requests receive a typed timeout when no verdict arrives" do
    {:ok, session} = FakeSession.start_link(self())

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: "timeout-" <> Integer.to_string(System.unique_integer([:positive])),
        project_id: "proj",
        session: session
      )

    on_exit(fn -> stop_authority(authority) end)
    spec = Mitos.Authority.launch_spec(authority)

    task =
      Task.async(fn ->
        authority_request(
          spec.socket_path,
          %{"method" => "approval", "token" => spec.token, "context" => %{}},
          6_000
        )
      end)

    assert_receive {:authority_request, %Request{}}, 1_000
    assert %{"ok" => false, "error" => "timeout"} = Task.await(task, 7_000)
  end

  defp authority_request(socket_path, request, timeout \\ 1_000) do
    {:ok, socket} =
      :gen_tcp.connect({:local, socket_path}, 0, [
        :binary,
        packet: :line,
        active: false
      ])

    :ok = :gen_tcp.send(socket, Jason.encode!(request) <> "\n")
    {:ok, line} = :gen_tcp.recv(socket, 0, timeout)
    :gen_tcp.close(socket)
    Jason.decode!(String.trim(line))
  end

  defp stop_authority(authority) do
    try do
      Mitos.Authority.stop(authority)
    catch
      :exit, _ -> :ok
    end
  end
end
