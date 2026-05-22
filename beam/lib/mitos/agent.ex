defmodule Mitos.Agent do
  @moduledoc """
  High-level entry point for running an AI coding agent over a TTY.

  Ties the three layers together: resolve a provider to a TTY adapter
  (`Mitos.Adapter.Registry`), build its launch command (`Mitos.Adapter`), start
  the PTY session (`Mitos.Orchestrator`), deliver any initial stdin, and attach
  a `Mitos.Session.PromptWatcher` so the session's screens become semantic
  prompt events.

      {:ok, run} = Mitos.Agent.start(provider: "claude", task: "Summarize this repo", cwd: "/work")
      run.session_id   #=> "sess-…"
      run.adapter      #=> "claude.pty"

  Subscribers (`Mitos.PubSub.subscribe(run.session_id)`) then receive the usual
  `{:mitos, id, {:raw|:event|:snapshot, …}}` stream plus
  `{:mitos, id, {:prompt, event}}` for detected approvals / final answers /
  errors.

  Options:

    * `:provider` (required) — provider id, e.g. `"claude"`
    * `:task` (required) — task text
    * `:files` — list of file paths attached to the task (default `[]`)
    * `:adapter_order` — modes to try (default `Mitos.Adapter.Registry.default_order/0`)
    * `:cwd`, `:cols`, `:rows`, `:idle_ms`, `:env` — passed to the adapter/session
    * `:argv` — required only for the `"generic"` provider
    * `:session_id` — pin the session id (default: generated)
    * `:detect` — attach the prompt watcher (default `true`)
  """
  alias Mitos.Adapter
  alias Mitos.Adapter.{Registry, Task}
  alias Mitos.Orchestrator
  alias Mitos.Session
  alias Mitos.Session.PromptWatcher

  @type run :: %{
          session_id: String.t(),
          pid: pid(),
          provider: String.t(),
          adapter: String.t(),
          mode: Adapter.mode()
        }

  @spec start(keyword()) :: {:ok, run()} | {:error, term()}
  def start(opts) do
    provider = Keyword.fetch!(opts, :provider)
    task_text = Keyword.fetch!(opts, :task)
    order = Keyword.get(opts, :adapter_order, Registry.default_order())
    task = %Task{text: task_text, files: Keyword.get(opts, :files, [])}

    with {:ok, module, mode} <- Registry.select(provider, order),
         {:ok, command} <- module.build(task, opts),
         {:ok, %{pid: pid, session_id: session_id}} <-
           Orchestrator.start_session(session_opts(command, opts)) do
      if command.initial_input, do: Session.write_stdin(pid, command.initial_input)

      if Keyword.get(opts, :detect, true) do
        start_watcher(session_id, command.prompt_rules)
      end

      {:ok,
       %{
         session_id: session_id,
         pid: pid,
         provider: provider,
         adapter: module.adapter_id(),
         mode: mode
       }}
    end
  end

  # Translate a resolved Command into Orchestrator.start_session/1 options,
  # dropping nils so the Session's own defaults apply.
  defp session_opts(command, opts) do
    [
      argv: command.argv,
      env: command.env,
      cwd: command.cwd,
      cols: command.cols,
      rows: command.rows,
      idle_ms: command.idle_ms,
      session_id: opts[:session_id]
    ]
    |> Enum.reject(fn {_k, v} -> is_nil(v) end)
  end

  defp start_watcher(session_id, rules) do
    DynamicSupervisor.start_child(
      Mitos.Watchers,
      {PromptWatcher, session_id: session_id, rules: rules}
    )
  end
end
