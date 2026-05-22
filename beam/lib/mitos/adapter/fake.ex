defmodule Mitos.Adapter.Fake do
  @moduledoc """
  Fake TTY adapter for integration tests — the BEAM analogue of
  `src/providers/fake.zig`. Instead of a real agent it launches a tiny shell
  script under the PTY that echoes the task and emits a `FAKE_FINAL:` marker,
  so the full path (adapter → session → sidecar → prompt watcher) can be
  exercised without a provider CLI installed.

  The task text is passed via the `MITOS_TASK` env var rather than spliced into
  the script, so no shell-quoting hazard.
  """
  @behaviour Mitos.Adapter

  alias Mitos.Adapter
  alias Mitos.Adapter.Task
  alias Mitos.PromptDetector.Rules

  # \r\n line endings: this runs under a PTY in cooked mode, where the screen
  # expects carriage-return + line-feed.
  # The trailing sleep keeps the PTY open long enough after the final marker
  # for an idle event (and the watcher's snapshot) to land before exit.
  @script ~S(printf 'FakeAgent\r\n'; printf 'FAKE_FINAL: %s\r\n' "$MITOS_TASK"; sleep 0.3)

  @impl true
  def provider, do: "fake"

  @impl true
  def adapter_id, do: "fake.pty"

  @impl true
  def mode, do: :pty

  @impl true
  def build(%Task{text: text}, opts) do
    env = [{"MITOS_TASK", text} | normalize_env(opts[:env])]
    opts = Keyword.put(opts, :env, env)
    {:ok, Adapter.build_command(__MODULE__, opts, argv: ["/bin/sh", "-c", @script])}
  end

  @impl true
  def prompt_rules, do: Rules.for_provider("fake")

  defp normalize_env(nil), do: []
  defp normalize_env(env) when is_list(env), do: env
  defp normalize_env(env) when is_map(env), do: Map.to_list(env)
end
