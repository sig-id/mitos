defmodule Mitos.Workload do
  @moduledoc """
  A workload is a seller-packaged process: a provider, a prompt template, and
  run policy, stored as one JSON file under the workloads directory. The
  template carries `{{placeholder}}` slots that are bound per request, so the
  fixed process stays server-side and only outputs leave the host.

  This is the unit the Gentix facilitator sells. `POST /workloads/:id/execute`
  (see `Mitos.Web.Router`) renders the template with the buyer's inputs, runs
  it through `Mitos.Workload.Runner`, and answers with a Gentix-compliant
  provider-execution response.

  Definition file (`<workloads_dir>/<id>.json`):

      {
        "provider": "claude",
        "template": "Critique this design...\\n\\n{{objective}}",
        "transport_order": ["stdio-json"],
        "env": {"SOME_VAR": "value"},
        "env_passthrough": ["ANTHROPIC_API_KEY"],
        "timeout_ms": 120000,
        "approvals": "deny",
        "title": "Design critique",
        "description": "Structured design critique process"
      }

  `env` values are stored in the file (operator-managed for now); secrets are
  better referenced via `env_passthrough`, which copies named variables from
  the host environment at run time. Neither is ever exposed over HTTP — see
  `public_view/1`.
  """

  @enforce_keys [:id, :provider, :template]
  defstruct [
    :id,
    :provider,
    :template,
    :cwd,
    :title,
    :description,
    transport_order: ["stdio-json", "pty"],
    env: %{},
    env_passthrough: [],
    timeout_ms: 120_000,
    idle_ms: 150,
    approvals: "deny"
  ]

  @id_pattern ~r/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/
  @placeholder ~r/\{\{\s*([a-zA-Z0-9_.\-]+)\s*\}\}/

  @doc """
  Directory holding workload definition files. Resolution order: app config
  `:workloads_dir`, `MITOS_WORKLOADS_DIR`, then `<repo>/workloads`.
  """
  def dir do
    Application.get_env(:mitos, :workloads_dir) ||
      System.get_env("MITOS_WORKLOADS_DIR") ||
      Path.expand("../../../workloads", __DIR__)
  end

  @doc "List all parseable workload definitions in the workloads directory."
  def list do
    case File.ls(dir()) do
      {:ok, entries} ->
        entries
        |> Enum.filter(&String.ends_with?(&1, ".json"))
        |> Enum.sort()
        |> Enum.flat_map(fn file ->
          id = Path.rootname(file)

          case fetch(id) do
            {:ok, workload} -> [workload]
            {:error, _} -> []
          end
        end)

      {:error, _} ->
        []
    end
  end

  @doc "Load one workload by id. The id must be a plain filename stem."
  def fetch(id) when is_binary(id) do
    cond do
      not Regex.match?(@id_pattern, id) ->
        {:error, :not_found}

      true ->
        path = Path.join(dir(), id <> ".json")

        with {:ok, raw} <- read_file(path),
             {:ok, json} <- decode_json(json_source(raw)),
             {:ok, workload} <- from_json(id, json) do
          {:ok, workload}
        end
    end
  end

  def fetch(_id), do: {:error, :not_found}

  @doc "Build a workload struct from decoded JSON, applying defaults."
  def from_json(id, %{} = json) do
    provider = json["provider"]
    template = json["template"]

    cond do
      not is_binary(provider) or provider == "" ->
        {:error, {:invalid_workload, "provider is required"}}

      not is_binary(template) or template == "" ->
        {:error, {:invalid_workload, "template is required"}}

      true ->
        defaults = %__MODULE__{id: id, provider: provider, template: template}

        {:ok,
         %__MODULE__{
           defaults
           | cwd: string_or(json["cwd"], defaults.cwd),
             title: string_or(json["title"], defaults.title),
             description: string_or(json["description"], defaults.description),
             transport_order: string_list_or(json["transport_order"], defaults.transport_order),
             env: map_or(json["env"], defaults.env),
             env_passthrough: string_list_or(json["env_passthrough"], defaults.env_passthrough),
             timeout_ms: positive_int_or(json["timeout_ms"], defaults.timeout_ms),
             idle_ms: positive_int_or(json["idle_ms"], defaults.idle_ms),
             approvals: approvals_or(json["approvals"], defaults.approvals)
         }}
    end
  end

  def from_json(_id, _json), do: {:error, {:invalid_workload, "definition must be a JSON object"}}

  @doc """
  Render the template with bindings. Every `{{placeholder}}` must have a
  binding; otherwise `{:error, {:missing_bindings, names}}`.
  """
  def render(%__MODULE__{template: template}, bindings) when is_map(bindings) do
    placeholders =
      @placeholder
      |> Regex.scan(template, capture: :all_but_first)
      |> List.flatten()
      |> Enum.uniq()

    case Enum.reject(placeholders, &Map.has_key?(bindings, &1)) do
      [] ->
        rendered =
          Regex.replace(@placeholder, template, fn _whole, name ->
            Map.fetch!(bindings, name)
          end)

        {:ok, rendered}

      missing ->
        {:error, {:missing_bindings, missing}}
    end
  end

  @doc """
  Environment for the agent run: the stored `env` map plus any
  `env_passthrough` variables present in the host environment.
  """
  def resolved_env(%__MODULE__{} = workload) do
    passthrough =
      for key <- workload.env_passthrough,
          value = System.get_env(key),
          is_binary(value),
          into: %{},
          do: {key, value}

    env = Map.merge(passthrough, workload.env)
    if env == %{}, do: nil, else: env
  end

  @doc "Listing view with secrets and run mechanics stripped."
  def public_view(%__MODULE__{} = workload) do
    %{
      "id" => workload.id,
      "provider" => workload.provider,
      "title" => workload.title,
      "description" => workload.description,
      "placeholders" =>
        @placeholder
        |> Regex.scan(workload.template, capture: :all_but_first)
        |> List.flatten()
        |> Enum.uniq(),
      "timeout_ms" => workload.timeout_ms
    }
  end

  defp read_file(path) do
    case File.read(path) do
      {:ok, raw} -> {:ok, raw}
      {:error, :enoent} -> {:error, :not_found}
      {:error, reason} -> {:error, {:invalid_workload, "unreadable definition: #{inspect(reason)}"}}
    end
  end

  # Definition files may carry a leading comment block of `//` lines so
  # operators can annotate them; strip those before JSON decoding.
  defp json_source(raw) do
    raw
    |> String.split("\n")
    |> Enum.reject(&String.starts_with?(String.trim_leading(&1), "//"))
    |> Enum.join("\n")
  end

  defp decode_json(raw) do
    case Jason.decode(raw) do
      {:ok, json} -> {:ok, json}
      {:error, error} -> {:error, {:invalid_workload, "bad JSON: #{Exception.message(error)}"}}
    end
  end

  defp string_or(value, _default) when is_binary(value) and value != "", do: value
  defp string_or(_value, default), do: default

  defp map_or(%{} = value, _default), do: value
  defp map_or(_value, default), do: default

  defp string_list_or(value, _default) when is_list(value) do
    case Enum.filter(value, &is_binary/1) do
      [] -> []
      list -> list
    end
  end

  defp string_list_or(_value, default), do: default

  defp positive_int_or(value, _default) when is_integer(value) and value > 0, do: value
  defp positive_int_or(_value, default), do: default

  defp approvals_or(value, _default) when value in ["deny", "ignore"], do: value
  defp approvals_or(_value, default), do: default
end
