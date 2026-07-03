defmodule Mitos.RunTest do
  @moduledoc """
  The P1–P3 exit criteria (09 §2) as executable checks: a capability runs in a
  hermetic local sandbox, egress is deny-all-except-broker with the secret
  brokered at the edge, diff + RESULT + evidence are harvested, the linear
  pipeline advances stage by stage, `fold(events) = state`, and recovery
  re-drives the pending Decision tail (04 §7).
  """
  use ExUnit.Case, async: true

  alias Mitos.{Fixtures, Log, Run, Vault}
  alias Mitos.Event.{Decision, Effect}

  @moduletag timeout: 120_000

  test "two-stage linear run: decisions, effects, harvest, budget fold" do
    plan = Fixtures.capability("plan")
    work = Fixtures.capability("work")
    ws = Fixtures.workspace(%{"README.md" => "# subject\n"})

    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "test", "project" => "e2e"},
        pipeline: Fixtures.pipeline(["plan", "work"]),
        capabilities: %{"plan" => plan, "work" => work},
        workspace: ws,
        inputs: %{"issue" => "do the thing"},
        budget: %{"usd" => 1.0}
      )

    state = Run.await(run_id)
    assert state.status == :finished

    # fold(events) = state: both stages finished with the soft result harvested.
    assert state.stage_order == ["plan", "work"]
    assert state.stages["plan"].status == :finished
    assert state.stages["work"].status == :finished
    assert state.stages["work"].result["signal"] == "ship"
    assert state.stages["work"].result["summary"] == "work complete"

    # budget_charged folds: 2 stages × the fixture's deterministic usage.
    assert state.spent.tokens == 240
    assert_in_delta state.spent.usd, 0.04, 1.0e-9

    events = Log.read(run_id)
    decisions = Enum.filter(events, &match?(%Decision{}, &1))

    # The Decision log is the policy trace: advance, advance, finish — in order.
    assert Enum.map(decisions, &{&1.type, &1.target}) ==
             [{:advance, "plan"}, {:advance, "work"}, {:finish, nil}]

    # Every Decision carries budget + inputs_hash (01 §8.1). (Disk-loaded
    # events carry JSON string keys.)
    assert Enum.all?(decisions, &(is_binary(&1.inputs_hash) and &1.budget["usd"] == 1.0))

    # append(d) BEFORE execute: each stage's Effects are caused_by its Decision.
    [d_plan, d_work, d_finish] = decisions
    effects = Enum.filter(events, &match?(%Effect{}, &1))

    for {d, stage} <- [{d_plan, "plan"}, {d_work, "work"}] do
      caused = Enum.filter(effects, &(&1.caused_by == d.seq))
      types = Enum.map(caused, & &1.type)

      assert :capability_started in types
      assert :iteration_started in types
      assert :iteration_finished in types
      assert :artifact_registered in types
      assert :capability_finished in types
      assert :budget_charged in types

      started = Enum.find(caused, &(&1.type == :capability_started))
      assert started.ref["stage_id"] == stage
      assert started.ref["trust_tier"] == "T1"
      # the resolved skill's sha256 — exact instructions, replayable (03 §2.1)
      assert started.payload["skill_sha256"] =~ ~r/\A[a-f0-9]{64}\z/

      finished = Enum.find(caused, &(&1.type == :capability_finished))
      assert finished.payload["diff_stat"] =~ "note.txt"
      assert Enum.any?(finished.payload["evidence"], &(&1["kind"] == "diff"))
    end

    # run_finished closes the timeline, caused by the finish Decision.
    run_finished = Enum.find(effects, &(&1.type == :run_finished))
    assert run_finished.caused_by == d_finish.seq
    assert run_finished.payload["status"] == "finished"

    # Evidence artifacts are content-addressed in the run store (05 §5.4).
    art = Enum.find(effects, &(&1.type == :artifact_registered and &1.ref["path"] == "note.txt"))
    assert {:ok, bytes} = Mitos.Proof.artifact(run_id, art.ref["sha256"])
    assert Mitos.sha256(bytes) == art.ref["sha256"]
  end

  test "brokered egress from inside the sandbox: secret attached at edge, never in sandbox or log" do
    dest = "api.seller#{:erlang.unique_integer([:positive])}.dev"
    cred_id = "cred_" <> dest
    Vault.put("seller_e2e", dest, "sekrit-key-xyz", credential_id: cred_id)

    Mitos.Broker.Destinations.mock(dest, fn outbound ->
      if outbound[:headers]["authorization"] == "Bearer sekrit-key-xyz" do
        %{status: 200, headers: %{}, body: "credentialed-ok"}
      else
        %{status: 401, headers: %{}, body: "no-credential"}
      end
    end)

    script = """
    #!/bin/sh
    set -e
    curl -s -X POST "$MITOS_BROKER/egress" \
      -H "x-mitos-egress-token: $MITOS_EGRESS_TOKEN" \
      -H "content-type: application/json" \
      -d '{"dest":"#{dest}","method":"post","body":"{}"}' > egress_allowed.txt
    curl -s -o denied_body.txt -w "%{http_code}" -X POST "$MITOS_BROKER/egress" \
      -H "x-mitos-egress-token: $MITOS_EGRESS_TOKEN" \
      -H "content-type: application/json" \
      -d '{"dest":"evil.exfil.io","method":"get"}' > denied_code.txt
    env | sort > env_dump.txt
    cat > "$MITOS_RESULT" <<'EOF'
    {"signal":"ship","confidence":1.0,"summary":"egress exercised",
     "evidence":[{"kind":"log","path":"egress_allowed.txt"}]}
    EOF
    """

    cap =
      Fixtures.capability("egresser",
        script: script,
        egress_policy: [%{"dest" => dest, "credential" => cred_id}],
        result: %{"evidence" => ["egress_allowed.txt", "env_dump.txt"]}
      )

    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "test", "project" => "egress"},
        pipeline: Fixtures.pipeline(["egresser"]),
        capabilities: %{"egresser" => cap},
        scope: "seller_e2e"
      )

    state = Run.await(run_id)
    assert state.status == :finished

    sandbox_ws =
      Path.join([Mitos.data_dir(), "sandboxes", run_id, "egresser", "workspace"])

    # ① The allowed crossing carried the credential — attached by the broker,
    #    invisible to the sandbox (the destination saw it; the sandbox got only
    #    the response).
    allowed = File.read!(Path.join(sandbox_ws, "egress_allowed.txt"))
    assert allowed =~ "credentialed-ok"

    # ② The unlisted destination was denied: deny-all-except-broker.
    assert File.read!(Path.join(sandbox_ws, "denied_code.txt")) =~ "403"
    assert File.read!(Path.join(sandbox_ws, "denied_body.txt")) =~ "egress_denied"

    # ③ Hermeticity: the sandbox env never contained the secret, and HOME was
    #    redirected into the sandbox (05 §6).
    env_dump = File.read!(Path.join(sandbox_ws, "env_dump.txt"))
    refute env_dump =~ "sekrit"
    assert env_dump =~ "HOME=" <> Path.join([Mitos.data_dir(), "sandboxes", run_id, "egresser", "home"])

    # ④ Both crossings are Effects on the log, with the credential *id* only.
    effects = run_id |> Log.read() |> Enum.filter(&match?(%Effect{}, &1))
    allowed_fx = Enum.find(effects, &(&1.type == :egress_allowed))
    denied_fx = Enum.find(effects, &(&1.type == :egress_denied))

    assert allowed_fx.ref["dest"] == dest
    assert allowed_fx.ref["credential_id"] == cred_id
    assert denied_fx.ref["dest"] == "evil.exfil.io"

    # ⑤ The secret never lands on the log (09 §4).
    refute File.read!(Log.path(run_id)) =~ "sekrit"
  end

  test "a blocking pre-hook aborts the step before model spend (03 §6)" do
    cap =
      Fixtures.capability("gated",
        hooks: %{"pre" => [%{"name" => "gate", "run" => "exit 1", "on_fail" => "block"}]}
      )

    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "test", "project" => "hooks"},
        pipeline: Fixtures.pipeline(["gated"]),
        capabilities: %{"gated" => cap}
      )

    state = Run.await(run_id)
    assert state.stages["gated"].result["signal"] == "blocked"

    events = Log.read(run_id)
    hook = Enum.find(events, &(match?(%Effect{}, &1) and &1.type == :hook_ran))
    assert hook.ref["phase"] == "pre"
    assert hook.payload["exit"] == 1

    # no model spend: zero iterations, zero tokens charged
    refute Enum.any?(events, &(match?(%Effect{}, &1) and &1.type == :iteration_started))
    assert state.spent.tokens == 0
  end

  test "budget exhaustion aborts instead of advancing (04 §11)" do
    a = Fixtures.capability("stage_a")
    b = Fixtures.capability("stage_b")

    # stage_a costs $0.02 (fixture usage); ceiling $0.015 ⇒ stage_b never opens.
    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "test", "project" => "budget"},
        pipeline: Fixtures.pipeline(["stage_a", "stage_b"], budget: %{"usd" => 0.015}),
        capabilities: %{"stage_a" => a, "stage_b" => b}
      )

    state = Run.await(run_id)
    assert state.status == :aborted
    assert state.stages["stage_a"].status == :finished
    refute Map.has_key?(state.stages, "stage_b")

    abort = Enum.find(Log.read(run_id), &match?(%Decision{type: :abort}, &1))
    assert abort.params["cause"] == "budget"
  end

  test "recovery re-drives the pending Decision, it does not re-decide (04 §7)" do
    solo = Fixtures.capability("solo")
    run_id = "test/redrive_" <> Mitos.id()

    # Simulate the crash: config durable, Decision on the log, Effect never
    # committed (the 01 §4 crash shape), process gone.
    Mitos.Run.Config.new(
      run_id: run_id,
      tenant: %{"org" => "test", "project" => "redrive"},
      pipeline: Fixtures.pipeline(["solo"]),
      capabilities: %{"solo" => solo}
    )
    |> Mitos.Run.Config.store()

    {:ok, w, []} = Log.open_writer(run_id)
    {_, w} = Log.append(w, Effect.new(run_id: run_id, type: :run_started))

    {d, w} =
      Log.append(
        w,
        Decision.new(
          run_id: run_id,
          actor: "orchestrator",
          type: :advance,
          target: "solo",
          params: %{"capability" => "solo", "intent" => ""},
          inputs_hash: "pre-crash",
          budget: %{}
        )
      )

    Log.close(w)

    {:ok, ^run_id} = Run.resume(run_id)
    state = Run.await(run_id)

    assert state.status == :finished
    assert state.stages["solo"].status == :finished

    events = Log.read(run_id)
    advances = Enum.filter(events, &match?(%Decision{type: :advance}, &1))

    # Exactly one advance — the pre-crash one. Recovery re-drove it; the
    # orchestrator was not consulted again for that stage.
    assert Enum.map(advances, & &1.seq) == [d.seq]

    started = Enum.filter(events, &(match?(%Effect{}, &1) and &1.type == :capability_started))
    assert Enum.map(started, & &1.caused_by) == [d.seq]
  end

  test "a stage whose capability is unknown is skipped, not fatal (loose intent)" do
    known = Fixtures.capability("known")

    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "test", "project" => "skip"},
        pipeline: Fixtures.pipeline(["ghost", "known"]),
        capabilities: %{"known" => known}
      )

    state = Run.await(run_id)
    assert state.status == :finished
    assert state.stages["known"].status == :finished

    skip = Enum.find(Log.read(run_id), &match?(%Decision{type: :skip}, &1))
    assert skip.target == "ghost"
  end
end
