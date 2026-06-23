defmodule Mitos.PipelineRegistry do
  @moduledoc """
  Operator-facing registry for authored pipeline definitions.

  The current executable pipeline unit is a workload JSON file. This module
  provides a narrower, path-safe create/update surface for the UI instead of
  making the browser write arbitrary files.
  """

  @id_pattern ~r/^[a-zA-Z0-9][a-zA-Z0-9_-]*$/
  @capability_keys ~w(
    data
    egress_policy
    harnesses
    hooks
    image
    name
    resources
    result
    skill
    skill_adapt
    tools
    trust_tier
  )

  @doc "Fetch one pipeline with operator-editable definition fields."
  def get(id) when is_binary(id) do
    with {:ok, workload} <- Mitos.Workload.fetch(id) do
      {:ok, detail(workload)}
    end
  end

  def get(_id), do: {:error, :not_found}

  @doc "Create a new workload-backed pipeline definition."
  def create(attrs) when is_map(attrs) do
    attrs = stringify(attrs)
    id = attrs["id"]

    with :ok <- validate_id(id),
         :ok <- ensure_available(id),
         {:ok, definition} <- definition_from_attrs(id, attrs, %{}),
         :ok <- persist(id, definition),
         {:ok, pipeline} <- get(id) do
      {:ok, pipeline}
    end
  end

  def create(_attrs), do: {:error, {:invalid_pipeline, "pipeline definition must be an object"}}

  @doc "Replace an existing workload-backed pipeline definition."
  def update(id, attrs) when is_binary(id) and is_map(attrs) do
    attrs = attrs |> stringify() |> Map.put("id", id)

    with :ok <- validate_id(id),
         true <- File.exists?(definition_path(id)) || {:error, :not_found},
         {:ok, existing} <- persisted_definition(id),
         {:ok, definition} <- definition_from_attrs(id, attrs, existing),
         :ok <- persist(id, definition),
         {:ok, pipeline} <- get(id) do
      {:ok, pipeline}
    end
  end

  def update(_id, _attrs),
    do: {:error, {:invalid_pipeline, "pipeline definition must be an object"}}

  defp detail(%Mitos.Workload{} = workload) do
    {:ok, public} = Mitos.WorkPoolView.pipeline(workload.id)

    public
    |> Map.put("provider", workload.provider)
    |> Map.put("template", workload.template)
    |> Map.put("cwd", workload.cwd)
    |> Map.put("transport_order", workload.transport_order)
    |> Map.put("approvals", workload.approvals)
    |> Map.put("env_passthrough", workload.env_passthrough)
    |> Map.put("env_keys", workload.env |> Map.keys() |> Enum.sort())
    |> maybe_put("workflow", persisted_workflow(workload.id))
    |> Map.put("editable", true)
  end

  defp definition_from_attrs(id, attrs, existing) do
    workflow = workflow_from_attrs(attrs)

    definition =
      %{
        "provider" => string_or_nil(attrs["provider"]),
        "template" => string_or_nil(attrs["template"]) || template_from_workflow(workflow)
      }
      |> maybe_put("title", string_or_nil(attrs["title"]))
      |> maybe_put("description", string_or_nil(attrs["description"]))
      |> maybe_put("timeout_ms", positive_int_or_nil(attrs["timeout_ms"]))
      |> maybe_put("cwd", string_or_nil(attrs["cwd"]))
      |> maybe_put(
        "approvals",
        value_or_existing(attrs, existing, "approvals", &approvals_or_nil/1)
      )
      |> maybe_put(
        "transport_order",
        value_or_existing(attrs, existing, "transport_order", &string_list_or_nil/1)
      )
      |> maybe_put(
        "env_passthrough",
        value_or_existing(attrs, existing, "env_passthrough", &string_list_allow_empty/1)
      )
      |> maybe_put("env", value_or_existing(attrs, existing, "env", &env_or_nil/1))
      |> maybe_put("workflow", workflow)
      |> maybe_put("capability", capability_from_attrs(id, attrs))

    case Mitos.Workload.from_json(id, definition, package_root: Mitos.Workload.dir()) do
      {:ok, _workload} -> {:ok, drop_nil_values(definition)}
      {:error, {:invalid_workload, detail}} -> {:error, {:invalid_pipeline, detail}}
      {:error, reason} -> {:error, reason}
    end
  end

  defp capability_from_attrs(id, attrs) do
    provider = string_or_nil(attrs["provider"])
    capability = attrs["capability"] |> map_or(%{}) |> stringify()

    trust_tier =
      string_or_nil(attrs["trust_tier"]) ||
        string_or_nil(capability["trust_tier"]) ||
        "T1"

    harnesses =
      case string_list_or_nil(capability["harnesses"]) do
        nil when is_binary(provider) -> [provider]
        nil -> []
        values -> values
      end

    capability
    |> Map.take(@capability_keys)
    |> Map.put("name", string_or_nil(capability["name"]) || string_or_nil(attrs["title"]) || id)
    |> Map.put("trust_tier", trust_tier)
    |> Map.put("harnesses", harnesses)
    |> drop_nil_values()
  end

  defp persist(id, definition) do
    with :ok <- File.mkdir_p(Mitos.Workload.dir()),
         :ok <-
           File.write(
             definition_path(id),
             Jason.encode!(definition, pretty: true) <> "\n"
           ) do
      :ok
    else
      {:error, reason} -> {:error, {:invalid_pipeline, "could not persist definition: #{reason}"}}
    end
  end

  defp definition_path(id), do: Path.join(Mitos.Workload.dir(), id <> ".json")

  defp persisted_workflow(id) do
    with {:ok, json} <- persisted_definition(id),
         %{} = workflow <- json["workflow"] do
      workflow_from_map(workflow)
    else
      _ -> nil
    end
  end

  defp persisted_definition(id) do
    with {:ok, raw} <- File.read(definition_path(id)),
         {:ok, json} <- Jason.decode(strip_json_comments(raw)) do
      {:ok, stringify(json)}
    else
      {:error, reason} ->
        {:error, {:invalid_pipeline, "could not read definition: #{inspect(reason)}"}}
    end
  end

  defp strip_json_comments(raw) do
    raw
    |> String.split("\n")
    |> Enum.reject(&String.starts_with?(String.trim_leading(&1), "//"))
    |> Enum.join("\n")
  end

  defp ensure_available(id) do
    if File.exists?(definition_path(id)) do
      {:error, {:conflict, id}}
    else
      :ok
    end
  end

  defp validate_id(id) when is_binary(id) do
    if Regex.match?(@id_pattern, id) do
      :ok
    else
      {:error, {:invalid_pipeline, "id must be a safe filename stem"}}
    end
  end

  defp validate_id(_id), do: {:error, {:invalid_pipeline, "id is required"}}

  defp stringify(%{} = map) do
    Map.new(map, fn {key, value} -> {to_string(key), stringify_value(value)} end)
  end

  defp stringify_value(%{} = map), do: stringify(map)
  defp stringify_value(list) when is_list(list), do: Enum.map(list, &stringify_value/1)
  defp stringify_value(value), do: value

  defp drop_nil_values(map) do
    map
    |> Enum.reject(fn {_key, value} -> is_nil(value) end)
    |> Map.new()
  end

  defp map_or(%{} = value, _default), do: value
  defp map_or(_value, default), do: default

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)

  defp string_or_nil(value) when is_binary(value) do
    value = String.trim(value)
    if value == "", do: nil, else: value
  end

  defp string_or_nil(_value), do: nil

  defp string_list_or_nil(values) when is_list(values) do
    values =
      values
      |> Enum.filter(&is_binary/1)
      |> Enum.map(&String.trim/1)
      |> Enum.reject(&(&1 == ""))

    if values == [], do: nil, else: values
  end

  defp string_list_or_nil(_values), do: nil

  defp string_list_allow_empty(values) when is_list(values) do
    values
    |> Enum.filter(&is_binary/1)
    |> Enum.map(&String.trim/1)
    |> Enum.reject(&(&1 == ""))
  end

  defp string_list_allow_empty(_values), do: nil

  defp env_or_nil(%{} = env) do
    env
    |> Enum.reduce(%{}, fn
      {key, value}, acc when is_binary(key) and is_binary(value) ->
        key = String.trim(key)
        if key == "", do: acc, else: Map.put(acc, key, value)

      _entry, acc ->
        acc
    end)
  end

  defp env_or_nil(_env), do: nil

  defp value_or_existing(attrs, existing, key, normalize) do
    if Map.has_key?(attrs, key) do
      normalize.(attrs[key])
    else
      existing[key]
    end
  end

  defp workflow_from_attrs(attrs) do
    attrs
    |> Map.get("workflow")
    |> map_or(%{})
    |> stringify()
    |> workflow_from_map()
  end

  defp workflow_from_map(%{} = workflow) do
    workflow =
      %{}
      |> maybe_put("goal", string_or_nil(workflow["goal"]))
      |> maybe_put("stages", string_list_or_nil(workflow["stages"]))
      |> maybe_put("constraints", string_list_or_nil(workflow["constraints"]))
      |> maybe_put("allowed_capabilities", string_list_or_nil(workflow["allowed_capabilities"]))
      |> maybe_put("steps", workflow_steps(workflow["steps"]))
      |> maybe_put("success_criteria", string_list_or_nil(workflow["success_criteria"]))
      |> maybe_put("checkpoints", string_list_or_nil(workflow["checkpoints"]))
      |> maybe_put("budget", workflow_budget(workflow["budget"]))

    if workflow == %{}, do: nil, else: workflow
  end

  defp workflow_from_map(_workflow), do: nil

  defp workflow_budget(%{} = budget) do
    budget =
      %{}
      |> maybe_put("tokens", positive_int_or_nil(budget["tokens"]))
      |> maybe_put("usd", positive_number_or_nil(budget["usd"]))
      |> maybe_put("wall_ms", positive_int_or_nil(budget["wall_ms"]))

    if budget == %{}, do: nil, else: budget
  end

  defp workflow_budget(_budget), do: nil

  defp workflow_steps(steps) when is_list(steps) do
    steps =
      steps
      |> Enum.with_index(1)
      |> Enum.flat_map(fn
        {%{} = step, index} ->
          step = stringify(step)
          capability_id = string_or_nil(step["capability_id"] || step["capability"])

          if capability_id do
            id = string_or_nil(step["id"]) || "step-#{index}"
            name = string_or_nil(step["name"]) || string_or_nil(step["stage"]) || capability_id

            [
              %{
                "id" => id,
                "name" => name,
                "capability_id" => capability_id
              }
              |> maybe_put("notes", string_or_nil(step["notes"]))
            ]
          else
            []
          end

        _other ->
          []
      end)

    if steps == [], do: nil, else: steps
  end

  defp workflow_steps(_steps), do: nil

  defp positive_number_or_nil(value) when is_integer(value) and value > 0, do: value
  defp positive_number_or_nil(value) when is_float(value) and value > 0, do: value

  defp positive_number_or_nil(value) when is_binary(value) do
    value = String.trim(value)

    with {number, ""} <- Float.parse(value),
         true <- number > 0 do
      number
    else
      _ -> nil
    end
  end

  defp positive_number_or_nil(_value), do: nil

  defp template_from_workflow(nil), do: nil

  defp template_from_workflow(%{} = workflow) do
    [
      "# Goal",
      workflow["goal"],
      "",
      "# Stages",
      markdown_list(workflow["stages"]),
      "",
      "# Allowed capabilities",
      markdown_list(workflow["allowed_capabilities"]),
      "",
      "# Steps",
      markdown_steps(workflow["steps"]),
      "",
      "# Constraints",
      markdown_list(workflow["constraints"]),
      "",
      "# Success criteria",
      markdown_list(workflow["success_criteria"]),
      "",
      "# Checkpoints",
      markdown_list(workflow["checkpoints"]),
      "",
      "# Buyer request",
      "{{objective}}"
    ]
    |> Enum.reject(&is_nil/1)
    |> Enum.join("\n")
    |> String.trim()
  end

  defp markdown_list(nil), do: nil
  defp markdown_list([]), do: nil
  defp markdown_list(items), do: items |> Enum.map(&"- #{&1}") |> Enum.join("\n")

  defp markdown_steps(nil), do: nil
  defp markdown_steps([]), do: nil

  defp markdown_steps(steps) do
    steps
    |> Enum.map(fn step ->
      note =
        case step["notes"] do
          notes when is_binary(notes) and notes != "" -> " — #{notes}"
          _other -> ""
        end

      "- #{step["name"]} [#{step["capability_id"]}]#{note}"
    end)
    |> Enum.join("\n")
  end

  defp positive_int_or_nil(value) when is_integer(value) and value > 0, do: value

  defp positive_int_or_nil(value) when is_binary(value) do
    case Integer.parse(String.trim(value)) do
      {int, ""} when int > 0 -> int
      _ -> nil
    end
  end

  defp positive_int_or_nil(_value), do: nil

  defp approvals_or_nil(value) when value in ["deny", "ignore"], do: value
  defp approvals_or_nil(_value), do: nil
end
