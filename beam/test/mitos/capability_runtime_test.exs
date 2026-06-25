defmodule Mitos.CapabilityRuntimeTest do
  use ExUnit.Case, async: true

  alias Mitos.Capability
  alias Mitos.CapabilityRuntime
  alias Mitos.Workload

  test "prepare_agent_options rejects symlinked runtime roots" do
    tmp = unique_tmp_dir("capability-runtime-root-symlink")
    outside = Path.join(tmp, "outside")
    runtime_root = Path.join(tmp, "runtime")

    File.mkdir_p!(outside)
    File.ln_s!(outside, runtime_root)

    on_exit(fn -> File.rm_rf!(tmp) end)

    assert {:error, {:mkdir_failed, ^runtime_root, :symlink}} =
             CapabilityRuntime.prepare_agent_options(workload(tmp), agent_opts(),
               runtime_root: runtime_root
             )

    refute File.exists?(Path.join([outside, "skill", "resolved.md"]))
  end

  test "prepare_agent_options rejects symlinked runtime file targets" do
    tmp = unique_tmp_dir("capability-runtime-file-symlink")
    runtime_root = Path.join(tmp, "runtime")
    outside_file = Path.join(tmp, "outside-skill.md")

    File.mkdir_p!(Path.join(runtime_root, "skill"))
    File.mkdir_p!(Path.join(runtime_root, "tools"))
    File.mkdir_p!(Path.join(runtime_root, "data"))
    File.write!(outside_file, "outside\n")
    File.ln_s!(outside_file, Path.join([runtime_root, "skill", "resolved.md"]))

    on_exit(fn -> File.rm_rf!(tmp) end)

    assert {:error, {:skill_materialize_failed, detail}} =
             CapabilityRuntime.prepare_agent_options(workload(tmp), agent_opts(),
               runtime_root: runtime_root
             )

    assert detail =~ "runtime file write failed"
    assert detail =~ ":symlink"
    assert File.read!(outside_file) == "outside\n"
  end

  test "prepare_agent_options rejects explicit workspaces under symlinked parents" do
    tmp = unique_tmp_dir("capability-runtime-workspace-parent-symlink")
    outside = Path.join(tmp, "outside")
    link = Path.join(tmp, "workspace-link")
    runtime_root = Path.join(tmp, "runtime")
    workspace = Path.join(link, "workspace")

    File.mkdir_p!(outside)
    File.ln_s!(outside, link)

    on_exit(fn -> File.rm_rf!(tmp) end)

    assert {:error, {:mkdir_failed, ^link, :symlink}} =
             CapabilityRuntime.prepare_agent_options(
               %{workload(tmp) | cwd: workspace},
               agent_opts(),
               runtime_root: runtime_root
             )

    refute File.exists?(Path.join(outside, "workspace"))
  end

  test "prepare_agent_options rejects tool execs through symlinked package parents" do
    tmp = unique_tmp_dir("capability-runtime-package-tool-parent-symlink")
    package_root = Path.join(tmp, "package")
    outside_tools = Path.join(tmp, "outside-tools")
    runtime_root = Path.join(tmp, "runtime")

    File.mkdir_p!(package_root)
    File.mkdir_p!(outside_tools)
    File.write!(Path.join(outside_tools, "extract.sh"), "#!/bin/sh\necho outside\n")
    File.ln_s!(outside_tools, Path.join(package_root, "tools-linked"))

    on_exit(fn -> File.rm_rf!(tmp) end)

    assert {:error, {:tool_exec_unavailable, "extract", "tools-linked/extract.sh", :path_symlink}} =
             CapabilityRuntime.prepare_agent_options(
               workload(package_root,
                 tools: [
                   %{"name" => "extract", "exec" => "tools-linked/extract.sh"}
                 ]
               ),
               agent_opts(),
               runtime_root: runtime_root
             )

    refute File.exists?(Path.join([runtime_root, "tools", "extract.sh"]))
  end

  test "prepare_agent_options rejects data files through symlinked package parents" do
    tmp = unique_tmp_dir("capability-runtime-package-data-parent-symlink")
    package_root = Path.join(tmp, "package")
    outside_data = Path.join(tmp, "outside-data")
    runtime_root = Path.join(tmp, "runtime")

    File.mkdir_p!(package_root)
    File.mkdir_p!(outside_data)
    File.write!(Path.join(outside_data, "ref.txt"), "outside data\n")
    File.ln_s!(outside_data, Path.join(package_root, "data-linked"))

    on_exit(fn -> File.rm_rf!(tmp) end)

    assert {:error, {:data_unavailable, "data-linked/ref.txt", :path_symlink}} =
             CapabilityRuntime.prepare_agent_options(
               workload(package_root,
                 data: %{"path" => "data-linked/ref.txt", "mount" => "/data", "ro" => true}
               ),
               agent_opts(),
               runtime_root: runtime_root
             )

    refute File.exists?(Path.join([runtime_root, "data", "ref.txt"]))
  end

  defp workload(package_root, capability_attrs \\ []) do
    %Workload{
      id: "capability-runtime",
      provider: "fake",
      template: "{{objective}}",
      package_root: package_root,
      capability:
        struct(
          %Capability{
            id: "cap:runtime",
            name: "capability-runtime",
            harnesses: ["fake"],
            trust_tier: "T1"
          },
          capability_attrs
        )
    }
  end

  defp agent_opts do
    [
      session_id: "run-capability-runtime",
      resolved_skill: %{
        "body" => "runtime skill\n",
        "source" => "skill.md",
        "sha256" => "sha256:" <> String.duplicate("0", 64)
      }
    ]
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "#{prefix}-#{System.unique_integer([:positive])}")
  end
end
