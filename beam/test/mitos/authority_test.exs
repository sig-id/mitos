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

    assert {:ok, child} =
             Mitos.Authority.attenuate_token(authority, spec.token, methods: ["approval"])

    assert child.project_id == "proj"
    assert child.methods == ["approval"]

    assert {:error, :scope_escalation} =
             Mitos.Authority.attenuate_token(authority, child.token,
               methods: ["approval", "memory_fetch"]
             )

    reply =
      authority_request(spec.socket_path, %{
        "method" => "memory_fetch",
        "token" => child.token,
        "context" => %{"key" => "notes"}
      })

    assert reply == %{"ok" => false, "error" => "unauthorized"}
  end

  test "socket path is owner-only and removed on stop" do
    {:ok, session} = FakeSession.start_link(self())
    socket_path = tmp_socket_path("private")
    on_exit(fn -> File.rm(socket_path) end)

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: "private-" <> Integer.to_string(System.unique_integer([:positive])),
        project_id: "proj",
        session: session,
        socket_path: socket_path
      )

    stat = File.lstat!(socket_path)
    assert stat.type == :other
    assert Bitwise.band(stat.mode, 0o777) == 0o600

    Mitos.Authority.stop(authority)
    refute File.exists?(socket_path)
  end

  test "startup refuses to unlink a non-socket file at socket path" do
    {:ok, session} = FakeSession.start_link(self())
    socket_path = tmp_socket_path("regular")
    File.write!(socket_path, "do not remove")
    on_exit(fn -> File.rm(socket_path) end)

    previous_trap_exit = Process.flag(:trap_exit, true)

    try do
      assert {:error, {:socket_path_exists, ^socket_path}} =
               Mitos.Authority.start_link(
                 run_id: "regular-" <> Integer.to_string(System.unique_integer([:positive])),
                 project_id: "proj",
                 session: session,
                 socket_path: socket_path
               )
    after
      Process.flag(:trap_exit, previous_trap_exit)
    end

    assert File.read!(socket_path) == "do not remove"
  end

  test "stop does not unlink a replacement socket at the same path" do
    {:ok, session} = FakeSession.start_link(self())
    socket_path = tmp_socket_path("replacement")
    on_exit(fn -> File.rm(socket_path) end)

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: "replacement-" <> Integer.to_string(System.unique_integer([:positive])),
        project_id: "proj",
        session: session,
        socket_path: socket_path
      )

    original = File.lstat!(socket_path)
    assert original.type == :other
    :ok = File.rm(socket_path)

    {:ok, replacement_socket} =
      :gen_tcp.listen(0, [
        :binary,
        packet: :line,
        active: false,
        ip: {:local, socket_path}
      ])

    on_exit(fn -> :gen_tcp.close(replacement_socket) end)
    replacement = File.lstat!(socket_path)
    assert replacement.type == :other

    refute {replacement.major_device, replacement.minor_device, replacement.inode} ==
             {original.major_device, original.minor_device, original.inode}

    Mitos.Authority.stop(authority)
    assert File.exists?(socket_path)

    :gen_tcp.close(replacement_socket)
  end

  test "socket rejects malformed authority requests before session dispatch" do
    {:ok, session} = FakeSession.start_link(self())

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: "malformed-" <> Integer.to_string(System.unique_integer([:positive])),
        project_id: "proj",
        session: session
      )

    on_exit(fn -> stop_authority(authority) end)
    spec = Mitos.Authority.launch_spec(authority)

    assert authority_request(spec.socket_path, %{
             "method" => "approval",
             "token" => spec.token,
             "action" => %{"not" => "a string"}
           }) == %{"ok" => false, "error" => "action_must_be_string"}

    assert authority_request(spec.socket_path, %{
             "method" => "approval",
             "token" => String.duplicate("t", 4 * 1024 + 1)
           }) == %{"ok" => false, "error" => "invalid_token"}

    refute_receive {:authority_request, %Request{}}, 100
  end

  test "socket rejects oversized request lines" do
    {:ok, session} = FakeSession.start_link(self())

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: "oversized-" <> Integer.to_string(System.unique_integer([:positive])),
        project_id: "proj",
        session: session
      )

    on_exit(fn -> stop_authority(authority) end)
    spec = Mitos.Authority.launch_spec(authority)

    reply =
      authority_request(spec.socket_path, %{
        "method" => "approval",
        "token" => spec.token,
        "context" => %{"prompt" => String.duplicate("x", 1024 * 1024 + 1)}
      })

    assert reply["ok"] == false
    assert reply["error"] in ["request_too_large", "invalid_json"]
    refute_receive {:authority_request, %Request{}}, 100
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

  defp tmp_socket_path(prefix) do
    Path.join(
      System.tmp_dir!(),
      "mitos-authority-#{prefix}-#{System.unique_integer([:positive])}.sock"
    )
  end
end
