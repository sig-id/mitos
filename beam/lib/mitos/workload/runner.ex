defmodule Mitos.Workload.Runner do
  @moduledoc """
  Executes one workload run synchronously: renders the template, starts an
  agent session, subscribes to its event stream, and blocks the calling
  process until the agent exits (or the workload timeout fires).

  The result favors the engine's `final_answer_candidate` prompt event when
  one was seen; otherwise it falls back to the ANSI-stripped raw output. The
  session id doubles as the run id, so the full trace under
  `.runs/<session_id>/` is the audit artifact for the run.
  """

  require Logger

  alias Mitos.Workload

  @type result :: %{
          result: String.t(),
          run_id: String.t(),
          exit_code: integer() | nil,
          errors: [String.t()]
        }

  # Strips CSI, OSC, and single-char escape sequences from PTY output.
  @ansi ~r/\x1B(?:\[[0-?]*[ -\/]*[@-~]|\][^\x07\x1B]*(?:\x07|\x1B\\)?|[@-Z\\-_])/

  @spec execute(%Workload{}, map()) :: {:ok, result()} | {:error, term()}
  def execute(%Workload{} = workload, bindings) when is_map(bindings) do
    with {:ok, task} <- Workload.render(workload, bindings) do
      run(workload, task)
    end
  end

  defp run(workload, task) do
    session_id = "wl-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)
    Mitos.PubSub.subscribe(session_id)

    opts =
      [
        provider: workload.provider,
        task: task,
        session_id: session_id,
        transport_order: workload.transport_order,
        idle_ms: workload.idle_ms
      ]
      |> maybe(:env, Workload.resolved_env(workload))
      |> maybe(:cwd, workload.cwd)

    case Mitos.Agent.start(opts) do
      {:ok, agent_run} ->
        deadline = System.monotonic_time(:millisecond) + workload.timeout_ms
        acc = %{raw: [], final: nil, candidate: nil, errors: []}
        collect(agent_run, workload, acc, deadline)

      {:error, reason} ->
        {:error, {:launch_failed, reason}}
    end
  end

  defp collect(agent_run, workload, acc, deadline) do
    session_id = agent_run.session_id
    remaining = deadline - System.monotonic_time(:millisecond)

    if remaining <= 0 do
      stop_session(session_id)
      {:error, {:timeout, session_id}}
    else
      receive do
        {:mitos, ^session_id, {:raw, bytes}} ->
          collect(agent_run, workload, %{acc | raw: [acc.raw, bytes]}, deadline)

        {:mitos, ^session_id, {:prompt, {:final_answer_candidate, text}}} ->
          collect(agent_run, workload, %{acc | candidate: text}, deadline)

        # Headless adapters (e.g. claude.print) report the agent's answer as a
        # typed FINAL_OUTPUT event; it is authoritative over codec candidates.
        {:mitos, ^session_id, {:event, %{"type" => "FINAL_OUTPUT", "text" => text}}}
        when is_binary(text) ->
          collect(agent_run, workload, %{acc | final: text}, deadline)

        {:mitos, ^session_id, {:event, %{"type" => "exit"} = event}} ->
          finish(acc, session_id, Map.get(event, "code", 0))

        {:mitos, ^session_id, {:event, %{"type" => "sidecar_exit"} = event}} ->
          finish(acc, session_id, Map.get(event, "code"))

        {:mitos, ^session_id, {:event, %{"type" => type} = event}} when type in ["ERROR", "error"] ->
          detail = Map.get(event, "detail", Map.get(event, "message", inspect(event)))
          collect(agent_run, workload, %{acc | errors: acc.errors ++ [to_string(detail)]}, deadline)

        {:mitos, ^session_id,
         {:event, %{"type" => "ACTION_REQUESTED", "origin" => "terminal_scrape", "request_id" => request_id}}}
        when workload.approvals == "deny" ->
          # Headless workload runs answer no prompts; denying keeps the run
          # moving to a terminal state instead of hanging until timeout.
          Mitos.Session.deny(agent_run.pid, request_id, "workload runs are non-interactive")
          collect(agent_run, workload, acc, deadline)

        {:mitos, ^session_id, _other} ->
          collect(agent_run, workload, acc, deadline)
      after
        remaining ->
          stop_session(session_id)
          {:error, {:timeout, session_id}}
      end
    end
  end

  defp finish(acc, session_id, exit_code) do
    text = acc.final || acc.candidate || clean(IO.iodata_to_binary(acc.raw))

    case String.trim(text || "") do
      "" ->
        {:error, {:empty_result, session_id, acc.errors}}

      result ->
        {:ok, %{result: result, run_id: session_id, exit_code: exit_code, errors: acc.errors}}
    end
  end

  defp clean(bytes) do
    bytes
    |> String.replace(@ansi, "")
    |> String.replace("\r\n", "\n")
    |> String.replace("\r", "\n")
  end

  defp stop_session(session_id) do
    Mitos.Orchestrator.stop_session(session_id)
  catch
    :exit, _ -> :ok
  end

  defp maybe(opts, _key, nil), do: opts
  defp maybe(opts, key, value), do: Keyword.put(opts, key, value)
end
