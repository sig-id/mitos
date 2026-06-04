defmodule Mitos.TraceTest do
  use ExUnit.Case, async: true

  test "writes manifest and JSONL events with binary-safe normalization" do
    base_dir = unique_tmp_dir("trace")
    on_exit(fn -> File.rm_rf!(base_dir) end)

    trace =
      Mitos.Trace.open!(
        run_id: "run-1",
        base_dir: base_dir,
        launch_spec: %{"provider" => "fake", "task" => "hello"}
      )

    trace =
      Mitos.Trace.append_event(trace, %{
        "type" => "STRUCTURED_OUTPUT",
        "bytes" => Msgpax.Bin.new(<<255, 0>>)
      })

    run_dir = Path.join([base_dir, ".runs", "run-1"])
    assert File.read!(Path.join(run_dir, "run.yaml")) =~ "provider: fake"

    [line] =
      run_dir
      |> Path.join("events.jsonl")
      |> File.read!()
      |> String.split("\n", trim: true)

    assert %{
             "run_id" => "run-1",
             "sequence" => 0,
             "event" => %{
               "type" => "STRUCTURED_OUTPUT",
               "bytes" => %{"encoding" => "base64", "data" => "/wA="}
             }
           } = Jason.decode!(line)

    assert trace.seq == 1
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
