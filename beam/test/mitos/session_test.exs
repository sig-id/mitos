defmodule Mitos.SessionTest do
  use ExUnit.Case, async: false

  @moduletag :integration

  setup_all do
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

    unless File.exists?(binary) do
      flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
    end

    # Subscribe to our test session's broadcasts before starting so we don't
    # miss the initial `hello` / `opened` events.
    {:ok, binary: binary}
  end

  test "drives a session end-to-end: hello → open → raw → exit", %{binary: binary} do
    session_id = "test-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
      Mitos.SessionSupervisor.start_session(
        session_id: session_id,
        sidecar_path: binary,
        trace_base_dir: trace_base_dir,
        # Use `bash -c` so we get a real PTY interaction. The newline-terminated
        # echo guarantees output makes it across before the child exits.
        argv: ["/bin/sh", "-c", "printf 'mitos %s\\n' OK; sleep 0.05"],
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

    # 1) Hello arrives once the sidecar is up.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "hello", "backend" => backend}}},
                   5_000

    assert backend in ["native", "compat"]

    # 2) Opened event with the spawned pid.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "opened", "pid" => spawned_pid}}},
                   5_000

    assert is_integer(spawned_pid) and spawned_pid > 0

    # 3) Raw bytes — the actual `mitos OK` echo. (Multiple RAW frames may
    # arrive; concatenate until we see it.) The engine also emits RAW_REF
    # offsets for the high-volume raw spool owned below the protocol seam.
    {raw, raw_refs} = drain_raw(session_id, "", [], 3_000)
    assert raw =~ "mitos OK"
    assert [%{"offset" => 0, "len" => len, "path" => path} | _] = raw_refs
    assert is_integer(len) and len > 0
    assert is_binary(path) and path =~ "/tmp/mitos-terminal-"

    # 4) Child exits → exit event surfaces to us.
    %{"type" => exit_type} = exit_evt = receive_exit_event(session_id, 5_000)
    assert exit_type in ["exit", "PROCESS_EXITED"]
    assert is_integer(exit_evt["code"]) or is_integer(exit_evt["signal"])

    # The Session itself stops normally after the sidecar exits. Poll for it
    # to deregister (now that env includes PATH, the child's `sleep 0.05`
    # actually waits, so a fixed 50ms sleep here would race).
    assert_eventually(500, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)
    refute Process.alive?(pid)

    run_dir = Path.join([trace_base_dir, ".runs", session_id])
    assert File.read!(Path.join(run_dir, "run.yaml")) =~ "run_id: #{session_id}"
    events = File.read!(Path.join(run_dir, "events.jsonl"))
    assert events =~ ~s("type":"hello")
    assert events =~ ~s("type":"RAW_REF")
    assert events =~ ~s("type":"PROCESS_SPAWNED")
    refute events =~ "mitos OK"
  end

  test "rejects raw sessions whose cwd contains symlinked parents", %{binary: binary} do
    session_id = "session-cwd-symlink-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-cwd-symlink-trace")
    outside = unique_tmp_dir("session-cwd-symlink-outside")
    link = unique_tmp_dir("session-cwd-symlink-link")

    File.mkdir_p!(Path.join(outside, "workspace"))
    File.ln_s!(outside, link)

    on_exit(fn ->
      File.rm_rf!(trace_base_dir)
      File.rm_rf!(outside)
      File.rm(link)
    end)

    assert {:error, {:invalid_cwd, :symlink}} =
             Mitos.SessionSupervisor.start_session(
               session_id: session_id,
               sidecar_path: binary,
               trace_base_dir: trace_base_dir,
               argv: ["/bin/sh", "-c", "printf ran > ran.txt"],
               cwd: Path.join(link, "workspace"),
               idle_ms: 50
             )

    refute File.exists?(Path.join([outside, "workspace", "ran.txt"]))
    refute File.exists?(Path.join([trace_base_dir, ".runs", session_id]))
  end

  test "rejects malformed raw session env before spawning", %{binary: binary} do
    session_id = "session-env-invalid-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-env-invalid-trace")

    marker =
      Path.join(
        System.tmp_dir!(),
        "mitos-session-env-invalid-#{System.unique_integer([:positive])}"
      )

    on_exit(fn ->
      File.rm_rf!(trace_base_dir)
      File.rm(marker)
    end)

    assert {:error, {:invalid_env, {:invalid_name, "PATH=/tmp/evil"}}} =
             Mitos.SessionSupervisor.start_session(
               session_id: session_id,
               sidecar_path: binary,
               trace_base_dir: trace_base_dir,
               argv: ["/bin/sh", "-c", "printf ran > #{marker}"],
               env: %{"PATH=/tmp/evil" => "ignored"},
               idle_ms: 50
             )

    refute File.exists?(marker)
    refute File.exists?(Path.join([trace_base_dir, ".runs", session_id]))
  end

  test "rejects raw sessions with invalid terminal dimensions before spawning", %{binary: binary} do
    session_id = "session-size-invalid-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-size-invalid-trace")

    marker =
      Path.join(
        System.tmp_dir!(),
        "mitos-session-size-invalid-#{System.unique_integer([:positive])}"
      )

    on_exit(fn ->
      File.rm_rf!(trace_base_dir)
      File.rm(marker)
    end)

    assert {:error, {:invalid_size, {:cols, :too_large}}} =
             Mitos.SessionSupervisor.start_session(
               session_id: session_id,
               sidecar_path: binary,
               trace_base_dir: trace_base_dir,
               argv: ["/bin/sh", "-c", "printf ran > #{marker}"],
               cols: 10_000,
               rows: 24,
               idle_ms: 50
             )

    refute File.exists?(marker)
    refute File.exists?(Path.join([trace_base_dir, ".runs", session_id]))
  end

  test "rejects raw sessions whose open control frame exceeds sidecar limit", %{binary: binary} do
    session_id =
      "session-open-too-large-" <> Integer.to_string(System.unique_integer([:positive]))

    trace_base_dir = unique_tmp_dir("session-open-too-large-trace")
    oversized_arg = :binary.copy("x", Mitos.Session.max_sidecar_frame_bytes() + 1)

    on_exit(fn -> File.rm_rf!(trace_base_dir) end)

    assert {:error, {:invalid_open_control, :too_large}} =
             Mitos.SessionSupervisor.start_session(
               session_id: session_id,
               sidecar_path: binary,
               trace_base_dir: trace_base_dir,
               argv: ["/bin/sh", "-c", oversized_arg],
               idle_ms: 50
             )

    refute File.exists?(Path.join([trace_base_dir, ".runs", session_id]))
  end

  test "rejects provider launches whose open control frame exceeds sidecar limit", %{
    binary: binary
  } do
    session_id = "launch-open-too-large-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("launch-open-too-large-trace")
    oversized_task = :binary.copy("x", Mitos.Session.max_sidecar_frame_bytes() + 1)

    on_exit(fn -> File.rm_rf!(trace_base_dir) end)

    assert {:error, {:invalid_open_control, :too_large}} =
             Mitos.SessionSupervisor.start_session(
               session_id: session_id,
               sidecar_path: binary,
               trace_base_dir: trace_base_dir,
               launch_spec: %{
                 provider: "fake",
                 task: oversized_task,
                 transport_order: ["pty"]
               },
               idle_ms: 50
             )

    refute File.exists?(Path.join([trace_base_dir, ".runs", session_id]))
  end

  test "rejects provider launches whose authority-expanded open control is too large", %{
    binary: binary
  } do
    session_id =
      "launch-open-authority-too-large-" <> Integer.to_string(System.unique_integer([:positive]))

    trace_base_dir = unique_tmp_dir("launch-open-authority-too-large-trace")
    project_id = "proj-open-frame"
    base_spec = %{provider: "fake", task: "", transport_order: ["pty"]}

    {:ok, authority} =
      Mitos.Authority.start_link(run_id: session_id, session: self(), project_id: project_id)

    authority_spec = Mitos.Authority.launch_spec(authority)
    Mitos.Authority.stop(authority)

    task = :binary.copy("x", max_open_task_bytes_under_limit(base_spec))
    launch_spec = %{base_spec | task: task}
    authority_launch_spec = Map.put(launch_spec, :authority, authority_spec)

    assert open_control_size(launch_spec) <= Mitos.Session.max_sidecar_frame_bytes()
    assert open_control_size(authority_launch_spec) > Mitos.Session.max_sidecar_frame_bytes()

    on_exit(fn -> File.rm_rf!(trace_base_dir) end)

    assert {:error, {:invalid_open_control, :too_large}} =
             Mitos.SessionSupervisor.start_session(
               session_id: session_id,
               sidecar_path: binary,
               trace_base_dir: trace_base_dir,
               project_id: project_id,
               launch_spec: launch_spec,
               idle_ms: 50
             )

    refute File.exists?(Path.join([trace_base_dir, ".runs", session_id]))
    refute File.exists?(authority_socket_path(session_id))
  end

  test "accepts raw session env list entries", %{binary: binary} do
    session_id = "session-env-list-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-env-list-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
      Mitos.SessionSupervisor.start_session(
        session_id: session_id,
        sidecar_path: binary,
        trace_base_dir: trace_base_dir,
        argv: ["/bin/sh", "-c", "printf 'env %s\\n' \"$MITOS_SESSION_ENV_LIST\""],
        env: [%{"name" => "MITOS_SESSION_ENV_LIST", "value" => "ok"}],
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
    assert collect_raw_until(session_id, "", "env ok", 5_000) =~ "env ok"
    Mitos.Session.shutdown(pid)
  end

  test "steers a pty session with semantic interactions", %{binary: binary} do
    session_id = "steer-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("steer-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
      Mitos.SessionSupervisor.start_session(
        session_id: session_id,
        sidecar_path: binary,
        trace_base_dir: trace_base_dir,
        # A one-line read/echo loop body: steering a :prompt through the
        # generic keymap must arrive as "hello\r" on the PTY.
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

    Mitos.Session.steer(pid, :prompt, text: "hello")

    assert_receive {:mitos, ^session_id,
                    {:event, %{"type" => "STEER_SENT", "interaction" => "prompt"}}},
                   5_000

    assert collect_raw_until(session_id, "", "got hello", 5_000) =~ "got hello"
  end

  test "rejects invalid semantic steering before sending sidecar control", %{binary: binary} do
    session_id = "steer-invalid-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("steer-invalid-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
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

    assert {:error, {:invalid_steer, {:text, :empty}}} =
             Mitos.Session.steer(pid, :prompt, text: "   ")

    assert {:error, {:invalid_steer, {:text, :required}}} =
             Mitos.Session.steer(pid, :answer)

    assert {:error, {:invalid_steer, {:text, :nul}}} =
             Mitos.Session.steer(pid, :prompt, text: "bad\0input")

    oversized_text = :binary.copy("x", Mitos.Session.max_steer_text_bytes() + 1)

    assert {:error, {:invalid_steer, {:text, :too_large}}} =
             Mitos.Session.steer(pid, :prompt, text: oversized_text)

    assert {:error, {:invalid_steer, {:option, :too_small}}} =
             Mitos.Session.steer(pid, :select_option, option: 0)

    assert {:error, {:invalid_steer, {:option, :too_large}}} =
             Mitos.Session.steer(pid, :select_option, option: 10)

    assert {:error, {:invalid_steer, {:option, :invalid}}} =
             Mitos.Session.steer(pid, :select_option, option: "1")

    assert {:error, {:invalid_steer, :invalid_options}} =
             Mitos.Session.steer(pid, :prompt, [:not_an_option])

    assert {:error, {:invalid_steer, :invalid_interaction}} =
             Mitos.Session.steer(pid, :bogus)

    assert {:error, {:invalid_steer, {:text, :empty}}} =
             Mitos.SandboxProvider.LocalSidecar.control(pid, {:steer, :prompt, text: "\n"})

    Mitos.Session.steer(pid, :prompt, text: "alive")
    assert collect_raw_until(session_id, "", "got alive", 5_000) =~ "got alive"
  end

  test "rejects oversized stdin before sending sidecar control", %{binary: binary} do
    session_id = "stdin-invalid-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("stdin-invalid-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
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

    oversized = :binary.copy("x", Mitos.Session.max_stdin_bytes() + 1)
    assert {:error, {:invalid_stdin, :too_large}} = Mitos.Session.write_stdin(pid, oversized)

    assert {:error, {:invalid_stdin, :too_large}} =
             Mitos.SandboxProvider.LocalSidecar.control(pid, {:write_stdin, oversized})

    Mitos.Session.write_stdin(pid, "alive\n")
    assert collect_raw_until(session_id, "", "got alive", 5_000) =~ "got alive"
  end

  test "rejects invalid authority resolution payloads before enqueueing control" do
    oversized = :binary.copy("x", Mitos.Session.max_authority_response_bytes() + 1)

    assert {:error, {:invalid_authority_resolution, {:request_id, :empty}}} =
             Mitos.Session.authorize(self(), "", "token")

    assert {:error, {:invalid_authority_resolution, {:request_id, :nul}}} =
             Mitos.Session.deny(self(), "request\0id", "no")

    assert {:error, {:invalid_authority_resolution, {:token, :too_large}}} =
             Mitos.Session.authorize(self(), "request-1", oversized)

    assert {:error, {:invalid_authority_resolution, {:reason, :too_large}}} =
             Mitos.Session.deny(self(), "request-1", oversized)

    assert {:error, {:invalid_authority_resolution, {:reason, :nul}}} =
             Mitos.Session.resolve(self(), "request-1", :deny, %{"reason" => "bad\0reason"})
  end

  test "rejects invalid signals before sending sidecar control", %{binary: binary} do
    session_id = "signal-invalid-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("signal-invalid-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
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
    assert {:error, {:invalid_signal, :too_large}} = Mitos.Session.signal(pid, 10_000)
    assert {:error, {:invalid_signal, :too_small}} = Mitos.Session.signal(pid, 0)
    assert {:error, {:invalid_signal, :invalid}} = Mitos.Session.signal(pid, "TERM")

    assert {:error, {:invalid_signal, :too_large}} =
             Mitos.SandboxProvider.LocalSidecar.control(pid, {:signal, 10_000})

    Mitos.Session.steer(pid, :prompt, text: "alive")
    assert collect_raw_until(session_id, "", "got alive", 5_000) =~ "got alive"
  end

  test "rejects invalid shutdown grace before sending sidecar control", %{binary: binary} do
    session_id = "shutdown-invalid-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("shutdown-invalid-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
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

    assert {:error, {:invalid_shutdown_grace_ms, :too_large}} =
             Mitos.Session.shutdown(pid, 120_000)

    assert {:error, {:invalid_shutdown_grace_ms, :too_small}} =
             Mitos.Session.shutdown(pid, -1)

    assert {:error, {:invalid_shutdown_grace_ms, :invalid}} =
             Mitos.Session.shutdown(pid, "now")

    assert {:error, {:invalid_shutdown_grace_ms, :too_large}} =
             Mitos.SandboxProvider.LocalSidecar.control(pid, {:shutdown, 120_000})

    Mitos.Session.steer(pid, :prompt, text: "alive")
    assert collect_raw_until(session_id, "", "got alive", 5_000) =~ "got alive"
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

  defp receive_exit_event(session_id, deadline_ms) do
    receive do
      {:mitos, ^session_id, {:event, %{"type" => type} = event}}
      when type in ["exit", "PROCESS_EXITED"] ->
        event

      {:mitos, ^session_id, _other} ->
        receive_exit_event(session_id, deadline_ms)
    after
      deadline_ms -> flunk("did not receive terminal exit event")
    end
  end

  defp assert_eventually(deadline_ms, fun) do
    deadline = System.monotonic_time(:millisecond) + deadline_ms

    Stream.unfold(:start, fn _ ->
      if fun.() do
        nil
      else
        if System.monotonic_time(:millisecond) > deadline do
          nil
        else
          Process.sleep(10)
          {:tick, :tick}
        end
      end
    end)
    |> Enum.to_list()

    assert fun.(), "condition did not become true within #{deadline_ms}ms"
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end

  defp max_open_task_bytes_under_limit(base_spec) do
    limit = Mitos.Session.max_sidecar_frame_bytes()

    binary_search_max(0, limit, fn size ->
      task = :binary.copy("x", size)
      open_control_size(%{base_spec | task: task}) <= limit
    end)
  end

  defp binary_search_max(low, high, predicate) when low >= high do
    if predicate.(low), do: low, else: low - 1
  end

  defp binary_search_max(low, high, predicate) do
    mid = div(low + high + 1, 2)

    if predicate.(mid) do
      binary_search_max(mid, high, predicate)
    else
      binary_search_max(low, mid - 1, predicate)
    end
  end

  defp open_control_size(launch_spec) do
    launch_spec
    |> Mitos.Sidecar.Protocol.open_launch(cols: 80, rows: 24, idle_ms: 50)
    |> Mitos.Sidecar.Protocol.encode_control()
    |> IO.iodata_length()
  end

  defp authority_socket_path(run_id) do
    safe_id = Regex.replace(~r/[^A-Za-z0-9_.-]/, run_id, "_")
    Path.join(System.tmp_dir!(), "mitos-authority-#{safe_id}.sock")
  end

  defp drain_raw(session_id, acc, raw_refs, deadline_ms) do
    receive do
      {:mitos, ^session_id, {:raw, bytes}} ->
        new = acc <> bytes

        if new =~ "mitos OK" and raw_refs != [] do
          {new, raw_refs}
        else
          drain_raw(session_id, new, raw_refs, deadline_ms)
        end

      {:mitos, ^session_id, {:event, %{"type" => "RAW_REF"} = event}} ->
        raw_refs = raw_refs ++ [event]

        if acc =~ "mitos OK" do
          {acc, raw_refs}
        else
          drain_raw(session_id, acc, raw_refs, deadline_ms)
        end

      # Ignore other events while we're hunting raw bytes.
      {:mitos, ^session_id, _other} ->
        drain_raw(session_id, acc, raw_refs, deadline_ms)
    after
      deadline_ms -> {acc, raw_refs}
    end
  end
end
