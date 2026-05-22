import Config

# Use an ephemeral port; tests don't bind to a fixed address and we should
# never collide with a running mitos webpty on the dev port.
config :mitos, http_port: 0

config :logger, level: :warning
