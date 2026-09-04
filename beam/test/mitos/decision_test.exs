defmodule Mitos.DecisionTest do
  @moduledoc """
  The decision plane (`15`) as executable checks.

  The load-bearing group is `describe "silence"`. Inverting the fail-safe /
  fail-secure split is the most dangerous defect available in this design — a
  default on an irreversible act means silence authorizes it — so the split is
  pinned here in both directions, and pinned as a *refusal to construct* rather
  than a check somewhere downstream that could be bypassed.
  """
  use ExUnit.Case, async: false

  alias Mitos.Decision
  alias Mitos.Decision.{Card, Policy}
  alias Mitos.Sink.Decide

  setup do
    Decision.clear()
    Decide.Destination.clear()
    :ok
  end

  describe "reversibility (15 §2)" do
    test "classifies on consequence, not on mechanism" do
      ws = "/tmp/ws"

      # Same mechanism (a write), different consequence.
      assert Policy.classify(%{kind: :workspace_write, paths: ["/tmp/ws/a.ex"], workspace: ws}) == :internal
      assert Policy.classify(%{kind: :workspace_write, paths: ["/etc/hosts"], workspace: ws}) == :recoverable

      # Same category (a push), different reach.
      assert Policy.classify(%{kind: :git_push, remote: "mirror", grants: %{mirrors: ["mirror"]}}) == :recoverable
      assert Policy.classify(%{kind: :git_push, remote: "origin"}) == :external

      assert Policy.classify(%{kind: :publish}) == :external
      assert Policy.classify(%{kind: :payment}) == :external
    end

    test "unclassifiable is not permission" do
      assert Policy.classify(%{kind: :something_nobody_modelled}) == :external
      assert Policy.classify(%{}) == :external
    end

    test "an explicit approval demand only ever escalates" do
      assert Policy.classify(%{kind: :workspace_write, paths: [], requires_approval: true}) == :external
    end

    test "the interrupt budget: internal acts never ask" do
      assert Policy.disposition(%{kind: :workspace_write, paths: ["/tmp/ws/a"], workspace: "/tmp/ws"}) == :act
      assert Policy.disposition(%{kind: :sandbox_write}) == {:review, :recoverable}
      assert Policy.disposition(%{kind: :publish}) == {:escalate, :external}

      # Unguessable by construction, whatever the class would have been.
      assert Policy.disposition(%{kind: :missing_input}) == {:escalate, :external}
      assert Policy.disposition(%{kind: :judgment}) == {:escalate, :external}
    end
  end

  describe "silence (15 §4)" do
    test "an external act may not carry a default, and the card refuses to exist" do
      assert {:error, :external_may_not_default} =
               Card.new(
                 run_id: "r",
                 kind: :approve,
                 reversibility: :external,
                 question: "Push to origin?",
                 default: "approve",
                 ttl_ms: 60_000
               )
    end

    test "a reversible act must carry a default, or the interrupt becomes a stall" do
      assert {:error, :reversible_requires_default} =
               Card.new(run_id: "r", kind: :approve, reversibility: :internal, question: "Write the file?")
    end

    test "default and ttl must agree in both directions" do
      assert {:error, :default_and_ttl_must_agree} =
               Card.new(
                 run_id: "r",
                 reversibility: :internal,
                 question: "q",
                 default: "approve",
                 ttl_ms: nil
               )
    end

    test "policy hands external acts no default even when one is asked for" do
      # A configurable fail-secure is a fail-safe with extra steps.
      assert Policy.silence(:external, default: "approve", ttl_ms: 1_000) == {nil, nil}
      assert {default, ttl} = Policy.silence(:internal, [])
      assert default == "approve"
      assert is_integer(ttl)
    end

    test "a default fires on the clock, and the log keeps that it was silence" do
      run_id = start_run()

      {:ok, card} =
        Card.new(
          run_id: run_id,
          reversibility: :internal,
          question: "Keep going?",
          options: [
            %{id: "yes", label: "Yes", consequence: "proceeds"},
            %{id: "no", label: "No", consequence: "stops"}
          ],
          default: "yes",
          ttl_ms: 60
        )

      {:ok, _} = Decision.raise_card(card, to: "operator")
      assert await_effect(run_id, :approval_resolved)
      assert :decision_defaulted in effects_of(run_id)

      resolved = effect(run_id, :approval_resolved)
      # `decision_precision` (16 §6) is computed from exactly this distinction,
      # so it must survive on the log rather than look like a human answer.
      assert resolved.payload["via"] == ":default_expired"
      assert resolved.payload["choice"] == "yes"
    end

    test "an external card arms no timer at all" do
      run_id = start_run()

      {:ok, card} =
        Card.new(
          run_id: run_id,
          reversibility: :external,
          question: "Publish the package?",
          options: [%{id: "publish", label: "Publish", consequence: "goes live"}]
        )

      {:ok, _} = Decision.raise_card(card, to: "operator")
      Process.sleep(200)

      # No timer was ever armed, so there is nothing that could fire late.
      refute :decision_defaulted in effects_of(run_id)
      assert [%Card{}] = Decision.open(run_id)
    end
  end

  describe "the card (15 §3)" do
    test "keeps the question answerable in a minute" do
      long = String.duplicate("a", 201)

      assert {:error, {:question_too_long, 201}} =
               Card.new(run_id: "r", reversibility: :external, question: long)

      assert {:error, :at_most_one_recommendation} =
               Card.new(
                 run_id: "r",
                 reversibility: :external,
                 question: "q",
                 options: [
                   %{id: "a", label: "A", recommended: true},
                   %{id: "b", label: "B", recommended: true}
                 ]
               )
    end

    test "round-trips through the log payload" do
      {:ok, card} =
        Card.new(
          run_id: "r",
          reversibility: :external,
          question: "Deploy?",
          options: [%{id: "yes", label: "Yes", consequence: "ships"}],
          blast_radius: %{destinations: ["prod"]}
        )

      back = Card.from_payload("r", Card.to_payload(card))
      assert back.decision_id == card.decision_id
      assert back.reversibility == :external
      assert back.default == nil
      assert hd(back.options).consequence == "ships"
    end
  end

  describe "delivery and resolution (15 §5)" do
    test "the rendered card says what silence will do" do
      run_id = start_run()

      {:ok, card} =
        Card.new(
          run_id: run_id,
          reversibility: :external,
          question: "Force-push to main?",
          options: [%{id: "yes", label: "Yes", consequence: "rewrites remote history"}]
        )

      {:ok, _} = Decision.raise_card(card, to: "phone")
      [delivered] = Decide.Destination.collect("phone")

      assert delivered.waiting_for_you
      assert delivered.text =~ "Waiting for you"
      assert delivered.text =~ "rewrites remote history"
      # Folded state only — a transcript can't reach the card (15 §3.1).
      refute Map.has_key?(delivered, :rationale)
    end

    test "identical cards across workers collapse and resolve together" do
      run_id = start_run()

      cards =
        for i <- 1..3 do
          {:ok, c} =
            Card.new(
              run_id: run_id,
              stage_id: "worker#{i}",
              reversibility: :external,
              question: "Missing GITHUB_TOKEN. Supply it?",
              options: [%{id: "supply", label: "Supply", consequence: "the run continues"}]
            )

          c
        end

      Enum.each(cards, &Decision.raise_card(&1, to: "phone"))

      # One dedupe key, one push (15 §5.3).
      assert length(Decide.Destination.collect("phone")) == 1

      :ok = Decision.resolve(hd(cards).decision_id, %{resolver: "ben", choice: "supply", via: "cli"})

      # ...and one answer clears every member.
      assert Decision.open(run_id) == []
    end

    test "an unauthenticated or out-of-tenant resolution is rejected, never applied" do
      run_id = start_run()

      {:ok, card} =
        Card.new(
          run_id: run_id,
          tenant: %{"org" => "acme"},
          reversibility: :external,
          question: "Approve?",
          options: [%{id: "yes", label: "Yes", consequence: "proceeds"}]
        )

      {:ok, _} = Decision.raise_card(card)

      assert {:error, :unauthenticated_resolver} =
               Decision.resolve(card.decision_id, %{choice: "yes"})

      assert {:error, :out_of_tenant} =
               Decision.resolve(card.decision_id, %{resolver: "mallory", tenant: %{"org" => "other"}, choice: "yes"})

      assert {:error, :not_an_option} =
               Decision.resolve(card.decision_id, %{resolver: "ben", choice: "something_else"})

      # Still open, and the rejections are on the record.
      assert [_] = Decision.open(run_id)
      assert :decision_rejected in effects_of(run_id)
    end

    test "the connector round trip is the same path a phone button uses" do
      run_id = start_run()

      {:ok, card} =
        Card.new(
          run_id: run_id,
          reversibility: :external,
          question: "Approve the egress?",
          options: [%{id: "allow", label: "Allow", consequence: "the call goes out"}]
        )

      {:ok, _} = Decision.raise_card(card)

      assert {:ok, _} =
               Mitos.Connector.Decision.ingest(%{}, %{
                 "decision_id" => card.decision_id,
                 "resolver" => "ben",
                 "choice" => "allow",
                 "via" => "telegram"
               })

      assert Decision.open(run_id) == []
      assert effect(run_id, :approval_resolved).payload["via"] == "telegram"

      # Idempotent: a re-delivered press finds nothing to apply twice.
      assert {:error, :unknown_decision} =
               Mitos.Connector.Decision.ingest(%{}, %{
                 "decision_id" => card.decision_id,
                 "resolver" => "ben",
                 "choice" => "allow"
               })
    end
  end

  # --- helpers ---

  defp start_run do
    run_id = "test/" <> Mitos.id("run")
    {:ok, _} = Mitos.Run.start_run(run_id: run_id, pipeline: "# Pipeline: park\n", capabilities: %{})
    run_id
  end

  # Poll rather than sleep: the append may be detached (the run has released its
  # writer), and a fixed sleep would be a flake waiting to happen.
  defp await_effect(run_id, type, timeout \\ 2_000) do
    deadline = System.monotonic_time(:millisecond) + timeout

    Stream.repeatedly(fn ->
      if type in effects_of(run_id) do
        true
      else
        Process.sleep(20)
        false
      end
    end)
    |> Enum.find(fn found -> found or System.monotonic_time(:millisecond) > deadline end)
  end

  defp effects_of(run_id) do
    run_id |> Mitos.Log.read() |> Enum.filter(&match?(%Mitos.Event.Effect{}, &1)) |> Enum.map(& &1.type)
  end

  defp effect(run_id, type) do
    run_id |> Mitos.Log.read() |> Enum.find(&match?(%Mitos.Event.Effect{type: ^type}, &1))
  end
end
