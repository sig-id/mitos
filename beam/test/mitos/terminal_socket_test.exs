defmodule Mitos.Web.TerminalSocketTest do
  use ExUnit.Case, async: true

  alias Mitos.Web.TerminalSocket

  test "binary stdin frame over size limit returns a websocket error event" do
    oversized = :binary.copy("x", Mitos.Session.max_stdin_bytes() + 1)
    pid = self()

    assert {[{:text, payload}], %{session_pid: ^pid}} =
             TerminalSocket.websocket_handle({:binary, oversized}, %{session_pid: pid})

    assert %{
             "type" => "event",
             "event" => %{
               "type" => "ERROR",
               "kind" => "stdin",
               "detail" => "{:invalid_stdin, :too_large}"
             }
           } = Jason.decode!(payload)
  end

  test "invalid semantic control requests return websocket error events" do
    steer =
      Jason.encode!(%{
        type: "steer",
        interaction: "prompt",
        text: ""
      })

    assert {[{:text, steer_payload}], _state} =
             TerminalSocket.websocket_handle({:text, steer}, %{session_pid: self()})

    assert %{
             "type" => "event",
             "event" => %{
               "type" => "ERROR",
               "kind" => "steer",
               "detail" => "{:invalid_steer, {:text, :empty}}"
             }
           } = Jason.decode!(steer_payload)

    oversized_steer =
      Jason.encode!(%{
        type: "steer",
        interaction: "prompt",
        text: :binary.copy("x", Mitos.Session.max_steer_text_bytes() + 1)
      })

    assert {[{:text, oversized_steer_payload}], _state} =
             TerminalSocket.websocket_handle({:text, oversized_steer}, %{session_pid: self()})

    assert %{
             "type" => "event",
             "event" => %{
               "type" => "ERROR",
               "kind" => "steer",
               "detail" => "{:invalid_steer, {:text, :too_large}}"
             }
           } = Jason.decode!(oversized_steer_payload)

    resize = Jason.encode!(%{type: "resize", cols: 10_000, rows: 24})

    assert {[{:text, resize_payload}], _state} =
             TerminalSocket.websocket_handle({:text, resize}, %{session_pid: self()})

    assert %{
             "type" => "event",
             "event" => %{
               "type" => "ERROR",
               "kind" => "resize",
               "detail" => "{:invalid_size, {:cols, :too_large}}"
             }
           } = Jason.decode!(resize_payload)
  end

  test "invalid authority control requests return websocket error events" do
    oversized = :binary.copy("x", Mitos.Session.max_authority_response_bytes() + 1)

    authorize =
      Jason.encode!(%{
        type: "authorize",
        action_id: "request-1",
        token: oversized
      })

    assert {[{:text, authorize_payload}], _state} =
             TerminalSocket.websocket_handle({:text, authorize}, %{session_pid: self()})

    assert %{
             "type" => "event",
             "event" => %{
               "type" => "ERROR",
               "kind" => "authorize",
               "detail" => "{:invalid_authority_resolution, {:token, :too_large}}"
             }
           } = Jason.decode!(authorize_payload)

    deny =
      Jason.encode!(%{
        type: "deny",
        action_id: "request-1",
        reason: oversized
      })

    assert {[{:text, deny_payload}], _state} =
             TerminalSocket.websocket_handle({:text, deny}, %{session_pid: self()})

    assert %{
             "type" => "event",
             "event" => %{
               "type" => "ERROR",
               "kind" => "deny",
               "detail" => "{:invalid_authority_resolution, {:reason, :too_large}}"
             }
           } = Jason.decode!(deny_payload)
  end
end
