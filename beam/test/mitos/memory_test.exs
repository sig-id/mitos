defmodule Mitos.MemoryTest do
  use ExUnit.Case, async: true

  test "folds Track A capability stats from canonical effects" do
    state =
      [
        started("run-a", "tenant-a", "cap:demo", "fake", "gpt-test", 0),
        iteration("run-a", "tenant-a", 1),
        charged("run-a", "tenant-a", 2, 1.25, 100),
        verification("run-a", "tenant-a", 3, "smart", "pass", "0.75"),
        finished("run-a", "tenant-a", "ship", 4),
        started("run-b", "tenant-a", "cap:demo", "fake", "gpt-test", 5),
        charged("run-b", "tenant-a", 6, 0.75, 50),
        verification("run-b", "tenant-a", 7, "smart", "unverified", 0.25),
        finished("run-b", "tenant-a", "fail", 8),
        started("run-c", "tenant-b", "cap:demo", "fake", "gpt-test", 9),
        charged("run-c", "tenant-b", 10, 9.0, 900),
        finished("run-c", "tenant-b", "ship", 11)
      ]
      |> Mitos.Memory.fold_events()

    assert %{
             "runs" => 2,
             "pass_rate" => 0.5,
             "fail_rate" => 0.5,
             "avg_iterations" => 0.5,
             "wall_ms" => %{"p50" => 1000},
             "usd" => %{"avg" => 1.0},
             "tokens" => %{"avg" => 75.0},
             "verifier" => %{
               "avg_score" => 0.5,
               "tier_pass_rate" => %{"smart" => 0.5}
             },
             "source_runs" => ["run-a", "run-b"]
           } = state["stats"]["tenant-a|cap:demo|gpt-test|fake"]

    assert %{
             "tasks_analyzed" => 2,
             "success_rate" => 0.5,
             "avg_cost_usdc" => 1.0,
             "avg_tokens" => 75.0,
             "source_runs" => ["run-a", "run-b"]
           } = state["global_stats"]["tenant-a"]

    assert %{
             "tasks_analyzed" => 1,
             "success_rate" => 1.0,
             "avg_cost_usdc" => 9.0,
             "source_runs" => ["run-c"]
           } = state["global_stats"]["tenant-b"]
  end

  test "writes observations with contradiction supersession and tenant-filtered retrieval" do
    base_dir = unique_tmp_dir("memory")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-memory", "tenant-a")

    trace =
      Mitos.Trace.append_effect(trace, %{
        type: "capability_finished",
        caused_by: 0,
        ref: %{"capability_ref" => "cap:demo"},
        payload: %{"status" => "completed", "result" => %{"signal" => "ship"}}
      })

    trace =
      Mitos.Memory.append_observation(
        trace,
        %{
          scope: %{tenant_id: "tenant-a", capability_ref: "cap:demo", model: "gpt-test"},
          kind: "decision",
          title: "Use library X",
          body: "Library X worked for this task.",
          claim: %{subject: "library-x", assertion: "use"},
          evidence: ["run-memory"],
          confidence: 0.6
        },
        caused_by: 0
      )

    _trace =
      Mitos.Memory.append_observation(
        trace,
        %{
          scope: %{tenant_id: "tenant-a", capability_ref: "cap:demo", model: "gpt-test"},
          kind: "failure_mode",
          title: "Avoid library X",
          body: "Library X breaks the buyer data format.",
          claim: %{subject: "library-x", assertion: "avoid"},
          evidence: ["run-memory"],
          confidence: 0.9
        },
        caused_by: 0
      )

    state =
      "run-memory" |> Mitos.Trace.read_events(base_dir: base_dir) |> Mitos.Memory.fold_events()

    observations = Map.values(state["observations"])

    assert [%{"status" => "active", "title" => "Avoid library X"}] =
             Enum.filter(observations, &(&1["status"] == "active"))

    assert [%{"status" => "superseded", "title" => "Use library X"}] =
             Enum.filter(observations, &(&1["status"] == "superseded"))

    assert [%{"reason" => "contradiction", "tenant_id" => "tenant-a"}] = state["supersessions"]

    assert [%{"title" => "Avoid library X"}] =
             Mitos.Memory.retrieve(state, "library",
               tenant_id: "tenant-a",
               scope: %{capability_ref: "cap:demo"}
             )

    assert [] = Mitos.Memory.retrieve(state, "library", tenant_id: "tenant-b")
  end

  test "privacy delete tombstones observations and redacts replay context" do
    base_dir = unique_tmp_dir("memory-privacy-delete")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-memory-delete", "tenant-a")

    trace =
      Mitos.Memory.append_observation(
        trace,
        %{
          id: "obs-private",
          scope: %{tenant_id: "tenant-a", capability_ref: "cap:demo", model: "gpt-test"},
          kind: "gotcha",
          title: "Private buyer detail",
          body: "Personal buyer context that must be removed.",
          claim: %{subject: "buyer-detail", assertion: "private"},
          evidence: ["run-memory-delete"],
          embedding: [0.1, 0.2],
          confidence: 0.7
        },
        caused_by: 0
      )

    _trace =
      Mitos.Memory.append_privacy_delete(trace, "obs-private",
        tenant_id: "tenant-a",
        caused_by: 0
      )

    state =
      "run-memory-delete"
      |> Mitos.Trace.read_events(base_dir: base_dir)
      |> Mitos.Memory.fold_events()

    tombstone = state["observations"]["obs-private"]

    assert tombstone["status"] == "tombstoned"
    assert tombstone["body"] == ""
    refute Map.has_key?(tombstone, "embedding")

    assert %{
             "reason" => "privacy_delete",
             "purged" => ["body", "embedding"]
           } = tombstone["redaction"]

    assert [
             %{
               "tenant_id" => "tenant-a",
               "superseded_id" => "obs-private",
               "superseded_by" => nil,
               "reason" => "privacy_delete",
               "effect_seq" => effect_seq
             }
           ] = state["supersessions"]

    assert is_integer(effect_seq)

    assert [] =
             Mitos.Memory.retrieve(state, "buyer",
               tenant_id: "tenant-a",
               scope: %{capability_ref: "cap:demo"}
             )

    slice =
      Mitos.Memory.context_slice(state, "buyer",
        tenant_id: "tenant-a",
        scope: %{capability_ref: "cap:demo"}
      )

    assert slice["body"] =~ "No tenant memory selected."
    refute slice["body"] =~ "Personal buyer context"
  end

  test "rejects privacy delete across tenant boundaries" do
    base_dir = unique_tmp_dir("memory-privacy-delete-tenant")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-memory-delete-tenant", "tenant-a")

    trace =
      Mitos.Memory.append_observation(
        trace,
        %{
          id: "obs-private",
          scope: %{tenant_id: "tenant-a", capability_ref: "cap:demo"},
          kind: "gotcha",
          title: "Tenant-owned detail",
          body: "Only tenant-a may delete this observation.",
          claim: %{subject: "tenant-detail", assertion: "private"},
          evidence: ["run-memory-delete-tenant"],
          confidence: 0.8
        },
        caused_by: 0
      )

    assert_raise ArgumentError, ~r/another tenant/, fn ->
      Mitos.Memory.append_privacy_delete(trace, "obs-private",
        tenant_id: "tenant-b",
        caused_by: 0
      )
    end
  end

  test "renders tenant-filtered knowledge context slices" do
    state = %{
      "stats" => %{},
      "supersessions" => [],
      "observations" => %{
        "obs-a" =>
          Mitos.Memory.observation(%{
            id: "obs-a",
            scope: %{tenant_id: "tenant-a", capability_ref: "cap:demo", model: "gpt-test"},
            kind: "fix_strategy",
            title: "Prefer small batches",
            body: "Small batches reduced retries for this capability.",
            claim: %{subject: "batch-size", assertion: "small"},
            evidence: ["run-a"],
            confidence: 0.8
          }),
        "obs-b" =>
          Mitos.Memory.observation(%{
            id: "obs-b",
            scope: %{tenant_id: "tenant-b", capability_ref: "cap:demo", model: "gpt-test"},
            kind: "gotcha",
            title: "Other tenant note",
            body: "This must never be injected for tenant-a.",
            claim: %{subject: "tenant", assertion: "other"},
            evidence: ["run-b"],
            confidence: 1.0
          })
      }
    }

    slice =
      Mitos.Memory.context_slice(state, "batches",
        tenant_id: "tenant-a",
        scope: %{capability_ref: "cap:demo", model: "gpt-test"},
        k: 5
      )

    assert slice["format"] == "KNOWLEDGE.md"
    assert [%{"title" => "Prefer small batches"}] = slice["entries"]
    assert slice["body"] =~ "Prefer small batches"
    refute slice["body"] =~ "Other tenant note"
  end

  test "retrieval down-weights observations attached to low pass-rate stats" do
    state = %{
      "stats" => %{
        "tenant-a|cap:bad|gpt-test|fake" => %{
          "scope" => %{
            "tenant_id" => "tenant-a",
            "capability_ref" => "cap:bad",
            "model" => "gpt-test",
            "harness" => "fake"
          },
          "pass_rate" => 0.0
        },
        "tenant-a|cap:good|gpt-test|fake" => %{
          "scope" => %{
            "tenant_id" => "tenant-a",
            "capability_ref" => "cap:good",
            "model" => "gpt-test",
            "harness" => "fake"
          },
          "pass_rate" => 1.0
        }
      },
      "supersessions" => [],
      "observations" => %{
        "obs-bad" =>
          Mitos.Memory.observation(%{
            id: "obs-bad",
            scope: %{tenant_id: "tenant-a", capability_ref: "cap:bad", model: "gpt-test"},
            kind: "fix_strategy",
            title: "Use retry strategy",
            body: "Retry strategy reduced one failure.",
            claim: %{subject: "retry", assertion: "use"},
            evidence: ["run-bad"],
            confidence: 0.5
          }),
        "obs-good" =>
          Mitos.Memory.observation(%{
            id: "obs-good",
            scope: %{tenant_id: "tenant-a", capability_ref: "cap:good", model: "gpt-test"},
            kind: "fix_strategy",
            title: "Use retry strategy",
            body: "Retry strategy consistently shipped.",
            claim: %{subject: "retry", assertion: "use"},
            evidence: ["run-good"],
            confidence: 0.5
          })
      }
    }

    assert [%{"id" => "obs-good"}, %{"id" => "obs-bad"}] =
             Mitos.Memory.retrieve(state, "strategy",
               tenant_id: "tenant-a",
               scope: %{model: "gpt-test"},
               k: 2
             )
  end

  test "rejects observations whose evidence references another tenant" do
    base_dir = unique_tmp_dir("memory-cross-tenant")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-memory-cross", "tenant-a")

    other_tenant_events = [
      finished("run-other", "tenant-b", "ship", 1)
    ]

    assert_raise ArgumentError, ~r/another tenant/, fn ->
      Mitos.Memory.append_observation(
        trace,
        %{
          scope: %{tenant_id: "tenant-a"},
          title: "Bad evidence",
          body: "This cites another tenant.",
          claim: %{subject: "evidence", assertion: "invalid"},
          evidence: ["run-other"]
        },
        caused_by: 0,
        events: other_tenant_events
      )
    end
  end

  test "rejects observations whose evidence references another tenant event sequence" do
    base_dir = unique_tmp_dir("memory-cross-tenant-event")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace = opened_trace(base_dir, "run-memory-cross-event", "tenant-a")

    other_tenant_events = [
      finished("run-other", "tenant-b", "ship", 7)
    ]

    assert_raise ArgumentError, ~r/another tenant/, fn ->
      Mitos.Memory.append_observation(
        trace,
        %{
          scope: %{tenant_id: "tenant-a"},
          title: "Bad event evidence",
          body: "This cites another tenant by event sequence.",
          claim: %{subject: "event-evidence", assertion: "invalid"},
          evidence: [%{event_seq: 7}]
        },
        caused_by: 0,
        events: other_tenant_events
      )
    end
  end

  defp opened_trace(base_dir, run_id, tenant_id) do
    trace =
      Mitos.Trace.open!(
        run_id: run_id,
        base_dir: base_dir,
        org_id: tenant_id,
        launch_spec: %{"provider" => "memory", "task" => "fold"}
      )

    Mitos.Trace.append_decision(trace, %{
      actor: "orchestrator",
      type: "advance",
      target: "memory",
      params: %{},
      inputs_hash: "sha256:state",
      rationale: "write memory",
      budget: %{"tokens" => 0, "usd" => 0, "wall_ms" => 0, "remaining" => %{}}
    })
  end

  defp started(run_id, tenant_id, capability_ref, harness, model, seq) do
    effect(run_id, tenant_id, seq, "capability_started", %{"capability_ref" => capability_ref}, %{
      "harness" => harness,
      "model" => model
    })
  end

  defp iteration(run_id, tenant_id, seq) do
    effect(run_id, tenant_id, seq, "iteration_finished", %{}, %{})
  end

  defp charged(run_id, tenant_id, seq, usd, tokens) do
    effect(run_id, tenant_id, seq, "budget_charged", %{}, %{
      "usd" => usd,
      "tokens" => tokens,
      "wall_ms" => 1000
    })
  end

  defp verification(run_id, tenant_id, seq, tier, verdict, score) do
    effect(run_id, tenant_id, seq, "verification_recorded", %{}, %{
      "tier" => tier,
      "verdict" => verdict,
      "score" => score
    })
  end

  defp finished(run_id, tenant_id, signal, seq) do
    effect(run_id, tenant_id, seq, "capability_finished", %{"capability_ref" => "cap:demo"}, %{
      "status" => if(signal == "fail", do: "failed", else: "completed"),
      "result" => %{"signal" => signal}
    })
  end

  defp effect(run_id, tenant_id, seq, type, ref, payload) do
    %{
      "kind" => "effect",
      "run_id" => run_id,
      "tenant" => %{"org_id" => tenant_id, "project_id" => "default", "actor_id" => "system"},
      "seq" => seq,
      "ts" => "2026-06-22T12:00:0#{rem(seq, 10)}Z",
      "type" => type,
      "caused_by" => 0,
      "ref" => ref,
      "payload" => payload
    }
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
