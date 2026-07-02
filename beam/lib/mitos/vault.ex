defmodule Mitos.Vault do
  @moduledoc """
  The secrets vault — the ONLY place real credentials exist (01 §8.5, 08 §3.2).

  Keys are scoped `(tenant/seller_id, destination) -> credential`. They live only
  here on the control plane; they never enter a sandbox, never appear in a
  LaunchSpec env, and never land on the event log (only `credential_id`
  references and brokered-crossing Effects do — 09 §4).

  In-memory for the MVP; a hosted deployment backs this with a real KMS.
  """
  use Agent

  def start_link(_opts \\ []), do: Agent.start_link(fn -> %{} end, name: __MODULE__)

  @doc "Register a credential for `(scope, dest)`. Returns its opaque credential_id."
  def put(scope, dest, secret, opts \\ []) do
    credential_id = Keyword.get(opts, :credential_id, Mitos.id("cred"))
    kind = Keyword.get(opts, :kind, :bearer)

    Agent.update(__MODULE__, fn m ->
      Map.put(m, {scope, dest}, %{credential_id: credential_id, kind: kind, secret: secret})
    end)

    credential_id
  end

  @doc "Fetch the real credential for `(scope, dest)`. Broker-only caller."
  def get(scope, dest), do: Agent.get(__MODULE__, &Map.get(&1, {scope, dest}))

  @doc "Fetch by credential_id + dest (broker attach path)."
  def get_by_id(credential_id, dest) do
    Agent.get(__MODULE__, fn m ->
      Enum.find_value(m, fn
        {{_scope, ^dest}, %{credential_id: ^credential_id} = c} -> c
        _ -> nil
      end)
    end)
  end

  def clear, do: Agent.update(__MODULE__, fn _ -> %{} end)
end
