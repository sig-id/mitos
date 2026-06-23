defmodule Mitos.SandboxProvider do
  @moduledoc """
  Behaviour for compute-plane session providers.

  Providers materialize a runnable sandbox/session for a launch spec and expose
  it as an OTP child. P0 only wires the local sidecar implementation; Nucleus,
  Docker, and remote microVM providers fill this seam in later phases.
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
