defmodule Mitos.Catalog do
  @moduledoc """
  In-memory registry of published capabilities and applications, plus their
  facilitator-side offers (08 §8, 12 §8). Identity (`capability_id`/`app_id`) is
  content-addressed (03 §9, 12 §7); the offer (price/payout) is separate and
  mutable — repricing never mints a new id (12 §8).
  """
  use Agent

  def start_link(_opts \\ []) do
    Agent.start_link(fn -> %{capabilities: %{}, apps: %{}, offers: %{}} end, name: __MODULE__)
  end

  # --- capabilities ---

  def put_capability(%Mitos.Capability.Manifest{} = m) do
    Agent.update(__MODULE__, fn s ->
      put_in(s, [:capabilities, m.capability_id], m)
    end)

    m.capability_id
  end

  def get_capability(id), do: Agent.get(__MODULE__, &get_in(&1, [:capabilities, id]))

  def capabilities, do: Agent.get(__MODULE__, & &1.capabilities)

  # --- applications ---

  def put_app(%Mitos.App.Manifest{} = app) do
    Agent.update(__MODULE__, fn s -> put_in(s, [:apps, app.app_id], app) end)
    app.app_id
  end

  def get_app(id), do: Agent.get(__MODULE__, &get_in(&1, [:apps, id]))

  def apps, do: Agent.get(__MODULE__, & &1.apps)

  # --- offers (facilitator-side, mutable) ---

  @doc """
  Attach an offer to a published id (08 §8 steps 4/6). `offer` carries
  `%{price: ..., payout_addr: ..., seller: ...}` and is looked up by
  capability_id, app_id, or entrypoint key.
  """
  def put_offer(key, offer) do
    Agent.update(__MODULE__, fn s -> put_in(s, [:offers, key], offer) end)
    key
  end

  def get_offer(key), do: Agent.get(__MODULE__, &get_in(&1, [:offers, key]))

  def clear, do: Agent.update(__MODULE__, fn _ -> %{capabilities: %{}, apps: %{}, offers: %{}} end)
end
