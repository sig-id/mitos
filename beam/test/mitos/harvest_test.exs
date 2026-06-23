defmodule Mitos.HarvestTest do
  use ExUnit.Case, async: true

  test "content-addresses declared result evidence paths" do
    workspace = unique_tmp_dir("harvest-evidence-workspace")
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    File.write!(
      Path.join(workspace, "RESULT"),
      Jason.encode!(%{"signal" => "ship", "summary" => "done", "evidence" => []})
    )

    File.write!(Path.join(workspace, "report.md"), "buyer-safe evidence\n")

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{
                 result: %{path: "RESULT", evidence: ["report.md"]}
               }
             })

    assert %{
             "kind" => "file",
             "path" => "report.md",
             "sha256" => "sha256:" <> _,
             "bytes" => 20
           } = Enum.find(harvest.result["evidence"], &(&1["path"] == "report.md"))

    refute harvest.result["synthetic"]
  end

  test "marks harvested result synthetic when declared evidence is missing" do
    workspace = unique_tmp_dir("harvest-missing-evidence-workspace")
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    File.write!(Path.join(workspace, "RESULT"), Jason.encode!(%{"signal" => "ship"}))

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{
                 result: %{path: "RESULT", evidence: ["missing.md"]}
               }
             })

    assert harvest.result["synthetic"]
    assert "evidence_missing" in harvest.result["needs"]
    assert harvest.result["evidence_missing"] == ["missing.md"]

    assert %{"path" => "missing.md", "missing" => true} =
             Enum.find(harvest.result["evidence"], &(&1["path"] == "missing.md"))
  end

  test "content-addresses self-reported result evidence paths" do
    workspace = unique_tmp_dir("harvest-self-reported-evidence-workspace")
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    File.write!(Path.join(workspace, "report.md"), "self-reported evidence\n")

    File.write!(
      Path.join(workspace, "RESULT"),
      Jason.encode!(%{
        "signal" => "ship",
        "summary" => "done",
        "evidence" => [%{"kind" => "report", "path" => "report.md"}]
      })
    )

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    assert %{
             "kind" => "report",
             "path" => "report.md",
             "sha256" => "sha256:" <> _,
             "bytes" => 23
           } = Enum.find(harvest.result["evidence"], &(&1["path"] == "report.md"))

    refute harvest.result["synthetic"]
  end

  test "promotes remote result artifact hash into effect payload result" do
    payload =
      Mitos.Harvest.effect_payload(%{
        diff: %{"path" => "diff.patch", "sha256" => "sha256:diff"},
        result: %{"signal" => "ship", "path" => "RESULT", "synthetic" => false},
        artifacts: [%{"kind" => "file", "path" => "RESULT", "sha256" => "sha256:result"}]
      })

    assert payload["result"]["sha256"] == "sha256:result"
  end

  test "marks result synthetic when evidence cannot be content-addressed" do
    workspace = unique_tmp_dir("harvest-invalid-evidence-workspace")
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    File.write!(
      Path.join(workspace, "RESULT"),
      Jason.encode!(%{
        "signal" => "ship",
        "summary" => "done",
        "evidence" => [%{"kind" => "external", "artifact" => "art://run/unhashed"}]
      })
    )

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    assert harvest.result["synthetic"]
    assert harvest.result["signal"] == "unknown"
    assert "evidence_missing" in harvest.result["needs"]
    assert harvest.result["evidence_missing"] == ["art://run/unhashed"]

    assert Enum.any?(harvest.result["evidence"], fn
             %{"kind" => "diff", "path" => "diff.patch", "sha256" => "sha256:" <> _} -> true
             _artifact -> false
           end)
  end

  test "marks result synthetic when it violates an inline result schema" do
    workspace = unique_tmp_dir("harvest-inline-schema-workspace")
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    File.write!(
      Path.join(workspace, "RESULT"),
      Jason.encode!(%{"signal" => "oops", "summary" => "bad signal"})
    )

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{
                 result: %{
                   path: "RESULT",
                   schema: %{
                     type: "object",
                     required: ["signal", "summary"],
                     properties: %{
                       signal: %{type: "string", enum: ["ship", "revise"]},
                       summary: %{type: "string"}
                     }
                   }
                 }
               }
             })

    assert harvest.result["synthetic"]
    assert harvest.result["needs"] == ["result_schema_invalid"]
    assert harvest.result["schema_error"] =~ "RESULT.signal"
  end

  test "validates result against a workspace schema file" do
    workspace = unique_tmp_dir("harvest-schema-file-workspace")
    on_exit(fn -> File.rm_rf!(workspace) end)
    File.mkdir_p!(workspace)

    File.write!(
      Path.join(workspace, "result.schema.json"),
      Jason.encode!(%{
        type: "object",
        required: ["signal", "confidence"],
        properties: %{
          signal: %{type: "string", enum: ["ship", "revise"]},
          confidence: %{type: "number", minimum: 0, maximum: 1}
        }
      })
    )

    File.write!(
      Path.join(workspace, "RESULT"),
      Jason.encode!(%{"signal" => "ship", "confidence" => 0.75})
    )

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{result: %{path: "RESULT", schema: "result.schema.json"}}
             })

    refute harvest.result["synthetic"]
    assert harvest.result["signal"] == "ship"
    assert harvest.result["confidence"] == 0.75
  end

  test "validates result against a package-relative schema file" do
    workspace = unique_tmp_dir("harvest-package-schema-workspace")
    package_root = unique_tmp_dir("harvest-package-schema-root")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(package_root)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(package_root)

    File.write!(
      Path.join(package_root, "result.schema.json"),
      Jason.encode!(%{
        type: "object",
        required: ["signal"],
        properties: %{signal: %{enum: ["ship"]}}
      })
    )

    File.write!(Path.join(workspace, "RESULT"), Jason.encode!(%{"signal" => "ship"}))

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               schema_root: package_root,
               launch_spec: %{result: %{path: "RESULT", schema: "result.schema.json"}}
             })

    refute harvest.result["synthetic"]
    assert harvest.result["signal"] == "ship"
  end

  test "prefers package schema root over a workspace schema with the same path" do
    workspace = unique_tmp_dir("harvest-schema-precedence-workspace")
    package_root = unique_tmp_dir("harvest-schema-precedence-root")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(package_root)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(package_root)

    File.write!(
      Path.join(package_root, "result.schema.json"),
      Jason.encode!(%{type: "object", required: ["signal"]})
    )

    File.write!(
      Path.join(workspace, "result.schema.json"),
      Jason.encode!(%{type: "object", required: ["must_not_win"]})
    )

    File.write!(Path.join(workspace, "RESULT"), Jason.encode!(%{"signal" => "ship"}))

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               schema_root: package_root,
               launch_spec: %{result: %{path: "RESULT", schema: "result.schema.json"}}
             })

    refute harvest.result["synthetic"]
    assert harvest.result["signal"] == "ship"
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
