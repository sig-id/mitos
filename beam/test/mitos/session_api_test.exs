defmodule Mitos.SessionApiTest do
  use ExUnit.Case, async: false

  import Plug.Test

  test "POST /sessions rejects malformed argv requests" do
    cases = [
      {%{}, "argv must be a non-empty list of strings"},
      {%{argv: "/bin/true"}, "argv must be a non-empty list of strings"},
      {%{argv: []}, "argv must be a non-empty list of strings"},
      {%{argv: [1]}, "argv entries must be strings"},
      {%{argv: [""]}, "argv command must not be empty"},
      {%{argv: ["/bin/tr\0ue"]}, "argv entries must not contain NUL"}
    ]

    for {body, detail} <- cases do
      conn = json_conn(:post, "/sessions", body) |> Mitos.Web.Router.call([])

      assert conn.status == 400
      response = Jason.decode!(conn.resp_body)
      assert response["error"] == "invalid session request"
      assert response["detail"] == detail
    end
  end

  test "POST /sessions reports normalized env failures as bad requests" do
    conn =
      json_conn(:post, "/sessions", %{
        argv: ["/bin/true"],
        env: %{"PATH=/tmp/evil" => "ignored"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 400
    response = Jason.decode!(conn.resp_body)
    assert response["error"] == "invalid session request"
    assert response["detail"] =~ "invalid_env"
    assert response["detail"] =~ "PATH=/tmp/evil"
  end

  test "POST /sessions reports invalid terminal dimensions as bad requests" do
    conn =
      json_conn(:post, "/sessions", %{
        argv: ["/bin/true"],
        cols: 10_000,
        rows: 24
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 400
    response = Jason.decode!(conn.resp_body)
    assert response["error"] == "invalid session request"
    assert response["detail"] =~ "invalid_size"
    assert response["detail"] =~ "cols"
  end

  test "POST /sessions/:id/steer reports invalid steering as bad requests" do
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

    unless File.exists?(binary) do
      flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
    end

    session_id = "api-steer-invalid-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("api-steer-invalid-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: _pid}} =
      Mitos.SessionSupervisor.start_session(
        session_id: session_id,
        sidecar_path: binary,
        trace_base_dir: trace_base_dir,
        argv: ["/bin/sh", "-c", "read line; printf 'got %s\\n' \"$line\""],
        cols: 80,
        rows: 24,
        idle_ms: 50
      )

    on_exit(fn ->
      try do
        Mitos.SessionSupervisor.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    assert_receive {:mitos, ^session_id, {:event, %{"type" => "opened"}}}, 5_000

    invalid_cases = [
      {%{interaction: "prompt", text: ""}, "text"},
      {%{interaction: "answer"}, "text"},
      {%{
         interaction: "prompt",
         text: :binary.copy("x", Mitos.Session.max_steer_text_bytes() + 1)
       }, "too_large"},
      {%{interaction: "select_option", option: 0}, "option"},
      {%{interaction: "select_option", option: 10}, "option"},
      {%{interaction: "select_option", option: "1"}, "option"}
    ]

    for {body, detail} <- invalid_cases do
      conn = json_conn(:post, "/sessions/#{session_id}/steer", body) |> Mitos.Web.Router.call([])

      assert conn.status == 400
      response = Jason.decode!(conn.resp_body)
      assert response["error"] == "invalid steering request"
      assert response["detail"] =~ detail
    end

    conn =
      json_conn(:post, "/sessions/#{session_id}/steer", %{
        interaction: "prompt",
        text: "api-alive"
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 200
    assert Jason.decode!(conn.resp_body)["ok"] == true
    assert collect_raw_until(session_id, "", "got api-alive", 5_000) =~ "got api-alive"
  end

  defp json_conn(method, path, body) do
    method
    |> conn(path, Jason.encode!(body))
    |> Plug.Conn.put_req_header("content-type", "application/json")
  end

  defp collect_raw_until(session_id, acc, needle, deadline_ms) do
    if acc =~ needle do
      acc
    else
      receive do
        {:mitos, ^session_id, {:raw, bytes}} ->
          collect_raw_until(session_id, acc <> bytes, needle, deadline_ms)

        {:mitos, ^session_id, _other} ->
          collect_raw_until(session_id, acc, needle, deadline_ms)
      after
        deadline_ms -> acc
      end
    end
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
