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

  alias Mitos.{Broker, Hooks, LaunchPolicy, Log, LoopManager, Pipeline}
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
  def record_broker_effect(run_id, fields) do
    GenServer.call(via(run_id), {:append_effect, fields})
  catch
    :exit, _ -> :ok
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
      waiters: []
    }

    srv =
      if events == [] do
        append_effect(srv, type: :run_started, payload: %{"tenant" => config.tenant})
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

      # A parked run (request_approval) waits for approval_resolved — the MVP
      # Linear policy never parks; an approval surface resumes it post-gate.
      state.status == :parked ->
        {:noreply, srv}

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
    {:reply, :ok, append_effect(srv, fields)}
  end

  def handle_call(:await, from, srv) do
    state = fold(srv)

    if State.terminal?(state) do
      {:reply, state, srv}
    else
      {:noreply, %{srv | waiters: [from | srv.waiters]}}
    end
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
      append_effect(srv,
        type: :run_finished,
        caused_by: d.seq,
        payload: %{"status" => "finished", "result_ref" => d.params[:result_ref] || d.params["result_ref"]}
      )

    send(self(), :step)
    srv
  end

  defp execute(srv, _state, %Decision{type: :abort} = d) do
    srv =
      append_effect(srv,
        type: :run_finished,
        caused_by: d.seq,
        payload: %{"status" => "aborted", "cause" => to_string(d.params[:cause] || d.params["cause"] || "unknown")}
      )

    send(self(), :step)
    srv
  end

  defp execute(srv, _state, %Decision{type: :request_approval} = d) do
    srv =
      append_effect(srv,
        type: :approval_requested,
        caused_by: d.seq,
        payload: d.params
      )

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

    append_effect(srv,
      type: :run_finished,
      caused_by: d.seq,
      payload: %{"status" => "aborted", "cause" => to_string(cause), "detail" => detail}
    )
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
              {%{iterations: 0, tokens: 0, usd: 0.0, wall_ms: 0, ok: false}, hook}

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

        append.(
          type: :budget_charged,
          caused_by: d.seq,
          payload: %{
            "tokens" => loop.tokens,
            "usd" => loop.usd,
            "wall_ms" => System.monotonic_time(:millisecond) - t0
          }
        )

        :ok
      after
        Broker.revoke(token)
        spec.provider.stop(%{handle: handle})
      end
    else
      {:error, reason} -> {:error, reason}
    end
  end

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

  defp append(srv, %Decision{} = d) do
    {stamped, writer} = Log.append(srv.writer, d)
    {stamped, %{srv | writer: writer, events: srv.events ++ [stamped]}}
  end

  defp append_effect(srv, fields) do
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

        append_effect(srv,
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

    Enum.each(srv.waiters, &GenServer.reply(&1, state))
    {:stop, :normal, %{srv | waiters: []}}
  end

  @impl true
  def terminate(_reason, srv) do
    Log.close(srv.writer)
    :ok
  end
end
