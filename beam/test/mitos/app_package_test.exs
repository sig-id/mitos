defmodule Mitos.AppPackageTest do
  use ExUnit.Case, async: false

  setup do
    root = unique_tmp_dir("app-package-app")
    package_dir = unique_tmp_dir("app-package-oci")
    install_dir = unique_tmp_dir("app-package-install")
    workloads = unique_tmp_dir("app-package-workloads")
    traces = unique_tmp_dir("app-package-traces")

    File.mkdir_p!(Path.join(root, "pipelines"))
    File.mkdir_p!(Path.join([root, "capabilities", "ship"]))
    File.mkdir_p!(workloads)
    File.mkdir_p!(traces)

    previous_workloads_dir = Application.get_env(:mitos, :workloads_dir)
    Application.put_env(:mitos, :workloads_dir, workloads)

    on_exit(fn ->
      restore_env(:workloads_dir, previous_workloads_dir)
      writable_rm_rf(root)
      writable_rm_rf(package_dir)
      writable_rm_rf(install_dir)
      writable_rm_rf(workloads)
      writable_rm_rf(traces)
    end)

    {:ok,
     root: root,
     package_dir: package_dir,
     install_dir: install_dir,
     workloads: workloads,
     traces: traces}
  end

  test "publishes a local app as an OCI image layout with frozen capability refs", %{
    root: root,
    package_dir: package_dir
  } do
    write_app!(root)

    assert {:ok, package} = Mitos.AppPackage.publish_local(root, package_dir, tag: "ship-1")
    assert package["app_id"] =~ ~r/^app:sha256:[0-9a-f]{64}$/
    assert package["layout_dir"] == Path.expand(package_dir)
    assert package["manifest_digest"] =~ ~r/^sha256:[0-9a-f]{64}$/

    assert %{"imageLayoutVersion" => "1.0.0"} =
             package_dir |> Path.join("oci-layout") |> File.read!() |> Jason.decode!()

    index = package_dir |> Path.join("index.json") |> File.read!() |> Jason.decode!()
    assert [manifest_descriptor] = index["manifests"]
    assert manifest_descriptor["artifactType"] == "application/vnd.mitos.application.v1+json"
    assert manifest_descriptor["annotations"]["dev.mitos.app_id"] == package["app_id"]
    assert manifest_descriptor["annotations"]["org.opencontainers.image.ref.name"] == "ship-1"

    oci_manifest = decode_blob!(package_dir, manifest_descriptor)
    assert oci_manifest["artifactType"] == "application/vnd.mitos.application.v1+json"
    assert oci_manifest["annotations"]["dev.mitos.app_id"] == package["app_id"]

    assert [
             app_manifest_descriptor,
             pipeline_descriptor
           ] = oci_manifest["layers"]

    assert app_manifest_descriptor["mediaType"] ==
             "application/vnd.mitos.application.manifest.v1+json"

    assert pipeline_descriptor["mediaType"] == "text/markdown; charset=utf-8"
    assert pipeline_descriptor["annotations"]["dev.mitos.pipeline.id"] == "ship-flow"

    frozen_manifest = decode_blob!(package_dir, app_manifest_descriptor)
    assert [%{"as" => "ship-cap", "ref" => cap_ref}] = frozen_manifest["capabilities"]
    assert cap_ref =~ ~r/^cap:sha256:[0-9a-f]{64}$/
    assert [%{"id" => "ship-flow", "sha256" => "sha256:" <> _}] = frozen_manifest["pipelines"]
  end

  test "published app identity changes when bundled content changes", %{
    root: root,
    package_dir: package_dir
  } do
    write_app!(root)

    first_dir = Path.join(package_dir, "first")
    second_dir = Path.join(package_dir, "second")

    assert {:ok, first} = Mitos.AppPackage.publish_local(root, first_dir)
    File.write!(Path.join(root, "pipelines/ship.md"), "# Pipeline: ship-flow\n\nChanged.\n")
    assert {:ok, second} = Mitos.AppPackage.publish_local(root, second_dir)

    refute second["app_id"] == first["app_id"]
  end

  test "published app identity changes when local capability content changes", %{
    root: root,
    package_dir: package_dir
  } do
    write_app!(root)

    first_dir = Path.join(package_dir, "first")
    second_dir = Path.join(package_dir, "second")

    assert {:ok, first} = Mitos.AppPackage.publish_local(root, first_dir)
    write_capability!(root, capability_name: "ship-cap-v2")
    assert {:ok, second} = Mitos.AppPackage.publish_local(root, second_dir)

    refute second["app_id"] == first["app_id"]
  end

  test "installs a local OCI layout back into an app directory with frozen refs", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root)
    assert {:ok, package} = Mitos.AppPackage.publish_local(root, package_dir)

    assert {:ok, install} = Mitos.AppPackage.install_local(package_dir, install_dir)
    assert install["app_id"] == package["app_id"]
    assert install["app_dir"] == Path.expand(install_dir)
    assert install["manifest_digest"] == package["manifest_digest"]

    assert {:ok, app} = Mitos.AppManifest.load(install_dir)
    assert app.app_id == package["app_id"]
    assert [%{"as" => "ship-cap", "ref" => cap_ref}] = app.capabilities
    assert cap_ref =~ ~r/^cap:sha256:[0-9a-f]{64}$/
    assert File.read!(Path.join(install_dir, "pipelines/ship.md")) =~ "Pipeline: ship-flow"
    refute File.exists?(Path.join(install_dir, "capabilities/ship/workload.json"))
  end

  test "installed app runs when its frozen capability is in the local catalog", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir,
    workloads: workloads,
    traces: traces
  } do
    write_app!(root)
    write_catalog_workload!(workloads)
    assert {:ok, package} = Mitos.AppPackage.publish_local(root, package_dir)
    assert {:ok, install} = Mitos.AppPackage.install_local(package_dir, install_dir)
    assert install["app_id"] == package["app_id"]

    assert {:ok, result} =
             Mitos.AppRunner.run(
               install_dir,
               "ship",
               %{"objective" => "from installed"},
               task_id: "installed-app-task",
               trace_base_dir: traces
             )

    assert result["app_id"] == package["app_id"]
    assert result["status"] == "completed"
    assert result["deliverable_summary"] =~ "FAKE_FINAL: App from installed"

    assert [
             %{
               "id" => "ship-cap",
               "capability_id" => "ship-cap",
               "run_id" => run_id
             }
           ] = result["pipeline"]["steps"]

    assert is_binary(run_id)
  end

  test "install applies tenant io binding overrides without changing app identity", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir,
    workloads: workloads,
    traces: traces
  } do
    write_app!(root,
      io: %{
        inputs: [%{connector: "web", entrypoint: "ship", path: "/default/ship"}],
        outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
      }
    )

    write_catalog_workload!(workloads)
    assert {:ok, package} = Mitos.AppPackage.publish_local(root, package_dir)

    override_io = %{
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

    assert {:ok, install} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               io: override_io,
               available_connectors: ["webhook"],
               available_sinks: ["reply"]
             )

    assert install["app_id"] == package["app_id"]
    assert install["bindings_applied?"] == true

    assert get_in(install, ["io", "inputs"]) == [
             %{
               "connector" => "webhook",
               "entrypoint" => "ship",
               "path" => "/tenant/ship",
               "select" => %{"mode" => "explicit", "pipeline" => "ship-flow"}
             }
           ]

    assert File.exists?(Path.join(install_dir, "bindings.json"))

    assert {:ok, app} = Mitos.AppManifest.load(install_dir)
    assert app.app_id == package["app_id"]
    assert [%{"connector" => "webhook", "path" => "/tenant/ship"}] = app.io["inputs"]
    assert [%{"sink" => "reply", "reply_to" => "input.reply_ref"}] = app.io["outputs"]

    assert {:ok, result} =
             Mitos.AppRunner.run(
               install_dir,
               "ship",
               %{"objective" => "from bound install"},
               task_id: "installed-bound-task",
               trace_base_dir: traces,
               reply_ref: "thread-tenant-1"
             )

    assert result["app_id"] == package["app_id"]

    assert [
             %{
               "receipt" => %{
                 "sink" => "reply",
                 "destination" => "thread-tenant-1",
                 "status" => "sent"
               },
               "payload" => %{"reply_ref" => "thread-tenant-1"}
             }
           ] = result["deliveries"]
  end

  test "install fails closed when tenant io binding sinks are unavailable", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root)
    assert {:ok, _package} = Mitos.AppPackage.publish_local(root, package_dir)

    override_io = %{
      inputs: [%{connector: "webhook", entrypoint: "ship", path: "/tenant/ship"}],
      outputs: [%{sink: "reply", entrypoint: "ship", on: "run.done"}]
    }

    assert {:error, {:invalid_application_package, "missing required connector webhook"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir, io: override_io)

    refute File.exists?(Path.join(install_dir, "app.json"))

    assert {:error, {:invalid_application_package, "missing required sink reply"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               io: override_io,
               available_connectors: ["webhook"]
             )

    refute File.exists?(Path.join(install_dir, "app.json"))

    assert {:error, {:invalid_application_package, "missing required sink reply"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               io: override_io,
               available_connectors: ["webhook"],
               available_sinks: ["web"]
             )

    refute File.exists?(Path.join(install_dir, "app.json"))

    assert {:ok, install} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               io: override_io,
               available_connectors: ["webhook"],
               available_sinks: ["reply"]
             )

    assert install["bindings_applied?"] == true
    assert File.exists?(Path.join(install_dir, "bindings.json"))
  end

  test "install fails closed when default io bindings are unavailable", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root,
      io: %{
        inputs: [%{connector: "webhook", entrypoint: "ship", path: "/default/ship"}],
        outputs: [%{sink: "reply", entrypoint: "ship", on: "run.done"}]
      }
    )

    assert {:ok, _package} = Mitos.AppPackage.publish_local(root, package_dir)

    assert {:error, {:invalid_application_package, "missing required connector webhook"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               available_connectors: ["chat"],
               available_sinks: ["reply"]
             )

    refute File.exists?(Path.join(install_dir, "app.json"))

    assert {:error, {:invalid_application_package, "missing required sink reply"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               available_connectors: ["webhook"],
               available_sinks: ["web"]
             )

    refute File.exists?(Path.join(install_dir, "app.json"))

    assert {:ok, install} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               available_connectors: ["webhook"],
               available_sinks: ["reply"]
             )

    assert install["bindings_applied?"] == false
    assert File.exists?(Path.join(install_dir, "app.json"))
  end

  test "install rejects credential-shaped binding override fields", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root)
    assert {:ok, _package} = Mitos.AppPackage.publish_local(root, package_dir)

    assert {:error,
            {:invalid_application_package,
             "application manifests must not contain secret field io.inputs.0.api_key"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               io: %{
                 inputs: [
                   %{
                     connector: "webhook",
                     entrypoint: "ship",
                     api_key: "must-not-enter-manifest"
                   }
                 ],
                 outputs: []
               }
             )

    refute File.exists?(Path.join(install_dir, "bindings.json"))
  end

  test "installed app runs with pinned OCI refs resolved from the local catalog", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir,
    workloads: workloads,
    traces: traces
  } do
    image_ref = "oci://registry.test/ship-cap@sha256:abc123"
    write_app!(root, cap_ref: image_ref)
    write_catalog_workload!(workloads, image: image_ref)
    assert {:ok, package} = Mitos.AppPackage.publish_local(root, package_dir)
    assert {:ok, install} = Mitos.AppPackage.install_local(package_dir, install_dir)
    assert install["app_id"] == package["app_id"]

    assert {:ok, app} = Mitos.AppManifest.load(install_dir)
    assert [%{"ref" => ^image_ref}] = app.capabilities

    assert {:ok, result} =
             Mitos.AppRunner.run(
               install_dir,
               "ship",
               %{"objective" => "from installed oci"},
               task_id: "installed-app-oci-task",
               trace_base_dir: traces
             )

    assert result["status"] == "completed"
    assert result["deliverable_summary"] =~ "FAKE_FINAL: App from installed oci"
    assert [%{"capability_id" => "ship-cap"}] = result["pipeline"]["steps"]
  end

  test "install verifies OCI blob digests before writing files", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root)
    assert {:ok, package} = Mitos.AppPackage.publish_local(root, package_dir)
    [pipeline_descriptor] = package["blobs"]["pipelines"]
    File.write!(blob_path(package_dir, pipeline_descriptor["digest"]), "tampered")

    assert {:error, {:invalid_application_package, "descriptor digest mismatch"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir)

    refute File.exists?(Path.join(install_dir, "app.json"))
  end

  test "install requires pipeline layer id and path annotations to match", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root)
    assert {:ok, _package} = Mitos.AppPackage.publish_local(root, package_dir)

    index_path = Path.join(package_dir, "index.json")
    index = index_path |> File.read!() |> Jason.decode!()
    [manifest_descriptor] = index["manifests"]
    oci_manifest = decode_blob!(package_dir, manifest_descriptor)
    [app_manifest_descriptor, pipeline_descriptor] = oci_manifest["layers"]

    pipeline_descriptor =
      put_in(
        pipeline_descriptor,
        ["annotations", "dev.mitos.pipeline.path"],
        "pipelines/other.md"
      )

    oci_manifest = Map.put(oci_manifest, "layers", [app_manifest_descriptor, pipeline_descriptor])

    manifest_descriptor = rewrite_descriptor!(package_dir, manifest_descriptor, oci_manifest)
    File.write!(index_path, Jason.encode!(Map.put(index, "manifests", [manifest_descriptor])))

    assert {:error, {:invalid_application_package, "pipeline layer \"ship-flow\" is missing"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir)

    refute File.exists?(Path.join(install_dir, "app.json"))
  end

  test "install rejects pipeline layers that would overwrite application control files", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root)
    assert {:ok, _package} = Mitos.AppPackage.publish_local(root, package_dir)

    index_path = Path.join(package_dir, "index.json")
    index = index_path |> File.read!() |> Jason.decode!()
    [manifest_descriptor] = index["manifests"]
    oci_manifest = decode_blob!(package_dir, manifest_descriptor)
    [app_manifest_descriptor, pipeline_descriptor] = oci_manifest["layers"]

    frozen_manifest =
      package_dir
      |> decode_blob!(app_manifest_descriptor)
      |> put_in(["pipelines", Access.at(0), "file"], "app.json")

    app_id = app_id_for(frozen_manifest)

    app_manifest_descriptor =
      rewrite_descriptor!(package_dir, app_manifest_descriptor, frozen_manifest)

    oci_manifest =
      oci_manifest
      |> put_in(["annotations", "dev.mitos.app_id"], app_id)
      |> Map.put("layers", [app_manifest_descriptor, pipeline_descriptor])

    manifest_descriptor =
      package_dir
      |> rewrite_descriptor!(manifest_descriptor, oci_manifest)
      |> put_in(["annotations", "dev.mitos.app_id"], app_id)

    File.write!(index_path, Jason.encode!(Map.put(index, "manifests", [manifest_descriptor])))

    assert {:error,
            {:invalid_application_package,
             "pipeline files must not overwrite application control files"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir)

    refute File.exists?(Path.join(install_dir, "app.json"))
  end

  test "install refuses an existing non-empty destination", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root)
    assert {:ok, _package} = Mitos.AppPackage.publish_local(root, package_dir)
    File.mkdir_p!(install_dir)
    File.write!(Path.join(install_dir, "existing"), "keep")

    assert {:error, {:invalid_application_package, "destination must be empty"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir)
  end

  test "install fails closed when declared connectors or harnesses are unavailable", %{
    root: root,
    package_dir: package_dir,
    install_dir: install_dir
  } do
    write_app!(root,
      io: %{
        inputs: [%{connector: "webhook", entrypoint: "ship"}],
        outputs: [%{sink: "web", entrypoint: "ship", on: "run.done"}]
      },
      requirements: %{connectors: ["webhook"], harnesses: ["fake"]}
    )

    assert {:ok, _package} = Mitos.AppPackage.publish_local(root, package_dir)

    assert {:error, {:invalid_application_package, "missing required connector webhook"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir)

    refute File.exists?(Path.join(install_dir, "app.json"))

    assert {:error, {:invalid_application_package, "missing required harness fake"}} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               available_connectors: ["webhook"]
             )

    refute File.exists?(Path.join(install_dir, "app.json"))

    assert {:ok, install} =
             Mitos.AppPackage.install_local(package_dir, install_dir,
               available_connectors: ["webhook"],
               available_harnesses: ["fake"]
             )

    assert File.exists?(Path.join(install_dir, "app.json"))
    assert install["app_id"] =~ ~r/^app:sha256:[0-9a-f]{64}$/
  end

  test "refuses an existing non-empty destination", %{root: root, package_dir: package_dir} do
    write_app!(root)
    File.mkdir_p!(package_dir)
    File.write!(Path.join(package_dir, "existing"), "keep")

    assert {:error, {:invalid_application_package, "destination must be empty"}} =
             Mitos.AppPackage.publish_local(root, package_dir)
  end

  test "rejects mutable OCI capability refs", %{root: root, package_dir: package_dir} do
    write_app!(root, cap_ref: "oci://registry.test/ship:latest")

    assert {:error,
            {:invalid_application_package, "OCI capability refs must be content-addressed"}} =
             Mitos.AppPackage.publish_local(root, package_dir)
  end

  defp write_app!(root, opts \\ []) do
    cap_ref = Keyword.get(opts, :cap_ref, "capabilities/ship/workload.json")

    File.write!(Path.join(root, "pipelines/ship.md"), """
    # Pipeline: ship-flow

    Goal: produce a backend application result.

    ## Stages
    1. **ship-cap** - run the local capability package.
    """)

    write_capability!(root)

    manifest =
      %{
        name: "ship-app",
        version: "1.0.0",
        capabilities: [
          %{ref: cap_ref, as: "ship-cap"}
        ],
        pipelines: [
          %{id: "ship-flow", file: "pipelines/ship.md"}
        ],
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
                  objective: %{type: "string"}
                }
              }
            }
          }
        ],
        io: Keyword.get(opts, :io, %{inputs: [], outputs: []}),
        services: %{requires: [], wants: []}
      }
      |> maybe_put(:requirements, Keyword.get(opts, :requirements))

    File.write!(Path.join(root, "app.json"), Jason.encode!(manifest))
  end

  defp write_capability!(root, opts \\ []) do
    File.write!(
      Path.join(root, "capabilities/ship/workload.json"),
      Jason.encode!(workload_json(opts))
    )
  end

  defp write_catalog_workload!(workloads, opts \\ []) do
    File.write!(Path.join(workloads, "ship-cap.json"), Jason.encode!(workload_json(opts)))
  end

  defp workload_json(opts) do
    %{
      provider: "fake",
      template: Keyword.get(opts, :template, "App {{objective}}"),
      capability: %{
        name: Keyword.get(opts, :capability_name, "ship-cap"),
        image: opts[:image],
        harnesses: ["fake"],
        trust_tier: "T1"
      }
    }
  end

  defp rewrite_descriptor!(layout_dir, descriptor, json) do
    bytes = canonical_json(json)
    digest = "sha256:" <> sha256_hex(bytes)
    File.write!(blob_path(layout_dir, digest), bytes)

    descriptor
    |> Map.put("digest", digest)
    |> Map.put("size", byte_size(bytes))
  end

  defp app_id_for(frozen_manifest),
    do: "app:sha256:" <> sha256_hex(canonical_json(frozen_manifest))

  defp decode_blob!(layout_dir, %{"digest" => "sha256:" <> hex}) do
    layout_dir
    |> blob_path("sha256:" <> hex)
    |> File.read!()
    |> Jason.decode!()
  end

  defp blob_path(layout_dir, "sha256:" <> hex),
    do: Path.join([layout_dir, "blobs", "sha256", hex])

  defp sha256_hex(bytes), do: Base.encode16(:crypto.hash(:sha256, bytes), case: :lower)

  defp canonical_json(%{} = map) do
    body =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map_join(",", fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> body <> "}"
  end

  defp canonical_json(values) when is_list(values) do
    "[" <> Enum.map_join(values, ",", &canonical_json/1) <> "]"
  end

  defp canonical_json(value), do: Jason.encode!(value)

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
