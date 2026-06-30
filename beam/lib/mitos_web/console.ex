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

  # Candidate workspace paths for the launch wizard's path picker. The console
  # is a local control-plane tool, so we offer the project the server runs in
  # plus its sibling directories — the common case ("review this repo",
  # "improve autofix") is right there without typing a path.
  @workspace_root_default "/media/data/proj"

  def workspace_default do
    cwd = File.cwd!()
    find_git_root(cwd) || cwd || @workspace_root_default
  end

  # Walk up from `dir` to the nearest ancestor containing a `.git`.
  defp find_git_root(dir) do
    dir = Path.expand(dir)
    Enum.reduce_while(Path.split(dir), dir, fn _part, current ->
      cond do
        File.dir?(Path.join(current, ".git")) or File.exists?(Path.join(current, ".git")) ->
          {:halt, current}

        current == "/" ->
          {:halt, nil}

        true ->
          {:cont, Path.dirname(current)}
      end
    end)
  end

  def workspace_candidates do
    root = workspace_default()

    siblings =
      case File.ls(Path.dirname(root)) do
        {:ok, entries} ->
          entries
          |> Enum.map(&Path.join(Path.dirname(root), &1))
          |> Enum.filter(&File.dir?/1)
          |> Enum.sort()

        _ ->
          []
      end

    Enum.uniq([root | siblings])
  end
end
