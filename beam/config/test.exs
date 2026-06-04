import Config

# Use an ephemeral port; tests don't bind to a fixed address.
config :mitos, http_port: 0

config :logger, level: :warning
