import Config

config :mitos,
  data_dir: Path.join(System.tmp_dir!(), "mitos-test-#{System.os_time(:millisecond)}"),
  http_port: 4746,
  broker_port: 4747

config :logger, level: :warning
