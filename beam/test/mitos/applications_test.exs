defmodule Mitos.ApplicationsTest do
  use ExUnit.Case, async: false

  import Plug.Test

  setup do
    trace_dir = unique_tmp_dir("app-route-traces")
    apps_dir = unique_tmp_dir("apps")
    packages_dir = unique_tmp_dir("app-packages")
    workloads_dir = unique_tmp_dir("app-route-workloads")

    File.mkdir_p!(trace_dir)
    File.mkdir_p!(apps_dir)
    File.mkdir_p!(packages_dir)
    File.mkdir_p!(workloads_dir)

    previous_trace_dir = Application.get_env(:mitos, :trace_base_dir)
    previous_apps_dir = Application.get_env(:mitos, :apps_dir)
    previous_packages_dir = Application.get_env(:mitos, :app_packages_dir)
    previous_workloads_dir = Application.get_env(:mitos, :workloads_dir)

    Application.put_env(:mitos, :trace_base_dir, trace_dir)
    Application.put_env(:mitos, :apps_dir, apps_dir)
    Application.put_env(:mitos, :app_packages_dir, packages_dir)
    Application.put_env(:mitos, :workloads_dir, workloads_dir)

    on_exit(fn ->
      restore_env(:trace_base_dir, previous_trace_dir)
      restore_env(:apps_dir, previous_apps_dir)
      restore_env(:app_packages_dir, previous_packages_dir)
      restore_env(:workloads_dir, previous_workloads_dir)
      writable_rm_rf(trace_dir)
      File.rm_rf!(apps_dir)
      File.rm_rf!(packages_dir)
      File.rm_rf!(workloads_dir)
    end)

    {:ok,
     trace_dir: trace_dir,
     apps_dir: apps_dir,
     packages_dir: packages_dir,
     workloads_dir: workloads_dir}
  end

  test "lists, fetches, and runs a local app entrypoint", %{
    apps_dir: apps_dir,
    trace_dir: trace_dir
  } do
    write_app!(apps_dir, "ship-app")

    list_conn = conn(:get, "/apps") |> Mitos.Web.Router.call([])
    assert list_conn.status == 200
    list_body = Jason.decode!(list_conn.resp_body)
    assert list_body["totals"] == %{"apps" => 1}
    assert [%{"id" => "ship-app", "name" => "ship-app"}] = list_body["apps"]

    get_conn = conn(:get, "/apps/ship-app") |> Mitos.Web.Router.call([])
    assert get_conn.status == 200
    app = Jason.decode!(get_conn.resp_body)["app"]
    assert app["app_id"] =~ ~r/^app:sha256:[0-9a-f]{64}$/
    assert [%{"id" => "ship", "pipeline" => "ship-flow"}] = app["entrypoints"]

    run_conn =
      json_conn(:post, "/apps/ship-app/entrypoints/ship/runs", %{
        inputs: %{objective: "from route", name: "Ada"},
        signal: %{
          connector: "webhook",
          idempotency_key: "webhook-delivery-1",
          reply_ref: "https://callback.test/runs/1"
        },
        task: %{task_id: "app-route-1"},
        tenant: %{org: "buyer-a", project: "design"}
      })
      |> Mitos.Web.Router.call([])

    assert run_conn.status == 200
    body = Jason.decode!(run_conn.resp_body)
    assert body["app_id"] == app["app_id"]
    assert body["entrypoint"] == "ship"
    assert body["app_run_id"] =~ "app-"
    assert body["pipeline_id"] == "ship-flow"
    assert body["pipeline"]["pipeline_id"] == "ship-flow"
    assert [%{"receipt" => %{"sink" => "web", "status" => "sent"}}] = body["deliveries"]
    assert Enum.map(body["pipeline"]["steps"], & &1["id"]) == ["ship-cap"]
    assert body["deliverable_summary"] =~ "FAKE_FINAL: App from route for Ada"

    app_events = Mitos.Trace.read_events(body["app_run_id"], base_dir: trace_dir)

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "signal_received",
               "ref" => %{
                 "connector" => "webhook",
                 "idempotency_key" => "webhook-delivery-1",
                 "task_id" => "app-route-1"
               },
               "payload" => %{
                 "intake" => %{"reply_ref" => "https://callback.test/runs/1"}
               }
             } ->
               true

             _event ->
               false
           end)

    task_done_seq =
      Enum.find_value(app_events, fn
        %{
          "kind" => "effect",
          "type" => "task_done",
          "ref" => %{"task_id" => "app-route-1"},
          "seq" => seq
        } ->
          seq

        _event ->
          nil
      end)

    assert is_integer(task_done_seq)

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "delivery_sent",
               "ref" => %{"sink" => "web", "task_id" => "app-route-1"},
               "caused_by" => ^task_done_seq
             } ->
               true

             _event ->
               false
           end)

    delivery_sent_count =
      Enum.count(app_events, fn
        %{
          "kind" => "effect",
          "type" => "delivery_sent",
          "ref" => %{"task_id" => "app-route-1"}
        } ->
          true

        _event ->
          false
      end)

    redrive_conn =
      json_conn(:post, "/app-runs/#{body["app_run_id"]}/deliveries/redrive", %{})
      |> Mitos.Web.Router.call([])

    assert redrive_conn.status == 200
    redrive = Jason.decode!(redrive_conn.resp_body)["redrive"]
    assert redrive["app_run_id"] == body["app_run_id"]
    assert redrive["app_id"] == app["app_id"]
    assert redrive["task_id"] == "app-route-1"
    assert redrive["idempotent?"] == true
    assert redrive["effects_appended"] == 0
    assert redrive["new_deliveries"] == 0
    assert redrive["deliveries"] == body["deliveries"]

    app_events_after_redrive = Mitos.Trace.read_events(body["app_run_id"], base_dir: trace_dir)

    assert delivery_sent_count ==
             Enum.count(app_events_after_redrive, fn
               %{
                 "kind" => "effect",
                 "type" => "delivery_sent",
                 "ref" => %{"task_id" => "app-route-1"}
               } ->
                 true

               _event ->
                 false
             end)

    board = conn(:get, "/work-pool") |> Mitos.Web.Router.call([]) |> decode_body()
    app_task = Enum.find(board["tasks"], &(&1["id"] == "app-route-1"))
    step_task = Enum.find(board["tasks"], &(&1["id"] == "app-route-1:ship-cap"))

    assert app_task["status"] == "done"
    assert app_task["pipeline"] == "ship-flow"
    assert app_task["source"] == "connector:webhook"
    assert get_in(app_task, ["payload", "dedupe_key"]) == "webhook-delivery-1"
    assert get_in(app_task, ["provenance", "app_id"]) == app["app_id"]
    assert get_in(app_task, ["provenance", "entrypoint"]) == "ship"

    assert step_task["status"] == "done"
    assert step_task["pipeline"] == "app:#{app["app_id"]}:ship-flow"
    assert get_in(step_task, ["provenance", "app_id"]) == app["app_id"]
    assert get_in(step_task, ["provenance", "entrypoint"]) == "ship"
  end

  test "publishes a local app package through the app route", %{
    apps_dir: apps_dir,
    packages_dir: packages_dir,
    workloads_dir: workloads_dir
  } do
    write_app!(apps_dir, "ship-app")
    write_catalog_workload!(workloads_dir)

    conn =
      json_conn(:post, "/apps/ship-app/packages", %{
        package_id: "ship-release",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 201

    package = Jason.decode!(conn.resp_body)["package"]
    assert package["package_id"] == "ship-release"
    assert package["app_id"] =~ ~r/^app:sha256:[0-9a-f]{64}$/
    assert package["layout_dir"] == Path.expand(Path.join(packages_dir, "ship-release"))
    assert package["manifest_digest"] =~ ~r/^sha256:[0-9a-f]{64}$/
    assert package["app"]["name"] == "ship-app"

    assert %{"imageLayoutVersion" => "1.0.0"} =
             package["layout_dir"] |> Path.join("oci-layout") |> File.read!() |> Jason.decode!()

    index = package["layout_dir"] |> Path.join("index.json") |> File.read!() |> Jason.decode!()
    assert [manifest_descriptor] = index["manifests"]
    assert manifest_descriptor["artifactType"] == "application/vnd.mitos.application.v1+json"
    assert manifest_descriptor["annotations"]["dev.mitos.app_id"] == package["app_id"]
    assert manifest_descriptor["annotations"]["org.opencontainers.image.ref.name"] == "ship-1"

    install_conn =
      json_conn(:post, "/app-packages/ship-release/install", %{
        install_id: "ship-installed",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert install_conn.status == 201
    install = Jason.decode!(install_conn.resp_body)["install"]
    assert install["package_id"] == "ship-release"
    assert install["install_id"] == "ship-installed"
    assert install["app_id"] == package["app_id"]
    assert install["app_dir"] == Path.expand(Path.join(apps_dir, "ship-installed"))
    assert install["app"]["id"] == "ship-installed"
    assert [%{"as" => "ship-cap", "ref" => "cap:sha256:" <> _}] = install["app"]["capabilities"]

    installed_conn = conn(:get, "/apps/ship-installed") |> Mitos.Web.Router.call([])
    assert installed_conn.status == 200
    installed_app = Jason.decode!(installed_conn.resp_body)["app"]
    assert installed_app["app_id"] == package["app_id"]
    assert installed_app["id"] == "ship-app"

    run_conn =
      json_conn(:post, "/apps/ship-installed/entrypoints/ship/runs", %{
        inputs: %{objective: "from installed package", name: "Ada"},
        task: %{task_id: "installed-route-1"},
        tenant: %{org: "buyer-a", project: "design"}
      })
      |> Mitos.Web.Router.call([])

    assert run_conn.status == 200
    run = Jason.decode!(run_conn.resp_body)
    assert run["app_id"] == package["app_id"]
    assert run["task_id"] == "installed-route-1"
    assert run["deliverable_summary"] =~ "FAKE_FINAL: Catalog from installed package for Ada"
    assert [%{"capability_id" => "ship-cap"}] = run["pipeline"]["steps"]
  end

  test "package publish route reports the frozen manifest without tenant binding sidecars", %{
    apps_dir: apps_dir
  } do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [%{connector: "web", entrypoint: "ship", path: "/default/ship"}],
        outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
      }
    )

    app_dir = Path.join(apps_dir, "ship-app")
    assert {:ok, base_app} = Mitos.AppManifest.load(app_dir, apply_bindings: false)

    File.write!(
      Path.join(app_dir, "bindings.json"),
      Jason.encode!(%{
        "app_id" => base_app.app_id,
        "io" => %{
          inputs: [%{connector: "webhook", entrypoint: "ship", path: "/tenant/ship"}],
          outputs: [
            %{
              sink: "reply",
              entrypoint: "ship",
              on: "run.done",
              reply_to: "input.reply_ref"
            }
          ]
        },
        "credential_scope" => "tenant_control_plane",
        "defaults_overridden?" => true
      })
    )

    bound_conn = conn(:get, "/apps/ship-app") |> Mitos.Web.Router.call([])
    assert bound_conn.status == 200

    assert get_in(decode_body(bound_conn), ["app", "io", "inputs", Access.at(0), "path"]) ==
             "/tenant/ship"

    publish_conn =
      json_conn(:post, "/apps/ship-app/packages", %{
        package_id: "ship-frozen",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert publish_conn.status == 201
    package = decode_body(publish_conn)["package"]
    assert get_in(package, ["app", "io", "inputs", Access.at(0), "path"]) == "/default/ship"
    assert get_in(package, ["app", "io", "outputs", Access.at(0), "sink"]) == "web"

    install_conn =
      json_conn(:post, "/app-packages/ship-frozen/install", %{
        install_id: "ship-frozen-install",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert install_conn.status == 201
    install = decode_body(install_conn)["install"]
    assert get_in(install, ["app", "io", "inputs", Access.at(0), "path"]) == "/default/ship"
    assert get_in(install, ["app", "io", "outputs", Access.at(0), "sink"]) == "web"
  end

  test "app package install route checks default io sink availability", %{apps_dir: apps_dir} do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [%{connector: "webhook", entrypoint: "ship", path: "/default/ship"}],
        outputs: [%{sink: "reply", entrypoint: "ship", on: "run.done"}]
      }
    )

    publish_conn =
      json_conn(:post, "/apps/ship-app/packages", %{
        package_id: "ship-default-io-release",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert publish_conn.status == 201

    unavailable_sink_conn =
      json_conn(:post, "/app-packages/ship-default-io-release/install", %{
        install_id: "ship-default-io-installed",
        tag: "ship-1",
        availability: %{connectors: ["webhook"], sinks: ["web"]}
      })
      |> Mitos.Web.Router.call([])

    assert unavailable_sink_conn.status == 422

    assert Jason.decode!(unavailable_sink_conn.resp_body) == %{
             "error" => "invalid application package",
             "detail" => "missing required sink reply"
           }

    refute File.exists?(Path.join([apps_dir, "ship-default-io-installed", "app.json"]))

    install_conn =
      json_conn(:post, "/app-packages/ship-default-io-release/install", %{
        install_id: "ship-default-io-installed",
        tag: "ship-1",
        availability: %{connectors: ["webhook"], sinks: ["reply"]}
      })
      |> Mitos.Web.Router.call([])

    assert install_conn.status == 201
    assert File.exists?(Path.join([apps_dir, "ship-default-io-installed", "app.json"]))
  end

  test "app package install route applies tenant io bindings for installed connector runs", %{
    apps_dir: apps_dir,
    workloads_dir: workloads_dir
  } do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [%{connector: "web", entrypoint: "ship", path: "/default/ship"}],
        outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
      }
    )

    write_catalog_workload!(workloads_dir)

    publish_conn =
      json_conn(:post, "/apps/ship-app/packages", %{
        package_id: "ship-release",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert publish_conn.status == 201
    package = Jason.decode!(publish_conn.resp_body)["package"]

    unavailable_sink_conn =
      json_conn(:post, "/app-packages/ship-release/install", %{
        install_id: "ship-installed",
        tag: "ship-1",
        available_connectors: ["webhook"],
        available_sinks: ["web"],
        io: %{
          inputs: [%{connector: "webhook", entrypoint: "ship", path: "/tenant/ship"}],
          outputs: [%{sink: "reply", entrypoint: "ship", on: "run.done"}]
        }
      })
      |> Mitos.Web.Router.call([])

    assert unavailable_sink_conn.status == 422

    assert Jason.decode!(unavailable_sink_conn.resp_body) == %{
             "error" => "invalid application package",
             "detail" => "missing required sink reply"
           }

    refute File.exists?(Path.join([apps_dir, "ship-installed", "app.json"]))

    install_conn =
      json_conn(:post, "/app-packages/ship-release/install", %{
        install_id: "ship-installed",
        tag: "ship-1",
        available_connectors: ["webhook"],
        available_sinks: ["reply"],
        io: %{
          inputs: [
            %{
              connector: "webhook",
              entrypoint: "ship",
              path: "/tenant/ship",
              select: %{mode: "explicit", pipeline: "ship-flow"}
            }
          ],
          outputs: [
            %{
              sink: "reply",
              entrypoint: "ship",
              on: "run.done",
              reply_to: "input.reply_ref"
            }
          ]
        }
      })
      |> Mitos.Web.Router.call([])

    assert install_conn.status == 201
    install = Jason.decode!(install_conn.resp_body)["install"]
    assert install["app_id"] == package["app_id"]
    assert install["bindings_applied?"] == true
    assert get_in(install, ["app", "io", "inputs", Access.at(0), "path"]) == "/tenant/ship"
    assert get_in(install, ["app", "io", "outputs", Access.at(0), "sink"]) == "reply"

    installed_conn = conn(:get, "/apps/ship-installed") |> Mitos.Web.Router.call([])
    assert installed_conn.status == 200
    installed_app = Jason.decode!(installed_conn.resp_body)["app"]
    assert get_in(installed_app, ["io", "inputs", Access.at(0), "path"]) == "/tenant/ship"

    run_conn =
      json_conn(:post, "/apps/ship-installed/connectors/webhook/signals", %{
        path: "/tenant/ship",
        delivery_id: "tenant-bound-1",
        reply_ref: "tenant-thread-1",
        inputs: %{objective: "from bound connector", name: "Ada"},
        task: %{task_id: "bound-installed-route"}
      })
      |> Mitos.Web.Router.call([])

    assert run_conn.status == 200
    run = Jason.decode!(run_conn.resp_body)
    assert run["app_id"] == package["app_id"]
    assert run["task_id"] == "bound-installed-route"
    assert get_in(run, ["io_binding", "path"]) == "/tenant/ship"

    assert [
             %{
               "receipt" => %{
                 "sink" => "reply",
                 "destination" => "tenant-thread-1",
                 "status" => "sent"
               },
               "payload" => %{"reply_ref" => "tenant-thread-1"}
             }
           ] = run["deliveries"]
  end

  test "connector signal route selects an explicit app input binding", %{
    apps_dir: apps_dir,
    trace_dir: trace_dir
  } do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [
          %{
            connector: "webhook",
            path: "/incoming/ship",
            entrypoint: "ship",
            select: %{mode: "explicit", pipeline: "ship-flow"}
          }
        ],
        outputs: [
          %{
            sink: "reply",
            on: "run.done",
            entrypoint: "ship",
            reply_to: "input.reply_ref"
          }
        ]
      }
    )

    conn =
      json_conn(:post, "/apps/ship-app/connectors/webhook/signals", %{
        path: "/incoming/ship",
        delivery_id: "evt-ship-1",
        reply_ref: "reply-thread-1",
        inputs: %{objective: "from webhook", name: "Ada"},
        task: %{task_id: "webhook-route-1"},
        tenant: %{org: "buyer-a", project: "design"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 200
    body = Jason.decode!(conn.resp_body)
    assert body["connector"] == "webhook"
    assert body["entrypoint"] == "ship"
    assert body["io_binding"]["path"] == "/incoming/ship"
    assert body["task_id"] == "webhook-route-1"
    assert body["deliverable_summary"] =~ "FAKE_FINAL: App from webhook for Ada"

    assert [
             %{
               "receipt" => %{
                 "sink" => "reply",
                 "destination" => "reply-thread-1",
                 "status" => "sent"
               },
               "payload" => %{"reply_ref" => "reply-thread-1"}
             }
           ] = body["deliveries"]

    app_events = Mitos.Trace.read_events(body["app_run_id"], base_dir: trace_dir)

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "signal_received",
               "ref" => %{
                 "connector" => "webhook",
                 "idempotency_key" => "evt-ship-1",
                 "task_id" => "webhook-route-1"
               },
               "payload" => %{"intake" => %{"reply_ref" => "reply-thread-1"}}
             } ->
               true

             _event ->
               false
           end)

    board = conn(:get, "/work-pool") |> Mitos.Web.Router.call([]) |> decode_body()
    task = Enum.find(board["tasks"], &(&1["id"] == "webhook-route-1"))
    assert task["source"] == "connector:webhook"
    assert task["status"] == "done"
    assert get_in(task, ["payload", "dedupe_key"]) == "evt-ship-1"
    assert get_in(task, ["payload", "reply_ref"]) == "reply-thread-1"

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "task_enqueued",
               "payload" => %{
                 "task" => %{
                   "payload" => %{
                     "dedupe_key" => "evt-ship-1",
                     "reply_ref" => "reply-thread-1"
                   }
                 }
               }
             } ->
               true

             _event ->
               false
           end)

    assert Enum.any?(app_events, fn
             %{
               "kind" => "effect",
               "type" => "delivery_sent",
               "ref" => %{"sink" => "reply", "dest" => "reply-thread-1"},
               "payload" => %{
                 "receipt" => %{"destination" => "reply-thread-1"},
                 "rendered" => %{"reply_ref" => "reply-thread-1"}
               }
             } ->
               true

             _event ->
               false
           end)

    duplicate_conn =
      json_conn(:post, "/apps/ship-app/connectors/webhook/signals", %{
        path: "/incoming/ship",
        delivery_id: "evt-ship-1",
        reply_ref: "reply-thread-redelivery",
        inputs: %{objective: "from webhook duplicate", name: "Grace"},
        task: %{task_id: "webhook-route-duplicate"},
        tenant: %{org: "buyer-a", project: "design"}
      })
      |> Mitos.Web.Router.call([])

    assert duplicate_conn.status == 200
    duplicate = Jason.decode!(duplicate_conn.resp_body)
    assert duplicate["duplicate?"] == true
    assert duplicate["app_run_id"] == body["app_run_id"]
    assert duplicate["task_id"] == "webhook-route-1"
    assert duplicate["deliverable_summary"] == body["deliverable_summary"]
    assert duplicate["deliveries"] == body["deliveries"]
    refute duplicate["deliverable_summary"] =~ "Grace"

    app_id = body["app_id"]
    all_events = Mitos.Runs.all_events(per_run_limit: :all)

    assert 1 ==
             Enum.count(all_events, fn
               %{
                 "kind" => "effect",
                 "type" => "signal_received",
                 "ref" => %{
                   "app_id" => ^app_id,
                   "connector" => "webhook",
                   "idempotency_key" => "evt-ship-1"
                 }
               } ->
                 true

               _event ->
                 false
             end)

    refute Enum.any?(all_events, fn
             %{
               "kind" => "effect",
               "type" => "task_enqueued",
               "payload" => %{"task" => %{"id" => "webhook-route-duplicate"}}
             } ->
               true

             _event ->
               false
           end)
  end

  test "connector signal route requires declared webhook paths", %{apps_dir: apps_dir} do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [
          %{
            connector: "webhook",
            entrypoint: "ship",
            select: %{mode: "explicit", pipeline: "ship-flow"}
          }
        ],
        outputs: [%{sink: "web", on: "run.done", entrypoint: "ship"}]
      }
    )

    unscoped_conn =
      json_conn(:post, "/apps/ship-app/connectors/webhook/signals", %{
        delivery_id: "evt-unscoped-1",
        inputs: %{objective: "from webhook", name: "Ada"},
        task: %{task_id: "webhook-unscoped"}
      })
      |> Mitos.Web.Router.call([])

    assert unscoped_conn.status == 200

    scoped_conn =
      json_conn(:post, "/apps/ship-app/connectors/webhook/signals", %{
        path: "/incoming/ship",
        delivery_id: "evt-scoped-1",
        inputs: %{objective: "from webhook", name: "Ada"},
        task: %{task_id: "webhook-scoped"}
      })
      |> Mitos.Web.Router.call([])

    assert scoped_conn.status == 404

    assert Jason.decode!(scoped_conn.resp_body) == %{
             "error" => "application connector not found",
             "connector" => "webhook"
           }
  end

  test "connector signal route rejects ambiguous input bindings", %{apps_dir: apps_dir} do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [
          %{
            connector: "webhook",
            path: "/incoming/ship",
            entrypoint: "ship",
            select: %{mode: "explicit", pipeline: "ship-flow"}
          },
          %{
            connector: "webhook",
            path: "/incoming/ship",
            entrypoint: "ship",
            select: %{mode: "explicit", pipeline: "ship-flow"}
          }
        ],
        outputs: [%{sink: "web", on: "run.done", entrypoint: "ship"}]
      }
    )

    conn =
      json_conn(:post, "/apps/ship-app/connectors/webhook/signals", %{
        path: "/incoming/ship",
        delivery_id: "evt-ambiguous-1",
        inputs: %{objective: "from webhook", name: "Ada"},
        task: %{task_id: "webhook-ambiguous"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 422

    assert Jason.decode!(conn.resp_body) == %{
             "error" => "invalid application io",
             "detail" => "input binding is ambiguous"
           }
  end

  test "connector signal dedupe honors an explicit trace base dir", %{apps_dir: apps_dir} do
    explicit_traces = unique_tmp_dir("app-explicit-traces")
    File.mkdir_p!(explicit_traces)

    on_exit(fn -> writable_rm_rf(explicit_traces) end)

    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [
          %{
            connector: "webhook",
            path: "/incoming/ship",
            entrypoint: "ship",
            select: %{mode: "explicit", pipeline: "ship-flow"}
          }
        ],
        outputs: [
          %{
            sink: "reply",
            on: "run.done",
            entrypoint: "ship",
            reply_to: "input.reply_ref"
          }
        ]
      }
    )

    assert {:ok, first} =
             Mitos.Applications.run_connector(
               "ship-app",
               "webhook",
               %{"objective" => "from explicit trace root", "name" => "Ada"},
               path: "/incoming/ship",
               delivery_id: "evt-explicit-1",
               reply_ref: "explicit-thread-1",
               task_id: "explicit-route-1",
               trace_base_dir: explicit_traces
             )

    assert first["task_id"] == "explicit-route-1"
    assert Mitos.Trace.read_events(first["app_run_id"], base_dir: explicit_traces) != []

    assert {:ok, duplicate} =
             Mitos.Applications.run_connector(
               "ship-app",
               "webhook",
               %{"objective" => "from explicit duplicate", "name" => "Grace"},
               path: "/incoming/ship",
               delivery_id: "evt-explicit-1",
               reply_ref: "explicit-thread-redelivery",
               task_id: "explicit-route-duplicate",
               trace_base_dir: explicit_traces
             )

    assert duplicate["duplicate?"] == true
    assert duplicate["app_run_id"] == first["app_run_id"]
    assert duplicate["task_id"] == "explicit-route-1"
    assert duplicate["deliverable_summary"] == first["deliverable_summary"]
    refute duplicate["deliverable_summary"] =~ "Grace"

    app_id = first["app_id"]
    events = Mitos.Trace.read_events(first["app_run_id"], base_dir: explicit_traces)

    assert 1 ==
             Enum.count(events, fn
               %{
                 "kind" => "effect",
                 "type" => "signal_received",
                 "ref" => %{
                   "app_id" => ^app_id,
                   "connector" => "webhook",
                   "idempotency_key" => "evt-explicit-1"
                 }
               } ->
                 true

               _event ->
                 false
             end)

    refute Enum.any?(events, fn
             %{
               "kind" => "effect",
               "type" => "task_enqueued",
               "payload" => %{"task" => %{"id" => "explicit-route-duplicate"}}
             } ->
               true

             _event ->
               false
           end)
  end

  test "connector signal route rejects unbound connectors", %{apps_dir: apps_dir} do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [%{connector: "webhook", path: "/incoming/ship", entrypoint: "ship"}],
        outputs: []
      }
    )

    conn =
      json_conn(:post, "/apps/ship-app/connectors/chat/signals", %{
        inputs: %{objective: "from chat", name: "Ada"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 404

    assert Jason.decode!(conn.resp_body) == %{
             "error" => "application connector not found",
             "connector" => "chat"
           }
  end

  test "app package install route fails closed when declared requirements are unavailable", %{
    apps_dir: apps_dir
  } do
    write_app!(apps_dir, "ship-app",
      io: %{
        inputs: [%{connector: "webhook", entrypoint: "ship"}],
        outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
      },
      requirements: %{connectors: ["webhook"], harnesses: ["fake"]}
    )

    publish_conn =
      json_conn(:post, "/apps/ship-app/packages", %{
        package_id: "ship-release",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert publish_conn.status == 201

    missing_connector_conn =
      json_conn(:post, "/app-packages/ship-release/install", %{
        install_id: "ship-installed",
        tag: "ship-1"
      })
      |> Mitos.Web.Router.call([])

    assert missing_connector_conn.status == 422

    assert Jason.decode!(missing_connector_conn.resp_body) == %{
             "error" => "invalid application package",
             "detail" => "missing required connector webhook"
           }

    refute File.exists?(Path.join([apps_dir, "ship-installed", "app.json"]))

    missing_harness_conn =
      json_conn(:post, "/app-packages/ship-release/install", %{
        install_id: "ship-installed",
        tag: "ship-1",
        available_connectors: ["webhook"]
      })
      |> Mitos.Web.Router.call([])

    assert missing_harness_conn.status == 422

    assert Jason.decode!(missing_harness_conn.resp_body) == %{
             "error" => "invalid application package",
             "detail" => "missing required harness fake"
           }

    refute File.exists?(Path.join([apps_dir, "ship-installed", "app.json"]))

    install_conn =
      json_conn(:post, "/app-packages/ship-release/install", %{
        install_id: "ship-installed",
        tag: "ship-1",
        availability: %{connectors: ["webhook"], harnesses: ["fake"]}
      })
      |> Mitos.Web.Router.call([])

    assert install_conn.status == 201
    assert File.exists?(Path.join([apps_dir, "ship-installed", "app.json"]))
  end

  test "activation route projects app services, pipeline requirements, and declared io", %{
    apps_dir: apps_dir
  } do
    write_app!(apps_dir, "ship-app",
      services: %{requires: ["render-pool"], wants: ["usage-meter"]},
      pipeline_requires: %{services: ["post-process"]},
      io: %{
        inputs: [
          %{
            entrypoint: "ship",
            connector: "webhook",
            path: "/apps/ship/runs",
            select: %{mode: "explicit", pipeline: "ship-flow"}
          }
        ],
        outputs: [
          %{
            entrypoint: "ship",
            sink: "webhook",
            on: "run.done",
            payload: %{include: ["result", "proof_excerpt"]}
          }
        ]
      }
    )

    conn =
      json_conn(:post, "/apps/ship-app/activations", %{
        units: [
          service_unit("render-pool", "ship-flow"),
          service_unit("post-process", "ship-flow")
        ]
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 200
    activation = Jason.decode!(conn.resp_body)["activation"]
    assert activation["app_id"] =~ ~r/^app:sha256:[0-9a-f]{64}$/

    assert activation["services"]["target"] == %{
             "id" => activation["app_id"],
             "description" => "Application ship-app",
             "scope" => "project",
             "requires" => ["render-pool"],
             "wants" => ["usage-meter"]
           }

    assert activation["services"]["pipeline_requires"] == [
             %{"pipeline" => "ship-flow", "services" => ["post-process"], "targets" => []}
           ]

    assert activation["services"]["missing_wants"] == ["usage-meter"]
    active = activation["services"]["active_set"]["active"]
    assert Map.has_key?(active, "render-pool")
    assert Map.has_key?(active, "post-process")
    refute Map.has_key?(active, "usage-meter")
    assert "#{activation["app_id"]} (requires)" in active["render-pool"]["why"]
    assert "pipeline:ship-flow (requires)" in active["post-process"]["why"]

    assert activation["io"]["defaults_applied?"] == false
    assert [%{"connector" => "webhook", "entrypoint" => "ship"}] = activation["io"]["inputs"]
    assert [%{"sink" => "webhook", "entrypoint" => "ship"}] = activation["io"]["outputs"]
    assert activation["io"]["credential_scope"] == "tenant_control_plane"
  end

  test "activation route fails closed when a required service is absent", %{
    apps_dir: apps_dir
  } do
    write_app!(apps_dir, "ship-app", services: %{requires: ["render-pool"], wants: []})

    conn =
      json_conn(:post, "/apps/ship-app/activations", %{units: []})
      |> Mitos.Web.Router.call([])

    assert conn.status == 422

    assert Jason.decode!(conn.resp_body) == %{
             "error" => "application activation failed",
             "detail" => "missing required service render-pool"
           }
  end

  test "activation route applies manual web defaults for apps without io bindings", %{
    apps_dir: apps_dir
  } do
    write_app!(apps_dir, "ship-app")

    conn =
      json_conn(:post, "/apps/ship-app/activations", %{units: []})
      |> Mitos.Web.Router.call([])

    assert conn.status == 200
    io = Jason.decode!(conn.resp_body)["activation"]["io"]
    assert io["defaults_applied?"] == true
    assert Enum.map(io["inputs"], & &1["connector"]) == ["manual", "web"]

    assert Enum.all?(
             io["inputs"],
             &(&1["select"] == %{"mode" => "explicit", "pipeline" => "ship-flow"})
           )

    assert [%{"sink" => "web", "entrypoint" => "ship", "default?" => true}] = io["outputs"]
  end

  test "app package route refuses an existing package directory", %{
    apps_dir: apps_dir,
    packages_dir: packages_dir
  } do
    write_app!(apps_dir, "ship-app")
    File.mkdir_p!(Path.join(packages_dir, "ship-release"))
    File.write!(Path.join([packages_dir, "ship-release", "existing"]), "keep")

    conn =
      json_conn(:post, "/apps/ship-app/packages", %{package_id: "ship-release"})
      |> Mitos.Web.Router.call([])

    assert conn.status == 422

    assert Jason.decode!(conn.resp_body) == %{
             "error" => "invalid application package",
             "detail" => "destination must be empty"
           }
  end

  test "app entrypoint run validates typed inputs before launching", %{apps_dir: apps_dir} do
    write_app!(apps_dir, "ship-app")

    conn =
      json_conn(:post, "/apps/ship-app/entrypoints/ship/runs", %{
        inputs: %{objective: "from route", name: 42}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 422

    assert Jason.decode!(conn.resp_body) == %{
             "error" => "invalid application input",
             "detail" => "input name has invalid type"
           }
  end

  test "app entrypoint run reports an unknown entrypoint", %{apps_dir: apps_dir} do
    write_app!(apps_dir, "ship-app")

    conn =
      json_conn(:post, "/apps/ship-app/entrypoints/missing/runs", %{
        inputs: %{objective: "from route", name: "Ada"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 404

    assert Jason.decode!(conn.resp_body) == %{
             "error" => "entrypoint not found",
             "entrypoint" => "missing"
           }
  end

  test "app entrypoint run reports an unresolved content-addressed capability ref", %{
    apps_dir: apps_dir
  } do
    ref = "cap:sha256:" <> String.duplicate("0", 64)
    write_app!(apps_dir, "ship-app", cap_ref: ref)

    conn =
      json_conn(:post, "/apps/ship-app/entrypoints/ship/runs", %{
        inputs: %{objective: "from route", name: "Ada"}
      })
      |> Mitos.Web.Router.call([])

    assert conn.status == 404

    body = Jason.decode!(conn.resp_body)
    assert body["error"] == "application capability ref not found"
    assert body["step_id"] == "ship-cap"
    assert body["capability_id"] == "ship-cap"
    assert body["ref"] == ref
    assert body["app_run_id"] =~ "app-"
  end

  defp write_app!(apps_dir, id, opts \\ []) do
    cap_ref = Keyword.get(opts, :cap_ref, "capabilities/ship/workload.json")
    root = Path.join(apps_dir, id)
    File.mkdir_p!(Path.join(root, "pipelines"))
    File.mkdir_p!(Path.join([root, "capabilities", "ship"]))

    File.write!(Path.join(root, "pipelines/ship.md"), """
    # Pipeline: ship-flow

    Goal: produce a backend application result.

    ## Stages
    1. **ship-cap** - run the local capability package.
    """)

    File.write!(
      Path.join(root, "capabilities/ship/workload.json"),
      Jason.encode!(%{
        provider: "fake",
        template:
          "App {{objective}} for {{name}} step {{pipeline_step}} previous {{previous_results}}",
        capability: %{name: "ship-cap", harnesses: ["fake"], trust_tier: "T1"}
      })
    )

    pipeline =
      %{id: "ship-flow", file: "pipelines/ship.md"}
      |> maybe_put(:requires, Keyword.get(opts, :pipeline_requires))

    File.write!(
      Path.join(root, "app.json"),
      Jason.encode!(%{
        name: "ship-app",
        version: "1.0.0",
        capabilities: [
          %{ref: cap_ref, as: "ship-cap"}
        ],
        pipelines: [pipeline],
        entrypoints: [
          %{
            id: "ship",
            pipeline: "ship-flow",
            title: "Ship app output",
            workspace: "none",
            inputs: %{
              schema: %{
                type: "object",
                required: ["objective"],
                additionalProperties: false,
                properties: %{
                  objective: %{type: "string"},
                  name: %{type: "string"}
                }
              }
            }
          }
        ],
        io: Keyword.get(opts, :io, %{inputs: [], outputs: []}),
        services: Keyword.get(opts, :services, %{requires: [], wants: []}),
        requirements: Keyword.get(opts, :requirements, %{})
      })
    )
  end

  defp service_unit(id, pipeline) do
    %{
      id: id,
      scope: "project",
      schedule: %{type: "manual"},
      execution: %{type: "pipeline", pipeline: pipeline}
    }
  end

  defp write_catalog_workload!(workloads_dir) do
    File.write!(
      Path.join(workloads_dir, "ship-cap.json"),
      Jason.encode!(%{
        provider: "fake",
        template:
          "Catalog {{objective}} for {{name}} step {{pipeline_step}} previous {{previous_results}}",
        capability: %{name: "ship-cap", harnesses: ["fake"], trust_tier: "T1"}
      })
    )
  end

  defp json_conn(method, path, body) do
    method
    |> conn(path, Jason.encode!(body))
    |> Plug.Conn.put_req_header("content-type", "application/json")
  end

  defp decode_body(conn), do: Jason.decode!(conn.resp_body)

  defp unique_tmp_dir(prefix) do
    path = Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
    File.rm_rf!(path)
    path
  end

  defp writable_rm_rf(path) do
    if File.exists?(path) do
      path
      |> Path.join("**/*")
      |> Path.wildcard(match_dot: true)
      |> Enum.each(fn entry ->
        case File.stat(entry) do
          {:ok, %{type: :directory, mode: mode}} -> File.chmod(entry, Bitwise.bor(mode, 0o700))
          {:ok, %{mode: mode}} -> File.chmod(entry, Bitwise.bor(mode, 0o600))
          {:error, _reason} -> :ok
        end
      end)

      File.chmod(path, 0o700)
      File.rm_rf!(path)
    end
  end

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)
end
