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
        defaults: ep["defaults"] || %{}
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
end
