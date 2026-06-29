defmodule MitosWeb.Endpoint do
  @moduledoc """
  Phoenix Endpoint hosting the LiveView operations console.

  Served by Plug.Cowboy (see `Mitos.Web.Endpoint`'s child_spec); the raw
  `/ws/:session_id` PTY socket is split off by the cowboy dispatch list before
  reaching this plug.

  Anything that does not match a LiveView route is forwarded to the JSON
  control-plane router `Mitos.Web.Router` (`/apps`, `/runs`, `/agents`, …),
  preserving the existing HTTP contract for the CLI, external callers, webhooks,
  and the smoke tests.
  """
  use Phoenix.Endpoint, otp_app: :mitos

  @session_options [
    store: :cookie,
    key: "_mitos_console_key",
    signing_salt: "mitos-console-session-salt"
  ]

  # The websocket/longpoll mount for LiveView.
  socket "/live", Phoenix.LiveView.Socket,
    websocket: [connect_info: [{:session, @session_options}]],
    longpoll: [connect_info: [{:session, @session_options}]]

  # Static assets: the vendored phoenix/phoenix_live_view JS clients plus our
  # hand-written `app.css` (the Svelte token layer, ported verbatim) and a
  # favicon. Files live in `priv/static/assets/`; `at: "/"` maps `/assets/*`
  # onto that directory. No NodeJS asset pipeline is required for the console.
  plug Plug.Static,
    at: "/",
    from: :mitos,
    gzip: false,
    only: ~w(assets)

  plug Plug.RequestId
  plug Plug.Telemetry, event_prefix: [:phoenix, :endpoint]

  plug Plug.Session,
    store: :cookie,
    key: "_mitos_console_key",
    signing_salt: "mitos-console-session-salt"

  plug MitosWeb.Router
end
