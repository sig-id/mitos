defmodule Mitos.KeyVault do
  @moduledoc """
  Minimal control-plane key vault used by the broker policy path.

  Secrets are configured on the BEAM side and never returned to callers. The
  broker receives only a redacted credential reference after destination scoping
  has been checked.
  """

  @credential_id_pattern ~r/^[a-zA-Z0-9][a-zA-Z0-9_.:-]*$/
  @secret_token_value_pattern ~r/^(sk-|sk_live_|sk_test_|rk_live_|ghp_|gho_|ghu_|ghs_|ghr_|github_pat_|xoxb-|xoxp-|xoxa-|xoxc-|xapp-|glpat-|ya29\.|AKIA)/i
  @secret_url_query_key_pattern ~r/(secret|token|api[_-]?key|password|credential|private[_-]?key|authorization|bearer|signature|sig)/i

  @doc """
  Register a seller-owned credential in the broker vault.

  The stored credential retains secret material control-plane side, but the return
  value is always a redacted reference suitable for HTTP responses and logs.
  """
  def register(attrs) when is_map(attrs) do
    attrs = normalize(attrs)

    with {:ok, seller_id} <- required_string(attrs, "seller_id"),
         {:ok, credential_id} <- credential_id(attrs),
         {:ok, destinations} <- destinations(attrs),
         {:ok, secret} <- required_string(attrs, "secret") do
      credential =
        attrs
        |> Map.take(["type", "secret", "header", "username", "certificate", "metadata"])
        |> Map.put_new("type", "opaque")
        |> Map.put("secret", secret)
        |> Map.put("destinations", destinations)

      vault = put_credential(vault(), seller_id, credential_id, credential)

      Application.put_env(:mitos, :key_vault, vault)
      {:ok, credential_ref(seller_id, credential_id, credential, destinations)}
    end
  end

  def register(_attrs), do: {:error, "key registration body must be an object"}

  @doc "List redacted credential references for a seller."
  def list(seller_id) when is_binary(seller_id) and seller_id != "" do
    vault()
    |> Map.get(seller_id, %{})
    |> Map.get("credentials", %{})
    |> credential_map()
    |> Enum.map(fn {credential_id, credential} ->
      destinations =
        cond do
          is_list(field(credential, "destinations")) -> field(credential, "destinations")
          is_binary(field(credential, "dest")) -> [field(credential, "dest")]
          is_binary(field(credential, "destination")) -> [field(credential, "destination")]
          true -> []
        end

      credential_ref(seller_id, credential_id, credential, destinations)
    end)
    |> Enum.sort_by(& &1["credential_id"])
  end

  def list(_seller_id), do: []

  @doc """
  Grant run-scoped credential leases declared by a launch spec.

  Invalid or unavailable credentials are not leased; the broker will still deny
  those requests at resolution time with the concrete credential error.
  """
  def lease_run(run_id, launch_spec) when is_binary(run_id) and is_map(launch_spec) do
    launch_spec = normalize(launch_spec)
    seller_id = seller_id(launch_spec)

    refs =
      launch_spec
      |> egress_policy()
      |> Enum.flat_map(&lease_ref(&1, seller_id))
      |> Enum.uniq_by(fn ref -> {ref["seller_id"], ref["credential_id"], ref["dest"]} end)
      |> Enum.sort_by(fn ref -> {ref["credential_id"], ref["dest"]} end)

    leases =
      leases()
      |> Map.put(run_id, refs)

    Application.put_env(:mitos, :key_vault_leases, leases)
    {:ok, refs}
  end

  def lease_run(_run_id, _launch_spec), do: {:ok, []}

  @doc """
  Grant run-scoped leases and fail if any declared credentialed egress rule
  cannot be bound.

  This is the launch-admission path for capability runs: unlike `lease_run/2`,
  it does not silently drop unavailable credentials.
  """
  def lease_run_strict(run_id, launch_spec) when is_binary(run_id) and is_map(launch_spec) do
    launch_spec = normalize(launch_spec)
    seller_id = seller_id(launch_spec)
    failures = lease_failures(egress_policy(launch_spec), seller_id)

    if failures == [] do
      lease_run(run_id, launch_spec)
    else
      {:error, {:credential_lease_failed, failures}}
    end
  end

  def lease_run_strict(_run_id, _launch_spec), do: {:error, {:credential_lease_failed, []}}

  @doc "Revoke every credential lease for a run."
  def revoke_run(run_id) when is_binary(run_id) do
    Application.put_env(:mitos, :key_vault_leases, Map.delete(leases(), run_id))
    :ok
  end

  def revoke_run(_run_id), do: :ok

  @doc "Return redacted active credential leases for a run."
  def leases(run_id) when is_binary(run_id), do: Map.get(leases(), run_id, [])
  def leases(_run_id), do: []

  @doc "Resolve a credential id for a destination without exposing secret material."
  def resolve(credential_id, dest, opts \\ [])
  def resolve(nil, _dest, _opts), do: {:ok, nil}
  def resolve("", _dest, _opts), do: {:ok, nil}

  def resolve(credential_id, dest, opts) when is_binary(credential_id) and is_binary(dest) do
    seller_id = seller_id(opts)

    case fetch_credential(vault(), credential_id, seller_id) do
      :not_found ->
        {:error, "credential unavailable"}

      {:error, reason} ->
        {:error, reason}

      {:ok, %{} = credential} ->
        if destination_allowed?(credential, dest) do
          credential_ref =
            %{
              "credential_id" => credential_id,
              "credential_type" => field(credential, "type", "opaque"),
              "dest" => dest
            }
            |> put_optional("seller_id", field(credential, "seller_id"))

          if lease_active?(opts, credential_ref) do
            {:ok, credential_ref}
          else
            {:error, "credential lease unavailable"}
          end
        else
          {:error, "credential destination mismatch"}
        end
    end
  end

  def resolve(_credential_id, _dest, _opts), do: {:error, "credential unavailable"}

  @doc """
  Resolve a credential for one run using only the active run lease.

  The broker-facing path should prefer this over caller-supplied seller ids:
  the run lease was created from the launch spec and carries the scoped seller
  identity needed to resolve the credential without exposing secret material.
  """
  def resolve_run(run_id, credential_id, dest)
      when is_binary(run_id) and is_binary(credential_id) and is_binary(dest) do
    case Enum.find(leases(run_id), &lease_matches?(&1, credential_id, dest)) do
      %{} = lease ->
        with {:ok, %{} = credential_ref} <-
               resolve(credential_id, dest, seller_id: lease["seller_id"], run_id: run_id) do
          {:ok, apply_lease_policy(credential_ref, lease)}
        end

      nil ->
        {:error, "credential lease unavailable"}
    end
  end

  def resolve_run(_run_id, _credential_id, _dest), do: {:error, "credential lease unavailable"}

  @doc false
  def resolve_run_attachment(run_id, credential_id, dest)
      when is_binary(run_id) and is_binary(credential_id) and is_binary(dest) do
    case Enum.find(leases(run_id), &lease_matches?(&1, credential_id, dest)) do
      %{"requires_approval" => true} ->
        {:error, "approval required"}

      %{} = lease ->
        seller_id = lease["seller_id"]

        with {:ok, %{} = credential} <- fetch_credential(vault(), credential_id, seller_id),
             true <- destination_allowed?(credential, dest),
             {:ok, header_name, header_value} <- credential_header(credential) do
          credential_ref =
            %{
              "credential_id" => credential_id,
              "credential_type" => field(credential, "type", "opaque"),
              "dest" => dest
            }
            |> put_optional("seller_id", field(credential, "seller_id"))

          {:ok,
           %{
             "credential" => credential_ref,
             "headers" => [{header_name, header_value}],
             "redaction_values" => credential_redaction_values(credential, header_value)
           }}
        else
          false -> {:error, "credential destination mismatch"}
          :not_found -> {:error, "credential unavailable"}
          {:error, reason} -> {:error, reason}
        end

      nil ->
        {:error, "credential lease unavailable"}
    end
  end

  def resolve_run_attachment(_run_id, _credential_id, _dest),
    do: {:error, "credential lease unavailable"}

  defp put_credential(vault, seller_id, credential_id, credential) do
    Map.update(vault, seller_id, %{"credentials" => %{credential_id => credential}}, fn
      %{} = seller_vault ->
        credentials =
          seller_vault
          |> Map.get("credentials", %{})
          |> credential_map()
          |> Map.put(credential_id, credential)

        Map.put(seller_vault, "credentials", credentials)

      _other ->
        %{"credentials" => %{credential_id => credential}}
    end)
  end

  defp credential_ref(seller_id, credential_id, credential, destinations) do
    %{
      "credential_id" => credential_id,
      "credential_type" => field(credential, "type", "opaque"),
      "seller_id" => seller_id,
      "destinations" => destinations
    }
  end

  defp credential_id(attrs) do
    value = field(attrs, "credential_id") || field(attrs, "id") || field(attrs, "name")

    case clean_string(value) do
      nil ->
        {:error, "credential_id is required"}

      credential_id ->
        validate_credential_id(credential_id)
    end
  end

  defp validate_credential_id(credential_id) do
    cond do
      credential_secret_material?(credential_id) ->
        {:error, "credential_id must reference an id, not secret material"}

      not Regex.match?(@credential_id_pattern, credential_id) ->
        {:error, "credential_id contains unsupported characters"}

      true ->
        {:ok, credential_id}
    end
  end

  defp destinations(attrs) do
    values =
      cond do
        is_list(field(attrs, "destinations")) -> field(attrs, "destinations")
        is_binary(field(attrs, "dest")) -> [field(attrs, "dest")]
        is_binary(field(attrs, "destination")) -> [field(attrs, "destination")]
        true -> []
      end

    destinations =
      values
      |> Enum.map(&clean_string/1)
      |> Enum.reject(&is_nil/1)
      |> Enum.uniq()

    cond do
      destinations == [] ->
        {:error, "at least one destination is required"}

      "*" in destinations ->
        {:error, "registered seller keys must be scoped to explicit destinations"}

      Enum.any?(destinations, &credential_secret_material?/1) ->
        {:error, "registered seller key destination must not contain secret material"}

      true ->
        {:ok, destinations}
    end
  end

  defp credential_secret_material?(value) do
    credential_url?(value) or
      credential_url_query?(value) or
      Regex.match?(@secret_token_value_pattern, value)
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

  defp required_string(attrs, key) do
    case clean_string(field(attrs, key)) do
      nil -> {:error, "#{key} is required"}
      value -> {:ok, value}
    end
  end

  defp vault do
    :mitos
    |> Application.get_env(:key_vault, %{})
    |> normalize_vault()
  end

  defp leases do
    :mitos
    |> Application.get_env(:key_vault_leases, %{})
    |> normalize_vault()
  end

  defp normalize_vault(%{} = vault), do: normalize(vault)

  defp normalize_vault(_vault), do: %{}

  defp fetch_credential(vault, credential_id, seller_id) do
    case scoped_credential(vault, credential_id, seller_id) do
      {:ok, credential} ->
        {:ok, credential}

      :not_found ->
        flat_credential(vault, credential_id, seller_id)
    end
  end

  defp scoped_credential(_vault, _credential_id, nil), do: :not_found
  defp scoped_credential(_vault, _credential_id, ""), do: :not_found

  defp scoped_credential(vault, credential_id, seller_id) do
    case Map.get(vault, seller_id) do
      %{} = seller_vault ->
        credential =
          seller_vault
          |> Map.get("credentials", seller_vault)
          |> credential_map()
          |> Map.get(credential_id)

        case credential do
          %{} = credential -> {:ok, Map.put_new(credential, "seller_id", seller_id)}
          _ -> :not_found
        end

      _ ->
        :not_found
    end
  end

  defp flat_credential(vault, credential_id, seller_id) do
    case Map.get(vault, credential_id) do
      %{} = credential -> seller_scoped_credential(credential, seller_id)
      _ -> :not_found
    end
  end

  defp seller_scoped_credential(credential, seller_id) do
    credential_seller = field(credential, "seller_id") || field(credential, "seller")

    cond do
      credential_seller in [nil, ""] ->
        {:ok, credential}

      seller_id == credential_seller ->
        {:ok, Map.put(credential, "seller_id", credential_seller)}

      true ->
        {:error, "credential seller mismatch"}
    end
  end

  defp credential_map(%{} = map), do: map
  defp credential_map(_map), do: %{}

  defp lease_ref(rule, seller_id) when is_map(rule) do
    credential_id = field(rule, "credential_id") || field(rule, "credential")
    dest = field(rule, "dest") || field(rule, "destination")

    with credential_id when is_binary(credential_id) and credential_id != "" <- credential_id,
         dest when is_binary(dest) and dest != "" and dest != "*" <- dest,
         {:ok, %{} = ref} <- resolve(credential_id, dest, seller_id: seller_id) do
      lease =
        ref
        |> Map.take(["credential_id", "credential_type", "dest", "seller_id"])
        |> apply_rule_policy(rule)

      [lease]
    else
      _ -> []
    end
  end

  defp lease_ref(_rule, _seller_id), do: []

  defp lease_failures(policy, seller_id) when is_list(policy) do
    policy
    |> Enum.filter(&is_map/1)
    |> Enum.flat_map(&lease_failure(&1, seller_id))
  end

  defp lease_failures(_policy, _seller_id), do: []

  defp lease_failure(rule, seller_id) do
    credential_id = field(rule, "credential_id") || field(rule, "credential")
    dest = field(rule, "dest") || field(rule, "destination")

    cond do
      credential_id in [nil, ""] ->
        []

      seller_id in [nil, ""] ->
        [
          lease_failure_ref(
            rule,
            dest,
            credential_id,
            "seller_id is required for credential lease"
          )
        ]

      not is_binary(dest) or dest == "" or dest == "*" ->
        [lease_failure_ref(rule, dest, credential_id, "explicit destination is required")]

      true ->
        case resolve(credential_id, dest, seller_id: seller_id) do
          {:ok, %{} = _ref} -> []
          {:ok, nil} -> []
          {:error, reason} -> [lease_failure_ref(rule, dest, credential_id, reason)]
        end
    end
  end

  defp lease_failure_ref(rule, dest, credential_id, reason) do
    %{
      "rule" => field(rule, "id"),
      "dest" => dest,
      "credential_id" => credential_id,
      "reason" => reason
    }
    |> Enum.reject(fn {_key, value} -> value in [nil, ""] end)
    |> Map.new()
  end

  defp lease_active?(opts, credential_ref) do
    case run_id(opts) do
      nil ->
        true

      "" ->
        true

      run_id ->
        Enum.any?(leases(run_id), &same_lease?(&1, credential_ref))
    end
  end

  defp same_lease?(lease, credential_ref) do
    lease["credential_id"] == credential_ref["credential_id"] and
      lease["dest"] == credential_ref["dest"] and
      Map.get(lease, "seller_id") == Map.get(credential_ref, "seller_id")
  end

  defp apply_rule_policy(ref, rule) do
    if truthy?(field(rule, "requires_approval", false)) do
      Map.put(ref, "requires_approval", true)
    else
      ref
    end
  end

  defp apply_lease_policy(credential_ref, %{"requires_approval" => true}) do
    Map.put(credential_ref, "requires_approval", true)
  end

  defp apply_lease_policy(credential_ref, _lease), do: credential_ref

  defp lease_matches?(lease, credential_id, dest) do
    lease["credential_id"] == credential_id and lease["dest"] == dest
  end

  defp credential_header(%{} = credential) do
    type = field(credential, "type", "opaque")
    secret = field(credential, "secret")

    cond do
      not is_binary(secret) or secret == "" ->
        {:error, "credential secret unavailable"}

      type == "bearer" ->
        {:ok, "authorization", "Bearer " <> secret}

      type == "header" ->
        case clean_header_name(field(credential, "header")) do
          nil -> {:error, "credential header is required"}
          header -> {:ok, header, secret}
        end

      type == "basic" ->
        case clean_string(field(credential, "username")) do
          nil ->
            {:error, "credential username is required"}

          username ->
            token = Base.encode64(username <> ":" <> secret)
            {:ok, "authorization", "Basic " <> token}
        end

      type == "opaque" and is_binary(field(credential, "header")) ->
        case clean_header_name(field(credential, "header")) do
          nil -> {:error, "credential header is required"}
          header -> {:ok, header, secret}
        end

      true ->
        {:error, "credential type #{type} cannot be attached by the HTTP broker"}
    end
  end

  defp credential_redaction_values(%{} = credential, header_value) do
    [field(credential, "secret"), header_value]
    |> Enum.filter(&(is_binary(&1) and &1 != ""))
    |> Enum.uniq()
  end

  defp destination_allowed?(credential, dest) do
    allowed =
      cond do
        is_binary(field(credential, "dest")) ->
          [field(credential, "dest")]

        is_binary(field(credential, "destination")) ->
          [field(credential, "destination")]

        is_list(field(credential, "destinations")) ->
          Enum.filter(field(credential, "destinations"), &is_binary/1)

        true ->
          []
      end

    "*" in allowed or dest in allowed
  end

  defp field(%{} = map, key, default \\ nil), do: Map.get(map, key, default)

  defp truthy?(true), do: true
  defp truthy?(1), do: true
  defp truthy?("true"), do: true
  defp truthy?("1"), do: true
  defp truthy?(_value), do: false

  defp seller_id(opts) when is_binary(opts), do: opts

  defp seller_id(%{} = opts) do
    field(opts, "seller_id") || field(opts, "seller") || field(opts, "seller_tenant_id")
  end

  defp seller_id(opts) when is_list(opts) do
    Keyword.get(opts, :seller_id) || Keyword.get(opts, :seller) ||
      Keyword.get(opts, :seller_tenant_id)
  end

  defp seller_id(_opts), do: nil

  defp run_id(%{} = opts), do: field(opts, "run_id") || field(opts, "run")
  defp run_id(opts) when is_list(opts), do: Keyword.get(opts, :run_id) || Keyword.get(opts, :run)
  defp run_id(_opts), do: nil

  defp egress_policy(%{} = launch_spec) do
    launch_spec
    |> field("egress_policy", :unset)
    |> case do
      :unset ->
        launch_spec
        |> field("policy", %{})
        |> nested_egress_policy()

      policy ->
        policy
    end
    |> case do
      policy when is_list(policy) -> Enum.filter(policy, &is_map/1)
      _other -> []
    end
  end

  defp nested_egress_policy(%{} = policy), do: field(policy, "egress_policy", [])
  defp nested_egress_policy(_policy), do: []

  defp clean_string(value) when is_binary(value) do
    case String.trim(value) do
      "" -> nil
      trimmed -> trimmed
    end
  end

  defp clean_string(_value), do: nil

  defp clean_header_name(value) do
    value
    |> clean_string()
    |> case do
      nil ->
        nil

      header ->
        if String.match?(header, ~r/^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/) do
          String.downcase(header)
        end
    end
  end

  defp normalize(%{} = map),
    do: Map.new(map, fn {key, value} -> {to_string(key), normalize(value)} end)

  defp normalize(list) when is_list(list), do: Enum.map(list, &normalize/1)
  defp normalize(value), do: value

  defp put_optional(map, _key, nil), do: map
  defp put_optional(map, _key, ""), do: map
  defp put_optional(map, key, value), do: Map.put(map, key, value)
end
