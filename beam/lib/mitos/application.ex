defmodule Mitos.Application do
  @moduledoc """
  Top-level supervision tree.

  Layout:

      Mitos.Supervisor (one_for_one)
      ├── Registry (Mitos.SessionRegistry)        # session_id → Session pid
      ├── Registry (Mitos.PubSub.Registry)         # session_id → subscribers
      ├── Mitos.Orchestrator (DynamicSupervisor)   # one child per session
      └── Mitos.Web.Endpoint (Plug.Cowboy)         # HTTP + WebSocket

  Each `Mitos.Session` GenServer owns one `mitos-term` OS process via a Port.
  A sidecar crash takes down only its owning Session; the DynamicSupervisor
  restarts it. "Let it crash" carried across the FFI boundary — exactly the
  guarantee a NIF would not give.
  """
  use Application

  @impl true
  def start(_type, _args) do
    children = [
      {Registry, keys: :unique, name: Mitos.SessionRegistry},
      {Registry, keys: :duplicate, name: Mitos.PubSub.Registry},
      Mitos.Orchestrator,
      Mitos.Web.Endpoint
    ]

    opts = [strategy: :one_for_one, name: Mitos.Supervisor]
    Supervisor.start_link(children, opts)
  end
end
