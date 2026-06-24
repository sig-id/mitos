defmodule Mitos.SandboxProvider do
  @moduledoc """
  Behaviour for compute-plane session providers.

  Providers materialize a runnable sandbox/session for a launch spec. The local
  sidecar implementation wraps the current engine process path for T0/T1 runs;
  the remote microVM implementation is the HTTP client seam used by T2-capable
  provider services.
  """

  @type launch_spec :: map()
  @type handle :: term()
  @type session :: term()
  @type command :: term()
  @type harvest_result :: %{diff: term(), result: term(), artifacts: list()}

  @callback prepare(launch_spec()) :: {:ok, handle()} | {:error, term()}
  @callback start(handle(), [String.t()], map() | keyword()) ::
              {:ok, session()} | {:error, term()}
  @callback stream(session()) :: Enumerable.t()
  @callback control(session(), command()) :: :ok | {:error, term()}
  @callback snapshot(session()) :: {:ok, term()} | :unsupported | {:error, term()}
  @callback harvest(session()) :: {:ok, harvest_result()} | {:error, term()}
  @callback stop(session()) :: :ok | {:error, term()}
  @callback capabilities() :: map()

  @typedoc "Options passed by `Mitos.SessionSupervisor.start_session/1`."
  @type start_options :: keyword()

  @callback child_spec(start_options()) :: Supervisor.child_spec()
end
