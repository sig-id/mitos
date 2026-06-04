trace_dir =
  Path.join(System.tmp_dir!(), "mitos-test-traces-#{System.unique_integer([:positive])}")

Application.put_env(:mitos, :trace_base_dir, trace_dir)

ExUnit.start()

ExUnit.after_suite(fn _result ->
  File.rm_rf!(trace_dir)
end)
