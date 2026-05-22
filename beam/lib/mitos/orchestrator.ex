defmodule Mitos.Orchestrator do
  @moduledoc """
  DynamicSupervisor for `Mitos.Session` children. Multi-agent orchestration
  composes here: fanning a task across agents, gating one on another's
  snapshot, broadcasting a kill — all ordinary OTP message-passing against
  the sessions this supervisor owns.
  """
  use DynamicSupervisor

  def start_link(_init_arg \\ nil) do
    DynamicSupervisor.start_link(__MODULE__, nil, name: __MODULE__)
  end

  @impl true
  def init(_), do: DynamicSupervisor.init(strategy: :one_for_one)

  @doc """
  Start a new session.

  Required:
    * `:argv` — list of strings, the agent argv (claude/codex/etc.)

  Optional:
    * `:session_id` (default: random UUID-ish string)
    * `:cwd`, `:env` (list of {k,v} or map)
    * `:cols`, `:rows`
    * `:idle_ms`
    * `:sidecar_path` — overrides `Application.get_env(:mitos, :sidecar_path)`
  """
  def start_session(opts) do
    session_id = Keyword.get(opts, :session_id, random_id())
    sidecar_path = Keyword.get_lazy(opts, :sidecar_path, &default_sidecar_path/0)

    opts =
      opts
      |> Keyword.put(:session_id, session_id)
      |> Keyword.put(:sidecar_path, sidecar_path)

    case DynamicSupervisor.start_child(__MODULE__, {Mitos.Session, opts}) do
      {:ok, pid} -> {:ok, %{pid: pid, session_id: session_id}}
      {:error, {:already_started, pid}} -> {:ok, %{pid: pid, session_id: session_id}}
      other -> other
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
    # <repo>/beam/lib/mitos/orchestrator.ex so we walk up three directories.
    Application.get_env(:mitos, :sidecar_path) ||
      System.get_env("MITOS_TERM_BIN") ||
      Path.expand("../../../zig-out/bin/mitos-term", __DIR__)
  end

  defp random_id do
    "sess-" <> (:crypto.strong_rand_bytes(8) |> Base.url_encode64(padding: false))
  end
end
