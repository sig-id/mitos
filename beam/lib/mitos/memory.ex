defmodule Mitos.Memory do
  @moduledoc """
  Tenant-scoped memory projection.

  Track A stats are derived from canonical Effect events. Track B observations
  are written as `memory_written` Effects and folded with write-time
  contradiction supersession. Retrieval always filters tenant first.
  """

  alias Mitos.Trace

  @observation_kinds ~w(pattern failure_mode fix_strategy decision gotcha escalation)
  @default_scope %{"tenant_id" => "default"}

  @doc "Build a canonical Track-B observation map."
  def observation(attrs) when is_map(attrs) do
    attrs = stringify(attrs)
    scope = Map.merge(@default_scope, map_or(attrs["scope"], %{}))
    evidence = list_or(attrs["evidence"])

    obs =
      %{
        "id" => string_or(attrs["id"], observation_id(attrs)),
        "scope" => scope,
        "kind" => kind_or(attrs["kind"], "pattern"),
        "title" => required_string!(attrs, "title"),
        "body" => string_or(attrs["body"], ""),
        "claim" => map_or(attrs["claim"], %{}),
        "evidence" => evidence,
        "embedding" => list_or(attrs["embedding"]),
        "confidence" => float_or(attrs["confidence"], 0.5),
        "status" => status_or(attrs["status"], "active"),
        "supersedes" => list_or(attrs["supersedes"]),
        "version" => int_or(attrs["version"], 1),
        "proposed_by" => string_or(attrs["proposed_by"], "memory-analyst"),
        "decay" => map_or(attrs["decay"], %{"use_count" => 0}),
        "created_at" => attrs["created_at"],
        "updated_at" => attrs["updated_at"]
      }

    validate_observation!(obs, [])
    obs
  end

  @doc """
  Append a `memory_written` Effect for one observation.

  Existing events are folded first so same-tenant contradictory observations can
  supersede active prior observations at write time.
  """
  def append_observation(%Trace{} = trace, attrs, opts) when is_map(attrs) and is_list(opts) do
    existing_events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    obs = observation(attrs)
    validate_observation!(obs, existing_events)

    state = fold_events(existing_events)
    supersessions = supersessions_for(obs, state)
    obs = Map.put(obs, "supersedes", Enum.map(supersessions, & &1["superseded_id"]))

    Trace.append_effect(trace, %{
      type: "memory_written",
      caused_by: Keyword.fetch!(opts, :caused_by),
      ref: %{
        "obs_id" => obs["id"],
        "op" => "write",
        "supersedes" => obs["supersedes"]
      },
      payload: %{
        "track" => "observation",
        "observation" => obs,
        "supersessions" => supersessions
      }
    })
  end

  @doc """
  Append privacy-delete tombstones for one or more observations in the same tenant.

  The observation remains auditable as a tombstoned record, but its body and
  embedding are removed from the folded projection and it leaves retrieval.
  """
  def append_privacy_delete(trace, obs_id_or_ids, opts)

  def append_privacy_delete(%Trace{} = trace, obs_id, opts)
      when is_binary(obs_id) and is_list(opts) do
    append_privacy_delete(trace, [obs_id], opts)
  end

  def append_privacy_delete(%Trace{} = trace, obs_ids, opts)
      when is_list(obs_ids) and is_list(opts) do
    tenant_id = opts |> Keyword.fetch!(:tenant_id) |> to_string()
    existing_events = Keyword.get_lazy(opts, :events, fn -> read_trace_events(trace) end)
    state = fold_events(existing_events)

    observations =
      obs_ids
      |> Enum.map(&privacy_delete_obs_id!/1)
      |> Enum.uniq()
      |> case do
        [] -> raise ArgumentError, "privacy delete requires at least one observation id"
        ids -> Enum.map(ids, &privacy_delete_observation!(state, &1, tenant_id))
      end

    supersessions =
      Enum.map(observations, &privacy_delete_supersession(&1, tenant_id, opts))

    redaction = %{
      "reason" => "privacy_delete",
      "tenant_id" => tenant_id,
      "obs_ids" => Enum.map(observations, & &1["id"]),
      "purged" => ["body", "embedding"]
    }

    Trace.append_effect(trace, %{
      type: "memory_written",
      caused_by: Keyword.fetch!(opts, :caused_by),
      ref: privacy_delete_ref(redaction["obs_ids"]),
      payload: %{
        "track" => "observation",
        "supersessions" => supersessions,
        "redaction" => redaction
      }
    })
  end

  @doc "Fold decoded trace events into memory stats and observations."
  def fold_events(events) when is_list(events) do
    effects =
      events
      |> Enum.flat_map(&canonical_effect/1)
      |> Enum.sort_by(&Map.get(&1, "seq", -1))

    samples = stat_samples(effects)

    %{
      "stats" => stats(samples),
      "global_stats" => global_stats(samples),
      "observations" => observations(effects),
      "supersessions" => supersession_records(effects)
    }
  end

  @doc "Read one run's events and fold the memory projection."
  def fold_run(run_id, opts \\ []) when is_binary(run_id) do
    run_id
    |> Trace.read_events(opts)
    |> fold_events()
  end

  @doc "Retrieve active observations scoped to one tenant."
  def retrieve(state, query, opts) when is_map(state) and is_list(opts) do
    tenant_id = Keyword.fetch!(opts, :tenant_id)
    k = Keyword.get(opts, :k, 5)
    query = to_string(query || "")
    scope = stringify(Keyword.get(opts, :scope, %{}))
    stats = Map.get(state, "stats", %{})

    state
    |> Map.get("observations", %{})
    |> Map.values()
    |> Enum.filter(&(get_in(&1, ["scope", "tenant_id"]) == tenant_id))
    |> Enum.filter(&(&1["status"] == "active"))
    |> Enum.map(&{score_observation(&1, query, scope, stats), &1})
    |> Enum.sort_by(fn {score, obs} -> {-score, obs["id"]} end)
    |> Enum.take(k)
    |> Enum.map(fn {_score, obs} -> obs end)
  end

  @doc "Render a tenant-filtered read-only knowledge slice for inbound context injection."
  def context_slice(state, query, opts) when is_map(state) and is_list(opts) do
    tenant_id = Keyword.fetch!(opts, :tenant_id)
    scope = stringify(Keyword.get(opts, :scope, %{}))
    max_chars = Keyword.get(opts, :max_chars, 8_000)

    entries =
      state
      |> retrieve(query, tenant_id: tenant_id, scope: scope, k: Keyword.get(opts, :k, 5))
      |> Enum.map(&context_entry/1)

    body = render_context_body(entries, max_chars)

    %{
      "kind" => "knowledge",
      "format" => "KNOWLEDGE.md",
      "tenant_id" => tenant_id,
      "scope" => scope,
      "query" => to_string(query || ""),
      "entries" => entries,
      "body" => body
    }
  end

  defp stat_samples(effects) do
    effects
    |> Enum.group_by(&Map.get(&1, "run_id"))
    |> Enum.flat_map(fn {_run_id, run_effects} -> stat_sample(run_effects) end)
  end

  defp stats(samples) do
    samples
    |> Enum.group_by(&scope_key/1)
    |> Map.new(fn {scope_key, samples} ->
      {scope_key, reduce_samples(samples)}
    end)
  end

  defp global_stats(samples) do
    samples
    |> Enum.group_by(&get_in(&1, ["scope", "tenant_id"]))
    |> Map.new(fn {tenant_id, samples} ->
      {tenant_id, reduce_global_samples(tenant_id, samples)}
    end)
  end

  defp stat_sample(run_effects) do
    started = Enum.find(run_effects, &(Map.get(&1, "type") == "capability_started"))

    finished =
      run_effects
      |> Enum.reverse()
      |> Enum.find(&(Map.get(&1, "type") == "capability_finished"))

    if started && finished do
      budget = budget_totals(run_effects)
      verifier = verifier_totals(run_effects)

      [
        %{
          "scope" => %{
            "tenant_id" => tenant_id(started),
            "capability_ref" => capability_ref(started) || capability_ref(finished),
            "model" => value(payload(started), "model"),
            "harness" => value(payload(started), "harness") || value(ref(started), "provider")
          },
          "run_id" => started["run_id"],
          "passed?" => pass?(finished),
          "iterations" => Enum.count(run_effects, &(Map.get(&1, "type") == "iteration_finished")),
          "wall_ms" => numeric(budget["wall_ms"]),
          "usd" => numeric(budget["usd"]),
          "tokens" => numeric(budget["tokens"]),
          "verifier_scores" => verifier["scores"],
          "verifier_tier_results" => verifier["tier_results"],
          "first_seen" => started["ts"],
          "updated_at" => finished["ts"]
        }
      ]
    else
      []
    end
  end

  defp reduce_samples(samples) do
    runs = length(samples)
    pass_count = Enum.count(samples, & &1["passed?"])
    fail_count = runs - pass_count

    %{
      "scope" => samples |> hd() |> Map.fetch!("scope"),
      "runs" => runs,
      "pass_rate" => ratio(pass_count, runs),
      "fix_rate" => ratio(pass_count, runs),
      "fail_rate" => ratio(fail_count, runs),
      "avg_iterations" => average(samples, "iterations"),
      "wall_ms" => %{
        "avg" => average(samples, "wall_ms"),
        "p50" => percentile(samples, "wall_ms", 0.5),
        "p95" => percentile(samples, "wall_ms", 0.95)
      },
      "usd" => %{"avg" => average(samples, "usd"), "p95" => percentile(samples, "usd", 0.95)},
      "tokens" => %{
        "avg" => average(samples, "tokens"),
        "p95" => percentile(samples, "tokens", 0.95)
      },
      "verifier" => %{
        "avg_score" => average_values(Enum.flat_map(samples, & &1["verifier_scores"])),
        "tier_pass_rate" => tier_pass_rates(Enum.flat_map(samples, & &1["verifier_tier_results"]))
      },
      "first_seen" =>
        samples
        |> Enum.map(& &1["first_seen"])
        |> Enum.reject(&is_nil/1)
        |> Enum.min(fn -> nil end),
      "updated_at" =>
        samples
        |> Enum.map(& &1["updated_at"])
        |> Enum.reject(&is_nil/1)
        |> Enum.max(fn -> nil end),
      "source_runs" => Enum.map(samples, & &1["run_id"])
    }
  end

  defp reduce_global_samples(tenant_id, samples) do
    runs = length(samples)
    pass_count = Enum.count(samples, & &1["passed?"])

    %{
      "scope" => %{"tenant_id" => tenant_id},
      "tasks_analyzed" => runs,
      "runs" => runs,
      "success_rate" => ratio(pass_count, runs),
      "avg_cost_usdc" => average(samples, "usd"),
      "avg_tokens" => average(samples, "tokens"),
      "avg_wall_ms" => average(samples, "wall_ms"),
      "first_seen" =>
        samples
        |> Enum.map(& &1["first_seen"])
        |> Enum.reject(&is_nil/1)
        |> Enum.min(fn -> nil end),
      "updated_at" =>
        samples
        |> Enum.map(& &1["updated_at"])
        |> Enum.reject(&is_nil/1)
        |> Enum.max(fn -> nil end),
      "source_runs" => Enum.map(samples, & &1["run_id"])
    }
  end

  defp observations(effects) do
    Enum.reduce(effects, %{}, fn effect, observations ->
      observations = apply_supersessions(effect, observations)

      case memory_observation(effect) do
        nil -> observations
        obs -> Map.put(observations, obs["id"], obs)
      end
    end)
    |> Enum.reject(fn {_id, obs} -> is_nil(obs) end)
    |> Map.new()
  end

  defp apply_supersessions(effect, observations) do
    effect
    |> supersession_records()
    |> Enum.reduce(observations, fn supersession, observations ->
      case supersession["superseded_id"] do
        obs_id when is_binary(obs_id) and obs_id != "" ->
          update_in(observations, [obs_id], fn
            nil -> nil
            old -> apply_supersession(old, supersession)
          end)

        _other ->
          observations
      end
    end)
  end

  defp verifier_totals(effects) do
    effects
    |> Enum.filter(&(Map.get(&1, "type") == "verification_recorded"))
    |> Enum.reduce(%{"scores" => [], "tier_results" => []}, fn effect, acc ->
      tier = verifier_tier(effect)

      acc
      |> maybe_add_verifier_score(verifier_score(effect))
      |> update_in(
        ["tier_results"],
        &[%{"tier" => tier, "passed?" => verifier_pass?(effect)} | &1]
      )
    end)
    |> update_in(["scores"], &Enum.reverse/1)
    |> update_in(["tier_results"], &Enum.reverse/1)
  end

  defp maybe_add_verifier_score(acc, nil), do: acc
  defp maybe_add_verifier_score(acc, score), do: update_in(acc, ["scores"], &[score | &1])

  defp verifier_tier(effect) do
    value(payload(effect), "tier") ||
      value(ref(effect), "tier") ||
      "unknown"
  end

  defp verifier_score(effect) do
    payload = payload(effect)

    [
      value(payload, "score"),
      value(payload, "avg_score"),
      value(payload, "evidence_score"),
      payload |> value("judgment", %{}) |> value("score")
    ]
    |> Enum.find_value(&numeric_or_nil/1)
  end

  defp verifier_pass?(effect) do
    payload = payload(effect)

    case value(payload, "verified") do
      value when is_boolean(value) ->
        value

      _other ->
        payload
        |> value("verdict")
        |> to_string()
        |> String.downcase()
        |> Kernel.in(~w(pass passed verified ok accepted approved success succeeded skip skipped))
    end
  end

  defp apply_supersession(old, %{"reason" => "privacy_delete"} = supersession) do
    old
    |> Map.put("status", "tombstoned")
    |> Map.put("body", "")
    |> Map.delete("embedding")
    |> Map.put("redaction", %{
      "reason" => "privacy_delete",
      "supersession_id" => supersession["id"],
      "effect_seq" => supersession["effect_seq"],
      "ts" => supersession["ts"],
      "purged" => ["body", "embedding"]
    })
  end

  defp apply_supersession(old, _supersession), do: Map.put(old, "status", "superseded")

  defp memory_observation(%{"type" => "memory_written"} = effect) do
    case payload(effect)["observation"] do
      %{} = obs ->
        obs
        |> observation()
        |> Map.put("created_at", value(obs, "created_at") || effect["ts"])
        |> Map.put("updated_at", effect["ts"])

      _other ->
        nil
    end
  end

  defp memory_observation(_effect), do: nil

  defp supersession_records(effects) when is_list(effects) do
    effects
    |> Enum.flat_map(&supersession_records/1)
  end

  defp supersession_records(%{"type" => "memory_written"} = effect) do
    effect
    |> payload()
    |> value("supersessions", [])
    |> List.wrap()
    |> Enum.filter(&is_map/1)
    |> Enum.map(fn supersession ->
      supersession
      |> stringify()
      |> Map.put_new("effect_seq", effect["seq"])
      |> Map.put_new("ts", effect["ts"])
    end)
  end

  defp supersession_records(_effect), do: []

  defp supersessions_for(obs, state) do
    state
    |> Map.get("observations", %{})
    |> Map.values()
    |> Enum.filter(&contradicts?(obs, &1))
    |> Enum.map(fn old ->
      %{
        "id" => supersession_id(old["id"], obs["id"]),
        "tenant_id" => get_in(obs, ["scope", "tenant_id"]),
        "superseded_id" => old["id"],
        "superseded_by" => obs["id"],
        "reason" => "contradiction",
        "similarity" => 1.0,
        "contradiction" => %{
          "subject" => get_in(obs, ["claim", "subject"]),
          "old_assertion" => get_in(old, ["claim", "assertion"]),
          "new_assertion" => get_in(obs, ["claim", "assertion"])
        },
        "decided_by" => "validator"
      }
    end)
  end

  defp privacy_delete_obs_id!(obs_id) when is_binary(obs_id) and obs_id != "", do: obs_id
  defp privacy_delete_obs_id!(_obs_id), do: raise(ArgumentError, "observation id is required")

  defp privacy_delete_observation!(state, obs_id, tenant_id) do
    case get_in(state, ["observations", obs_id]) do
      nil ->
        raise ArgumentError, "privacy delete observation #{obs_id} was not found"

      %{} = obs ->
        if get_in(obs, ["scope", "tenant_id"]) == tenant_id do
          obs
        else
          raise ArgumentError, "privacy delete observation #{obs_id} belongs to another tenant"
        end
    end
  end

  defp privacy_delete_supersession(obs, tenant_id, opts) do
    %{
      "id" => supersession_id(obs["id"], "privacy_delete"),
      "tenant_id" => tenant_id,
      "superseded_id" => obs["id"],
      "superseded_by" => nil,
      "reason" => "privacy_delete",
      "similarity" => 1.0,
      "contradiction" => nil,
      "decided_by" => string_or(Keyword.get(opts, :decided_by), "privacy-delete")
    }
  end

  defp privacy_delete_ref([obs_id]) do
    %{
      "obs_id" => obs_id,
      "op" => "privacy_delete",
      "supersedes" => [obs_id]
    }
  end

  defp privacy_delete_ref(obs_ids) do
    %{
      "obs_ids" => obs_ids,
      "op" => "privacy_delete",
      "supersedes" => obs_ids
    }
  end

  defp contradicts?(new, old) do
    get_in(new, ["scope", "tenant_id"]) == get_in(old, ["scope", "tenant_id"]) and
      old["status"] == "active" and
      same_subject?(new, old) and
      get_in(new, ["claim", "assertion"]) not in [nil, get_in(old, ["claim", "assertion"])]
  end

  defp same_subject?(new, old) do
    subject = get_in(new, ["claim", "subject"])
    is_binary(subject) and subject != "" and subject == get_in(old, ["claim", "subject"])
  end

  defp validate_observation!(obs, events) do
    cond do
      get_in(obs, ["scope", "tenant_id"]) in [nil, ""] ->
        raise ArgumentError, "observation scope.tenant_id is required"

      obs["evidence"] == [] ->
        raise ArgumentError, "observation evidence must not be empty"

      cross_tenant_evidence?(obs, events) ->
        raise ArgumentError, "observation evidence references another tenant"

      true ->
        :ok
    end
  end

  defp cross_tenant_evidence?(_obs, []), do: false

  defp cross_tenant_evidence?(obs, events) do
    tenant_id = get_in(obs, ["scope", "tenant_id"])
    effect_tenants = effect_tenants(events)

    obs
    |> Map.get("evidence", [])
    |> Enum.any?(&evidence_cross_tenant?(&1, effect_tenants, tenant_id))
  end

  defp effect_tenants(events) do
    effects = Enum.flat_map(events, &canonical_effect/1)

    %{
      runs: Map.new(effects, fn effect -> {effect["run_id"], tenant_id(effect)} end),
      event_refs:
        effects
        |> Enum.flat_map(fn effect ->
          with run_id when is_binary(run_id) <- effect["run_id"],
               seq when not is_nil(seq) <- seq_key(effect["seq"]) do
            [{{run_id, seq}, tenant_id(effect)}]
          else
            _other -> []
          end
        end)
        |> Map.new(),
      seqs:
        Enum.reduce(effects, %{}, fn effect, acc ->
          case seq_key(effect["seq"]) do
            nil ->
              acc

            seq ->
              Map.update(
                acc,
                seq,
                MapSet.new([tenant_id(effect)]),
                &MapSet.put(&1, tenant_id(effect))
              )
          end
        end)
    }
  end

  defp evidence_cross_tenant?(evidence, effect_tenants, tenant_id) do
    run_cross_tenant?(evidence_run_id(evidence), effect_tenants.runs, tenant_id) or
      Enum.any?(evidence_event_refs(evidence), fn ref ->
        event_cross_tenant?(ref, effect_tenants, tenant_id)
      end)
  end

  defp run_cross_tenant?(run_id, run_tenants, tenant_id) when is_binary(run_id) do
    Map.has_key?(run_tenants, run_id) and run_tenants[run_id] != tenant_id
  end

  defp run_cross_tenant?(_run_id, _run_tenants, _tenant_id), do: false

  defp evidence_run_id(%{"run_id" => run_id}), do: run_id
  defp evidence_run_id(%{"event" => %{"run_id" => run_id}}), do: run_id
  defp evidence_run_id(run_id) when is_binary(run_id), do: run_id
  defp evidence_run_id(_evidence), do: nil

  defp evidence_event_refs(%{"event_seq" => %{"run_id" => run_id, "seq" => seq}}),
    do: [{:run_seq, run_id, seq_key(seq)}]

  defp evidence_event_refs(%{"event_seq" => seq, "run_id" => run_id}),
    do: [{:run_seq, run_id, seq_key(seq)}]

  defp evidence_event_refs(%{"event" => %{"run_id" => run_id, "seq" => seq}}),
    do: [{:run_seq, run_id, seq_key(seq)}]

  defp evidence_event_refs(%{"run_id" => run_id, "seq" => seq}),
    do: [{:run_seq, run_id, seq_key(seq)}]

  defp evidence_event_refs(%{"event_seq" => seq}), do: [{:seq, seq_key(seq)}]
  defp evidence_event_refs(%{"seq" => seq}), do: [{:seq, seq_key(seq)}]
  defp evidence_event_refs(_evidence), do: []

  defp event_cross_tenant?({:run_seq, run_id, seq}, effect_tenants, tenant_id)
       when is_binary(run_id) and not is_nil(seq) do
    case Map.fetch(effect_tenants.event_refs, {run_id, seq}) do
      {:ok, ref_tenant_id} -> ref_tenant_id != tenant_id
      :error -> false
    end
  end

  defp event_cross_tenant?({:seq, seq}, effect_tenants, tenant_id) when not is_nil(seq) do
    effect_tenants.seqs
    |> Map.get(seq, MapSet.new())
    |> Enum.any?(&(&1 != tenant_id))
  end

  defp event_cross_tenant?(_ref, _effect_tenants, _tenant_id), do: false

  defp seq_key(seq) when is_integer(seq), do: seq

  defp seq_key(seq) when is_binary(seq) do
    case Integer.parse(seq) do
      {int, ""} -> int
      _other -> nil
    end
  end

  defp seq_key(_seq), do: nil

  defp score_observation(obs, query, scope, stats) do
    text = String.downcase(obs["title"] <> "\n" <> obs["body"])

    query_score =
      if query != "" and String.contains?(text, String.downcase(query)), do: 1.0, else: 0.0

    scope_score = scope_score(obs["scope"], scope)
    confidence = float_or(obs["confidence"], 0.5)
    use_count = get_in(obs, ["decay", "use_count"]) || 0
    performance_score = performance_score(obs["scope"], stats)

    query_score + scope_score + confidence + min(use_count, 10) / 100 + performance_score
  end

  defp context_entry(obs) do
    %{
      "id" => obs["id"],
      "kind" => obs["kind"],
      "title" => obs["title"],
      "body" => obs["body"],
      "claim" => map_or(obs["claim"], %{}),
      "confidence" => obs["confidence"],
      "evidence" => list_or(obs["evidence"])
    }
  end

  defp render_context_body([], _max_chars), do: "# KNOWLEDGE\n\nNo tenant memory selected.\n"

  defp render_context_body(entries, max_chars) do
    body =
      [
        "# KNOWLEDGE\n",
        "The following read-only observations were selected by mitos memory retrieval.\n\n",
        Enum.map_join(entries, "\n", &render_context_entry/1)
      ]
      |> IO.iodata_to_binary()

    if is_integer(max_chars) and max_chars > 0 do
      String.slice(body, 0, max_chars)
    else
      body
    end
  end

  defp render_context_entry(entry) do
    claim = map_or(entry["claim"], %{})
    evidence = entry["evidence"] |> list_or() |> Enum.map_join(", ", &to_string/1)

    [
      "## ",
      entry["title"],
      "\n",
      "- kind: ",
      entry["kind"],
      "\n",
      "- confidence: ",
      to_string(entry["confidence"]),
      "\n",
      "- claim: ",
      to_string(claim["subject"] || ""),
      " => ",
      to_string(claim["assertion"] || ""),
      "\n",
      "- evidence: ",
      evidence,
      "\n\n",
      entry["body"],
      "\n"
    ]
  end

  defp scope_score(obs_scope, requested_scope) do
    ["capability_ref", "model", "harness"]
    |> Enum.count(fn key ->
      requested_scope[key] not in [nil, ""] and requested_scope[key] == obs_scope[key]
    end)
    |> Kernel.*(0.25)
  end

  defp performance_score(obs_scope, stats) do
    case stats_pass_rate(obs_scope, stats) do
      nil -> 0
      pass_rate -> numeric(pass_rate) - 0.5
    end
  end

  defp stats_pass_rate(obs_scope, stats) do
    stats
    |> Map.values()
    |> Enum.filter(fn
      %{"scope" => stat_scope} -> scope_includes?(stat_scope, obs_scope)
      _stat -> false
    end)
    |> Enum.map(& &1["pass_rate"])
    |> Enum.reject(&is_nil/1)
    |> Enum.max(fn -> nil end)
  end

  defp scope_includes?(stat_scope, obs_scope) do
    ["tenant_id", "capability_ref", "model", "harness"]
    |> Enum.all?(fn key ->
      obs_scope[key] in [nil, ""] or stat_scope[key] == obs_scope[key]
    end)
  end

  defp budget_totals(effects) do
    effects
    |> Enum.filter(&(Map.get(&1, "type") == "budget_charged"))
    |> Enum.map(&payload/1)
    |> Enum.reduce(%{"tokens" => 0, "usd" => 0.0, "wall_ms" => 0}, fn payload, acc ->
      %{
        "tokens" => acc["tokens"] + numeric(payload["tokens"]),
        "usd" => acc["usd"] + numeric(payload["usd"]),
        "wall_ms" => acc["wall_ms"] + numeric(payload["wall_ms"])
      }
    end)
  end

  defp pass?(finished) do
    payload = payload(finished)
    status = value(payload, "status")
    signal = payload |> value("result", %{}) |> value("signal")

    status not in ["failed", "interrupted"] and signal not in ["fail", "failed", "reject"]
  end

  defp scope_key(%{"scope" => scope}) do
    [
      scope["tenant_id"],
      scope["capability_ref"],
      scope["model"],
      scope["harness"]
    ]
    |> Enum.map_join("|", &to_string/1)
  end

  defp capability_ref(effect) do
    value(ref(effect), "capability_ref") ||
      value(payload(effect), "capability_ref")
  end

  defp tenant_id(effect) do
    tenant = value(effect, "tenant", %{})

    value(tenant, "tenant_id") ||
      value(tenant, "org_id") ||
      value(ref(effect), "tenant_id") ||
      value(payload(effect), "tenant_id") ||
      "default"
  end

  defp read_trace_events(%Trace{} = trace) do
    base_dir =
      trace.dir
      |> Path.dirname()
      |> Path.dirname()

    Trace.read_events(trace.run_id, base_dir: base_dir)
  end

  defp canonical_effect(%{"kind" => "effect"} = event), do: [event]
  defp canonical_effect(%{"event" => %{"kind" => "effect"} = event}), do: [event]
  defp canonical_effect(_event), do: []

  defp payload(event), do: map_or(event["payload"], %{})
  defp ref(event), do: map_or(event["ref"], %{})

  defp value(map, key, default \\ nil)

  defp value(%{} = map, key, default),
    do: Map.get(map, key, Map.get(map, to_string(key), default))

  defp value(_map, _key, default), do: default

  defp required_string!(map, key) do
    value = map[key]

    if is_binary(value) and value != "" do
      value
    else
      raise ArgumentError, "#{key} must be a non-empty string"
    end
  end

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default

  defp kind_or(value, _default) when value in @observation_kinds, do: value
  defp kind_or(value, default) when is_atom(value), do: kind_or(Atom.to_string(value), default)
  defp kind_or(_value, default), do: default

  defp status_or(value, _default) when value in ["active", "superseded", "tombstoned"], do: value

  defp status_or(value, default) when is_atom(value),
    do: status_or(Atom.to_string(value), default)

  defp status_or(_value, default), do: default

  defp int_or(value, _default) when is_integer(value), do: value
  defp int_or(_value, default), do: default

  defp float_or(value, _default) when is_float(value), do: value
  defp float_or(value, _default) when is_integer(value), do: value / 1
  defp float_or(_value, default), do: default

  defp numeric(value) when is_integer(value) or is_float(value), do: value

  defp numeric(value) when is_binary(value) do
    case Float.parse(value) do
      {number, ""} -> number
      _ -> 0
    end
  end

  defp numeric(_value), do: 0

  defp numeric_or_nil(value) when is_integer(value) or is_float(value), do: value

  defp numeric_or_nil(value) when is_binary(value) do
    case Float.parse(value) do
      {number, ""} -> number
      _ -> nil
    end
  end

  defp numeric_or_nil(_value), do: nil

  defp list_or(value) when is_list(value), do: Enum.map(value, &stringify/1)
  defp list_or(_value), do: []

  defp map_or(%{} = map, _default), do: stringify(map)
  defp map_or(_value, default), do: default

  defp ratio(_count, 0), do: 0.0
  defp ratio(count, total), do: count / total

  defp average([], _key), do: 0
  defp average(samples, key), do: Enum.sum(Enum.map(samples, &numeric(&1[key]))) / length(samples)

  defp average_values([]), do: 0
  defp average_values(values), do: Enum.sum(Enum.map(values, &numeric/1)) / length(values)

  defp tier_pass_rates([]), do: %{}

  defp tier_pass_rates(results) do
    results
    |> Enum.group_by(& &1["tier"])
    |> Map.new(fn {tier, tier_results} ->
      pass_count = Enum.count(tier_results, & &1["passed?"])
      {tier, ratio(pass_count, length(tier_results))}
    end)
  end

  defp percentile([], _key, _p), do: 0

  defp percentile(samples, key, p) do
    values = samples |> Enum.map(&numeric(&1[key])) |> Enum.sort()
    index = max(ceil(length(values) * p) - 1, 0)
    Enum.at(values, index)
  end

  defp observation_id(attrs) do
    "obs_" <>
      (:crypto.hash(:sha256, canonical_json(Map.drop(attrs, ["id"])))
       |> Base.encode16(case: :lower)
       |> String.slice(0, 24))
  end

  defp supersession_id(old_id, new_id),
    do: "sup_" <> String.slice(observation_id(%{old: old_id, new: new_id}), 4, 24)

  defp stringify(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify(value)} end)

  defp stringify(list) when is_list(list), do: Enum.map(list, &stringify/1)
  defp stringify(value), do: value

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
end
