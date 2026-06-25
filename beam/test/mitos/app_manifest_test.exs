defmodule Mitos.AppManifestTest do
  use ExUnit.Case, async: true

  defp app_root do
    root = Path.join(System.tmp_dir!(), "mitos-app-#{System.unique_integer([:positive])}")
    File.rm_rf!(root)
    File.mkdir_p!(Path.join(root, "pipelines"))
    File.write!(Path.join(root, "pipelines/resize.md"), "# Resize\n\nRun image-resize.\n")
    root
  end

  defp manifest do
    %{
      name: "image-resize-app",
      version: "1.2.0",
      description: "Resize images",
      authors: ["design-tools@example.test"],
      capabilities: [
        %{ref: "oci://registry.test/image-resize@sha256:abc123", as: "image-resize"}
      ],
      pipelines: [
        %{id: "resize-and-deliver", file: "pipelines/resize.md"}
      ],
      entrypoints: [
        %{
          id: "resize",
          pipeline: "resize-and-deliver",
          title: "Resize an image",
          workspace: "dataset",
          inputs: %{
            schema: %{
              type: "object",
              required: ["image", "width"],
              additionalProperties: false,
              properties: %{
                image: %{type: "file"},
                width: %{type: "int"},
                watermark: %{type: "boolean"}
              }
            }
          },
          defaults: %{
            budget: %{usd: 2, wall_ms: 300_000},
            constraints: ["stay within sRGB"]
          }
        }
      ],
      io: %{
        inputs: [%{connector: "webhook", path: "/apps/resize/runs", entrypoint: "resize"}],
        outputs: [%{sink: "web", on: "run.done", entrypoint: "resize"}]
      },
      services: %{requires: ["render-pool"], wants: ["usage-meter"]},
      requirements: %{
        harnesses: ["codex"],
        connectors: ["webhook"],
        egress: ["api.figma.com"],
        trust_tier: "T1",
        resources: %{cpu: 2, mem: "4Gi"}
      }
    }
  end

  test "loads a JSON app manifest and computes content identity" do
    root = app_root()
    File.write!(Path.join(root, "app.json"), Jason.encode!(manifest()))

    assert {:ok, app} = Mitos.AppManifest.load(root)
    assert app.name == "image-resize-app"
    assert app.version == "1.2.0"
    assert app.app_id =~ ~r/^app:sha256:[0-9a-f]{64}$/
    assert [%{"as" => "image-resize"}] = app.capabilities
    assert [%{"id" => "resize-and-deliver", "sha256" => "sha256:" <> _}] = app.pipelines

    assert [%{"id" => "resize", "workspace" => %{"kind" => "dataset"}, "parameters" => []}] =
             app.entrypoints

    assert app.io["inputs"] |> hd() |> Map.get("entrypoint") == "resize"
    assert app.services["requires"] == ["render-pool"]
    assert app.services["wants"] == ["usage-meter"]
    assert app.requirements["harnesses"] == ["codex"]
    assert app.requirements["connectors"] == ["webhook"]
    assert app.requirements["egress"] == ["api.figma.com"]
    assert app.requirements["trust_tier"] == "T1"
    assert app.requirements["resources"] == %{"cpu" => 2, "mem" => "4Gi"}
  end

  test "rejects symlinked app manifest files during discovery" do
    root = app_root()

    outside =
      Path.join(
        System.tmp_dir!(),
        "mitos-app-manifest-outside-#{System.unique_integer([:positive])}"
      )

    File.mkdir_p!(outside)
    outside_manifest = Path.join(outside, "app.json")
    File.write!(outside_manifest, Jason.encode!(manifest()))
    symlink_manifest = Path.join(root, "app.json")
    File.ln_s!(outside_manifest, symlink_manifest)

    on_exit(fn ->
      File.rm_rf!(root)
      File.rm_rf!(outside)
    end)

    assert {:error, {:application_not_found, ^root}} = Mitos.AppManifest.load(root)

    assert {:error, {:invalid_application, "application manifest path must not be a symlink"}} =
             Mitos.AppManifest.load(symlink_manifest)
  end

  test "app_id changes when a bundled pipeline changes" do
    root = app_root()

    assert {:ok, first} = Mitos.AppManifest.from_json(manifest(), app_root: root)

    File.write!(Path.join(root, "pipelines/resize.md"), "# Resize v2\n")

    assert {:ok, changed} = Mitos.AppManifest.from_json(manifest(), app_root: root)
    refute changed.app_id == first.app_id
  end

  test "accepts frozen pipeline hashes but recomputes them from bundled files" do
    root = app_root()
    supplied_hash = "sha256:" <> String.duplicate("0", 64)

    assert {:ok, app} =
             manifest()
             |> put_in([:pipelines, Access.at(0), :sha256], supplied_hash)
             |> Mitos.AppManifest.from_json(app_root: root)

    assert [%{"sha256" => actual_hash}] = app.pipelines
    assert actual_hash =~ ~r/^sha256:[0-9a-f]{64}$/
    refute actual_hash == supplied_hash
  end

  test "rejects pipeline files that resolve outside the app root through symlinks" do
    root = app_root()

    outside =
      Path.join(System.tmp_dir!(), "mitos-app-outside-#{System.unique_integer([:positive])}")

    File.mkdir_p!(outside)
    outside_pipeline = Path.join(outside, "resize.md")
    File.write!(outside_pipeline, "# Outside\n")
    File.rm!(Path.join(root, "pipelines/resize.md"))
    File.ln_s!(outside_pipeline, Path.join(root, "pipelines/resize.md"))

    on_exit(fn ->
      File.rm_rf!(root)
      File.rm_rf!(outside)
    end)

    assert {:error, {:invalid_application, "pipelines.0.file escapes the application root"}} =
             Mitos.AppManifest.from_json(manifest(), app_root: root)
  end

  test "maps an entrypoint run to a canonical work-pool task" do
    root = app_root()
    assert {:ok, app} = Mitos.AppManifest.from_json(manifest(), app_root: root)

    assert {:ok, task} =
             Mitos.AppManifest.task_for_entrypoint(
               app,
               "resize",
               %{"image" => %{"path" => "input.png"}, "width" => 320, "watermark" => true},
               task_id: "task-resize-1",
               source: "connector:webhook",
               idempotency_key: "webhook-delivery-1",
               reply_ref: "reply-thread-1",
               tenant: %{"org" => "buyer", "project" => "campaign"}
             )

    assert task["id"] == "task-resize-1"
    assert task["pipeline"] == "resize-and-deliver"
    assert task["source"] == "connector:webhook"
    assert task["tenant"] == %{"org" => "buyer", "project" => "campaign"}
    assert task["budget"] == %{"usd" => 2, "wall_ms" => 300_000}
    assert task["payload"]["workspace"] == %{"kind" => "dataset"}
    assert task["payload"]["inputs"]["width"] == 320
    assert task["payload"]["dedupe_key"] == "webhook-delivery-1"
    assert task["payload"]["reply_ref"] == "reply-thread-1"
    assert task["payload"]["constraints"] == ["stay within sRGB"]
    assert task["provenance"]["app_id"] == app.app_id
    assert task["provenance"]["entrypoint"] == "resize"
  end

  test "validates entrypoint inputs against the supported schema subset" do
    root = app_root()
    assert {:ok, app} = Mitos.AppManifest.from_json(manifest(), app_root: root)

    assert :ok =
             Mitos.AppManifest.validate_inputs(app, "resize", %{
               "image" => "input.png",
               "width" => 320
             })

    assert {:error, {:invalid_application_input, "missing required input width"}} =
             Mitos.AppManifest.validate_inputs(app, "resize", %{"image" => "input.png"})

    assert {:error, {:invalid_application_input, "input width has invalid type"}} =
             Mitos.AppManifest.validate_inputs(app, "resize", %{
               "image" => "input.png",
               "width" => "320"
             })

    assert {:error, {:invalid_application_input, "unknown input height"}} =
             Mitos.AppManifest.validate_inputs(app, "resize", %{
               "image" => "input.png",
               "width" => 320,
               "height" => 200
             })
  end

  test "normalizes a workspace object with a ref and resolves it onto the task" do
    root = app_root()

    manifest =
      manifest()
      |> put_in([:entrypoints, Access.at(0), :workspace], %{kind: "repo", ref: "./repo"})

    assert {:ok, app} = Mitos.AppManifest.from_json(manifest, app_root: root)

    assert [%{"workspace" => %{"kind" => "repo", "ref" => "./repo"}}] = app.entrypoints

    assert {:ok, task} =
             Mitos.AppManifest.task_for_entrypoint(
               app,
               "resize",
               %{"image" => "i.png", "width" => 10},
               workspace: %{"ref" => "https://example.test/repo.git", "refspec" => "main"}
             )

    assert task["payload"]["workspace"] == %{
             "kind" => "repo",
             "ref" => "https://example.test/repo.git",
             "refspec" => "main"
           }
  end

  test "normalizes entrypoint parameters with defaults and types" do
    root = app_root()

    manifest =
      manifest()
      |> put_in(
        [:entrypoints, Access.at(0), :parameters],
        [
          %{name: "policy", type: "string", default: "default", description: "how"},
          %{name: "workers", type: "integer", default: 1}
        ]
      )

    assert {:ok, app} = Mitos.AppManifest.from_json(manifest, app_root: root)

    assert [%{"parameters" => [first, second]}] = app.entrypoints

    assert first == %{
             "name" => "policy",
             "type" => "string",
             "default" => "default",
             "description" => "how"
           }

    assert second == %{"name" => "workers", "type" => "integer", "default" => 1}
  end

  test "rejects malformed entrypoint parameters before task construction" do
    root = app_root()

    assert {:error, {:invalid_application, "entrypoints.0.parameters must be a list"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :parameters], %{name: "x"})
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "entrypoints.0.parameters.0.ui")}} =
             manifest()
             |> put_in(
               [:entrypoints, Access.at(0), :parameters],
               [%{name: "x", type: "string", ui: "slider"}]
             )
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "entrypoints.0.parameters.0.name is required"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :parameters], [%{type: "string"}])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "entrypoints.0.parameters.0.type is unsupported"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :parameters], [%{name: "x", type: "enum"}])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "entrypoints.0.parameters.0.default has invalid type"}} =
             manifest()
             |> put_in(
               [:entrypoints, Access.at(0), :parameters],
               [%{name: "x", type: "integer", default: "fast"}]
             )
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(duplicate entrypoints.0.parameters name "x")}} =
             manifest()
             |> put_in(
               [:entrypoints, Access.at(0), :parameters],
               [%{name: "x", type: "string"}, %{name: "x", type: "string"}]
             )
             |> Mitos.AppManifest.from_json(app_root: root)
  end

  test "applies parameter defaults and overrides on task construction" do
    root = app_root()

    manifest =
      manifest()
      |> put_in(
        [:entrypoints, Access.at(0), :parameters],
        [
          %{name: "policy", type: "string", default: "default"},
          %{name: "workers", type: "integer", default: 1}
        ]
      )

    assert {:ok, app} = Mitos.AppManifest.from_json(manifest, app_root: root)

    assert {:ok, task} =
             Mitos.AppManifest.task_for_entrypoint(
               app,
               "resize",
               %{"image" => "i.png", "width" => 10},
               parameters: %{"workers" => 4}
             )

    assert task["payload"]["parameters"] == %{"policy" => "default", "workers" => 4}

    assert {:error, {:invalid_application_input, "unknown parameter tempo"}} =
             Mitos.AppManifest.task_for_entrypoint(
               app,
               "resize",
               %{"image" => "i.png", "width" => 10},
               parameters: %{"tempo" => "allegro"}
             )

    assert {:error, {:invalid_application_input, "parameter workers has invalid type"}} =
             Mitos.AppManifest.task_for_entrypoint(
               app,
               "resize",
               %{"image" => "i.png", "width" => 10},
               parameters: %{"workers" => "four"}
             )
  end

  test "rejects malformed entrypoint defaults before task construction" do
    root = app_root()

    assert {:error, {:invalid_application, "entrypoints.0.defaults must be an object"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :defaults], "budget:2")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "entrypoints.0.defaults.budget must be an object"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :defaults, :budget], ["usd:2"])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application, ~s(unsupported field "entrypoints.0.defaults.budget.cpu")}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :defaults, :budget], %{cpu: 2})
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "entrypoints.0.defaults.budget.wall_ms must be a non-negative number"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :defaults, :budget, :wall_ms], -1)
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "entrypoints.0.defaults.constraints must be a list"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :defaults, :constraints], "stay within sRGB")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "entrypoints.0.defaults.success_criteria.0 must be a non-empty string"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :defaults, :success_criteria], [""])
             |> Mitos.AppManifest.from_json(app_root: root)
  end

  test "rejects malformed entrypoint input schema declarations" do
    root = app_root()

    shorthand_schema = %{
      type: "object",
      required: ["image", "width"],
      additionalProperties: false,
      image: %{type: "file"},
      width: %{type: "int"}
    }

    assert {:ok, shorthand_app} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :inputs, :schema], shorthand_schema)
             |> Mitos.AppManifest.from_json(app_root: root)

    assert :ok =
             Mitos.AppManifest.validate_inputs(shorthand_app, "resize", %{
               "image" => "input.png",
               "width" => 320
             })

    assert {:error, {:invalid_application, "entrypoints.0.inputs.schema.required must be a list"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :inputs, :schema, :required], "image")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application, "entrypoints.0.inputs.schema.properties must be an object"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :inputs, :schema, :properties], [])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "entrypoints.0.inputs.schema.properties.width.type is unsupported"}} =
             manifest()
             |> put_in(
               [:entrypoints, Access.at(0), :inputs, :schema, :properties, :width, :type],
               "uint32"
             )
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "entrypoints.0.inputs.schema.width must be an object"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :inputs, :schema], %{
               type: "object",
               width: "int"
             })
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "entrypoints.0.inputs.schema.additionalProperties must be a boolean"}} =
             manifest()
             |> put_in(
               [:entrypoints, Access.at(0), :inputs, :schema, :additionalProperties],
               "false"
             )
             |> Mitos.AppManifest.from_json(app_root: root)
  end

  test "rejects unknown fields and secret-bearing manifest keys" do
    root = app_root()

    assert {:error, {:invalid_application, ~s(unsupported field "x-extra")}} =
             manifest()
             |> Map.put(:"x-extra", true)
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "capabilities.0.extra")}} =
             manifest()
             |> put_in([:capabilities, Access.at(0), :extra], true)
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "pipelines.0.title")}} =
             manifest()
             |> put_in([:pipelines, Access.at(0), :title], "Resize")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "entrypoints.0.schedule")}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :schedule], %{type: "cron"})
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "entrypoints.0.inputs.ui")}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :inputs, :ui], %{widget: "file"})
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "application manifests must not contain secret field io.inputs.0.api_key"}} =
             manifest()
             |> put_in([:io, :inputs], [
               %{connector: "webhook", entrypoint: "resize", api_key: "x"}
             ])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "application manifests must not contain secret field io.outputs.0.headers.authorization"}} =
             manifest()
             |> put_in([:io, :outputs], [
               %{
                 sink: "webhook",
                 entrypoint: "resize",
                 headers: %{authorization: "Bearer must-not-enter-manifest"}
               }
             ])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "application manifests must not contain credential-bearing URL field io.outputs.0.url"}} =
             manifest()
             |> put_in([:io, :outputs], [
               %{
                 sink: "webhook",
                 entrypoint: "resize",
                 url: "https://hook-user:hook-pass@hooks.example.test/deliver"
               }
             ])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "application manifests must not contain credential-bearing URL query field io.outputs.0.url"}} =
             manifest()
             |> put_in([:io, :outputs], [
               %{
                 sink: "webhook",
                 entrypoint: "resize",
                 url: "https://hooks.example.test/deliver?token=must-not-enter-manifest"
               }
             ])
             |> Mitos.AppManifest.from_json(app_root: root)
  end

  test "rejects invalid references and unsafe pipeline files" do
    root = app_root()

    assert {:error, {:invalid_application, "entrypoints.0.pipeline references unknown pipeline"}} =
             manifest()
             |> put_in([:entrypoints, Access.at(0), :pipeline], "missing")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "pipelines.0.file must not contain .."}} =
             manifest()
             |> put_in([:pipelines, Access.at(0), :file], "../escape.md")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "pipelines.0.file must not overwrite application control files"}} =
             manifest()
             |> put_in([:pipelines, Access.at(0), :file], "app.json")
             |> Mitos.AppManifest.from_json(app_root: root)
  end

  test "rejects invalid service and requirement declarations" do
    root = app_root()

    assert {:error, {:invalid_application, "services.requires must be a list"}} =
             manifest()
             |> put_in([:services, :requires], "render-pool")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "services.units")}} =
             manifest()
             |> put_in([:services, :units], [%{id: "render-pool"}])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "pipelines.0.requires.services must be a list"}} =
             manifest()
             |> put_in([:pipelines, Access.at(0), :requires], %{services: "render-pool"})
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "pipelines.0.requires.priority")}} =
             manifest()
             |> put_in([:pipelines, Access.at(0), :requires], %{priority: "high"})
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, ~s(unsupported field "requirements.foo")}} =
             manifest()
             |> put_in([:requirements, :foo], "bar")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "requirements.trust_tier must be one of T0, T1, T2"}} =
             manifest()
             |> put_in([:requirements, :trust_tier], "T9")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application, "requirements.connectors.0 must be a non-empty string"}} =
             manifest()
             |> put_in([:requirements, :connectors], [""])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "requirements.resources must be an object"}} =
             manifest()
             |> put_in([:requirements, :resources], ["cpu:2"])
             |> Mitos.AppManifest.from_json(app_root: root)
  end

  test "rejects invalid io bindings and under-declared connector requirements" do
    root = app_root()

    assert {:error, {:invalid_application, ~s(unsupported field "io.defaults")}} =
             manifest()
             |> put_in([:io, :defaults], [])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "io.inputs.0.connector is required"}} =
             manifest()
             |> put_in([:io, :inputs], [%{entrypoint: "resize"}])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "io.outputs.0.sink is required"}} =
             manifest()
             |> put_in([:io, :outputs], [%{entrypoint: "resize"}])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application,
             "requirements.connectors must include io.inputs connector webhook"}} =
             manifest()
             |> put_in([:requirements, :connectors], ["chat"])
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "io.inputs.0.select must be an object"}} =
             manifest()
             |> put_in([:io, :inputs, Access.at(0), :select], "explicit")
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error, {:invalid_application, "io.inputs.0.select.mode is unsupported"}} =
             manifest()
             |> put_in([:io, :inputs, Access.at(0), :select], %{mode: "smart"})
             |> Mitos.AppManifest.from_json(app_root: root)

    assert {:error,
            {:invalid_application, "io.inputs.0.select.pipeline must match entrypoint pipeline"}} =
             manifest()
             |> put_in([:io, :inputs, Access.at(0), :select], %{pipeline: "other-flow"})
             |> Mitos.AppManifest.from_json(app_root: root)
  end

  test "applies app-scoped tenant bindings sidecar without changing identity" do
    root = app_root()
    File.write!(Path.join(root, "app.json"), Jason.encode!(manifest()))
    assert {:ok, base_app} = Mitos.AppManifest.load(root)

    sidecar_io = %{
      inputs: [%{connector: "chat", entrypoint: "resize"}],
      outputs: [
        %{
          sink: "reply",
          entrypoint: "resize",
          on: "run.done",
          reply_to: "input.reply_ref"
        }
      ]
    }

    File.write!(
      Path.join(root, "bindings.json"),
      Jason.encode!(%{
        "app_id" => base_app.app_id,
        "io" => sidecar_io,
        "credential_scope" => "tenant_control_plane",
        "defaults_overridden?" => true
      })
    )

    assert {:ok, app} = Mitos.AppManifest.load(root)
    assert app.app_id == base_app.app_id
    assert app.raw["io"] == base_app.raw["io"]
    assert [%{"connector" => "chat", "entrypoint" => "resize"}] = app.io["inputs"]

    assert [
             %{
               "sink" => "reply",
               "entrypoint" => "resize",
               "on" => "run.done",
               "reply_to" => "input.reply_ref"
             }
           ] = app.io["outputs"]
  end

  test "fails closed on unscoped or malformed tenant bindings sidecars" do
    root = app_root()
    File.write!(Path.join(root, "app.json"), Jason.encode!(manifest()))
    assert {:ok, app} = Mitos.AppManifest.load(root)

    File.write!(Path.join(root, "bindings.json"), Jason.encode!(%{io: %{}}))

    assert {:error, {:invalid_application, "bindings.json app_id is required"}} =
             Mitos.AppManifest.load(root)

    assert {:ok, ignored_sidecar} = Mitos.AppManifest.load(root, apply_bindings: false)
    assert ignored_sidecar.app_id == app.app_id
    assert ignored_sidecar.io == app.io

    File.write!(
      Path.join(root, "bindings.json"),
      Jason.encode!(%{app_id: "app:sha256:other", io: %{}})
    )

    assert {:error, {:invalid_application, "bindings.json app_id mismatch"}} =
             Mitos.AppManifest.load(root)

    File.write!(Path.join(root, "bindings.json"), Jason.encode!(%{app_id: app.app_id}))

    assert {:error, {:invalid_application, "bindings.json io is required"}} =
             Mitos.AppManifest.load(root)

    File.write!(
      Path.join(root, "bindings.json"),
      Jason.encode!(%{app_id: app.app_id, io: [], extra: true})
    )

    assert {:error, {:invalid_application, ~s(unsupported field "bindings.json.extra")}} =
             Mitos.AppManifest.load(root)

    File.write!(
      Path.join(root, "bindings.json"),
      Jason.encode!(%{app_id: app.app_id, io: %{inputs: [], outputs: [], defaults: %{}}})
    )

    assert {:error, {:invalid_application, ~s(unsupported field "io.defaults")}} =
             Mitos.AppManifest.load(root)
  end

  test "rejects symlinked tenant bindings sidecars" do
    root = app_root()
    File.write!(Path.join(root, "app.json"), Jason.encode!(manifest()))
    assert {:ok, app} = Mitos.AppManifest.load(root)

    outside =
      Path.join(System.tmp_dir!(), "mitos-bindings-outside-#{System.unique_integer([:positive])}")

    File.mkdir_p!(outside)

    File.write!(
      Path.join(outside, "bindings.json"),
      Jason.encode!(%{app_id: app.app_id, io: %{inputs: [], outputs: []}})
    )

    File.ln_s!(Path.join(outside, "bindings.json"), Path.join(root, "bindings.json"))

    on_exit(fn ->
      File.rm_rf!(root)
      File.rm_rf!(outside)
    end)

    assert {:error, {:invalid_application, "could not read bindings.json: :symlink"}} =
             Mitos.AppManifest.load(root)
  end

  test "loads a YAML app manifest with flow collections" do
    root = app_root()

    File.write!(
      Path.join(root, "app.yaml"),
      """
      name: image-resize-yaml
      version: 1.2.0
      description: Resize images from YAML
      authors: [design-tools@example.test]
      capabilities:
        - {ref: "oci://registry.test/image-resize@sha256:abc123", as: image-resize}
      pipelines:
        - {id: resize-and-deliver, file: pipelines/resize.md}
      entrypoints:
        - id: resize
          pipeline: resize-and-deliver
          title: Resize an image
          workspace: dataset
          inputs:
            schema:
              type: object
              required: [image, width]
              additionalProperties: false
              properties:
                image: {type: file}
                width: {type: int}
          defaults:
            budget: {usd: 2, wall_ms: 300000}
      io:
        inputs:
          - {connector: webhook, path: /apps/resize/runs, entrypoint: resize}
        outputs:
          - {sink: web, on: run.done, entrypoint: resize}
      services:
        requires: [render-pool]
      requirements:
        harnesses: [codex]
        egress: ["api.figma.com"]
        trust_tier: T1
      """
    )

    assert {:ok, app} = Mitos.AppManifest.load(root)
    assert app.name == "image-resize-yaml"
    assert app.version == "1.2.0"
    assert app.authors == ["design-tools@example.test"]

    assert app.capabilities == [
             %{"ref" => "oci://registry.test/image-resize@sha256:abc123", "as" => "image-resize"}
           ]

    assert [%{"id" => "resize", "inputs" => %{"schema" => schema}}] = app.entrypoints
    assert schema["required"] == ["image", "width"]
    assert get_in(schema, ["properties", "width", "type"]) == "int"
    assert hd(app.io["inputs"])["path"] == "/apps/resize/runs"
    assert app.services["requires"] == ["render-pool"]
  end
end
