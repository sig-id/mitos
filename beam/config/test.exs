import Config

config :mitos,
  data_dir: Path.join(System.tmp_dir!(), "mitos-test-#{System.os_time(:millisecond)}"),
  http_port: 4746,
  broker_port: 4747,
  # isolate the model-egress proxy so the suite never collides with a live `serve`.
  model_proxy_port: 4748

config :logger, level: :warning
