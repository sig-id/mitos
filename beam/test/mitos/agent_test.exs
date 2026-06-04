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
                       "action_id" => action_id,
                       "context" => context
                     }}},
                   5_000

    assert action_id =~ "action-"
    assert context =~ "FAKE_APPROVAL"
  end
end
