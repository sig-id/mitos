defmodule Mitos.AppPackage do
  @moduledoc """
  Local OCI-layout packaging for agentic applications.

  `publish_local/3` freezes an app directory into a reproducible OCI artifact
  layout on disk. It bundles the normalized app manifest and pipeline markdown,
  resolves local capability path/workload refs to content-addressed capability
  ids, and writes `oci-layout`, `index.json`, and `blobs/sha256/*`.

  This does not push to a registry or pull remote `oci://` refs; it creates the
  local artifact shape that a registry transport can upload later.
  """

  @artifact_type "application/vnd.mitos.application.v1+json"
  @manifest_media_type "application/vnd.oci.image.manifest.v1+json"
  @config_media_type "application/vnd.mitos.application.config.v1+json"
  @app_manifest_media_type "application/vnd.mitos.application.manifest.v1+json"
  @pipeline_media_type "text/markdown; charset=utf-8"
  @id_pattern ~r/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/
  @sha256_hex_pattern ~r/\A[0-9a-f]{64}\z/
  @path_ref_prefixes ["./", "app/", "apps/", "capabilities/", "workloads/"]
  @reserved_app_files ~w(app.json app.yaml app.yml bindings.json)

  @doc """
  Publish a local app directory into an OCI image-layout directory.

  The destination may be absent or empty. Existing non-empty directories are
  refused so publishing never overwrites unrelated data.
  """
  def publish_local(app_path, dest_dir, opts \\ [])

  def publish_local(app_path, dest_dir, opts) when is_binary(app_path) and is_binary(dest_dir) do
    with {:ok, app} <- Mitos.AppManifest.load(app_path, apply_bindings: false),
         :ok <- prepare_dest(dest_dir),
         {:ok, frozen_manifest} <- frozen_manifest(app),
         app_id <- app_id(frozen_manifest),
         {:ok, descriptors} <- write_blobs(dest_dir, app, frozen_manifest, app_id),
         {:ok, oci_manifest_descriptor} <-
           write_oci_manifest(dest_dir, app, app_id, descriptors),
         :ok <- write_index(dest_dir, app, app_id, oci_manifest_descriptor, opts) do
      {:ok,
       %{
         "app_id" => app_id,
         "layout_dir" => Path.expand(dest_dir),
         "manifest_digest" => oci_manifest_descriptor["digest"],
         "blobs" => descriptors
       }}
    end
  end

  def publish_local(_app_path, _dest_dir, _opts),
    do: {:error, {:invalid_application_package, "paths must be strings"}}

  @doc """
  Install a local OCI image-layout application package into an app directory.

  The installer verifies the OCI layout, manifest/app annotations, descriptor
  digests, descriptor sizes, and bundled pipeline hashes before writing files.
  The destination may be absent or empty.
  """
  def install_local(layout_dir, dest_dir, opts \\ [])

  def install_local(layout_dir, dest_dir, opts)
      when is_binary(layout_dir) and is_binary(dest_dir) and is_list(opts) do
    with {:ok, manifest_descriptor, oci_manifest} <- read_oci_manifest(layout_dir, opts),
         {:ok, frozen_manifest} <- read_frozen_app_manifest(layout_dir, oci_manifest),
         app_id <- app_id(frozen_manifest),
         :ok <- verify_app_id_annotation(manifest_descriptor, app_id),
         :ok <- verify_app_id_annotation(oci_manifest, app_id),
         :ok <- verify_install_requirements(frozen_manifest, opts),
         :ok <- verify_default_io_availability(frozen_manifest, opts),
         {:ok, binding_sidecar} <- install_binding_sidecar(frozen_manifest, app_id, opts),
         {:ok, files} <- install_files(layout_dir, frozen_manifest, oci_manifest),
         :ok <- prepare_install_dest(dest_dir),
         :ok <- write_install(dest_dir, frozen_manifest, files, binding_sidecar, app_id),
         {:ok, app} <- Mitos.AppManifest.load(dest_dir),
         :ok <- verify_loaded_app_id(app, app_id) do
      install =
        %{
          "app_id" => app.app_id,
          "app_dir" => Path.expand(dest_dir),
          "manifest_digest" => manifest_descriptor["digest"],
          "pipelines" => Enum.map(app.pipelines, &Map.take(&1, ~w(id file sha256 requires))),
          "bindings_applied?" => binding_sidecar != nil
        }
        |> maybe_put("io", binding_sidecar && binding_sidecar["io"])

      {:ok, install}
    end
  end

  def install_local(_layout_dir, _dest_dir, _opts),
    do: {:error, {:invalid_application_package, "paths must be strings"}}

  defp prepare_dest(dest_dir) do
    case File.lstat(dest_dir) do
      {:ok, %{type: :directory}} ->
        case File.ls(dest_dir) do
          {:ok, []} ->
            mkdir_layout(dest_dir)

          {:ok, _entries} ->
            {:error, {:invalid_application_package, "destination must be empty"}}

          {:error, reason} ->
            {:error,
             {:invalid_application_package, "could not inspect destination: #{inspect(reason)}"}}
        end

      {:ok, %{type: :symlink}} ->
        {:error, {:invalid_application_package, "destination must not be a symlink"}}

      {:ok, _stat} ->
        {:error, {:invalid_application_package, "destination must be a directory"}}

      {:error, :enoent} ->
        mkdir_layout(dest_dir)

      {:error, reason} ->
        {:error,
         {:invalid_application_package, "could not inspect destination: #{inspect(reason)}"}}
    end
  end

  defp prepare_install_dest(dest_dir) do
    case File.lstat(dest_dir) do
      {:ok, %{type: :directory}} ->
        case File.ls(dest_dir) do
          {:ok, []} ->
            :ok

          {:ok, _entries} ->
            {:error, {:invalid_application_package, "destination must be empty"}}

          {:error, reason} ->
            {:error,
             {:invalid_application_package, "could not inspect destination: #{inspect(reason)}"}}
        end

      {:ok, %{type: :symlink}} ->
        {:error, {:invalid_application_package, "destination must not be a symlink"}}

      {:ok, _stat} ->
        {:error, {:invalid_application_package, "destination must be a directory"}}

      {:error, :enoent} ->
        :ok

      {:error, reason} ->
        {:error,
         {:invalid_application_package, "could not inspect destination: #{inspect(reason)}"}}
    end
  end

  defp mkdir_layout(dest_dir) do
    with :ok <- mkdir_regular_p(Path.join([dest_dir, "blobs", "sha256"])) do
      :ok
    else
      {:error, reason} ->
        {:error,
         {:invalid_application_package, "could not create OCI layout: #{inspect(reason)}"}}
    end
  end

  defp frozen_manifest(%Mitos.AppManifest{} = app) do
    with {:ok, capabilities} <- frozen_capabilities(app) do
      {:ok, Map.put(app.raw, "capabilities", capabilities)}
    end
  end

  defp frozen_capabilities(%Mitos.AppManifest{} = app) do
    app.capabilities
    |> Enum.reduce_while({:ok, []}, fn capability, {:ok, acc} ->
      case frozen_capability(app, capability) do
        {:ok, frozen} -> {:cont, {:ok, acc ++ [frozen]}}
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp frozen_capability(app, %{"as" => alias, "ref" => ref} = capability)
       when is_binary(alias) and is_binary(ref) do
    with {:ok, frozen_ref} <- frozen_capability_ref(app, alias, ref) do
      {:ok, Map.put(capability, "ref", frozen_ref)}
    end
  end

  defp frozen_capability(_app, _capability),
    do: {:error, {:invalid_application_package, "capability entries require ref and as"}}

  defp frozen_capability_ref(app, alias, ref) do
    cond do
      capability_id_ref?(ref) ->
        {:ok, ref}

      oci_digest_ref?(ref) ->
        {:ok, ref}

      String.starts_with?(ref, "oci://") ->
        {:error, {:invalid_application_package, "OCI capability refs must be content-addressed"}}

      path_ref?(ref) ->
        local_capability_ref(app, alias, ref)

      Regex.match?(@id_pattern, ref) ->
        ref |> Mitos.Workload.fetch() |> workload_capability_ref(ref)

      true ->
        {:error, {:invalid_application_package, "unsupported capability ref #{inspect(ref)}"}}
    end
  end

  defp local_capability_ref(%Mitos.AppManifest{root: root}, alias, ref) when is_binary(root) do
    with :ok <- safe_relative_path(ref),
         path <- Path.expand(ref, root),
         true <- inside_root?(path, root),
         true <- no_symlink_path?(path, root),
         {:ok, workload_path} <- workload_path(path, alias),
         true <- no_symlink_path?(workload_path, root),
         {:ok, raw} <- read_regular_file(workload_path),
         {:ok, json} <- Jason.decode(strip_json_comments(raw)),
         {:ok, %Mitos.Workload{capability: %Mitos.Capability{id: capability_id}}} <-
           Mitos.Workload.from_json(alias, json, package_root: Path.dirname(workload_path)) do
      {:ok, capability_id}
    else
      false ->
        {:error, {:invalid_application_package, "capability ref escapes the application root"}}

      {:error, %Jason.DecodeError{} = error} ->
        {:error,
         {:invalid_application_package,
          "bad capability workload JSON: #{Exception.message(error)}"}}

      {:ok, %Mitos.Workload{}} ->
        {:error, {:invalid_application_package, "capability ref #{inspect(ref)} has no manifest"}}

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp local_capability_ref(_app, _alias, _ref),
    do: {:error, {:invalid_application_package, "application root is required"}}

  defp workload_capability_ref(
         {:ok, %Mitos.Workload{capability: %Mitos.Capability{id: id}}},
         _ref
       ),
       do: {:ok, id}

  defp workload_capability_ref({:ok, %Mitos.Workload{}}, ref),
    do: {:error, {:invalid_application_package, "capability ref #{inspect(ref)} has no manifest"}}

  defp workload_capability_ref({:error, reason}, _ref), do: {:error, reason}

  defp write_blobs(dest_dir, app, frozen_manifest, app_id) do
    with {:ok, config} <- write_json_blob(dest_dir, app_config(app, app_id), @config_media_type),
         {:ok, manifest} <- write_json_blob(dest_dir, frozen_manifest, @app_manifest_media_type),
         {:ok, pipelines} <- write_pipeline_blobs(dest_dir, app) do
      {:ok, %{"config" => config, "app_manifest" => manifest, "pipelines" => pipelines}}
    end
  end

  defp write_pipeline_blobs(dest_dir, %Mitos.AppManifest{} = app) do
    app.pipelines
    |> Enum.reduce_while({:ok, []}, fn pipeline, {:ok, acc} ->
      with {:ok, path} <- pipeline_path(app, pipeline),
           {:ok, bytes} <- read_regular_file(path),
           {:ok, descriptor} <- write_blob(dest_dir, bytes, @pipeline_media_type) do
        descriptor =
          descriptor
          |> put_in(["annotations"], %{
            "org.opencontainers.image.title" => pipeline["file"],
            "dev.mitos.pipeline.id" => pipeline["id"],
            "dev.mitos.pipeline.path" => pipeline["file"]
          })

        {:cont, {:ok, acc ++ [descriptor]}}
      else
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp pipeline_path(%Mitos.AppManifest{root: root}, %{"file" => file})
       when is_binary(root) and is_binary(file) do
    path = Path.expand(file, root)

    cond do
      not inside_root?(path, root) ->
        {:error, {:invalid_application_package, "pipeline file escapes the application root"}}

      true ->
        case no_symlink_path?(path, root) do
          true ->
            {:ok, path}

          false ->
            {:error, {:invalid_application_package, "pipeline file escapes the application root"}}

          {:error, reason} ->
            {:error,
             {:invalid_application_package, "could not read pipeline file: #{inspect(reason)}"}}
        end
    end
  end

  defp pipeline_path(_app, _pipeline),
    do: {:error, {:invalid_application_package, "application root is required"}}

  defp write_oci_manifest(dest_dir, app, app_id, descriptors) do
    layers = [descriptors["app_manifest"] | descriptors["pipelines"]]

    manifest = %{
      "schemaVersion" => 2,
      "mediaType" => @manifest_media_type,
      "artifactType" => @artifact_type,
      "config" => descriptors["config"],
      "layers" => layers,
      "annotations" => %{
        "dev.mitos.app_id" => app_id,
        "org.opencontainers.image.title" => app.name,
        "org.opencontainers.image.version" => app.version
      }
    }

    write_json_blob(dest_dir, manifest, @manifest_media_type)
  end

  defp write_index(dest_dir, app, app_id, manifest_descriptor, opts) do
    index = %{
      "schemaVersion" => 2,
      "mediaType" => "application/vnd.oci.image.index.v1+json",
      "manifests" => [
        manifest_descriptor
        |> Map.put("artifactType", @artifact_type)
        |> Map.put("annotations", %{
          "dev.mitos.app_id" => app_id,
          "org.opencontainers.image.ref.name" => Keyword.get(opts, :tag, app.name),
          "org.opencontainers.image.title" => app.name,
          "org.opencontainers.image.version" => app.version
        })
      ]
    }

    with :ok <-
           write_regular_file(
             Path.join(dest_dir, "oci-layout"),
             Jason.encode!(%{"imageLayoutVersion" => "1.0.0"}) <> "\n"
           ),
         :ok <-
           write_regular_file(
             Path.join(dest_dir, "index.json"),
             Jason.encode!(index, pretty: true) <> "\n"
           ) do
      :ok
    else
      {:error, reason} ->
        {:error, {:invalid_application_package, "could not write OCI index: #{inspect(reason)}"}}
    end
  end

  defp write_json_blob(dest_dir, value, media_type) do
    write_blob(dest_dir, canonical_json(value), media_type)
  end

  defp write_blob(dest_dir, bytes, media_type) when is_binary(bytes) do
    digest = sha256(bytes)
    path = blob_path(dest_dir, digest)

    with :ok <- write_regular_file(path, bytes) do
      {:ok, %{"mediaType" => media_type, "digest" => digest, "size" => byte_size(bytes)}}
    else
      {:error, reason} ->
        {:error, {:invalid_application_package, "could not write blob: #{inspect(reason)}"}}
    end
  end

  defp read_oci_manifest(layout_dir, opts) do
    with :ok <- ensure_layout_dir(layout_dir),
         {:ok, %{"imageLayoutVersion" => "1.0.0"}} <-
           read_json_file(Path.join(layout_dir, "oci-layout")),
         {:ok, index} <- read_json_file(Path.join(layout_dir, "index.json")),
         {:ok, descriptor} <- select_manifest_descriptor(index, opts),
         :ok <- ensure_artifact_descriptor(descriptor),
         {:ok, manifest} <- read_descriptor_json(layout_dir, descriptor),
         :ok <- ensure_oci_manifest(manifest) do
      {:ok, descriptor, manifest}
    else
      {:ok, _other_layout} ->
        {:error, {:invalid_application_package, "unsupported OCI layout version"}}

      {:error, _reason} = error ->
        error
    end
  end

  defp read_frozen_app_manifest(layout_dir, %{"layers" => layers}) when is_list(layers) do
    case Enum.find(layers, &(&1["mediaType"] == @app_manifest_media_type)) do
      nil -> {:error, {:invalid_application_package, "application manifest layer is missing"}}
      descriptor -> read_descriptor_json(layout_dir, descriptor)
    end
  end

  defp read_frozen_app_manifest(_layout_dir, _manifest),
    do: {:error, {:invalid_application_package, "OCI manifest layers must be a list"}}

  defp read_json_file(path) do
    with {:ok, bytes} <- read_regular_file(path),
         {:ok, json} <- Jason.decode(bytes) do
      {:ok, json}
    else
      {:error, %Jason.DecodeError{} = error} ->
        {:error,
         {:invalid_application_package,
          "bad JSON in #{Path.basename(path)}: #{Exception.message(error)}"}}

      {:error, reason} ->
        {:error,
         {:invalid_application_package,
          "could not read #{Path.basename(path)}: #{inspect(reason)}"}}
    end
  end

  defp select_manifest_descriptor(%{"manifests" => manifests}, opts) when is_list(manifests) do
    tag = Keyword.get(opts, :tag)
    app_id = Keyword.get(opts, :app_id)

    descriptor =
      Enum.find(manifests, fn descriptor ->
        artifact_descriptor?(descriptor) and descriptor_matches?(descriptor, tag, app_id)
      end)

    case descriptor do
      nil -> {:error, {:invalid_application_package, "application artifact manifest is missing"}}
      descriptor -> {:ok, descriptor}
    end
  end

  defp select_manifest_descriptor(_index, _opts),
    do: {:error, {:invalid_application_package, "OCI index manifests must be a list"}}

  defp descriptor_matches?(_descriptor, nil, nil), do: true

  defp descriptor_matches?(descriptor, tag, app_id) do
    annotations = map_or_empty(descriptor["annotations"])
    tag_matches? = is_nil(tag) or annotations["org.opencontainers.image.ref.name"] == tag
    app_id_matches? = is_nil(app_id) or annotations["dev.mitos.app_id"] == app_id
    tag_matches? and app_id_matches?
  end

  defp ensure_artifact_descriptor(descriptor) do
    if artifact_descriptor?(descriptor) do
      :ok
    else
      {:error, {:invalid_application_package, "descriptor is not a Mitos application artifact"}}
    end
  end

  defp artifact_descriptor?(%{} = descriptor) do
    descriptor["artifactType"] == @artifact_type or
      descriptor["mediaType"] == @manifest_media_type
  end

  defp artifact_descriptor?(_descriptor), do: false

  defp ensure_oci_manifest(%{"schemaVersion" => 2, "artifactType" => @artifact_type}), do: :ok

  defp ensure_oci_manifest(_manifest),
    do: {:error, {:invalid_application_package, "blob is not a Mitos application manifest"}}

  defp read_descriptor_json(layout_dir, descriptor) do
    with {:ok, bytes} <- read_descriptor_blob(layout_dir, descriptor),
         {:ok, json} <- Jason.decode(bytes) do
      {:ok, json}
    else
      {:error, %Jason.DecodeError{} = error} ->
        {:error,
         {:invalid_application_package,
          "bad JSON in descriptor #{inspect(descriptor["digest"])}: #{Exception.message(error)}"}}

      {:error, _reason} = error ->
        error
    end
  end

  defp read_descriptor_blob(layout_dir, %{"digest" => digest} = descriptor)
       when is_binary(digest) do
    with :ok <- verify_descriptor_digest_ref(digest),
         path <- blob_path(layout_dir, digest),
         true <- no_symlink_path?(Path.dirname(path), layout_dir),
         {:ok, bytes} <- read_regular_file(path),
         :ok <- verify_descriptor_digest(bytes, digest),
         :ok <- verify_descriptor_size(bytes, descriptor) do
      {:ok, bytes}
    else
      {:error, {:invalid_application_package, _detail}} = error ->
        error

      false ->
        {:error, {:invalid_application_package, "descriptor blob path must not contain symlinks"}}

      {:error, reason} ->
        {:error,
         {:invalid_application_package,
          "could not read blob #{inspect(digest)}: #{inspect(reason)}"}}
    end
  end

  defp read_descriptor_blob(_layout_dir, _descriptor),
    do: {:error, {:invalid_application_package, "descriptor digest must be sha256"}}

  defp ensure_layout_dir(layout_dir) do
    case File.lstat(layout_dir) do
      {:ok, %{type: :directory}} ->
        :ok

      {:ok, %{type: :symlink}} ->
        {:error, {:invalid_application_package, "layout directory must not be a symlink"}}

      {:ok, _stat} ->
        {:error, {:invalid_application_package, "layout path must be a directory"}}

      {:error, reason} ->
        {:error,
         {:invalid_application_package, "could not inspect layout directory: #{inspect(reason)}"}}
    end
  end

  defp verify_descriptor_digest_ref("sha256:" <> hex) do
    if Regex.match?(@sha256_hex_pattern, hex) do
      :ok
    else
      {:error, {:invalid_application_package, "descriptor digest must be canonical sha256"}}
    end
  end

  defp verify_descriptor_digest_ref(_digest),
    do: {:error, {:invalid_application_package, "descriptor digest must be sha256"}}

  defp verify_descriptor_digest(bytes, digest) do
    if sha256(bytes) == digest do
      :ok
    else
      {:error, {:invalid_application_package, "descriptor digest mismatch"}}
    end
  end

  defp verify_descriptor_size(bytes, %{"size" => size}) when is_integer(size) do
    if byte_size(bytes) == size do
      :ok
    else
      {:error, {:invalid_application_package, "descriptor size mismatch"}}
    end
  end

  defp verify_descriptor_size(_bytes, _descriptor), do: :ok

  defp install_files(layout_dir, frozen_manifest, oci_manifest) do
    frozen_manifest
    |> Map.get("pipelines", [])
    |> Enum.reduce_while({:ok, []}, fn pipeline, {:ok, acc} ->
      case install_file(layout_dir, pipeline, oci_manifest) do
        {:ok, file} -> {:cont, {:ok, acc ++ [file]}}
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp install_file(layout_dir, %{"id" => id, "file" => file, "sha256" => expected}, oci_manifest)
       when is_binary(id) and is_binary(file) and is_binary(expected) do
    with :ok <- safe_relative_path(file, "pipeline files"),
         {:ok, descriptor} <- pipeline_descriptor(oci_manifest, id, file),
         {:ok, bytes} <- read_descriptor_blob(layout_dir, descriptor),
         :ok <- verify_pipeline_digest(bytes, expected) do
      {:ok, {file, bytes}}
    end
  end

  defp install_file(_layout_dir, _pipeline, _oci_manifest),
    do: {:error, {:invalid_application_package, "pipeline entries require id, file, and sha256"}}

  defp pipeline_descriptor(%{"layers" => layers}, id, file) when is_list(layers) do
    case Enum.find(layers, &pipeline_descriptor?(&1, id, file)) do
      nil -> {:error, {:invalid_application_package, "pipeline layer #{inspect(id)} is missing"}}
      descriptor -> {:ok, descriptor}
    end
  end

  defp pipeline_descriptor(_oci_manifest, _id, _file),
    do: {:error, {:invalid_application_package, "OCI manifest layers must be a list"}}

  defp pipeline_descriptor?(%{"mediaType" => @pipeline_media_type} = descriptor, id, file) do
    annotations = map_or_empty(descriptor["annotations"])
    annotations["dev.mitos.pipeline.id"] == id and annotations["dev.mitos.pipeline.path"] == file
  end

  defp pipeline_descriptor?(_descriptor, _id, _file), do: false

  defp verify_pipeline_digest(bytes, expected) do
    if sha256(bytes) == expected do
      :ok
    else
      {:error, {:invalid_application_package, "pipeline layer digest mismatch"}}
    end
  end

  defp write_install(dest_dir, frozen_manifest, files, binding_sidecar, app_id) do
    with {:ok, tmp_dir} <- create_install_tmp_dir(dest_dir) do
      result =
        with :ok <- write_install_tree(tmp_dir, frozen_manifest, files, binding_sidecar),
             :ok <- validate_install_tree(tmp_dir, app_id),
             :ok <- commit_install(dest_dir, tmp_dir) do
          :ok
        end

      case result do
        :ok ->
          :ok

        {:error, reason} ->
          File.rm_rf(tmp_dir)
          install_write_error(reason)
      end
    else
      {:error, reason} ->
        install_write_error(reason)
    end
  end

  defp validate_install_tree(tmp_dir, app_id) do
    with {:ok, app} <- Mitos.AppManifest.load(tmp_dir),
         :ok <- verify_loaded_app_id(app, app_id) do
      :ok
    else
      {:error, {:invalid_application, detail}} ->
        {:error, {:invalid_application_package, detail}}

      {:error, {:invalid_application_input, detail}} ->
        {:error, {:invalid_application_package, detail}}

      {:error, _reason} = error ->
        error
    end
  end

  defp install_write_error({:invalid_application_package, _detail} = reason), do: {:error, reason}

  defp install_write_error(reason) do
    {:error, {:invalid_application_package, "could not write installed app: #{inspect(reason)}"}}
  end

  defp write_install_tree(tmp_dir, frozen_manifest, files, binding_sidecar) do
    with :ok <-
           write_regular_file(
             Path.join(tmp_dir, "app.json"),
             Jason.encode!(frozen_manifest, pretty: true) <> "\n"
           ),
         :ok <- write_binding_sidecar(tmp_dir, binding_sidecar),
         :ok <- write_install_files(tmp_dir, files) do
      :ok
    end
  end

  defp create_install_tmp_dir(dest_dir) do
    dest_dir = Path.expand(dest_dir)
    parent = Path.dirname(dest_dir)
    name = Path.basename(dest_dir)

    with :ok <- mkdir_regular_p(parent) do
      create_install_tmp_dir(parent, name, 0)
    end
  end

  defp create_install_tmp_dir(_parent, _name, 16), do: {:error, :eexist}

  defp create_install_tmp_dir(parent, name, attempts) do
    path = Path.join(parent, ".#{name}.install-#{install_tmp_suffix()}")

    case File.mkdir(path) do
      :ok -> {:ok, path}
      {:error, :eexist} -> create_install_tmp_dir(parent, name, attempts + 1)
      {:error, reason} -> {:error, reason}
    end
  end

  defp install_tmp_suffix do
    random = :crypto.strong_rand_bytes(8) |> Base.url_encode64(padding: false)
    "#{System.unique_integer([:positive, :monotonic])}-#{random}"
  end

  defp commit_install(dest_dir, tmp_dir) do
    case File.lstat(dest_dir) do
      {:ok, %{type: :directory}} ->
        case File.ls(dest_dir) do
          {:ok, []} ->
            with :ok <- File.rmdir(dest_dir) do
              File.rename(tmp_dir, dest_dir)
            end

          {:ok, _entries} ->
            {:error, :destination_not_empty}

          {:error, reason} ->
            {:error, reason}
        end

      {:ok, %{type: :symlink}} ->
        {:error, :destination_symlink}

      {:error, :enoent} ->
        File.rename(tmp_dir, dest_dir)

      {:ok, _stat} ->
        {:error, :destination_not_directory}

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp write_binding_sidecar(_dest_dir, nil), do: :ok

  defp write_binding_sidecar(dest_dir, %{} = binding_sidecar) do
    write_regular_file(
      Path.join(dest_dir, "bindings.json"),
      Jason.encode!(binding_sidecar, pretty: true) <> "\n"
    )
  end

  defp write_install_files(dest_dir, files) do
    Enum.reduce_while(files, :ok, fn {relative, bytes}, :ok ->
      with {:ok, path} <- install_path(dest_dir, relative),
           :ok <- write_regular_file(path, bytes) do
        {:cont, :ok}
      else
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp install_path(dest_dir, relative) do
    root = Path.expand(dest_dir)
    path = Path.expand(relative, root)

    if inside_root?(path, root) do
      {:ok, path}
    else
      {:error, {:invalid_application_package, "pipeline file escapes the application root"}}
    end
  end

  defp verify_app_id_annotation(value, app_id) do
    case get_in(value, ["annotations", "dev.mitos.app_id"]) do
      nil ->
        :ok

      ^app_id ->
        :ok

      _other ->
        {:error, {:invalid_application_package, "app_id annotation mismatch"}}
    end
  end

  defp verify_loaded_app_id(%Mitos.AppManifest{app_id: app_id}, app_id), do: :ok

  defp verify_loaded_app_id(_app, _app_id),
    do: {:error, {:invalid_application_package, "installed app_id mismatch"}}

  defp verify_install_requirements(%{"requirements" => %{} = requirements}, opts) do
    with :ok <-
           verify_available_requirement(
             "connector",
             string_list(requirements["connectors"]),
             option_string_list(opts, :available_connectors)
           ),
         :ok <-
           verify_available_requirement(
             "harness",
             string_list(requirements["harnesses"]),
             option_string_list(opts, :available_harnesses)
           ) do
      :ok
    end
  end

  defp verify_install_requirements(_frozen_manifest, _opts), do: :ok

  defp verify_default_io_availability(frozen_manifest, opts) do
    if Keyword.has_key?(opts, :io) do
      :ok
    else
      verify_default_io_availability_if_bound(frozen_manifest, opts)
    end
  end

  defp verify_default_io_availability_if_bound(%{"io" => %{} = io}, opts) do
    with :ok <-
           verify_available_binding_class_when_declared(
             "connector",
             io["inputs"],
             "connector",
             option_string_list_when_present(opts, :available_connectors)
           ),
         :ok <-
           verify_available_binding_class_when_declared(
             "sink",
             io["outputs"],
             "sink",
             option_string_list_when_present(opts, :available_sinks)
           ) do
      :ok
    end
  end

  defp verify_default_io_availability_if_bound(_frozen_manifest, _opts), do: :ok

  defp install_binding_sidecar(frozen_manifest, app_id, opts) do
    case Keyword.get(opts, :io) do
      nil ->
        {:ok, nil}

      %{} = io ->
        with {:ok, base_app} <- app_from_frozen_manifest(frozen_manifest, app_id),
             {:ok, effective_app} <- Mitos.AppManifest.apply_io_bindings(base_app, io),
             :ok <- verify_install_binding_availability(effective_app.io, opts) do
          {:ok,
           %{
             "app_id" => app_id,
             "io" => effective_app.io,
             "credential_scope" => "tenant_control_plane",
             "defaults_overridden?" => true
           }}
        else
          {:error, {:invalid_application, detail}} ->
            {:error, {:invalid_application_package, detail}}

          {:error, _reason} = error ->
            error
        end

      _other ->
        {:error, {:invalid_application_package, "install io bindings must be an object"}}
    end
  end

  defp app_from_frozen_manifest(%{"entrypoints" => entrypoints} = frozen_manifest, app_id)
       when is_list(entrypoints) do
    {:ok,
     %Mitos.AppManifest{
       app_id: app_id,
       name: frozen_manifest["name"],
       version: frozen_manifest["version"],
       entrypoints: entrypoints
     }}
  end

  defp app_from_frozen_manifest(_frozen_manifest, _app_id) do
    {:error, {:invalid_application, "frozen application manifest has no entrypoints"}}
  end

  defp verify_install_binding_availability(%{} = io, opts) do
    with :ok <-
           verify_available_binding_class(
             "connector",
             io["inputs"],
             "connector",
             option_string_list(opts, :available_connectors)
           ),
         :ok <-
           verify_available_binding_class(
             "sink",
             io["outputs"],
             "sink",
             option_string_list(opts, :available_sinks)
           ) do
      :ok
    end
  end

  defp verify_install_binding_availability(_io, _opts), do: :ok

  defp verify_available_binding_class(label, bindings, key, available) when is_list(bindings) do
    required =
      bindings
      |> Enum.map(&optional_string(&1[key]))
      |> Enum.reject(&is_nil/1)

    verify_available_requirement(label, required, available)
  end

  defp verify_available_binding_class(_label, _bindings, _key, _available), do: :ok

  defp verify_available_binding_class_when_declared(_label, _bindings, _key, :not_declared),
    do: :ok

  defp verify_available_binding_class_when_declared(label, bindings, key, {:declared, available}),
    do: verify_available_binding_class(label, bindings, key, available)

  defp verify_available_requirement(_label, [], _available), do: :ok

  defp verify_available_requirement(label, required, available) do
    available = MapSet.new(available)

    required
    |> Enum.uniq()
    |> Enum.reject(&MapSet.member?(available, &1))
    |> case do
      [] ->
        :ok

      [name | _rest] ->
        {:error, {:invalid_application_package, "missing required #{label} #{name}"}}
    end
  end

  defp app_config(app, app_id) do
    %{
      "app_id" => app_id,
      "name" => app.name,
      "version" => app.version,
      "created" => "1970-01-01T00:00:00Z"
    }
  end

  defp app_id(frozen_manifest), do: "app:" <> sha256(canonical_json(frozen_manifest))

  defp blob_path(dest_dir, "sha256:" <> hex = digest) do
    case verify_descriptor_digest_ref(digest) do
      :ok ->
        Path.join([dest_dir, "blobs", "sha256", hex])

      {:error, {:invalid_application_package, detail}} ->
        raise ArgumentError, detail
    end
  end

  defp sha256(bytes) when is_binary(bytes) do
    digest =
      :crypto.hash(:sha256, bytes)
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end

  defp capability_id_ref?(ref), do: String.starts_with?(ref, "cap:sha256:")

  defp oci_digest_ref?(ref),
    do: String.starts_with?(ref, "oci://") and String.contains?(ref, "@sha256:")

  defp path_ref?(ref) do
    String.ends_with?(ref, ".json") or String.contains?(ref, "/") or
      Enum.any?(@path_ref_prefixes, &String.starts_with?(ref, &1))
  end

  defp safe_relative_path(path), do: safe_relative_path(path, "capability refs")

  defp safe_relative_path(path, label) do
    cond do
      Path.type(path) != :relative ->
        {:error, {:invalid_application_package, "#{label} must be relative paths"}}

      ".." in Path.split(path) ->
        {:error, {:invalid_application_package, "#{label} must not contain .."}}

      reserved_app_file?(path) ->
        {:error,
         {:invalid_application_package, "#{label} must not overwrite application control files"}}

      true ->
        :ok
    end
  end

  defp reserved_app_file?(path) do
    case Path.split(path) do
      [file] -> file in @reserved_app_files
      _parts -> false
    end
  end

  defp workload_path(path, alias) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} ->
        {:ok, path}

      {:ok, %{type: :directory}} ->
        [Path.join(path, "workload.json"), Path.join(path, alias <> ".json")]
        |> Enum.find_value(&workload_file_candidate/1)
        |> case do
          nil ->
            {:error,
             {:invalid_application_package, "capability ref does not contain workload.json"}}

          workload_path ->
            {:ok, workload_path}
        end

      {:ok, %{type: :symlink}} ->
        {:ok, path}

      {:ok, _stat} ->
        {:error, {:invalid_application_package, "capability ref is unavailable"}}

      {:error, _reason} ->
        {:error, {:invalid_application_package, "capability ref is unavailable"}}
    end
  end

  defp workload_file_candidate(path) do
    case File.lstat(path) do
      {:ok, %{type: type}} when type in [:regular, :symlink] -> path
      _other -> nil
    end
  end

  defp inside_root?(path, root) do
    root = Path.expand(root)
    path = Path.expand(path)
    path == root or String.starts_with?(path, root <> "/")
  end

  defp no_symlink_path?(path, root) do
    root = Path.expand(root)
    path = Path.expand(path)
    parts = if path == root, do: [], else: Path.split(Path.relative_to(path, root))

    with {:ok, %{type: root_type}} <- File.lstat(root),
         true <- root_type != :symlink do
      parts
      |> Enum.reduce_while(root, fn part, current ->
        candidate = Path.join(current, part)

        case File.lstat(candidate) do
          {:ok, %{type: :symlink}} -> {:halt, false}
          {:ok, _stat} -> {:cont, candidate}
          {:error, reason} -> {:halt, {:error, reason}}
        end
      end)
      |> case do
        false -> false
        {:error, reason} -> {:error, reason}
        _last_path -> true
      end
    end
  end

  defp read_regular_file(path) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} -> File.read(path)
      {:ok, %{type: :symlink}} -> {:error, :symlink}
      {:ok, _stat} -> {:error, :not_regular}
      {:error, reason} -> {:error, reason}
    end
  end

  defp mkdir_regular_p(path) do
    path = Path.expand(path)
    parent = Path.dirname(path)

    if path == parent do
      ensure_existing_regular_dir(path)
    else
      with :ok <- mkdir_regular_p(parent) do
        ensure_regular_dir(path)
      end
    end
  end

  defp ensure_existing_regular_dir(path) do
    case File.lstat(path) do
      {:ok, %{type: :directory}} -> :ok
      {:ok, %{type: :symlink}} -> {:error, {:mkdir_failed, path, :symlink}}
      {:ok, _stat} -> {:error, {:mkdir_failed, path, :not_directory}}
      {:error, reason} -> {:error, {:mkdir_failed, path, reason}}
    end
  end

  defp ensure_regular_dir(path) do
    case File.lstat(path) do
      {:ok, %{type: :directory}} ->
        :ok

      {:ok, %{type: :symlink}} ->
        {:error, {:mkdir_failed, path, :symlink}}

      {:ok, _stat} ->
        {:error, {:mkdir_failed, path, :not_directory}}

      {:error, :enoent} ->
        case File.mkdir(path) do
          :ok -> :ok
          {:error, :eexist} -> ensure_regular_dir(path)
          {:error, reason} -> {:error, {:mkdir_failed, path, reason}}
        end

      {:error, reason} ->
        {:error, {:mkdir_failed, path, reason}}
    end
  end

  defp write_regular_file(path, bytes) when is_binary(bytes) do
    path = Path.expand(path)

    with :ok <- mkdir_regular_p(Path.dirname(path)) do
      write_regular_file_leaf(path, bytes)
    end
  end

  defp write_regular_file_leaf(path, bytes) do
    case File.lstat(path) do
      {:ok, %{type: :regular}} ->
        with :ok <- File.rm(path) do
          write_new_regular_file(path, bytes)
        end

      {:error, :enoent} ->
        write_new_regular_file(path, bytes)

      {:ok, %{type: :symlink}} ->
        {:error, :symlink}

      {:ok, _stat} ->
        {:error, :not_regular}

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp write_new_regular_file(path, bytes) do
    case File.open(path, [:write, :binary, :exclusive], fn io -> IO.binwrite(io, bytes) end) do
      {:ok, :ok} -> :ok
      {:ok, {:error, reason}} -> {:error, reason}
      {:error, :eexist} -> write_regular_file_leaf(path, bytes)
      {:error, reason} -> {:error, reason}
    end
  end

  defp strip_json_comments(raw) do
    raw
    |> String.split("\n")
    |> Enum.reject(&String.starts_with?(String.trim_leading(&1), "//"))
    |> Enum.join("\n")
  end

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

  defp option_string_list(opts, key), do: opts |> Keyword.get(key, []) |> string_list()

  defp option_string_list_when_present(opts, key) do
    if Keyword.has_key?(opts, key) do
      {:declared, option_string_list(opts, key)}
    else
      :not_declared
    end
  end

  defp string_list(values) when is_list(values) do
    values
    |> Enum.filter(&is_binary/1)
    |> Enum.map(&String.trim/1)
    |> Enum.reject(&(&1 == ""))
  end

  defp string_list(_values), do: []

  defp optional_string(value) when is_binary(value) do
    value = String.trim(value)
    if value == "", do: nil, else: value
  end

  defp optional_string(_value), do: nil

  defp map_or_empty(%{} = map), do: map
  defp map_or_empty(_value), do: %{}

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, _key, false), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)
end
