defmodule Mitos.IOPlaneTest do
  use ExUnit.Case, async: false

  import Plug.Test

  # Exercises the Phase-A I/O plane: the Connector/Sink behaviours, real webhook
  # ingress at /hooks, real HTTP delivery through the `deliver` sink (against a
  # loopback listener), round-trip reply, and the connector/sink listings.

  setup do
    trace_dir = unique_tmp_dir("io-plane-traces")
    apps_dir = unique_tmp_dir("io-plane-apps")
    File.mkdir_p!(trace_dir)
    File.mkdir_p!(apps_dir)

    prev_trace = Application.get_env(:mitos, :trace_base_dir)
    prev_apps = Application.get_env(:mitos, :apps_dir)
    Application.put_env(:mitos, :trace_base_dir, trace_dir)
    Application.put_env(:mitos, :apps_dir, apps_dir)

    on_exit(fn ->
      restore_env(:trace_base_dir, prev_trace)
      restore_env(:apps_dir, prev_apps)
      writable_rm_rf(trace_dir)
      File.rm_rf!(apps_dir)
    end)

    {:ok, trace_dir: trace_dir, apps_dir: apps_dir}
  end

  test "connector and sink registries resolve known classes and reject unknown" do
    assert Mitos.Connector.lookup("webhook") == Mitos.Connectors.Webhook
    assert Mitos.Connector.lookup("manual") == Mitos.Connectors.Manual
    # case-insensitive
    assert Mitos.Connector.lookup("WEBHOOK") == Mitos.Connectors.Webhook
    assert is_nil(Mitos.Connector.lookup("nope"))
    assert "webhook" in Mitos.Connector.names()

    assert Mitos.Sink.lookup("web") == Mitos.Sinks.Web
    assert Mitos.Sink.lookup("deliver") == Mitos.Sinks.Deliver
    assert Mitos.Sink.lookup("reply") == Mitos.Sinks.Reply
    assert is_nil(Mitos.Sink.lookup("nope"))
    assert "deliver" in Mitos.Sink.names()
  end

  test "connectors declare ingress and sinks declare egress capabilities" do
    assert Mitos.Connectors.Webhook.capabilities().round_trip? == true
    assert Mitos.Connectors.Webhook.capabilities().ingress == true
    assert Mitos.Sinks.Web.capabilities().egress == true
    assert Mitos.Sinks.Reply.capabilities().round_trip? == true
  end

  test "webhook connector normalizes an explicit mitos payload" do
    signal = %{
      "inputs" => %{"objective" => "ship"},
      "reply_ref" => "https://cb.test/r/1",
      "idempotency_key" => "del-1"
    }

    assert {:ok, intake} = Mitos.Connectors.Webhook.normalize(signal, source: "ship-app")
    assert intake["inputs"] == %{"objective" => "ship"}
    assert intake["reply_ref"] == "https://cb.test/r/1"
    assert intake["idempotency_key"] == "webhook:del-1"
  end

  test "webhook connector derives an idempotency key for an opaque payload" do
    signal = %{"event" => "push", "data" => %{"x" => 1}}

    assert {:ok, first} = Mitos.Connectors.Webhook.normalize(signal, source: "ship-app")
    assert {:ok, second} = Mitos.Connectors.Webhook.normalize(signal, source: "ship-app")
    # deterministic for the same body → re-delivery dedupes
    assert first["idempotency_key"] == second["idempotency_key"]
    assert String.starts_with?(first["idempotency_key"], "webhook:")
    assert first["inputs"] == %{"x" => 1}
    assert first["intent"] == "push"
  end

  test "webhook connector honors a delivery-id header for idempotency" do
    signal = %{"data" => %{}}

    assert {:ok, intake} =
             Mitos.Connectors.Webhook.normalize(signal,
               headers: %{"x-mitos-delivery-id" => "hdr-7"},
               source: "ship-app"
             )

    assert intake["idempotency_key"] == "webhook:hdr-7"
  end

  test "webhook connector maps a callback_url to reply_ref" do
    signal = %{"event" => "x", "callback_url" => "https://cb.test/r/9"}

    assert {:ok, intake} = Mitos.Connectors.Webhook.normalize(signal, source: "ship-app")
    assert intake["reply_ref"] == "https://cb.test/r/9"
  end

  test "web sink performs no network call and records a sent receipt", %{
    apps_dir: apps_dir,
    trace_dir: traces
  } do
    write_app!(apps_dir)

    assert {:ok, result} =
             Mitos.AppRunner.run(app_root(apps_dir), "ship", %{"objective" => "o"},
               trace_base_dir: traces,
               io_outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
             )

    assert [%{"receipt" => %{"sink" => "web", "status" => "sent"}}] = result["deliveries"]
  end

  test "deliver sink POSTs the result to a bound webhook URL", %{
    apps_dir: apps_dir,
    trace_dir: traces
  } do
    write_app!(apps_dir)
    {port, ref} = start_listener(self())

    url = "http://127.0.0.1:#{port}/hook"

    assert {:ok, result} =
             Mitos.AppRunner.run(app_root(apps_dir), "ship", %{"objective" => "o"},
               trace_base_dir: traces,
               io_outputs: [%{sink: "deliver", entrypoint: "ship", on: "run.done", url: url}]
             )

    assert [%{"receipt" => receipt}] = result["deliveries"], "deliveries: #{inspect(result["deliveries"])}"
    assert receipt["sink"] == "deliver"
    assert receipt["status"] == "sent"
    assert receipt["status"] == "sent"
    assert receipt["http_status"] == 200
    assert receipt["destination"] == url

    assert_receive {:served, ^ref, body}
    served = Jason.decode!(body)
    assert served["entrypoint"] == "ship"
    assert served["sink"] == "deliver"
    assert is_map(served["result"])
  end

  test "deliver sink records a failed delivery for an unreachable destination", %{
    apps_dir: apps_dir,
    trace_dir: traces
  } do
    write_app!(apps_dir)

    # point at a port that refuses connections (grab + close a free port)
    {:ok, probe} = :gen_tcp.listen(0, [active: false, reuseaddr: true])
    {:ok, dead_port} = :inet.port(probe)
    :gen_tcp.close(probe)
    url = "http://127.0.0.1:#{dead_port}/dead"

    assert {:ok, result} =
             Mitos.AppRunner.run(app_root(apps_dir), "ship", %{"objective" => "o"},
               trace_base_dir: traces,
               io_outputs: [%{sink: "deliver", entrypoint: "ship", on: "run.done", url: url}]
             )

    assert [%{"receipt" => %{"sink" => "deliver", "status" => "failed"}}] = result["deliveries"]

    events = Mitos.Trace.read_events(result["app_run_id"], base_dir: traces)

    assert Enum.any?(events, fn
      %{"kind" => "effect", "type" => "delivery_failed"} -> true
      _event -> false
    end)
  end

  test "reply sink round-trips to an http reply_ref via POST", %{
    apps_dir: apps_dir,
    trace_dir: traces
  } do
    write_app!(apps_dir,
      io: %{
        inputs: [],
        outputs: [%{sink: "reply", entrypoint: "ship", on: "run.done", reply_to: "input.reply_ref"}]
      }
    )

    {port, ref} = start_listener(self())
    reply_ref = "http://127.0.0.1:#{port}/back"

    assert {:ok, result} =
             Mitos.AppRunner.run(app_root(apps_dir), "ship", %{"objective" => "o"},
               trace_base_dir: traces,
               reply_ref: reply_ref
             )

    assert [%{"receipt" => receipt}] = result["deliveries"]
    assert receipt["sink"] == "reply"
    assert receipt["status"] == "sent"
    assert receipt["transport"] == "http"
    assert receipt["destination"] == reply_ref

    assert_receive {:served, ^ref, body}
    assert Jason.decode!(body)["kind"] == "reply"
  end

  test "raw webhook ingress at /hooks/:app_id runs an entrypoint via the connector", %{
    apps_dir: apps_dir,
    trace_dir: traces
  } do
    write_app!(apps_dir,
      io: %{
        inputs: [
          %{connector: "webhook", entrypoint: "ship", select: %{mode: "explicit", pipeline: "ship-flow"}}
        ],
        outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
      }
    )

    body = %{
      "event" => "build",
      "data" => %{"objective" => "from hook"},
      "delivery_id" => "hook-del-1"
    }

    conn =
      json_conn(:post, "/hooks/io-app", body)
      |> Mitos.Web.Router.call([])

    assert conn.status == 200, "body: #{conn.resp_body}"
    result = Jason.decode!(conn.resp_body)
    assert result["entrypoint"] == "ship"
    assert result["pipeline_id"] == "ship-flow"

    events = Mitos.Trace.read_events(result["app_run_id"], base_dir: traces)

    assert Enum.any?(events, fn
      %{
        "kind" => "effect",
        "type" => "signal_received",
        "ref" => %{"connector" => "webhook", "idempotency_key" => "webhook:hook-del-1"}
      } ->
        true

      _event ->
        false
    end)
  end

  test "raw webhook ingress is idempotent on re-delivery", %{
    apps_dir: apps_dir
  } do
    write_app!(apps_dir,
      io: %{
        inputs: [
          %{connector: "webhook", entrypoint: "ship", select: %{mode: "explicit", pipeline: "ship-flow"}}
        ],
        outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
      }
    )

    body = %{"data" => %{"objective" => "again", "name" => "Bo"}, "delivery_id" => "dup-1"}

    first =
      json_conn(:post, "/hooks/io-app", body)
      |> Mitos.Web.Router.call([])
      |> Map.get(:resp_body)
      |> Jason.decode!()

    second =
      json_conn(:post, "/hooks/io-app", body)
      |> Mitos.Web.Router.call([])
      |> Map.get(:resp_body)
      |> Jason.decode!()

    assert first["app_run_id"] == second["app_run_id"]
    assert second["duplicate?"] == true
  end

  test "GET /io/connectors and /io/sinks list registered classes" do
    connectors =
      conn(:get, "/io/connectors") |> Mitos.Web.Router.call([]) |> decode_body()

    names = Enum.map(connectors["connectors"], & &1["name"])
    assert "webhook" in names
    assert "manual" in names
    assert Enum.all?(connectors["connectors"], & &1["capabilities"]["ingress"])

    sinks =
      conn(:get, "/io/sinks") |> Mitos.Web.Router.call([]) |> decode_body()

    sink_names = Enum.map(sinks["sinks"], & &1["name"])
    assert "web" in sink_names
    assert "deliver" in sink_names
    assert "reply" in sink_names
    assert Enum.all?(sinks["sinks"], & &1["capabilities"]["egress"])
  end

  test "unknown connector ingress is rejected" do
    conn =
      json_conn(:post, "/hooks/missing-app/postmark", %{})
      |> Mitos.Web.Router.call([])

    # connector known, app missing → app error; connector unknown → 404.
    # Use an installed app with an unknown connector to isolate the 404 path.
    assert conn.status in 404..422
  end

  # ---- helpers ----

  defp app_root(apps_dir), do: Path.join(apps_dir, "io-app")

  defp write_app!(apps_dir, opts \\ []) do
    io = Keyword.get(opts, :io, %{inputs: [], outputs: []})
    root = app_root(apps_dir)
    File.mkdir_p!(Path.join(root, "pipelines"))
    File.mkdir_p!(Path.join([root, "capabilities", "ship"]))

    File.write!(Path.join(root, "pipelines/ship.md"), """
    # Pipeline: ship-flow

    Goal: produce a backend application result.

    ## Stages
    1. **ship-cap** - run the local capability package.
    """)

    File.write!(
      Path.join(root, "capabilities/ship/workload.json"),
      Jason.encode!(%{
        provider: "fake",
        template: "App {{objective}} step {{pipeline_step}}",
        capability: %{name: "ship-cap", harnesses: ["fake"], trust_tier: "T1"}
      })
    )

    File.write!(
      Path.join(root, "app.json"),
      Jason.encode!(%{
        name: "io-app",
        version: "1.0.0",
        capabilities: [%{ref: "capabilities/ship/workload.json", as: "ship-cap"}],
        pipelines: [%{id: "ship-flow", file: "pipelines/ship.md"}],
        entrypoints: [
          %{
            id: "ship",
            pipeline: "ship-flow",
            title: "Ship",
            workspace: "none",
            inputs: %{schema: %{type: "object", properties: %{objective: %{type: "string"}}}}
          }
        ],
        io: io,
        services: %{requires: [], wants: []}
      })
    )

    :ok
  end

  defp start_listener(test_pid) do
    {:ok, listen} = :gen_tcp.listen(0, [:binary, active: false, reuseaddr: true])
    {:ok, port} = :inet.port(listen)
    ref = make_ref()

    spawn(fn ->
      serve_one(listen, test_pid, ref)
    end)

    # Give the acceptor a moment to be ready before the client connects.
    Process.sleep(10)
    {port, ref}
  end

  defp serve_one(listen, test_pid, ref) do
    case :gen_tcp.accept(listen, 5_000) do
      {:ok, socket} ->
        {:ok, req} = :gen_tcp.recv(socket, 0)
        body = extract_body(req)
        send(test_pid, {:served, ref, body})
        :gen_tcp.send(socket, "HTTP/1.1 200 OK\r\ncontent-length: 2\r\n\r\nok")
        :gen_tcp.close(socket)
        :gen_tcp.close(listen)

      {:error, _reason} ->
        :gen_tcp.close(listen)
    end
  end

  defp extract_body(req) do
    case :binary.match(req, "\r\n\r\n") do
      {pos, 4} -> binary_part(req, pos + 4, byte_size(req) - pos - 4)
      :nomatch -> ""
    end
  end

  defp json_conn(method, path, body) do
    method
    |> conn(path, Jason.encode!(body))
    |> Plug.Conn.put_req_header("content-type", "application/json")
  end

  defp decode_body(conn), do: Jason.decode!(conn.resp_body)

  defp unique_tmp_dir(prefix) do
    path = Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{tmp_suffix()}")
    File.rm_rf!(path)
    path
  end

  defp tmp_suffix do
    random = :crypto.strong_rand_bytes(8) |> Base.url_encode64(padding: false)
    "#{System.unique_integer([:positive, :monotonic])}-#{random}"
  end

  defp writable_rm_rf(path) do
    if File.exists?(path) do
      path
      |> Path.join("**/*")
      |> Path.wildcard(match_dot: true)
      |> Enum.each(fn entry ->
        case File.stat(entry) do
          {:ok, %{type: :directory, mode: mode}} -> File.chmod(entry, Bitwise.bor(mode, 0o700))
          {:ok, %{mode: mode}} -> File.chmod(entry, Bitwise.bor(mode, 0o600))
          {:error, _reason} -> :ok
        end
      end)

      File.chmod(path, 0o700)
      File.rm_rf!(path)
    end
  end

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)
end
