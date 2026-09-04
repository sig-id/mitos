defmodule ParkingPolicy do
  @moduledoc false
  # Parks on the first turn and stays parked (04 §3.3 `request_approval`), so a
  # test can act on a LIVE run — the state a supervisor and a decision card both
  # exist to serve.
  @behaviour Mitos.Orchestrator.Policy

  @impl true
  def decide(_ctx) do
    Mitos.Event.Decision.new(
      actor: "orchestrator",
      type: :request_approval,
      params: %{"question" => "hold here", "blocking" => true, "stakes" => "test"},
      inputs_hash: "park",
      rationale: "test policy parks",
      budget: %{}
    )
  end
end

defmodule Mitos.AutonomyTest do
  @moduledoc """
  The autonomy plane (`16`) as executable checks.

  The load-bearing group is `describe "the acceptance predicate"`. It is the
  only ground truth a long-horizon run has: without an oracle the run cannot
  reach, `rewind`, `predicate_stall`, restart intensity, and every metric in
  `16 §6` are measuring the model's opinion of itself.
  """
  use ExUnit.Case, async: false

  alias Mitos.{Acceptance, Autonomy, Checkpoint, Fixtures, Run, Supervision}
  alias Mitos.Capability.Manifest
  alias Mitos.Event.{Decision, Effect}

  @moduletag timeout: 120_000

  describe "the acceptance predicate (16 §3.2)" do
    test "runs outside the workspace, against an oracle restored from the capability" do
      # The capability ships the oracle; the workspace ships a subverted copy.
      cap = capability_with_oracle("exit 0")
      ws = workspace_with(%{"oracle/check.sh" => "#!/bin/sh\nexit 1\n"})

      outcome =
        Acceptance.run(%{
          manifest: Manifest.load!(cap),
          run_id: "test/" <> Mitos.id(),
          stage_id: "verify",
          workspace: ws,
          diff_paths: []
        })

      # "Make the tests pass" and "make the runner lie" are the same gradient
      # until the oracle is out of reach. It is out of reach.
      assert outcome.verdict == :pass
    end

    test "a diff that touches the oracle is an event, not a judgment call" do
      cap = capability_with_oracle("exit 0")
      ws = workspace_with(%{})
      me = self()

      outcome =
        Acceptance.run(%{
          manifest: Manifest.load!(cap),
          run_id: "test/" <> Mitos.id(),
          stage_id: "verify",
          workspace: ws,
          diff_paths: ["src/main.ex", "oracle/check.sh"],
          append: fn fields -> send(me, {:effect, fields[:type], fields[:payload]}) end
        })

      assert outcome.tampered
      assert outcome.verdict == :fail
      assert_received {:effect, :oracle_tampered, %{"touched" => ["oracle/check.sh"]}}
    end

    test "a failing oracle is a failing verdict, whatever the capability claims" do
      cap = capability_with_oracle("exit 3")

      outcome =
        Acceptance.run(%{
          manifest: Manifest.load!(cap),
          run_id: "test/" <> Mitos.id(),
          stage_id: "verify",
          workspace: workspace_with(%{}),
          diff_paths: []
        })

      assert outcome.verdict == :fail
      assert outcome.exit_status == 3
    end

    test "no predicate means no unattended horizon" do
      plain = Manifest.load!(Fixtures.capability("plain"))
      refute Acceptance.unattended_ok?(plain)

      assert {:zero, reasons} = Autonomy.grant([plain], [Mitos.Harness.Fake])
      assert :no_acceptance_predicate in reasons

      oracled = Manifest.load!(capability_with_oracle("exit 0"))
      assert Acceptance.unattended_ok?(oracled)
      assert {:ok, horizon} = Autonomy.grant([oracled], [Mitos.Harness.Fake])
      assert horizon > 0
    end

    test "a manifest declaring an oracle it cannot check tampering against is refused" do
      dir = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "bad"])
      File.mkdir_p!(dir)
      File.write!(Path.join(dir, "skill.md"), "skill")

      File.write!(Path.join(dir, "mitos.yaml"), """
      name: bad
      version: 1.0.0
      acceptance: {"cmd": ["true"], "oracle_paths": []}
      """)

      assert {:error, :acceptance_requires_oracle_paths} = Manifest.load(dir)
    end
  end

  describe "checkpoints and rewind (16 §3)" do
    test "green is the predicate's verdict and nothing else" do
      run_id = "test/" <> Mitos.id()
      ws = workspace_with(%{"a.txt" => "one"})

      passing = Checkpoint.take(%{run_id: run_id, stage_id: "s1", seq: 1, workspace: ws, acceptance: %{verdict: :pass}})
      # A step that "looks done" with no passing oracle is not a state worth
      # rewinding to, so it must not be recorded as one.
      skipped = Checkpoint.take(%{run_id: run_id, stage_id: "s2", seq: 2, workspace: ws, acceptance: %{verdict: :skipped}})
      failed = Checkpoint.take(%{run_id: run_id, stage_id: "s3", seq: 3, workspace: ws, acceptance: %{verdict: :fail}})

      assert passing.green
      refute skipped.green
      refute failed.green

      assert Checkpoint.last_green([passing, skipped, failed]).checkpoint_id == passing.checkpoint_id
    end

    test "restores the archived tree even with no provider snapshot" do
      run_id = "test/" <> Mitos.id()
      ws = workspace_with(%{"a.txt" => "before"})

      cp = Checkpoint.take(%{run_id: run_id, stage_id: "s", seq: 1, workspace: ws, acceptance: %{verdict: :pass}})

      File.write!(Path.join(ws, "a.txt"), "after")
      dest = Path.join([Mitos.data_dir(), "restore", Mitos.id()])

      assert {:ok, :archive} = Checkpoint.restore(cp, dest)
      assert File.read!(Path.join(dest, "a.txt")) == "before"
    end

    test "a rewind without a memo is refused, because it would repeat the failure" do
      run_id = start_parked_run()

      {:ok, _seq} =
        Mitos.Run.append_decision(
          run_id,
          Decision.new(actor: "supervisor", type: :rewind, target: nil, params: %{"scope" => "step"})
        )

      await_effect(run_id, :run_finished)
      state = Run.status(run_id)
      assert state.status == :aborted

      abort = Enum.find(state.decision_log, &(&1.type == :abort))
      assert abort.params["detail"] =~ "memo"
    end

    test "rewind appends; the abandoned segment stays on the record" do
      run_id = "test/" <> Mitos.id("run")
      ws = workspace_with(%{"a.txt" => "green"})

      {:ok, _} =
        Run.start_run(
          run_id: run_id,
          pipeline: "# Pipeline: hold\n",
          capabilities: %{},
          workspace: ws,
          policy: ParkingPolicy
        )

      await_status(run_id, :parked)

      # A green checkpoint the run can go back to.
      Run.append_effect(run_id, %{
        type: :checkpoint_taken,
        ref: %{"checkpoint_id" => "cp1", "stage_id" => "s1"},
        payload: %{
          "seq" => 0,
          "green" => true,
          "archive" => archive_of(ws),
          "predicate" => %{"verdict" => "pass", "delta" => nil, "score" => nil, "evidence_ref" => nil}
        }
      })

      {:ok, _} =
        Mitos.Run.append_decision(
          run_id,
          Decision.new(
            actor: "supervisor",
            type: :rewind,
            target: "cp1",
            params: %{"scope" => "step", "memo" => "tests regressed after the refactor", "checkpoint_id" => "cp1"}
          )
        )

      await_effect(run_id, :rewound)
      state = Run.status(run_id)
      events = Mitos.Log.read(run_id)

      rewound = Enum.find(events, &match?(%Effect{type: :rewound}, &1))
      assert rewound.payload["memo"] =~ "regressed"
      assert rewound.payload["green"]

      # Appended, never rewritten: both the checkpoint and the rewind survive.
      assert Enum.any?(events, &match?(%Effect{type: :checkpoint_taken}, &1))
      assert length(state.checkpoints) == 1
    end
  end

  describe "restart intensity (16 §4)" do
    test "bounds a ladder that would otherwise rotate signals forever" do
      sup = Supervision.new(max_restarts: 2, max_seconds: 3600)

      {:ok, sup} = Supervision.restart(sup, "step_a")
      {:ok, sup} = Supervision.restart(sup, "step_a")
      assert {:exceeded, :step, _sup} = Supervision.restart(sup, "step_a")
    end

    test "an old restart falls out of the window" do
      sup = Supervision.new(max_restarts: 1, max_seconds: 10)
      now = 1_000

      {:ok, sup} = Supervision.restart(sup, "s", now)
      assert {:exceeded, :step, _} = Supervision.restart(sup, "s", now + 1)
      assert {:ok, _} = Supervision.restart(sup, "s", now + 50)
    end

    test "a stalled predicate always rewinds, never repairs forward" do
      sup = Supervision.new(strategy: :retry_step)

      # Even under a retry strategy: repairing forward from a state the oracle
      # says is not improving is exactly how long runs die (16 §0).
      assert {{:rewind, :step}, _} = Supervision.next_move(sup, :predicate_stall, "s")
      assert {{:rewind, :step}, _} = Supervision.next_move(sup, :oscillation, "s")
      assert {{:retry, :step}, _} = Supervision.next_move(sup, :repeated_error, "s")
    end

    test "exhausted intensity escalates rather than climbing on" do
      sup = Supervision.new(max_restarts: 1)
      {_move, sup} = Supervision.next_move(sup, :verify_fail, "s")
      {move, _sup} = Supervision.next_move(sup, :verify_fail, "s")

      assert {:escalate, {:intensity_exhausted, :step}} = move
    end
  end

  describe "the fleet supervisor (16 §5)" do
    test "sees evidence and structurally cannot see narration" do
      run_id = run_with_stage()
      e = Mitos.Fleet.evidence(run_id)

      assert e.run_id == run_id
      assert [stage | _] = e.stages
      assert stage.result_signal == "ship"

      # A supervisor that reads a worker's narration inherits its delusions.
      json = Mitos.canonical_json(sanitize(e))
      refute json =~ "work complete"
      refute json =~ "rationale"
      refute json =~ "assistant_text"
    end

    test "cannot declare another run finished" do
      for forbidden <- [:advance, :insert, :skip, :finish] do
        assert_raise ArgumentError, ~r/supervisor may not emit/, fn ->
          Decision.new(actor: "supervisor", type: forbidden, target: "x")
        end
      end

      for permitted <- Mitos.Fleet.permitted() do
        assert %Decision{} = Decision.new(actor: "supervisor", type: permitted)
      end
    end

    test "can act on a PARKED run — the one that most needs a supervisor" do
      run_id = start_parked_run()
      assert Mitos.Run.status(run_id).status == :parked

      # A park waits for a human (15 §4). It must not swallow a supervisor's
      # override, or the supervisor is useless exactly where it matters.
      {:ok, _seq} = Mitos.Fleet.act(run_id, :abort, %{cause: :policy, detail: "supervisor stopped it"})

      await_effect(run_id, :run_finished)
      assert Mitos.Run.status(run_id).status == :aborted
    end

    test "flags runs worth looking at, without a model" do
      run_id = run_with_stage()

      # A capability that declares no oracle has no green checkpoints by
      # construction; flagging that would drown the real signal in noise.
      assert Mitos.Fleet.attention([run_id]) |> Enum.map(& &1.run_id) == []

      blocked = start_parked_run()

      Run.append_effect(blocked, %{
        type: :approval_requested,
        ref: %{"decision_id" => "d1"},
        payload: %{"decision_id" => "d1", "reversibility" => "external", "question" => "?", "default" => nil}
      })

      assert [%{reasons: reasons}] = Mitos.Fleet.attention([blocked])
      assert :blocked_on_human in reasons
    end
  end

  describe "the metrics (16 §6)" do
    test "a run nobody had to answer reports no human decisions" do
      run_id = run_with_stage()
      m = Autonomy.metrics(run_id)

      assert m.decisions_raised == 0
      assert m.autonomy_ratio == :infinity
    end

    test "decision precision separates a human answer from silence" do
      run_id = start_parked_run()

      raise_and_resolve(run_id, "d1", via: "cli")
      raise_and_resolve(run_id, "d2", via: ":default_expired", defaulted: true)

      m = Autonomy.metrics(run_id)

      # Three raised: the park the policy itself asked for, plus d1 and d2.
      assert m.decisions_raised == 3
      assert m.decisions_defaulted == 1
      # A card always resolved as its default should not have been raised —
      # this is the number that says so.
      assert m.decision_precision == 0.5
    end

    test "the unattended horizon ends at the first card with no default" do
      run_id = start_parked_run()

      Run.append_effect(run_id, %{
        type: :approval_requested,
        ref: %{"decision_id" => "d"},
        payload: %{"decision_id" => "d", "reversibility" => "external", "question" => "?", "default" => nil}
      })

      m = Autonomy.metrics(run_id)
      assert is_integer(m.unattended_horizon_ms)
    end

    test "a fleet ratio aggregates totals, not a mean of ratios" do
      quiet = run_with_stage()
      noisy = start_parked_run()
      raise_and_resolve(noisy, "d1", via: "cli")

      f = Autonomy.fleet([quiet, noisy])
      assert f.runs == 2
      # One fully-autonomous short run must not hide a fleet that interrupts.
      refute f.autonomy_ratio == :infinity
    end
  end

  # --- fixtures ---

  defp capability_with_oracle(body) do
    dir = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "oracled"])
    File.mkdir_p!(Path.join(dir, "oracle"))
    File.write!(Path.join(dir, "skill.md"), "skill")

    check = Path.join([dir, "oracle", "check.sh"])
    File.write!(check, "#!/bin/sh\n#{body}\n")
    File.chmod!(check, 0o755)

    File.write!(Path.join(dir, "mitos.yaml"), """
    name: oracled
    version: 1.0.0
    harnesses: ["fake"]
    acceptance: {"cmd": ["sh", "oracle/check.sh"], "oracle_paths": ["oracle/**"], "timeout_ms": 30000}
    """)

    dir
  end

  defp workspace_with(files) do
    ws = Path.join([Mitos.data_dir(), "ws", Mitos.id()])
    File.mkdir_p!(ws)

    Enum.each(files, fn {rel, body} ->
      path = Path.join(ws, rel)
      File.mkdir_p!(Path.dirname(path))
      File.write!(path, body)
    end)

    ws
  end

  defp archive_of(ws) do
    dest = Path.join([Mitos.data_dir(), "archive", Mitos.id()])
    File.mkdir_p!(Path.dirname(dest))
    File.cp_r!(ws, dest)
    dest
  end

  defp run_with_stage do
    cap = Fixtures.capability("work")
    ws = Fixtures.workspace(%{"README.md" => "# subject\n"})

    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "test", "project" => "autonomy"},
        pipeline: Fixtures.pipeline(["work"]),
        capabilities: %{"work" => cap},
        workspace: ws,
        inputs: %{},
        budget: %{"usd" => 1.0}
      )

    Run.await(run_id)
    run_id
  end

  defp start_parked_run do
    run_id = "test/" <> Mitos.id("run")

    {:ok, _} =
      Run.start_run(
        run_id: run_id,
        pipeline: "# Pipeline: park\n",
        capabilities: %{},
        policy: ParkingPolicy
      )

    await_status(run_id, :parked)
    # The park is a Decision; the card is raised off-process (04 §11), so a test
    # that counts cards must wait for the Effect, not just the parked status.
    await_effect(run_id, :approval_requested)
    run_id
  end

  defp await_status(run_id, status, tries \\ 100) do
    cond do
      Run.status(run_id).status == status -> :ok
      tries == 0 -> :timeout
      true ->
        Process.sleep(20)
        await_status(run_id, status, tries - 1)
    end
  end

  defp raise_and_resolve(run_id, id, opts) do
    default = if opts[:defaulted], do: "yes", else: nil

    append(run_id, %{
      type: :approval_requested,
      ref: %{"decision_id" => id},
      payload: %{"decision_id" => id, "question" => "?", "default" => default}
    })

    if opts[:defaulted] do
      append(run_id, %{type: :decision_defaulted, ref: %{"decision_id" => id}, payload: %{"choice" => "yes"}})
    end

    append(run_id, %{
      type: :approval_resolved,
      ref: %{"decision_id" => id},
      payload: %{"decision_id" => id, "via" => opts[:via], "choice" => "yes"}
    })
  end

  defp append(run_id, fields) do
    if Mitos.Log.writer_held?(run_id) do
      Run.append_effect(run_id, fields)
    else
      Mitos.Log.append_detached(run_id, Effect.new(fields))
    end
  end

  defp await_effect(run_id, type, tries \\ 100) do
    types = run_id |> Mitos.Log.read() |> Enum.map(& &1.type)

    cond do
      type in types -> :ok
      tries == 0 -> {:timeout, types}
      true ->
        Process.sleep(20)
        await_effect(run_id, type, tries - 1)
    end
  end

  defp sanitize(%{} = m), do: Map.new(m, fn {k, v} -> {to_string(k), sanitize(v)} end)
  defp sanitize(l) when is_list(l), do: Enum.map(l, &sanitize/1)
  defp sanitize(a) when is_atom(a) and not is_boolean(a) and not is_nil(a), do: to_string(a)
  defp sanitize(other), do: other
end
