defmodule Mitos.AgentTest do
  use ExUnit.Case, async: false

  @moduletag :integration

  setup_all do
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

    unless File.exists?(binary) do
      flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
    end

    :ok
  end

  test "fake agent run: launch spec → session → engine prompt event" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "hello",
        session_id: session_id,
        cols: 80,
        rows: 24,
        # Short idle so the watcher snapshots while the fake stand-in is still
        # alive (its script sleeps 0.3s after printing the final marker).
        idle_ms: 40
      )

    on_exit(fn ->
      try do
        Mitos.Orchestrator.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    assert run.provider == "fake"
    assert run.adapter == nil
    assert run.mode == :engine
    assert run.transport_order == ["stdio-json", "pty"]
    assert run.session_id == session_id

    # The sidecar comes up and the agent's output flows through.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "hello"}}}, 5_000

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "RUN_STARTED",
                       "provider" => "fake",
                       "adapter" => "fake.headless",
                       "transport" => "stdio-json"
                     }}},
                   5_000

    # The engine's vt-screen codec emits semantic prompt events directly.
    assert_receive {:mitos, ^session_id, {:prompt, {:final_answer_candidate, "hello"}}}, 5_000
  end

  test "unknown provider is reported by the engine, not a BEAM registry" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    Mitos.PubSub.subscribe(session_id)

    assert {:ok, run} =
             Mitos.Agent.start(provider: "does-not-exist", task: "x", session_id: session_id)

    on_exit(fn ->
      try do
        Mitos.Orchestrator.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    assert run.provider == "does-not-exist"

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ERROR",
                       "kind" => "launch",
                       "detail" => "ProviderNotRegistered"
                     }}},
                   5_000
  end

  test "approval prompts bubble up as action requests" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    Mitos.PubSub.subscribe(session_id)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "FAKE_APPROVAL: Run tests? [y/N]",
        session_id: session_id,
        transport_order: ["stdio-json"]
      )

    on_exit(fn ->
      try do
        Mitos.Orchestrator.stop_session(session_id)
      catch
        :exit, _ -> :ok
      end
    end)

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ACTION_REQUESTED",
                       "action" => "terminal_input",
                       "origin" => "terminal_scrape",
                       "action_id" => action_id,
                       "request_id" => request_id,
                       "producer_request_id" => producer_request_id,
                       "context" => context
                     }}},
                   5_000

    assert action_id == request_id
    assert action_id =~ "request-"
    assert producer_request_id =~ "terminal-scrape-"
    assert context =~ "FAKE_APPROVAL"
  end

  test "terminal scrape verdicts route back to the sidecar with canonical ids" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "Run tests? [y/N]",
        session_id: session_id,
        transport_order: ["pty"],
        idle_ms: 50
      )

    on_exit(fn -> stop_session(session_id) end)

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ACTION_REQUESTED",
                       "origin" => "terminal_scrape",
                       "request_id" => request_id
                     }}},
                   5_000

    Mitos.Session.authorize(run.pid, request_id, "y\n")

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "STRUCTURED_OUTPUT",
                       "kind" => "authorization",
                       "origin" => "terminal_scrape",
                       "request_id" => ^request_id,
                       "action_id" => ^request_id,
                       "decision" => "allow",
                       "token" => "y\n"
                     }}},
                   5_000
  end

  test "fast-path approvals are persisted as requested and decided" do
    session_id = "agent-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("fast-path-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, _run} =
      Mitos.Agent.start(
        provider: "fake",
        task: "FAKE_APPROVAL: Run tests? [y/N]",
        session_id: session_id,
        transport_order: ["stdio-json"],
        trace_base_dir: trace_base_dir,
        policy: %{tier2_fast_path_rules: ["Run tests?"]}
      )

    on_exit(fn -> stop_session(session_id) end)

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "ACTION_REQUESTED",
                       "request_id" => request_id,
                       "fast_path" => true,
                       "auto_decision" => "authorize"
                     }}},
                   5_000

    assert_receive {:mitos, ^session_id,
                    {:event,
                     %{
                       "type" => "AUTHORITY_VERDICT",
                       "request_id" => ^request_id,
                       "decision" => "authorize",
                       "payload" => %{"fast_path" => true}
                     }}},
                   5_000

    assert_eventually(1_000, fn -> Registry.lookup(Mitos.SessionRegistry, session_id) == [] end)

    approvals = File.read!(Path.join([trace_base_dir, ".runs", session_id, "approvals.jsonl"]))
    assert approvals =~ ~s("type":"approval_requested")
    assert approvals =~ ~s("type":"approval_decision")
    assert approvals =~ request_id
    assert approvals =~ ~s("decision":"authorize")
  end

  defp stop_session(session_id) do
    try do
      Mitos.Orchestrator.stop_session(session_id)
    catch
      :exit, _ -> :ok
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
end
