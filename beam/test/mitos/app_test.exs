defmodule Mitos.AppTest do
  @moduledoc """
  01 §8.8 / 12: the application manifest — content-addressed identity, pinned
  capabilities, entrypoints as the runnable surface. A user runs a pipeline
  named by an entrypoint, never a bare capability.
  """
  use ExUnit.Case, async: true

  alias Mitos.{App, Fixtures, Run}

  defp build_app do
    dir = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "app"])
    File.mkdir_p!(Path.join(dir, "pipelines"))

    cap = Fixtures.capability("worker")
    File.cp_r!(cap, Path.join(dir, "worker"))

    File.write!(
      Path.join(dir, "pipelines/main.md"),
      Fixtures.pipeline(["worker"], name: "main-flow")
    )

    File.write!(Path.join(dir, "app.yaml"), """
    name: test-app
    version: 0.1.0
    capabilities:
      - { ref: "worker", as: "worker" }
    pipelines:
      - { id: "main", file: "pipelines/main.md" }
    entrypoints:
      - id: "go"
        pipeline: "main"
        title: "Run the worker"
        workspace: "none"
        inputs: { "schema": { "subject": "string" } }
        defaults: { "tone": "neutral" }
    """)

    dir
  end

  test "loads with content-addressed app_id; repackaging identically is stable (12 §7)" do
    dir = build_app()
    app = App.Manifest.load!(dir)

    assert app.name == "test-app"
    assert app.app_id =~ ~r/\A[a-f0-9]{64}\z/
    assert [%{as: "worker", capability_id: cap_id}] = app.capabilities
    assert cap_id =~ ~r/\A[a-f0-9]{64}\z/

    # identity is content: loading again yields the same id
    assert App.Manifest.load!(dir).app_id == app.app_id

    ep = App.Manifest.entrypoint(app, "go")
    assert ep.pipeline == "main"
    assert ep.workspace == %{kind: "none"}
    assert App.Manifest.pipeline(app, "main").sha256 == Mitos.sha256(App.Manifest.pipeline(app, "main").body)
  end

  test "an entrypoint maps onto a run (12 §4: entrypoint → Task → pipeline)" do
    dir = build_app()
    app = App.Manifest.load!(dir)
    ep = App.Manifest.entrypoint(app, "go")
    pipeline = App.Manifest.pipeline(app, ep.pipeline)

    capabilities = Map.new(app.capabilities, fn c -> {c.as, Path.expand(c.ref, dir)} end)

    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "test", "project" => app.name},
        pipeline: pipeline.body,
        capabilities: capabilities,
        inputs: Map.merge(ep.defaults, %{"subject" => "x"})
      )

    state = Run.await(run_id)
    assert state.status == :finished
    assert state.stages["worker"].result["signal"] == "ship"
  end
end
