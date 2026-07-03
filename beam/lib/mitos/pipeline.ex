defmodule Mitos.Pipeline do
  @moduledoc """
  Pipeline intent in markdown, loose format (04 §8). A pipeline file is a doc
  the orchestrator *reads*: intent may be a fenced YAML block, a numbered
  stages list, or prose. There is **no rigid schema validator** — tooling MAY
  lint and warn, but MUST NOT reject a pipeline for shape. This parser
  extracts the SHOULD-fields of 04 §8.1 on a best-effort basis and always
  succeeds; whatever it cannot extract stays available to the orchestrator as
  the raw `body`.
  """

  defstruct name: nil,
            goal: nil,
            # %{"usd" => _, "tokens" => _, "wall_ms" => _}
            budget: %{},
            constraints: [],
            allowed_capabilities: [],
            success_criteria: [],
            checkpoints: [],
            # [%{name, intent}] in declared order
            stages: [],
            body: nil,
            sha256: nil

  @type t :: %__MODULE__{}

  @doc "Parse a pipeline markdown body. Never fails; missing fields stay defaults."
  def parse(body) when is_binary(body) do
    yaml = merged_yaml(body)

    %__MODULE__{
      name: parse_name(body),
      goal: parse_goal(body),
      budget: normalize_budget(yaml["budget"]),
      constraints: List.wrap(yaml["constraints"]),
      allowed_capabilities: List.wrap(yaml["allowed_capabilities"] || yaml["allowed"]),
      success_criteria: List.wrap(yaml["success_criteria"] || yaml["success"]),
      checkpoints: List.wrap(yaml["checkpoints"]),
      stages: parse_stages(body),
      body: body,
      sha256: Mitos.sha256(body)
    }
  end

  def parse!(body), do: parse(body)

  # --- extraction (each tolerant, each optional) ---

  defp parse_name(body) do
    case Regex.run(~r/^#\s*Pipeline:\s*(.+)$/m, body) do
      [_, name] -> String.trim(name)
      _ ->
        case Regex.run(~r/^#\s+(.+)$/m, body) do
          [_, name] -> String.trim(name)
          _ -> nil
        end
    end
  end

  # `goal` is the one near-required field (04 §8.1) — but even it is extracted,
  # not validated; the orchestrator decides what to do about its absence.
  defp parse_goal(body) do
    case Regex.run(~r/^Goal:\s*(.+?)(?:\n\s*\n|\n```|\z)/ms, body) do
      [_, goal] -> goal |> String.replace(~r/\s+/, " ") |> String.trim()
      _ -> nil
    end
  end

  # Merge every yaml fence in the doc (04 §8.2 uses one; prose docs may use none).
  defp merged_yaml(body) do
    ~r/```ya?ml\n(.*?)```/ms
    |> Regex.scan(body, capture: :all_but_first)
    |> Enum.reduce(%{}, fn [block], acc ->
      case YamlElixir.read_from_string(block) do
        {:ok, %{} = m} -> Map.merge(acc, m)
        _ -> acc
      end
    end)
  end

  defp normalize_budget(nil), do: %{}

  defp normalize_budget(%{} = b),
    do: Map.new(b, fn {k, v} -> {to_string(k), v} end)

  defp normalize_budget(n) when is_number(n), do: %{"usd" => n}
  defp normalize_budget(_), do: %{}

  # Stages: a numbered list under a "Stages" heading — `1. **name** — intent`.
  # Prose fallback: bold capability names in the stages section, in order.
  defp parse_stages(body) do
    section = stages_section(body)

    numbered =
      ~r/^\s*\d+\.\s+\*\*([^*]+)\*\*\s*[—–-]?\s*(.*)$/mu
      |> Regex.scan(section)
      |> Enum.map(fn [_, name, intent] ->
        %{name: String.trim(name), intent: String.trim(intent)}
      end)

    if numbered != [] do
      numbered
    else
      ~r/\*\*([a-zA-Z0-9_-]+)\*\*/
      |> Regex.scan(section, capture: :all_but_first)
      |> Enum.map(fn [name] -> %{name: name, intent: ""} end)
      |> Enum.uniq_by(& &1.name)
    end
  end

  defp stages_section(body) do
    case Regex.run(~r/^##?#?\s*Stages.*?$(.*?)(?:^##?\s|\z)/ms, body) do
      [_, section] -> section
      _ ->
        case Regex.run(~r/^Stages\s*\(?.*?\)?:?\s*$(.*)\z/ms, body) do
          [_, section] -> section
          _ -> ""
        end
    end
  end
end
