defmodule Mitos.Broker.Destinations do
  @moduledoc """
  Pluggable resolution of an egress destination to a forwarder. Real
  destinations forward over HTTP; tests register mock destinations so a run can
  exercise brokered egress deterministically without a network.

  This is a test/dev seam only — it does not relax the broker's authority model:
  a destination still only forwards if `egress_policy` matched (05 §5.3).
  """
  use Agent

  def start_link(_ \\ []), do: Agent.start_link(fn -> %{} end, name: __MODULE__)

  defp ensure_started do
    case Process.whereis(__MODULE__) do
      nil -> start_link()
      _ -> :ok
    end
  end

  @doc "Register a mock destination: `dest` string -> fn(outbound) -> response map."
  def mock(dest, fun) when is_function(fun, 1) do
    ensure_started()
    Agent.update(__MODULE__, &Map.put(&1, dest, fun))
  end

  def clear do
    ensure_started()
    Agent.update(__MODULE__, fn _ -> %{} end)
  end

  @doc "Resolve a destination to `{:mock, fun}` or `:http`."
  def resolve(dest) do
    ensure_started()

    case Agent.get(__MODULE__, &Map.get(&1, dest)) do
      nil -> :http
      fun -> {:mock, fun}
    end
  end
end
