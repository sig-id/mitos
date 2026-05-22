defmodule Mitos.Adapter do
  @moduledoc """
  TTY agent adapters: per-provider knowledge for launching a coding agent CLI
  under an interactive pseudo-terminal.

  The BEAM side of Mitos is the *interactive* runtime — every session it owns
  drives a `mitos-term` PTY sidecar (see `Mitos.Session`). The headless /
  exec-json adapters live on the Zig side (`src/providers/`) and are run by the
  one-shot `mitos` CLI; here we only implement the `:pty` mode, which the
  runtime config (`.agent-runtime.yaml`) lists as each provider's fallback:

      claude: adapter_order: [headless_json, pty]
      codex:  adapter_order: [exec_json, pty]
      gemini: adapter_order: [headless_json, pty]

  An adapter is a pure module: it does not own a process. It answers two
  questions for a provider —

    * **how to launch it** — `c:build/2` returns a `Mitos.Adapter.Command`
      (argv, env, optional initial stdin) mirroring the Zig `buildPtyCommand`
      for that provider; and
    * **how to read it** — `c:prompt_rules/0` returns the
      `Mitos.PromptDetector.Rules` used to turn its screen text into semantic
      prompt events.

  `Mitos.Agent` wires an adapter to a live `Mitos.Session` plus a
  `Mitos.Session.PromptWatcher`; `Mitos.Adapter.Registry` resolves a provider
  name + adapter-order to an adapter module.
  """

  alias Mitos.Adapter.Command

  defmodule Task do
    @moduledoc "A unit of work handed to an adapter: the prompt text plus optional files."
    @enforce_keys [:text]
    defstruct text: nil, files: []

    @type t :: %__MODULE__{text: String.t(), files: [String.t()]}
  end

  defmodule Command do
    @moduledoc """
    A resolved launch plan for one PTY session. `argv`/`env`/`cwd`/`cols`/
    `rows`/`idle_ms` feed straight into `Mitos.Orchestrator.start_session/1`.

    `initial_input` is bytes written to the agent's stdin immediately after the
    PTY opens — used by REPL-style agents (e.g. gemini) that take the task
    interactively rather than as an argv positional. `prompt_rules` travels with
    the command so the `PromptWatcher` can be configured from one place.
    """
    @enforce_keys [:provider, :adapter_id, :argv, :prompt_rules]
    defstruct [
      :provider,
      :adapter_id,
      :argv,
      :prompt_rules,
      mode: :pty,
      env: nil,
      cwd: nil,
      cols: 80,
      rows: 24,
      idle_ms: 250,
      initial_input: nil
    ]

    @type t :: %__MODULE__{
            provider: String.t(),
            adapter_id: String.t(),
            argv: [String.t()],
            prompt_rules: Mitos.PromptDetector.Rules.t(),
            mode: :pty,
            env: nil | map() | [{String.t(), String.t()}],
            cwd: nil | String.t(),
            cols: pos_integer(),
            rows: pos_integer(),
            idle_ms: non_neg_integer(),
            initial_input: nil | binary()
          }
  end

  @typedoc "Adapter execution mode. Only `:pty` is implemented BEAM-side."
  @type mode :: :pty

  @doc "Provider id this adapter serves, e.g. `\"claude\"`. Matches the Zig `ProviderId`."
  @callback provider() :: String.t()

  @doc "Stable adapter id, e.g. `\"claude.pty\"`. Matches the Zig `AdapterId`."
  @callback adapter_id() :: String.t()

  @doc "Execution mode. Always `:pty` for these adapters."
  @callback mode() :: mode()

  @doc """
  Build the launch `Command` for `task`.

  `opts` is the keyword list passed to `Mitos.Agent.start/1`; adapters read
  `:cwd`, `:cols`, `:rows`, `:idle_ms`, `:env` from it and may default or
  override them. Returns `{:ok, command}` or `{:error, reason}`.
  """
  @callback build(task :: Task.t(), opts :: keyword()) ::
              {:ok, Command.t()} | {:error, term()}

  @doc "Prompt-detection rules for this provider's screen text."
  @callback prompt_rules() :: Mitos.PromptDetector.Rules.t()

  @doc """
  Build a `Command` for `module` from `opts`, filling provider/adapter metadata
  and the launch defaults so individual adapters only specify what's unique
  (`:argv`, optional `:initial_input`).
  """
  @spec build_command(module(), keyword(), keyword()) :: Command.t()
  def build_command(module, opts, fields) do
    %Command{
      provider: module.provider(),
      adapter_id: module.adapter_id(),
      mode: module.mode(),
      prompt_rules: module.prompt_rules(),
      argv: Keyword.fetch!(fields, :argv),
      initial_input: Keyword.get(fields, :initial_input),
      env: opts[:env],
      cwd: opts[:cwd],
      cols: Keyword.get(opts, :cols, 80),
      rows: Keyword.get(opts, :rows, 24),
      idle_ms: Keyword.get(opts, :idle_ms, 250)
    }
  end
end
