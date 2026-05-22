defmodule Mitos.PromptDetector do
  @moduledoc """
  Turns terminal screen text into semantic prompt events. The BEAM-side
  counterpart of `src/prompt/detectors.zig`; the marker rules per provider
  match `src/prompt/providers.zig` so the two runtimes classify the same
  screens identically.

  Detection is deterministic and pattern-based (layer 1 + 2 of the design's
  detection stack). It never injects terminal input — it only emits events for
  the orchestration brain / approval controller to act on.

  Events (mirroring the Zig `PromptEvent` union):

      {:approval_requested, %{text: String.t(), risk: :low | :medium | :high, action: action | nil}}
      {:question_asked, %{text: String.t()}}
      {:final_answer_candidate, String.t()}
      {:error_detected, String.t()}
      :idle_detected

  where `action` is `nil`, `{:install_package, %{manager: ..., package: ...}}`,
  or `{:git_push, %{remote: ..., branch: ...}}`.
  """

  alias Mitos.PromptDetector.Rules

  @type risk :: :low | :medium | :high

  @type action ::
          {:install_package, %{manager: String.t(), package: String.t()}}
          | {:git_push, %{remote: String.t(), branch: String.t()}}

  @type event ::
          {:approval_requested, %{text: String.t(), risk: risk(), action: action() | nil}}
          | {:question_asked, %{text: String.t()}}
          | {:final_answer_candidate, String.t()}
          | {:error_detected, String.t()}
          | :idle_detected

  @doc """
  Detect prompt events in `text` using `rules`.

  Mirrors `detectWithRules`: an approval marker and a plain question are
  mutually exclusive (approval wins); a final-answer marker and an error
  marker may each add one further event. Returns events in
  approval/question → final → error order.
  """
  @spec detect(String.t(), Rules.t()) :: [event()]
  def detect(text, %Rules{} = rules) when is_binary(text) do
    leading =
      cond do
        first_marker(text, rules.approval_markers) != nil ->
          [{:approval_requested, %{text: text, risk: risk(text), action: infer_action(text)}}]

        looks_like_question?(text) ->
          [{:question_asked, %{text: text}}]

        true ->
          []
      end

    final =
      case first_marker(text, rules.final_markers) do
        nil -> []
        marker -> [{:final_answer_candidate, String.trim(after_marker(text, marker))}]
      end

    error =
      case first_marker(text, rules.error_markers) do
        nil -> []
        marker -> [{:error_detected, String.trim(after_marker(text, marker))}]
      end

    leading ++ final ++ error
  end

  @doc """
  Reassemble the visible screen text from a sidecar SNAPSHOT map.

  The snapshot's `"cells"` are a flat list of `%{"x" => col, "y" => row,
  "t" => text}` maps (see `emitSnapshotFrame` in `src/sidecar/sidecar.zig`).
  We group by row, order by column, concatenate the cell text, and join rows
  with newlines — trailing blank columns/rows are trimmed.
  """
  @spec visible_text(map()) :: String.t()
  def visible_text(%{"cells" => cells}) when is_list(cells) do
    cells
    |> Enum.group_by(&Map.get(&1, "y", 0))
    |> Enum.sort_by(fn {y, _} -> y end)
    |> Enum.map_join("\n", fn {_y, row_cells} ->
      row_cells
      |> Enum.sort_by(&Map.get(&1, "x", 0))
      |> Enum.map_join("", &Map.get(&1, "t", ""))
      |> String.trim_trailing()
    end)
    |> String.trim_trailing()
  end

  def visible_text(_), do: ""

  # -- internals ------------------------------------------------------------

  defp first_marker(text, markers) do
    Enum.find(markers, fn marker -> String.contains?(text, marker) end)
  end

  defp after_marker(text, marker) do
    case String.split(text, marker, parts: 2) do
      [_, rest] -> rest
      [whole] -> whole
    end
  end

  defp looks_like_question?(text) do
    text |> String.trim_trailing(" \t\r\n") |> String.ends_with?("?")
  end

  @high_risk ["rm -rf", "delete", "secret", ".ssh", ".aws"]
  @medium_risk ["install", "network", "push", "write"]

  defp risk(text) do
    cond do
      contains_any?(text, @high_risk) -> :high
      contains_any?(text, @medium_risk) -> :medium
      true -> :low
    end
  end

  defp infer_action(text) do
    cond do
      String.contains?(text, "npm install") ->
        {:install_package, %{manager: "npm", package: "*"}}

      String.contains?(text, "git push") ->
        {:git_push, %{remote: "origin", branch: "unknown"}}

      true ->
        nil
    end
  end

  defp contains_any?(text, needles), do: Enum.any?(needles, &String.contains?(text, &1))
end
