defmodule Mitos.SessionSupervisor do
  @moduledoc """
  DynamicSupervisor for sandbox-provider session children.

  This supervisor owns local engine sessions; higher-level orchestration is a
  separate control-plane concern. The current implementation starts the local
  sidecar provider, which wraps `Mitos.Session`.
  """
  use DynamicSupervisor

  @default_provider Mitos.SandboxProvider.LocalSidecar

  def start_link(_init_arg \\ nil) do
    DynamicSupervisor.start_link(__MODULE__, nil, name: __MODULE__)
  end

  @impl true
  def init(_), do: DynamicSupervisor.init(strategy: :one_for_one)

  @doc """
  Start a new provider-backed session.

  Required:
    * `:launch_spec` - provider/task/transport order for agent runs, or
    * `:argv` - legacy raw command for compatibility sessions

  Optional:
    * `:session_id` (default: random UUID-ish string)
    * `:sandbox_provider` (default: `Mitos.SandboxProvider.LocalSidecar`)
    * `:cwd`, `:env` (list of {k,v} or map)
    * `:cols`, `:rows`
    * `:idle_ms`
    * `:sidecar_path` - overrides `Application.get_env(:mitos, :sidecar_path)`
  """
  def start_session(opts) do
    session_id = Keyword.get(opts, :session_id, random_id())
    provider = Keyword.get(opts, :sandbox_provider, @default_provider)
    sidecar_path = Keyword.get_lazy(opts, :sidecar_path, &default_sidecar_path/0)

    opts =
      opts
      |> Keyword.put(:session_id, session_id)
      |> Keyword.put(:sidecar_path, sidecar_path)

    case DynamicSupervisor.start_child(__MODULE__, provider.child_spec(opts)) do
      {:ok, pid} ->
        {:ok, %{pid: pid, session_id: session_id, sandbox_provider: provider}}

      {:error, {:already_started, pid}} ->
        {:ok, %{pid: pid, session_id: session_id, sandbox_provider: provider}}

      other ->
        other
    end
  end

  def stop_session(session_id) do
    case Registry.lookup(Mitos.SessionRegistry, session_id) do
      [{pid, _}] -> DynamicSupervisor.terminate_child(__MODULE__, pid)
      [] -> {:error, :not_found}
    end
  end

  def list_sessions do
    DynamicSupervisor.which_children(__MODULE__)
    |> Enum.map(fn {_, pid, _, _} -> Mitos.Session.session_id(pid) end)
  end

  defp default_sidecar_path do
    # Resolve to <repo>/zig-out/bin/mitos-term: this file lives at
    # <repo>/beam/lib/mitos/session_supervisor.ex so we walk up three directories.
    Application.get_env(:mitos, :sidecar_path) ||
      System.get_env("MITOS_TERM_BIN") ||
      Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
  end

  defp random_id do
    "sess-" <> (:crypto.strong_rand_bytes(8) |> Base.url_encode64(padding: false))
  end
end
