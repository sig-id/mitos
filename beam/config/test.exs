import Config

config :mitos,
  data_dir: Path.join(System.tmp_dir!(), "mitos-test-#{System.os_time(:millisecond)}"),
  http_port: 4746,
  broker_port: 4747,
  # isolate the model-egress proxy so the suite never collides with a live `serve`.
  model_proxy_port: 4748,
  # Several tests assert on sandbox internals AFTER a run finishes (hermetic
  # env, brokered-egress evidence written inside the sandbox). Reaping on finish
  # is the production default; here the copy is the subject under test, so it
  # stays and `Mitos.GC` is exercised explicitly instead.
  gc: [reap_on_finish: false, keep_days: 7]

config :logger, level: :warning
