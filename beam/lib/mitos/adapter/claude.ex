defmodule Mitos.Adapter.Claude do
  @moduledoc """
  TTY adapter for Anthropic's `claude` CLI. Mirrors `buildPtyCommand` in
  `src/providers/claude.zig`: the interactive form passes the task as a
  positional after `--` so flags in the task text aren't reinterpreted.

      claude -- <task>
  """
  @behaviour Mitos.Adapter

  alias Mitos.Adapter
  alias Mitos.Adapter.Task
  alias Mitos.PromptDetector.Rules

  @impl true
  def provider, do: "claude"

  @impl true
  def adapter_id, do: "claude.pty"

  @impl true
  def mode, do: :pty

  @impl true
  def build(%Task{text: text}, opts) do
    {:ok, Adapter.build_command(__MODULE__, opts, argv: ["claude", "--", text])}
  end

  @impl true
  def prompt_rules, do: Rules.for_provider("claude")
end
