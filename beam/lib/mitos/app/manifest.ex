defmodule Mitos.App.Manifest do
  @moduledoc """
  The application manifest (01 §8.8, elaborated 12). The application is the
  shipping unit and unit of sale: a bundle of pipelines + the capabilities they
  reference + this manifest. It *references* contracts; it MUST NOT redefine them,
  and it carries no secrets and no price (the offer is facilitator-side, 12 §8).

  Identity is content (12 §7):

      app_id = H(canonical(manifest) ‖ sorted(capability_id …) ‖ sorted(pipeline_sha256 …))
  """

  @enforce_keys [:name, :version, :dir, :app_id]
  defstruct name: nil,
            version: nil,
            description: nil,
            dir: nil,
            # [%{ref, as, capability_id}]  (12 §3)
            capabilities: [],
            # [%{id, file, sha256, body}]  (12 §1)
            pipelines: [],
            # [entrypoint map]  (12 §4)
            entrypoints: [],
            # %{inputs: [...], outputs: [...]}  DEFAULT bindings (10 §4, 12 §5)
            io: %{},
            # %{requires: [...], wants: [...]}  (06 §9–10, 12 §6)
            services: %{},
            # install-time transparency (12 §11)
            requirements: %{},
            app_id: nil

  @doc "Load an application from a directory (app.yaml + pipelines/*)."
  def load(dir) do
    with {:ok, raw} <- read_yaml(Path.join(dir, "app.yaml")),
         {:ok, pipelines} <- load_pipelines(dir, raw["pipelines"] || []),
         {:ok, capabilities} <- resolve_capabilities(dir, raw["capabilities"] || []) do
      build(dir, raw, pipelines, capabilities)
    end
  end

  def load!(dir) do
    case load(dir) do
      {:ok, m} -> m
      {:error, reason} -> raise "application manifest error in #{dir}: #{inspect(reason)}"
    end
  end

  defp read_yaml(path) do
    case YamlElixir.read_from_file(path) do
      {:ok, map} when is_map(map) -> {:ok, map}
      {:ok, _} -> {:error, :not_a_map}
      {:error, e} -> {:error, {:yaml, e}}
    end
  end

  defp load_pipelines(dir, specs) do
    result =
      Enum.map(specs, fn spec ->
        file = spec["file"]
        path = Path.join(dir, file)

        case File.read(path) do
          {:ok, body} ->
            %{id: spec["id"], file: file, body: body, sha256: Mitos.sha256(body)}

          {:error, _} ->
            {:error, {:missing_pipeline, path}}
        end
      end)

    case Enum.find(result, &match?({:error, _}, &1)) do
      nil -> {:ok, result}
      err -> err
    end
  end

  # A capability ref may be an oci://…@sha256 (pinned) or a local path (dev,
  # resolved to a capability_id at load — the dev→publish freeze of 12 §7).
  defp resolve_capabilities(dir, specs) do
    result =
      Enum.map(specs, fn spec ->
        ref = spec["ref"]

        cond do
          is_binary(ref) and String.contains?(ref, "@sha256:") ->
            %{ref: ref, as: spec["as"], capability_id: extract_digest(ref)}

          is_binary(ref) ->
            cap_dir = Path.join(dir, ref)

            case Mitos.Capability.Manifest.load(cap_dir) do
              {:ok, m} -> %{ref: ref, as: spec["as"] || m.name, capability_id: m.capability_id}
              {:error, e} -> {:error, {:capability, ref, e}}
            end

          true ->
            {:error, {:bad_capability_ref, spec}}
        end
      end)

    case Enum.find(result, &match?({:error, _}, &1)) do
      nil -> {:ok, result}
      err -> err
    end
  end

  defp extract_digest(ref), do: ref |> String.split("@sha256:") |> List.last()

  defp build(dir, raw, pipelines, capabilities) do
    cap_ids = capabilities |> Enum.map(& &1.capability_id) |> Enum.sort()
    pipe_shas = pipelines |> Enum.map(& &1.sha256) |> Enum.sort()

    manifest_core = %{
      "name" => raw["name"],
      "version" => raw["version"],
      "entrypoints" => raw["entrypoints"] || [],
      "io" => raw["io"] || %{},
      "services" => raw["services"] || %{},
      "requirements" => raw["requirements"] || %{}
    }

    app_id =
      Mitos.sha256(
        Mitos.canonical_json(manifest_core) <> Enum.join(cap_ids) <> Enum.join(pipe_shas)
      )

    {:ok,
     %__MODULE__{
       name: raw["name"] || Path.basename(dir),
       version: raw["version"] || "0.0.0",
       description: raw["description"],
       dir: dir,
       capabilities: capabilities,
       pipelines: pipelines,
       entrypoints: normalize_entrypoints(raw["entrypoints"] || []),
       io: raw["io"] || %{},
       services: raw["services"] || %{},
       requirements: raw["requirements"] || %{},
       app_id: app_id
     }}
  end

  defp normalize_entrypoints(list) do
    Enum.map(list, fn ep ->
      %{
        id: ep["id"],
        pipeline: ep["pipeline"],
        title: ep["title"],
        description: ep["description"],
        workspace: normalize_workspace(ep["workspace"]),
        inputs: ep["inputs"] || %{},
        parameters: ep["parameters"] || [],
        defaults: ep["defaults"] || %{},
        draft: ep["draft"] == true
      }
    end)
  end

  defp normalize_workspace(nil), do: %{kind: "none"}
  defp normalize_workspace(kind) when is_binary(kind), do: %{kind: kind}

  defp normalize_workspace(%{} = w),
    do: %{kind: w["kind"] || "none", ref: w["ref"], refspec: w["refspec"]}

  @doc "Find an entrypoint by id."
  def entrypoint(%__MODULE__{entrypoints: eps}, id),
    do: Enum.find(eps, &(&1.id == id))

  @doc "Find a pipeline by id."
  def pipeline(%__MODULE__{pipelines: ps}, id), do: Enum.find(ps, &(&1.id == id))

  @doc "Is an entrypoint a draft (imported, order form not yet typed — 12 §10)?"
  def draft?(%__MODULE__{} = app, ep_id) do
    case entrypoint(app, ep_id) do
      %{draft: true} -> true
      _ -> false
    end
  end

  @doc """
  12 §4 — validate caller inputs against an entrypoint's `inputs.schema` BEFORE
  any model spend. Supports two spellings the manifests use: a JSON-Schema
  `{properties, required}` map, or a flat `{field: type}` map. A typed order is
  what makes the job sellable (the quote and refund rule need to know "the job",
  12 §10); rejecting here means no escrow is taken for an un-runnable order.
  """
  def validate_inputs(entrypoint, inputs) when is_map(inputs) do
    schema = (entrypoint.inputs || %{})["schema"] || entrypoint.inputs || %{}
    props = normalize_props(schema["properties"] || drop_meta(schema))
    required = required_fields(schema, props)

    inputs = Map.new(inputs, fn {k, v} -> {to_string(k), v} end)

    with :ok <- check_required(required, inputs, props),
         :ok <- check_types(props, inputs) do
      :ok
    end
  end

  defp drop_meta(schema), do: Map.drop(schema, ["required", "type", "$schema"])

  # Normalize property specs. The flat {field: "string"} shorthand means a
  # field of that type (required only if declared — via a top-level `required`
  # list or the field's own `required: true`); a full {field: {type: …}} map is
  # used as-is.
  defp normalize_props(props) do
    Map.new(props, fn
      {k, v} when is_binary(v) -> {k, %{"type" => v}}
      {k, %{} = v} -> {k, v}
      {k, v} -> {k, %{"type" => to_string(v)}}
    end)
  end

  # `required` may be a top-level list, or per-property (`{field: {required: true}}`).
  defp required_fields(%{"required" => list}, _props) when is_list(list), do: list

  defp required_fields(_, props) do
    for {k, v} <- props, truthy?(v["required"]), do: k
  end

  defp check_required(required, inputs, _props) do
    missing = for k <- required, not Map.has_key?(inputs, k), do: k
    if missing == [], do: :ok, else: {:error, {:missing_required, missing}}
  end

  defp check_types(props, inputs) do
    Enum.reduce_while(props, :ok, fn {field, spec}, _acc ->
      case Map.get(inputs, field) do
        nil ->
          {:cont, :ok}

        value ->
          case check_one_type(spec["type"], value) do
            :ok -> {:cont, :ok}
            {:error, _} = e -> {:halt, e}
          end
      end
    end)
  end

  defp check_one_type(nil, _), do: :ok
  defp check_one_type("string", v) when is_binary(v), do: :ok
  defp check_one_type("file", v) when is_binary(v), do: :ok
  defp check_one_type("int", v) when is_integer(v), do: :ok
  defp check_one_type("integer", v) when is_integer(v), do: :ok
  defp check_one_type("number", v) when is_number(v), do: :ok
  defp check_one_type("boolean", v) when is_boolean(v), do: :ok

  defp check_one_type(type, v) when is_binary(v) and type in ["int", "integer", "number", "boolean"] do
    case coerce(type, v) do
      {:ok, _} -> :ok
      :error -> {:error, {:bad_type, type, v}}
    end
  end

  defp check_one_type(type, v), do: {:error, {:bad_type, type, v}}

  defp coerce(type, v) do
    case type do
      t when t in ["int", "integer"] ->
        case Integer.parse(v) do
          {n, ""} -> {:ok, n}
          _ -> :error
        end

      "number" ->
        case Float.parse(v) do
          {n, ""} -> {:ok, n}
          _ -> :error
        end

      "boolean" ->
        case v do
          b when b in ["true", "false"] -> {:ok, b == "true"}
          _ -> :error
        end
    end
  end

  defp truthy?(true), do: true
  defp truthy?("true"), do: true
  defp truthy?(_), do: false
end
