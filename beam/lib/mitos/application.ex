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
      # Seed the Vault's model credentials at boot (app.manifest in prod, the
      # operator's ~/.pi/agent/auth.json in dev). Runs before any run starts.
      Mitos.Broker.ModelCredentials,
      Mitos.Catalog,
      Mitos.WorkPool,
      # I/O plane (10): bindings registry + ingress dedup, plus sink delivery
      # transports (in-process for the MVP; chat/email breadth is demand-gated).
      Mitos.IO,
      # cross-run knowledge service (07) — two-track memory + distill.
      Mitos.Memory,
      Mitos.Sink.Notify.Destination,
      Mitos.Sink.Reply.Destination,
      Mitos.Broker.Destinations,
      Mitos.Settlement.Mock,
      Mitos.Facilitator,
      # The MITM certificate authority for the TLS model-egress proxy (05 R1).
      Mitos.Broker.CertAuthority,
      {DynamicSupervisor, name: Mitos.Run.Supervisor, strategy: :one_for_one},
      {Task.Supervisor, name: Mitos.Run.TaskSupervisor},
      {Mitos.Broker, port: Application.fetch_env!(:mitos, :broker_port)},
      # The broker ingress is loopback-only — the sandbox's single way out (05 §5.3).
      {Bandit,
       plug: Mitos.Broker.Listener,
       ip: {127, 0, 0, 1},
       port: Application.fetch_env!(:mitos, :broker_port)},
      {Bandit, plug: Mitos.Web.Router, port: Application.fetch_env!(:mitos, :http_port)}
      | model_proxy_child() ++ tls_mitm_child()
    ]

    Supervisor.start_link(children, strategy: :one_for_one, name: Mitos.Supervisor)
  end

  defp model_proxy_child do
    case Application.get_env(:mitos, :model_proxy_port, 0) do
      port when port > 0 ->
        [{Bandit, plug: Mitos.Broker.ModelProxy, ip: {127, 0, 0, 1}, port: port}]

      _ ->
        []
    end
  end

  # The TLS-MITM edge (05 R1): started only when a port is configured.
  defp tls_mitm_child do
    case Application.get_env(:mitos, :mitm_proxy_port, 0) do
      port when port > 0 -> [{Mitos.Broker.TlsMitm, port: port}]
      _ -> []
    end
  end
end
