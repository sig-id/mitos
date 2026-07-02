ExUnit.start()

# Each test run gets an isolated data_dir under tmp (config/test.exs). Clean it
# up between the suite runs to keep the event logs deterministic.
File.rm_rf(Mitos.data_dir())
File.mkdir_p!(Mitos.data_dir())
