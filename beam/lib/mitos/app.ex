defmodule Mitos.App do
  @moduledoc """
  The application lifecycle (12): adding an app to a tenant activates its
  shipped I/O defaults (10 §4 / 12 §5 — *ship defaults, bind at install*), and
  running an entrypoint is one backend op shared by the web form, the CLI, and
  connectors (01 §10.11).

  The manifest's `io` block is the *ship* phase: it names connector/sink classes
  and how to render/select, with **no credentials** (creds are tenant-side,
  `10 §7`). `bind/2` is the *install* phase: it turns those defaults into live
  `Mitos.IO.Binding`s for a tenant, resolving each entrypoint's pipeline +
  capabilities into the connector's `select`.

  Authority stays where `01`/`05` put it: this module wires, it does not grant
  egress or override trust tiers (12 §11).
  """

  alias Mitos.{App, Catalog, IO, Run}
  alias Mitos.IO.Binding

  @doc "Add an app to a tenant: store it and activate its shipped I/O defaults."
  def add(%App.Manifest{} = app, tenant) do
    Catalog.put_app(app)
    bind(app, tenant)
    app
  end

  @doc """
  Activate an app's `io` defaults for a tenant (12 §5). Each `io.inputs` entry
  becomes an input `Binding` (connector → entrypoint `select`); each `io.outputs`
  entry becomes an output `Binding` (sink on a terminal Effect). Unknown classes
  are skipped — an app MAY name a usage-specific connector not yet registered.
  """
  def bind(%App.Manifest{} = app, tenant) do
    org = tenant_org(tenant)

    for entry <- List.wrap(app.io["inputs"]), do: register_input(app, entry, tenant, org)
    for entry <- List.wrap(app.io["outputs"]), do: register_output(app, entry, tenant, org)

    :ok
  end

  @doc """
  Run an application entrypoint (12 §4: entrypoint → Task → pipeline). Validates
  inputs against the entrypoint's schema BEFORE any model spend (12 §4: "validate
  before quote, quote before run"). Shared by the web order form, the CLI, and
  connectors — the same op (01 §10.11).
  """
  def run_entrypoint(%App.Manifest{} = app, ep_id, tenant, caller_inputs) do
    with %{id: ^ep_id} = ep <- App.Manifest.entrypoint(app, ep_id) || :no_entrypoint,
         :ok <- App.Manifest.validate_inputs(ep, caller_inputs.inputs || %{}),
         pipeline when not is_nil(pipeline) <- App.Manifest.pipeline(app, ep.pipeline) || :no_pipeline do
      capabilities =
        Map.new(app.capabilities, fn c -> {c.as, Path.expand(c.ref, app.dir)} end)

      Run.start_run(
        tenant: tenant,
        pipeline: pipeline.body,
        capabilities: capabilities,
        workspace: caller_inputs[:workspace],
        inputs: Map.merge(ep.defaults["inputs"] || %{}, caller_inputs.inputs || %{}),
        parameters: Map.merge(default_parameters(ep), caller_inputs[:parameters] || %{}),
        budget: Map.merge(ep.defaults["budget"] || %{}, caller_inputs[:budget] || %{})
      )
    else
      :no_entrypoint -> {:error, :no_entrypoint}
      :no_pipeline -> {:error, :no_pipeline}
      {:error, _} = e -> e
    end
  end

  # --- binding registration ---

  defp register_input(app, entry, tenant, _org) do
    connector = entry["connector"] || entry[:connector]

    with mod when not is_nil(mod) <- IO.connector_module(connector),
         ep_id when not is_nil(ep_id) <- entry["entrypoint"] do
      name = binding_name(app, ep_id, connector)

      IO.register(%Binding{
        tenant: tenant,
        name: name,
        direction: :input,
        kind: String.to_atom(connector),
        module: mod,
        config: strip_creds(entry),
        select: select_for(app, ep_id, entry["select"])
      })
    end
  end

  defp register_output(app, entry, tenant, _org) do
    sink = entry["sink"] || entry[:sink]

    with mod when not is_nil(mod) <- IO.sink_module(sink),
         ep_id when not is_nil(ep_id) <- entry["entrypoint"] do
      name = binding_name(app, ep_id, sink)

      IO.register(%Binding{
        tenant: tenant,
        name: name,
        direction: :output,
        kind: String.to_atom(sink),
        module: mod,
        config: strip_creds(entry),
        on: IO.normalize_on(entry["on"] || "capability_finished"),
        cut: :commercial
      })
    end
  end

  defp select_for(app, ep_id, raw_select) do
    ep = App.Manifest.entrypoint(app, ep_id)
    pipeline = ep && App.Manifest.pipeline(app, ep.pipeline)

    capabilities =
      Map.new(app.capabilities, fn c -> {c.as, Path.expand(c.ref, app.dir)} end)

    %{
      mode: (raw_select && raw_select["mode"]) || "explicit",
      pipeline: pipeline && %{name: pipeline.id, body: pipeline.body},
      capabilities: capabilities,
      entrypoint: ep_id
    }
  end

  defp binding_name(app, ep_id, class), do: "#{app.name}.#{ep_id}.#{class}"

  # Strip credentials/tokens from a manifest io entry (12 §5: ship defaults
  # carry no creds). A tenant re-binds with vault-backed creds at install.
  defp strip_creds(entry) do
    entry
    |> Map.drop(["secret", "token", "credential", "credentials"])
    |> Map.drop([:secret, :token, :credential, :credentials])
  end

  defp default_parameters(ep) do
    for p <- ep.parameters || [], into: %{} do
      {p["name"], Map.get(p, "default")}
    end
  end

  defp tenant_org(%{"org" => org}), do: org
  defp tenant_org(%{org: org}), do: org
  defp tenant_org(org) when is_binary(org), do: org
  defp tenant_org(_), do: "local"
end
