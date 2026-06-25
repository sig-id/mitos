defmodule Mitos.SessionAuthorityTest do
  use ExUnit.Case, async: false

  @moduletag :integration

  setup_all do
    binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

    unless File.exists?(binary) do
      flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
    end

    {:ok, binary: binary}
  end

  test "socket-origin requests block, resolve, and return typed payloads", %{binary: binary} do
    session_id = "authority-session-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-authority-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        session_id: session_id,
        trace_base_dir: trace_base_dir,
        project_id: "proj-a",
        provider: "generic",
        task: "",
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "sleep 10"],
        sidecar_path: binary,
        idle_ms: 50,
        egress_policy: [
          %{
            "id" => "allow-api-example",
            "dest" => "api.example.test",
            "requires_approval" => true
          },
          %{
            "id" => "deny-blocked-example",
            "dest" => "blocked.example.test",
            "requires_approval" => true
          }
        ]
      )

    pid = run.pid
    on_exit(fn -> stop_session(session_id) end)

    %{launch_spec: %{authority: authority}} = :sys.get_state(pid)
    token = authority.token
    socket_path = authority.socket_path

    approval =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "approval",
        "token" => token,
        "context" => %{"prompt" => "continue?"}
      })

    assert approval["decision"] == "authorize"
    assert approval["payload"] == %{"approved_by" => "test"}

    put =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "memory_put",
        "token" => token,
        "context" => %{"key" => "notes", "bytes" => "remember me"}
      })

    assert put["payload"] == %{"key" => "notes", "bytes" => 11}

    fetch =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "memory_fetch",
        "token" => token,
        "context" => %{"key" => "notes"}
      })

    assert fetch["payload"] == %{"key" => "notes", "found" => true, "bytes" => "remember me"}

    broker =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "tool_broker",
        "token" => token,
        "context" => %{
          "argv" => ["/bin/sh", "-c", "printf broker-ok"],
          "dest" => "api.example.test",
          "credential_id" => "seller-key-ref",
          "policy_rule" => "allow-api-example"
        }
      })

    assert broker["payload"]["stdout"] == "broker-ok"
    assert broker["payload"]["exit_status"] == 0

    denied =
      denied_request(pid, session_id, socket_path, %{
        "method" => "tool_broker",
        "token" => token,
        "context" => %{
          "argv" => ["/bin/sh", "-c", "printf should-not-run"],
          "dest" => "blocked.example.test",
          "credential_id" => "blocked-key-ref"
        }
      })

    assert denied["decision"] == "deny"
    assert denied["reason"] == "policy denied"
    assert denied["payload"] == %{}

    artifact =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "artifact_register",
        "token" => token,
        "context" => %{"kind" => "screenshot", "path" => "screenshots/one.png"}
      })

    assert artifact["payload"]["kind"] == "screenshot"
    assert artifact["payload"]["path"] == "screenshots/one.png"

    subrun =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "subrun_spawn",
        "token" => token,
        "context" => %{
          "parent_task_id" => "parent-task",
          "task" => %{
            "id" => "child-task",
            "payload" => %{"intent" => "answer parent"},
            "priority" => 7
          },
          "return_to" => %{"channel" => "authority_socket", "request_id" => "invoke-1"},
          "timeout_ms" => 1_000
        }
      })

    assert subrun["payload"]["child_task_id"] == "child-task"
    assert subrun["payload"]["parent_task_id"] == "parent-task"
    assert subrun["payload"]["pool"] == "calls"
    assert subrun["payload"]["status"] == "queued"

    subrun_pool =
      session_id
      |> Mitos.Trace.read_events(base_dir: trace_base_dir)
      |> Mitos.WorkPool.fold_events()

    assert get_in(subrun_pool, ["tasks", "parent-task", "status"]) == "parked"
    assert get_in(subrun_pool, ["tasks", "parent-task", "parked_reason"]) == "awaiting-invoke"

    subtask_return =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "subtask_return",
        "token" => token,
        "context" => %{
          "parent_task_id" => "parent-task",
          "child_task_id" => "child-task",
          "result" => %{"summary" => "subtask complete", "signal" => "ship"}
        }
      })

    assert subtask_return["payload"]["child_task_id"] == "child-task"
    assert subtask_return["payload"]["parent_task_id"] == "parent-task"
    assert subtask_return["payload"]["status"] == "returned"

    assert subtask_return["payload"]["result"] == %{
             "summary" => "subtask complete",
             "signal" => "ship"
           }

    emitted =
      authorized_request(pid, session_id, socket_path, %{
        "method" => "task_emit",
        "token" => token,
        "context" => %{
          "parent_task_id" => "parent-task",
          "pool" => "batch",
          "dedupe_key" => "follow-up:artifact-check",
          "task" => %{
            "id" => "follow-up-task",
            "payload" => %{"intent" => "check generated artifact"}
          },
          "budget_cap" => %{"tokens" => 500},
          "budget" => %{"tokens" => 100}
        }
      })

    assert emitted["payload"]["task_id"] == "follow-up-task"
    assert emitted["payload"]["parent_task_id"] == "parent-task"
    assert emitted["payload"]["pool"] == "batch"
    assert emitted["payload"]["status"] == "queued"

    emitted_pool =
      session_id
      |> Mitos.Trace.read_events(base_dir: trace_base_dir)
      |> Mitos.WorkPool.fold_events()

    assert get_in(emitted_pool, ["tasks", "follow-up-task", "status"]) == "queued"
    assert get_in(emitted_pool, ["tasks", "follow-up-task", "source"]) == "emitted:parent-task"

    run_dir = Path.join([trace_base_dir, ".runs", session_id])
    commands = File.read!(Path.join(run_dir, "commands.jsonl"))
    events = File.read!(Path.join(run_dir, "events.jsonl"))
    approvals = File.read!(Path.join(run_dir, "approvals.jsonl"))

    assert commands =~ "TOOL_BROKER_COMMAND"
    assert commands =~ "broker-ok"
    assert events =~ "ARTIFACT_REGISTERED"
    assert approvals =~ ~s("origin":"authority_socket")
    assert approvals =~ ~s("type":"approval_requested")
    assert approvals =~ ~s("type":"approval_decision")

    canonical_events =
      session_id
      |> Mitos.Trace.read_events(base_dir: trace_base_dir)
      |> Enum.filter(&(Map.get(&1, "kind") in ["decision", "effect"]))

    assert [%{"kind" => "decision", "seq" => decision_seq, "type" => "advance"} | _] =
             canonical_events

    for effect_type <- [
          "approval_requested",
          "approval_resolved",
          "egress_attempted",
          "egress_allowed",
          "egress_completed",
          "egress_denied",
          "tool_brokered",
          "memory_written",
          "artifact_registered",
          "invoke_requested",
          "task_done",
          "subtask_returned",
          "task_emitted",
          "task_enqueued"
        ] do
      assert Enum.any?(canonical_events, fn
               %{"kind" => "effect", "type" => ^effect_type, "caused_by" => ^decision_seq} ->
                 true

               _ ->
                 false
             end)
    end

    assert %{
             "seq" => attempted_seq,
             "payload" => %{
               "command" => "/bin/sh -c printf broker-ok",
               "origin" => "authority_socket"
             }
           } = find_effect(canonical_events, "egress_attempted", "api.example.test")

    assert %{
             "seq" => allowed_seq,
             "payload" => %{"status" => 0, "stdout_bytes" => stdout_bytes}
           } = find_effect(canonical_events, "egress_allowed", "api.example.test")

    assert stdout_bytes == byte_size("broker-ok")

    assert %{
             "seq" => completed_seq,
             "payload" => %{"status" => 0, "stdout_bytes" => ^stdout_bytes}
           } = find_effect(canonical_events, "egress_completed", "api.example.test")

    assert attempted_seq < allowed_seq
    assert allowed_seq < completed_seq

    assert Enum.any?(canonical_events, fn
             %{
               "kind" => "effect",
               "type" => "egress_allowed",
               "ref" => %{
                 "dest" => "api.example.test",
                 "credential_id" => "seller-key-ref",
                 "policy_rule" => "allow-api-example"
               }
             } ->
               true

             _ ->
               false
           end)

    assert Enum.any?(canonical_events, fn
             %{
               "kind" => "effect",
               "type" => "egress_denied",
               "ref" => %{
                 "dest" => "blocked.example.test",
                 "credential_id" => "blocked-key-ref"
               },
               "payload" => %{"reason" => "policy denied"}
             } ->
               true

             _ ->
               false
           end)

    pool = Mitos.WorkPool.fold_events(canonical_events)

    assert get_in(pool, ["tasks", "parent-task", "status"]) == "queued"

    assert get_in(pool, ["tasks", "parent-task", "last_subtask_result"]) == %{
             "summary" => "subtask complete",
             "signal" => "ship"
           }

    assert get_in(pool, ["tasks", "child-task", "status"]) == "done"
    assert get_in(pool, ["tasks", "child-task", "pool"]) == "calls"
    assert get_in(pool, ["tasks", "child-task", "source"]) == "invoke:parent-task"
    assert get_in(pool, ["tasks", "child-task", "priority"]) == 7

    assert get_in(pool, ["tasks", "child-task", "payload"]) == %{
             "intent" => "answer parent"
           }

    assert get_in(pool, ["invocations", "parent-task->child-task", "return_to"]) == %{
             "channel" => "authority_socket",
             "request_id" => "invoke-1"
           }

    assert get_in(pool, ["invocations", "parent-task->child-task", "status"]) == "returned"
  end

  test "tool broker rejects cwd paths containing symlinked parents", %{binary: binary} do
    session_id =
      "authority-broker-cwd-symlink-" <> Integer.to_string(System.unique_integer([:positive]))

    trace_base_dir = unique_tmp_dir("session-authority-broker-cwd-symlink-trace")
    outside = unique_tmp_dir("session-authority-broker-cwd-symlink-outside")
    link = unique_tmp_dir("session-authority-broker-cwd-symlink-link")

    File.mkdir_p!(Path.join(outside, "workspace"))
    File.ln_s!(outside, link)

    on_exit(fn ->
      stop_session(session_id)
      File.rm_rf!(trace_base_dir)
      File.rm_rf!(outside)
      File.rm(link)
    end)

    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        session_id: session_id,
        trace_base_dir: trace_base_dir,
        project_id: "proj-a",
        provider: "generic",
        task: "",
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "sleep 10"],
        sidecar_path: binary,
        idle_ms: 50,
        egress_policy: [
          %{
            "id" => "allow-api-example",
            "dest" => "api.example.test",
            "requires_approval" => true
          }
        ]
      )

    %{launch_spec: %{authority: authority}} = :sys.get_state(run.pid)
    marker = Path.join([outside, "workspace", "broker-ran.txt"])

    response =
      authorized_request(run.pid, session_id, authority.socket_path, %{
        "method" => "tool_broker",
        "token" => authority.token,
        "context" => %{
          "argv" => ["/bin/sh", "-c", "printf ran > broker-ran.txt"],
          "cwd" => Path.join(link, "workspace"),
          "dest" => "api.example.test"
        }
      })

    assert response["ok"] == false
    assert response["error"] == "request_failed"
    assert response["detail"] =~ "tool_broker cwd must not contain symlinks"
    refute File.exists?(marker)
  end

  test "subrun spawn timeout resumes parked parent with an error result", %{binary: binary} do
    session_id =
      "authority-subrun-timeout-" <> Integer.to_string(System.unique_integer([:positive]))

    trace_base_dir = unique_tmp_dir("session-authority-subrun-timeout-trace")
    on_exit(fn -> File.rm_rf!(trace_base_dir) end)
    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        session_id: session_id,
        trace_base_dir: trace_base_dir,
        project_id: "proj-a",
        provider: "generic",
        task: "",
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "sleep 10"],
        sidecar_path: binary,
        idle_ms: 50
      )

    on_exit(fn -> stop_session(session_id) end)

    %{launch_spec: %{authority: authority}} = :sys.get_state(run.pid)
    token = authority.token
    socket_path = authority.socket_path

    subrun =
      authorized_request(run.pid, session_id, socket_path, %{
        "method" => "subrun_spawn",
        "token" => token,
        "context" => %{
          "parent_task_id" => "parent-timeout",
          "task" => %{
            "id" => "child-timeout",
            "payload" => %{"intent" => "answer too slowly"}
          },
          "timeout_ms" => 25
        }
      })

    assert subrun["payload"]["status"] == "queued"

    assert_eventually(1_000, fn ->
      pool =
        session_id
        |> Mitos.Trace.read_events(base_dir: trace_base_dir)
        |> Mitos.WorkPool.fold_events()

      get_in(pool, ["invocations", "parent-timeout->child-timeout", "status"]) == "timed_out"
    end)

    events = Mitos.Trace.read_events(session_id, base_dir: trace_base_dir)
    pool = Mitos.WorkPool.fold_events(events)
    result = %{"ok" => false, "error" => "timeout", "timeout_ms" => 25}

    assert get_in(pool, ["tasks", "parent-timeout", "status"]) == "queued"
    assert get_in(pool, ["tasks", "parent-timeout", "last_subtask_result"]) == result
    assert get_in(pool, ["tasks", "child-timeout", "status"]) == "failed"
    assert get_in(pool, ["tasks", "child-timeout", "status_reason"]) == "timeout"
    assert get_in(pool, ["invocations", "parent-timeout->child-timeout", "result"]) == result
    assert Enum.any?(events, &(Map.get(&1, "type") == "subtask_timed_out"))
  end

  test "authority memory methods reject symlinked memory files", %{binary: binary} do
    session_id =
      "authority-memory-symlink-" <> Integer.to_string(System.unique_integer([:positive]))

    trace_base_dir = unique_tmp_dir("session-authority-memory-symlink-trace")
    outside = unique_tmp_dir("session-authority-memory-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(trace_base_dir)
      File.rm_rf!(outside)
    end)

    Mitos.PubSub.subscribe(session_id)

    {:ok, run} =
      Mitos.Agent.start(
        session_id: session_id,
        trace_base_dir: trace_base_dir,
        project_id: "proj-a",
        provider: "generic",
        task: "",
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "sleep 10"],
        sidecar_path: binary,
        idle_ms: 50
      )

    on_exit(fn -> stop_session(session_id) end)

    %{launch_spec: %{authority: authority}} = :sys.get_state(run.pid)
    run_dir = Path.join([trace_base_dir, ".runs", session_id])
    memory_dir = Path.join([run_dir, "artifacts", "memory", "proj-a"])
    File.mkdir_p!(memory_dir)
    File.mkdir_p!(outside)

    outside_file = Path.join(outside, "linked-memory")
    File.write!(outside_file, "outside secret")
    File.ln_s!(outside_file, Path.join(memory_dir, "linked"))

    fetch =
      authorized_request(run.pid, session_id, authority.socket_path, %{
        "method" => "memory_fetch",
        "token" => authority.token,
        "context" => %{"key" => "linked"}
      })

    assert fetch["ok"] == false
    assert fetch["error"] == "request_failed"
    assert fetch["detail"] =~ "memory file must not be a symlink"

    put =
      authorized_request(run.pid, session_id, authority.socket_path, %{
        "method" => "memory_put",
        "token" => authority.token,
        "context" => %{"key" => "linked", "bytes" => "overwritten"}
      })

    assert put["ok"] == false
    assert put["error"] == "request_failed"
    assert put["detail"] =~ "memory file must not be a symlink"
    assert File.read!(outside_file) == "outside secret"
  end

  test "egress policy auto-allows matching broker destination and denies the rest", %{
    binary: binary
  } do
    session_id = "authority-policy-" <> Integer.to_string(System.unique_integer([:positive]))
    trace_base_dir = unique_tmp_dir("session-authority-policy-trace")
    previous_vault = Application.get_env(:mitos, :key_vault)
    previous_leases = Application.get_env(:mitos, :key_vault_leases)

    Application.put_env(:mitos, :key_vault, %{
      "seller-a" => %{
        "credentials" => %{
          "seller-key-ref" => %{
            "type" => "bearer",
            "destinations" => ["api.allowed.test", "api.approved.test"],
            "secret" => "super-secret-token"
          }
        }
      },
      "other-seller-key-ref" => %{
        "seller_id" => "seller-b",
        "type" => "bearer",
        "destinations" => ["api.other-seller.test"],
        "secret" => "other-super-secret-token"
      }
    })

    on_exit(fn ->
      File.rm_rf!(trace_base_dir)

      if previous_vault do
        Application.put_env(:mitos, :key_vault, previous_vault)
      else
        Application.delete_env(:mitos, :key_vault)
      end

      if previous_leases do
        Application.put_env(:mitos, :key_vault_leases, previous_leases)
      else
        Application.delete_env(:mitos, :key_vault_leases)
      end
    end)

    {:ok, run} =
      Mitos.Agent.start(
        session_id: session_id,
        trace_base_dir: trace_base_dir,
        project_id: "proj-a",
        seller_id: "seller-a",
        provider: "generic",
        task: "",
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "sleep 10"],
        sidecar_path: binary,
        idle_ms: 50,
        egress_policy: [
          %{
            "id" => "allow-api-example",
            "dest" => "api.allowed.test",
            "credential_id" => "seller-key-ref",
            "requires_approval" => false
          },
          %{
            "id" => "missing-credential",
            "dest" => "api.missing-credential.test",
            "credential_id" => "missing-key-ref",
            "requires_approval" => false
          },
          %{
            "id" => "wrong-seller-credential",
            "dest" => "api.other-seller.test",
            "credential_id" => "other-seller-key-ref",
            "requires_approval" => false
          },
          %{
            "id" => "approve-api-example",
            "dest" => "api.approved.test",
            "credential_id" => "seller-key-ref",
            "requires_approval" => true
          }
        ]
      )

    on_exit(fn -> stop_session(session_id) end)

    %{launch_spec: %{authority: authority}} = :sys.get_state(run.pid)
    token = authority.token
    socket_path = authority.socket_path
    assert_eventually(1_000, fn -> Mitos.KeyVault.leases(session_id) != [] end)

    allowed =
      authority_request(
        socket_path,
        %{
          "method" => "tool_broker",
          "token" => token,
          "context" => %{
            "argv" => ["/bin/sh", "-c", "printf policy-ok"],
            "dest" => "api.allowed.test"
          }
        },
        6_000
      )

    assert allowed["decision"] == "authorize"
    assert allowed["payload"]["stdout"] == "policy-ok"
    assert allowed["payload"]["exit_status"] == 0

    Mitos.PubSub.subscribe(session_id)

    approved =
      authorized_request(run.pid, session_id, socket_path, %{
        "method" => "tool_broker",
        "token" => token,
        "context" => %{
          "argv" => ["/bin/sh", "-c", "printf approved-policy-ok"],
          "dest" => "api.approved.test"
        }
      })

    assert approved["decision"] == "authorize"
    assert approved["payload"]["stdout"] == "approved-policy-ok"
    assert approved["payload"]["exit_status"] == 0

    missing_credential =
      authority_request(
        socket_path,
        %{
          "method" => "tool_broker",
          "token" => token,
          "context" => %{
            "argv" => ["/bin/sh", "-c", "printf should-not-run"],
            "dest" => "api.missing-credential.test"
          }
        },
        6_000
      )

    assert missing_credential["decision"] == "deny"
    assert missing_credential["reason"] == "credential unavailable"
    assert missing_credential["payload"] == %{}

    wrong_seller_credential =
      authority_request(
        socket_path,
        %{
          "method" => "tool_broker",
          "token" => token,
          "context" => %{
            "argv" => ["/bin/sh", "-c", "printf should-not-run"],
            "dest" => "api.other-seller.test"
          }
        },
        6_000
      )

    assert wrong_seller_credential["decision"] == "deny"
    assert wrong_seller_credential["reason"] == "credential seller mismatch"
    assert wrong_seller_credential["payload"] == %{}

    denied =
      authority_request(
        socket_path,
        %{
          "method" => "tool_broker",
          "token" => token,
          "context" => %{
            "argv" => ["/bin/sh", "-c", "printf should-not-run"],
            "dest" => "api.denied.test"
          }
        },
        6_000
      )

    assert denied["decision"] == "deny"
    assert denied["reason"] == "egress policy denied"
    assert denied["payload"] == %{}

    canonical_events =
      session_id
      |> Mitos.Trace.read_events(base_dir: trace_base_dir)
      |> Enum.filter(&(Map.get(&1, "kind") == "effect"))

    assert Enum.any?(canonical_events, fn
             %{
               "type" => "egress_allowed",
               "ref" => %{
                 "dest" => "api.allowed.test",
                 "credential_id" => "seller-key-ref",
                 "seller_id" => "seller-a",
                 "policy_rule" => "allow-api-example"
               }
             } ->
               true

             _ ->
               false
           end)

    assert Enum.any?(canonical_events, fn
             %{
               "type" => "egress_allowed",
               "ref" => %{
                 "dest" => "api.approved.test",
                 "credential_id" => "seller-key-ref",
                 "seller_id" => "seller-a",
                 "policy_rule" => "approve-api-example"
               }
             } ->
               true

             _ ->
               false
           end)

    assert Enum.any?(canonical_events, fn
             %{
               "type" => "egress_denied",
               "ref" => %{"dest" => "api.other-seller.test"},
               "payload" => %{"reason" => "credential seller mismatch"}
             } ->
               true

             _ ->
               false
           end)

    assert Enum.any?(canonical_events, fn
             %{
               "type" => "egress_denied",
               "ref" => %{"dest" => "api.missing-credential.test"},
               "payload" => %{"reason" => "credential unavailable"}
             } ->
               true

             _ ->
               false
           end)

    assert Enum.any?(canonical_events, fn
             %{
               "type" => "egress_denied",
               "ref" => %{"dest" => "api.denied.test"},
               "payload" => %{"reason" => "egress policy denied"}
             } ->
               true

             _ ->
               false
           end)

    refute inspect(canonical_events) =~ "super-secret-token"
    refute inspect(canonical_events) =~ "other-super-secret-token"
  end

  test "broker defaults to deny when no egress policy is configured", %{binary: binary} do
    session_id =
      "authority-default-deny-" <> Integer.to_string(System.unique_integer([:positive]))

    trace_base_dir = unique_tmp_dir("session-authority-default-deny-trace")

    on_exit(fn -> File.rm_rf!(trace_base_dir) end)

    {:ok, run} =
      Mitos.Agent.start(
        session_id: session_id,
        trace_base_dir: trace_base_dir,
        project_id: "proj-a",
        provider: "generic",
        task: "",
        transport_order: ["pty"],
        argv: ["/bin/sh", "-c", "sleep 10"],
        sidecar_path: binary,
        idle_ms: 50
      )

    on_exit(fn -> stop_session(session_id) end)

    %{launch_spec: %{authority: authority}} = :sys.get_state(run.pid)

    denied =
      authority_request(
        authority.socket_path,
        %{
          "method" => "tool_broker",
          "token" => authority.token,
          "context" => %{
            "argv" => ["/bin/sh", "-c", "printf should-not-run"],
            "dest" => "api.default-deny.test"
          }
        },
        6_000
      )

    assert denied["decision"] == "deny"
    assert denied["reason"] == "egress policy denied"
    assert denied["payload"] == %{}

    canonical_events =
      session_id
      |> Mitos.Trace.read_events(base_dir: trace_base_dir)
      |> Enum.filter(&(Map.get(&1, "kind") == "effect"))

    assert Enum.any?(canonical_events, fn
             %{
               "type" => "egress_denied",
               "ref" => %{"dest" => "api.default-deny.test"},
               "payload" => %{"reason" => "egress policy denied"}
             } ->
               true

             _ ->
               false
           end)

    refute Enum.any?(canonical_events, &(Map.get(&1, "type") == "egress_attempted"))
    refute Enum.any?(canonical_events, &(Map.get(&1, "type") == "tool_brokered"))
  end

  defp authorized_request(session_pid, session_id, socket_path, request) do
    task = Task.async(fn -> authority_request(socket_path, request, 6_000) end)
    method = request["method"]

    request_id = receive_authority_request(session_id, method)
    Mitos.Session.resolve(session_pid, request_id, :authorize, %{"approved_by" => "test"})
    Task.await(task, 7_000)
  end

  defp denied_request(session_pid, session_id, socket_path, request) do
    task = Task.async(fn -> authority_request(socket_path, request, 6_000) end)
    method = request["method"]

    request_id = receive_authority_request(session_id, method)
    Mitos.Session.resolve(session_pid, request_id, :deny, %{"reason" => "policy denied"})
    Task.await(task, 7_000)
  end

  defp receive_authority_request(session_id, method) do
    receive do
      {:mitos, ^session_id,
       {:event,
        %{
          "type" => "ACTION_REQUESTED",
          "origin" => "authority_socket",
          "method" => ^method,
          "request_id" => request_id
        }}} ->
        request_id

      {:mitos, ^session_id, _other} ->
        receive_authority_request(session_id, method)
    after
      5_000 -> flunk("did not receive authority_socket ACTION_REQUESTED for #{method}")
    end
  end

  defp assert_eventually(deadline_ms, fun) do
    deadline = System.monotonic_time(:millisecond) + deadline_ms
    do_assert_eventually(deadline, fun)
  end

  defp do_assert_eventually(deadline, fun) do
    if fun.() do
      assert true
    else
      if System.monotonic_time(:millisecond) >= deadline do
        flunk("condition was not met before timeout")
      else
        Process.sleep(20)
        do_assert_eventually(deadline, fun)
      end
    end
  end

  defp authority_request(socket_path, request, timeout) do
    {:ok, socket} =
      :gen_tcp.connect({:local, socket_path}, 0, [
        :binary,
        packet: :line,
        active: false
      ])

    :ok = :gen_tcp.send(socket, Jason.encode!(request) <> "\n")
    {:ok, line} = :gen_tcp.recv(socket, 0, timeout)
    :gen_tcp.close(socket)
    Jason.decode!(String.trim(line))
  end

  defp find_effect(events, type, dest) do
    Enum.find(events, fn
      %{"kind" => "effect", "type" => ^type, "ref" => %{"dest" => ^dest}} -> true
      %{"type" => ^type, "ref" => %{"dest" => ^dest}} -> true
      _event -> false
    end)
  end

  defp stop_session(session_id) do
    try do
      Mitos.SessionSupervisor.stop_session(session_id)
    catch
      :exit, _ -> :ok
    end
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
