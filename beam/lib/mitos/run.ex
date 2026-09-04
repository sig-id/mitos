defmodule Mitos.Run do
  @moduledoc """
  One run of a pipeline, as a process (04 §11): this GenServer owns the run's
  `Mitos.Log.Writer` and is the **single writer** — every Decision and Effect,
  including brokered egress crossings from concurrently executing stages,
  funnels through here, so the log stays one totally-ordered stream and `fold`
  stays deterministic.

  The control loop is 04 §3.4, literally:

      state   ← fold(log)
      if terminal(state): stop
      d       ← policy.decide(ctx)          # the orchestrator seam
      append(d)                             # durable BEFORE execute
      effects ← execute(d)                  # step runtime, in a monitored task
      append(effects)

  Recovery is replay (04 §7): `resume/1` reopens the log, folds it, and
  **re-drives** any Decision that has no committed Effect — it never re-decides
  the pending tail. Stage execution runs in a `Task.Supervisor` child so the
  writer stays responsive while a harness runs; a stage crash lands as a
  substrate `abort`, never a hung run.
  """
  use GenServer, restart: :temporary

  alias Mitos.{Acceptance, Broker, Checkpoint, Hooks, LaunchPolicy, Log, LoopManager, Pipeline, Supervision}
  alias Mitos.Event.{Decision, Effect}
  alias Mitos.Run.{Config, State}

  # --- client API (UI, CLI, API, automation — same operations, 01 §10.11) ---

  def start_run(fields) do
    config = Config.new(fields)

    case DynamicSupervisor.start_child(Mitos.Run.Supervisor, {__MODULE__, config}) do
      {:ok, _pid} -> {:ok, config.run_id}
      {:error, {:already_started, _}} -> {:error, :already_running}
      {:error, reason} -> {:error, reason}
    end
  end

  @doc "Resume a crashed/stopped run from its durable config + log (04 §7)."
  def resume(run_id) do
    with {:ok, config} <- Config.load(run_id),
         {:ok, _pid} <- DynamicSupervisor.start_child(Mitos.Run.Supervisor, {__MODULE__, config}) do
      {:ok, run_id}
    end
  end

  @doc "Block until the run is terminal; returns the folded final state."
  def await(run_id, timeout \\ 120_000) do
    GenServer.call(via(run_id), :await, timeout)
  catch
    # Process already gone — the log has the answer (state is never process-only).
    :exit, {:noproc, _} -> State.fold(run_id)
    :exit, {:normal, _} -> State.fold(run_id)
  end

  @doc "Folded read model, from disk — works with or without a live process."
  def status(run_id), do: State.fold(run_id)

  @doc "Broker listener → run log: append a brokered-crossing Effect (04 §11)."
  def record_broker_effect(run_id, fields), do: append_effect(run_id, fields)

  @doc """
  Fleet supervisor → run log: append a Decision through the run's single writer
  (04 §11). The supervisor acts on ANOTHER run, so its move must enter that
  run's log the same way every other Decision does — one totally-ordered stream,
  no side channel (16 §5.2).
  """
  def append_decision(run_id, %Decision{} = d) do
    GenServer.call(via(run_id), {:append_decision, d})
  catch
    :exit, _ -> {:error, :run_not_running}
  end

  @doc "I/O plane → run log: append an Effect through the run's single writer (04 §11)."
  def append_effect(run_id, fields) do
    GenServer.call(via(run_id), {:append_effect, fields})
  catch
    # The run released its writer between the lookup and the call (it finished,
    # or parked and stopped). Silently dropping the Effect here used to be
    # invisible and is exactly how a decision resolved after a park — the record
    # `decision_precision` (16 §6) is computed from — would vanish. Fall back to
    # a detached append instead, which is safe precisely because no process
    # holds the writer (01 §4).
    :exit, _ ->
      case Mitos.Log.append_detached(run_id, Effect.new(Map.new(fields))) do
        {:ok, _stamped} -> :ok
        other -> other
      end
  end

  def start_link(%Config{} = config) do
    GenServer.start_link(__MODULE__, config, name: via(config.run_id))
  end

  defp via(run_id), do: {:via, Registry, {Mitos.Run.Registry, run_id}}

  # --- server ---

  @impl true
  def init(%Config{} = config) do
    {:ok, writer, events} = Log.open_writer(config.run_id)
    Config.store(config)

    manifests =
      Map.new(config.capabilities, fn {name, dir} ->
        {name, Mitos.Capability.Manifest.load!(dir)}
      end)

    intent = Pipeline.parse(config.pipeline)

    srv = %{
      config: config,
      writer: writer,
      events: events,
      intent: intent,
      manifests: manifests,
      # 04 §8.1: the pipeline's declared budget is the run ceiling unless the
      # caller (task/purchase) sets one — caller wins per axis.
      budget: Map.merge(intent.budget, config.budget),
      running: nil,
      # 16 §4: the outer bound on the ladder. Per-signal counters are first
      # responses; this is what stops a run retrying forever by rotating which
      # signal trips.
      sup: Supervision.new(max_iterations: config.max_iterations),
      waiters: []
    }

    srv =
      if events == [] do
        srv = emit_ingress(config, srv)
        commit_effect(srv,
          type: :run_started,
          payload: %{
            "tenant" => config.tenant,
            "workspace" => config.workspace,
            # 05 §5.1 relaxed by explicit operator choice: an unsandboxed run
            # edits the real tree, so the record has to say so — a reader of
            # this log must never have to guess whether the boundary was there.
            "workspace_mode" => to_string(config.workspace_mode || :clone)
          }
        )
      else
        srv
      end

    send(self(), :step)
    {:ok, srv}
  end

  @impl true
  def handle_info(:step, srv) do
    state = fold(srv)

    cond do
      State.terminal?(state) ->
        finalize(srv, state)

      # A parked run (request_approval) waits for approval_resolved (15 §4).
      #
      # But it must still accept an OVERRIDE: a fleet supervisor's `rewind` /
      # `abort` / `retry` (16 §5.2), or a human's abort. The runs most in need
      # of a supervisor move are precisely the stuck ones, so a park that
      # swallowed those decisions would make the supervisor useless exactly
      # where it matters.
      state.status == :parked ->
        case override_decision(state) do
          nil -> {:noreply, srv}
          d -> {:noreply, execute(srv, state, d)}
        end

      srv.running != nil ->
        {:noreply, srv}

      true ->
        case State.pending_decisions(state) do
          # Crash recovery: re-drive, don't re-decide (04 §7).
          [pending | _] -> {:noreply, execute(srv, state, pending)}
          [] -> {:noreply, decide_and_execute(srv, state)}
        end
    end
  end

  # Stage task finished.
  def handle_info({ref, result}, %{running: {ref, decision}} = srv) do
    Process.demonitor(ref, [:flush])
    srv = %{srv | running: nil}

    srv =
      case result do
        :ok ->
          srv

        {:ok, %{signals: []}} ->
          srv

        # 04 §4.4 / 16 §4: the loop-manager climbed its local ladder and a
        # signal is still tripping. Restart intensity decides whether there is
        # a move left, and which — never the signal alone.
        {:ok, %{signals: [signal | _]} = outcome} ->
          handle_signal(srv, decision, signal, outcome)

        {:error, reason} ->
          substrate_abort(srv, decision, "launch refused: #{inspect(reason)}", :policy)
      end

    send(self(), :step)
    {:noreply, srv}
  end

  def handle_info({:DOWN, ref, :process, _pid, reason}, %{running: {ref, decision}} = srv) do
    srv =
      %{srv | running: nil}
      |> substrate_abort(decision, "stage crashed: #{inspect(reason)}", :unrecoverable)

    send(self(), :step)
    {:noreply, srv}
  end

  def handle_info(_msg, srv), do: {:noreply, srv}

  @impl true
  def handle_call({:append_effect, fields}, _from, srv) do
    {:reply, :ok, commit_effect(srv, fields)}
  end

  def handle_call({:append_decision, %Decision{} = d}, _from, srv) do
    {stamped, srv} = append(srv, d)
    send(self(), :step)
    {:reply, {:ok, stamped.seq}, srv}
  end

  def handle_call(:await, from, srv) do
    state = fold(srv)

    if State.terminal?(state) do
      {:reply, state, srv}
    else
      {:noreply, %{srv | waiters: [from | srv.waiters]}}
    end
  end

  # A pending Decision that outranks the park: anything a supervisor emitted
  # (its whole vocabulary is override-shaped, 16 §5.2) plus a terminal abort.
  defp override_decision(state) do
    state
    |> State.pending_decisions()
    |> Enum.find(fn d -> d.actor == "supervisor" or d.type == :abort end)
  end

  # --- the control loop body ---

  defp decide_and_execute(srv, state) do
    ctx = %{
      intent: srv.intent,
      state: state,
      budget: srv.budget,
      remaining: remaining(srv.budget, state.spent),
      capabilities: srv.manifests,
      config: srv.config
    }

    decision = srv.config.policy.decide(ctx)

    # 04 §11: the substrate refuses to execute a Decision whose grant exceeds
    # remaining — it emits abort{cause: budget} instead. Backstop; the policy
    # is expected to have checked first.
    decision =
      if decision.type in [:advance, :insert, :retry] and exhausted?(ctx.remaining) do
        Decision.new(
          actor: "orchestrator",
          type: :abort,
          params: %{cause: :budget, detail: "substrate refusal: budget exhausted"},
          inputs_hash: decision.inputs_hash,
          rationale: "substrate refused #{decision.type}: budget exhausted",
          budget: decision.budget
        )
      else
        decision
      end

    {decision, srv} = append(srv, decision)
    execute(srv, fold(srv), decision)
  end

  defp execute(srv, _state, %Decision{type: t} = d) when t in [:advance, :insert, :retry] do
    run_id = srv.config.run_id
    me = self()
    append = fun_append(me)
    config = srv.config
    manifests = srv.manifests
    intent = srv.intent

    task =
      Task.Supervisor.async_nolink(Mitos.Run.TaskSupervisor, fn ->
        execute_stage(run_id, d, config, manifests, intent, append)
      end)

    %{srv | running: {task.ref, d}}
  end

  defp execute(srv, _state, %Decision{type: :finish} = d) do
    srv =
      commit_effect(srv,
        type: :run_finished,
        caused_by: d.seq,
        payload: %{"status" => "finished", "result_ref" => d.params[:result_ref] || d.params["result_ref"]}
      )

    send(self(), :step)
    srv
  end

  defp execute(srv, _state, %Decision{type: :abort} = d) do
    srv =
      commit_effect(srv,
        type: :run_finished,
        caused_by: d.seq,
        payload: %{"status" => "aborted", "cause" => to_string(d.params[:cause] || d.params["cause"] || "unknown")}
      )

    send(self(), :step)
    srv
  end

  # 15: an approval is a typed card, not a question in a payload. The card is
  # raised from a task because the decision driver appends through THIS
  # process's writer (04 §11) — calling it inline would deadlock the writer.
  defp execute(srv, _state, %Decision{type: :request_approval} = d) do
    raise_card_async(srv, d, d.params)
    send(self(), :step)
    srv
  end

  # 16 §3.3: restore the last green state and re-open. Rewind APPENDS — the
  # abandoned segment stays on the log, which is what keeps attribution
  # possible (16 §6.1) — and `memo` is required, because a rewind without one
  # repeats the failure it rewound from.
  defp execute(srv, state, %Decision{type: :rewind} = d) do
    target = param(d, "checkpoint_id")
    memo = param(d, "memo")

    checkpoint =
      cond do
        target -> State.checkpoint(state, target)
        param(d, "allow_non_green") -> List.last(state.checkpoints)
        true -> State.last_green(state)
      end

    srv =
      cond do
        is_nil(memo) or memo == "" ->
          substrate_abort(srv, d, "rewind without a memo repeats the failure", :policy)

        is_nil(checkpoint) ->
          substrate_abort(srv, d, "no green checkpoint to rewind to", :unrecoverable)

        true ->
          restore_checkpoint(srv, d, checkpoint, memo)
      end

    send(self(), :step)
    srv
  end

  # skip / switch / escalate need no substrate mechanism here — they fold.
  defp execute(srv, _state, %Decision{}) do
    send(self(), :step)
    srv
  end

  defp substrate_abort(srv, caused_by_decision, detail, cause) do
    {d, srv} =
      append(
        srv,
        Decision.new(
          actor: "orchestrator",
          type: :abort,
          params: %{cause: cause, detail: detail},
          inputs_hash: caused_by_decision.inputs_hash,
          rationale: "substrate: " <> detail,
          budget: caused_by_decision.budget
        )
      )

    commit_effect(srv,
      type: :run_finished,
      caused_by: d.seq,
      payload: %{"status" => "aborted", "cause" => to_string(cause), "detail" => detail}
    )
  end

  # The loop-manager exhausted its intra-step ladder. Ask supervision what is
  # left: a bounded retry, a rewind to the last green state, or a human.
  defp handle_signal(srv, decision, signal, outcome) do
    {move, sup} = Supervision.next_move(srv.sup, signal, decision.target)
    srv = %{srv | sup: sup}

    case move do
      {:retry, _} ->
        {_d, srv} = append(srv, ladder_decision(:retry, decision, signal, outcome))
        srv

      {:rewind, scope} ->
        {_d, srv} = append(srv, rewind_decision(decision, signal, scope, outcome))
        srv

      {:escalate, reason} ->
        {d, srv} = append(srv, ladder_decision(:escalate, decision, signal, outcome))

        # 15 §2: the substrate has run out of moves. That is exactly when a
        # human is worth interrupting, and the kind is `judgment` — unguessable
        # by construction, so no default may advance it.
        raise_card_async(srv, d, %{
          kind: :approve,
          act: %{kind: :judgment},
          question: "#{decision.target}: #{signal} and the ladder is exhausted. Continue?",
          attempted: "#{decision.target} (#{signal})",
          blocked_by: inspect(reason),
          options: [
            %{id: "retry", label: "Retry the step", consequence: "one more attempt, same approach"},
            %{id: "rewind", label: "Rewind to last green", consequence: "discard this segment and re-attempt"},
            %{id: "abort", label: "Stop the run", consequence: "the run ends; work so far is kept"}
          ]
        })

        srv
    end
  end

  defp ladder_decision(type, opening, signal, outcome) do
    Decision.new(
      actor: "loop_manager",
      type: type,
      target: opening.target,
      params: %{
        signal: to_string(signal),
        feedback: memo_for(signal, outcome),
        climb_history: outcome[:signals] || []
      },
      inputs_hash: opening.inputs_hash,
      rationale: "loop-manager: #{signal}",
      budget: opening.budget
    )
  end

  defp rewind_decision(opening, signal, scope, outcome) do
    Decision.new(
      actor: "loop_manager",
      type: :rewind,
      target: nil,
      params: %{
        scope: to_string(scope),
        stage_id: opening.target,
        memo: memo_for(signal, outcome),
        reason_signal: to_string(signal)
      },
      inputs_hash: opening.inputs_hash,
      rationale: "loop-manager: rewind on #{signal}",
      budget: opening.budget
    )
  end

  # The failure memo carried into the retry (04 §4.2). Repairing forward
  # without it is how a long run loops on the same mistake.
  defp memo_for(signal, outcome) do
    "previous attempt tripped #{signal}" <>
      case outcome[:acceptance] do
        nil -> ""
        v -> "; acceptance verdict was #{v}"
      end
  end

  defp restore_checkpoint(srv, d, checkpoint, memo) do
    if srv.config.workspace_mode == :in_place do
      restore_in_place(srv, d, checkpoint, memo)
    else
      restore_by_clone(srv, d, checkpoint, memo)
    end
  end

  # Unsandboxed: there is no copy to repoint at, so the rewind happens in the
  # operator's own tree. Tracked content only — see `Checkpoint.restore_in_place/2`.
  defp restore_in_place(srv, d, checkpoint, memo) do
    case Checkpoint.restore_in_place(checkpoint, srv.config.workspace) do
      {:ok, how} ->
        commit_effect(srv,
          type: :rewound,
          caused_by: d.seq,
          ref: %{"checkpoint_id" => checkpoint.checkpoint_id, "stage_id" => checkpoint.stage_id},
          payload: %{
            "via" => to_string(how),
            "memo" => memo,
            "green" => checkpoint.green,
            "commit" => checkpoint.workspace_commit,
            "note" => "tracked content only; untracked files were left in place"
          }
        )

      {:error, reason} ->
        substrate_abort(srv, d, "in-place rewind failed: #{inspect(reason)}", :unrecoverable)
    end
  end

  defp restore_by_clone(srv, d, checkpoint, memo) do
    dest = Path.join([Mitos.run_dir(srv.config.run_id), "rewind", checkpoint.checkpoint_id])

    case Checkpoint.restore(checkpoint, dest) do
      {:ok, how} ->
        # The next `prepare` clones from here (05 §5.1), so the rewound state
        # becomes the run's input without touching the log or the sandbox that
        # produced it.
        config = %{srv.config | workspace: dest}
        Mitos.Run.Config.store(config)

        %{srv | config: config}
        |> commit_effect(
          type: :rewound,
          caused_by: d.seq,
          ref: %{"checkpoint_id" => checkpoint.checkpoint_id, "stage_id" => checkpoint.stage_id},
          payload: %{"via" => to_string(elem_or(how)), "memo" => memo, "green" => checkpoint.green}
        )

      {:error, reason} ->
        substrate_abort(srv, d, "rewind failed: #{inspect(reason)}", :unrecoverable)
    end
  end

  defp elem_or({tag, _}), do: tag
  defp elem_or(tag), do: tag

  defp param(%Decision{params: p}, key), do: p[key] || p[String.to_atom(key)]

  # Raised off-process: the decision driver appends through this process's
  # writer, so an inline call would deadlock (04 §11).
  defp raise_card_async(srv, %Decision{} = d, fields) do
    run_id = srv.config.run_id
    tenant = srv.config.tenant
    fields = Map.new(fields)

    Task.Supervisor.start_child(Mitos.Run.TaskSupervisor, fn ->
      act = fields[:act] || %{kind: :judgment}

      built =
        Mitos.Decision.Policy.card_for(act, %{
          run_id: run_id,
          caused_by: d.seq,
          tenant: tenant,
          stage_id: d.target,
          kind: fields[:kind] || :approve,
          question: fields[:question] || to_string(fields["question"] || "Approval required"),
          context: %{attempted: fields[:attempted], blocked_by: fields[:blocked_by], evidence_refs: []},
          options: fields[:options] || [],
          cost_of_wrong: fields[:cost_of_wrong] || %{}
        })

      case built do
        {:ok, card} -> Mitos.Decision.raise_card(card, to: "operator")
        {:error, reason} -> {:error, reason}
      end
    end)

    :ok
  end

  # --- the step runtime (03 §6 bracket), in a supervised task ---

  defp execute_stage(run_id, %Decision{} = d, config, manifests, intent, append) do
    name = d.target
    manifest = Map.fetch!(manifests, name)

    launch_ctx = %{
      run_id: run_id,
      stage_id: name,
      workspace: config.workspace,
      scope: config.scope,
      trust_tier: config.trust_tier,
      workspace_mode: config.workspace_mode,
      context: []
    }

    with {:ok, spec} <- LaunchPolicy.compile(manifest, launch_ctx),
         {:ok, handle} <- spec.provider.prepare(spec) do
      token = Broker.grant(run_id, d.seq, spec.scope, spec.egress_policy)
      t0 = System.monotonic_time(:millisecond)

      append.(
        type: :capability_started,
        caused_by: d.seq,
        ref: %{
          "stage_id" => name,
          "capability_ref" => manifest.capability_id,
          "harness" => spec.harness,
          "trust_tier" => spec.trust_tier,
          "sandbox" => handle.dir
        },
        # The resolved skill's sha — the run's exact instructions, replayable
        # without forking capability identity (03 §2.1/§9).
        payload: %{"skill_sha256" => handle.skill_sha}
      )

      try do
        {loop, blocked_by} =
          case Hooks.run(manifest.hooks.pre, handle, :pre, fn f -> append.([{:caused_by, d.seq} | f]) end) do
            {:blocked, hook} ->
              # 03 §6: a blocking pre gate aborts the step BEFORE model spend.
              {%{iterations: 0, tokens: 0, usd: 0.0, wall_ms: 0, ok: false, signals: []}, hook}

            :ok ->
              env = %{
                "MITOS_EGRESS_TOKEN" => token,
                "MITOS_BROKER" => Mitos.Broker.Listener.endpoint()
              }

              loop =
                LoopManager.run_step(%{
                  provider: spec.provider,
                  handle: handle,
                  prompt: compose_prompt(intent, d, config),
                  env: env,
                  append: fn f -> append.([{:caused_by, d.seq} | f]) end,
                  max_iterations: config.max_iterations,
                  run_id: run_id,
                  stage_id: name,
                  # 14 §2.1: T_block comes from the harness's observed think
                  # time, never a fixed guess — a harness that legitimately
                  # thinks for minutes must not read as blocked at 45s.
                  thresholds: %{block: Mitos.Harness.block_threshold(spec.harness_mod)},
                  # Signals read the workspace, not the narration (04 §4.3).
                  diff_sha: fn -> workspace_sha(handle.workspace) end,
                  # 16 §2 `blocked_on_human`: no iteration boundary will ever
                  # report this, so the clock does.
                  on_blocked: fn window ->
                    on_blocked(run_id, d, name, handle, spec, window, append)
                  end,
                  # inner-zoom trace, dev cut only (11 §4)
                  trace: Path.join([Mitos.run_dir(run_id), "trace", name <> ".jsonl"])
                })

              {loop, nil}
          end

        post =
          Hooks.run(manifest.hooks.post, handle, :post, fn f -> append.([{:caused_by, d.seq} | f]) end)

        {:ok, harvested} = spec.provider.harvest(%{handle: handle})

        harvest_effects(append, d.seq, run_id, name, manifest, harvested, %{
          blocked_by: blocked_by,
          unverified: match?({:blocked, _}, post),
          loop: loop
        })

        # 16 §3.2: the hard verdict, run OUTSIDE the workspace against an oracle
        # restored from the capability dir. `result` above is the capability's
        # self-report; this is whether it happened.
        acceptance =
          Acceptance.run(%{
            manifest: manifest,
            run_id: run_id,
            stage_id: name,
            workspace: handle.workspace,
            diff_paths: diff_paths(harvested.diff),
            append: append,
            caused_by: d.seq
          })

        # 16 §3.1: the segment boundary. `green` is the predicate's verdict and
        # nothing else — a step that looks done with no passing oracle is not a
        # state worth rewinding to.
        Checkpoint.take(%{
          run_id: run_id,
          stage_id: name,
          seq: d.seq,
          workspace: handle.workspace,
          workspace_mode: handle[:workspace_mode] || :clone,
          provider: spec.provider,
          acceptance: acceptance,
          append: append,
          caused_by: d.seq
        })

        append.(
          type: :budget_charged,
          caused_by: d.seq,
          payload: %{
            "tokens" => loop.tokens,
            "usd" => loop.usd,
            "wall_ms" => System.monotonic_time(:millisecond) - t0
          }
        )

        {:ok,
         %{
           signals: signals_after(loop, acceptance),
           acceptance: acceptance.verdict,
           tampered: acceptance.tampered
         }}
      after
        Broker.revoke(token)
        spec.provider.stop(%{handle: handle})
      end
    else
      {:error, reason} -> {:error, reason}
    end
  end

  # A failing oracle is a signal in its own right (04 §4.3 `verify_fail`), and a
  # tampered one is never merely a failure — it is an escalation (16 §3.2).
  defp signals_after(loop, acceptance) do
    cond do
      acceptance.tampered -> [:oracle_tampered]
      Map.get(loop, :signals, []) != [] -> loop.signals
      acceptance.verdict == :fail -> [:verify_fail]
      true -> []
    end
  end

  # The workspace as one fingerprint — what `no_progress` and `oscillation` are
  # computed from (04 §4.3). Path + size + mtime over the gitignore-honouring
  # file set, never file contents: the signal only needs to know whether the
  # tree moved, and reading a real checkout on every iteration is an I/O storm,
  # not a measurement.
  defp workspace_sha(workspace) do
    case Mitos.Workspace.fingerprint(workspace) do
      {:ok, digest} -> digest
      {:error, _} -> nil
    end
  end

  defp diff_paths(%{patch: patch}) when is_binary(patch) do
    Regex.scan(~r/^\+\+\+ b\/(.+)$/m, patch)
    |> Enum.map(fn [_, path] -> String.trim(path) end)
    |> Enum.uniq()
  end

  defp diff_paths(_), do: []

  # 16 §2: the run went quiet past T_block with an open step. Classify the modal
  # state if there is a terminal to read (14 §3.2); on `:unknown`, or with no
  # terminal at all, escalate anyway — a stall detected with an unreadable
  # screen still escalates, with less information, on time.
  defp on_blocked(run_id, %Decision{} = d, stage_id, handle, spec, window, append) do
    append.(
      caused_by: d.seq,
      type: :escalation_raised,
      ref: %{"stage_id" => stage_id},
      payload: %{
        "signal" => "blocked_on_human",
        "quiet_for_ms" => window.quiet_for_ms,
        "distinct_execs_since" => window.distinct_execs_since
      }
    )

    modal = classify_modal(handle, spec)

    Task.Supervisor.start_child(Mitos.Run.TaskSupervisor, fn ->
      built =
        Mitos.Decision.Policy.card_for(%{kind: :missing_input}, %{
          run_id: run_id,
          caused_by: d.seq,
          stage_id: stage_id,
          kind: :approve,
          question: blocked_question(stage_id, modal),
          context: %{
            attempted: "#{stage_id} ran for #{div(window.quiet_for_ms, 1000)}s then went quiet",
            blocked_by: modal_summary(modal),
            evidence_refs: []
          },
          options: modal_options(modal)
        })

      case built do
        {:ok, card} -> Mitos.Decision.raise_card(card, to: "operator")
        {:error, _reason} -> :ok
      end
    end)

    :ok
  end

  # No terminal means no modal state to read — which is an answer, not a
  # failure. C3 is optional; C1+C2 carry the run (14 §3.4).
  defp classify_modal(handle, spec) do
    case handle[:terminal] do
      nil ->
        nil

      terminal ->
        classifier = Mitos.Observation.Classifier.for_harness(spec.harness_mod)

        case Mitos.Observation.Terminal.snapshot(terminal) do
          {:ok, grid} -> classifier.classify(grid)
          {:error, _} -> nil
        end
    end
  rescue
    _ -> nil
  end

  defp blocked_question(stage_id, %{kind: kind, prompt_text: text}) when kind != :unknown,
    do: String.slice("#{stage_id} is waiting: #{text || kind}", 0, 200)

  defp blocked_question(stage_id, _modal),
    do: "#{stage_id} has been idle with no progress. What should it do?"

  defp modal_summary(nil), do: "no terminal to read"
  defp modal_summary(%{kind: :unknown}), do: "an unrecognized prompt is on screen"
  defp modal_summary(%{kind: kind}), do: "waiting at a #{kind}"

  defp modal_options(%{kind: :menu, options: [_ | _] = options}),
    do: Enum.map(options, &%{id: &1.id, label: &1.label, consequence: "selects #{&1.label}"})

  defp modal_options(_),
    do: [
      %{id: "approve", label: "Approve and continue", consequence: "the step proceeds"},
      %{id: "abort", label: "Stop the step", consequence: "the step ends; work so far is kept"}
    ]

  defp harvest_effects(append, caused_by, run_id, stage_id, manifest, harvested, meta) do
    diff_artifacts = register_diff(append, caused_by, run_id, harvested.diff)

    Enum.each(harvested.artifacts, fn a ->
      append.(
        type: :artifact_registered,
        caused_by: caused_by,
        ref: %{"artifact" => a["artifact"], "path" => a["path"], "sha256" => a["sha256"]},
        payload: %{"bytes" => a["bytes"]}
      )
    end)

    result =
      if meta.blocked_by do
        %{
          "signal" => "blocked",
          "confidence" => 1.0,
          "summary" => "pre-hook #{meta.blocked_by} blocked the step before model spend",
          "metrics" => %{},
          "evidence" => [],
          "needs" => []
        }
      else
        harvested.result
      end

    append.(
      type: :capability_finished,
      caused_by: caused_by,
      ref: %{"stage_id" => stage_id, "capability_ref" => manifest.capability_id},
      payload: %{
        "result" => result,
        "evidence" => harvested.artifacts ++ diff_artifacts,
        "diff_stat" => harvested.diff.stat,
        "iterations" => meta.loop.iterations,
        "harness_ok" => meta.loop.ok,
        "unverified" => meta.unverified
      }
    )
  end

  # The workspace diff is itself first-class harvested evidence (05 §5.4).
  defp register_diff(_append, _caused_by, _run_id, %{patch: ""}), do: []

  defp register_diff(append, caused_by, run_id, %{patch: patch}) do
    sha = Mitos.sha256(patch)
    store = Path.join(Mitos.run_dir(run_id), "artifacts")
    File.mkdir_p!(store)
    File.write!(Path.join(store, sha), patch)
    artifact = "art://#{run_id}/#{sha}"

    append.(
      type: :artifact_registered,
      caused_by: caused_by,
      ref: %{"artifact" => artifact, "path" => "<workspace diff>", "sha256" => sha, "kind" => "diff"},
      payload: %{"bytes" => byte_size(patch)}
    )

    [%{"kind" => "diff", "artifact" => artifact, "sha256" => sha, "bytes" => byte_size(patch)}]
  end

  # 04 §4.2 iteration-1 context: the original intent, verbatim — pipeline goal,
  # this stage's intent, the run inputs; authority is env, prompt is affordance
  # (01 §1.5), so this only *tells* the agent what the mounts already grant.
  defp compose_prompt(intent, %Decision{} = d, config) do
    stage_intent = d.params[:intent] || d.params["intent"] || ""

    """
    # Mitos capability step

    Pipeline: #{intent.name || "(unnamed)"}
    Goal: #{intent.goal || "(see stage intent)"}
    Stage: #{d.target}#{if stage_intent != "", do: " — " <> stage_intent, else: ""}

    ## Inputs
    #{Mitos.canonical_json(config.inputs)}

    ## Instructions
    Read and follow the skill file at the path in $MITOS_SKILL.
    Work in the current directory (the run workspace).
    Before finishing, write your RESULT JSON to the path in $MITOS_RESULT
    with fields: signal, confidence, summary, metrics, evidence, needs.
    #{constraints_block(intent)}
    """
  end

  defp constraints_block(%{constraints: []}), do: ""

  defp constraints_block(%{constraints: cs}) do
    "\n## Constraints\n" <> Enum.map_join(cs, "\n", &("- " <> to_string(&1)))
  end

  # --- log plumbing (single writer) ---

  # I/O-plane ingress provenance (10 §3/§7): a run started from a connector
  # records the signal + the task it was enqueued from as its opening Effects,
  # so ingress folds into the same log as everything else (no parallel ledger).
  defp emit_ingress(%{ingress: nil} = _config, srv), do: srv

  defp emit_ingress(%{ingress: %{connector: name, intake: intake}, task_id: task_id}, srv) do
    srv
    |> commit_effect(
      type: :signal_received,
      ref: %{"connector" => name},
      payload: %{"intent" => intake[:intent], "idempotency_key" => intake[:idempotency_key]}
    )
    |> commit_effect(
      type: :task_enqueued,
      ref: %{"task" => task_id, "source" => "connector:#{name}"},
      payload: %{"pool" => "io"}
    )
  end

  defp emit_ingress(_config, srv), do: srv

  defp append(srv, %Decision{} = d) do
    {stamped, writer} = Log.append(srv.writer, d)
    {stamped, %{srv | writer: writer, events: srv.events ++ [stamped]}}
  end

  defp commit_effect(srv, fields) do
    fields = Map.new(fields)

    effect =
      Effect.new(
        type: Map.fetch!(fields, :type),
        caused_by: fields[:caused_by],
        ref: normalize_map(fields[:ref] || extract_ref(fields)),
        payload: normalize_map(fields[:payload] || extract_payload(fields))
      )

    {stamped, writer} = Log.append(srv.writer, effect)
    %{srv | writer: writer, events: srv.events ++ [stamped]}
  end

  # Broker effect fields arrive flat (dest/credential_id/status/...); shape them.
  defp extract_ref(fields) do
    fields
    |> Map.take([:dest, :credential_id])
    |> normalize_map()
  end

  defp extract_payload(fields) do
    fields
    |> Map.drop([:type, :caused_by, :run_id, :ref, :dest, :credential_id])
    |> normalize_map()
  end

  defp normalize_map(nil), do: %{}
  defp normalize_map(m), do: Map.new(m, fn {k, v} -> {to_string(k), v} end)

  defp fun_append(run_pid) do
    fn fields ->
      GenServer.call(run_pid, {:append_effect, Map.new(fields)}, 60_000)
    end
  end

  defp fold(srv), do: State.fold(srv.config.run_id, srv.events)

  # Remaining only over declared axes (a 0/absent ceiling = unconstrained, MVP).
  defp remaining(budget, spent) do
    [{"usd", :usd}, {"tokens", :tokens}, {"wall_ms", :wall_ms}]
    |> Enum.reduce(%{}, fn {key, axis}, acc ->
      case budget[key] do
        ceiling when is_number(ceiling) and ceiling > 0 ->
          Map.put(acc, axis, ceiling - Map.fetch!(spent, axis))

        _ ->
          acc
      end
    end)
  end

  defp exhausted?(remaining) when map_size(remaining) == 0, do: false
  defp exhausted?(remaining), do: Enum.any?(remaining, fn {_axis, v} -> v <= 0 end)

  defp finalize(srv, state) do
    # A crash after a terminal Decision can leave run_finished uncommitted —
    # backfill it on recovery so the timeline read (08 §5) is complete.
    srv =
      if Enum.any?(srv.events, &match?(%Effect{type: :run_finished}, &1)) do
        srv
      else
        terminal_seq =
          srv.events
          |> Enum.filter(&match?(%Decision{type: t} when t in [:finish, :abort], &1))
          |> Enum.map(& &1.seq)
          |> Enum.max(fn -> nil end)

        commit_effect(srv,
          type: :run_finished,
          caused_by: terminal_seq,
          payload: %{"status" => to_string(state.status)}
        )
      end

    if srv.config.offer_key do
      Mitos.Facilitator.settle(srv.config.run_id, srv.config.offer_key, state)
    end

    if srv.config.task_id do
      status = if state.status == :finished, do: :done, else: :failed
      Mitos.WorkPool.update_status(srv.config.task_id, status)
    end

    # I/O-plane egress (10 §5): if this tenant has output bindings, drive delivery
    # once the run process has released its writer (single-writer, 01 §4). The
    # driver is idempotent, so a crash between here and delivery is recovered by
    # `mitos deliver <run_id>` / resume.
    maybe_schedule_delivery(srv.config)

    # The sandbox is a copy; the evidence was content-addressed into the run
    # store at harvest (05 §5.4). Keeping it for every successful run is how a
    # disk fills, so a clean finish reclaims it — and a failure keeps it.
    Mitos.GC.reap_after_run(srv.config.run_id, state.status)

    Enum.each(srv.waiters, &GenServer.reply(&1, state))
    {:stop, :normal, %{srv | waiters: []}}
  end

  defp maybe_schedule_delivery(%{run_id: run_id, tenant: tenant}) do
    if Mitos.IO.any_output_bindings?(tenant) do
      run_pid = self()

      Task.Supervisor.start_child(Mitos.Run.TaskSupervisor, fn ->
        ref = Process.monitor(run_pid)
        receive do
          {:DOWN, ^ref, _, _, _} -> :ok
        after
          30_000 -> :ok
        end

        Mitos.IO.deliver_for(run_id)
      end)
    end
  end

  @impl true
  def terminate(_reason, srv) do
    Log.close(srv.writer)
    :ok
  end
end
