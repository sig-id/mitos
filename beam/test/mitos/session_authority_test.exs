defmodule Mitos.SessionAuthorityTest do
  use ExUnit.Case, async: false

  @moduletag :integration

  setup_all do
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

    unless File.exists?(binary) do
      flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
    end

    {:ok, binary: binary}
  end

  test "socket-origin requests block, resolve, and return typed payloads", %{binary: binary} do
    session_id = "authority-session-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-authority-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
      Mitos.Orchestrator.start_session(
        session_id: session_id,
        sidecar_path: binary,
        trace_base_dir: trace_base_dir,
        project_id: "proj-a",
        launch_spec: %{
          version: 1,
          provider: "generic",
          task: "",
          transport_order: ["pty"],
          argv: ["/bin/sh", "-c", "sleep 10"]
        },
        idle_ms: 50
      )

    on_exit(fn -> stop_session(session_id) end)

    %{launch_spec: %{authority: authority}} = :sys.get_state(pid)
    token = authority.token
    socket_path = authority.socket_path

    approval =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "approval",
        "token" => token,
        "context" => %{"prompt" => "continue?"}
      })

    assert approval["decision"] == "authorize"
    assert approval["payload"] == %{"approved_by" => "test"}

    put =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "memory_put",
        "token" => token,
        "context" => %{"key" => "notes", "bytes" => "remember me"}
      })

    assert put["payload"] == %{"key" => "notes", "bytes" => 11}

    fetch =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "memory_fetch",
        "token" => token,
        "context" => %{"key" => "notes"}
      })

    assert fetch["payload"] == %{"key" => "notes", "found" => true, "bytes" => "remember me"}

    broker =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "tool_broker",
        "token" => token,
        "context" => %{"argv" => ["/bin/sh", "-c", "printf broker-ok"]}
      })

    assert broker["payload"]["stdout"] == "broker-ok"
    assert broker["payload"]["exit_status"] == 0

    artifact =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "artifact_register",
        "token" => token,
        "context" => %{"kind" => "screenshot", "path" => "screenshots/one.png"}
      })

    assert artifact["payload"]["kind"] == "screenshot"
    assert artifact["payload"]["path"] == "screenshots/one.png"

    run_dir = Path.join([trace_base_dir, ".runs", session_id])
    commands = File.read!(Path.join(run_dir, "commands.jsonl"))
    events = File.read!(Path.join(run_dir, "events.jsonl"))
    approvals = File.read!(Path.join(run_dir, "approvals.jsonl"))

    assert commands =~ "TOOL_BROKER_COMMAND"
    assert commands =~ "broker-ok"
    assert events =~ "ARTIFACT_REGISTERED"
    assert approvals =~ ~s("origin":"authority_socket")
    assert approvals =~ ~s("type":"approval_requested")
    assert approvals =~ ~s("type":"approval_decision")
  end

  defp authorized_request(session_pid, session_id, socket_path, request) do
    task = Task.async(fn -> authority_request(socket_path, request, 6_000) end)
    method = request["method"]

    request_id = receive_authority_request(session_id, method)
    Mitos.Session.resolve(session_pid, request_id, :authorize, %{"approved_by" => "test"})
    Task.await(task, 7_000)
  end

  defp receive_authority_request(session_id, method) do
    receive do
      {:mitos, ^session_id,
       {:event,
        %{
          "type" => "ACTION_REQUESTED",
          "origin" => "authority_socket",
          "method" => ^method,
          "request_id" => request_id
        }}} ->
        request_id

      {:mitos, ^session_id, _other} ->
        receive_authority_request(session_id, method)
    after
      5_000 -> flunk("did not receive authority_socket ACTION_REQUESTED for #{method}")
    end
  end

  defp authority_request(socket_path, request, timeout) do
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

  defp stop_session(session_id) do
    try do
      Mitos.Orchestrator.stop_session(session_id)
    catch
      :exit, _ -> :ok
    end
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
