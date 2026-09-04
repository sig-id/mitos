defmodule ScreenOnlyHarness do
  @moduledoc false
  # A harness observable only through C1 + the terminal: no headless stream, no
  # session record. 14 §4 says it may be watched, never left, and never served
  # to a Market run — a buyer's proof cannot be built from a screen.
  def observation do
    %{
      structured_output: :none,
      session_artifacts: nil,
      interactive: true,
      modal_classifier: Mitos.Observation.Classifier.Heuristic,
      pty_control: true,
      resume: :none,
      think_time_p95_ms: 10_000
    }
  end
end

defmodule Mitos.ObservationTest do
  @moduledoc """
  The observation plane (`14`) as executable checks.

  The load-bearing one is `describe "the terminal"`: it pins the property that
  lets `01 §8.6`'s ban be lifted — a snapshot is a pure function of the spool
  prefix, so a screen read is replayable evidence rather than an unrepeatable
  observation. If that stops holding, C3 stops composing with audit and proof
  and must be banned again.
  """
  use ExUnit.Case, async: true

  alias Mitos.Observation.{Classifier, Clock, Contract, Session, Terminal, Vt}

  @fast %{snap: 10, steer: 25, block: 60}

  describe "the VT parser (14 §3.1)" do
    test "is a pure function of the byte prefix, however the bytes arrive" do
      frames = "\e[2J\e[1;1Hmenu\r\n1. Yes\r\n2. No\r\n\e[?25l"

      whole = Vt.feed(Vt.new(6, 20), frames)

      byte_at_a_time =
        frames
        |> :binary.bin_to_list()
        |> Enum.reduce(Vt.new(6, 20), fn b, vt -> Vt.feed(vt, <<b>>) end)

      # This is the whole audit story: a read boundary cannot change what the
      # screen said.
      assert Vt.digest(whole) == Vt.digest(byte_at_a_time)
      assert Vt.render(whole) == Vt.render(byte_at_a_time)
    end

    test "holds a split escape sequence instead of mis-parsing its halves" do
      partial = Vt.feed(Vt.new(4, 20), "abc\e[2;")
      refute Vt.stable?(partial)

      completed = Vt.feed(partial, "3HZ")
      assert Vt.stable?(completed)
      assert Enum.at(Vt.render(completed), 1) == "  Z"
    end

    test "holds a truncated UTF-8 sequence rather than emitting a broken cell" do
      <<a, b, c>> = "é" <> "x"
      partial = Vt.feed(Vt.new(2, 10), <<a>>)
      refute Vt.stable?(partial)

      done = Vt.feed(partial, <<b, c>>)
      assert Vt.stable?(done)
      assert Enum.at(Vt.render(done), 0) == "éx"
    end

    test "alt-screen entry is a different modal world, not more scrollback" do
      vt = Vt.feed(Vt.new(4, 20), "scrollback\e[?1049h")
      assert vt.alt_screen
      assert Vt.render(vt) == ["", "", "", ""]
    end
  end

  describe "the quiescence clock (14 §2)" do
    test "a repeated exec argv does not reset the window" do
      {:ok, clock} = Clock.start_link(thresholds: @fast)

      # The polling-loop false negative: `sleep 1; curl` executes forever and
      # progresses never. A naive idle detector calls this busy for ever.
      Enum.each(1..5, fn _ ->
        Clock.bump(clock, :exec, ["curl", "https://example.test"])
        Process.sleep(15)
      end)

      assert Clock.read(clock).quiet_for_ms >= 60
      assert Clock.read(clock).distinct_execs_since == 1
    end

    test "a distinct exec argv does reset the window" do
      {:ok, clock} = Clock.start_link(thresholds: @fast)
      Process.sleep(40)
      Clock.bump(clock, :exec, ["make", "build"])
      Process.sleep(5)

      assert Clock.read(clock).quiet_for_ms < 40
      assert Clock.read(clock).distinct_execs_since == 1
    end

    test "crossing T_block fires blocked_on_human exactly once per window" do
      me = self()

      {:ok, clock} =
        Clock.start_link(
          thresholds: @fast,
          on_block: fn w -> send(me, {:blocked, w}) end,
          on_effect: fn fields -> send(me, {:effect, fields[:type]}) end
        )

      assert_receive {:blocked, %{quiet_for_ms: ms}}, 1_000
      assert ms >= @fast.block
      refute_receive {:blocked, _}, 200

      # Each level is an Effect, at most once per window (14 §2.1).
      assert_received {:effect, :quiescent}

      Clock.bump(clock, :write, "note.txt")
      assert_receive {:blocked, _}, 1_000
    end

    test "refuses a threshold order that would steer inside a repaint" do
      assert_raise ArgumentError, ~r/14 §2.1/, fn ->
        Clock.thresholds(%{snap: 900, steer: 100, block: 50})
      end
    end
  end

  describe "the terminal (14 §3.1)" do
    test "a snapshot replays from the spool and verifies its digest" do
      {:ok, clock} = Clock.start_link(thresholds: @fast)
      {:ok, term} = start_terminal(clock)

      Terminal.feed(term, "\e[2J\e[1;1Hchoose:\r\n1. Yes\r\n2. No\r\n")
      Process.sleep(@fast.snap * 2)

      {:ok, grid} = Terminal.snapshot(term)
      assert "choose:" in grid.lines

      {:ok, replayed} = Terminal.replay(Terminal.spool_path(term), rows: 24, cols: 80)
      assert replayed.digest == grid.digest
      assert replayed.lines == grid.lines
    end

    test "refuses to materialize a torn frame" do
      {:ok, clock} = Clock.start_link(thresholds: @fast)
      {:ok, term} = start_terminal(clock)

      Terminal.feed(term, "abc\e[2;")
      Process.sleep(@fast.snap * 2)

      # Stable in time, but the parser is mid-sequence: not a document.
      assert {:error, :unstable} = Terminal.snapshot(term)
    end

    test "refuses to read a moving screen" do
      {:ok, clock} = Clock.start_link(thresholds: %{snap: 400, steer: 800, block: 5_000})
      {:ok, term} = start_terminal(clock)

      Terminal.feed(term, "repainting")
      assert {:error, :busy} = Terminal.snapshot(term)
    end

    test "an unguarded read must be asked for explicitly" do
      {:ok, term} = start_terminal(nil)
      Terminal.feed(term, "hello")

      assert {:error, :no_clock} = Terminal.snapshot(term)
      assert {:ok, grid} = Terminal.snapshot(term, unguarded: true)
      assert "hello" in grid.lines
    end

    test "secrets are redacted on the way to the durable spool" do
      {:ok, term} = start_terminal(nil)
      Terminal.feed(term, "export KEY=sk-abcdefghijklmnopqrstuvwx\r\n")

      spool = File.read!(Terminal.spool_path(term))
      refute spool =~ "sk-abcdefghijklmnopqrstuvwx"
      assert spool =~ "sk-<redacted>"
    end
  end

  describe "the modal classifier (14 §3.2)" do
    test "recognizes the shapes it claims and refuses to guess at the rest" do
      assert kind("Do you want to proceed?\n  1. Yes\n> 2. No") == :menu
      assert kind("Overwrite the file? (y/N)") == :prompt
      assert kind("user@host:~/proj $") == :idle

      # The safety property: an unrecognized screen escalates rather than
      # producing a keystroke nobody understood.
      assert kind("▚▚▚ ░░░ ▚▚▚") == :unknown
    end

    test "reports the selected option of a menu" do
      c = classify("pick one\n  1. Alpha\n❯ 2. Beta\n  3. Gamma")
      assert c.kind == :menu
      assert Enum.map(c.options, & &1.id) == ["1", "2", "3"]
      assert c.selected == 1
    end

    test "declares a transition for every command it can encode" do
      c = classify("Continue?\n  1. Yes\n  2. No")
      assert {:ok, bytes} = Classifier.Heuristic.encode(c, {:select, "2"})
      assert bytes =~ "\r"
      assert {:ok, _expectation} = Classifier.Heuristic.transition(c, {:select, "2"})

      # A command it cannot predict is refused, not attempted (14 §3.3).
      assert :unknown = Classifier.Heuristic.transition(c, :something_novel)
    end
  end

  describe "the observation matrix and its contract (14 §4, §5)" do
    test "every registered adapter passes its static contract" do
      results = Contract.check_all()

      for r <- results do
        assert r.ok, "#{inspect(r.harness)} failed: #{inspect(Contract.failed(r.findings))}"
      end
    end

    test "an adapter with no structured channel may be watched but not left" do
      # Every shipped adapter has a stream, so the C1+C3-only posture needs a
      # stub — but it is the posture 14 §4 exists to refuse, so it is worth
      # pinning that the refusal actually happens.
      assert Mitos.Harness.channels(Mitos.Harness.Fake).unattended_ok

      channels = Mitos.Harness.channels(ScreenOnlyHarness)
      refute channels.unattended_ok
      refute channels.market_ok
      assert channels.c3
      assert channels.reason == :no_structured_channel
    end

    test "T_block is derived from observed think time, never a fixed guess" do
      assert Mitos.Harness.block_threshold(Mitos.Harness.ClaudeCode) >
               Mitos.Harness.block_threshold(Mitos.Harness.Fake)
    end

    test "quarantine drops the unattended grant, on the record" do
      Mitos.Autonomy.quarantine(Mitos.Harness.Fake, [:contract_failed])
      on_exit(fn -> Mitos.Autonomy.unquarantine(Mitos.Harness.Fake) end)

      assert {:zero, reasons} = Mitos.Autonomy.grant([], [Mitos.Harness.Fake])
      assert :harness_quarantined in reasons
    end
  end

  describe "session artifacts (14 §1.1)" do
    test "expands a path template against a mitos-controlled sandbox" do
      resolved =
        Session.resolve("{home}/.claude/projects/{project_slug}/*.jsonl", %{
          home: "/sandbox/home",
          workspace: "/sandbox/workspace"
        })

      assert resolved == "/sandbox/home/.claude/projects/-sandbox-workspace/*.jsonl"
    end

    test "tails an interactive session's own record into the normalized vocabulary" do
      home = Path.join([Mitos.data_dir(), "obs", Mitos.id()])
      workspace = Path.join(home, "workspace")
      project = Path.join([home, ".claude", "projects", Session.project_slug(workspace)])
      File.mkdir_p!(project)
      artifact = Path.join(project, "session.jsonl")
      File.write!(artifact, "")

      {:ok, clock} = Clock.start_link(thresholds: @fast)

      {:ok, session} =
        Session.start_link(
          harness_mod: Mitos.Harness.ClaudeCode,
          sandbox: %{home: home, workspace: workspace},
          clock: clock,
          poll_ms: 10
        )

      File.write!(
        artifact,
        Mitos.JSON.encode!(%{
          "type" => "assistant",
          "message" => %{"content" => [%{"type" => "text", "text" => "planning"}]}
        }) <> "\n",
        [:append]
      )

      Process.sleep(40)
      events = Session.sync(session)

      # An unmodified interactive session, observed structurally — no screen
      # scraping and no mode change (14 §1.1).
      assert Enum.any?(events, &(&1.type == :assistant_text and &1[:text] == "planning"))

      # And the append counted as activity: a thinking harness is not blocked.
      assert Clock.read(clock).session_appends_since > 0
    end
  end

  defp start_terminal(clock) do
    Terminal.start_link(
      run_id: "test/" <> Mitos.id(),
      stage_id: "s",
      clock: clock,
      thresholds: @fast
    )
  end

  defp classify(text) do
    vt = Vt.feed(Vt.new(24, 80), String.replace(text, "\n", "\r\n"))
    {:ok, grid} = Terminal.replay_from_vt(vt)
    Classifier.Heuristic.classify(grid)
  end

  defp kind(text), do: classify(text).kind
end
