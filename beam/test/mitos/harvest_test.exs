defmodule Mitos.HarvestTest do
  use ExUnit.Case, async: true

  test "redacts secret-looking workspace diff content before writing trace artifacts" do
    workspace = unique_tmp_dir("harvest-redacted-diff-workspace")
    trace_dir = unique_tmp_dir("harvest-redacted-diff-trace")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(trace_dir)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(trace_dir)
    git!(workspace, ["init"])
    git!(workspace, ["config", "user.email", "mitos@example.test"])
    git!(workspace, ["config", "user.name", "Mitos Test"])
    File.write!(Path.join(workspace, "tracked.txt"), "base\n")
    git!(workspace, ["add", "tracked.txt"])
    git!(workspace, ["commit", "-m", "base"])

    File.write!(
      Path.join(workspace, "tracked.txt"),
      "API_KEY=diff-secret\ncallback=https://user:pass@example.test/hook?token=diff-token&visible=ok\n"
    )

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               trace_dir: trace_dir,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    patch = File.read!(Path.join(trace_dir, "diff.patch"))

    assert patch == harvest.diff["patch"]
    assert harvest.diff["sha256"] == sha256(patch)
    assert patch =~ "+API_KEY=[redacted]"
    assert patch =~ "https://example.test/hook?token=[redacted]&visible=ok"

    refute patch =~ "diff-secret"
    refute patch =~ "user:pass"
    refute patch =~ "diff-token"
  end

  test "does not harvest git diffs through symlinked workspaces" do
    workspace = unique_tmp_dir("harvest-symlink-workspace")
    outside = unique_tmp_dir("harvest-symlink-workspace-outside")
    trace_dir = unique_tmp_dir("harvest-symlink-workspace-trace")

    on_exit(fn ->
      File.rm(workspace)
      File.rm_rf!(outside)
      File.rm_rf!(trace_dir)
    end)

    File.mkdir_p!(outside)
    File.mkdir_p!(trace_dir)
    git!(outside, ["init"])
    git!(outside, ["config", "user.email", "mitos@example.test"])
    git!(outside, ["config", "user.name", "Mitos Test"])
    File.write!(Path.join(outside, "tracked.txt"), "base\n")
    git!(outside, ["add", "tracked.txt"])
    git!(outside, ["commit", "-m", "base"])
    File.write!(Path.join(outside, "tracked.txt"), "API_KEY=outside-diff-secret\n")
    File.ln_s!(outside, workspace)

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               trace_dir: trace_dir,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    patch = File.read!(Path.join(trace_dir, "diff.patch"))

    assert patch == ""
    assert harvest.diff["empty?"]
    refute patch =~ "outside-diff-secret"
  end

  test "refuses symlinked trace artifact outputs" do
    workspace = unique_tmp_dir("harvest-trace-output-symlink-workspace")
    trace_dir = unique_tmp_dir("harvest-trace-output-symlink-trace")
    outside = unique_tmp_dir("harvest-trace-output-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(trace_dir)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(trace_dir)
    File.mkdir_p!(outside)

    outside_patch = Path.join(outside, "diff.patch")
    File.write!(outside_patch, "outside patch")
    File.ln_s!(outside_patch, Path.join(trace_dir, "diff.patch"))

    assert_raise ArgumentError, ~r/trace artifact must not be a symlink/, fn ->
      Mitos.Harvest.collect(%{
        cwd: workspace,
        trace_dir: trace_dir,
        launch_spec: %{result: %{path: "RESULT"}}
      })
    end

    assert File.read!(outside_patch) == "outside patch"
  end

  test "refuses trace directories reached through symlinked parents" do
    workspace = unique_tmp_dir("harvest-trace-parent-symlink-workspace")
    actual_parent = unique_tmp_dir("harvest-trace-parent-symlink-actual")
    link_parent = unique_tmp_dir("harvest-trace-parent-symlink-link")
    trace_dir = Path.join(link_parent, "trace")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm(link_parent)
      File.rm_rf!(actual_parent)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(actual_parent)
    File.ln_s!(actual_parent, link_parent)
    File.mkdir_p!(trace_dir)

    assert_raise ArgumentError, ~r/trace directory must not contain symlinks/, fn ->
      Mitos.Harvest.collect(%{
        cwd: workspace,
        trace_dir: trace_dir,
        launch_spec: %{result: %{path: "RESULT"}}
      })
    end

    refute File.exists?(Path.join(actual_parent, "trace/diff.patch"))
  end

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

  test "treats symlinked RESULT files as unavailable" do
    workspace = unique_tmp_dir("harvest-result-symlink-workspace")
    outside = unique_tmp_dir("harvest-result-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(outside)

    outside_result = Path.join(outside, "RESULT")
    File.write!(outside_result, Jason.encode!(%{"signal" => "ship", "summary" => "outside"}))
    File.ln_s!(outside_result, Path.join(workspace, "RESULT"))

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    assert harvest.result["synthetic"]
    assert harvest.result["signal"] == "unknown"
    assert harvest.result["summary"] == "RESULT artifact unavailable: :symlink"
    assert harvest.result["needs"] == ["result_unavailable"]
    refute Map.has_key?(harvest.result, "sha256")
  end

  test "does not content-address symlinked declared evidence" do
    workspace = unique_tmp_dir("harvest-evidence-symlink-workspace")
    outside = unique_tmp_dir("harvest-evidence-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(outside)

    File.write!(Path.join(workspace, "RESULT"), Jason.encode!(%{"signal" => "ship"}))
    outside_report = Path.join(outside, "report.md")
    File.write!(outside_report, "outside evidence\n")
    File.ln_s!(outside_report, Path.join(workspace, "report.md"))

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{result: %{path: "RESULT", evidence: ["report.md"]}}
             })

    assert harvest.result["synthetic"]
    assert "evidence_missing" in harvest.result["needs"]
    assert harvest.result["evidence_missing"] == ["report.md"]

    assert %{"path" => "report.md", "missing" => true} =
             evidence =
             Enum.find(harvest.result["evidence"], &(&1["path"] == "report.md"))

    refute Map.has_key?(evidence, "sha256")
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

  test "does not load symlinked workspace schema files" do
    workspace = unique_tmp_dir("harvest-schema-symlink-workspace")
    outside = unique_tmp_dir("harvest-schema-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(outside)

    outside_schema = Path.join(outside, "result.schema.json")

    File.write!(
      outside_schema,
      Jason.encode!(%{type: "object", required: ["signal"]})
    )

    File.ln_s!(outside_schema, Path.join(workspace, "result.schema.json"))
    File.write!(Path.join(workspace, "RESULT"), Jason.encode!(%{"signal" => "ship"}))

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               launch_spec: %{result: %{path: "RESULT", schema: "result.schema.json"}}
             })

    assert harvest.result["synthetic"]
    assert harvest.result["needs"] == ["result_schema_unavailable"]
    assert harvest.result["schema_error"] == ":schema_unavailable"
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

  test "does not content-address registered artifacts through symlinks" do
    workspace = unique_tmp_dir("harvest-artifact-symlink-workspace")
    trace_dir = unique_tmp_dir("harvest-artifact-symlink-trace")
    outside = unique_tmp_dir("harvest-artifact-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(trace_dir)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(trace_dir)
    File.mkdir_p!(outside)

    File.write!(Path.join(workspace, "RESULT"), Jason.encode!(%{"signal" => "ship"}))
    outside_artifact = Path.join(outside, "artifact.txt")
    File.write!(outside_artifact, "outside artifact\n")
    File.ln_s!(outside_artifact, Path.join(workspace, "artifact.txt"))

    File.write!(
      Path.join(trace_dir, "events.jsonl"),
      Jason.encode!(%{
        "kind" => "effect",
        "type" => "artifact_registered",
        "ref" => %{"kind" => "file", "path" => "artifact.txt"}
      }) <> "\n"
    )

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               trace_dir: trace_dir,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    assert [%{"kind" => "file", "path" => "artifact.txt"} = artifact] = harvest.artifacts
    refute Map.has_key?(artifact, "sha256")
    refute Map.has_key?(artifact, "bytes")
  end

  test "does not read registered artifacts from symlinked trace event logs" do
    workspace = unique_tmp_dir("harvest-event-log-symlink-workspace")
    trace_dir = unique_tmp_dir("harvest-event-log-symlink-trace")
    outside = unique_tmp_dir("harvest-event-log-symlink-outside")

    on_exit(fn ->
      File.rm_rf!(workspace)
      File.rm_rf!(trace_dir)
      File.rm_rf!(outside)
    end)

    File.mkdir_p!(workspace)
    File.mkdir_p!(trace_dir)
    File.mkdir_p!(outside)

    File.write!(Path.join(workspace, "RESULT"), Jason.encode!(%{"signal" => "ship"}))
    File.write!(Path.join(workspace, "artifact.txt"), "workspace artifact\n")

    outside_events = Path.join(outside, "events.jsonl")

    File.write!(
      outside_events,
      Jason.encode!(%{
        "kind" => "effect",
        "type" => "artifact_registered",
        "ref" => %{"kind" => "file", "path" => "artifact.txt"}
      }) <> "\n"
    )

    File.ln_s!(outside_events, Path.join(trace_dir, "events.jsonl"))

    assert {:ok, harvest} =
             Mitos.Harvest.collect(%{
               cwd: workspace,
               trace_dir: trace_dir,
               launch_spec: %{result: %{path: "RESULT"}}
             })

    assert harvest.artifacts == []
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

  defp git!(cwd, args) do
    case System.cmd("git", ["-C", cwd | args], stderr_to_stdout: true) do
      {_output, 0} -> :ok
      {output, status} -> flunk("git #{Enum.join(args, " ")} failed with #{status}: #{output}")
    end
  end

  defp sha256(bytes) do
    digest =
      :crypto.hash(:sha256, bytes)
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end
end
