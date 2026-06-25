defmodule Mitos.BundledAppsTest do
  use ExUnit.Case, async: false

  @apps_root Path.expand("../../../apps", __DIR__)

  test "bundled Chief Wiggum apps load as local Mitos applications" do
    for id <- ["auto-code-review", "kanban-task-worker"] do
      app_root = Path.join(@apps_root, id)

      assert {:ok, app} = Mitos.AppManifest.load(app_root)
      assert app.name == id
      assert app.version == "1.0.0"
      assert app.app_id =~ ~r/^app:sha256:[0-9a-f]{64}$/
      assert [_entrypoint] = app.entrypoints
      assert [_pipeline] = app.pipelines
      assert [%{"as" => _alias, "ref" => ref}] = app.capabilities
      assert String.starts_with?(ref, "capabilities/")
    end
  end

  test "bundled Chief Wiggum capabilities have valid workload manifests" do
    cases = [
      {"auto-code-review", "code-review", "capabilities/code-review/workload.json"},
      {"kanban-task-worker", "task-worker", "capabilities/task-worker/workload.json"}
    ]

    for {app_id, alias, workload_ref} <- cases do
      app_root = Path.join(@apps_root, app_id)
      workload_path = Path.join(app_root, workload_ref)
      package_root = Path.dirname(workload_path)

      assert {:ok, raw} = File.read(workload_path)
      assert {:ok, json} = Jason.decode(raw)
      assert {:ok, workload} = Mitos.Workload.from_json(alias, json, package_root: package_root)
      assert workload.provider == "opencode"
      assert workload.capability.name == alias || workload.capability.name == app_id
      assert "opencode" in workload.capability.harnesses
      assert "codex" in workload.capability.harnesses
      assert workload.capability.skill == "skill.md"
      assert workload.capability.result["schema"] == "result.schema.json"
    end
  end

  test "bundled app registry defaults to the repository apps directory" do
    previous_apps_dir = Application.get_env(:mitos, :apps_dir)
    previous_apps_env = System.get_env("MITOS_APPS_DIR")

    try do
      Application.delete_env(:mitos, :apps_dir)
      System.delete_env("MITOS_APPS_DIR")
      assert Path.expand(Mitos.Applications.dir()) == @apps_root
    after
      restore_env(:apps_dir, previous_apps_dir)
      restore_system_env("MITOS_APPS_DIR", previous_apps_env)
    end
  end

  defp restore_env(_key, nil), do: Application.delete_env(:mitos, :apps_dir)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)

  defp restore_system_env(key, nil), do: System.delete_env(key)
  defp restore_system_env(key, value), do: System.put_env(key, value)
end
