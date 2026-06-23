defmodule Mitos.CapabilitySetupView do
  @moduledoc """
  Operator-facing setup/readiness projection for the smallest runnable unit.

  A capability is still executed through the workload endpoint, but this view
  exposes the mechanics intentionally hidden from buyer-safe workload listings:
  provider, transport, definition file, env key presence, and sandbox posture.
  Secret values are never included.
  """

  @active_statuses ~w(blocked queued scheduled running parked)

  @doc "Return setup and readiness details for all workload-backed capabilities."
  def all do
    tasks = work_pool_tasks()
    capabilities = Enum.map(Mitos.Workload.list(), &capability_view(&1, tasks))

    %{
      "workloads_dir" => Mitos.Workload.dir(),
      "execute_endpoint" => "/workloads/:id/execute",
      "ui_setup" => %{
        "definition_route" => "#/capabilities/new",
        "definition_in_ui" => true,
        "host_env_external" => true
      },
      "capabilities" => capabilities,
      "totals" => %{
        "capabilities" => length(capabilities),
        "runnable" => Enum.count(capabilities, &get_in(&1, ["setup", "can_run"])),
        "with_runs" => Enum.count(capabilities, &(get_in(&1, ["work", "tasks", "total"]) > 0)),
        "needs_setup" => Enum.count(capabilities, &(get_in(&1, ["setup", "status"]) != "ready"))
      }
    }
  end

  @doc "Return one capability setup view by workload id."
  def get(id) when is_binary(id) do
    with {:ok, workload} <- Mitos.Workload.fetch(id) do
      {:ok, capability_view(workload, work_pool_tasks())}
    end
  end

  def get(_id), do: {:error, :not_found}

  defp capability_view(%Mitos.Workload{} = workload, tasks) do
    public = Mitos.Workload.public_view(workload)
    sandbox = sandbox_view(workload)
    env = env_view(workload)
    blockers = blockers(env, sandbox)

    public
    |> Map.merge(%{
      "kind" => "workload_capability",
      "template" => workload.template,
      "runtime" => runtime_view(workload),
      "capability" => capability_detail(workload),
      "files" => files_view(workload),
      "env" => env,
      "sandbox" => sandbox,
      "work" => work_view(workload, tasks),
      "setup" => %{
        "status" => setup_status(blockers),
        "can_run" => blockers == [],
        "blocking" => blockers
      },
      "editable" => true
    })
  end

  defp work_pool_tasks do
    Mitos.WorkPoolView.board()["tasks"] || []
  end

  defp work_view(%Mitos.Workload{} = workload, tasks) do
    pipeline_id = "workload:#{workload.id}"
    workload_tasks = Enum.filter(tasks, &(&1["pipeline"] == pipeline_id))

    recent_tasks =
      Enum.sort_by(workload_tasks, &(&1["updated_at"] || &1["created_at"] || &1["id"]), :desc)

    %{
      "pipeline_id" => pipeline_id,
      "tasks" => %{
        "total" => length(workload_tasks),
        "active" => Enum.count(workload_tasks, &(&1["status"] in @active_statuses)),
        "done" => Enum.count(workload_tasks, &(&1["status"] == "done")),
        "failed" => Enum.count(workload_tasks, &(&1["status"] == "failed"))
      },
      "recent_runs" =>
        recent_tasks
        |> Enum.flat_map(&(&1["runs"] || []))
        |> Enum.uniq()
        |> Enum.take(5),
      "recent_tasks" =>
        recent_tasks
        |> Enum.take(5)
        |> Enum.map(&recent_task/1)
    }
  end

  defp recent_task(task) do
    %{
      "id" => task["id"],
      "status" => task["status"],
      "pool" => task["pool"],
      "source" => task["source"],
      "runs" => task["runs"] || [],
      "tenant" => task["tenant"] || %{},
      "created_at" => task["created_at"],
      "updated_at" => task["updated_at"],
      "input_keys" => task |> get_in(["payload", "inputs"]) |> input_keys()
    }
  end

  defp input_keys(%{} = inputs), do: inputs |> Map.keys() |> Enum.sort()
  defp input_keys(_inputs), do: []

  defp runtime_view(%Mitos.Workload{} = workload) do
    %{
      "provider" => workload.provider,
      "transport_order" => workload.transport_order,
      "approvals" => workload.approvals,
      "timeout_ms" => workload.timeout_ms,
      "cwd" => workload.cwd
    }
  end

  defp capability_detail(%Mitos.Workload{capability: %Mitos.Capability{} = capability}) do
    capability
    |> Mitos.Capability.public_view()
    |> Map.put("harnesses", capability.harnesses)
    |> Map.put("published", true)
  end

  defp capability_detail(%Mitos.Workload{} = workload) do
    %{
      "name" => workload.title || workload.id,
      "trust_tier" => "T1",
      "harnesses" => [workload.provider],
      "published" => false
    }
  end

  defp files_view(%Mitos.Workload{} = workload) do
    definition_path = Path.join(Mitos.Workload.dir(), workload.id <> ".json")

    [
      %{
        "kind" => "workload_definition",
        "label" => "Workload JSON",
        "path" => definition_path,
        "required" => true,
        "present" => File.regular?(definition_path)
      },
      %{
        "kind" => "prompt_template",
        "label" => "Prompt template",
        "path" => "#{workload.id}.json:template",
        "required" => true,
        "present" => is_binary(workload.template) and workload.template != ""
      }
    ] ++ capability_files(workload.capability)
  end

  defp capability_files(nil), do: []

  defp capability_files(%Mitos.Capability{} = capability) do
    [
      %{
        "kind" => "capability_manifest",
        "label" => "Capability manifest",
        "path" => "embedded:capability",
        "required" => false,
        "present" => true
      }
    ]
    |> maybe_append(capability.skill, fn skill ->
      %{
        "kind" => "skill",
        "label" => "Skill instructions",
        "path" => skill,
        "required" => false,
        "present" => true
      }
    end)
    |> maybe_append(non_empty_map(capability.data), fn data ->
      %{
        "kind" => "data",
        "label" => "Data reference",
        "path" => data_ref(data),
        "required" => false,
        "present" => true
      }
    end)
    |> maybe_append(non_empty_map(capability.result), fn _result ->
      %{
        "kind" => "result_schema",
        "label" => "Result schema",
        "path" => "embedded:capability.result",
        "required" => false,
        "present" => true
      }
    end)
  end

  defp env_view(%Mitos.Workload{} = workload) do
    passthrough =
      workload.env_passthrough
      |> Enum.sort()
      |> Enum.map(fn key ->
        %{"name" => key, "source" => "host_env", "present" => present_env?(key)}
      end)

    %{
      "stored_keys" => workload.env |> Map.keys() |> Enum.sort(),
      "passthrough" => passthrough,
      "missing_passthrough" =>
        passthrough
        |> Enum.reject(& &1["present"])
        |> Enum.map(& &1["name"])
    }
  end

  defp sandbox_view(%Mitos.Workload{} = workload) do
    tier = trust_tier(workload)

    case Mitos.SandboxPolicy.select_workload_provider(workload) do
      {:ok, provider} ->
        %{
          "status" => "ready",
          "trust_tier" => tier,
          "provider" => provider_label(provider),
          "posture" => Mitos.SandboxPolicy.launch_posture(tier, %{})
        }

      {:error, {:sandbox_provider_ineligible, blocked_tier, detail}} ->
        %{
          "status" => "blocked",
          "trust_tier" => blocked_tier,
          "reason" => "no eligible sandbox provider",
          "detail" => json_safe(detail)
        }

      {:error, reason} ->
        %{
          "status" => "blocked",
          "trust_tier" => tier,
          "reason" => inspect(reason)
        }
    end
  end

  defp blockers(env, sandbox) do
    []
    |> maybe_block(env["missing_passthrough"] != [], %{
      "kind" => "env",
      "detail" => "missing host env",
      "keys" => env["missing_passthrough"]
    })
    |> maybe_block(sandbox["status"] != "ready", %{
      "kind" => "sandbox",
      "detail" => sandbox["reason"] || "sandbox unavailable"
    })
  end

  defp setup_status([]), do: "ready"

  defp setup_status(blockers) do
    if Enum.any?(blockers, &(&1["kind"] == "env")), do: "needs_env", else: "blocked"
  end

  defp maybe_block(blockers, true, block), do: blockers ++ [block]
  defp maybe_block(blockers, false, _block), do: blockers

  defp trust_tier(%Mitos.Workload{capability: %Mitos.Capability{trust_tier: tier}}), do: tier
  defp trust_tier(%Mitos.Workload{}), do: "T1"

  defp present_env?(key) do
    case System.get_env(key) do
      value when is_binary(value) and value != "" -> true
      _other -> false
    end
  end

  defp data_ref(%{} = data) do
    Enum.find_value(["snapshot", "volume", "path", "bind"], "embedded:capability.data", &data[&1])
  end

  defp non_empty_map(%{} = map) when map_size(map) > 0, do: map
  defp non_empty_map(_other), do: nil

  defp maybe_append(list, nil, _fun), do: list
  defp maybe_append(list, value, fun), do: list ++ [fun.(value)]

  defp provider_label(provider) when is_atom(provider), do: inspect(provider)
  defp provider_label(provider), do: to_string(provider)

  defp json_safe(%{} = map) do
    Map.new(map, fn {key, value} -> {to_string(key), json_safe(value)} end)
  end

  defp json_safe(list) when is_list(list), do: Enum.map(list, &json_safe/1)
  defp json_safe(value) when is_atom(value), do: inspect(value)
  defp json_safe(value), do: value
end
