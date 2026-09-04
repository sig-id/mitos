defmodule Mitos.PtyTest do
  @moduledoc """
  The local PTY transport (`14 §3`, `01 §7`) driving a real interactive process
  end to end: bytes in, a stable snapshot at a quiescence edge, a verified
  steer, and a replay that recomputes the same grid from the spool.
  """
  use ExUnit.Case, async: true

  alias Mitos.Observation.{Classifier, Clock, Pty, Terminal}

  @fast %{snap: 30, steer: 60, block: 5_000}

  setup do
    unless Pty.available?(), do: raise("script(1) is required for the pty transport test")
    :ok
  end

  test "reads a real pty, snapshots at a quiescence edge, and replays from the spool" do
    {:ok, clock} = Clock.start_link(thresholds: @fast)

    {:ok, pty} =
      Pty.open("/bin/sh", ["-c", "printf 'Apply this edit?\\r\\n  1. Yes\\r\\n  2. No\\r\\n'; sleep 3"],
        run_id: "test/" <> Mitos.id(),
        stage_id: "pty",
        clock: clock,
        thresholds: @fast
      )

    on_exit(fn -> Pty.close(pty) end)
    terminal = Pty.terminal(pty)

    assert {:ok, grid} = Terminal.snapshot(terminal, wait: 3_000, require_content: true)
    assert "Apply this edit?" in grid.lines

    # The audit property, against real terminal bytes rather than a fixture:
    # the grid is a pure function of the spool prefix (14 §3.1).
    {:ok, replayed} = Terminal.replay(Terminal.spool_path(terminal), rows: 24, cols: 80)
    assert replayed.digest == grid.digest

    assert Classifier.Heuristic.classify(grid).kind == :menu
  end

  test "the write half reaches the process, and the steer verifies the outcome" do
    {:ok, clock} = Clock.start_link(thresholds: @fast)

    # An interactive prompt: it waits for a line, then repaints.
    script = ~S"""
    printf 'Overwrite the file? (y/n) '
    read answer
    printf '\r\n\033[2J\033[1;1Hoverwrote with %s\r\n' "$answer"
    sleep 3
    """

    {:ok, pty} =
      Pty.open("/bin/sh", ["-c", script],
        run_id: "test/" <> Mitos.id(),
        stage_id: "pty",
        clock: clock,
        thresholds: @fast
      )

    on_exit(fn -> Pty.close(pty) end)
    terminal = Pty.terminal(pty)

    # Wait for the prompt to settle before steering — the transport keeps
    # draining while we block, which is the point of it owning the port.
    assert {:ok, before} = Terminal.snapshot(terminal, wait: 3_000, require_content: true)
    assert Classifier.Heuristic.classify(before).kind == :prompt

    me = self()

    ctx =
      Pty.steer_ctx(pty, :approve,
        clock: clock,
        stage_id: "pty",
        thresholds: @fast,
        settle_ms: 3_000,
        append: fn fields -> send(me, {:effect, fields[:type]}) end
      )

    assert {:ok, %{before: before_d, after: after_d}} = Mitos.Steer.apply(ctx)
    assert before_d != after_d
    assert_received {:effect, :steer_applied}

    {:ok, grid} = Terminal.snapshot(terminal, unguarded: true)
    assert Enum.any?(grid.lines, &(&1 =~ "overwrote with y"))
  end
end
