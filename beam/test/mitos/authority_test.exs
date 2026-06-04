defmodule Mitos.AuthorityTest do
  use ExUnit.Case, async: false

  test "serves run-scoped JSON requests over a unix socket" do
    run_id = "auth-" <> Integer.to_string(System.unique_integer([:positive]))

    {:ok, authority} =
      Mitos.Authority.start_link(
        run_id: run_id,
        project_id: "proj",
        session: self()
      )

    on_exit(fn ->
      try do
        Mitos.Authority.stop(authority)
      catch
        :exit, _ -> :ok
      end
    end)

    spec = Mitos.Authority.launch_spec(authority)
    assert spec.run_id == run_id
    assert spec.project_id == "proj"
    assert is_binary(spec.socket_path)
    assert is_binary(spec.token)

    unauthorized =
      authority_request(spec.socket_path, %{
        "method" => "approval",
        "token" => "wrong"
      })

    assert unauthorized == %{"ok" => false, "error" => "unauthorized"}

    reply =
      authority_request(spec.socket_path, %{
        "method" => "approval",
        "token" => spec.token,
        "action" => "run_command",
        "context" => %{"command" => "mix test"}
      })

    assert %{"ok" => true, "action_id" => action_id, "status" => "requested"} = reply

    assert_receive {:authority_event,
                    %{
                      "type" => "ACTION_REQUESTED",
                      "source" => "authority",
                      "method" => "approval",
                      "action_id" => ^action_id,
                      "action" => "run_command",
                      "context_json" => %{"command" => "mix test"}
                    }},
                   1_000
  end

  defp authority_request(socket_path, request) do
    {:ok, socket} =
      :gen_tcp.connect({:local, socket_path}, 0, [
        :binary,
        packet: :line,
        active: false
      ])

    :ok = :gen_tcp.send(socket, Jason.encode!(request) <> "\n")
    {:ok, line} = :gen_tcp.recv(socket, 0, 1_000)
    :gen_tcp.close(socket)
    Jason.decode!(String.trim(line))
  end
end
