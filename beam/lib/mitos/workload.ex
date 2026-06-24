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
  @secret_env_fragments ~w(
    API_KEY
    AUTHORIZATION
    BEARER
    CLIENT_SECRET
    CREDENTIAL
    PASSWORD
    PASSWD
    PRIVATE_KEY
    REFRESH_TOKEN
    SECRET
    TOKEN
  )
  defstruct [
    :id,
    :provider,
    :template,
    :cwd,
    :title,
    :description,
    :package_root,
    :capability,
    :pricing,
    :seller_id,
    :payout_address,
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
             {:ok, workload} <- from_json(id, json, package_root: Path.dirname(path)) do
          {:ok, workload}
        end
    end
  end

  def fetch(_id), do: {:error, :not_found}

  @doc "Build a workload struct from decoded JSON, applying defaults."
  def from_json(id, json, opts \\ [])

  def from_json(id, %{} = json, opts) do
    provider = json["provider"]
    template = json["template"]

    with :ok <- require_string(provider, "provider"),
         :ok <- require_string(template, "template"),
         {:ok, pricing} <- pricing_from_json(json["pricing"] || json["price"]),
         {:ok, payout_address} <- payout_address_from_json(json, pricing),
         {:ok, capability} <- capability_from_json(id, json, pricing, opts),
         :ok <- capability_accepts_provider(capability, provider),
         :ok <- capability_env_policy(capability, json) do
      defaults = %__MODULE__{id: id, provider: provider, template: template}

      {:ok,
       %__MODULE__{
         defaults
         | cwd: string_or(json["cwd"], defaults.cwd),
           title: string_or(json["title"], defaults.title),
           description: string_or(json["description"], defaults.description),
           package_root: Keyword.get(opts, :package_root),
           capability: capability,
           pricing: pricing,
           seller_id: seller_id(json),
           payout_address: payout_address,
           transport_order: string_list_or(json["transport_order"], defaults.transport_order),
           env: map_or(json["env"], defaults.env),
           env_passthrough: string_list_or(json["env_passthrough"], defaults.env_passthrough),
           timeout_ms: positive_int_or(json["timeout_ms"], defaults.timeout_ms),
           idle_ms: positive_int_or(json["idle_ms"], defaults.idle_ms),
           approvals: approvals_or(json["approvals"], defaults.approvals)
       }}
    else
      {:error, {:invalid_capability, detail}} ->
        {:error, {:invalid_workload, "invalid capability manifest: #{detail}"}}

      {:error, {:invalid_pricing, detail}} ->
        {:error, {:invalid_workload, "invalid pricing: #{detail}"}}

      {:error, reason} ->
        {:error, {:invalid_workload, reason}}
    end
  end

  def from_json(_id, _json, _opts),
    do: {:error, {:invalid_workload, "definition must be a JSON object"}}

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
    view = %{
      "id" => workload.id,
      "title" => workload.title,
      "description" => workload.description,
      "placeholders" =>
        @placeholder
        |> Regex.scan(workload.template, capture: :all_but_first)
        |> List.flatten()
        |> Enum.uniq(),
      "timeout_ms" => workload.timeout_ms
    }

    if workload.capability do
      Map.put(view, "capability", Mitos.Capability.public_view(workload.capability))
    else
      view
    end
    |> maybe_put("pricing", public_pricing(workload.pricing))
  end

  defp require_string(value, _field) when is_binary(value) and value != "", do: :ok
  defp require_string(_value, field), do: {:error, "#{field} is required"}

  defp capability_from_json(id, json, pricing, opts) do
    case json["capability_manifest"] || json["capability"] do
      nil when is_nil(pricing) ->
        {:ok, nil}

      nil ->
        {:error, {:invalid_capability, "paid publication requires a capability manifest"}}

      %{} = manifest ->
        id
        |> published_manifest(manifest, pricing)
        |> then(&Mitos.Capability.from_json(id, &1, package_root: Keyword.get(opts, :package_root)))

      _ ->
        {:error, {:invalid_capability, "capability must be an object"}}
    end
  end

  defp published_manifest(_id, %{} = manifest, nil), do: manifest

  defp published_manifest(_id, %{} = manifest, _pricing) do
    Map.put(manifest, "trust_tier", "T2")
  end

  defp capability_accepts_provider(nil, _provider), do: :ok

  defp capability_accepts_provider(%Mitos.Capability{} = capability, provider) do
    if Mitos.Capability.compatible_harness?(capability, provider) do
      :ok
    else
      {:error, "provider #{inspect(provider)} is not declared in capability.harnesses"}
    end
  end

  defp capability_env_policy(nil, _json), do: :ok

  defp capability_env_policy(%Mitos.Capability{}, %{} = json) do
    env = map_or(json["env"], %{})
    passthrough = string_list_or(json["env_passthrough"], [])

    cond do
      passthrough != [] ->
        {:error, "capability workloads must not use env_passthrough"}

      secret_key = secret_env_key(env) ->
        {:error, "capability env must not contain secret-like key #{secret_key}"}

      true ->
        :ok
    end
  end

  defp secret_env_key(%{} = env) do
    env
    |> Map.keys()
    |> Enum.find(&secret_env_key?/1)
  end

  defp secret_env_key?(key) do
    key = key |> to_string() |> String.upcase()
    Enum.any?(@secret_env_fragments, &String.contains?(key, &1))
  end

  defp read_file(path) do
    case File.read(path) do
      {:ok, raw} ->
        {:ok, raw}

      {:error, :enoent} ->
        {:error, :not_found}

      {:error, reason} ->
        {:error, {:invalid_workload, "unreadable definition: #{inspect(reason)}"}}
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

  defp pricing_from_json(nil), do: {:ok, nil}

  defp pricing_from_json(%{} = pricing) do
    type = pricing["type"] || "flat"
    take_rate_bps = positive_int_or(pricing["take_rate_bps"], 1500)
    take_floor = pricing["take_floor_usdc"]
    compute_surcharge = pricing["compute_surcharge_usdc"] || "0"

    with {:ok, pricing_fields} <- pricing_fields(type, pricing),
         {:ok, take_rate_bps} <- take_rate_bps_from_json(take_rate_bps),
         {:ok, take_floor} <- required_pricing_field(take_floor, "take_floor_usdc is required"),
         {:ok, _floor} <- positive_amount_micros(take_floor, "take_floor_usdc must be positive"),
         {:ok, _compute_surcharge} <- Mitos.Settlement.amount_micros(compute_surcharge) do
      {:ok,
       Map.merge(pricing_fields, %{
         "currency" => "USDC",
         "take_rate_bps" => take_rate_bps,
         "take_floor_usdc" => to_string(take_floor),
         "compute_surcharge_usdc" => to_string(compute_surcharge)
       })}
    else
      {:error, reason} -> {:error, {:invalid_pricing, to_string(reason)}}
    end
  end

  defp pricing_from_json(_pricing), do: {:error, {:invalid_pricing, "pricing must be an object"}}

  defp required_pricing_field(value, _message) when is_binary(value) and value != "",
    do: {:ok, value}

  defp required_pricing_field(value, _message) when is_integer(value) or is_float(value),
    do: {:ok, value}

  defp required_pricing_field(_value, message), do: {:error, message}

  defp take_rate_bps_from_json(take_rate_bps) when take_rate_bps in 1000..1500,
    do: {:ok, take_rate_bps}

  defp take_rate_bps_from_json(_take_rate_bps),
    do: {:error, "take_rate_bps must be between 1000 and 1500"}

  defp positive_amount_micros(value, message) do
    case Mitos.Settlement.amount_micros(value) do
      {:ok, micros} when micros > 0 -> {:ok, micros}
      {:ok, _zero} -> {:error, message}
      {:error, reason} -> {:error, reason}
    end
  end

  defp pricing_fields("flat", pricing) do
    amount = pricing["amount_usdc"] || pricing["amount"]

    with {:ok, _amount} <- positive_amount_micros(amount, "amount_usdc must be positive") do
      {:ok, %{"type" => "flat", "amount_usdc" => to_string(amount)}}
    end
  end

  defp pricing_fields("metered", pricing) do
    amount = pricing["amount_usdc"] || pricing["ceiling_usdc"] || pricing["amount"]
    base = pricing["base_usdc"] || "0"

    unit_prices =
      pricing["unit_prices_usdc"]
      |> Kernel.||(pricing["unit_prices"])
      |> map_or(%{})
      |> stringify_values()

    with {:ok, _amount} <- positive_amount_micros(amount, "amount_usdc must be positive"),
         {:ok, _base} <- Mitos.Settlement.amount_micros(base),
         :ok <- validate_unit_prices(unit_prices) do
      {:ok,
       %{
         "type" => "metered",
         "amount_usdc" => to_string(amount),
         "base_usdc" => to_string(base),
         "unit_prices_usdc" => stringify_values(unit_prices)
       }}
    end
  end

  defp pricing_fields(_type, _pricing), do: {:error, "pricing type must be flat or metered"}

  defp validate_unit_prices(unit_prices) do
    valid_units = ~w(token egress tool artifact ms)

    cond do
      unit_prices == %{} ->
        {:error, "metered pricing requires unit_prices_usdc"}

      Enum.any?(Map.keys(unit_prices), &(&1 not in valid_units)) ->
        {:error, "metered pricing unit must be one of token, egress, tool, artifact, ms"}

      true ->
        unit_prices
        |> Map.values()
        |> Enum.reduce_while(:ok, fn value, :ok ->
          case Mitos.Settlement.amount_micros(value) do
            {:ok, _micros} -> {:cont, :ok}
            {:error, reason} -> {:halt, {:error, reason}}
          end
        end)
    end
  end

  defp stringify_values(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), to_string(value)} end)

  defp payout_address_from_json(_json, nil), do: {:ok, nil}

  defp payout_address_from_json(json, %{} = _pricing) do
    case payout_address(json) do
      nil -> {:error, {:invalid_pricing, "paid publication requires payout_address"}}
      payout_address -> {:ok, payout_address}
    end
  end

  defp payout_address(json) do
    pricing = json["pricing"] || json["price"] || %{}
    string_or(json["payout_address"] || pricing["payout_address"], nil)
  end

  defp seller_id(json) do
    pricing = json["pricing"] || json["price"] || %{}

    string_or(
      json["seller_id"] || json["seller"] || pricing["seller_id"] || pricing["seller"],
      nil
    )
  end

  defp public_pricing(nil), do: nil

  defp public_pricing(%{} = pricing) do
    Map.take(pricing, ["type", "amount_usdc", "currency"])
  end

  defp maybe_put(map, _key, nil), do: map
  defp maybe_put(map, key, value), do: Map.put(map, key, value)

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
