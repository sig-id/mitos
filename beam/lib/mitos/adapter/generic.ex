defmodule Mitos.Adapter.Generic do
  @moduledoc """
  Universal PTY adapter (`generic-pty.terminal` in `src/providers/pty.zig`):
  the caller supplies the argv directly and the task text — if any — is written
  to stdin once the PTY opens. Use this to drive any interactive command Mitos
  has no provider-specific knowledge of.

  Requires `:argv` in opts (a non-empty list of strings).
  """
  @behaviour Mitos.Adapter

  alias Mitos.Adapter
  alias Mitos.Adapter.Task
  alias Mitos.PromptDetector.Rules

  @impl true
  def provider, do: "generic"

  @impl true
  def adapter_id, do: "generic-pty.terminal"

  @impl true
  def mode, do: :pty

  @impl true
  def build(%Task{text: text}, opts) do
    case opts[:argv] do
      [_ | _] = argv ->
        initial = if text in [nil, ""], do: nil, else: text
        {:ok, Adapter.build_command(__MODULE__, opts, argv: argv, initial_input: initial)}

      _ ->
        {:error, :missing_argv}
    end
  end

  @impl true
  def prompt_rules, do: Rules.generic()
end
