defmodule Mitos.Orchestrator.AgenticTest do
  @moduledoc """
  04 §3: the agentic policy maps ONE model call → ONE Decision, and degrades to
  Linear on a model outage. We inject a fake model client to drive non-linear
  decisions (finish, targeted advance) and assert the fallback.
  """
  use ExUnit.Case, async: false

  alias Mitos.Event.Decision
  alias Mitos.Orchestrator.Policy.Agentic

  # a fake model client returning a canned decision
  defmodule FinishModel do
    @behaviour Mitos.Orchestrator.Model
    @impl true
    def complete(_prompt, _opts),
      do: {:ok, %{"action" => "finish", "rationale" => "goal met"}}
  end

  defmodule TargetedAdvanceModel do
    @behaviour Mitos.Orchestrator.Model
    @impl true
    def complete(_prompt, _opts),
      do: {:ok, %{"action" => "advance", "target" => "review", "rationale" => "jump to review"}}
  end

  defp ctx do
    intent = Mitos.Pipeline.parse(Mitos.Fixtures.pipeline(["plan", "execute", "review"]))
    stages = %{"plan" => %{status: :finished, result: %{"signal" => "ship"}}, "execute" => %{status: :finished, result: %{"signal" => "ship"}}}

    %{
      intent: intent,
      state: %Mitos.Run.State{status: :running, stages: stages, stage_order: ["plan", "execute"], decisions: [], effects: [], last_seq: 5},
      budget: %{"usd" => 10.0},
      remaining: %{usd: 5.0, tokens: 1000, wall_ms: 60_000},
      capabilities: %{"plan" => nil, "execute" => nil, "review" => nil},
      config: %{}
    }
  end

  setup do
    on_exit(fn -> Application.put_env(:mitos, :orchestrator_model, Mitos.Orchestrator.Model.Stub) end)
    :ok
  end

  test "with the Stub (default) it degrades to Linear: advances the next unfinished stage" do
    Application.put_env(:mitos, :orchestrator_model, Mitos.Orchestrator.Model.Stub)
    %Decision{type: :advance, target: "review"} = Agentic.decide(ctx())
  end

  test "a model returning finish produces a finish Decision" do
    Application.put_env(:mitos, :orchestrator_model, FinishModel)
    %Decision{type: :finish, rationale: "goal met"} = Agentic.decide(ctx())
  end

  test "a model returning a targeted advance produces that Decision" do
    Application.put_env(:mitos, :orchestrator_model, TargetedAdvanceModel)
    %Decision{type: :advance, target: "review", params: %{"capability" => "review"}} =
      Agentic.decide(ctx())
  end

  test "budget exhaustion bypasses the model (Linear floor)" do
    Application.put_env(:mitos, :orchestrator_model, FinishModel)
    exhausted = %{ctx() | remaining: %{usd: 0.0, tokens: 0, wall_ms: 0}}
    %Decision{type: :abort} = Agentic.decide(exhausted)
  end
end
