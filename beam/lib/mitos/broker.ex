defmodule Mitos.Broker do
  @moduledoc """
  The egress credential broker (01 §8.5, 05 §5.3) — the single outbound authority
  point and the ONLY place real credentials exist at run time.

  Sandbox egress is **deny-all-except-broker**. The broker attaches or withholds
  the real credential per `egress_policy` + destination, gates approvals, and logs
  every crossing as an Effect (`egress_allowed` / `egress_denied`). Secrets live
  only at the broker; the sandbox only ever sees an *uncredentialed* request leave.

  Per 01 §7, the local reach is an in-process channel: gVisor's netstack denies
  all outbound and the capability reaches the broker "over a unix-domain socket."
  For the local provider we realize that channel as a run-scoped in-process call —
  the capability's egress attempts are routed here by the provider, never given a
  TCP path to the host network. A real HTTP forward-proxy for remote providers
  (E2B/Daytona forced proxy, R1) plugs in behind the same `request/2` algorithm.

  Per-request algorithm (05 §5.3):

      on request(dest, method, headers, body):
        rule = match(dest, egress_policy)
        none                     -> egress_denied; 403          # default deny
        {requires_approval:true} -> approval_requested; PARK
        {credential_id:c}        -> cred = vault.get(c); attach; forward; egress_allowed
  """
  use GenServer

  defstruct port: nil, listener: nil, leases: %{}

  # A run-scoped egress lease: the policy + credential scope granted at
  # capability_started and revoked at capability_finished/abort (08 §3.2).
  defmodule Lease do
    @moduledoc false
    @enforce_keys [:token, :run_id, :seq, :scope, :egress_policy]
    defstruct [:token, :run_id, :seq, :scope, :egress_policy]
  end

  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, name: __MODULE__)
  end

  @impl true
  def init(opts) do
    port = Keyword.get(opts, :port)
    # The forward-proxy TCP listener is a TARGET for remote providers; the local
    # in-process channel needs no socket. We open it lazily/optionally.
    {:ok, %__MODULE__{port: port, leases: %{}}}
  end

  @doc """
  Grant a run-scoped egress lease. `scope` keys credential lookup in the vault
  (typically the seller_id / tenant). Returns an opaque token the sandbox channel
  presents on each egress attempt.
  """
  def grant(run_id, seq, scope, egress_policy) do
    GenServer.call(__MODULE__, {:grant, run_id, seq, scope, egress_policy})
  end

  @doc "Revoke a lease at run end (08 §3.2 lease teardown)."
  def revoke(token), do: GenServer.call(__MODULE__, {:revoke, token})

  @doc """
  A single egress attempt from the sandbox. Runs the 05 §5.3 algorithm. Returns:

      {:allowed, response, effect_fields}   # response :: %{status, headers, body}
      {:denied, effect_fields}
      {:parked, effect_fields}              # requires_approval

  `effect_fields` is a keyword list for the caller (the provider/run server) to
  append as an `egress_allowed`/`egress_denied`/`approval_requested` Effect —
  keeping the broker out of the log-writing path (single writer, 04 §11).
  """
  def request(token, %{} = req), do: GenServer.call(__MODULE__, {:request, token, req})

  @impl true
  def handle_call({:grant, run_id, seq, scope, egress_policy}, _from, st) do
    token = Mitos.id("egress")
    lease = %Lease{token: token, run_id: run_id, seq: seq, scope: scope, egress_policy: egress_policy}
    {:reply, token, %{st | leases: Map.put(st.leases, token, lease)}}
  end

  def handle_call({:revoke, token}, _from, st) do
    {:reply, :ok, %{st | leases: Map.delete(st.leases, token)}}
  end

  def handle_call({:request, token, req}, _from, st) do
    case Map.get(st.leases, token) do
      nil ->
        {:reply, {:denied, effect(:egress_denied, nil, req, reason: "no_lease")}, st}

      %Lease{} = lease ->
        {:reply, decide(lease, req), st}
    end
  end

  # --- the 05 §5.3 per-request decision ---

  defp decide(%Lease{} = lease, req) do
    dest = req[:dest] || req["dest"]

    case match_rule(lease.egress_policy, dest) do
      nil ->
        # default deny
        {:denied, effect(:egress_denied, lease, req, reason: "no_matching_policy", dest: dest)}

      rule ->
        if truthy(rule["requires_approval"]) do
          {:parked,
           effect(:approval_requested, lease, req, dest: dest, credential: rule["credential"])}
        else
          attach_and_forward(rule, lease, req, dest)
        end
    end
  end

  defp attach_and_forward(rule, lease, req, dest) do
    credential_id = rule["credential"]
    cred = credential_id && Mitos.Vault.get_by_id(credential_id, dest)

    # The real credential is fetched HERE and attached at the edge; the sandbox
    # never holds it. `forward/2` does the actual outbound call.
    outbound = attach(cred, req)
    resp = forward(outbound, dest)

    {:allowed, resp,
     effect(:egress_allowed, lease, req,
       dest: dest,
       credential_id: credential_id,
       status: resp.status,
       bytes: byte_size(resp.body || "")
     )}
  end

  # Attach the credential to the outbound request (bearer/header/basic/mTLS).
  # Never returns the secret to the caller — it stays inside `outbound`.
  defp attach(nil, req), do: req

  defp attach(%{kind: :bearer, secret: secret}, req) do
    headers = Map.put(req[:headers] || req["headers"] || %{}, "authorization", "Bearer " <> secret)
    Map.put(req, :headers, headers)
  end

  defp attach(%{kind: :header, secret: {name, value}}, req) do
    headers = Map.put(req[:headers] || req["headers"] || %{}, name, value)
    Map.put(req, :headers, headers)
  end

  defp attach(%{secret: secret}, req) do
    headers = Map.put(req[:headers] || req["headers"] || %{}, "authorization", "Bearer " <> secret)
    Map.put(req, :headers, headers)
  end

  # Actually perform the outbound crossing. For the MVP the "forward" is pluggable:
  # a test/mocked destination returns a canned response; a real HTTP call uses
  # :httpc. Egress is always logged by the caller as an Effect.
  defp forward(outbound, dest) do
    case Mitos.Broker.Destinations.resolve(dest) do
      {:mock, fun} ->
        fun.(outbound)

      :http ->
        http_forward(outbound, dest)
    end
  end

  defp http_forward(outbound, dest) do
    method = (outbound[:method] || outbound["method"] || "get") |> to_string() |> String.downcase()
    url = outbound[:url] || outbound["url"] || "https://#{dest}/"
    headers = for {k, v} <- outbound[:headers] || %{}, do: {String.to_charlist(k), String.to_charlist(v)}

    request =
      case method do
        m when m in ["post", "put", "patch"] ->
          body = outbound[:body] || ""
          ct = String.to_charlist(outbound[:content_type] || "application/json")
          {String.to_charlist(url), headers, ct, body}

        _ ->
          {String.to_charlist(url), headers}
      end

    case :httpc.request(String.to_atom(method), request, [], body_format: :binary) do
      {:ok, {{_, status, _}, _resp_headers, body}} ->
        %{status: status, headers: %{}, body: body}

      {:error, reason} ->
        %{status: 599, headers: %{}, body: "broker_forward_error: #{inspect(reason)}"}
    end
  end

  defp match_rule(policy, dest) when is_list(policy) do
    Enum.find(policy, fn rule ->
      pattern = rule["dest"] || rule[:dest]
      match_dest?(pattern, dest)
    end)
  end

  defp match_rule(_, _), do: nil

  defp match_dest?(nil, _), do: false
  defp match_dest?(pattern, dest) when pattern == dest, do: true

  defp match_dest?(pattern, dest) when is_binary(pattern) and is_binary(dest) do
    cond do
      String.starts_with?(pattern, "*.") ->
        suffix = String.trim_leading(pattern, "*")
        String.ends_with?(dest, suffix)

      true ->
        false
    end
  end

  defp match_dest?(_, _), do: false

  defp truthy(true), do: true
  defp truthy("true"), do: true
  defp truthy(_), do: false

  # Effect fields carry the lease's run_id + opening-Decision seq so the caller
  # (broker listener / provider) can route the crossing to the run's single log
  # writer with causality intact (04 §6: caused_by = the opening Decision).
  defp effect(type, lease, req, extra) do
    lease_fields =
      case lease do
        %Lease{run_id: run_id, seq: seq} -> [run_id: run_id, caused_by: seq]
        nil -> []
      end

    [type: type, dest: req[:dest] || req["dest"]] ++ lease_fields ++ extra
  end
end
