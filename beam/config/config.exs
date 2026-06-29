import Config

config :mitos,
  http_addr: {127, 0, 0, 1},
  # 7682 by default for the BEAM HTTP/WebSocket control plane.
  http_port: 7682
  # sidecar_path: set at runtime, defaults to ../zig-out/bin/mitos-term

# Phoenix LiveView drives the `/console` operations console. LiveViews call
# the same backend ops the HTTP API exposes — no HTTP loopback from the UI.
config :mitos, MitosWeb.Endpoint,
  url: [host: "localhost", port: 7682],
  adapter: Phoenix.Endpoint.Cowboy2Adapter,
  pubsub_server: Mitos.PubSub.Phoenix,
  live_view: [signing_salt: "mitos-console-signing-salt-2026"],
  render_errors: [
    formats: [html: MitosWeb.ErrorHTML],
    layout: false
  ],
  server: false,
  secret_key_base:
    System.get_env(
      "SECRET_KEY_BASE",
      "mitos-dev-secret-key-base-not-for-production-0123456789abcdef0123456789abcdef"
    )

config :phoenix, :json_library, Jason

config :logger, :default_formatter,
  format: "$time [$level] $message\n",
  level: :debug

import_config "#{config_env()}.exs"
