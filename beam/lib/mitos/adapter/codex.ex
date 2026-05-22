defmodule Mitos.Adapter.Codex do
  @moduledoc """
  TTY adapter for the `codex` CLI. Mirrors `buildPtyCommand` in
  `src/providers/codex.zig`: interactive codex is launched without its alt
  screen (so the byte stream stays on the main buffer where the terminal core
  and prompt detector can read it) and pinned to the workspace dir.

      codex --no-alt-screen --cd <cwd> <task>

  `--cd` needs a concrete directory; when the caller gives no `:cwd` we fall
  back to `"."` (resolved against the sidecar's working directory).
  """
  @behaviour Mitos.Adapter

  alias Mitos.Adapter
  alias Mitos.Adapter.Task
  alias Mitos.PromptDetector.Rules

  @impl true
  def provider, do: "codex"

  @impl true
  def adapter_id, do: "codex.pty"

  @impl true
  def mode, do: :pty

  @impl true
  def build(%Task{text: text}, opts) do
    cwd = opts[:cwd] || "."
    argv = ["codex", "--no-alt-screen", "--cd", cwd, text]
    {:ok, Adapter.build_command(__MODULE__, opts, argv: argv)}
  end

  @impl true
  def prompt_rules, do: Rules.for_provider("codex")
end
