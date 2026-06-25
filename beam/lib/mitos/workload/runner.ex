defmodule Mitos.Workload.Runner do
  @moduledoc """
  Executes one workload run synchronously: renders the template, starts an
  agent session, subscribes to its event stream, and blocks the calling
  process until the agent exits (or the workload timeout fires).

  The result favors the engine's `final_answer_candidate` prompt event when
  one was seen; otherwise it falls back to the ANSI-stripped raw output. The
  session id doubles as the run id, so the full trace under
  `.runs/<session_id>/` is the audit artifact for the run.
  """

  require Logger

  alias Mitos.Workload

  @type result :: %{
          result: String.t(),
          run_id: String.t(),
          exit_code: integer() | nil,
          errors: [String.t()]
        }

  # Strips CSI, OSC, and single-char escape sequences from PTY output.
  @ansi ~r/\x1B(?:\[[0-?]*[ -\/]*[@-~]|\][^\x07\x1B]*(?:\x07|\x1B\\)?|[@-Z\\-_])/

  @spec execute(%Workload{}, map(), keyword()) :: {:ok, result()} | {:error, term()}
  def execute(%Workload{} = workload, bindings, opts \\ []) when is_map(bindings) do
    with {:ok, provider} <- Mitos.SandboxPolicy.select_workload_provider(workload),
         {:ok, task} <- Workload.render(workload, bindings) do
      task_context = task_context(workload, bindings, task, opts)
      run(workload, task, provider, opts, task_context)
    end
  end

  @doc """
  Run the seller publication validation gate: policy compile, T2 dry-run, and
  RESULT/schema/evidence checks without x402 admission or settlement.
  """
  def validate_publication(%Workload{} = workload, bindings, opts \\ []) when is_map(bindings) do
    validation_opts =
      opts
      |> Keyword.put(:pool, "publication:validation")
      |> Keyword.put(:source, "publication:validation")

    with :ok <- publishable?(workload),
         {:ok, _provider} <- Mitos.SandboxPolicy.select_workload_provider(workload),
         :ok <- publication_preflight(workload),
         {:ok, result} <- execute(workload, bindings, validation_opts) do
      proof = Mitos.Proof.build(result.run_id)
      validation = publication_validation(workload, result, proof)

      if validation["status"] == "passed" do
        {:ok, validation}
      else
        {:error, {:publication_validation_failed, validation}}
      end
    end
  end

  @doc """
  Dispatch a work-pool Task through the workload execution path.

  The Task payload must name a workload either as `payload.workload_id` or as a
  `pipeline` of the form `workload:<id>`.
  """
  def dispatch_task(%{} = task, _opts \\ []) do
    with {:ok, workload_id} <- task_workload_id(task),
         {:ok, workload} <- Workload.fetch(workload_id),
         {:ok, result} <-
           execute(workload, task_bindings(task),
             task_id: task["id"],
             tenant: task["tenant"],
             source: task["source"],
             pool: task["pool"],
             priority: task["priority"],
             budget: task["budget"]
           ) do
      {:ok,
       %{
         "run_id" => result.run_id,
         "summary" => result.result,
         "exit_code" => result.exit_code,
         "errors" => result.errors,
         "workload_id" => workload.id
       }}
    else
      {:error, reason} -> {:error, reason}
    end
  end

  defp run(workload, task, Mitos.SandboxProvider.LocalSidecar, opts, task_context) do
    run_opts = opts
    session_id = "wl-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)
    Mitos.PubSub.subscribe(session_id)

    agent_opts =
      [
        provider: workload.provider,
        task: task,
        session_id: session_id,
        transport_order: workload.transport_order,
        idle_ms: workload.idle_ms
      ]
      |> maybe(:knowledge, knowledge_slice(workload, task, run_opts))
      |> maybe(:env, sandbox_env(workload))
      |> maybe(:cwd, workload.cwd)
      |> maybe(:schema_root, workload.package_root)
      |> maybe(:seller_id, workload.seller_id)
      |> maybe(:budget, Keyword.get(run_opts, :budget))
      |> with_capability(workload.capability)
      |> with_trace_tenant(Keyword.get(run_opts, :tenant))
      |> maybe(:trace_base_dir, Keyword.get(run_opts, :trace_base_dir))

    with {:ok, agent_opts} <- with_resolved_skill(agent_opts, workload, run_opts),
         {:ok, agent_opts} <-
           Mitos.CapabilityRuntime.prepare_agent_options(workload, agent_opts, run_opts) do
      start_local_agent(workload, task_context, session_id, agent_opts)
    else
      {:error, {:skill_unavailable, _skill, _reason} = reason} ->
        task_trace = start_task_trace(session_id, task_context, 0, agent_opts)
        result = {:error, {:runtime_prepare_failed, reason}}
        finish_task_trace(task_trace, task_context, result)
        result

      {:error, {:skill_resolution_failed, _skill, _reason} = reason} ->
        task_trace = start_task_trace(session_id, task_context, 0, agent_opts)
        result = {:error, {:runtime_prepare_failed, reason}}
        finish_task_trace(task_trace, task_context, result)
        result

      {:error, reason} ->
        task_trace = start_task_trace(session_id, task_context, 0, agent_opts)
        result = {:error, {:runtime_prepare_failed, reason}}
        finish_task_trace(task_trace, task_context, result)
        result
    end
  end

  defp run(workload, task, provider, opts, task_context) when is_atom(provider) do
    session_id = "wl-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)

    with {:ok, launch_spec} <- launch_spec(workload, task, session_id, opts) do
      case open_remote_trace(session_id, launch_spec, opts) do
        {:ok, trace, decision_seq} ->
          trace = start_task_trace(trace, task_context, decision_seq)

          case start_remote_session(provider, launch_spec, workload, session_id) do
            {:ok, session} ->
              case run_remote_session(
                     provider,
                     session,
                     trace,
                     decision_seq,
                     launch_spec,
                     workload.package_root,
                     session_id,
                     workload.timeout_ms
                   ) do
                {:ok, trace, result} ->
                  finish_task_trace(trace, task_context, result)
                  result

                {:timeout, trace} ->
                  result = {:error, {:timeout, session_id}}
                  finish_task_trace(trace, task_context, result)
                  result

                {:error, trace, reason} ->
                  result = {:error, {:launch_failed, session_id, reason}}
                  finish_task_trace(trace, task_context, result)
                  result
              end

            {:error, reason} ->
              _ = Mitos.KeyVault.revoke_run(session_id)
              result = {:error, {:launch_failed, session_id, reason}}
              finish_task_trace(trace, task_context, result)
              result
          end

        {:error, reason} ->
          remote_trace_open_error(session_id, reason)
      end
    else
      {:error, reason} ->
        _ = Mitos.KeyVault.revoke_run(session_id)
        {:error, {:launch_failed, session_id, reason}}
    end
  end

  defp remote_trace_open_error(_session_id, {:budget_exceeded, _abort} = reason),
    do: {:error, reason}

  defp remote_trace_open_error(session_id, reason),
    do: {:error, {:launch_failed, session_id, reason}}

  defp start_local_agent(workload, task_context, session_id, opts) do
    case Mitos.Agent.start(opts) do
      {:ok, agent_run} ->
        task_trace = start_task_trace(session_id, task_context, 0, opts)
        deadline = System.monotonic_time(:millisecond) + workload.timeout_ms
        acc = %{raw: [], final: nil, candidate: nil, errors: []}
        result = collect(agent_run, workload, acc, deadline)
        finish_task_trace(task_trace, task_context, result)
        result

      {:error, {:budget_exceeded, _abort} = reason} ->
        {:error, reason}

      {:error, reason} ->
        task_trace = start_task_trace(session_id, task_context, 0, opts)
        result = {:error, {:launch_failed, reason}}
        finish_task_trace(task_trace, task_context, result)
        result
    end
  end

  defp start_remote_session(provider, launch_spec, workload, session_id) do
    with {:ok, handle} <- provider.prepare(launch_spec),
         {:ok, session} <- provider.start(handle, [], sandbox_env(workload)) do
      {:ok, session}
    else
      {:error, reason} ->
        _ = Mitos.KeyVault.revoke_run(session_id)
        {:error, reason}
    end
  end

  defp run_remote_session(
         provider,
         session,
         trace,
         decision_seq,
         launch_spec,
         schema_root,
         session_id,
         timeout_ms
       ) do
    try do
      case collect_remote(provider, session, trace, decision_seq, timeout_ms) do
        {:ok, trace, acc} ->
          case provider.harvest(session) do
            {:ok, harvest} ->
              harvest = validate_remote_harvest(harvest, launch_spec, schema_root)
              trace = trace_remote_finish(trace, decision_seq, acc, harvest)
              {:ok, trace, finish_remote(acc, harvest, session_id)}

            {:error, reason} ->
              {:error, trace, reason}
          end

        {:timeout, trace, acc} ->
          control_result = interrupt_remote_session(provider, session, timeout_ms)
          trace = trace_remote_timeout(trace, decision_seq, timeout_ms, control_result)
          acc = %{acc | exit_code: nil, errors: acc.errors ++ ["timeout"]}

          with {:ok, harvest} <- provider.harvest(session) do
            harvest = validate_remote_harvest(harvest, launch_spec, schema_root)
            trace = trace_remote_finish(trace, decision_seq, acc, harvest, status: "timeout")
            {:timeout, trace}
          else
            {:error, reason} -> {:error, trace, {:timeout_harvest_failed, reason}}
          end

        {:error, trace, reason} ->
          {:error, trace, reason}
      end
    rescue
      exception -> {:error, trace, Exception.message(exception)}
    catch
      kind, reason -> {:error, trace, {kind, reason}}
    after
      stop_remote_session(provider, session)
      _ = Mitos.KeyVault.revoke_run(session_id)
    end
  end

  defp stop_remote_session(provider, session) do
    provider.stop(session)
  rescue
    exception ->
      Logger.warning("remote sandbox stop failed: #{Exception.message(exception)}")
      {:error, exception}
  catch
    kind, reason ->
      Logger.warning("remote sandbox stop failed: #{inspect({kind, reason})}")
      {:error, {kind, reason}}
  end

  defp collect(agent_run, workload, acc, deadline) do
    session_id = agent_run.session_id
    remaining = deadline - System.monotonic_time(:millisecond)

    if remaining <= 0 do
      stop_session(session_id)
      {:error, {:timeout, session_id}}
    else
      receive do
        {:mitos, ^session_id, {:raw, bytes}} ->
          collect(agent_run, workload, %{acc | raw: [acc.raw, bytes]}, deadline)

        {:mitos, ^session_id, {:prompt, {:final_answer_candidate, text}}} ->
          collect(agent_run, workload, %{acc | candidate: text}, deadline)

        # Headless adapters (e.g. claude.print) report the agent's answer as a
        # typed FINAL_OUTPUT event; it is authoritative over codec candidates.
        {:mitos, ^session_id, {:event, %{"type" => "FINAL_OUTPUT", "text" => text}}}
        when is_binary(text) ->
          collect(agent_run, workload, %{acc | final: text}, deadline)

        {:mitos, ^session_id, {:event, %{"type" => type} = event}}
        when type in ["exit", "PROCESS_EXITED"] ->
          finish(acc, session_id, Map.get(event, "code", 0))

        {:mitos, ^session_id, {:event, %{"type" => "sidecar_exit"} = event}} ->
          finish(acc, session_id, Map.get(event, "code"))

        {:mitos, ^session_id, {:event, %{"type" => type} = event}}
        when type in ["ERROR", "error"] ->
          detail = Map.get(event, "detail", Map.get(event, "message", inspect(event)))

          collect(
            agent_run,
            workload,
            %{acc | errors: acc.errors ++ [to_string(detail)]},
            deadline
          )

        {:mitos, ^session_id,
         {:event,
          %{
            "type" => "ACTION_REQUESTED",
            "origin" => "terminal_scrape",
            "request_id" => request_id
          }}}
        when workload.approvals == "deny" ->
          # Headless workload runs answer no prompts; denying keeps the run
          # moving to a terminal state instead of hanging until timeout.
          Mitos.Session.deny(agent_run.pid, request_id, "workload runs are non-interactive")
          collect(agent_run, workload, acc, deadline)

        {:mitos, ^session_id, _other} ->
          collect(agent_run, workload, acc, deadline)
      after
        remaining ->
          stop_session(session_id)
          {:error, {:timeout, session_id}}
      end
    end
  end

  defp finish(acc, session_id, exit_code) do
    text = acc.final || acc.candidate || clean(IO.iodata_to_binary(acc.raw))

    case String.trim(text || "") do
      "" ->
        {:error, {:empty_result, session_id, acc.errors}}

      result ->
        {:ok, %{result: result, run_id: session_id, exit_code: exit_code, errors: acc.errors}}
    end
  end

  defp collect_remote(provider, session, trace, decision_seq, timeout_ms) do
    deadline = System.monotonic_time(:millisecond) + timeout_ms
    ref = make_ref()
    parent = self()

    {pid, monitor} =
      spawn_monitor(fn ->
        try do
          provider.stream(session)
          |> Enum.each(fn event -> send(parent, {:remote_stream_event, ref, event}) end)

          send(parent, {:remote_stream_done, ref})
        rescue
          exception ->
            send(parent, {:remote_stream_failed, ref, Exception.message(exception)})
        catch
          kind, reason ->
            send(parent, {:remote_stream_failed, ref, {kind, reason}})
        end
      end)

    collect_remote_messages(trace, decision_seq, remote_acc(), ref, pid, monitor, deadline)
  end

  defp collect_remote_messages(trace, decision_seq, acc, ref, pid, monitor, deadline) do
    remaining = deadline - System.monotonic_time(:millisecond)

    if remaining <= 0 do
      stop_remote_collector(pid, monitor)
      {:timeout, trace_token_usage(trace, decision_seq, acc.usage), acc}
    else
      receive do
        {:remote_stream_event, ^ref, event} ->
          {trace, acc} = record_remote_event(trace, decision_seq, acc, event)
          collect_remote_messages(trace, decision_seq, acc, ref, pid, monitor, deadline)

        {:remote_stream_done, ^ref} ->
          Process.demonitor(monitor, [:flush])
          {:ok, trace_token_usage(trace, decision_seq, acc.usage), acc}

        {:remote_stream_failed, ^ref, reason} ->
          stop_remote_collector(pid, monitor)
          {:error, trace_token_usage(trace, decision_seq, acc.usage), reason}

        {:DOWN, ^monitor, :process, ^pid, :normal} ->
          {:ok, trace_token_usage(trace, decision_seq, acc.usage), acc}

        {:DOWN, ^monitor, :process, ^pid, reason} ->
          {:error, trace_token_usage(trace, decision_seq, acc.usage), reason}
      after
        remaining ->
          stop_remote_collector(pid, monitor)
          {:timeout, trace_token_usage(trace, decision_seq, acc.usage), acc}
      end
    end
  end

  defp stop_remote_collector(pid, monitor) do
    if Process.alive?(pid), do: Process.exit(pid, :kill)

    receive do
      {:DOWN, ^monitor, :process, ^pid, _reason} -> :ok
    after
      100 -> Process.demonitor(monitor, [:flush])
    end
  end

  defp record_remote_event(trace, _decision_seq, acc, event) do
    event = normalize_event(event)
    source_sequence = trace.seq
    usage = token_usage_entry(event, source_sequence)
    trace = Mitos.Trace.append_event(trace, event)

    acc =
      acc
      |> maybe_add_usage(usage)
      |> case do
        acc ->
          case event do
            %{"type" => "FINAL_OUTPUT", "text" => text} when is_binary(text) ->
              %{acc | final: text}

            %{"type" => "OUTPUT", "bytes" => bytes} when is_binary(bytes) ->
              %{acc | raw: [acc.raw, bytes]}

            %{"type" => type, "code" => code} when type in ["exit", "PROCESS_EXITED"] ->
              %{acc | exit_code: code}

            %{"type" => type} when type in ["ERROR", "error"] ->
              detail = Map.get(event, "detail", Map.get(event, "message", inspect(event)))
              %{acc | errors: acc.errors ++ [to_string(detail)]}

            _event ->
              acc
          end
      end

    {trace, acc}
  end

  defp remote_acc, do: %{final: nil, raw: [], errors: [], exit_code: 0, usage: []}

  defp token_usage_entry(%{"type" => type} = event, source_sequence) when is_binary(type) do
    if String.downcase(type) == "token_usage" do
      {token_usage_ref(event, source_sequence), token_usage_payload(event)}
    end
  end

  defp token_usage_entry(_event, _source_sequence), do: nil

  defp maybe_add_usage(acc, nil), do: acc
  defp maybe_add_usage(acc, usage), do: %{acc | usage: acc.usage ++ [usage]}

  defp trace_token_usage(trace, _decision_seq, []), do: trace

  defp trace_token_usage(trace, decision_seq, usage_entries) do
    Mitos.Trace.append_effect(trace, %{
      type: "budget_charged",
      caused_by: decision_seq,
      ref: aggregate_token_usage_ref(usage_entries),
      payload: aggregate_token_usage_payload(usage_entries)
    })
  end

  defp aggregate_token_usage_ref(usage_entries) do
    refs = Enum.map(usage_entries, fn {ref, _payload} -> ref end)

    %{
      "source_type" => common_ref(refs, "source_type") || "token_usage",
      "source_sequences" => Enum.flat_map(refs, &List.wrap(&1["source_sequence"])),
      "provider" => common_ref(refs, "provider"),
      "model" => common_ref(refs, "model")
    }
    |> Enum.reject(fn {_key, value} -> value in [nil, "", []] end)
    |> Map.new()
  end

  defp aggregate_token_usage_payload(usage_entries) do
    payloads = Enum.map(usage_entries, fn {_ref, payload} -> payload end)

    %{
      "billing_basis" => "provider_token_usage",
      "tokens" => sum_payload(payloads, "tokens"),
      "input_tokens" => sum_payload(payloads, "input_tokens"),
      "output_tokens" => sum_payload(payloads, "output_tokens"),
      "cached_tokens" => sum_payload(payloads, "cached_tokens"),
      "usd" => sum_payload(payloads, "usd"),
      "wall_ms" => sum_payload(payloads, "wall_ms")
    }
  end

  defp common_ref(refs, key) do
    values =
      refs
      |> Enum.map(& &1[key])
      |> Enum.reject(&(&1 in [nil, ""]))
      |> Enum.uniq()

    case values do
      [value] -> value
      _ -> nil
    end
  end

  defp sum_payload(payloads, key), do: Enum.sum(Enum.map(payloads, &numeric(&1[key])))

  defp trace_remote_finish(trace, decision_seq, acc, harvest, opts \\ []) do
    status = Keyword.get(opts, :status, if(acc.exit_code == 0, do: "completed", else: "failed"))

    Mitos.Trace.append_effect(trace, %{
      type: "capability_finished",
      caused_by: decision_seq,
      ref: %{"result" => harvest.result["path"] || "RESULT"},
      payload: %{
        "status" => status,
        "exit_code" => acc.exit_code,
        "signal" => nil,
        "final_output" => acc.final,
        "harvest" => Mitos.Harvest.effect_payload(harvest),
        "result" => harvest.result,
        "verification" => %{"verified" => true, "failed_hooks" => []},
        "hooks" => %{"pre" => [], "post" => []}
      }
    })
  end

  defp trace_remote_timeout(trace, decision_seq, timeout_ms, control_result) do
    Mitos.Trace.append_effect(trace, %{
      type: "capability_interrupted",
      caused_by: decision_seq,
      ref: %{"reason" => "timeout"},
      payload: %{
        "reason" => "timeout",
        "timeout_ms" => timeout_ms,
        "control" => control_payload(control_result)
      }
    })
  end

  defp interrupt_remote_session(provider, session, timeout_ms) do
    case safe_remote_control(
           provider,
           session,
           {:steer, :interrupt, reason: "timeout", timeout_ms: timeout_ms}
         ) do
      :ok -> :ok
      {:error, :unsupported_control} -> safe_remote_control(provider, session, {:signal, 2})
      {:error, reason} -> {:error, reason}
    end
  end

  defp safe_remote_control(provider, session, command) do
    provider.control(session, command)
  rescue
    exception -> {:error, Exception.message(exception)}
  catch
    kind, reason -> {:error, {kind, reason}}
  end

  defp control_payload(:ok), do: %{"status" => "sent"}
  defp control_payload({:error, reason}), do: %{"status" => "failed", "reason" => inspect(reason)}

  defp task_context(workload, bindings, rendered_task, opts) do
    task_id =
      Keyword.get(opts, :task_id) ||
        Map.get(bindings, "task_id") ||
        "task-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)

    %{
      id: task_id,
      tenant: tenant_or_default(Keyword.get(opts, :tenant)),
      source: Keyword.get(opts, :source, "manual"),
      pool:
        Keyword.get_lazy(opts, :pool, fn ->
          if Mitos.Settlement.paid?(workload), do: "paid:default", else: "interactive"
        end),
      priority: Keyword.get(opts, :priority, 0),
      pipeline: Keyword.get(opts, :pipeline, "workload:#{workload.id}"),
      payload: %{
        "intent" => rendered_task,
        "workload_id" => workload.id,
        "inputs" => bindings
      },
      budget: Keyword.get(opts, :budget, %{}),
      provenance: Keyword.get(opts, :provenance, %{})
    }
  end

  defp start_task_trace(session_id, task_context, caused_by, opts)
       when is_binary(session_id) do
    [run_id: session_id]
    |> maybe(:base_dir, Keyword.get(opts, :trace_base_dir))
    |> Mitos.Trace.open!()
    |> start_task_trace(task_context, caused_by)
  end

  defp start_task_trace(trace, task_context, caused_by) do
    trace
    |> Mitos.WorkPool.append_enqueue(task_context, caused_by: caused_by)
    |> Mitos.WorkPool.append_status(task_context.id, :scheduled,
      caused_by: caused_by,
      run_id: trace.run_id
    )
    |> Mitos.WorkPool.append_status(task_context.id, :running,
      caused_by: caused_by,
      run_id: trace.run_id
    )
  end

  defp finish_task_trace(trace, task_context, {:ok, result}) do
    Mitos.WorkPool.append_status(trace, task_context.id, :done,
      caused_by: 0,
      run_id: result.run_id,
      result: %{"summary" => result.result, "exit_code" => result.exit_code}
    )
  end

  defp finish_task_trace(trace, task_context, {:error, reason}) do
    Mitos.WorkPool.append_status(trace, task_context.id, :failed,
      caused_by: 0,
      reason: inspect(reason)
    )
  end

  defp finish_remote(acc, harvest, session_id) do
    text =
      acc.final ||
        harvest.result["summary"] ||
        harvest.result["signal"] ||
        clean(IO.iodata_to_binary(acc.raw))

    case String.trim(to_string(text || "")) do
      "" ->
        {:error, {:empty_result, session_id, acc.errors}}

      result ->
        {:ok, %{result: result, run_id: session_id, exit_code: acc.exit_code, errors: acc.errors}}
    end
  end

  defp publishable?(%Workload{capability: nil}),
    do: {:error, {:invalid_workload, "paid publication requires a capability manifest"}}

  defp publishable?(%Workload{} = workload) do
    if Mitos.Settlement.paid?(workload) do
      :ok
    else
      {:error, :payment_not_configured}
    end
  end

  defp publication_validation(%Workload{} = workload, result, proof) do
    delivery = proof["delivery"] || %{}
    harvested_result = delivery["result"] || %{}
    artifacts = proof["artifacts"] || []
    checks = publication_checks(workload, result, harvested_result, artifacts)

    %{
      "schema_id" => "gentix.capability_publication_validation",
      "schema_version" => 1,
      "workload_id" => workload.id,
      "capability_ref" => workload.capability && workload.capability.id,
      "status" => validation_status(checks),
      "checks" => checks,
      "dry_run" => %{
        "run_id" => result.run_id,
        "exit_code" => result.exit_code,
        "errors" => result.errors
      },
      "evidence" => %{
        "artifacts" => proof["artifacts"],
        "delivery" => delivery,
        "proof" => proof
      }
    }
  end

  defp publication_preflight(%Workload{} = workload) do
    case key_binding_check(workload) do
      %{"status" => "passed"} ->
        :ok

      %{} = key_binding ->
        checks = [
          policy_compile_check(workload),
          key_binding,
          %{"name" => "dry_run", "status" => "skipped", "detail" => "preflight failed"}
        ]

        {:error,
         {:publication_validation_failed,
          %{
            "schema_id" => "gentix.capability_publication_validation",
            "schema_version" => 1,
            "workload_id" => workload.id,
            "capability_ref" => workload.capability && workload.capability.id,
            "status" => validation_status(checks),
            "checks" => checks,
            "dry_run" => nil,
            "evidence" => %{}
          }}}
    end
  end

  defp publication_checks(workload, result, harvested_result, artifacts) do
    [
      policy_compile_check(workload),
      key_binding_check(workload),
      dry_run_check(result),
      result_schema_check(workload, harvested_result),
      evidence_check(workload, harvested_result, artifacts)
    ]
  end

  defp policy_compile_check(workload) do
    %{
      "name" => "policy_compile",
      "status" => "passed",
      "detail" => "selected sandbox provider satisfies #{workload.capability.trust_tier}"
    }
  end

  defp key_binding_check(%Workload{capability: capability, seller_id: seller_id}) do
    bindings =
      capability
      |> capability_egress_policy()
      |> Enum.flat_map(&credential_binding(&1, seller_id))

    failures = Enum.reject(bindings, &(&1["status"] == "passed"))

    cond do
      failures != [] ->
        %{
          "name" => "key_bindings",
          "status" => "failed",
          "detail" => "declared credential is not registered for seller and destination",
          "failures" => failures
        }

      bindings == [] ->
        %{"name" => "key_bindings", "status" => "passed", "checked" => 0}

      true ->
        %{"name" => "key_bindings", "status" => "passed", "checked" => length(bindings)}
    end
  end

  defp capability_egress_policy(%Mitos.Capability{egress_policy: policy}) when is_list(policy),
    do: policy

  defp capability_egress_policy(_capability), do: []

  defp credential_binding(%{} = rule, seller_id) do
    credential_id = value(rule, :credential_id) || value(rule, :credential)
    dest = value(rule, :dest) || value(rule, :destination)

    cond do
      credential_id in [nil, ""] ->
        []

      seller_id in [nil, ""] ->
        [
          binding_failure(
            rule,
            dest,
            credential_id,
            "seller_id is required for credential binding"
          )
        ]

      true ->
        case Mitos.KeyVault.resolve(credential_id, dest || "", seller_id: seller_id) do
          {:ok, %{} = ref} ->
            [
              %{
                "status" => "passed",
                "rule" => value(rule, :id),
                "dest" => ref["dest"],
                "credential_id" => ref["credential_id"],
                "seller_id" => ref["seller_id"]
              }
              |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
              |> Map.new()
            ]

          {:ok, nil} ->
            []

          {:error, reason} ->
            [binding_failure(rule, dest, credential_id, reason)]
        end
    end
  end

  defp credential_binding(_rule, _seller_id), do: []

  defp binding_failure(rule, dest, credential_id, reason) do
    %{
      "status" => "failed",
      "rule" => value(rule, :id),
      "dest" => dest,
      "credential_id" => credential_id,
      "reason" => reason
    }
    |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
    |> Map.new()
  end

  defp dry_run_check(%{exit_code: exit_code, errors: errors}) do
    if exit_code in [0, nil] and errors == [] do
      %{"name" => "dry_run", "status" => "passed"}
    else
      %{
        "name" => "dry_run",
        "status" => "failed",
        "detail" => "dry-run exited #{inspect(exit_code)}",
        "errors" => errors
      }
    end
  end

  defp result_schema_check(%Workload{capability: capability}, result) do
    schema = capability && capability.result && capability.result["schema"]
    needs = List.wrap(result["needs"])

    cond do
      schema in [nil, ""] ->
        %{
          "name" => "result_schema",
          "status" => "failed",
          "detail" => "capability result.schema is required for publication validation"
        }

      Enum.any?(needs, &(&1 in ["result_schema_invalid", "result_schema_unavailable"])) ->
        %{
          "name" => "result_schema",
          "status" => "failed",
          "detail" => result["summary"],
          "schema_error" => result["schema_error"]
        }

      Enum.any?(needs, &(&1 in ["result_invalid", "result_missing"])) or
          result["synthetic"] == true ->
        %{
          "name" => "result_schema",
          "status" => "failed",
          "detail" => result["summary"] || "RESULT is synthetic"
        }

      true ->
        %{"name" => "result_schema", "status" => "passed"}
    end
  end

  defp evidence_check(%Workload{capability: capability}, result, artifacts) do
    declared =
      capability
      |> case do
        %Mitos.Capability{result: %{} = result_spec} -> List.wrap(result_spec["evidence"])
        _ -> []
      end
      |> Enum.filter(&is_binary/1)

    missing = List.wrap(result["evidence_missing"])
    harvested = List.wrap(result["evidence"]) ++ List.wrap(artifacts)
    missing_declared = missing_declared_evidence(declared, harvested)

    cond do
      missing != [] ->
        %{
          "name" => "evidence",
          "status" => "failed",
          "detail" => "declared evidence is missing",
          "missing" => missing
        }

      missing_declared != [] ->
        %{
          "name" => "evidence",
          "status" => "failed",
          "detail" => "declared evidence was not content-addressed by harvest",
          "missing" => missing_declared
        }

      true ->
        %{
          "name" => "evidence",
          "status" => "passed",
          "declared" => declared,
          "harvested" => length(harvested)
        }
    end
  end

  defp missing_declared_evidence(declared, harvested) do
    Enum.reject(declared, fn path ->
      Enum.any?(harvested, &content_addressed_evidence?(&1, path))
    end)
  end

  defp content_addressed_evidence?(%{} = artifact, path) do
    artifact_path = artifact["path"] || artifact["artifact"]
    sha = artifact["sha256"]

    artifact_path == path and is_binary(sha) and String.starts_with?(sha, "sha256:") and
      artifact["missing"] != true
  end

  defp content_addressed_evidence?(_artifact, _path), do: false

  defp validation_status(checks) do
    if Enum.all?(checks, &(Map.get(&1, "status") == "passed")), do: "passed", else: "failed"
  end

  defp validate_remote_harvest(
         %{result: %{} = result, diff: diff} = harvest,
         launch_spec,
         schema_root
       ) do
    %{
      harvest
      | result:
          Mitos.Harvest.validate_result_schema(result, nil, launch_spec, diff || %{}, schema_root)
    }
  end

  defp validate_remote_harvest(harvest, _launch_spec, _schema_root), do: harvest

  defp task_workload_id(%{"payload" => %{"workload_id" => workload_id}})
       when is_binary(workload_id) and workload_id != "",
       do: {:ok, workload_id}

  defp task_workload_id(%{"pipeline" => "workload:" <> workload_id})
       when workload_id != "",
       do: {:ok, workload_id}

  defp task_workload_id(_task), do: {:error, :missing_workload_id}

  defp task_bindings(%{"payload" => %{"inputs" => %{} = inputs}}), do: inputs
  defp task_bindings(_task), do: %{}

  defp open_remote_trace(session_id, launch_spec, opts) do
    trace =
      [run_id: session_id, launch_spec: launch_spec]
      |> with_trace_tenant(Keyword.get(opts, :tenant))
      |> Mitos.Trace.open!()

    case Mitos.Trace.append_decision_checked(trace, %{
           actor: "orchestrator",
           type: "advance",
           target: "agent:#{launch_spec.provider}",
           params: %{
             provider: launch_spec.provider,
             task: launch_spec.task,
             transport_order: launch_spec.transport_order,
             capability_ref: Map.get(launch_spec, :capability_ref),
             trust_tier: Map.get(launch_spec, :trust_tier)
           },
           inputs_hash: inputs_hash(launch_spec),
           rationale: "Launch #{launch_spec.provider} through a remote sandbox provider",
           budget:
             Keyword.get(opts, :budget, %{
               "tokens" => 0,
               "usd" => 0,
               "wall_ms" => 0,
               "remaining" => %{}
             })
         }) do
      {:ok, trace, %{"seq" => decision_seq}} ->
        trace =
          Mitos.Trace.append_effect(trace, %{
            type: "capability_started",
            caused_by: decision_seq,
            ref: %{
              "provider" => launch_spec.provider,
              "transport" => "remote_microvm",
              "capability_ref" => Map.get(launch_spec, :capability_ref)
            },
            payload:
              %{"harness" => launch_spec.provider, "transport" => "remote_microvm"}
              |> maybe_put_string(
                "resolved_skill",
                resolved_skill_effect(Map.get(launch_spec, :resolved_skill))
              )
          })

        case lease_remote_credentials(session_id, launch_spec) do
          {:ok, _leases} ->
            {:ok, trace, decision_seq}

          {:error, reason} ->
            _ = Mitos.KeyVault.revoke_run(session_id)
            {:error, reason}
        end

      {:abort, _trace, abort} ->
        {:error, {:budget_exceeded, abort}}
    end
  end

  defp lease_remote_credentials(session_id, %{trust_tier: "T2"} = launch_spec),
    do: Mitos.KeyVault.lease_run_strict(session_id, launch_spec)

  defp lease_remote_credentials(session_id, launch_spec),
    do: Mitos.KeyVault.lease_run(session_id, launch_spec)

  defp launch_spec(workload, task, session_id, opts) do
    launch_fields =
      [
        version: 1,
        run_id: session_id,
        provider: workload.provider,
        task: task,
        transport_order: workload.transport_order
      ]
      |> maybe(:knowledge, knowledge_slice(workload, task, opts))
      |> maybe(:workspace, Keyword.get(opts, :workspace))
      |> maybe(:cwd, launch_cwd(workload))
      |> maybe(:seller_id, workload.seller_id)
      |> with_capability(workload.capability)

    with {:ok, launch_fields} <- with_resolved_skill(launch_fields, workload, opts) do
      {:ok,
       launch_fields
       |> Map.new()
       |> with_launch_posture()}
    end
  end

  defp knowledge_slice(workload, task, opts) do
    with %{} = state <- memory_state(opts),
         tenant_id when is_binary(tenant_id) <- tenant_id(opts),
         %{"entries" => [_ | _]} = slice <-
           Mitos.Memory.context_slice(state, task,
             tenant_id: tenant_id,
             scope: memory_scope(workload, opts),
             k: Keyword.get(opts, :memory_k, 5),
             max_chars: Keyword.get(opts, :memory_max_chars, 8_000)
           ) do
      slice
    else
      _none -> nil
    end
  end

  defp memory_state(opts) do
    cond do
      is_map(Keyword.get(opts, :memory_state)) ->
        Keyword.fetch!(opts, :memory_state)

      is_list(Keyword.get(opts, :memory_events)) ->
        opts |> Keyword.fetch!(:memory_events) |> Mitos.Memory.fold_events()

      true ->
        nil
    end
  end

  defp memory_scope(workload, opts) do
    %{
      "capability_ref" => workload.capability && workload.capability.id,
      "model" => Keyword.get(opts, :model),
      "harness" => workload.provider
    }
    |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
    |> Map.new()
  end

  defp tenant_id(opts) do
    tenant = Keyword.get(opts, :tenant, %{})

    value(tenant, :tenant_id) ||
      value(tenant, :org_id) ||
      value(tenant, :org) ||
      Keyword.get(opts, :tenant_id) ||
      "default"
  end

  defp tenant_or_default(tenant) do
    tenant = normalize_tenant(tenant)

    if tenant == [] do
      %{"org" => "default", "project" => "default"}
    else
      Map.new(tenant)
    end
  end

  defp with_trace_tenant(opts, tenant) when is_list(opts) do
    tenant = tenant_or_default(tenant)

    opts
    |> maybe(:org_id, tenant["org"] || tenant["org_id"] || tenant["tenant_id"])
    |> maybe(:project_id, tenant["project"] || tenant["project_id"])
    |> maybe(:actor_id, tenant["actor"] || tenant["actor_id"] || tenant["user_id"])
  end

  defp normalize_tenant(%{} = tenant) do
    tenant
    |> Enum.flat_map(fn
      {key, value} when is_binary(value) and value != "" -> [{to_string(key), value}]
      _other -> []
    end)
  end

  defp normalize_tenant(_tenant), do: []

  defp value(%{} = map, key) do
    Map.get(map, key) || Map.get(map, to_string(key))
  end

  defp value(_map, _key), do: nil

  defp inputs_hash(launch_spec) do
    digest =
      :crypto.hash(:sha256, canonical_json(launch_spec))
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end

  defp canonical_json(%{} = map) do
    body =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map_join(",", fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> body <> "}"
  end

  defp canonical_json(list) when is_list(list),
    do: "[" <> Enum.map_join(list, ",", &canonical_json/1) <> "]"

  defp canonical_json(value), do: Jason.encode!(value)

  defp normalize_event(%{} = event),
    do: Map.new(event, fn {key, value} -> {to_string(key), value} end)

  defp normalize_event(other), do: %{"type" => "EVENT", "value" => inspect(other)}

  defp clean(bytes) do
    bytes
    |> String.replace(@ansi, "")
    |> String.replace("\r\n", "\n")
    |> String.replace("\r", "\n")
  end

  defp stop_session(session_id) do
    Mitos.SessionSupervisor.stop_session(session_id)
  catch
    :exit, _ -> :ok
  end

  defp sandbox_env(%Workload{capability: %Mitos.Capability{trust_tier: "T2"}}), do: nil
  defp sandbox_env(%Workload{} = workload), do: Workload.resolved_env(workload)

  defp launch_cwd(%Workload{capability: %Mitos.Capability{trust_tier: "T2"}}), do: nil
  defp launch_cwd(%Workload{} = workload), do: workload.cwd

  defp token_usage_ref(event, source_sequence) do
    %{
      "source_sequence" => source_sequence,
      "source_type" => event["type"],
      "provider" => event["provider"],
      "model" => event["model"]
    }
    |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
    |> Map.new()
  end

  defp token_usage_payload(event) do
    usage = usage_payload(event)
    input_tokens = numeric(usage["input_tokens"] || usage["prompt_tokens"])
    output_tokens = numeric(usage["output_tokens"] || usage["completion_tokens"])
    cached_tokens = numeric(usage["cached_tokens"] || usage["cache_read_tokens"])

    tokens =
      numeric(
        usage["tokens"] ||
          usage["total_tokens"] ||
          input_tokens + output_tokens + cached_tokens
      )

    %{
      "billing_basis" => "provider_token_usage",
      "tokens" => tokens,
      "input_tokens" => input_tokens,
      "output_tokens" => output_tokens,
      "cached_tokens" => cached_tokens,
      "usd" => numeric(usage["usd"] || usage["cost_usd"] || value(usage["cost"], :usd)),
      "wall_ms" => numeric(usage["wall_ms"])
    }
  end

  defp usage_payload(%{"payload" => %{} = payload}), do: normalize_event(payload)
  defp usage_payload(event), do: event

  defp numeric(value) when is_integer(value) or is_float(value), do: value

  defp numeric(value) when is_binary(value) do
    case Float.parse(value) do
      {number, ""} -> number
      _ -> 0
    end
  end

  defp numeric(_value), do: 0

  defp maybe(opts, _key, nil), do: opts
  defp maybe(opts, key, value), do: Keyword.put(opts, key, value)

  defp with_capability(opts, nil), do: opts

  defp with_capability(opts, %Mitos.Capability{} = capability) do
    capability
    |> Mitos.Capability.launch_fields()
    |> Enum.reduce(opts, fn {key, value}, acc -> Keyword.put(acc, key, value) end)
  end

  defp with_resolved_skill(opts, %Workload{capability: nil}, _run_opts), do: {:ok, opts}

  defp with_resolved_skill(opts, %Workload{} = workload, run_opts) do
    case resolve_workload_skill(workload, run_opts) do
      {:ok, nil} -> {:ok, opts}
      {:ok, resolved_skill} -> {:ok, Keyword.put(opts, :resolved_skill, resolved_skill)}
      {:error, reason} -> {:error, reason}
    end
  end

  defp resolve_workload_skill(
         %Workload{capability: %Mitos.Capability{skill: skill}} = workload,
         opts
       )
       when is_binary(skill) and skill != "" do
    with {:ok, path} <- skill_source_path(workload, skill),
         {:ok, base_skill} <- read_skill_source(skill, path),
         {:ok, resolved} <- resolve_skill_body(workload, opts, skill, base_skill) do
      {:ok,
       resolved
       |> Map.put("path", "/skill/resolved.md")
       |> Map.put("source", skill)}
    end
  end

  defp resolve_workload_skill(_workload, _opts), do: {:ok, nil}

  defp skill_source_path(%Workload{} = workload, skill) do
    case package_file(workload.package_root, skill) do
      {:ok, path} -> {:ok, path}
      {:error, reason} -> {:error, {:skill_unavailable, skill, reason}}
    end
  end

  defp read_skill_source(skill, path) do
    case read_regular_file(path) do
      {:ok, body} -> {:ok, body}
      {:error, reason} -> {:error, {:skill_unavailable, skill, reason}}
    end
  end

  defp resolve_skill_body(workload, opts, skill, base_skill) do
    case Mitos.Capability.resolve_skill(base_skill, selected_skill_overlays(workload, opts)) do
      {:ok, resolved} -> {:ok, resolved}
      {:error, reason} -> {:error, {:skill_resolution_failed, skill, reason}}
    end
  end

  defp package_file(package_root, path) when is_binary(package_root) and is_binary(path) do
    expanded =
      if Path.type(path) == :absolute do
        Path.expand(path)
      else
        Path.expand(path, package_root)
      end

    package_root = Path.expand(package_root)

    cond do
      not (String.starts_with?(expanded, package_root <> "/") or expanded == package_root) ->
        {:error, :path_escape}

      true ->
        case no_symlink_path?(expanded, package_root) do
          true -> {:ok, expanded}
          false -> {:error, :symlink}
          {:error, reason} -> {:error, reason}
        end
    end
  end

  defp package_file(_package_root, _path), do: {:error, :missing_package_root}

  defp no_symlink_path?(path, root) do
    root = Path.expand(root)
    path = Path.expand(path)
    parts = if path == root, do: [], else: Path.split(Path.relative_to(path, root))

    with {:ok, %{type: root_type}} <- File.lstat(root),
         true <- root_type != :symlink do
      parts
      |> Enum.reduce_while(root, fn part, current ->
        candidate = Path.join(current, part)

        case File.lstat(candidate) do
          {:ok, %{type: :symlink}} -> {:halt, false}
          {:ok, _stat} -> {:cont, candidate}
          {:error, reason} -> {:halt, {:error, reason}}
        end
      end)
      |> case do
        false -> false
        {:error, reason} -> {:error, reason}
        _last_path -> true
      end
    else
      false -> false
      {:error, reason} -> {:error, reason}
    end
  end

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, %{type: :symlink}} -> {:error, :symlink}
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
    end
  end

  defp selected_skill_overlays(
         %Workload{capability: %Mitos.Capability{} = capability} = workload,
         opts
       ) do
    overlays = capability.skill_adapt || %{}

    opts
    |> skill_overlay_candidates(workload)
    |> Enum.find_value(fn key -> Map.get(overlays, key) end)
    |> List.wrap()
  end

  defp skill_overlay_candidates(opts, %Workload{} = workload) do
    model = Keyword.get(opts, :model)

    [model, model_family(model), workload.provider, "default"]
    |> Enum.filter(&is_binary/1)
    |> Enum.reject(&(&1 == ""))
    |> Enum.uniq()
  end

  defp model_family(nil), do: nil

  defp model_family(model) when is_binary(model) do
    model
    |> String.split(["-", ":"], parts: 2)
    |> List.first()
  end

  defp model_family(_model), do: nil

  defp with_launch_posture(%{trust_tier: trust_tier} = launch_spec) do
    Map.put_new(
      launch_spec,
      :posture,
      Mitos.SandboxPolicy.launch_posture(trust_tier, launch_spec)
    )
  end

  defp with_launch_posture(launch_spec), do: launch_spec

  defp maybe_put_string(map, _key, nil), do: map
  defp maybe_put_string(map, key, value), do: Map.put(map, key, value)

  defp resolved_skill_effect(%{} = resolved_skill) do
    Map.take(resolved_skill, ["path", "source", "sha256"])
  end

  defp resolved_skill_effect(_resolved_skill), do: nil
end
