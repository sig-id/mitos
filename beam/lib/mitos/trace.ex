defmodule Mitos.Trace do
  @moduledoc """
  File-backed run trace owned by the BEAM control plane.

  The engine owns high-volume byte spools and sends references upward. This
  module records the decoded protocol event stream and manifest under
  `.runs/<run_id>/`, matching the product artifact names used by the Zig trace
  layer.
  """

  require Logger

  defstruct [
    :run_id,
    :dir,
    :org_id,
    :project_id,
    :actor_id,
    seq: 0
  ]

  @artifact_files ~w(
    events.jsonl
    terminal.snapshots.jsonl
    sandbox.events.jsonl
    resources.jsonl
    commands.jsonl
    approvals.jsonl
    test_results.jsonl
  )

  @doc """
  Base directory under which `.runs/` lives. Resolution order: app config
  `:trace_base_dir`, `MITOS_TRACE_DIR`, then the current working directory.
  Shared by the trace writer and the read side (`Mitos.Runs`).
  """
  def base_dir do
    Application.get_env(:mitos, :trace_base_dir) ||
      System.get_env("MITOS_TRACE_DIR") ||
      File.cwd!()
  end

  @doc "Open or initialize a trace directory for a run."
  def open!(opts) do
    run_id = Keyword.fetch!(opts, :run_id)
    base_dir = Keyword.get_lazy(opts, :base_dir, &base_dir/0)
    dir = Path.join([base_dir, ".runs", run_id])

    File.mkdir_p!(dir)
    launch_spec = Keyword.get(opts, :launch_spec, %{})
    trace_context = trace_context(opts, launch_spec, dir)
    write_manifest_once!(dir, Keyword.put(opts, :trace_context, trace_context))

    for file <- @artifact_files do
      ensure_file!(Path.join(dir, file))
    end

    File.mkdir_p!(Path.join(dir, "screenshots"))
    File.mkdir_p!(Path.join(dir, "artifacts"))

    %__MODULE__{
      run_id: run_id,
      dir: dir,
      org_id: trace_context.org_id,
      project_id: trace_context.project_id,
      actor_id: trace_context.actor_id,
      seq: next_sequence(dir)
    }
  end

  def append_event(%__MODULE__{} = trace, event) when is_map(event) do
    record = %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "tenant" => tenant_record(trace),
      "sequence" => trace.seq,
      "timestamp_ms" => System.system_time(:millisecond),
      "event" => normalize(event)
    }

    append_jsonl!(trace, "events.jsonl", record)
    maybe_append_sidecar!(trace, record)
    %{trace | seq: trace.seq + 1}
  rescue
    exception ->
      Logger.warning(
        "failed to append trace event for #{inspect(trace.run_id)}: #{Exception.message(exception)}"
      )

      trace
  end

  @decision_types ~w(advance insert retry switch skip request_approval escalate finish abort)
  @decision_actors ~w(orchestrator loop_manager)

  @doc """
  Append a canonical DecisionEvent (`01_ARCHITECTURE` §8.1).

  The decision is durable before any substrate effect runs. `append_effect/2`
  links subsequent EffectEvents back to this decision through `caused_by`.
  """
  def append_decision(%__MODULE__{} = trace, attrs) when is_map(attrs) do
    case append_decision_checked(trace, attrs) do
      {:ok, trace, _decision} -> trace
      {:abort, trace, _decision} -> trace
    end
  end

  @doc """
  Append a DecisionEvent, refusing over-budget grants with `abort{cause:budget}`.

  Returns `{:ok, trace, decision}` for accepted decisions and
  `{:abort, trace, decision}` when the requested budget grant exceeds its
  declared `budget.remaining`. Callers that would execute work for the decision
  should use this variant so they can stop before appending decision effects.
  """
  def append_decision_checked(%__MODULE__{} = trace, attrs) when is_map(attrs) do
    record = %{
      "schema_version" => 1,
      "kind" => "decision",
      "run_id" => trace.run_id,
      "tenant" => tenant_record(trace),
      "seq" => trace.seq,
      "ts" => iso8601_now(),
      "actor" => required_string!(attrs, "actor"),
      "type" => required_string!(attrs, "type"),
      "target" => normalize(Map.get(attrs, "target", Map.get(attrs, :target))),
      "params" => normalize(Map.get(attrs, "params", Map.get(attrs, :params, %{}))),
      "inputs_hash" => required_string!(attrs, "inputs_hash"),
      "rationale" => required_string!(attrs, "rationale"),
      "budget" => normalize(Map.get(attrs, "budget", Map.get(attrs, :budget, %{})))
    }

    unless record["actor"] in @decision_actors do
      raise ArgumentError, "invalid decision actor #{inspect(record["actor"])}"
    end

    unless record["type"] in @decision_types do
      raise ArgumentError, "invalid decision type #{inspect(record["type"])}"
    end

    case budget_violation(record) do
      nil ->
        append_jsonl!(trace, "events.jsonl", record)
        {:ok, %{trace | seq: trace.seq + 1}, record}

      violation ->
        abort = budget_abort_decision(record, violation)
        append_jsonl!(trace, "events.jsonl", abort)
        {:abort, %{trace | seq: trace.seq + 1}, abort}
    end
  end

  @doc "Append a canonical EffectEvent (`01_ARCHITECTURE` §8.2)."
  def append_effect(%__MODULE__{} = trace, attrs) when is_map(attrs) do
    record = %{
      "schema_version" => 1,
      "kind" => "effect",
      "run_id" => trace.run_id,
      "tenant" => tenant_record(trace),
      "seq" => trace.seq,
      "ts" => iso8601_now(),
      "type" => required_string!(attrs, "type"),
      "caused_by" => required_integer!(attrs, "caused_by"),
      "ref" => normalize(Map.get(attrs, "ref", Map.get(attrs, :ref, %{}))),
      "payload" => normalize(Map.get(attrs, "payload", Map.get(attrs, :payload, %{})))
    }

    if duplicate_effect?(trace, record) do
      trace
    else
      case effect_budget_violation(trace, record) do
        nil ->
          :ok

        violation ->
          raise ArgumentError,
                "budget charge would exceed decision budget: #{inspect(violation)}"
      end

      append_jsonl!(trace, "events.jsonl", record)
      %{trace | seq: trace.seq + 1}
    end
  end

  @doc "Read decoded events for a run from the trace store."
  def read_events(run_id, opts \\ []) when is_binary(run_id) do
    base_dir = Keyword.get_lazy(opts, :base_dir, &base_dir/0)
    path = Path.join([base_dir, ".runs", run_id, "events.jsonl"])

    case File.read(path) do
      {:ok, raw} ->
        raw
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          case Jason.decode(line) do
            {:ok, %{} = event} -> [event]
            _ -> []
          end
        end)

      {:error, _} ->
        []
    end
  end

  @doc """
  Fold canonical Decision/Effect events into deterministic replay state.

  Legacy sidecar protocol events are ignored by this fold; they remain debug
  trace records until the substrate emits canonical EffectEvents for them.
  """
  def fold_events(events) when is_list(events) do
    canonical =
      events
      |> Enum.filter(&(Map.get(&1, "kind") in ["decision", "effect"]))
      |> Enum.sort_by(&Map.get(&1, "seq", -1))

    decisions = Enum.filter(canonical, &(Map.get(&1, "kind") == "decision"))
    effects = Enum.filter(canonical, &(Map.get(&1, "kind") == "effect"))

    effects_by_decision =
      effects
      |> Enum.group_by(&Map.get(&1, "caused_by"))

    last_committed_effect_seq =
      effects
      |> Enum.map(&Map.get(&1, "seq"))
      |> Enum.filter(&is_integer/1)
      |> Enum.max(fn -> -1 end)

    state_events =
      Enum.filter(canonical, fn event ->
        seq = Map.get(event, "seq")

        is_integer(seq) and
          (seq <= last_committed_effect_seq or terminal_decision?(event))
      end)

    state_decisions = Enum.filter(state_events, &(Map.get(&1, "kind") == "decision"))
    state_effects = Enum.filter(state_events, &(Map.get(&1, "kind") == "effect"))

    state_effects_by_decision =
      state_effects
      |> Enum.group_by(&Map.get(&1, "caused_by"))

    pending_decisions =
      decisions
      |> Enum.filter(fn decision ->
        seq = Map.get(decision, "seq")

        is_integer(seq) and seq > last_committed_effect_seq and not terminal_decision?(decision) and
          not Map.has_key?(effects_by_decision, seq)
      end)

    incomplete_egress_attempts = incomplete_egress_attempts(effects)

    %{
      "run_id" => canonical |> List.first(%{}) |> Map.get("run_id"),
      "last_seq" =>
        canonical
        |> Enum.map(&Map.get(&1, "seq"))
        |> Enum.filter(&is_integer/1)
        |> Enum.max(fn -> -1 end),
      "last_committed_effect_seq" => last_committed_effect_seq,
      "decisions" => state_decisions,
      "effects" => state_effects,
      "effects_by_decision" => state_effects_by_decision,
      "all_decisions" => decisions,
      "all_effects" => effects,
      "pending_decisions" => pending_decisions,
      "incomplete_egress_attempts" => incomplete_egress_attempts,
      "egress_replay_requires_approval?" => incomplete_egress_attempts != [],
      "budget" => budget_state(state_decisions, state_effects),
      "terminal?" => terminal?(state_decisions, state_effects)
    }
  end

  @doc "Read and fold one run's canonical log."
  def fold_run(run_id, opts \\ []) when is_binary(run_id) do
    run_id
    |> read_events(opts)
    |> fold_events()
  end

  defp incomplete_egress_attempts(effects) do
    completed =
      effects
      |> Enum.filter(&(Map.get(&1, "type") in ["egress_completed", "egress_denied"]))
      |> Enum.flat_map(fn effect ->
        case egress_crossing_key(effect) do
          nil -> []
          key -> [key]
        end
      end)
      |> MapSet.new()

    effects
    |> Enum.filter(&(Map.get(&1, "type") == "egress_attempted"))
    |> Enum.filter(fn attempt ->
      case egress_crossing_key(attempt) do
        nil -> true
        key -> not MapSet.member?(completed, key)
      end
    end)
  end

  defp egress_crossing_key(%{"caused_by" => caused_by, "ref" => ref}) when is_map(ref) do
    dest = ref["dest"] || ref["destination"] || ref["url"] || ref["host"]
    nonce = ref["nonce"] || ref["request_id"] || ref["id"]

    if dest do
      [caused_by, dest, nonce]
    end
  end

  defp egress_crossing_key(_event), do: nil

  @doc """
  Recover one run by re-driving logged decisions that have no committed Effect.

  The callback receives each pending DecisionEvent and returns `:noop`,
  `{:ok, effect_attrs}`, `{:ok, [effect_attrs]}`, or `{:error, reason}`. Returned
  effects are appended with `caused_by` set to the pending decision sequence, so
  recovery never re-decides and never loses causality.

  If the folded log contains an `egress_attempted` Effect without a matching
  `egress_completed`/`egress_denied`, recovery fails closed with
  `{:error, {:egress_replay_requires_approval, attempts}}`. Pass
  `allow_incomplete_egress_replay: true` only after that repeat has been approved.
  """
  def recover_run(run_id, redrive_fun) when is_binary(run_id) and is_function(redrive_fun, 1),
    do: recover_run(run_id, [], redrive_fun)

  def recover_run(run_id, opts, redrive_fun)
      when is_binary(run_id) and is_list(opts) and is_function(redrive_fun, 1) do
    if run_dir_exists?(run_id, opts) do
      state = fold_run(run_id, opts)

      with {:ok, pending} <- recoverable_decisions(state, opts) do
        trace =
          open!([run_id: run_id] |> maybe_put_option(:base_dir, Keyword.get(opts, :base_dir)))

        case redrive_pending(trace, pending, redrive_fun, []) do
          {:ok, trace, effects} ->
            {:ok,
             %{
               run_id: run_id,
               pending_decisions: pending,
               redriven_effects: Enum.reverse(effects),
               state: fold_run(run_id, opts),
               trace: trace
             }}

          {:error, reason} ->
            {:error, reason}
        end
      end
    else
      {:error, :not_found}
    end
  end

  def append_snapshot(%__MODULE__{} = trace, snapshot) when is_map(snapshot) do
    snapshot = normalize(snapshot)

    append_jsonl!(trace, "terminal.snapshots.jsonl", %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "snapshot" => snapshot
    })

    append_event(trace, %{
      "type" => "SNAPSHOT",
      "seq" => Map.get(snapshot, "seq"),
      "artifact" => %{"kind" => "terminal_snapshots", "path" => "terminal.snapshots.jsonl"}
    })
  rescue
    exception ->
      Logger.warning(
        "failed to append trace snapshot for #{inspect(trace.run_id)}: #{Exception.message(exception)}"
      )

      trace
  end

  def append_command(%__MODULE__{} = trace, event) when is_map(event) do
    record = %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "event" => normalize(event)
    }

    append_jsonl!(trace, "commands.jsonl", record)
    trace
  rescue
    exception ->
      Logger.warning(
        "failed to append command trace for #{inspect(trace.run_id)}: #{Exception.message(exception)}"
      )

      trace
  end

  defp write_manifest_once!(dir, opts) do
    path = Path.join(dir, "run.yaml")
    if File.exists?(path), do: :ok, else: write_manifest!(path, opts)
  end

  defp redrive_pending(trace, [], _redrive_fun, effects), do: {:ok, trace, effects}

  defp redrive_pending(trace, [decision | rest], redrive_fun, effects) do
    with {:ok, recovered_effects} <- normalize_recovery_result(redrive_fun.(decision)) do
      decision_seq = Map.fetch!(decision, "seq")

      trace =
        Enum.reduce(recovered_effects, trace, fn effect, trace ->
          effect =
            effect
            |> Map.delete("caused_by")
            |> Map.put(:caused_by, decision_seq)

          append_effect(trace, effect)
        end)

      redrive_pending(trace, rest, redrive_fun, Enum.reverse(recovered_effects) ++ effects)
    end
  end

  defp normalize_recovery_result(:noop), do: {:ok, []}
  defp normalize_recovery_result({:ok, nil}), do: {:ok, []}
  defp normalize_recovery_result({:ok, %{} = effect}), do: {:ok, [effect]}

  defp normalize_recovery_result({:ok, effects}) when is_list(effects) do
    if Enum.all?(effects, &is_map/1) do
      {:ok, effects}
    else
      {:error, {:invalid_recovery_effects, effects}}
    end
  end

  defp normalize_recovery_result({:error, reason}), do: {:error, reason}

  defp normalize_recovery_result(other), do: {:error, {:invalid_recovery_result, other}}

  defp recoverable_decisions(state, opts) do
    case Map.get(state, "incomplete_egress_attempts", []) do
      [] ->
        {:ok, Map.get(state, "pending_decisions", [])}

      attempts ->
        if Keyword.get(opts, :allow_incomplete_egress_replay, false) do
          pending =
            state
            |> Map.get("pending_decisions", [])
            |> Kernel.++(egress_replay_decisions(state, attempts))
            |> Enum.uniq_by(&Map.get(&1, "seq"))
            |> Enum.sort_by(&Map.get(&1, "seq", -1))

          {:ok, pending}
        else
          {:error, {:egress_replay_requires_approval, attempts}}
        end
    end
  end

  defp egress_replay_decisions(state, attempts) do
    decisions_by_seq =
      state
      |> Map.get("all_decisions", [])
      |> Map.new(fn decision -> {Map.get(decision, "seq"), decision} end)

    attempts
    |> Enum.map(&Map.get(&1, "caused_by"))
    |> Enum.filter(&is_integer/1)
    |> Enum.uniq()
    |> Enum.flat_map(fn seq ->
      case Map.fetch(decisions_by_seq, seq) do
        {:ok, decision} -> [decision]
        :error -> []
      end
    end)
  end

  defp run_dir_exists?(run_id, opts) do
    opts
    |> Keyword.get_lazy(:base_dir, &base_dir/0)
    |> Path.join(".runs")
    |> Path.join(run_id)
    |> File.dir?()
  end

  defp maybe_put_option(opts, _key, nil), do: opts
  defp maybe_put_option(opts, key, value), do: Keyword.put(opts, key, value)

  defp write_manifest!(path, opts) do
    provider =
      opts
      |> Keyword.get(:launch_spec, %{})
      |> provider_from_launch_spec()

    task =
      opts
      |> Keyword.get(:launch_spec, %{})
      |> task_from_launch_spec()

    launch_spec = Keyword.get(opts, :launch_spec, %{})
    trace_context = Keyword.fetch!(opts, :trace_context)

    manifest =
      [
        "schema_version: 1",
        "run_id: #{Keyword.fetch!(opts, :run_id)}",
        "created_at_ms: #{System.system_time(:millisecond)}",
        "org_id: #{trace_context.org_id}",
        "project_id: #{trace_context.project_id}",
        "actor_id: #{trace_context.actor_id}",
        "provider: #{provider}",
        "task: #{task}"
      ]
      |> maybe_manifest_line("capability_ref", launch_spec_value(launch_spec, "capability_ref"))
      |> maybe_manifest_line("trust_tier", launch_spec_value(launch_spec, "trust_tier"))
      |> Kernel.++([""])

    File.write!(path, Enum.join(manifest, "\n"))
  end

  defp ensure_file!(path) do
    unless File.exists?(path), do: File.write!(path, "")
  end

  defp trace_context(opts, launch_spec, dir) do
    manifest = read_manifest_values(Path.join(dir, "run.yaml"))

    %{
      org_id: context_value(opts, launch_spec, manifest, :org_id, "default"),
      project_id: context_value(opts, launch_spec, manifest, :project_id, "default"),
      actor_id: context_value(opts, launch_spec, manifest, :actor_id, "system")
    }
  end

  defp context_value(opts, launch_spec, manifest, key, default) do
    key_string = Atom.to_string(key)

    value =
      Keyword.get(opts, key) ||
        launch_spec_value(launch_spec, key_string) ||
        Map.get(manifest, key_string) ||
        default

    normalize_context_value(value, default)
  end

  defp normalize_context_value(value, _default) when is_binary(value) and value != "", do: value
  defp normalize_context_value(value, _default) when is_atom(value), do: Atom.to_string(value)

  defp normalize_context_value(value, _default) when is_integer(value),
    do: Integer.to_string(value)

  defp normalize_context_value(_value, default), do: default

  defp read_manifest_values(path) do
    case File.read(path) do
      {:ok, raw} ->
        raw
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          case String.split(line, ": ", parts: 2) do
            [key, value] -> [{key, value}]
            _ -> []
          end
        end)
        |> Map.new()

      {:error, _reason} ->
        %{}
    end
  end

  defp tenant_record(%__MODULE__{} = trace) do
    %{
      "org_id" => trace.org_id || "default",
      "project_id" => trace.project_id || "default",
      "actor_id" => trace.actor_id || "system"
    }
  end

  defp next_sequence(dir) do
    dir
    |> Path.join("events.jsonl")
    |> read_sequence_values()
    |> Enum.max(fn -> -1 end)
    |> Kernel.+(1)
  end

  defp read_sequence_values(path) do
    case File.read(path) do
      {:ok, raw} ->
        raw
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          with {:ok, %{} = record} <- Jason.decode(line),
               seq when is_integer(seq) <- Map.get(record, "seq", Map.get(record, "sequence")) do
            [seq]
          else
            _ -> []
          end
        end)

      {:error, _} ->
        []
    end
  end

  defp provider_from_launch_spec(%{"provider" => provider}), do: provider
  defp provider_from_launch_spec(%{provider: provider}), do: provider
  defp provider_from_launch_spec(_), do: "raw-command"

  defp task_from_launch_spec(%{"task" => task}), do: task
  defp task_from_launch_spec(%{task: task}), do: task
  defp task_from_launch_spec(_), do: ""

  defp launch_spec_value(%{} = launch_spec, key) do
    Map.get(launch_spec, key, Map.get(launch_spec, String.to_atom(key)))
  end

  defp launch_spec_value(_launch_spec, _key), do: nil

  defp maybe_manifest_line(lines, _key, nil), do: lines
  defp maybe_manifest_line(lines, _key, ""), do: lines
  defp maybe_manifest_line(lines, key, value), do: lines ++ ["#{key}: #{value}"]

  defp append_jsonl!(trace, file, record) do
    line = Jason.encode!(record)
    File.write!(Path.join(trace.dir, file), line <> "\n", [:append])
  end

  defp maybe_append_sidecar!(trace, %{"event" => %{"type" => type}} = record)
       when type in ["PROCESS_SPAWNED", "PROCESS_EXITED"] do
    append_jsonl!(trace, "commands.jsonl", record)
  end

  defp maybe_append_sidecar!(trace, %{"event" => %{"type" => "ACTION_REQUESTED"} = event}) do
    append_jsonl!(trace, "approvals.jsonl", %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "type" => "approval_requested",
      "approval_id" => event["request_id"] || event["action_id"],
      "origin" => event["origin"] || event["source"],
      "method" => event["method"],
      "action" => event["action"],
      "risk" => event["risk"],
      "context" => event["context"]
    })
  end

  defp maybe_append_sidecar!(trace, %{"event" => %{"type" => "AUTHORITY_VERDICT"} = event}) do
    append_jsonl!(trace, "approvals.jsonl", %{
      "schema_version" => 1,
      "run_id" => trace.run_id,
      "timestamp_ms" => System.system_time(:millisecond),
      "type" => "approval_decision",
      "approval_id" => event["request_id"] || event["action_id"],
      "origin" => event["origin"],
      "method" => event["method"],
      "decision" => event["decision"],
      "reason" => event["reason"],
      "payload" => event["payload"]
    })
  end

  defp maybe_append_sidecar!(_trace, _record), do: :ok

  defp iso8601_now do
    DateTime.utc_now()
    |> DateTime.truncate(:millisecond)
    |> DateTime.to_iso8601()
  end

  defp required_string!(attrs, key) do
    value = Map.get(attrs, key, Map.get(attrs, String.to_atom(key)))

    if is_binary(value) and value != "" do
      value
    else
      raise ArgumentError, "#{key} must be a non-empty string"
    end
  end

  defp required_integer!(attrs, key) do
    value = Map.get(attrs, key, Map.get(attrs, String.to_atom(key)))

    if is_integer(value) do
      value
    else
      raise ArgumentError, "#{key} must be an integer"
    end
  end

  defp budget_violation(%{"type" => "abort"}), do: nil

  defp budget_violation(%{"budget" => %{"remaining" => %{} = remaining} = budget}) do
    violations =
      ["tokens", "usd", "wall_ms"]
      |> Enum.flat_map(fn key ->
        with requested when is_number(requested) <- numeric_budget_value(Map.get(budget, key, 0)),
             allowed when is_number(allowed) <- numeric_budget_value(Map.get(remaining, key)),
             true <- requested > allowed do
          [%{"dimension" => key, "requested" => requested, "remaining" => allowed}]
        else
          _ -> []
        end
      end)

    case violations do
      [] -> nil
      _ -> %{"violations" => violations, "remaining" => remaining}
    end
  end

  defp budget_violation(_record), do: nil

  defp effect_budget_violation(%__MODULE__{} = trace, %{
         "type" => "budget_charged",
         "caused_by" => caused_by,
         "payload" => %{} = payload
       }) do
    events = read_trace_events(trace)

    with %{} = decision <- decision_by_seq(events, caused_by),
         %{} = budget <- Map.get(decision, "budget") do
      violations =
        ["tokens", "usd", "wall_ms"]
        |> Enum.flat_map(fn dimension ->
          with limit when is_number(limit) <- budget_limit(budget, dimension),
               attempted when attempted > 0 <- charge_dimension(payload, dimension) do
            already = charged_so_far(events, caused_by, dimension)
            total = already + attempted

            if total > limit + 1.0e-9 do
              [
                %{
                  "dimension" => dimension,
                  "limit" => limit,
                  "already_charged" => already,
                  "attempted" => attempted,
                  "total" => total
                }
              ]
            else
              []
            end
          else
            _ -> []
          end
        end)

      case violations do
        [] -> nil
        _ -> %{"caused_by" => caused_by, "violations" => violations}
      end
    else
      _ -> nil
    end
  end

  defp effect_budget_violation(_trace, _record), do: nil

  defp duplicate_effect?(%__MODULE__{} = trace, %{"kind" => "effect"} = record) do
    record_key = effect_idempotency_key(record)

    is_list(record_key) and
      Enum.any?(read_trace_events(trace), fn
        %{"kind" => "effect"} = event -> effect_idempotency_key(event) == record_key
        _event -> false
      end)
  end

  defp duplicate_effect?(_trace, _record), do: false

  defp effect_idempotency_key(%{
         "type" => "capability_started",
         "caused_by" => caused_by
       }),
       do: ["capability_started", caused_by]

  defp effect_idempotency_key(%{"type" => type, "caused_by" => caused_by, "ref" => ref})
       when type in ["egress_attempted", "egress_allowed", "egress_completed", "egress_denied"] and
              is_map(ref) do
    dest = ref["dest"] || ref["destination"] || ref["url"] || ref["host"]
    nonce = ref["nonce"] || ref["request_id"] || ref["id"]

    if dest do
      [type, caused_by, dest, nonce]
    end
  end

  defp effect_idempotency_key(
         %{"type" => "artifact_registered", "caused_by" => caused_by} = event
       ) do
    ref = Map.get(event, "ref", %{})
    artifact_id = ref["artifact_id"] || ref["artifact"] || ref["sha256"] || ref["path"]

    if artifact_id do
      ["artifact_registered", caused_by, artifact_id]
    end
  end

  defp effect_idempotency_key(%{"type" => "commit", "caused_by" => caused_by, "ref" => ref})
       when is_map(ref) do
    sha = ref["sha"] || ref["commit"] || ref["commit_sha"]
    if sha, do: ["commit", caused_by, sha]
  end

  defp effect_idempotency_key(%{"type" => "budget_charged", "caused_by" => caused_by}),
    do: ["budget_charged", caused_by]

  defp effect_idempotency_key(%{"type" => "signal_received", "ref" => ref})
       when is_map(ref) do
    connector = ref["connector"]
    key = ref["idempotency_key"] || ref["delivery_id"] || ref["signal_id"]
    if connector && key, do: ["signal_received", connector, key]
  end

  defp effect_idempotency_key(%{"type" => type, "ref" => ref})
       when type in ["delivery_attempted", "delivery_sent", "delivery_failed"] and is_map(ref) do
    sink = ref["sink"]
    dest = ref["dest"] || ref["destination"] || ref["url"] || ref["delivery_id"]
    if sink && dest, do: [type, sink, dest]
  end

  defp effect_idempotency_key(_event), do: nil

  defp budget_state(decisions, effects) do
    charges = Enum.filter(effects, &(Map.get(&1, "type") == "budget_charged"))

    charged_by_decision =
      charges
      |> Enum.group_by(&Map.get(&1, "caused_by"))
      |> Map.new(fn {decision_seq, charges} -> {decision_seq, charge_totals(charges)} end)

    current_decision = List.last(decisions)
    current_seq = current_decision && current_decision["seq"]
    current_budget = if current_decision, do: current_decision["budget"] || %{}, else: %{}
    current_spent = Map.get(charged_by_decision, current_seq, zero_budget())

    %{
      "current" => current_budget,
      "current_spent" => current_spent,
      "charged" => charge_totals(charges),
      "charged_by_decision" => charged_by_decision,
      "remaining" => remaining_after_charge(current_budget, current_spent)
    }
  end

  defp charge_totals(charges) do
    Enum.reduce(charges, zero_budget(), fn effect, totals ->
      payload = Map.get(effect, "payload", %{})

      %{
        "tokens" => totals["tokens"] + charge_dimension(payload, "tokens"),
        "usd" => totals["usd"] + charge_dimension(payload, "usd"),
        "wall_ms" => totals["wall_ms"] + charge_dimension(payload, "wall_ms")
      }
    end)
  end

  defp zero_budget, do: %{"tokens" => 0, "usd" => 0, "wall_ms" => 0}

  defp remaining_after_charge(%{} = budget, %{} = spent) do
    ["tokens", "usd", "wall_ms"]
    |> Map.new(fn dimension ->
      base = budget_dimension_base(budget, dimension) || 0

      {dimension, base - Map.get(spent, dimension, 0)}
    end)
  end

  defp remaining_after_charge(_budget, _spent), do: zero_budget()

  defp budget_dimension_base(%{"remaining" => %{} = remaining} = budget, dimension) do
    numeric_budget_value(Map.get(remaining, dimension)) ||
      numeric_budget_value(Map.get(budget, dimension))
  end

  defp budget_dimension_base(%{} = budget, dimension),
    do: numeric_budget_value(Map.get(budget, dimension))

  defp read_trace_events(%__MODULE__{} = trace) do
    path = Path.join(trace.dir, "events.jsonl")

    case File.read(path) do
      {:ok, raw} ->
        raw
        |> String.split("\n", trim: true)
        |> Enum.flat_map(fn line ->
          case Jason.decode(line) do
            {:ok, %{} = event} -> [event]
            _ -> []
          end
        end)

      {:error, _reason} ->
        []
    end
  end

  defp decision_by_seq(events, seq) do
    Enum.find(events, fn
      %{"kind" => "decision", "seq" => ^seq} -> true
      _event -> false
    end)
  end

  defp budget_limit(%{"remaining" => %{} = remaining} = budget, dimension) do
    case numeric_budget_value(Map.get(remaining, dimension)) do
      value when is_number(value) -> value
      nil -> positive_budget_value(Map.get(budget, dimension))
    end
  end

  defp budget_limit(%{} = budget, dimension),
    do: positive_budget_value(Map.get(budget, dimension))

  defp budget_limit(_budget, _dimension), do: nil

  defp positive_budget_value(value) do
    case numeric_budget_value(value) do
      number when is_number(number) and number > 0 -> number
      _ -> nil
    end
  end

  defp charged_so_far(events, caused_by, dimension) do
    events
    |> Enum.filter(fn
      %{"kind" => "effect", "type" => "budget_charged", "caused_by" => ^caused_by} -> true
      _event -> false
    end)
    |> Enum.map(fn %{"payload" => payload} -> charge_dimension(payload, dimension) end)
    |> Enum.sum()
  end

  defp charge_dimension(%{} = payload, "usd") do
    numeric_budget_value(payload["usd"]) ||
      numeric_budget_value(payload["amount_usdc"]) ||
      usd_micros_value(payload["usd_micros"]) ||
      0
  end

  defp charge_dimension(%{} = payload, "tokens"), do: numeric_budget_value(payload["tokens"]) || 0

  defp charge_dimension(%{} = payload, "wall_ms"),
    do: numeric_budget_value(payload["wall_ms"]) || 0

  defp charge_dimension(_payload, _dimension), do: 0

  defp usd_micros_value(value) when is_integer(value) or is_float(value), do: value / 1_000_000
  defp usd_micros_value(_value), do: nil

  defp budget_abort_decision(record, violation) do
    %{
      record
      | "actor" => "orchestrator",
        "type" => "abort",
        "target" => nil,
        "params" => %{
          "cause" => "budget",
          "detail" => "decision budget grant exceeds remaining budget",
          "requested" => Map.drop(record["budget"] || %{}, ["remaining"]),
          "remaining" => violation["remaining"],
          "violations" => violation["violations"],
          "refused_decision" => %{
            "actor" => record["actor"],
            "type" => record["type"],
            "target" => record["target"],
            "params" => record["params"]
          }
        },
        "rationale" => "Refused over-budget decision grant"
    }
  end

  defp numeric_budget_value(value) when is_integer(value) or is_float(value), do: value

  defp numeric_budget_value(value) when is_binary(value) do
    case Float.parse(value) do
      {number, ""} -> number
      _ -> nil
    end
  end

  defp numeric_budget_value(_value), do: nil

  defp terminal?(decisions, effects) do
    Enum.any?(decisions, &(Map.get(&1, "type") in ["finish", "abort"])) or
      Enum.any?(effects, &(Map.get(&1, "type") == "capability_finished"))
  end

  defp terminal_decision?(%{"kind" => "decision", "type" => type})
       when type in ["finish", "abort"],
       do: true

  defp terminal_decision?(_event), do: false

  defp normalize(%Msgpax.Bin{data: data}), do: normalize(data)

  defp normalize(%_{} = value), do: inspect(value)

  defp normalize(value) when is_map(value) do
    Map.new(value, fn {key, val} -> {to_string(key), normalize(val)} end)
  end

  defp normalize(value) when is_list(value), do: Enum.map(value, &normalize/1)

  defp normalize(value) when is_binary(value) do
    if String.valid?(value) do
      value
    else
      %{"encoding" => "base64", "data" => Base.encode64(value)}
    end
  end

  defp normalize(value) when is_boolean(value) or is_nil(value), do: value
  defp normalize(value) when is_atom(value), do: to_string(value)
  defp normalize(value), do: value
end
