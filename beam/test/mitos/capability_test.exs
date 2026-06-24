defmodule Mitos.CapabilityTest do
  use ExUnit.Case, async: true

  test "parses canonical capability manifest fields and produces launch fields" do
    manifest = %{
      "name" => "design-critique",
      "version" => "1.4.0",
      "image" => "oci://registry.mitos/design-critique@sha256:abc",
      "data" => %{"snapshot" => "snap://designs/ref", "mount" => "/data", "ro" => true},
      "harnesses" => ["fake", "codex"],
      "skill" => "./skill.md",
      "tools" => [
        %{
          "name" => "heuristics",
          "input" => %{"svg" => "string"},
          "output" => %{"violations" => ["object"]},
          "deterministic" => true,
          "side_effects" => ["workspace_read"]
        }
      ],
      "trust_tier" => "T1",
      "skill_adapt" => %{
        "gpt" => "@adapt append skill:rules Prefer compact reasoning."
      },
      "resources" => %{"cpu" => 2, "timeout" => "900s"},
      "egress_policy" => [
        %{
          "dest" => "api.example.test",
          "credential" => "seller_key",
          "requires_approval" => false
        }
      ],
      "result" => %{"schema" => "./result.schema.json", "evidence" => ["critique.md"]},
      "hooks" => %{
        "pre" => [%{"name" => "prepare", "run" => ["true"], "on_fail" => "block"}],
        "post" => [%{"name" => "gate", "run" => ["true"], "on_fail" => "block"}]
      }
    }

    assert {:ok, capability} = Mitos.Capability.from_json("critique", manifest)
    assert capability.name == "design-critique"
    assert capability.version == "1.4.0"
    assert capability.id == "cap:" <> capability.digest
    assert capability.digest =~ "sha256:"
    assert capability.harnesses == ["fake", "codex"]
    assert capability.trust_tier == "T1"
    assert Mitos.Capability.compatible_harness?(capability, "fake")
    refute Mitos.Capability.compatible_harness?(capability, "claude")

    launch_fields = Mitos.Capability.launch_fields(capability)
    assert launch_fields.capability_ref == capability.id
    assert launch_fields.trust_tier == "T1"
    assert launch_fields.egress_policy == capability.egress_policy
    assert launch_fields.result == capability.result
    assert launch_fields.hooks == capability.hooks
    assert launch_fields.skill_adapt == capability.skill_adapt

    assert %{
             "capability_ref" => _,
             "name" => "design-critique",
             "trust_tier" => "T1",
             "result" => %{"schema" => "./result.schema.json", "evidence" => ["critique.md"]}
           } = Mitos.Capability.public_view(capability)

    refute Map.has_key?(Mitos.Capability.public_view(capability), "harnesses")
  end

  test "package-aware identity changes when pillar content changes" do
    package_root =
      Path.join(System.tmp_dir!(), "mitos-capability-id-#{System.unique_integer([:positive])}")

    File.mkdir_p!(Path.join(package_root, "tools"))
    File.mkdir_p!(Path.join(package_root, "data"))

    on_exit(fn -> File.rm_rf!(package_root) end)

    skill_path = Path.join(package_root, "skill.md")
    tool_path = Path.join([package_root, "tools", "extract.py"])
    data_path = Path.join([package_root, "data", "ref.txt"])

    File.write!(skill_path, "role v1\n")
    File.write!(tool_path, "print('tool v1')\n")
    File.write!(data_path, "data v1\n")

    manifest = %{
      "name" => "content-addressed",
      "harnesses" => ["fake"],
      "skill" => "skill.md",
      "data" => %{"path" => "data", "mount" => "/data", "ro" => true},
      "tools" => [
        %{
          "name" => "extract",
          "exec" => "tools/extract.py",
          "input" => %{},
          "output" => %{},
          "deterministic" => true
        }
      ]
    }

    assert {:ok, first} =
             Mitos.Capability.from_json("content-addressed", manifest,
               package_root: package_root
             )

    assert {:ok, same} =
             Mitos.Capability.from_json("content-addressed", manifest,
               package_root: package_root
             )

    assert same.id == first.id

    File.write!(skill_path, "role v2\n")

    assert {:ok, changed_skill} =
             Mitos.Capability.from_json("content-addressed", manifest,
               package_root: package_root
             )

    refute changed_skill.id == first.id

    File.write!(skill_path, "role v1\n")
    File.write!(tool_path, "print('tool v2')\n")

    assert {:ok, changed_tool} =
             Mitos.Capability.from_json("content-addressed", manifest,
               package_root: package_root
             )

    refute changed_tool.id == first.id

    File.write!(tool_path, "print('tool v1')\n")
    File.write!(data_path, "data v2\n")

    assert {:ok, changed_data} =
             Mitos.Capability.from_json("content-addressed", manifest,
               package_root: package_root
             )

    refute changed_data.id == first.id
  end

  test "resolves per-model skill adaptation overlays deterministically" do
    base = """
    <!-- @skill:role -->
    Base role.
    <!-- @skill:rules -->
    Base rule.
    <!-- @skill:procedure -->
    Step one.
    <!-- @skill:success -->
    Ship evidence.
    """

    assert {:ok, %{"body" => ^base, "sha256" => "sha256:" <> _}} =
             Mitos.Capability.resolve_skill(base, [])

    overlay = """
    @adapt prepend skill:rules Check buyer constraints first.
    @adapt append skill:procedure
    Step two.
    Step three.
    @adapt replace skill:success Emit RESULT with content-addressed evidence.
    """

    assert {:ok, %{"body" => resolved, "sha256" => "sha256:" <> _}} =
             Mitos.Capability.resolve_skill(base, [overlay])

    assert resolved =~ """
           <!-- @skill:rules -->
           Check buyer constraints first.
           Base rule.
           """

    assert resolved =~ """
           <!-- @skill:procedure -->
           Step one.
           Step two.
           Step three.
           """

    assert resolved =~ """
           <!-- @skill:success -->
           Emit RESULT with content-addressed evidence.
           """

    assert {:error, {:adapt_anchor_not_found, "skill:missing"}} =
             Mitos.Capability.resolve_skill(base, [
               "@adapt append skill:missing unreachable"
             ])

    assert {:error, {:missing_skill_anchors, ["skill:procedure", "skill:success"]}} =
             Mitos.Capability.resolve_skill(
               """
               <!-- @skill:role -->
               Base role.
               <!-- @skill:rules -->
               Base rule.
               """,
               []
             )
  end

  test "accepts combined tool schemas" do
    assert {:ok, capability} =
             Mitos.Capability.from_json("typed-tools", %{
               "name" => "typed-tools",
               "harnesses" => ["fake"],
               "tools" => [
                 %{
                   "name" => "extract",
                   "schema" => %{
                     "input" => %{"type" => "object"},
                     "output" => %{"type" => "object"}
                   },
                   "deterministic" => true
                 }
               ]
             })

    assert [%{"name" => "extract", "schema" => %{"input" => _, "output" => _}}] =
             capability.tools
  end

  test "rejects malformed tool declarations" do
    base = %{"name" => "typed-tools", "harnesses" => ["fake"]}

    assert {:error, {:invalid_capability, "tools must be a list"}} =
             Mitos.Capability.from_json("bad-tools", Map.put(base, "tools", %{}))

    assert {:error, {:invalid_capability, "tools.0 must be an object"}} =
             Mitos.Capability.from_json("bad-tools", Map.put(base, "tools", ["extract"]))

    assert {:error, {:invalid_capability, "tools.0.name is required"}} =
             Mitos.Capability.from_json(
               "bad-tools",
               Map.put(base, "tools", [
                 %{
                   "schema" => %{"input" => %{}, "output" => %{}},
                   "deterministic" => true
                 }
               ])
             )

    assert {:error, {:invalid_capability, "tools.0 must declare input/output or schema object"}} =
             Mitos.Capability.from_json(
               "bad-tools",
               Map.put(base, "tools", [%{"name" => "extract", "deterministic" => true}])
             )

    assert {:error, {:invalid_capability, "tools.0.deterministic must be true"}} =
             Mitos.Capability.from_json(
               "bad-tools",
               Map.put(base, "tools", [
                 %{"name" => "extract", "input" => %{}, "output" => %{}, "deterministic" => false}
               ])
             )

    assert {:error, {:invalid_capability, "tools.0.side_effects must be a list of strings"}} =
             Mitos.Capability.from_json(
               "bad-tools",
               Map.put(base, "tools", [
                 %{
                   "name" => "extract",
                   "input" => %{},
                   "output" => %{},
                   "deterministic" => true,
                   "side_effects" => ["workspace_write", ""]
                 }
               ])
             )
  end

  test "rejects malformed hook declarations" do
    base = %{"name" => "hooked", "harnesses" => ["fake"]}

    assert {:ok, capability} =
             Mitos.Capability.from_json(
               "skip-hook",
               Map.put(base, "hooks", %{
                 "post" => [%{"name" => "optional-gate", "run" => ["false"], "on_fail" => "skip"}]
               })
             )

    assert get_in(capability.hooks, ["post", Access.at(0), "on_fail"]) == "skip"

    assert {:error, {:invalid_capability, "hooks must be an object"}} =
             Mitos.Capability.from_json("bad-hooks", Map.put(base, "hooks", []))

    assert {:error, {:invalid_capability, ~s(hooks contains unsupported phase "during")}} =
             Mitos.Capability.from_json(
               "bad-hooks",
               Map.put(base, "hooks", %{"during" => []})
             )

    assert {:error, {:invalid_capability, "hooks.pre must be a list"}} =
             Mitos.Capability.from_json(
               "bad-hooks",
               Map.put(base, "hooks", %{"pre" => %{}})
             )

    assert {:error, {:invalid_capability, "hooks.pre.0 must be an object"}} =
             Mitos.Capability.from_json("bad-hooks", Map.put(base, "hooks", %{"pre" => ["sh"]}))

    assert {:error, {:invalid_capability, "hooks.post.0.name is required"}} =
             Mitos.Capability.from_json(
               "bad-hooks",
               Map.put(base, "hooks", %{"post" => [%{"run" => ["true"]}]})
             )

    assert {:error,
            {:invalid_capability,
             "hooks.post.0.run must be a string or a non-empty list of strings"}} =
             Mitos.Capability.from_json(
               "bad-hooks",
               Map.put(base, "hooks", %{"post" => [%{"name" => "gate", "run" => []}]})
             )

    assert {:error,
            {:invalid_capability, "hooks.post.0.on_fail must be one of block, warn, skip"}} =
             Mitos.Capability.from_json(
               "bad-hooks",
               Map.put(base, "hooks", %{
                 "post" => [%{"name" => "gate", "run" => ["true"], "on_fail" => "continue"}]
               })
             )

    assert {:error, {:invalid_capability, "hooks.post.0.deterministic must be true"}} =
             Mitos.Capability.from_json(
               "bad-hooks",
               Map.put(base, "hooks", %{
                 "post" => [%{"name" => "gate", "run" => ["true"], "deterministic" => false}]
               })
             )

    assert {:error, {:invalid_capability, "hooks.post.0.side_effects must be a list of strings"}} =
             Mitos.Capability.from_json(
               "bad-hooks",
               Map.put(base, "hooks", %{
                 "post" => [
                   %{"name" => "gate", "run" => ["true"], "side_effects" => ["workspace_read", 1]}
                 ]
               })
             )
  end

  test "rejects manifests without harnesses or with invalid trust tier" do
    assert {:error, {:invalid_capability, "harnesses must list at least one harness"}} =
             Mitos.Capability.from_json("bad", %{"name" => "bad"})

    assert {:error, {:invalid_capability, "trust_tier must be one of T0, T1, T2"}} =
             Mitos.Capability.from_json("bad", %{
               "name" => "bad",
               "harnesses" => ["fake"],
               "trust_tier" => "T9"
             })
  end

  test "rejects baked runtime bindings that mitos injects at launch" do
    base = %{"name" => "portable", "harnesses" => ["fake"], "trust_tier" => "T1"}

    assert {:error,
            {:invalid_capability,
             "capability manifests must not bake injected runtime field model"}} =
             Mitos.Capability.from_json("bad-model", Map.put(base, "model", "gpt-test"))

    assert {:error,
            {:invalid_capability,
             "capability manifests must not bake injected runtime field provider"}} =
             Mitos.Capability.from_json("bad-provider", Map.put(base, "provider", "fake"))
  end

  test "rejects unsupported top-level manifest fields" do
    base = %{"name" => "portable", "harnesses" => ["fake"], "trust_tier" => "T1"}

    assert {:error,
            {:invalid_capability, ~s(capability manifest contains unsupported field "notes")}} =
             Mitos.Capability.from_json("bad-notes", Map.put(base, "notes", "do this"))

    assert {:error,
            {:invalid_capability, ~s(capability manifest contains unsupported field "x_vendor")}} =
             Mitos.Capability.from_json("bad-extension", Map.put(base, "x_vendor", %{}))
  end

  test "enforces content-addressed image and read-only data invariants" do
    base = %{"name" => "secure", "harnesses" => ["fake"], "trust_tier" => "T2"}

    assert {:error, {:invalid_capability, "image must be content-addressed with @sha256:"}} =
             Mitos.Capability.from_json(
               "bad-image",
               Map.put(base, "image", "oci://repo/demo:latest")
             )

    assert {:error, {:invalid_capability, "data.ro must be true"}} =
             Mitos.Capability.from_json(
               "rw-data",
               Map.put(base, "data", %{
                 "snapshot" => "snap://demo",
                 "mount" => "/data",
                 "ro" => false
               })
             )

    assert {:error, {:invalid_capability, "T2 data must not declare bind or path mounts"}} =
             Mitos.Capability.from_json(
               "bind-data",
               Map.put(base, "data", %{"bind" => "/host/path", "mount" => "/data", "ro" => true})
             )

    assert {:error, {:invalid_capability, "T2 data must declare a snapshot or volume"}} =
             Mitos.Capability.from_json(
               "no-snapshot",
               Map.put(base, "data", %{"mount" => "/data", "ro" => true})
             )

    assert {:ok, capability} =
             Mitos.Capability.from_json(
               "good-t2",
               base
               |> Map.put("image", "oci://repo/demo@sha256:abc")
               |> Map.put("data", %{"snapshot" => "snap://demo", "mount" => "/data", "ro" => true})
             )

    assert capability.trust_tier == "T2"
  end

  test "rejects baked secrets in capability manifests" do
    base = %{
      "name" => "secure",
      "harnesses" => ["fake"],
      "trust_tier" => "T2",
      "image" => "oci://repo/demo@sha256:abc",
      "data" => %{"snapshot" => "snap://demo", "mount" => "/data", "ro" => true}
    }

    assert {:error, {:invalid_capability, ~s(egress_policy contains unsupported field "secret")}} =
             Mitos.Capability.from_json(
               "bad-egress-secret",
               Map.put(base, "egress_policy", [
                 %{
                   "dest" => "api.example.test",
                   "credential" => "seller_key",
                   "secret" => "must-not-be-here"
                 }
               ])
             )

    assert {:error,
            {:invalid_capability,
             "capability manifests must not contain baked secret field tools.0.env"}} =
             Mitos.Capability.from_json(
               "bad-tool-env",
               Map.put(base, "tools", [
                 %{"name" => "leaky-tool", "env" => %{"API_KEY" => "must-not-be-here"}}
               ])
             )

    assert {:error,
            {:invalid_capability,
             "capability manifests must not contain baked secret field resources.token"}} =
             Mitos.Capability.from_json(
               "bad-resource-token",
               Map.put(base, "resources", %{"token" => "must-not-be-here"})
             )
  end
end
