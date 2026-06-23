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
