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
        inputs: { "schema": { "subject": { "type": "string", "required": true } } }
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

  # --- input validation (12 §4: validate before quote, quote before run) ---

  test "validate_inputs rejects missing required fields, accepts a well-formed order" do
    ep = %{inputs: %{"schema" => %{"properties" => %{"image" => %{"type" => "file"}}, "required" => ["image"]}}}
    assert {:error, {:missing_required, ["image"]}} = App.Manifest.validate_inputs(ep, %{})
    assert :ok = App.Manifest.validate_inputs(ep, %{"image" => "img.png"})
  end

  test "validate_inputs accepts the flat {field: type} schema spelling and checks types" do
    ep = %{inputs: %{"schema" => %{"width" => %{"type" => "int", "required" => true}, "name" => "string"}}}
    assert {:error, {:missing_required, ["width"]}} = App.Manifest.validate_inputs(ep, %{"name" => "x"})
    assert :ok = App.Manifest.validate_inputs(ep, %{"width" => 64, "name" => "x"})
    # a string that coerces to int is accepted; a non-coercible value is rejected
    assert :ok = App.Manifest.validate_inputs(ep, %{"width" => "64", "name" => "x"})
    assert {:error, {:bad_type, "int", _}} = App.Manifest.validate_inputs(ep, %{"width" => "wide"})
  end

  test "run_entrypoint validates inputs before any model spend" do
    dir = build_app()
    app = App.Manifest.load!(dir)

    # the 'go' entrypoint declares inputs.schema { subject: string }
    assert {:error, {:missing_required, _}} =
             App.run_entrypoint(app, "go", %{"org" => "test", "project" => app.name}, %{inputs: %{}})

    {:ok, run_id} =
      App.run_entrypoint(app, "go", %{"org" => "test", "project" => app.name}, %{inputs: %{"subject" => "hi"}})

    assert Run.await(run_id).status == :finished
  end

  # --- import eve (12 §10, 03 §10.1) ---

  test "import eve scaffolds a draft T2 app from an Eve agent/ directory" do
    eve = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "eve-agent"])
    File.mkdir_p!(Path.join(eve, "channels"))
    File.mkdir_p!(Path.join(eve, "connections"))
    File.mkdir_p!(Path.join(eve, "tools"))
    File.mkdir_p!(Path.join(eve, "evals"))

    File.write!(Path.join(eve, "instructions.md"), """
    # Sketch bot

    Turn a brief into a rough sketch. Ask clarifying questions first.
    """)

    File.write!(Path.join(eve, "channels/slack.ts"), "// slack channel")
    File.write!(Path.join(eve, "connections/figma.ts"), "// figma connection")
    File.write!(Path.join(eve, "tools/draw.ts"), "// a tool")
    File.write!(Path.join(eve, "evals/smoke.json"), "[]")

    out = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "sketch-app"])
    assert {:ok, ^out} = Mitos.Import.Eve.scaffold(eve, out)

    app = App.Manifest.load!(out)
    assert app.name == "sketch-bot"
    assert [%{as: "agent"}] = app.capabilities

    ep = App.Manifest.entrypoint(app, "run")
    # the message→order gap: a stub { message: string } order, marked draft (12 §10)
    assert ep.draft == true
    assert App.Manifest.draft?(app, "run")
    assert get_in(ep.inputs, ["schema", "message", "type"]) == "string"

    # imported capability defaults to T2 (03 §10.1); egress claims from connections/*
    assert app.requirements["trust_tier"] == "T2"
    assert "figma" in (app.requirements["egress"] || [])
    [first_input | _] = app.io["inputs"]
    assert first_input["connector"] == "slack"
  end
end
