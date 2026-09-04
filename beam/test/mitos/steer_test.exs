defmodule Mitos.SteerTest do
  @moduledoc """
  Control as a verified transaction (`14 §3.3`).

  The two properties pinned here are the ones that separate this from the
  implementation that failed: a write happens only at a quiescence edge with a
  predicted outcome, and a write whose outcome did not match is **never retried
  automatically** — a screen that went somewhere unexpected means the classifier
  is wrong about this build, and pressing another key is how the wrong keystroke
  lands in a prompt nobody understood.
  """
  use ExUnit.Case, async: true

  alias Mitos.Observation.{Clock, Terminal}
  alias Mitos.Steer

  @fast %{snap: 10, steer: 20, block: 5_000}

  @menu "\e[2J\e[1;1HApply this edit?\r\n  1. Yes\r\n  2. No\r\n"
  @after_apply "\e[2J\e[1;1Hediting src/main.ex\r\n"

  test "writes only after a predicted transition, and records both digests" do
    {ctx, _} = session(@menu, fn _bytes, term -> Terminal.feed(term, @after_apply) end)

    assert {:ok, %{before: before_digest, after: after_digest}} = Steer.apply(ctx)
    assert before_digest != after_digest

    assert_received {:effect, fields}
    assert fields[:type] == :steer_applied
    assert fields[:payload]["before_digest"] == before_digest
    assert fields[:payload]["after_digest"] == after_digest
    assert fields[:payload]["before_kind"] == "menu"
  end

  test "a screen that did not move as predicted is rejected, not retried" do
    # The TUI ignores the keystroke — the classifier is wrong about this build.
    {ctx, agent} = session(@menu, fn _bytes, _term -> :ok end)

    assert {:error, {:escalate, :transition_mismatch}} = Steer.apply(ctx)

    assert_received {:effect, fields}
    assert fields[:type] == :steer_rejected
    assert fields[:payload]["reason"] == "transition_mismatch"

    # One command byte-string, then a single ESC to back out of the modal —
    # and no second attempt at a different key.
    assert [_command, "\e"] = Agent.get(agent, & &1)
  end

  test "refuses to write to a screen nobody understood" do
    {ctx, agent} = session("▚▚▚ ░░░ ▚▚▚\r\n", fn _b, _t -> :ok end)

    assert {:error, {:escalate, :unknown_modal}} = Steer.apply(ctx)
    # Nothing was written at all: `:unknown` escalates rather than guessing.
    assert Agent.get(agent, & &1) == []
  end

  test "refuses a command whose outcome the classifier cannot predict" do
    {ctx, agent} = session(@menu, fn _b, _t -> :ok end)
    ctx = %{ctx | command: :some_novel_command}

    assert {:error, {:escalate, _}} = Steer.apply(ctx)
    assert Agent.get(agent, & &1) == []
  end

  test "will not write into a repaint" do
    {:ok, clock} = Clock.start_link(thresholds: %{snap: 400, steer: 800, block: 5_000})
    {:ok, term} = Terminal.start_link(run_id: "test/" <> Mitos.id(), stage_id: "s", clock: clock, thresholds: %{snap: 400, steer: 800, block: 5_000})
    {:ok, agent} = Agent.start_link(fn -> [] end)

    Terminal.feed(term, @menu)

    ctx = %{
      terminal: term,
      command: :approve,
      settle_ms: 50,
      thresholds: %{snap: 400, steer: 800, block: 5_000},
      write: fn bytes -> Agent.update(agent, &(&1 ++ [bytes])) end
    }

    assert {:error, {:escalate, :busy}} = Steer.apply(ctx)
    assert Agent.get(agent, & &1) == []
  end

  # A terminal at `screen`, plus a `write` that feeds back whatever the TUI is
  # supposed to do in response.
  defp session(screen, respond) do
    me = self()
    {:ok, clock} = Clock.start_link(thresholds: @fast)

    {:ok, term} =
      Terminal.start_link(run_id: "test/" <> Mitos.id(), stage_id: "s", clock: clock, thresholds: @fast)

    {:ok, agent} = Agent.start_link(fn -> [] end)
    Terminal.feed(term, screen)

    ctx = %{
      terminal: term,
      clock: clock,
      command: :approve,
      stage_id: "s",
      settle_ms: 500,
      thresholds: @fast,
      append: fn fields -> send(me, {:effect, fields}) end,
      write: fn bytes ->
        Agent.update(agent, &(&1 ++ [bytes]))
        respond.(bytes, term)
        :ok
      end
    }

    {ctx, agent}
  end
end
