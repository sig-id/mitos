defmodule MitosWeb.Console do
  @moduledoc """
  Console-side data access — the LiveView equivalent of the Svelte `lib/api.ts`.

  Every call here goes straight to the same backend ops the JSON/HTTP control
  plane exposes (`Mitos.Applications`, `Mitos.Runs`, `Mitos.Connector`,
  `Mitos.Sink`, `Mitos.Session`). The console never round-trips through HTTP.
  """

  def list_apps, do: Mitos.Applications.list()

  def fetch_app(id) do
    case Mitos.Applications.fetch(id) do
      {:ok, app} -> {:ok, Mitos.Applications.public_view(app, local_id: id)}
      error -> error
    end
  end

  def list_runs, do: Mitos.Runs.list()

  def fetch_run(id), do: Mitos.Runs.fetch(id)

  def list_connectors do
    Enum.map(Mitos.Connector.names(), fn name ->
      module = Mitos.Connector.lookup(name)
      %{"name" => name, "capabilities" => module.capabilities()}
    end)
  end

  def list_sinks do
    Enum.map(Mitos.Sink.names(), fn name ->
      module = Mitos.Sink.lookup(name)
      %{"name" => name, "capabilities" => module.capabilities()}
    end)
  end

  def add_app(source) when is_binary(source) do
    Mitos.Applications.add_source(source, nil, [])
  end

  def run_entrypoint(app_id, entrypoint_id, inputs, opts) do
    Mitos.Applications.run_entrypoint(app_id, entrypoint_id, inputs, opts)
  end

  def steer(session_id, interaction, opts) when is_list(opts) do
    case Registry.lookup(Mitos.SessionRegistry, session_id) do
      [{pid, _}] -> Mitos.Session.steer(pid, interaction, opts)
      [] -> {:error, :unknown_session}
    end
  end
end
