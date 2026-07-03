defmodule Mitos.SandboxProvider do
  @moduledoc """
  The one provider behaviour, many backends (01 §8.4, elaborated 05 §2).

  Lifecycle: `prepare → start → (stream ⇄ control)* → [snapshot] → harvest → stop`.

  `prepare/1` materializes a runnable, hermetic sandbox — workspace cloned IN,
  capability image RO, RO DATA snapshot, context slices, egress wired to the
  broker — and MUST NOT start the harness, MUST carry no secrets in env, and
  MUST fail closed if egress cannot be wired to the broker (05 §2). `harvest/1`
  is a pure read that MUST be callable after a clean OR failed/timed-out stop
  (05 §5.4): results are pulled by mitos, never pushed by the agent.
  """

  @type handle :: term()
  @type session :: term()

  @doc "Materialize a hermetic sandbox from a LaunchSpec. Idempotent on run_id."
  @callback prepare(Mitos.LaunchSpec.t()) :: {:ok, handle} | {:error, term()}

  @doc "Launch the harness process inside the prepared sandbox (env = non-secret config only)."
  @callback start(handle, argv :: [String.t()], env :: map()) ::
              {:ok, session} | {:error, term()}

  @doc "Yield the NORMALIZED harness event stream (01 §8.6) — the loop-manager's input."
  @callback stream(session) :: Enumerable.t()

  @doc "Steer the live session: approve | answer | interrupt | select | prompt. Mitos-initiated."
  @callback control(session, command :: term()) :: :ok | {:error, term()}

  @doc "Capture resumable state, or :unsupported (event-log replay is the authoritative fallback, 05 §8)."
  @callback snapshot(session) :: {:ok, term()} | :unsupported

  @doc "Extract results without trusting the agent: workspace diff + RESULT + registered artifacts."
  @callback harvest(session) ::
              {:ok, %{diff: term(), result: map(), artifacts: [map()]}} | {:error, term()}

  @doc "Terminate the session and release resources. Idempotent."
  @callback stop(session) :: :ok

  @doc "Static descriptor for the launch policy compiler (05 §4). `egress` MUST be `:broker`."
  @callback capabilities() :: %{
              required(:isolation) => atom(),
              required(:remote?) => boolean(),
              required(:egress) => :broker,
              required(:data_mount) => atom(),
              required(:snapshot?) => boolean(),
              required(:net_modes) => [atom()],
              optional(atom()) => term()
            }
end
