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
      # I/O plane (10): bindings registry + ingress dedup, plus sink delivery
      # transports (in-process for the MVP; chat/email breadth is demand-gated).
      Mitos.IO,
      Mitos.Sink.Notify.Destination,
      Mitos.Sink.Reply.Destination,
      Mitos.Broker.Destinations,
      Mitos.Settlement.Mock,
      Mitos.Facilitator,
      {DynamicSupervisor, name: Mitos.Run.Supervisor, strategy: :one_for_one},
      {Task.Supervisor, name: Mitos.Run.TaskSupervisor},
      {Mitos.Broker, port: Application.fetch_env!(:mitos, :broker_port)},
      # The broker ingress is loopback-only — the sandbox's single way out (05 §5.3).
      {Bandit,
       plug: Mitos.Broker.Listener,
       ip: {127, 0, 0, 1},
       port: Application.fetch_env!(:mitos, :broker_port)},
      {Bandit, plug: Mitos.Web.Router, port: Application.fetch_env!(:mitos, :http_port)}
    ]

    Supervisor.start_link(children, strategy: :one_for_one, name: Mitos.Supervisor)
  end
end
