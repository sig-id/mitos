defmodule Mitos.AppManifest do
  @moduledoc """
  Local application manifest loader and validator.

  This implements the executable subset of `spec/12_APPLICATION.md`: a manifest
  can be loaded from an app directory, its references are normalized, pipeline
  files are content-hashed, an immutable `app_id` is derived, and an entrypoint
  can be mapped into the canonical WorkPool Task shape.

  Local manifests can be JSON or the dependency-free YAML subset decoded by
  `Mitos.ManifestYaml`. Publishing into a local OCI image layout is implemented
  by `Mitos.AppPackage`.
  """

  @id_pattern ~r/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/
  @secret_key_pattern ~r/(secret|token|api[_-]?key|password|credential|private[_-]?key)/i
  @top_level_keys ~w(
    authors
    capabilities
    description
    entrypoints
    io
    license
    name
    pipelines
    requirements
    services
    version
  )
  @workspace_values ~w(repo dataset none)
  @trust_tiers ~w(T0 T1 T2)
  @pipeline_requires_keys ~w(services targets)
  @service_keys ~w(requires wants)
  @requirement_keys ~w(connectors egress harnesses resources trust_tier)
  @binding_sidecar_keys ~w(app_id credential_scope defaults_overridden? io)
  @reserved_app_files ~w(app.json app.yaml app.yml bindings.json)
  @schema_top_level_keys ~w(type required additionalProperties properties description title)
  @input_schema_types ~w(string integer int number boolean bool object array file)

  defstruct [
    :app_id,
    :root,
    :name,
    :version,
    :description,
    :authors,
    :license,
    capabilities: [],
    pipelines: [],
    entrypoints: [],
    io: %{"inputs" => [], "outputs" => []},
    services: %{"requires" => [], "wants" => []},
    requirements: %{},
    raw: %{}
  ]

  @type t :: %__MODULE__{
          app_id: String.t(),
          root: String.t() | nil,
          name: String.t(),
          version: String.t(),
          description: String.t() | nil,
          authors: [String.t()],
          license: String.t() | nil,
          capabilities: [map()],
          pipelines: [map()],
          entrypoints: [map()],
          io: map(),
          services: map(),
          requirements: map(),
          raw: map()
        }

  @doc """
  Load an application manifest from an app directory or a JSON manifest path.

  Directory loads look for `app.json`, `app.yaml`, then `app.yml`. YAML files are
  discovered so callers get a clear unsupported-format error instead of a
  misleading not-found.
  """
  @spec load(String.t(), keyword()) :: {:ok, t()} | {:error, term()}
  def load(path_or_dir, opts \\ []) when is_binary(path_or_dir) do
    with {:ok, path} <- manifest_path(path_or_dir),
         {:ok, raw} <- File.read(path),
         {:ok, manifest} <- decode_manifest(path, raw),
         {:ok, app} <- from_json(manifest, Keyword.put_new(opts, :app_root, Path.dirname(path))) do
      maybe_apply_binding_sidecar(app, Path.dirname(path), opts)
    end
  end

  @doc "Normalize and validate an already-decoded JSON manifest."
  @spec from_json(map(), keyword()) :: {:ok, t()} | {:error, term()}
  def from_json(manifest, opts \\ [])

  def from_json(%{} = manifest, opts) do
    manifest = stringify(manifest)
    app_root = opts[:app_root]

    with :ok <- reject_unknown_top_level_fields(manifest),
         :ok <- reject_secret_fields(manifest),
         {:ok, name} <- required_string(manifest, "name"),
         {:ok, version} <- required_string(manifest, "version"),
         {:ok, capabilities} <- normalize_capabilities(manifest["capabilities"]),
         {:ok, pipelines} <- normalize_pipelines(manifest["pipelines"], app_root),
         {:ok, entrypoints} <- normalize_entrypoints(manifest["entrypoints"], pipelines),
         {:ok, io} <- normalize_io(manifest["io"], entrypoints),
         {:ok, services} <- normalize_services(manifest["services"]),
         {:ok, requirements} <- normalize_requirements(manifest["requirements"]),
         :ok <- validate_requirement_connectors(requirements, io) do
      normalized =
        %{
          "name" => name,
          "version" => version,
          "capabilities" => capabilities,
          "pipelines" => Enum.map(pipelines, &Map.take(&1, ["id", "file", "sha256", "requires"])),
          "entrypoints" => entrypoints,
          "io" => io,
          "services" => services,
          "requirements" => requirements
        }
        |> maybe_put("description", optional_string(manifest["description"]))
        |> maybe_put("authors", string_list(manifest["authors"]))
        |> maybe_put("license", optional_string(manifest["license"]))

      {:ok,
       %__MODULE__{
         app_id: app_id(normalized),
         root: app_root && Path.expand(app_root),
         name: name,
         version: version,
         description: optional_string(manifest["description"]),
         authors: string_list(manifest["authors"]),
         license: optional_string(manifest["license"]),
         capabilities: capabilities,
         pipelines: pipelines,
         entrypoints: entrypoints,
         io: io,
         services: services,
         requirements: requirements,
         raw: normalized
       }}
    end
  end

  def from_json(_manifest, _opts),
    do: {:error, {:invalid_application, "application manifest must be an object"}}

  @doc "Return one normalized entrypoint by id."
  @spec entrypoint(t(), String.t()) :: {:ok, map()} | {:error, term()}
  def entrypoint(%__MODULE__{} = app, entrypoint_id) when is_binary(entrypoint_id) do
    case Enum.find(app.entrypoints, &(&1["id"] == entrypoint_id)) do
      nil -> {:error, {:unknown_entrypoint, entrypoint_id}}
      %{} = entrypoint -> {:ok, entrypoint}
    end
  end

  @doc "Validate entrypoint input values against the manifest's JSON-schema subset."
  @spec validate_inputs(t(), String.t(), map()) :: :ok | {:error, term()}
  def validate_inputs(%__MODULE__{} = app, entrypoint_id, inputs) when is_map(inputs) do
    with {:ok, entrypoint} <- entrypoint(app, entrypoint_id) do
      validate_input_schema(input_schema(entrypoint), stringify(inputs))
    end
  end

  def validate_inputs(_app, _entrypoint_id, _inputs),
    do: {:error, {:invalid_application_input, "inputs must be an object"}}

  @doc """
  Map an entrypoint run into the canonical WorkPool Task shape.

  The returned task can be appended with `Mitos.WorkPool.append_enqueue/3` or
  handed to a dispatcher that knows how to execute the named pipeline.
  """
  @spec task_for_entrypoint(t(), String.t(), map(), keyword()) :: {:ok, map()} | {:error, term()}
  def task_for_entrypoint(app, entrypoint_id, inputs, opts \\ [])

  def task_for_entrypoint(%__MODULE__{} = app, entrypoint_id, inputs, opts)
      when is_map(inputs) do
    with {:ok, entrypoint} <- entrypoint(app, entrypoint_id),
         :ok <- validate_input_schema(input_schema(entrypoint), stringify(inputs)) do
      defaults = map_or(entrypoint["defaults"], %{})

      task =
        Mitos.WorkPool.task(%{
          "id" => Keyword.get(opts, :task_id) || generated_task_id(app, entrypoint),
          "tenant" => Keyword.get(opts, :tenant, %{"org" => "default", "project" => app.name}),
          "source" => Keyword.get(opts, :source, "manual"),
          "pool" => Keyword.get(opts, :pool, "default"),
          "priority" => Keyword.get(opts, :priority, 0),
          "pipeline" => entrypoint["pipeline"],
          "payload" =>
            %{
              "intent" => Keyword.get(opts, :intent) || entrypoint["title"] || entrypoint["id"],
              "workspace" => entrypoint["workspace"],
              "inputs" => stringify(inputs)
            }
            |> maybe_put("dedupe_key", opts[:dedupe_key] || opts[:idempotency_key])
            |> maybe_put("reply_ref", opts[:reply_ref])
            |> maybe_put("constraints", opts[:constraints] || defaults["constraints"])
            |> maybe_put(
              "success_criteria",
              opts[:success_criteria] || defaults["success_criteria"]
            ),
          "budget" => Keyword.get(opts, :budget) || defaults["budget"] || %{},
          "provenance" => %{
            "app_id" => app.app_id,
            "app_name" => app.name,
            "entrypoint" => entrypoint["id"]
          }
        })

      {:ok, task}
    end
  end

  def task_for_entrypoint(_app, _entrypoint_id, _inputs, _opts),
    do: {:error, {:invalid_application_input, "inputs must be an object"}}

  @doc """
  Apply tenant-side I/O binding overrides to a loaded app without changing app_id.

  Binding overrides are install/runtime state, not part of the immutable app
  package identity. They are still validated like manifest `io` and must not
  contain credential-shaped fields.
  """
  @spec apply_io_bindings(t(), map()) :: {:ok, t()} | {:error, term()}
  def apply_io_bindings(%__MODULE__{} = app, %{} = io) do
    with :ok <- reject_secret_fields(%{"io" => io}),
         {:ok, normalized_io} <- normalize_io(io, app.entrypoints) do
      {:ok, %{app | io: normalized_io}}
    end
  end

  def apply_io_bindings(%__MODULE__{}, _io),
    do: {:error, {:invalid_application, "install io bindings must be an object"}}

  defp manifest_path(path) do
    cond do
      File.dir?(path) ->
        ["app.json", "app.yaml", "app.yml"]
        |> Enum.map(&Path.join(path, &1))
        |> Enum.find(&File.regular?/1)
        |> case do
          nil -> {:error, {:application_not_found, path}}
          manifest -> {:ok, manifest}
        end

      File.regular?(path) ->
        {:ok, path}

      true ->
        {:error, {:application_not_found, path}}
    end
  end

  defp maybe_apply_binding_sidecar(app, dir, opts) do
    if Keyword.get(opts, :apply_bindings, true) == false do
      {:ok, app}
    else
      apply_binding_sidecar(app, dir)
    end
  end

  defp apply_binding_sidecar(app, dir) do
    path = Path.join(dir, "bindings.json")

    case File.read(path) do
      {:ok, raw} ->
        with {:ok, %{} = sidecar} <- Jason.decode(raw),
             :ok <- validate_binding_sidecar(sidecar, app),
             {:ok, app} <- apply_io_bindings(app, sidecar["io"]) do
          {:ok, app}
        else
          {:ok, _other} ->
            {:error, {:invalid_application, "bindings.json must be an object"}}

          {:error, %Jason.DecodeError{} = error} ->
            {:error,
             {:invalid_application, "bad JSON in bindings.json: #{Exception.message(error)}"}}

          {:error, _reason} = error ->
            error
        end

      {:error, :enoent} ->
        {:ok, app}

      {:error, reason} ->
        {:error, {:invalid_application, "could not read bindings.json: #{inspect(reason)}"}}
    end
  end

  defp validate_binding_sidecar(%{} = sidecar, app) do
    with :ok <- reject_unknown_fields(sidecar, @binding_sidecar_keys, "bindings.json"),
         :ok <- verify_binding_sidecar_app_id(sidecar, app),
         :ok <- verify_binding_sidecar_io(sidecar),
         :ok <- verify_binding_sidecar_metadata(sidecar) do
      :ok
    end
  end

  defp verify_binding_sidecar_app_id(%{"app_id" => app_id}, %__MODULE__{app_id: app_id})
       when is_binary(app_id),
       do: :ok

  defp verify_binding_sidecar_app_id(%{"app_id" => app_id}, _app) when is_binary(app_id),
    do: {:error, {:invalid_application, "bindings.json app_id mismatch"}}

  defp verify_binding_sidecar_app_id(%{"app_id" => _other}, _app),
    do: {:error, {:invalid_application, "bindings.json app_id must be a string"}}

  defp verify_binding_sidecar_app_id(_sidecar, _app),
    do: {:error, {:invalid_application, "bindings.json app_id is required"}}

  defp verify_binding_sidecar_io(%{"io" => %{} = _io}), do: :ok

  defp verify_binding_sidecar_io(%{"io" => _other}),
    do: {:error, {:invalid_application, "bindings.json io must be an object"}}

  defp verify_binding_sidecar_io(_sidecar),
    do: {:error, {:invalid_application, "bindings.json io is required"}}

  defp verify_binding_sidecar_metadata(%{"credential_scope" => value}) when not is_binary(value),
    do: {:error, {:invalid_application, "bindings.json credential_scope must be a string"}}

  defp verify_binding_sidecar_metadata(%{"defaults_overridden?" => value})
       when not is_boolean(value),
       do:
         {:error, {:invalid_application, "bindings.json defaults_overridden? must be a boolean"}}

  defp verify_binding_sidecar_metadata(_sidecar), do: :ok

  defp decode_manifest(path, raw) do
    case String.downcase(Path.extname(path)) do
      ".json" ->
        case Jason.decode(raw) do
          {:ok, %{} = manifest} ->
            {:ok, manifest}

          {:ok, _other} ->
            {:error, {:invalid_application, "application manifest must be an object"}}

          {:error, reason} ->
            {:error, {:invalid_application_json, Exception.message(reason)}}
        end

      ext when ext in [".yaml", ".yml"] ->
        Mitos.ManifestYaml.decode(raw)

      ext ->
        {:error, {:unsupported_manifest_format, ext}}
    end
  end

  defp reject_unknown_top_level_fields(manifest) do
    reject_unknown_fields(manifest, @top_level_keys, nil)
  end

  defp reject_unknown_fields(map, allowed, nil) do
    extras =
      map
      |> Map.keys()
      |> Enum.reject(&(&1 in allowed))
      |> Enum.sort()

    case extras do
      [] -> :ok
      [field | _] -> {:error, {:invalid_application, ~s(unsupported field "#{field}")}}
    end
  end

  defp reject_unknown_fields(map, allowed, field) do
    extras =
      map
      |> Map.keys()
      |> Enum.reject(&(&1 in allowed))
      |> Enum.sort()

    case extras do
      [] -> :ok
      [extra | _] -> {:error, {:invalid_application, ~s(unsupported field "#{field}.#{extra}")}}
    end
  end

  defp reject_secret_fields(value), do: reject_secret_fields(value, [])

  defp reject_secret_fields(%{} = map, path) do
    Enum.reduce_while(map, :ok, fn {key, value}, :ok ->
      key = to_string(key)
      field_path = Enum.reverse([key | path]) |> Enum.join(".")

      cond do
        Regex.match?(@secret_key_pattern, key) ->
          {:halt,
           {:error,
            {:invalid_application,
             "application manifests must not contain secret field #{field_path}"}}}

        true ->
          case reject_secret_fields(value, [key | path]) do
            :ok -> {:cont, :ok}
            {:error, _reason} = error -> {:halt, error}
          end
      end
    end)
  end

  defp reject_secret_fields(values, path) when is_list(values) do
    values
    |> Enum.with_index()
    |> Enum.reduce_while(:ok, fn {value, index}, :ok ->
      case reject_secret_fields(value, [Integer.to_string(index) | path]) do
        :ok -> {:cont, :ok}
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp reject_secret_fields(_value, _path), do: :ok

  defp normalize_capabilities(values) when is_list(values) do
    with {:ok, capabilities} <-
           normalize_indexed(values, "capabilities", fn value, index ->
             with %{} = capability <- map_or_nil(value),
                  {:ok, ref} <- required_string(capability, "ref", "capabilities.#{index}.ref"),
                  {:ok, alias} <- required_string(capability, "as", "capabilities.#{index}.as"),
                  :ok <- validate_id(alias, "capabilities.#{index}.as") do
               {:ok, %{"ref" => ref, "as" => alias}}
             else
               nil ->
                 {:error, {:invalid_application, "capabilities.#{index} must be an object"}}

               {:error, _reason} = error ->
                 error
             end
           end),
         :ok <- ensure_nonempty(capabilities, "capabilities"),
         :ok <- ensure_unique(capabilities, "as", "capability alias") do
      {:ok, capabilities}
    end
  end

  defp normalize_capabilities(_values),
    do: {:error, {:invalid_application, "capabilities must be a non-empty list"}}

  defp normalize_pipelines(values, app_root) when is_list(values) do
    with {:ok, root} <- require_app_root(app_root),
         {:ok, pipelines} <-
           normalize_indexed(values, "pipelines", fn value, index ->
             with %{} = pipeline <- map_or_nil(value),
                  {:ok, id} <- required_string(pipeline, "id", "pipelines.#{index}.id"),
                  :ok <- validate_id(id, "pipelines.#{index}.id"),
                  {:ok, file} <- required_string(pipeline, "file", "pipelines.#{index}.file"),
                  {:ok, sha} <- pipeline_file_sha(root, file, index),
                  {:ok, requires} <- normalize_pipeline_requires(pipeline["requires"], index) do
               {:ok,
                %{"id" => id, "file" => file, "sha256" => sha}
                |> maybe_put("requires", requires)}
             else
               nil ->
                 {:error, {:invalid_application, "pipelines.#{index} must be an object"}}

               {:error, _reason} = error ->
                 error
             end
           end),
         :ok <- ensure_nonempty(pipelines, "pipelines"),
         :ok <- ensure_unique(pipelines, "id", "pipeline id") do
      {:ok, pipelines}
    end
  end

  defp normalize_pipelines(_values, _app_root),
    do: {:error, {:invalid_application, "pipelines must be a non-empty list"}}

  defp normalize_pipeline_requires(nil, _index), do: {:ok, nil}

  defp normalize_pipeline_requires(%{} = requires, index) do
    requires = stringify(requires)

    with :ok <-
           reject_unknown_fields(requires, @pipeline_requires_keys, "pipelines.#{index}.requires"),
         {:ok, services} <-
           required_string_list(requires["services"], "pipelines.#{index}.requires.services"),
         {:ok, targets} <-
           required_string_list(requires["targets"], "pipelines.#{index}.requires.targets") do
      {:ok,
       %{}
       |> maybe_put("services", services)
       |> maybe_put("targets", targets)}
    end
  end

  defp normalize_pipeline_requires(_requires, index),
    do: {:error, {:invalid_application, "pipelines.#{index}.requires must be an object"}}

  defp normalize_entrypoints(values, pipelines) when is_list(values) do
    pipeline_ids = MapSet.new(Enum.map(pipelines, & &1["id"]))

    with {:ok, entrypoints} <-
           normalize_indexed(values, "entrypoints", fn value, index ->
             with %{} = entrypoint <- map_or_nil(value),
                  {:ok, id} <- required_string(entrypoint, "id", "entrypoints.#{index}.id"),
                  :ok <- validate_id(id, "entrypoints.#{index}.id"),
                  {:ok, pipeline} <-
                    required_string(entrypoint, "pipeline", "entrypoints.#{index}.pipeline"),
                  :ok <- ensure_pipeline_exists(pipeline, pipeline_ids, index),
                  {:ok, workspace} <- workspace_value(entrypoint["workspace"], index),
                  {:ok, inputs} <- normalize_inputs(entrypoint["inputs"], index) do
               {:ok,
                %{
                  "id" => id,
                  "pipeline" => pipeline,
                  "workspace" => workspace,
                  "inputs" => inputs
                }
                |> maybe_put("title", optional_string(entrypoint["title"]))
                |> maybe_put("description", optional_string(entrypoint["description"]))
                |> maybe_put("defaults", map_or_nil(entrypoint["defaults"]))}
             else
               nil ->
                 {:error, {:invalid_application, "entrypoints.#{index} must be an object"}}

               {:error, _reason} = error ->
                 error
             end
           end),
         :ok <- ensure_nonempty(entrypoints, "entrypoints"),
         :ok <- ensure_unique(entrypoints, "id", "entrypoint id") do
      {:ok, entrypoints}
    end
  end

  defp normalize_entrypoints(_values, _pipelines),
    do: {:error, {:invalid_application, "entrypoints must be a non-empty list"}}

  defp normalize_inputs(nil, _index), do: {:ok, %{"schema" => %{}}}

  defp normalize_inputs(%{} = inputs, index) do
    inputs = stringify(inputs)
    schema = inputs["schema"] || %{}

    if is_map(schema) do
      with {:ok, schema} <- normalize_input_schema(schema, index) do
        {:ok, Map.put(inputs, "schema", schema)}
      end
    else
      {:error, {:invalid_application, "entrypoints.#{index}.inputs.schema must be an object"}}
    end
  end

  defp normalize_inputs(_inputs, index),
    do: {:error, {:invalid_application, "entrypoints.#{index}.inputs must be an object"}}

  defp normalize_input_schema(%{} = schema, index) do
    schema = stringify(schema)

    with :ok <- validate_schema_root_type(schema, index),
         :ok <- validate_schema_required(schema, index),
         :ok <- validate_schema_additional_properties(schema, index),
         :ok <- validate_schema_properties(schema, index),
         :ok <- validate_schema_shorthand_properties(schema, index) do
      {:ok, schema}
    end
  end

  defp validate_schema_root_type(%{"type" => "object"}, _index), do: :ok

  defp validate_schema_root_type(%{"type" => _type}, index),
    do: {:error, {:invalid_application, "entrypoints.#{index}.inputs.schema.type must be object"}}

  defp validate_schema_root_type(_schema, _index), do: :ok

  defp validate_schema_required(%{"required" => required}, index) when is_list(required) do
    case required_string_list(required, "entrypoints.#{index}.inputs.schema.required") do
      {:ok, _values} -> :ok
      {:error, _reason} = error -> error
    end
  end

  defp validate_schema_required(%{"required" => _required}, index),
    do:
      {:error,
       {:invalid_application, "entrypoints.#{index}.inputs.schema.required must be a list"}}

  defp validate_schema_required(_schema, _index), do: :ok

  defp validate_schema_additional_properties(%{"additionalProperties" => value}, _index)
       when is_boolean(value),
       do: :ok

  defp validate_schema_additional_properties(%{"additionalProperties" => _value}, index),
    do:
      {:error,
       {:invalid_application,
        "entrypoints.#{index}.inputs.schema.additionalProperties must be a boolean"}}

  defp validate_schema_additional_properties(_schema, _index), do: :ok

  defp validate_schema_properties(%{"properties" => properties}, index) when is_map(properties) do
    properties
    |> stringify()
    |> Enum.reduce_while(:ok, fn {name, property}, :ok ->
      field = "entrypoints.#{index}.inputs.schema.properties.#{name}"
      validate_input_property_schema(property, field)
    end)
  end

  defp validate_schema_properties(%{"properties" => _properties}, index),
    do:
      {:error,
       {:invalid_application, "entrypoints.#{index}.inputs.schema.properties must be an object"}}

  defp validate_schema_properties(_schema, _index), do: :ok

  defp validate_schema_shorthand_properties(schema, index) do
    schema
    |> Map.drop(@schema_top_level_keys)
    |> Enum.reduce_while(:ok, fn {name, property}, :ok ->
      field = "entrypoints.#{index}.inputs.schema.#{name}"
      validate_input_property_schema(property, field)
    end)
  end

  defp validate_input_property_schema(%{} = property, field) do
    property = stringify(property)

    case property["type"] do
      nil ->
        {:cont, :ok}

      type when is_binary(type) and type in @input_schema_types ->
        {:cont, :ok}

      type when is_binary(type) ->
        {:halt, {:error, {:invalid_application, "#{field}.type is unsupported"}}}

      _type ->
        {:halt, {:error, {:invalid_application, "#{field}.type must be a string"}}}
    end
  end

  defp validate_input_property_schema(_property, field),
    do: {:halt, {:error, {:invalid_application, "#{field} must be an object"}}}

  defp normalize_io(nil, _entrypoints), do: {:ok, %{"inputs" => [], "outputs" => []}}

  defp normalize_io(%{} = io, entrypoints) do
    io = stringify(io)
    entrypoints_by_id = Map.new(entrypoints, &{&1["id"], &1})

    with {:ok, inputs} <-
           normalize_io_bindings(io["inputs"] || [], "io.inputs", entrypoints_by_id),
         {:ok, outputs} <-
           normalize_io_bindings(io["outputs"] || [], "io.outputs", entrypoints_by_id) do
      {:ok, %{"inputs" => inputs, "outputs" => outputs}}
    end
  end

  defp normalize_io(_io, _entrypoints),
    do: {:error, {:invalid_application, "io must be an object"}}

  defp normalize_io_bindings(values, field, entrypoints_by_id) when is_list(values) do
    class_key = io_binding_class_key(field)

    normalize_indexed(values, field, fn value, index ->
      with %{} = binding <- map_or_nil(value),
           {:ok, _class} <- required_string(binding, class_key, "#{field}.#{index}.#{class_key}"),
           {:ok, entrypoint} <-
             required_string(binding, "entrypoint", "#{field}.#{index}.entrypoint"),
           {:ok, entrypoint_spec} <-
             io_binding_entrypoint(entrypoints_by_id, entrypoint, field, index),
           :ok <- validate_io_binding_select(binding, field, index, entrypoint_spec) do
        {:ok, binding}
      else
        nil -> {:error, {:invalid_application, "#{field}.#{index} must be an object"}}
        {:error, _reason} = error -> error
      end
    end)
  end

  defp normalize_io_bindings(_values, field, _entrypoints_by_id),
    do: {:error, {:invalid_application, "#{field} must be a list"}}

  defp io_binding_entrypoint(entrypoints_by_id, entrypoint, field, index) do
    case Map.get(entrypoints_by_id, entrypoint) do
      %{} = entrypoint_spec -> {:ok, entrypoint_spec}
      _missing -> {:error, {:invalid_application, "#{field}.#{index}.entrypoint is unknown"}}
    end
  end

  defp validate_io_binding_select(_binding, "io.outputs", _index, _entrypoint), do: :ok

  defp validate_io_binding_select(%{"select" => select}, field, index, entrypoint)
       when is_map(select) do
    select = stringify(select)

    with :ok <- validate_io_select_mode(select["mode"], field, index),
         :ok <- validate_io_select_pipeline(select["pipeline"], field, index, entrypoint) do
      :ok
    end
  end

  defp validate_io_binding_select(%{"select" => _select}, field, index, _entrypoint),
    do: {:error, {:invalid_application, "#{field}.#{index}.select must be an object"}}

  defp validate_io_binding_select(_binding, _field, _index, _entrypoint), do: :ok

  defp validate_io_select_mode(nil, _field, _index), do: :ok
  defp validate_io_select_mode("explicit", _field, _index), do: :ok

  defp validate_io_select_mode(mode, field, index) when is_binary(mode),
    do: {:error, {:invalid_application, "#{field}.#{index}.select.mode is unsupported"}}

  defp validate_io_select_mode(_mode, field, index),
    do: {:error, {:invalid_application, "#{field}.#{index}.select.mode must be a string"}}

  defp validate_io_select_pipeline(nil, _field, _index, _entrypoint), do: :ok

  defp validate_io_select_pipeline(pipeline, _field, _index, %{"pipeline" => pipeline})
       when is_binary(pipeline) and pipeline != "",
       do: :ok

  defp validate_io_select_pipeline(pipeline, field, index, _entrypoint)
       when is_binary(pipeline) and pipeline != "",
       do:
         {:error,
          {:invalid_application,
           "#{field}.#{index}.select.pipeline must match entrypoint pipeline"}}

  defp validate_io_select_pipeline(_pipeline, field, index, _entrypoint),
    do:
      {:error,
       {:invalid_application, "#{field}.#{index}.select.pipeline must be a non-empty string"}}

  defp io_binding_class_key("io.inputs"), do: "connector"
  defp io_binding_class_key("io.outputs"), do: "sink"

  defp normalize_services(nil), do: {:ok, %{"requires" => [], "wants" => []}}

  defp normalize_services(%{} = services) do
    services = stringify(services)

    with :ok <- reject_unknown_fields(services, @service_keys, "services"),
         {:ok, requires} <- required_string_list(services["requires"], "services.requires"),
         {:ok, wants} <- required_string_list(services["wants"], "services.wants") do
      {:ok, %{"requires" => requires, "wants" => wants}}
    end
  end

  defp normalize_services(_services),
    do: {:error, {:invalid_application, "services must be an object"}}

  defp normalize_requirements(nil), do: {:ok, %{}}

  defp normalize_requirements(%{} = requirements) do
    requirements = stringify(requirements)

    with :ok <- reject_unknown_fields(requirements, @requirement_keys, "requirements"),
         {:ok, harnesses} <-
           required_string_list(requirements["harnesses"], "requirements.harnesses"),
         {:ok, connectors} <-
           required_string_list(requirements["connectors"], "requirements.connectors"),
         {:ok, egress} <- required_string_list(requirements["egress"], "requirements.egress"),
         {:ok, trust_tier} <-
           normalize_requirement_trust_tier(requirements["trust_tier"]),
         {:ok, resources} <- normalize_requirement_resources(requirements["resources"]) do
      {:ok,
       %{}
       |> maybe_put("harnesses", harnesses)
       |> maybe_put("connectors", connectors)
       |> maybe_put("egress", egress)
       |> maybe_put("trust_tier", trust_tier)
       |> maybe_put("resources", resources)}
    end
  end

  defp normalize_requirements(_requirements),
    do: {:error, {:invalid_application, "requirements must be an object"}}

  defp normalize_requirement_trust_tier(nil), do: {:ok, nil}

  defp normalize_requirement_trust_tier(value) do
    case optional_string(value) do
      tier when tier in @trust_tiers ->
        {:ok, tier}

      _other ->
        {:error, {:invalid_application, "requirements.trust_tier must be one of T0, T1, T2"}}
    end
  end

  defp normalize_requirement_resources(nil), do: {:ok, nil}
  defp normalize_requirement_resources(%{} = resources), do: {:ok, resources}

  defp normalize_requirement_resources(_resources),
    do: {:error, {:invalid_application, "requirements.resources must be an object"}}

  defp validate_requirement_connectors(%{"connectors" => declared}, %{"inputs" => inputs})
       when is_list(inputs) do
    declared = MapSet.new(declared)

    missing =
      inputs
      |> Enum.map(&optional_string(&1["connector"]))
      |> Enum.reject(&is_nil/1)
      |> Enum.uniq()
      |> Enum.reject(&MapSet.member?(declared, &1))

    case missing do
      [] ->
        :ok

      [connector | _rest] ->
        {:error,
         {:invalid_application,
          "requirements.connectors must include io.inputs connector #{connector}"}}
    end
  end

  defp validate_requirement_connectors(_requirements, _io), do: :ok

  defp normalize_indexed(values, _field, fun) do
    values
    |> Enum.with_index()
    |> Enum.reduce_while({:ok, []}, fn {value, index}, {:ok, acc} ->
      case fun.(value, index) do
        {:ok, normalized} -> {:cont, {:ok, acc ++ [normalized]}}
        {:error, _reason} = error -> {:halt, error}
      end
    end)
  end

  defp ensure_nonempty([], field),
    do: {:error, {:invalid_application, "#{field} must be a non-empty list"}}

  defp ensure_nonempty(_values, _field), do: :ok

  defp ensure_unique(values, key, label) do
    duplicates =
      values
      |> Enum.map(& &1[key])
      |> Enum.frequencies()
      |> Enum.find(fn {_value, count} -> count > 1 end)

    case duplicates do
      nil -> :ok
      {value, _count} -> {:error, {:invalid_application, "duplicate #{label} #{inspect(value)}"}}
    end
  end

  defp require_app_root(nil),
    do: {:error, {:invalid_application, "app_root is required to hash pipeline files"}}

  defp require_app_root(root) when is_binary(root), do: {:ok, Path.expand(root)}

  defp pipeline_file_sha(root, file, index) do
    with :ok <- safe_relative_path(file, "pipelines.#{index}.file"),
         path <- Path.expand(file, root),
         true <- inside_root?(path, root),
         {:ok, bytes} <- File.read(path) do
      {:ok, sha256(bytes)}
    else
      false ->
        {:error, {:invalid_application, "pipelines.#{index}.file escapes the application root"}}

      {:error, {:invalid_application, _detail} = reason} ->
        {:error, reason}

      {:error, reason} ->
        {:error,
         {:invalid_application, "could not read pipelines.#{index}.file: #{inspect(reason)}"}}
    end
  end

  defp safe_relative_path(path, field) when is_binary(path) do
    cond do
      Path.type(path) != :relative ->
        {:error, {:invalid_application, "#{field} must be a relative path"}}

      ".." in Path.split(path) ->
        {:error, {:invalid_application, "#{field} must not contain .."}}

      reserved_app_file?(path) ->
        {:error, {:invalid_application, "#{field} must not overwrite application control files"}}

      true ->
        :ok
    end
  end

  defp safe_relative_path(_path, field),
    do: {:error, {:invalid_application, "#{field} must be a relative path"}}

  defp reserved_app_file?(path) do
    case Path.split(path) do
      [file] -> file in @reserved_app_files
      _parts -> false
    end
  end

  defp inside_root?(path, root) do
    root = Path.expand(root)
    path = Path.expand(path)
    path == root or String.starts_with?(path, root <> "/")
  end

  defp ensure_pipeline_exists(pipeline, pipeline_ids, index) do
    if MapSet.member?(pipeline_ids, pipeline) do
      :ok
    else
      {:error,
       {:invalid_application, "entrypoints.#{index}.pipeline references unknown pipeline"}}
    end
  end

  defp workspace_value(nil, _index), do: {:ok, "none"}
  defp workspace_value(value, _index) when value in @workspace_values, do: {:ok, value}

  defp workspace_value(_value, index),
    do: {:error, {:invalid_application, "entrypoints.#{index}.workspace is invalid"}}

  defp input_schema(%{"inputs" => %{"schema" => %{} = schema}}), do: schema
  defp input_schema(_entrypoint), do: %{}

  defp validate_input_schema(schema, inputs) do
    schema = stringify(schema)
    properties = schema_properties(schema)
    required = string_list(schema["required"])

    cond do
      missing_required(required, inputs) != [] ->
        {:error,
         {:invalid_application_input,
          "missing required input #{missing_required(required, inputs) |> hd()}"}}

      schema["additionalProperties"] == false and unknown_inputs(properties, inputs) != [] ->
        {:error,
         {:invalid_application_input,
          "unknown input #{unknown_inputs(properties, inputs) |> hd()}"}}

      true ->
        validate_input_types(properties, inputs)
    end
  end

  defp schema_properties(%{"properties" => %{} = properties}), do: stringify(properties)

  defp schema_properties(%{} = schema) do
    schema
    |> Map.drop(~w(type required additionalProperties description title))
    |> Enum.filter(fn {_key, value} -> is_map(value) end)
    |> Map.new()
  end

  defp missing_required(required, inputs), do: Enum.reject(required, &Map.has_key?(inputs, &1))

  defp unknown_inputs(properties, inputs) do
    allowed = MapSet.new(Map.keys(properties))

    inputs
    |> Map.keys()
    |> Enum.reject(&MapSet.member?(allowed, &1))
  end

  defp validate_input_types(properties, inputs) do
    Enum.reduce_while(inputs, :ok, fn {key, value}, :ok ->
      case Map.get(properties, key) do
        %{} = property ->
          case input_type_valid?(property["type"], value) do
            true ->
              {:cont, :ok}

            false ->
              {:halt, {:error, {:invalid_application_input, "input #{key} has invalid type"}}}
          end

        _property ->
          {:cont, :ok}
      end
    end)
  end

  defp input_type_valid?(nil, _value), do: true
  defp input_type_valid?("string", value), do: is_binary(value)
  defp input_type_valid?("integer", value), do: is_integer(value)
  defp input_type_valid?("int", value), do: is_integer(value)
  defp input_type_valid?("number", value), do: is_integer(value) or is_float(value)
  defp input_type_valid?("boolean", value), do: is_boolean(value)
  defp input_type_valid?("bool", value), do: is_boolean(value)
  defp input_type_valid?("object", value), do: is_map(value)
  defp input_type_valid?("array", value), do: is_list(value)
  defp input_type_valid?("file", value) when is_binary(value), do: value != ""
  defp input_type_valid?("file", %{} = value), do: is_binary(value["path"] || value["name"])
  defp input_type_valid?(_type, _value), do: true

  defp generated_task_id(app, entrypoint) do
    "app-" <>
      Base.url_encode64(
        :crypto.strong_rand_bytes(8),
        padding: false
      ) <>
      "-#{app.name}-#{entrypoint["id"]}"
  end

  defp app_id(manifest), do: "app:" <> sha256(canonical_json(manifest))

  defp sha256(bytes) when is_binary(bytes) do
    digest =
      :crypto.hash(:sha256, bytes)
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
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

  defp required_string(map, key), do: required_string(map, key, key)

  defp required_string(%{} = map, key, field) do
    case optional_string(map[key]) do
      nil -> {:error, {:invalid_application, "#{field} is required"}}
      value -> {:ok, value}
    end
  end

  defp validate_id(value, field) do
    if Regex.match?(@id_pattern, value) do
      :ok
    else
      {:error, {:invalid_application, "#{field} must be a safe identifier"}}
    end
  end

  defp stringify(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), stringify_value(value)} end)

  defp stringify_value(%{} = map), do: stringify(map)
  defp stringify_value(values) when is_list(values), do: Enum.map(values, &stringify_value/1)
  defp stringify_value(value), do: value

  defp map_or(%{} = map, _default), do: stringify(map)
  defp map_or(_value, default), do: default

  defp map_or_nil(%{} = map), do: stringify(map)
  defp map_or_nil(_value), do: nil

  defp optional_string(value) when is_binary(value) do
    value = String.trim(value)
    if value == "", do: nil, else: value
  end

  defp optional_string(_value), do: nil

  defp string_list(values) when is_list(values) do
    values
    |> Enum.filter(&is_binary/1)
    |> Enum.map(&String.trim/1)
    |> Enum.reject(&(&1 == ""))
  end

  defp string_list(_values), do: []

  defp required_string_list(nil, _field), do: {:ok, []}

  defp required_string_list(values, field) when is_list(values) do
    normalize_indexed(values, field, fn value, index ->
      case optional_string(value) do
        nil -> {:error, {:invalid_application, "#{field}.#{index} must be a non-empty string"}}
        string -> {:ok, string}
      end
    end)
  end

  defp required_string_list(_values, field),
    do: {:error, {:invalid_application, "#{field} must be a list"}}

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, _key, []), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)
end
