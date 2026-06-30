defmodule Mitos.Application do
  @moduledoc """
  Top-level supervision tree.

  Layout:

      Mitos.Supervisor (one_for_one)
      ├── Registry (Mitos.SessionRegistry)        # session_id → Session pid
      ├── Registry (Mitos.PubSub.Registry)         # session_id → subscribers
      ├── Mitos.SessionSupervisor (DynamicSupervisor)   # one Session per session
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
      Mitos.SessionSupervisor,
      {Task.Supervisor, name: Mitos.ConsoleTaskSupervisor},
      # Phoenix.PubSub backs the LiveView console (`/console`). The existing
      # Registry-based `Mitos.PubSub` (raw PTY byte fan-out) is unchanged.
      {Phoenix.PubSub, adapter: Phoenix.PubSub.PG2, name: Mitos.PubSub.Phoenix},
      # Boots the Phoenix Endpoint supervisor (persistent terms, code reloader,
      # static cache) WITHOUT binding an HTTP port — Plug.Cowboy below owns the
      # socket and the custom `/ws/:id` dispatch.
      MitosWeb.Endpoint,
      # HTTP + WebSocket entry. The cowboy dispatch routes the raw `/ws/:id`
      # PTY socket to `Mitos.Web.TerminalSocket` and everything else to the
      # Phoenix Endpoint, which serves the LiveView console under `/console`
      # and forwards the rest to the JSON API router (`Mitos.Web.Router`).
      Mitos.Web.Endpoint
    ]

    opts = [strategy: :one_for_one, name: Mitos.Supervisor]
    Supervisor.start_link(children, opts)
  end
end
