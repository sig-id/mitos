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
    Mitos.PubSub.subscribe(session_id)

    {:ok, %{pid: pid}} =
      Mitos.Orchestrator.start_session(
        session_id: session_id,
        sidecar_path: binary,
        # Use `bash -c` so we get a real PTY interaction. The newline-terminated
        # echo guarantees output makes it across before the child exits.
        argv: ["/bin/sh", "-c", "printf 'mitos %s\\n' OK; sleep 0.05"],
        cols: 80,
        rows: 24,
        idle_ms: 50
      )

    on_exit(fn ->
      try do
        Mitos.Orchestrator.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    # 1) Hello arrives once the sidecar is up.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "hello", "backend" => backend}}}, 5_000
    assert backend in ["native", "compat"]

    # 2) Opened event with the spawned pid.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "opened", "pid" => spawned_pid}}}, 5_000
    assert is_integer(spawned_pid) and spawned_pid > 0

    # 3) Raw bytes — the actual `mitos OK` echo. (Multiple RAW frames may
    # arrive; concatenate until we see it.)
    raw = drain_raw(session_id, "", 3_000)
    assert raw =~ "mitos OK"

    # 4) Child exits → exit event surfaces to us.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "exit"} = exit_evt}}, 5_000
    assert is_integer(exit_evt["code"]) or is_integer(exit_evt["signal"])

    # The Session itself stops normally after the sidecar exits. Poll for it
    # to deregister (now that env includes PATH, the child's `sleep 0.05`
    # actually waits, so a fixed 50ms sleep here would race).
    assert_eventually(500, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)
    refute Process.alive?(pid)
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

  defp drain_raw(session_id, acc, deadline_ms) do
    receive do
      {:mitos, ^session_id, {:raw, bytes}} ->
        new = acc <> bytes
        if new =~ "mitos OK", do: new, else: drain_raw(session_id, new, deadline_ms)

      # Ignore other events while we're hunting raw bytes.
      {:mitos, ^session_id, _other} ->
        drain_raw(session_id, acc, deadline_ms)
    after
      deadline_ms -> acc
    end
  end
end
