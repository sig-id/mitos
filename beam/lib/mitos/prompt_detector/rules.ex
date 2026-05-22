defmodule Mitos.PromptDetector.Rules do
  @moduledoc """
  Marker sets used by `Mitos.PromptDetector` to classify screen text. The
  defaults and the per-provider rules mirror `src/prompt/detectors.zig`
  (`DetectorRules`) and `src/prompt/providers.zig` so the BEAM and Zig runtimes
  agree on what counts as an approval / final answer / error.
  """

  defstruct final_markers: ["Final answer:", "FINAL:"],
            approval_markers: ["? [y/N]", "? [Y/n]", "Do you want to proceed?", "Allow command?"],
            error_markers: ["Error:", "error:", "fatal:", "codex error:"]

  @type t :: %__MODULE__{
          final_markers: [String.t()],
          approval_markers: [String.t()],
          error_markers: [String.t()]
        }

  @doc "Generic deterministic rules (layer 2). Used when a provider has none of its own."
  @spec generic() :: t()
  def generic, do: %__MODULE__{}

  @doc """
  Provider-specific rules, keyed by provider id. Falls back to `generic/0` for
  unknown providers — matching `rulesForProvider` in `src/prompt/providers.zig`.
  """
  @spec for_provider(String.t()) :: t()
  def for_provider("fake") do
    %__MODULE__{
      final_markers: ["FAKE_FINAL:"],
      approval_markers: ["FAKE_APPROVAL:", "? [y/N]"],
      error_markers: ["FAKE_ERROR:", "Error:"]
    }
  end

  def for_provider("codex") do
    %__MODULE__{
      final_markers: ["Final answer:", "FINAL:"],
      approval_markers: ["? [y/N]", "Do you want to proceed?", "Allow command?"],
      error_markers: ["codex error:", "Error:", "error:"]
    }
  end

  def for_provider("claude") do
    %__MODULE__{
      final_markers: ["Final answer:", "Done."],
      approval_markers: ["Do you want to proceed?", "Allow this command?", "? [y/N]"],
      error_markers: ["Claude encountered an error", "Error:"]
    }
  end

  def for_provider("gemini") do
    %__MODULE__{
      final_markers: ["Final answer:", "Response:"],
      approval_markers: ["Allow command?", "Proceed?", "? [y/N]"],
      error_markers: ["Gemini error:", "Error:"]
    }
  end

  def for_provider(_other), do: generic()
end
