defmodule Mitos.Adapter.Gemini do
  @moduledoc """
  TTY adapter for the `gemini` CLI.

  Unlike claude/codex, `src/providers/gemini.zig` only ships a headless
  adapter — gemini's interactive CLI takes its prompt through the TUI rather
  than as an argv positional. So the PTY form here launches the bare REPL and
  delivers the task as the first line of stdin (`initial_input`) once the PTY
  is open:

      gemini            # then write "<task>\\r" to stdin

  This is the universal-fallback shape (`adapter_order: [headless_json, pty]`):
  when gemini's structured mode is unavailable, drive it like a human would.
  """
  @behaviour Mitos.Adapter

  alias Mitos.Adapter
  alias Mitos.Adapter.Task
  alias Mitos.PromptDetector.Rules

  @impl true
  def provider, do: "gemini"

  @impl true
  def adapter_id, do: "gemini.pty"

  @impl true
  def mode, do: :pty

  @impl true
  def build(%Task{text: text}, opts) do
    {:ok, Adapter.build_command(__MODULE__, opts, argv: ["gemini"], initial_input: text <> "\r")}
  end

  @impl true
  def prompt_rules, do: Rules.for_provider("gemini")
end
