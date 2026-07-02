defmodule Mitos.Application do
  # One supervision tree, one control plane (09 P0). BEAM owns all
  # orchestration, the log, the broker, tenancy, and metering (01 §7).
  use Application

  @impl true
  def start(_type, _args) do
    File.mkdir_p!(Mitos.data_dir())

    children = [
      {Registry, keys: :unique, name: Mitos.Run.Registry},
      Mitos.Vault,
      Mitos.Catalog,
      Mitos.WorkPool,
      {DynamicSupervisor, name: Mitos.Run.Supervisor, strategy: :one_for_one},
      {Mitos.Broker, port: Application.fetch_env!(:mitos, :broker_port)},
      {Bandit, plug: Mitos.Web.Router, port: Application.fetch_env!(:mitos, :http_port)}
    ]

    Supervisor.start_link(children, strategy: :one_for_one, name: Mitos.Supervisor)
  end
end
