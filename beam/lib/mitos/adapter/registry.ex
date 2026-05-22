defmodule Mitos.Adapter.Registry do
  @moduledoc """
  Resolves a provider name + adapter-order to a TTY adapter module — the
  BEAM-side counterpart of `AdapterRegistry.select` in
  `src/adapters/registry.zig`.

  Because this runtime only implements interactive `:pty` adapters, selection
  walks the requested `adapter_order` and picks the first mode for which the
  provider has a registered adapter. Given a config order like
  `[:headless_json, :pty]`, the headless modes are simply skipped here and the
  `:pty` fallback is chosen — exactly the role the BEAM plays in the stack.

  Errors mirror the Zig `SelectionError`:

    * `{:error, :provider_not_registered}` — no adapter for this provider
    * `{:error, :adapter_unavailable}` — provider known, but none of the
      requested modes are supported here
  """

  alias Mitos.Adapter.{Claude, Codex, Fake, Gemini, Generic}

  # provider id => [{mode, adapter_module}], in preference order within a mode.
  @adapters %{
    "fake" => [{:pty, Fake}],
    "claude" => [{:pty, Claude}],
    "codex" => [{:pty, Codex}],
    "gemini" => [{:pty, Gemini}],
    "generic" => [{:pty, Generic}],
    "generic-pty" => [{:pty, Generic}]
  }

  @default_order [:native_api, :headless_json, :exec_json, :pty, :raw_shell]

  @doc "Provider ids with a registered TTY adapter."
  @spec providers() :: [String.t()]
  def providers, do: Map.keys(@adapters)

  @doc """
  Default adapter order. Lists every mode so config orders pass through
  unchanged; only `:pty` actually resolves here, as the last-resort fallback.
  """
  @spec default_order() :: [Mitos.Adapter.mode()]
  def default_order, do: @default_order

  @doc """
  Select an adapter for `provider` honoring `order` (a list of modes).

  Returns `{:ok, module, mode}`, `{:error, :provider_not_registered}`, or
  `{:error, :adapter_unavailable}`.
  """
  @spec select(String.t(), [atom()]) ::
          {:ok, module(), Mitos.Adapter.mode()}
          | {:error, :provider_not_registered | :adapter_unavailable}
  def select(provider, order \\ @default_order) when is_binary(provider) do
    case Map.fetch(@adapters, provider) do
      :error ->
        {:error, :provider_not_registered}

      {:ok, entries} ->
        order
        |> Enum.find_value(fn mode ->
          case List.keyfind(entries, mode, 0) do
            {^mode, module} -> {:ok, module, mode}
            nil -> nil
          end
        end)
        |> case do
          nil -> {:error, :adapter_unavailable}
          found -> found
        end
    end
  end
end
