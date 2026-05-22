import Config

config :mitos,
  http_addr: {127, 0, 0, 1},
  # 7682 by default — leaves 7681 to the legacy Zig `mitos webpty` server.
  http_port: 7682
  # sidecar_path: set at runtime, defaults to ../zig-out/bin/mitos-term

config :logger, :default_formatter,
  format: "$time [$level] $message\n",
  level: :info

import_config "#{config_env()}.exs"
