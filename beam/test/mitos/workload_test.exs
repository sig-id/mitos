defmodule Mitos.TestRemoteMicrovmProvider do
  @behaviour Mitos.SandboxProvider

  @impl true
  def prepare(%{} = launch_spec) do
    if String.contains?(launch_spec.task, "prepare failure") do
      {:error, :prepare_failed}
    else
      {:ok, launch_spec}
    end
  end

  @impl true
  def start(%{} = launch_spec, _argv, env) do
    send(self(), {:test_remote_microvm_env, env})
    send(self(), {:test_remote_microvm_launch_spec, launch_spec})

    {:ok,
     %{
       launch_spec: launch_spec,
       events: remote_events(launch_spec),
       harvest: %{
         diff: %{"path" => "diff.patch", "sha256" => "sha256:remote-diff", "empty?" => true},
         result: remote_result(launch_spec),
         artifacts: remote_artifacts(launch_spec)
       }
     }}
  end

  @impl true
  def stream(%{launch_spec: %{task: task}, events: events}) when is_binary(task) do
    if String.contains?(task, "timeout job") do
      Stream.resource(
        fn -> :pending end,
        fn
          :pending ->
            Process.sleep(10_000)
            {[], :done}

          :done ->
            {:halt, :done}
        end,
        fn _state -> :ok end
      )
    else
      events
    end
  end

  def stream(%{events: events}), do: events

  @impl true
  def control(%{launch_spec: launch_spec}, command) do
    send(self(), {:test_remote_microvm_control, launch_spec.run_id, command})
    :ok
  end

  def control(_session, _command), do: {:error, :unsupported_control}

  @impl true
  def snapshot(_session), do: :unsupported

  @impl true
  def harvest(%{launch_spec: %{task: task}}) when is_binary(task) do
    if String.contains?(task, "harvest failure") do
      {:error, :harvest_failed}
    else
      {:ok,
       %{
         diff: %{"path" => "diff.patch", "sha256" => "sha256:remote-diff", "empty?" => true},
         result: remote_result(%{task: task}),
         artifacts: remote_artifacts(%{task: task})
       }}
    end
  end

  @impl true
  def harvest(%{harvest: harvest}), do: {:ok, harvest}

  @impl true
  def stop(%{launch_spec: launch_spec}) do
    send(self(), {:test_remote_microvm_stopped, launch_spec.run_id})
    :ok
  end

  def stop(_session), do: :ok

  @impl true
  def capabilities do
    %{
      isolation: :microvm,
      remote?: true,
      egress: :broker,
      data_mount: :snapshot,
      metering: [:cpu, :mem, :wall, :egress],
      snapshot?: true,
      net_modes: [:broker_proxy]
    }
  end

  @impl true
  def child_spec(_opts), do: %{id: __MODULE__, start: {Task, :start_link, [fn -> :ok end]}}

  defp remote_events(launch_spec) do
    events =
      [
        %{"type" => "RUN_STARTED", "provider" => launch_spec.provider, "transport" => "remote"}
      ] ++
        if String.contains?(launch_spec.task, "empty output") do
          []
        else
          [%{"type" => "FINAL_OUTPUT", "text" => "REMOTE_FINAL: #{launch_spec.task}"}]
        end

    token_usage_events =
      cond do
        String.contains?(launch_spec.task, "multi token usage") ->
          [
            %{
              "type" => "token_usage",
              "provider" => launch_spec.provider,
              "model" => "gpt-test",
              "input_tokens" => 10,
              "output_tokens" => 5,
              "usd" => 0.25,
              "wall_ms" => 750
            },
            %{
              "type" => "token_usage",
              "provider" => launch_spec.provider,
              "model" => "gpt-test",
              "input_tokens" => 7,
              "output_tokens" => 3,
              "usd" => 0.15,
              "wall_ms" => 500
            }
          ]

        String.contains?(launch_spec.task, "token usage") ->
          [
            %{
              "type" => "token_usage",
              "provider" => launch_spec.provider,
              "model" => "gpt-test",
              "input_tokens" => 10,
              "output_tokens" => 5,
              "usd" => 0.25,
              "wall_ms" => 750
            }
          ]

        true ->
          []
      end

    events = events ++ token_usage_events

    events ++ [%{"type" => "PROCESS_EXITED", "code" => 0}]
  end

  defp remote_result(launch_spec) do
    cond do
      String.contains?(launch_spec.task, "empty output") ->
        %{"path" => "RESULT", "synthetic" => true, "needs" => ["result_missing"]}

      String.contains?(launch_spec.task, "schema invalid") ->
        %{
          "signal" => "revise",
          "summary" => "remote result",
          "path" => "RESULT",
          "synthetic" => false
        }

      true ->
        %{
          "signal" => "ship",
          "summary" => "remote result",
          "path" => "RESULT",
          "synthetic" => false
        }
    end
  end

  defp remote_artifacts(launch_spec) do
    artifacts = [%{"kind" => "file", "path" => "RESULT", "sha256" => "sha256:result"}]

    if String.contains?(launch_spec.task, "evidence ok") do
      artifacts ++
        [
          %{
            "kind" => "file",
            "path" => "report.md",
            "sha256" => "sha256:report",
            "bytes" => 128
          }
        ]
    else
      artifacts
    end
  end
end

defmodule Mitos.WorkloadTest do
  use ExUnit.Case, async: false

  import Plug.Test
  import Plug.Conn

  alias Mitos.Workload

  setup do
    tmp = Path.join(System.tmp_dir!(), "mitos-workloads-#{System.unique_integer([:positive])}")
    File.mkdir_p!(tmp)
    previous = Application.get_env(:mitos, :workloads_dir)
    Application.put_env(:mitos, :workloads_dir, tmp)

    on_exit(fn ->
      if previous do
        Application.put_env(:mitos, :workloads_dir, previous)
      else
        Application.delete_env(:mitos, :workloads_dir)
      end

      File.rm_rf!(tmp)
    end)

    {:ok, dir: tmp}
  end

  defp write_workload!(dir, id, json) do
    File.write!(Path.join(dir, id <> ".json"), Jason.encode!(json))
  end

  defp maybe_put_metered_units(pricing, "metered"),
    do: Map.put(pricing, :unit_prices_usdc, %{token: "0.01"})

  defp maybe_put_metered_units(pricing, _type), do: pricing

  describe "fetch/1" do
    test "loads a definition with defaults", %{dir: dir} do
      write_workload!(dir, "echo", %{provider: "fake", template: "{{objective}}"})

      assert {:ok, workload} = Workload.fetch("echo")
      assert workload.id == "echo"
      assert workload.provider == "fake"
      assert workload.package_root == dir
      assert workload.transport_order == ["stdio-json", "pty"]
      assert workload.timeout_ms == 120_000
      assert workload.approvals == "deny"
    end

    test "strips // comment lines before decoding", %{dir: dir} do
      File.write!(
        Path.join(dir, "annotated.json"),
        "// operator note\n{\"provider\": \"fake\", \"template\": \"hi\"}\n"
      )

      assert {:ok, %Workload{provider: "fake"}} = Workload.fetch("annotated")
    end

    test "rejects ids that are not plain filename stems" do
      assert {:error, :not_found} = Workload.fetch("../escape")
      assert {:error, :not_found} = Workload.fetch("a/b")
      assert {:error, :not_found} = Workload.fetch(".hidden")
    end

    test "missing file is not_found" do
      assert {:error, :not_found} = Workload.fetch("nope")
    end

    test "definition without provider or template is invalid", %{dir: dir} do
      write_workload!(dir, "bad", %{template: "x"})
      assert {:error, {:invalid_workload, _}} = Workload.fetch("bad")

      write_workload!(dir, "bad2", %{provider: "fake"})
      assert {:error, {:invalid_workload, _}} = Workload.fetch("bad2")
    end

    test "loads canonical capability manifest fields", %{dir: dir} do
      write_workload!(dir, "cap", %{
        provider: "fake",
        template: "{{objective}}",
        env: %{MODE: "dry-run"},
        capability: %{
          name: "demo-capability",
          version: "0.1.0",
          harnesses: ["fake", "codex"],
          trust_tier: "T1",
          egress_policy: [
            %{dest: "api.example.test", credential: "seller-key", requires_approval: false}
          ],
          result: %{path: "RESULT", evidence: ["report.md"]},
          hooks: %{post: [%{name: "gate", run: ["true"], on_fail: "block"}]}
        }
      })

      assert {:ok, workload} = Workload.fetch("cap")
      assert %Mitos.Capability{} = workload.capability
      assert workload.capability.name == "demo-capability"
      assert workload.capability.trust_tier == "T1"
      assert workload.capability.result == %{"path" => "RESULT", "evidence" => ["report.md"]}
      assert workload.env == %{"MODE" => "dry-run"}
    end

    test "loads capability identity from package pillar content", %{dir: dir} do
      File.mkdir_p!(Path.join(dir, "tools"))
      File.mkdir_p!(Path.join(dir, "data"))
      File.write!(Path.join(dir, "skill.md"), "skill v1\n")
      File.write!(Path.join([dir, "tools", "extract.py"]), "print('tool v1')\n")
      File.write!(Path.join([dir, "data", "ref.txt"]), "data v1\n")

      write_workload!(dir, "cap-content-id", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "content-id",
          harnesses: ["fake"],
          skill: "skill.md",
          data: %{path: "data", mount: "/data", ro: true},
          tools: [
            %{
              name: "extract",
              exec: "tools/extract.py",
              input: %{},
              output: %{},
              deterministic: true
            }
          ]
        }
      })

      assert {:ok, first} = Workload.fetch("cap-content-id")

      File.write!(Path.join(dir, "skill.md"), "skill v2\n")

      assert {:ok, changed} = Workload.fetch("cap-content-id")
      refute changed.capability.id == first.capability.id
    end

    test "rejects capability workloads that would inject secrets through env", %{dir: dir} do
      write_workload!(dir, "cap-env-passthrough", %{
        provider: "fake",
        template: "{{objective}}",
        env_passthrough: ["ANTHROPIC_API_KEY"],
        capability: %{
          name: "cap-env-passthrough",
          harnesses: ["fake"],
          trust_tier: "T1"
        }
      })

      assert {:error, {:invalid_workload, "capability workloads must not use env_passthrough"}} =
               Workload.fetch("cap-env-passthrough")

      write_workload!(dir, "cap-secret-env", %{
        provider: "fake",
        template: "{{objective}}",
        env: %{API_KEY: "must-not-enter-sandbox"},
        capability: %{
          name: "cap-secret-env",
          harnesses: ["fake"],
          trust_tier: "T1"
        }
      })

      assert {:error,
              {:invalid_workload, "capability env must not contain secret-like key API_KEY"}} =
               Workload.fetch("cap-secret-env")
    end

    test "loads flat paid endpoint pricing", %{dir: dir} do
      write_workload!(dir, "paid", %{
        provider: "fake",
        template: "{{objective}}",
        seller_id: "seller-a",
        capability: %{
          name: "paid-demo",
          harnesses: ["fake"],
          trust_tier: "T1"
        },
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          compute_surcharge_usdc: "0.05",
          payout_address: "usdc:seller:demo"
        }
      })

      assert {:ok, workload} = Workload.fetch("paid")
      assert workload.pricing["type"] == "flat"
      assert workload.pricing["amount_usdc"] == "2.50"
      assert workload.pricing["take_rate_bps"] == 1500
      assert workload.pricing["compute_surcharge_usdc"] == "0.05"
      assert workload.seller_id == "seller-a"
      assert workload.payout_address == "usdc:seller:demo"
      assert workload.capability.trust_tier == "T2"
    end

    test "loads metered paid endpoint pricing", %{dir: dir} do
      write_workload!(dir, "metered", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "metered-demo",
          harnesses: ["fake"],
          trust_tier: "T1"
        },
        pricing: %{
          type: "metered",
          amount_usdc: "5.00",
          base_usdc: "0.25",
          take_floor_usdc: "0.01",
          unit_prices_usdc: %{
            token: "0.01",
            egress: "0.10",
            tool: "0.05",
            artifact: "0.20",
            ms: "0.000001"
          },
          payout_address: "usdc:seller:demo"
        }
      })

      assert {:ok, workload} = Workload.fetch("metered")
      assert workload.pricing["type"] == "metered"
      assert workload.pricing["amount_usdc"] == "5.00"
      assert workload.pricing["base_usdc"] == "0.25"

      assert workload.pricing["unit_prices_usdc"] == %{
               "token" => "0.01",
               "egress" => "0.10",
               "tool" => "0.05",
               "artifact" => "0.20",
               "ms" => "0.000001"
             }

      assert workload.capability.trust_tier == "T2"
    end

    test "accepts hosted take-rate bounds", %{dir: dir} do
      for {id, take_rate_bps} <- [{"paid-floor-take", 1000}, {"paid-ceiling-take", 1500}] do
        write_workload!(dir, id, %{
          provider: "fake",
          template: "{{objective}}",
          capability: %{
            name: id,
            harnesses: ["fake"],
            trust_tier: "T1"
          },
          pricing: %{
            type: "flat",
            amount_usdc: "2.50",
            take_rate_bps: take_rate_bps,
            take_floor_usdc: "0.01",
            payout_address: "usdc:seller:demo"
          }
        })

        assert {:ok, workload} = Workload.fetch(id)
        assert workload.pricing["take_rate_bps"] == take_rate_bps
      end
    end

    test "rejects paid definitions without a capability manifest", %{dir: dir} do
      write_workload!(dir, "paid-no-cap", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        }
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("paid-no-cap")
      assert detail =~ "paid publication requires a capability manifest"
    end

    test "rejects paid definitions without a payout address", %{dir: dir} do
      write_workload!(dir, "paid-no-payout", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{type: "flat", amount_usdc: "2.50", take_floor_usdc: "0.01"},
        capability: %{
          name: "paid-demo",
          harnesses: ["fake"],
          trust_tier: "T1"
        }
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("paid-no-payout")
      assert detail =~ "invalid pricing"
      assert detail =~ "paid publication requires payout_address"
    end

    test "rejects hosted take rates outside the facilitator bounds", %{dir: dir} do
      for {id, take_rate_bps} <- [{"paid-low-take", 999}, {"paid-high-take", 1501}] do
        write_workload!(dir, id, %{
          provider: "fake",
          template: "{{objective}}",
          capability: %{
            name: id,
            harnesses: ["fake"],
            trust_tier: "T1"
          },
          pricing: %{
            type: "flat",
            amount_usdc: "2.50",
            take_rate_bps: take_rate_bps,
            take_floor_usdc: "0.01",
            payout_address: "usdc:seller:demo"
          }
        })

        assert {:error, {:invalid_workload, detail}} = Workload.fetch(id)
        assert detail =~ "invalid pricing"
        assert detail =~ "take_rate_bps must be between 1000 and 1500"
      end
    end

    test "rejects non-positive take floor", %{dir: dir} do
      write_workload!(dir, "paid-zero-floor", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "paid-zero-floor",
          harnesses: ["fake"],
          trust_tier: "T1"
        },
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0",
          payout_address: "usdc:seller:demo"
        }
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("paid-zero-floor")
      assert detail =~ "invalid pricing"
      assert detail =~ "take_floor_usdc must be positive"
    end

    test "rejects non-positive paid escrow amounts", %{dir: dir} do
      for type <- ["flat", "metered"] do
        id = "paid-zero-#{type}"

        pricing =
          %{
            type: type,
            amount_usdc: "0",
            take_floor_usdc: "0.01",
            payout_address: "usdc:seller:demo"
          }
          |> maybe_put_metered_units(type)

        write_workload!(dir, id, %{
          provider: "fake",
          template: "{{objective}}",
          capability: %{
            name: id,
            harnesses: ["fake"],
            trust_tier: "T1"
          },
          pricing: pricing
        })

        assert {:error, {:invalid_workload, detail}} = Workload.fetch(id)
        assert detail =~ "invalid pricing"
        assert detail =~ "amount_usdc must be positive"
      end
    end

    test "rejects paid definitions without an explicit take floor", %{dir: dir} do
      write_workload!(dir, "paid-no-floor", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "paid-no-floor",
          harnesses: ["fake"],
          trust_tier: "T1"
        },
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          payout_address: "usdc:seller:demo"
        }
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("paid-no-floor")
      assert detail =~ "invalid pricing"
      assert detail =~ "take_floor_usdc is required"
    end

    test "rejects invalid pricing", %{dir: dir} do
      write_workload!(dir, "bad-price", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{type: "metered", amount_usdc: "2.50", take_floor_usdc: "0.01"}
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("bad-price")
      assert detail =~ "invalid pricing"
    end

    test "rejects capability manifests incompatible with selected provider", %{dir: dir} do
      write_workload!(dir, "bad-cap", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{name: "bad", harnesses: ["codex"], trust_tier: "T1"}
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("bad-cap")
      assert detail =~ "not declared in capability.harnesses"
    end

    test "rejects capability manifests that bake injected runtime bindings", %{dir: dir} do
      write_workload!(dir, "bad-runtime-binding", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "bad-runtime-binding",
          harnesses: ["fake"],
          trust_tier: "T1",
          model: "gpt-test"
        }
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("bad-runtime-binding")
      assert detail =~ "invalid capability manifest"
      assert detail =~ "must not bake injected runtime field model"
    end

    test "rejects capability manifests with unsupported top-level fields", %{dir: dir} do
      write_workload!(dir, "bad-capability-extension", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "bad-capability-extension",
          harnesses: ["fake"],
          trust_tier: "T1",
          notes: "not part of the canonical manifest"
        }
      })

      assert {:error, {:invalid_workload, detail}} = Workload.fetch("bad-capability-extension")
      assert detail =~ "invalid capability manifest"
      assert detail =~ ~s(unsupported field "notes")
    end
  end

  describe "render/2" do
    test "substitutes bindings", %{dir: dir} do
      write_workload!(dir, "tpl", %{provider: "fake", template: "Do {{objective}} for {{ name }}"})

      {:ok, workload} = Workload.fetch("tpl")

      assert {:ok, "Do review for ben"} =
               Workload.render(workload, %{"objective" => "review", "name" => "ben"})
    end

    test "reports missing bindings", %{dir: dir} do
      write_workload!(dir, "tpl", %{provider: "fake", template: "{{a}} {{b}}"})
      {:ok, workload} = Workload.fetch("tpl")

      assert {:error, {:missing_bindings, missing}} = Workload.render(workload, %{"a" => "1"})
      assert missing == ["b"]
    end
  end

  describe "public_view/1" do
    test "exposes placeholders but never env", %{dir: dir} do
      write_workload!(dir, "secret", %{
        provider: "fake",
        template: "{{objective}}",
        env: %{"API_KEY" => "sekrit"},
        env_passthrough: ["OTHER_KEY"]
      })

      {:ok, workload} = Workload.fetch("secret")
      view = Workload.public_view(workload)

      assert view["placeholders"] == ["objective"]
      refute Map.has_key?(view, "provider")
      refute Map.has_key?(view, "env")
      refute inspect(view) =~ "sekrit"
    end

    test "exposes public pricing without payout internals", %{dir: dir} do
      write_workload!(dir, "paid", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "paid-demo",
          harnesses: ["fake"],
          trust_tier: "T1"
        },
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        }
      })

      {:ok, workload} = Workload.fetch("paid")
      view = Workload.public_view(workload)

      assert view["pricing"] == %{"type" => "flat", "amount_usdc" => "2.50", "currency" => "USDC"}
      assert view["capability"]["trust_tier"] == "T2"
      refute Map.has_key?(view["capability"], "harnesses")
      refute inspect(view) =~ "usdc:seller:demo"
    end

    test "exposes public capability metadata without recipe fields", %{dir: dir} do
      write_workload!(dir, "cap", %{
        provider: "fake",
        template: "{{objective}}",
        capability: %{
          name: "demo-capability",
          harnesses: ["fake"],
          trust_tier: "T1",
          skill: "./secret-skill.md",
          tools: [
            %{
              name: "secret-tool",
              input: %{"type" => "object"},
              output: %{"type" => "object"},
              deterministic: true
            }
          ],
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("cap")
      view = Workload.public_view(workload)

      assert view["capability"]["name"] == "demo-capability"
      assert view["capability"]["trust_tier"] == "T1"
      refute Map.has_key?(view["capability"], "harnesses")
      refute inspect(view) =~ "secret-skill"
      refute inspect(view) =~ "secret-tool"
    end
  end

  describe "HTTP gateway" do
    @moduletag :integration

    setup do
      binary = Path.expand("../../../zig-out/bin/mitos-term", __DIR__)

      unless File.exists?(binary) do
        flunk("mitos-term binary not built at #{binary} — run `zig build` from the repo root")
      end

      previous_broker_endpoint = Application.get_env(:mitos, :broker_endpoint)
      previous_broker_ca_ref = Application.get_env(:mitos, :broker_ca_ref)

      Application.put_env(:mitos, :broker_endpoint, "https://broker.test/proxy")
      Application.put_env(:mitos, :broker_ca_ref, "ca:mitos:test")

      on_exit(fn ->
        restore_env(:broker_endpoint, previous_broker_endpoint)
        restore_env(:broker_ca_ref, previous_broker_ca_ref)
      end)

      :ok
    end

    defp post_json(path, body, headers \\ []) do
      conn(:post, path, Jason.encode!(body))
      |> put_req_header("content-type", "application/json")
      |> put_headers(headers)
      |> Mitos.Web.Router.call([])
    end

    defp put_headers(conn, headers) do
      Enum.reduce(headers, conn, fn {key, value}, acc -> put_req_header(acc, key, value) end)
    end

    test "lists workloads", %{dir: dir} do
      write_workload!(dir, "echo", %{provider: "fake", template: "{{objective}}"})

      conn = conn(:get, "/workloads") |> Mitos.Web.Router.call([])
      assert conn.status == 200

      body = Jason.decode!(conn.resp_body)
      assert [%{"id" => "echo"} = workload] = body["workloads"]
      refute Map.has_key?(workload, "provider")
    end

    test "executes a gentix provider_execution_request through the fake provider", %{dir: dir} do
      write_workload!(dir, "echo", %{
        provider: "fake",
        template: "{{objective}}",
        transport_order: ["stdio-json"],
        timeout_ms: 30_000
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        task: %{
          task_id: "task_test_1",
          objective: "echo this objective",
          expected_output: "the same text"
        },
        capability: %{capability_id: "cap_demo_echo"},
        request: %{}
      }

      conn = post_json("/workloads/echo/execute", request)
      assert conn.status == 200

      body = Jason.decode!(conn.resp_body)
      assert body["deliverable_summary"] == "RESULT artifact missing"
      assert body["deliverable"]["result"]["signal"] == "unknown"
      refute inspect(body["deliverable"]) =~ "FAKE_FINAL"
      assert body["evidence"]["run_id"] =~ "wl-"
      assert body["execution_id"] == body["evidence"]["run_id"]
      assert body["evidence"]["proof"]["redaction"]["source"] == "effect_events_only"
      assert body["evidence"]["proof"]["effect_log_sha256"] =~ "sha256:"
      assert body["evidence"]["proof"]["metering"]["effect_units"] >= 2
      assert body["evidence"]["delivery"]["result"]["signal"] == "unknown"
      refute Map.has_key?(body, "model")

      assert {:ok, run} = Mitos.Runs.fetch(body["execution_id"])
      pool = Mitos.WorkPool.fold_events(run["events"])
      task = get_in(pool, ["tasks", "task_test_1"])

      assert task["status"] == "done"
      assert task["pool"] == "interactive"
      assert task["source"] == "manual"
      assert task["runs"] == [body["execution_id"]]
    end

    test "local structured token usage is folded into canonical budget charges", %{dir: dir} do
      previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)
      trace_base_dir = unique_tmp_dir("local-token-usage-trace")
      Application.put_env(:mitos, :trace_base_dir, trace_base_dir)

      on_exit(fn ->
        if previous_trace_base_dir do
          Application.put_env(:mitos, :trace_base_dir, previous_trace_base_dir)
        else
          Application.delete_env(:mitos, :trace_base_dir)
        end

        writable_rm_rf!(trace_base_dir)
      end)

      write_workload!(dir, "usage-local", %{
        provider: "fake",
        template: "{{objective}}",
        transport_order: ["stdio-json"],
        timeout_ms: 30_000
      })

      {:ok, workload} = Workload.fetch("usage-local")

      assert {:ok, result} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "local token usage job"})

      events = Mitos.Trace.read_events(result.run_id, base_dir: trace_base_dir)

      assert Enum.any?(events, fn
               %{
                 "kind" => "effect",
                 "type" => "budget_charged",
                 "ref" => %{
                   "provider" => "fake",
                   "model" => "fake-local",
                   "source_type" => "STRUCTURED_OUTPUT",
                   "stream" => "stdout"
                 },
                 "payload" => %{
                   "billing_basis" => "provider_token_usage",
                   "tokens" => 10,
                   "input_tokens" => 7,
                   "output_tokens" => 3,
                   "usd" => 0.11,
                   "wall_ms" => 125
                 }
               } ->
                 true

               _ ->
                 false
             end)

      proof = Mitos.Proof.from_events(events)

      assert proof["metering"]["budget_charged"] == %{
               "tokens" => 10,
               "usd" => 0.11,
               "wall_ms" => 125
             }
    end

    test "local capability runs with materialized skill tools and data pillars", %{dir: dir} do
      previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)
      trace_base_dir = unique_tmp_dir("local-capability-runtime-trace")
      Application.put_env(:mitos, :trace_base_dir, trace_base_dir)

      on_exit(fn ->
        if previous_trace_base_dir do
          Application.put_env(:mitos, :trace_base_dir, previous_trace_base_dir)
        else
          Application.delete_env(:mitos, :trace_base_dir)
        end

        writable_rm_rf!(trace_base_dir)
      end)

      File.mkdir_p!(Path.join(dir, "tools"))
      File.mkdir_p!(Path.join(dir, "data"))

      File.write!(Path.join(dir, "skill.md"), """
      <!-- @skill:role -->
      Local pillar checker.
      <!-- @skill:rules -->
      Base local rule.
      <!-- @skill:procedure -->
      Read data and write RESULT through the tool.
      <!-- @skill:success -->
      RESULT and report.md exist.
      """)

      tool_path = Path.join([dir, "tools", "write_result.py"])

      File.write!(tool_path, """
      #!/usr/bin/env python3
      import json
      import os
      import pathlib

      skill = pathlib.Path(os.environ["MITOS_RESOLVED_SKILL"]).read_text()
      data = pathlib.Path(os.environ["MITOS_DATA_DIR"]) / "ref.txt"
      data_write_denied = False
      try:
          (pathlib.Path(os.environ["MITOS_DATA_DIR"]) / "mutate.txt").write_text("bad")
      except OSError:
          data_write_denied = True
      report = pathlib.Path("report.md")
      report.write_text(
          "skill_has_overlay=%s\\ndata=%s\\ndata_write_denied=%s\\n"
          % ("Overlay local rule." in skill, data.read_text().strip(), data_write_denied)
      )
      pathlib.Path("RESULT").write_text(json.dumps({
          "signal": "ship",
          "confidence": 1.0,
          "summary": report.read_text().strip(),
          "evidence": [{"kind": "report", "path": "report.md"}],
      }))
      """)

      File.chmod!(tool_path, 0o755)
      File.write!(Path.join([dir, "data", "ref.txt"]), "reference-corpus\n")

      write_workload!(dir, "local-pillars", %{
        provider: "fake",
        template: "use three pillars for {{objective}}",
        transport_order: ["stdio-json"],
        timeout_ms: 30_000,
        capability: %{
          name: "local-pillars",
          harnesses: ["fake"],
          trust_tier: "T1",
          skill: "skill.md",
          skill_adapt: %{
            fake: "@adapt append skill:rules Overlay local rule."
          },
          data: %{path: "data", mount: "/data", ro: true},
          tools: [
            %{
              name: "write_result",
              exec: "tools/write_result.py",
              input: %{},
              output: %{},
              deterministic: true,
              side_effects: ["workspace_write", "data_read"]
            }
          ],
          result: %{path: "RESULT", evidence: ["report.md"]},
          hooks: %{
            post: [
              %{name: "write-result", run: ["/bin/sh", "-c", "write_result.py"], on_fail: "block"}
            ]
          }
        }
      })

      {:ok, workload} = Workload.fetch("local-pillars")

      assert {:ok, result} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "backend smoke"})

      events = Mitos.Trace.read_events(result.run_id, base_dir: trace_base_dir)
      runtime_root = Path.join([trace_base_dir, ".runs", result.run_id, "runtime"])

      assert File.read!(Path.join([runtime_root, "skill", "resolved.md"])) =~
               "Overlay local rule."

      assert File.exists?(Path.join([runtime_root, "tools", "write_result.py"]))
      assert File.read!(Path.join([runtime_root, "data", "ref.txt"])) == "reference-corpus\n"
      refute File.exists?(Path.join([runtime_root, "data", "mutate.txt"]))

      assert %{"items" => [%{"runtime_exec" => runtime_exec}]} =
               Jason.decode!(File.read!(Path.join([runtime_root, "tools", "tools.json"])))

      assert runtime_exec == Path.join([runtime_root, "tools", "write_result.py"])
      assert read_only?(Path.join([runtime_root, "skill"]))
      assert read_only?(Path.join([runtime_root, "tools"]))
      assert read_only?(Path.join([runtime_root, "data"]))

      assert Enum.any?(events, fn
               %{
                 "kind" => "effect",
                 "type" => "hook_executed",
                 "ref" => %{"phase" => "post", "name" => "write-result"},
                 "payload" => %{"success?" => true}
               } ->
                 true

               _ ->
                 false
             end)

      assert Enum.any?(events, fn
               %{
                 "kind" => "effect",
                 "type" => "capability_finished",
                 "payload" => %{
                   "result" => %{
                     "signal" => "ship",
                     "summary" =>
                       "skill_has_overlay=True\ndata=reference-corpus\ndata_write_denied=True"
                   },
                   "harvest" => %{"result" => %{"evidence" => evidence}}
                 }
               } ->
                 Enum.any?(evidence, &(&1["path"] == "report.md" and is_binary(&1["sha256"])))

               _ ->
                 false
             end)

      assert Enum.any?(events, fn
               %{
                 "kind" => "decision",
                 "params" => %{
                   "capability_runtime" => %{
                     "workspace" => workspace,
                     "skill" => %{"sha256" => "sha256:" <> _}
                   }
                 }
               }
               when is_binary(workspace) ->
                 String.starts_with?(workspace, runtime_root)

               _ ->
                 false
             end)
    end

    test "local capability fails closed when declared skill file is missing", %{dir: dir} do
      write_workload!(dir, "missing-skill-local", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "missing-skill-local",
          harnesses: ["fake"],
          trust_tier: "T1",
          skill: "missing.md",
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("missing-skill-local")

      assert {:error, {:runtime_prepare_failed, {:skill_unavailable, "missing.md", :enoent}}} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "must fail"})
    end

    test "local capability fails closed when declared skill lacks required anchors", %{dir: dir} do
      File.write!(Path.join(dir, "skill.md"), """
      <!-- @skill:role -->
      Base role.
      <!-- @skill:rules -->
      Base rule.
      """)

      write_workload!(dir, "invalid-skill-local", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "invalid-skill-local",
          harnesses: ["fake"],
          trust_tier: "T1",
          skill: "skill.md",
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("invalid-skill-local")

      assert {:error,
              {:runtime_prepare_failed,
               {:skill_resolution_failed, "skill.md",
                {:missing_skill_anchors, ["skill:procedure", "skill:success"]}}}} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "must fail"})
    end

    test "local capability fails closed when declared data source is unavailable", %{dir: dir} do
      write_workload!(dir, "missing-data-local", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "missing-data-local",
          harnesses: ["fake"],
          trust_tier: "T1",
          data: %{path: "missing-data", mount: "/data", ro: true},
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("missing-data-local")

      assert {:error, {:runtime_prepare_failed, {:data_unavailable, "missing-data", :missing}}} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "must fail"})
    end

    test "local capability refuses data path escapes before launch", %{dir: dir} do
      write_workload!(dir, "data-escape-local", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "data-escape-local",
          harnesses: ["fake"],
          trust_tier: "T1",
          data: %{path: "../outside-data", mount: "/data", ro: true},
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("data-escape-local")

      assert {:error,
              {:runtime_prepare_failed, {:data_unavailable, "../outside-data", :path_escape}}} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "must fail"})
    end

    test "paid capability endpoint refuses local execution when no T2 provider is configured", %{
      dir: dir
    } do
      workspace = Path.join(dir, "paid-workspace")
      File.mkdir_p!(workspace)

      write_workload!(dir, "paid", %{
        provider: "fake",
        template: "{{objective}}",
        cwd: workspace,
        transport_order: ["stdio-json"],
        timeout_ms: 30_000,
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "paid-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"},
          hooks: %{
            post: [
              %{
                name: "write-result",
                run:
                  "printf '%s' '{\"signal\":\"ship\",\"summary\":\"paid result\",\"evidence\":[]}' > RESULT",
                on_fail: "block"
              }
            ]
          }
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        task: %{objective: "paid job"},
        request: %{}
      }

      before_runs = length(Mitos.Runs.list())
      conn = post_json("/capabilities/paid/runs", request)

      assert conn.status == 503
      assert length(Mitos.Runs.list()) == before_runs

      body = Jason.decode!(conn.resp_body)
      assert body["error"] == "no eligible sandbox provider"
      assert body["trust_tier"] == "T2"
      assert body["detail"]["reason"] =~ "remote microVM-class provider"
    end

    test "publication validation dry-runs a paid capability through T2 provider", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "validate-ok", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "validation-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{
            path: "RESULT",
            evidence: ["report.md"],
            schema: %{
              type: "object",
              required: ["signal"],
              properties: %{signal: %{enum: ["ship"]}, summary: %{type: "string"}}
            }
          }
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        task: %{objective: "publication validation evidence ok job"},
        request: %{}
      }

      conn = post_json("/capabilities/validate-ok/validate", request)
      assert conn.status == 200
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(conn.resp_body)
      assert body["schema_id"] == "gentix.capability_publication_validation"
      assert body["status"] == "passed"
      assert body["workload_id"] == "validate-ok"
      assert body["capability_ref"] =~ "cap:sha256:"
      assert body["dry_run"]["run_id"] =~ "wl-"
      assert body["dry_run"]["exit_code"] == 0
      refute Map.has_key?(body, "settlement")
      refute Map.has_key?(body["evidence"], "payment")

      assert Enum.any?(
               body["checks"],
               &(&1["name"] == "policy_compile" and &1["status"] == "passed")
             )

      assert %{"name" => "dry_run", "status" => "passed"} in body["checks"]
      assert %{"name" => "result_schema", "status" => "passed"} in body["checks"]

      assert Enum.any?(
               body["checks"],
               &(&1["name"] == "evidence" and &1["status"] == "passed" and
                   &1["declared"] == ["report.md"])
             )

      assert body["evidence"]["delivery"]["result"]["signal"] == "ship"

      assert Enum.any?(body["evidence"]["artifacts"], fn
               %{"kind" => "file", "path" => "report.md", "sha256" => "sha256:report"} -> true
               _ -> false
             end)

      assert body["evidence"]["proof"]["redaction"]["source"] == "effect_events_only"

      assert {:ok, run} = Mitos.Runs.fetch(body["dry_run"]["run_id"])
      assert run["manifest"]["trust_tier"] == "T2"

      pool = Mitos.WorkPool.fold_events(run["events"])
      [task] = Map.values(pool["tasks"])
      assert task["pool"] == "publication:validation"
      assert task["source"] == "publication:validation"
    end

    test "publication validation loads result.schema relative to workload package", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      File.write!(
        Path.join(dir, "result.schema.json"),
        Jason.encode!(%{
          type: "object",
          required: ["signal"],
          properties: %{signal: %{enum: ["ship"]}, summary: %{type: "string"}}
        })
      )

      write_workload!(dir, "validate-package-schema", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "validation-package-schema-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT", schema: "result.schema.json"}
        }
      })

      conn =
        post_json("/capabilities/validate-package-schema/validate", %{
          task: %{objective: "publication validation package schema job"},
          request: %{}
        })

      assert conn.status == 200
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(conn.resp_body)
      assert body["status"] == "passed"
      assert %{"name" => "result_schema", "status" => "passed"} in body["checks"]
      assert body["evidence"]["delivery"]["result"]["signal"] == "ship"
    end

    test "publication validation fails when declared egress credential is unregistered", %{
      dir: dir
    } do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      previous_vault = Application.get_env(:mitos, :key_vault)
      previous_leases = Application.get_env(:mitos, :key_vault_leases)

      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])
      Application.delete_env(:mitos, :key_vault)
      Application.delete_env(:mitos, :key_vault_leases)

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end

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

      write_workload!(dir, "validate-missing-key", %{
        provider: "fake",
        template: "{{objective}}",
        seller_id: "seller-a",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "validation-missing-key-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          egress_policy: [
            %{
              id: "provider-api",
              dest: "api.provider.test",
              credential_id: "provider-key"
            }
          ],
          result: %{
            path: "RESULT",
            schema: %{type: "object", required: ["signal"]}
          }
        }
      })

      conn =
        post_json("/capabilities/validate-missing-key/validate", %{
          task: %{objective: "publication validation missing key job"},
          request: %{}
        })

      assert conn.status == 422
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      body = Jason.decode!(conn.resp_body)
      assert body["status"] == "failed"
      assert body["dry_run"] == nil

      assert Enum.any?(body["checks"], fn
               %{
                 "name" => "key_bindings",
                 "status" => "failed",
                 "failures" => [
                   %{
                     "credential_id" => "provider-key",
                     "dest" => "api.provider.test",
                     "reason" => "credential unavailable"
                   }
                   | _
                 ]
               } ->
                 true

               _ ->
                 false
             end)
    end

    test "publication validation accepts seller-scoped registered egress credentials", %{
      dir: dir
    } do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      previous_vault = Application.get_env(:mitos, :key_vault)
      previous_leases = Application.get_env(:mitos, :key_vault_leases)

      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])
      Application.delete_env(:mitos, :key_vault_leases)

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end

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

      assert {:ok, _credential_ref} =
               Mitos.KeyVault.register(%{
                 seller_id: "seller-a",
                 credential_id: "provider-key",
                 type: "bearer",
                 destinations: ["api.provider.test"],
                 secret: "seller-a-secret"
               })

      write_workload!(dir, "validate-registered-key", %{
        provider: "fake",
        template: "{{objective}}",
        seller_id: "seller-a",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "validation-registered-key-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          egress_policy: [
            %{
              id: "provider-api",
              dest: "api.provider.test",
              credential_id: "provider-key"
            }
          ],
          result: %{
            path: "RESULT",
            schema: %{type: "object", required: ["signal"]}
          }
        }
      })

      conn =
        post_json("/capabilities/validate-registered-key/validate", %{
          task: %{objective: "publication validation registered key job"},
          request: %{}
        })

      assert conn.status == 200
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(conn.resp_body)
      assert body["status"] == "passed"
      assert %{"name" => "key_bindings", "status" => "passed", "checked" => 1} in body["checks"]
      refute inspect(body) =~ "seller-a-secret"
    end

    test "publication validation fails when declared evidence is not harvested", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "validate-missing-evidence", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "validation-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{
            path: "RESULT",
            evidence: ["report.md"],
            schema: %{
              type: "object",
              required: ["signal"],
              properties: %{signal: %{enum: ["ship"]}}
            }
          }
        }
      })

      conn =
        post_json("/capabilities/validate-missing-evidence/validate", %{
          task: %{objective: "publication validation job"},
          request: %{}
        })

      assert conn.status == 422
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(conn.resp_body)
      assert body["status"] == "failed"

      assert Enum.any?(body["checks"], fn
               %{
                 "name" => "evidence",
                 "status" => "failed",
                 "detail" => "declared evidence was not content-addressed by harvest",
                 "missing" => ["report.md"]
               } ->
                 true

               _ ->
                 false
             end)
    end

    test "publication validation fails when result schema is missing", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "validate-no-schema", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "validation-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"}
        }
      })

      conn =
        post_json("/capabilities/validate-no-schema/validate", %{
          task: %{objective: "publication validation job"},
          request: %{}
        })

      assert conn.status == 422
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(conn.resp_body)
      assert body["status"] == "failed"

      assert Enum.any?(body["checks"], fn
               %{
                 "name" => "result_schema",
                 "status" => "failed",
                 "detail" => "capability result.schema is required for publication validation"
               } ->
                 true

               _ ->
                 false
             end)
    end

    test "publication validation fails when dry-run RESULT violates schema", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "validate-bad-schema", %{
        provider: "fake",
        template: "{{objective}}",
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "validation-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{
            path: "RESULT",
            schema: %{
              type: "object",
              required: ["signal"],
              properties: %{signal: %{enum: ["ship"]}}
            }
          }
        }
      })

      conn =
        post_json("/capabilities/validate-bad-schema/validate", %{
          task: %{objective: "schema invalid publication validation job"},
          request: %{}
        })

      assert conn.status == 422
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(conn.resp_body)
      assert body["status"] == "failed"

      assert Enum.any?(body["checks"], fn
               %{
                 "name" => "result_schema",
                 "status" => "failed",
                 "schema_error" => schema_error
               } ->
                 schema_error =~ "RESULT.signal"

               _ ->
                 false
             end)

      assert body["evidence"]["delivery"]["result"]["synthetic"]
      assert "result_schema_invalid" in body["evidence"]["delivery"]["result"]["needs"]
    end

    test "paid capability endpoint executes through configured remote T2 provider", %{dir: dir} do
      workspace = Path.join(dir, "paid-remote-workspace")
      File.mkdir_p!(workspace)

      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "paid-remote", %{
        provider: "fake",
        template: "{{objective}}",
        seller_id: "seller-a",
        cwd: workspace,
        env: %{"MODE" => "must-not-enter-t2"},
        timeout_ms: 30_000,
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "paid-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"}
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        tenant: %{org: "buyer-org", project: "buyer-project", actor: "buyer-user"},
        buyer_workspace: %{
          snapshot: "snap://buyer/input-designs",
          mount: "/workspace",
          ro: true,
          sha256: "sha256:buyer-inputs"
        },
        task: %{objective: "paid remote job"},
        request: %{}
      }

      invalid_workspace_conn =
        post_json(
          "/capabilities/paid-remote/runs",
          Map.put(request, :buyer_workspace, %{path: workspace, ro: true})
        )

      assert invalid_workspace_conn.status == 422
      invalid_workspace_body = Jason.decode!(invalid_workspace_conn.resp_body)
      assert invalid_workspace_body["error"] == "invalid request"
      assert invalid_workspace_body["detail"] =~ "provider snapshot or volume"
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      invalid_mount_conn =
        post_json(
          "/capabilities/paid-remote/runs",
          Map.put(request, :buyer_workspace, %{
            snapshot: "snap://buyer/input-designs",
            mount: "/data",
            ro: true
          })
        )

      assert invalid_mount_conn.status == 422
      invalid_mount_body = Jason.decode!(invalid_mount_conn.resp_body)
      assert invalid_mount_body["error"] == "invalid request"
      assert invalid_mount_body["detail"] =~ "/workspace"
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      before_quote_runs = length(Mitos.Runs.list())
      quote_conn = post_json("/capabilities/paid-remote/runs", request)
      assert quote_conn.status == 402
      assert length(Mitos.Runs.list()) == before_quote_runs
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      quote = Jason.decode!(quote_conn.resp_body)

      invalid_payment =
        Jason.encode!(%{
          quote_id: quote["quote_id"],
          amount_usdc: "1.00",
          currency: "USDC",
          pay_to: quote["pay_to"],
          status: "settled",
          settlement_tx: "usdc-tx-underpaid"
        })

      before_invalid_runs = length(Mitos.Runs.list())

      invalid_conn =
        post_json("/capabilities/paid-remote/runs", request, [{"x-payment", invalid_payment}])

      assert invalid_conn.status == 402
      assert length(Mitos.Runs.list()) == before_invalid_runs
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      invalid_body = Jason.decode!(invalid_conn.resp_body)
      assert invalid_body["error"] == "amount is below quote"

      payment =
        Jason.encode!(%{
          quote_id: quote["quote_id"],
          amount_usdc: "2.50",
          currency: "USDC",
          pay_to: quote["pay_to"],
          status: "settled",
          settlement_tx: "usdc-tx-remote"
        })

      paid_conn = post_json("/capabilities/paid-remote/runs", request, [{"x-payment", payment}])
      assert paid_conn.status == 200
      assert_receive {:test_remote_microvm_env, nil}
      assert_receive {:test_remote_microvm_launch_spec, launch_spec}
      assert launch_spec.seller_id == "seller-a"
      refute Map.has_key?(launch_spec, :cwd)

      assert launch_spec.workspace == %{
               "snapshot" => "snap://buyer/input-designs",
               "mount" => "/workspace",
               "ro" => true,
               "sha256" => "sha256:buyer-inputs"
             }

      assert launch_spec.posture["trust_tier"] == "T2"
      assert launch_spec.posture["net_mode"] == "deny_all_except_broker"
      assert launch_spec.posture["egress"] == "broker"
      assert launch_spec.posture["host_network"] == false
      assert launch_spec.posture["loopback_bypass"] == false
      assert launch_spec.posture["broker_endpoint"] == "https://broker.test/proxy"
      assert launch_spec.posture["ca_ref"] == "ca:mitos:test"
      assert launch_spec.posture["data_mount"] == "snapshot"
      assert launch_spec.posture["metering"] == "billing_ledger"
      assert launch_spec.posture["metered_dimensions"] == ["cpu", "mem", "wall", "egress"]
      assert launch_spec.posture["workspace"]["host_bind"] == false
      assert launch_spec.posture["data"]["mode"] == "ro"

      body = Jason.decode!(paid_conn.resp_body)
      assert body["deliverable"]["summary"] == "remote result"
      assert body["deliverable"]["result"]["signal"] == "ship"
      assert body["deliverable"]["result"]["sha256"] == "sha256:result"
      refute inspect(body["deliverable"]) =~ "REMOTE_FINAL"
      assert body["evidence"]["delivery"]["result"]["signal"] == "ship"
      assert body["evidence"]["delivery"]["result"]["sha256"] == "sha256:result"
      refute Map.has_key?(body, "model")

      assert body["evidence"]["artifacts"] == [
               %{"kind" => "file", "path" => "RESULT", "sha256" => "sha256:result"}
             ]

      assert body["evidence"]["proof"]["metering"]["budget_charged_units"] == 1
      assert body["settlement"]["status"] == "settled"
      assert body["settlement"]["settlement_tx"] == "usdc-tx-remote"

      assert {:ok, run} = Mitos.Runs.fetch(body["execution_id"])
      assert run["manifest"]["org_id"] == "buyer-org"
      assert run["manifest"]["project_id"] == "buyer-project"
      assert run["manifest"]["actor_id"] == "buyer-user"
      assert run["manifest"]["trust_tier"] == "T2"
      assert run["proof"]["delivery"]["result"]["signal"] == "ship"

      assert Enum.all?(run["events"], fn
               %{"kind" => kind, "tenant" => tenant} when kind in ["decision", "effect"] ->
                 tenant == %{
                   "org_id" => "buyer-org",
                   "project_id" => "buyer-project",
                   "actor_id" => "buyer-user"
                 }

               _event ->
                 true
             end)

      pool = Mitos.WorkPool.fold_events(run["events"])
      [task] = Map.values(pool["tasks"])
      assert task["status"] == "done"
      assert task["pool"] == "paid:default"
      assert task["source"] == "purchase:#{quote["quote_id"]}"

      assert task["tenant"] == %{
               "org" => "buyer-org",
               "project" => "buyer-project",
               "actor" => "buyer-user"
             }
    end

    test "paid T2 capability endpoint fails before provider start when egress credential is missing",
         %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      previous_vault = Application.get_env(:mitos, :key_vault)
      previous_leases = Application.get_env(:mitos, :key_vault_leases)

      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])
      Application.delete_env(:mitos, :key_vault)
      Application.delete_env(:mitos, :key_vault_leases)

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end

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

      write_workload!(dir, "paid-remote-missing-key", %{
        provider: "fake",
        template: "{{objective}}",
        seller_id: "seller-a",
        timeout_ms: 30_000,
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "paid-missing-key-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          egress_policy: [
            %{id: "provider-api", dest: "api.provider.test", credential_id: "provider-key"}
          ],
          result: %{path: "RESULT"}
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        tenant: %{org: "buyer-org", project: "buyer-project", actor: "buyer-user"},
        buyer_workspace: %{
          snapshot: "snap://buyer/input-designs",
          mount: "/workspace",
          ro: true,
          sha256: "sha256:buyer-inputs"
        },
        task: %{objective: "paid remote missing key job"},
        request: %{}
      }

      quote_conn = post_json("/capabilities/paid-remote-missing-key/runs", request)
      assert quote_conn.status == 402
      quote = Jason.decode!(quote_conn.resp_body)

      payment =
        Jason.encode!(%{
          quote_id: quote["quote_id"],
          amount_usdc: "2.50",
          currency: "USDC",
          pay_to: quote["pay_to"],
          status: "settled",
          settlement_tx: "usdc-tx-missing-key"
        })

      paid_conn =
        post_json("/capabilities/paid-remote-missing-key/runs", request, [
          {"x-payment", payment}
        ])

      assert paid_conn.status == 502
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      body = Jason.decode!(paid_conn.resp_body)
      assert body["error"] == "agent launch failed"
      assert body["detail"] =~ "credential_lease_failed"
      assert body["detail"] =~ "provider-key"
      assert body["detail"] =~ "api.provider.test"
      assert body["settlement"]["status"] == "refund_due"
      assert Application.get_env(:mitos, :key_vault_leases, %{})[body["run_id"]] in [nil, []]
    end

    test "paid capability endpoint rejects buyer tenant matching seller tenant", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "paid-same-tenant", %{
        provider: "fake",
        template: "{{objective}}",
        seller_id: "seller-a",
        timeout_ms: 30_000,
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "paid-same-tenant-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"}
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        tenant: %{org: "seller-a", project: "buyer-project", actor: "buyer-user"},
        task: %{objective: "paid same tenant job"},
        request: %{}
      }

      before_runs = length(Mitos.Runs.list())
      conn = post_json("/capabilities/paid-same-tenant/runs", request)

      assert conn.status == 422
      assert length(Mitos.Runs.list()) == before_runs
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      body = Jason.decode!(conn.resp_body)
      assert body["error"] == "buyer and seller tenants must be distinct"
      assert body["seller_id"] == "seller-a"
    end

    test "metered paid capability endpoint refunds unused escrow from proof units", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "paid-metered-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        pricing: %{
          type: "metered",
          amount_usdc: "5.00",
          base_usdc: "0.25",
          unit_prices_usdc: %{token: "0.01"},
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "paid-metered-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"}
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        task: %{objective: "token usage paid remote job"},
        request: %{}
      }

      quote_conn = post_json("/capabilities/paid-metered-remote/runs", request)
      assert quote_conn.status == 402

      quote = Jason.decode!(quote_conn.resp_body)
      assert quote["amount_usdc"] == "5.00"

      payment =
        Jason.encode!(%{
          quote_id: quote["quote_id"],
          amount_usdc: "5.00",
          currency: "USDC",
          pay_to: quote["pay_to"],
          status: "settled",
          settlement_tx: "usdc-tx-metered-remote"
        })

      paid_conn =
        post_json("/capabilities/paid-metered-remote/runs", request, [{"x-payment", payment}])

      assert paid_conn.status == 200
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(paid_conn.resp_body)
      assert body["deliverable"]["summary"] == "remote result"
      assert body["deliverable"]["result"]["signal"] == "ship"
      refute inspect(body["deliverable"]) =~ "REMOTE_FINAL"
      assert body["evidence"]["proof"]["metering"]["budget_charged_units"] == 2
      assert body["evidence"]["proof"]["metering"]["budget_charged"]["tokens"] == 15

      assert body["settlement"]["status"] == "settled"
      assert body["settlement"]["escrowed_usdc"] == "5.000000"
      assert body["settlement"]["buyer_charge_usdc"] == "0.400000"
      assert body["settlement"]["refund_due_usdc"] == "4.600000"
      assert body["settlement"]["platform_take_usdc"] == "0.060000"
      assert body["settlement"]["seller_net_usdc"] == "0.340000"

      assert {:ok, _run} = Mitos.Runs.fetch(body["execution_id"])
      quote_id = quote["quote_id"]

      assert Enum.any?(Mitos.Trace.read_events(body["execution_id"]), fn
               %{
                 "kind" => "effect",
                 "type" => "budget_charged",
                 "ref" => %{"quote_id" => ^quote_id, "pricing_type" => "metered"},
                 "payload" => %{
                   "billing_basis" => "metered_price",
                   "amount_usdc" => "0.400000"
                 }
               } ->
                 true

               _ ->
                 false
             end)
    end

    test "paid capability endpoint settles refund when no deliverable is produced", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "paid-empty-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          compute_surcharge_usdc: "0.15",
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "paid-empty-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"}
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        task: %{objective: "empty output paid remote job"},
        request: %{}
      }

      quote_conn = post_json("/capabilities/paid-empty-remote/runs", request)
      assert quote_conn.status == 402
      quote = Jason.decode!(quote_conn.resp_body)

      payment =
        Jason.encode!(%{
          quote_id: quote["quote_id"],
          amount_usdc: "2.50",
          currency: "USDC",
          pay_to: quote["pay_to"],
          status: "settled",
          settlement_tx: "usdc-tx-empty-remote"
        })

      paid_conn =
        post_json("/capabilities/paid-empty-remote/runs", request, [{"x-payment", payment}])

      assert paid_conn.status == 502
      assert_receive {:test_remote_microvm_env, nil}

      body = Jason.decode!(paid_conn.resp_body)
      assert body["error"] == "workload produced no output"
      assert body["run_id"] =~ "wl-"
      assert body["settlement"]["status"] == "refund_due"
      assert body["settlement"]["buyer_charge_usdc"] == "0.150000"
      assert body["settlement"]["refund_due_usdc"] == "2.350000"
      assert body["settlement"]["platform_take_usdc"] == "0.000000"
      assert body["settlement"]["seller_net_usdc"] == "0.000000"
      assert body["evidence"]["proof"]["delivery"]["result"]["synthetic"]
      assert body["evidence"]["proof"]["metering"]["budget_charged"]["usd"] == 0.15

      quote_id = quote["quote_id"]

      assert Enum.any?(Mitos.Trace.read_events(body["run_id"]), fn
               %{
                 "kind" => "effect",
                 "type" => "budget_charged",
                 "ref" => %{"quote_id" => ^quote_id},
                 "payload" => %{"amount_usdc" => "0.150000"}
               } ->
                 true

               _ ->
                 false
             end)
    end

    test "paid capability endpoint settles refund when remote launch fails", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "paid-launch-fail-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        pricing: %{
          type: "flat",
          amount_usdc: "2.50",
          compute_surcharge_usdc: "0.15",
          take_rate_bps: 1500,
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "paid-launch-fail-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"}
        }
      })

      request = %{
        schema_id: "gentix.provider_execution_request",
        schema_version: 1,
        task: %{objective: "prepare failure paid remote job"},
        request: %{}
      }

      quote_conn = post_json("/capabilities/paid-launch-fail-remote/runs", request)
      assert quote_conn.status == 402
      quote = Jason.decode!(quote_conn.resp_body)

      payment =
        Jason.encode!(%{
          quote_id: quote["quote_id"],
          amount_usdc: "2.50",
          currency: "USDC",
          pay_to: quote["pay_to"],
          status: "settled",
          settlement_tx: "usdc-tx-launch-fail-remote"
        })

      paid_conn =
        post_json("/capabilities/paid-launch-fail-remote/runs", request, [{"x-payment", payment}])

      assert paid_conn.status == 502
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}

      body = Jason.decode!(paid_conn.resp_body)
      assert body["error"] == "agent launch failed"
      assert body["detail"] == ":prepare_failed"
      assert body["run_id"] =~ "wl-"
      assert body["settlement"]["status"] == "refund_due"
      assert body["settlement"]["buyer_charge_usdc"] == "0.150000"
      assert body["settlement"]["refund_due_usdc"] == "2.350000"
      assert body["evidence"]["proof"]["delivery"]["result"] == nil
      assert body["evidence"]["proof"]["metering"]["budget_charged"]["usd"] == 0.15

      assert {:ok, run} = Mitos.Runs.fetch(body["run_id"])
      assert run["proof"]["delivery"]["result"] == nil
    end

    test "remote session cleanup runs when harvest fails after start", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      previous_leases = Application.get_env(:mitos, :key_vault_leases)

      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])
      Application.delete_env(:mitos, :key_vault_leases)

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end

        if previous_leases do
          Application.put_env(:mitos, :key_vault_leases, previous_leases)
        else
          Application.delete_env(:mitos, :key_vault_leases)
        end
      end)

      write_workload!(dir, "remote-harvest-fail", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "remote-harvest-fail-demo",
          harnesses: ["fake"],
          trust_tier: "T2",
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("remote-harvest-fail")

      assert {:error, {:launch_failed, run_id, :harvest_failed}} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "harvest failure job"})

      assert_receive {:test_remote_microvm_env, nil}
      assert_receive {:test_remote_microvm_launch_spec, %{run_id: ^run_id}}
      assert_receive {:test_remote_microvm_stopped, ^run_id}

      refute Map.has_key?(Application.get_env(:mitos, :key_vault_leases, %{}), run_id)

      assert {:ok, run} = Mitos.Runs.fetch(run_id)
      pool = Mitos.WorkPool.fold_events(run["events"])
      [task] = Map.values(pool["tasks"])
      assert task["status"] == "failed"

      assert Enum.any?(run["events"], fn
               %{
                 "kind" => "effect",
                 "type" => "task_failed",
                 "payload" => %{"reason" => reason}
               } ->
                 reason =~ ":harvest_failed"

               _ ->
                 false
             end)
    end

    test "remote workload timeout interrupts, harvests partial result, and stops", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)
      previous_leases = Application.get_env(:mitos, :key_vault_leases)
      trace_base_dir = unique_tmp_dir("remote-timeout-trace")

      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])
      Application.put_env(:mitos, :trace_base_dir, trace_base_dir)
      Application.delete_env(:mitos, :key_vault_leases)

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end

        if previous_trace_base_dir do
          Application.put_env(:mitos, :trace_base_dir, previous_trace_base_dir)
        else
          Application.delete_env(:mitos, :trace_base_dir)
        end

        if previous_leases do
          Application.put_env(:mitos, :key_vault_leases, previous_leases)
        else
          Application.delete_env(:mitos, :key_vault_leases)
        end

        File.rm_rf!(trace_base_dir)
      end)

      write_workload!(dir, "remote-timeout", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 50,
        capability: %{
          name: "remote-timeout-demo",
          harnesses: ["fake"],
          trust_tier: "T2",
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("remote-timeout")

      assert {:error, {:timeout, run_id}} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "timeout job"})

      assert_receive {:test_remote_microvm_env, nil}
      assert_receive {:test_remote_microvm_launch_spec, %{run_id: ^run_id}}
      assert_receive {:test_remote_microvm_control, ^run_id, {:steer, :interrupt, opts}}
      assert opts[:reason] == "timeout"
      assert opts[:timeout_ms] == 50
      assert_receive {:test_remote_microvm_stopped, ^run_id}

      refute Map.has_key?(Application.get_env(:mitos, :key_vault_leases, %{}), run_id)

      events = Mitos.Trace.read_events(run_id, base_dir: trace_base_dir)
      pool = Mitos.WorkPool.fold_events(events)
      [task] = Map.values(pool["tasks"])

      assert task["status"] == "failed"
      assert task["status_reason"] =~ ":timeout"

      assert Enum.any?(events, fn
               %{
                 "kind" => "effect",
                 "type" => "capability_interrupted",
                 "payload" => %{
                   "reason" => "timeout",
                   "timeout_ms" => 50,
                   "control" => %{"status" => "sent"}
                 }
               } ->
                 true

               _ ->
                 false
             end)

      assert Enum.any?(events, fn
               %{
                 "kind" => "effect",
                 "type" => "capability_finished",
                 "payload" => %{
                   "status" => "timeout",
                   "exit_code" => nil,
                   "harvest" => %{"result" => %{"signal" => "ship"}}
                 }
               } ->
                 true

               _ ->
                 false
             end)
    end

    test "remote token usage is folded into canonical budget charges", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)
      trace_base_dir = unique_tmp_dir("remote-token-usage-trace")

      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])
      Application.put_env(:mitos, :trace_base_dir, trace_base_dir)

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end

        if previous_trace_base_dir do
          Application.put_env(:mitos, :trace_base_dir, previous_trace_base_dir)
        else
          Application.delete_env(:mitos, :trace_base_dir)
        end

        File.rm_rf!(trace_base_dir)
      end)

      write_workload!(dir, "usage-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "usage-demo",
          harnesses: ["fake"],
          trust_tier: "T2",
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("usage-remote")

      assert {:ok, result} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "multi token usage job"})

      events = Mitos.Trace.read_events(result.run_id, base_dir: trace_base_dir)

      assert Enum.any?(events, fn
               %{
                 "kind" => "effect",
                 "type" => "budget_charged",
                 "ref" => %{
                   "provider" => "fake",
                   "model" => "gpt-test",
                   "source_type" => "token_usage"
                 },
                 "payload" => %{
                   "billing_basis" => "provider_token_usage",
                   "tokens" => 25,
                   "input_tokens" => 17,
                   "output_tokens" => 8,
                   "usd" => 0.4,
                   "wall_ms" => 1250
                 }
               } ->
                 true

               _ ->
                 false
             end)

      proof = Mitos.Proof.from_events(events)

      assert proof["metering"]["budget_charged_units"] == 1

      assert proof["metering"]["budget_charged"] == %{
               "tokens" => 25,
               "usd" => 0.4,
               "wall_ms" => 1250
             }
    end

    test "drains a service-produced task into workload dispatch", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "service-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        pricing: %{
          type: "flat",
          amount_usdc: "1.00",
          take_floor_usdc: "0.01",
          payout_address: "usdc:seller:demo"
        },
        capability: %{
          name: "service-demo",
          harnesses: ["fake"],
          trust_tier: "T1",
          result: %{path: "RESULT"}
        }
      })

      base_dir = Path.join(dir, "trace")
      trace = opened_dispatch_trace(base_dir, "run-service-drain")

      {trace, task} =
        Mitos.ServiceRuntime.append_trigger(
          trace,
          %{
            id: "service-remote-listener",
            scope: "project",
            execution: %{type: "pipeline", pipeline: "workload:service-remote"},
            pool: "paid:default"
          },
          %{type: "webhook", delivery_id: "evt-service", inputs: %{objective: "service job"}},
          caused_by: 0,
          task_id: "svc-task-1"
        )

      {:ok, drained_task, result, _trace} =
        Mitos.WorkPool.drain_once(
          trace,
          "paid:default",
          &Mitos.Workload.Runner.dispatch_task/1,
          caused_by: 0
        )

      assert drained_task["id"] == task["id"]
      assert result["workload_id"] == "service-remote"
      assert result["summary"] == "REMOTE_FINAL: service job"
      assert result["run_id"] =~ "wl-"
      assert_receive {:test_remote_microvm_env, nil}

      assert {:ok, run} = Mitos.Runs.fetch(result["run_id"])
      pool = Mitos.WorkPool.fold_events(run["events"])
      run_task = get_in(pool, ["tasks", "svc-task-1"])

      assert run_task["status"] == "done"
      assert run_task["source"] == "service:service-remote-listener:webhook"
      assert run_task["pool"] == "paid:default"
    end

    test "injects tenant-filtered memory slice into remote launch spec", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      write_workload!(dir, "memory-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "memory-demo",
          harnesses: ["fake"],
          trust_tier: "T2",
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("memory-remote")

      tenant_obs =
        Mitos.Memory.observation(%{
          id: "obs-tenant",
          scope: %{tenant_id: "org-a", capability_ref: workload.capability.id, harness: "fake"},
          kind: "fix_strategy",
          title: "Prefer small batches",
          body: "Small batches work best for this buyer workload.",
          claim: %{subject: "batch-size", assertion: "small"},
          evidence: ["run-a"],
          confidence: 0.9
        })

      other_obs =
        Mitos.Memory.observation(%{
          id: "obs-other",
          scope: %{tenant_id: "org-b", capability_ref: workload.capability.id, harness: "fake"},
          kind: "gotcha",
          title: "Other tenant note",
          body: "This note belongs to another tenant.",
          claim: %{subject: "tenant", assertion: "other"},
          evidence: ["run-b"],
          confidence: 1.0
        })

      memory_state = %{
        "stats" => %{},
        "supersessions" => [],
        "observations" => %{"obs-tenant" => tenant_obs, "obs-other" => other_obs}
      }

      assert {:ok, result} =
               Mitos.Workload.Runner.execute(
                 workload,
                 %{"objective" => "batch this data"},
                 tenant: %{"org" => "org-a", "project" => "proj-a"},
                 memory_state: memory_state
               )

      assert result.result == "REMOTE_FINAL: batch this data"
      assert_receive {:test_remote_microvm_env, nil}
      assert_receive {:test_remote_microvm_launch_spec, launch_spec}

      assert launch_spec.knowledge["tenant_id"] == "org-a"
      assert launch_spec.knowledge["body"] =~ "Prefer small batches"
      refute launch_spec.knowledge["body"] =~ "Other tenant note"
    end

    test "resolves capability skill overlays into remote launch spec and trace", %{dir: dir} do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      previous_trace_base_dir = Application.get_env(:mitos, :trace_base_dir)
      trace_base_dir = unique_tmp_dir("resolved-skill-trace")

      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])
      Application.put_env(:mitos, :trace_base_dir, trace_base_dir)

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end

        if previous_trace_base_dir do
          Application.put_env(:mitos, :trace_base_dir, previous_trace_base_dir)
        else
          Application.delete_env(:mitos, :trace_base_dir)
        end

        File.rm_rf!(trace_base_dir)
      end)

      File.write!(Path.join(dir, "skill.md"), """
      <!-- @skill:role -->
      Base role.
      <!-- @skill:rules -->
      Base rule.
      <!-- @skill:procedure -->
      Step one.
      <!-- @skill:success -->
      Ship evidence.
      """)

      write_workload!(dir, "skill-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "skill-demo",
          harnesses: ["fake"],
          trust_tier: "T2",
          skill: "skill.md",
          skill_adapt: %{
            fake: "@adapt append skill:rules Overlay rule."
          },
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("skill-remote")

      assert {:ok, result} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "resolve skill"})

      assert_receive {:test_remote_microvm_env, nil}
      assert_receive {:test_remote_microvm_launch_spec, launch_spec}

      assert %{
               "path" => "/skill/resolved.md",
               "source" => "skill.md",
               "sha256" => "sha256:" <> _,
               "body" => body
             } = launch_spec.resolved_skill

      assert body =~ "Base rule.\nOverlay rule."
      refute Map.has_key?(launch_spec.resolved_skill, "source_path")

      events = Mitos.Trace.read_events(result.run_id, base_dir: trace_base_dir)
      expected_sha = launch_spec.resolved_skill["sha256"]

      assert Enum.any?(events, fn
               %{
                 "kind" => "effect",
                 "type" => "capability_started",
                 "payload" => %{
                   "resolved_skill" => %{
                     "path" => "/skill/resolved.md",
                     "source" => "skill.md",
                     "sha256" => ^expected_sha
                   }
                 }
               } ->
                 true

               _event ->
                 false
             end)

      started =
        Enum.find(
          events,
          &(Map.get(&1, "kind") == "effect" and Map.get(&1, "type") == "capability_started")
        )

      refute inspect(started) =~ "Base rule"
      refute inspect(started) =~ "Overlay rule"

      proof = Mitos.Proof.build(result.run_id, base_dir: trace_base_dir)
      refute inspect(proof) =~ "Base rule"
      refute inspect(proof) =~ "Overlay rule"
    end

    test "remote capability fails before provider start when declared skill overlay is invalid",
         %{
           dir: dir
         } do
      previous_providers = Application.get_env(:mitos, :sandbox_providers)
      Application.put_env(:mitos, :sandbox_providers, [Mitos.TestRemoteMicrovmProvider])

      on_exit(fn ->
        if previous_providers do
          Application.put_env(:mitos, :sandbox_providers, previous_providers)
        else
          Application.delete_env(:mitos, :sandbox_providers)
        end
      end)

      File.write!(Path.join(dir, "skill.md"), """
      <!-- @skill:role -->
      Base role.
      <!-- @skill:rules -->
      Base rule.
      <!-- @skill:procedure -->
      Step one.
      <!-- @skill:success -->
      Ship evidence.
      """)

      write_workload!(dir, "invalid-skill-remote", %{
        provider: "fake",
        template: "{{objective}}",
        timeout_ms: 30_000,
        capability: %{
          name: "invalid-skill-remote",
          harnesses: ["fake"],
          trust_tier: "T2",
          skill: "skill.md",
          skill_adapt: %{
            fake: "@adapt append skill:missing Overlay rule."
          },
          result: %{path: "RESULT"}
        }
      })

      {:ok, workload} = Workload.fetch("invalid-skill-remote")

      assert {:error,
              {:launch_failed, run_id,
               {:skill_resolution_failed, "skill.md", {:adapt_anchor_not_found, "skill:missing"}}}} =
               Mitos.Workload.Runner.execute(workload, %{"objective" => "must fail"})

      assert String.starts_with?(run_id, "wl-")
      refute_receive {:test_remote_microvm_env, _}
      refute_receive {:test_remote_microvm_launch_spec, _}
    end

    test "missing bindings yield 422", %{dir: dir} do
      write_workload!(dir, "needs-input", %{provider: "fake", template: "{{custom_field}}"})

      conn = post_json("/workloads/needs-input/execute", %{task: %{objective: "x"}})
      assert conn.status == 422
      assert Jason.decode!(conn.resp_body)["missing"] == ["custom_field"]
    end

    test "unknown workload yields 404" do
      conn = post_json("/workloads/missing/execute", %{})
      assert conn.status == 404
    end
  end

  defp opened_dispatch_trace(base_dir, run_id) do
    File.mkdir_p!(base_dir)

    trace =
      Mitos.Trace.open!(
        run_id: run_id,
        base_dir: base_dir,
        launch_spec: %{"provider" => "service-runtime", "task" => "drain"}
      )

    Mitos.Trace.append_decision(trace, %{
      actor: "orchestrator",
      type: "advance",
      target: "service-runtime",
      params: %{},
      inputs_hash: "sha256:service-drain",
      rationale: "drain service task",
      budget: %{"tokens" => 0, "usd" => 0, "wall_ms" => 0, "remaining" => %{}}
    })
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end

  defp read_only?(path) do
    match?({:ok, %{mode: mode}} when Bitwise.band(mode, 0o222) == 0, File.stat(path))
  end

  defp writable_rm_rf!(path) do
    make_tree_writable(path)
    File.rm_rf(path)
  end

  defp make_tree_writable(path) do
    cond do
      File.dir?(path) ->
        File.chmod(path, 0o755)

        path
        |> File.ls!()
        |> Enum.each(fn entry -> make_tree_writable(Path.join(path, entry)) end)

        File.chmod(path, 0o755)

      File.exists?(path) ->
        File.chmod(path, 0o644)

      true ->
        :ok
    end
  end

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)
end
