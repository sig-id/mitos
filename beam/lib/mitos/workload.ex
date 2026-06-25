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
  @secret_url_query_key_pattern ~r/(secret|token|api[_-]?key|password|credential|private[_-]?key|authorization|bearer|signature|sig)/i
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
    with {:ok, root} <- trusted_workloads_dir(),
         {:ok, entries} <- File.ls(root) do
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
    else
      _error -> []
    end
  end

  @doc "Load one workload by id. The id must be a plain filename stem."
  def fetch(id) when is_binary(id) do
    cond do
      not Regex.match?(@id_pattern, id) ->
        {:error, :not_found}

      true ->
        with {:ok, root} <- trusted_workloads_dir(),
             path <- Path.join(root, id <> ".json"),
             {:ok, raw} <- read_file(path),
             {:ok, json} <- decode_json(json_source(raw)),
             {:ok, workload} <- from_json(id, json, package_root: Path.dirname(path)) do
          {:ok, workload}
        else
          {:error, :workloads_dir_unavailable} -> {:error, :not_found}
          {:error, _reason} = error -> error
        end
    end
  end

  def fetch(_id), do: {:error, :not_found}

  @doc "Build a workload struct from decoded JSON, applying defaults."
  def from_json(id, json, opts \\ [])

  def from_json(id, %{} = json, opts) do
    provider = json["provider"]
    template = json["template"]
    defaults = %__MODULE__{id: id, provider: provider, template: template}

    with :ok <- require_string(provider, "provider"),
         :ok <- require_string(template, "template"),
         {:ok, env} <- env_from_json(json["env"], defaults.env),
         {:ok, env_passthrough} <-
           env_passthrough_from_json(json["env_passthrough"], defaults.env_passthrough),
         {:ok, pricing} <- pricing_from_json(json["pricing"] || json["price"]),
         {:ok, payout_address} <- payout_address_from_json(json, pricing),
         {:ok, capability} <- capability_from_json(id, json, pricing, opts),
         :ok <- capability_accepts_provider(capability, provider),
         :ok <- capability_env_policy(capability, env, env_passthrough) do
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
           env: env,
           env_passthrough: env_passthrough,
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
        |> then(
          &Mitos.Capability.from_json(id, &1, package_root: Keyword.get(opts, :package_root))
        )

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

  defp capability_env_policy(nil, _env, _passthrough), do: :ok

  defp capability_env_policy(%Mitos.Capability{}, %{} = env, passthrough) do
    cond do
      passthrough != [] ->
        {:error, "capability workloads must not use env_passthrough"}

      secret_key = secret_env_key(env) ->
        {:error, "capability env must not contain secret-like key #{secret_key}"}

      secret_value_key = secret_env_value_key(env) ->
        {:error,
         "capability env must not contain credential-bearing URL value for #{secret_value_key}"}

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

  defp secret_env_value_key(%{} = env) do
    env
    |> Enum.find_value(fn
      {key, value} when is_binary(value) ->
        if credential_url?(value) or credential_url_query?(value), do: to_string(key)

      _entry ->
        nil
    end)
  end

  defp credential_url?(value) do
    value
    |> URI.parse()
    |> case do
      %URI{scheme: scheme, host: host, userinfo: userinfo}
      when is_binary(scheme) and is_binary(host) and is_binary(userinfo) ->
        userinfo != ""

      _uri ->
        false
    end
  end

  defp credential_url_query?(value) do
    value
    |> URI.parse()
    |> case do
      %URI{scheme: scheme, host: host, query: query}
      when is_binary(scheme) and is_binary(host) and is_binary(query) ->
        query
        |> String.split("&", trim: true)
        |> Enum.any?(&credential_url_query_pair?/1)

      _uri ->
        false
    end
  end

  defp credential_url_query_pair?(pair) do
    [key | _rest] = String.split(pair, "=", parts: 2)
    Regex.match?(@secret_url_query_key_pattern, safe_form_decode(key))
  end

  defp safe_form_decode(value) do
    URI.decode_www_form(value)
  rescue
    ArgumentError -> value
  end

  defp read_file(path) do
    case read_regular_file(path) do
      {:ok, raw} ->
        {:ok, raw}

      {:error, :enoent} ->
        {:error, :not_found}

      {:error, reason} ->
        {:error, {:invalid_workload, "unreadable definition: #{inspect(reason)}"}}
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

  defp trusted_workloads_dir do
    path = Path.expand(dir())

    with true <- regular_dir?(path),
         true <- no_symlink_absolute_path?(path) do
      {:ok, path}
    else
      _ -> {:error, :workloads_dir_unavailable}
    end
  end

  defp regular_dir?(path), do: match?({:ok, %{type: :directory}}, File.lstat(path))

  defp no_symlink_absolute_path?(path) do
    root = Path.expand("/")
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
          {:error, _reason} -> {:halt, false}
        end
      end)
      |> case do
        false -> false
        _last_path -> true
      end
    else
      _ -> false
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

  defp env_from_json(nil, default), do: {:ok, default}

  defp env_from_json(%{} = env, _default) do
    Enum.reduce_while(env, {:ok, %{}}, fn {key, value}, {:ok, acc} ->
      with {:ok, key} <- validate_env_name(key, "env name"),
           {:ok, value} <- validate_env_value(key, value) do
        {:cont, {:ok, Map.put(acc, key, value)}}
      else
        {:error, reason} -> {:halt, {:error, reason}}
      end
    end)
  end

  defp env_from_json(_env, _default), do: {:error, "env must be an object"}

  defp env_passthrough_from_json(nil, default), do: {:ok, default}

  defp env_passthrough_from_json(values, _default) when is_list(values) do
    Enum.reduce_while(values, {:ok, []}, fn value, {:ok, acc} ->
      case validate_env_name(value, "env_passthrough entry") do
        {:ok, name} -> {:cont, {:ok, [name | acc]}}
        {:error, reason} -> {:halt, {:error, reason}}
      end
    end)
    |> case do
      {:ok, values} -> {:ok, Enum.reverse(values)}
      {:error, reason} -> {:error, reason}
    end
  end

  defp env_passthrough_from_json(_values, _default),
    do: {:error, "env_passthrough must be a list"}

  defp validate_env_name(name, field) when is_binary(name) do
    cond do
      name == "" -> {:error, "#{field} must not be empty"}
      String.contains?(name, <<0>>) -> {:error, "#{field} must not contain NUL"}
      String.contains?(name, "=") -> {:error, "#{field} must not contain ="}
      true -> {:ok, name}
    end
  end

  defp validate_env_name(_name, field), do: {:error, "#{field} must be a string"}

  defp validate_env_value(_key, value) when not is_binary(value),
    do: {:error, "env values must be strings"}

  defp validate_env_value(key, value) do
    if String.contains?(value, <<0>>) do
      {:error, "env value for #{inspect(key)} must not contain NUL"}
    else
      {:ok, value}
    end
  end

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
