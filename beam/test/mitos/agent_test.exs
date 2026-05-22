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

  test "fake agent run: adapter → session → sidecar → prompt watcher" do
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
    assert run.adapter == "fake.pty"
    assert run.mode == :pty
    assert run.session_id == session_id

    # The sidecar comes up and the agent's output flows through.
    assert_receive {:mitos, ^session_id, {:event, %{"type" => "hello"}}}, 5_000

    # The PromptWatcher turns the idle screen into a semantic prompt event,
    # picking up the fake adapter's FAKE_FINAL marker.
    assert_receive {:mitos, ^session_id, {:prompt, {:final_answer_candidate, "hello"}}}, 5_000
  end

  test "unknown provider is rejected before any session starts" do
    assert {:error, :provider_not_registered} =
             Mitos.Agent.start(provider: "does-not-exist", task: "x")
  end
end
