defmodule Mitos.PipelineRegistryTest do
  use ExUnit.Case, async: false

  setup do
    previous_workloads_dir = Application.get_env(:mitos, :workloads_dir)

    on_exit(fn ->
      restore_env(:workloads_dir, previous_workloads_dir)
    end)

    :ok
  end

  test "create refuses a symlinked workload definition directory" do
    outside = unique_tmp_dir("pipeline-registry-symlink-target")
    link = unique_tmp_dir("pipeline-registry-symlink")
    File.mkdir_p!(outside)
    File.ln_s!(outside, link)
    Application.put_env(:mitos, :workloads_dir, link)

    on_exit(fn ->
      File.rm(link)
      File.rm_rf!(outside)
    end)

    assert {:error, {:invalid_pipeline, detail}} =
             Mitos.PipelineRegistry.create(%{
               id: "new-pipeline",
               provider: "fake",
               template: "Do {{objective}}"
             })

    assert detail =~ "could not persist definition"
    assert detail =~ ":symlink"
    refute File.exists?(Path.join(outside, "new-pipeline.json"))
  end

  test "update refuses to read definitions through a symlinked workload directory" do
    outside = unique_tmp_dir("pipeline-registry-update-symlink-target")
    link = unique_tmp_dir("pipeline-registry-update-symlink")
    File.mkdir_p!(outside)
    File.write!(Path.join(outside, "existing-pipeline.json"), "not json")
    File.ln_s!(outside, link)
    Application.put_env(:mitos, :workloads_dir, link)

    on_exit(fn ->
      File.rm(link)
      File.rm_rf!(outside)
    end)

    assert {:error, {:invalid_pipeline, detail}} =
             Mitos.PipelineRegistry.update("existing-pipeline", %{
               provider: "fake",
               template: "Do {{objective}}"
             })

    assert detail =~ "workload definition directory unavailable"
    refute detail =~ "bad JSON"
    assert File.read!(Path.join(outside, "existing-pipeline.json")) == "not json"
  end

  defp unique_tmp_dir(prefix) do
    path = Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{tmp_suffix()}")
    File.rm_rf!(path)
    path
  end

  defp tmp_suffix do
    random = :crypto.strong_rand_bytes(8) |> Base.url_encode64(padding: false)
    "#{System.unique_integer([:positive, :monotonic])}-#{random}"
  end

  defp restore_env(key, nil), do: Application.delete_env(:mitos, key)
  defp restore_env(key, value), do: Application.put_env(:mitos, key, value)
end
