defmodule Mitos.Observation.Classifier do
  @moduledoc """
  The bounded question C3 is allowed to answer (14 §3.2): *what is this terminal
  waiting for, right now* — a closed classification, never open-ended content
  extraction.

  Three rules make this safe:

    * The output type is closed. A classifier returns one of
      `:idle | :prompt | :menu | :input | :busy | :error | :unknown`.
    * **`:unknown` is an answer.** It escalates to a human (`15`); it never
      guesses. A guess converts a display change into an unlogged authority
      decision.
    * A classifier also declares what each steer command should *do* —
      `encode/2` and `transition/2` — so `Mitos.Steer` can verify the outcome
      rather than fire and forget (14 §3.3).

  Classifiers are per-harness, versioned with the adapter, and covered by
  nightly contract tests against upstream `@latest` (14 §5). A classifier that
  fails its contract quarantines the adapter.
  """

  alias Mitos.Observation.Terminal.Grid

  @type kind :: :idle | :prompt | :menu | :input | :busy | :error | :unknown

  @type classification :: %{
          kind: kind,
          options: [%{id: String.t(), label: String.t()}],
          selected: non_neg_integer | nil,
          prompt_text: String.t() | nil,
          digest: String.t()
        }

  @doc "Classify a stable grid. MUST return `:unknown` rather than guess."
  @callback classify(Grid.t()) :: classification

  @doc "Bytes to write for a steer command in this modal state (14 §3.3)."
  @callback encode(classification, command :: term) :: {:ok, binary} | :unsupported

  @doc """
  What the after-grid must look like for the command to have applied. Returning
  `:unknown` means the classifier cannot predict it — `Mitos.Steer` then refuses
  the write rather than performing an unverifiable one.
  """
  @callback transition(classification, command :: term) :: {:ok, expectation :: term} | :unknown

  @doc "The vocabulary a classification can carry."
  def kinds, do: [:idle, :prompt, :menu, :input, :busy, :error, :unknown]

  @doc "A classification that says nothing — the safe default (14 §3.2)."
  def unknown(%Grid{} = grid),
    do: %{kind: :unknown, options: [], selected: nil, prompt_text: nil, digest: grid.digest}

  @doc """
  Does the after-state satisfy the expectation `transition/2` declared?

  Expectations are deliberately coarse — `{:kind, k}`, `{:not_kind, k}`,
  `:changed`, `:unchanged` — because a fine-grained prediction would be one more
  thing to get wrong about a build of a harness we do not control.
  """
  def satisfied?(:changed, before_grid, after_grid, _after_class),
    do: before_grid.digest != after_grid.digest

  def satisfied?(:unchanged, before_grid, after_grid, _after_class),
    do: before_grid.digest == after_grid.digest

  def satisfied?({:kind, k}, _b, _a, after_class), do: after_class.kind == k
  def satisfied?({:not_kind, k}, _b, _a, after_class), do: after_class.kind != k

  def satisfied?({:any, exps}, b, a, ac),
    do: Enum.any?(exps, &satisfied?(&1, b, a, ac))

  def satisfied?({:all, exps}, b, a, ac),
    do: Enum.all?(exps, &satisfied?(&1, b, a, ac))

  @doc """
  Does this expectation require the screen to move? `Mitos.Steer` uses it to
  decide whether its after-read may accept the pre-write grid: when a change is
  expected, an identical grid means the response has not arrived yet, not that
  the command did nothing (`14 §3.3`).
  """
  def entails_change?(:changed), do: true
  def entails_change?({:all, exps}), do: Enum.any?(exps, &entails_change?/1)
  def entails_change?({:any, exps}), do: Enum.all?(exps, &entails_change?/1)
  def entails_change?(_), do: false

  @doc "Resolve the classifier module for a harness adapter (14 §4 observation matrix)."
  def for_harness(harness_mod) do
    case harness_mod.observation() do
      %{modal_classifier: nil} -> __MODULE__.Heuristic
      %{modal_classifier: mod} -> mod
      _ -> __MODULE__.Heuristic
    end
  rescue
    UndefinedFunctionError -> __MODULE__.Heuristic
  end
end

defmodule Mitos.Observation.Classifier.Heuristic do
  @moduledoc """
  The default modal classifier (14 §3.2) — the shapes essentially every TUI
  permission prompt takes, and nothing else.

  It is deliberately conservative. Anything it does not recognize is
  `:unknown`, which escalates to a human rather than producing a keystroke on a
  screen nobody understood. A classifier that stretched to cover an unfamiliar
  prompt would be trading a rare interruption for a rare wrong answer in an
  irreversible place; that trade is never worth taking.

  A harness with a distinctive TUI ships its own classifier and declares it in
  `observation.modal_classifier` (14 §4).
  """

  @behaviour Mitos.Observation.Classifier

  alias Mitos.Observation.Classifier
  alias Mitos.Observation.Terminal.Grid

  # Cursor/selection markers TUIs use for the highlighted row.
  @markers ["❯", "›", ">", "*", "→"]
  # Braille/ellipsis spinners are distinctive enough to match anywhere. The
  # ASCII ones are not: a "/" appears in every shell path, so they only count as
  # a spinner when they LEAD the line. (Found by the 14 §5 contract check, which
  # is the point of having one.)
  @spinners ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⣹", "⣺", "▪", "…"]
  @ascii_spinner ~r/^[|\/\-\\]\s/u

  @impl true
  def classify(%Grid{} = grid) do
    lines = grid |> Grid.content() |> Enum.take(-12)
    base = Classifier.unknown(grid)

    cond do
      lines == [] -> %{base | kind: :idle}
      error?(lines) -> %{base | kind: :error, prompt_text: List.last(lines)}
      true -> classify_tail(base, lines)
    end
  end

  defp classify_tail(base, lines) do
    options = menu_options(lines)

    cond do
      options != [] ->
        %{base | kind: :menu, options: options, selected: selected_index(lines, options), prompt_text: question(lines)}

      yes_no?(lines) ->
        %{
          base
          | kind: :prompt,
            options: [%{id: "yes", label: "yes"}, %{id: "no", label: "no"}],
            prompt_text: List.last(lines)
        }

      free_input?(lines) ->
        %{base | kind: :input, prompt_text: List.last(lines)}

      busy?(lines) ->
        %{base | kind: :busy}

      shell_idle?(lines) ->
        %{base | kind: :idle}

      # Recognizing nothing is the honest answer, and it is an escalation, not
      # a failure (14 §3.2).
      true ->
        base
    end
  end

  @impl true
  def encode(%{kind: :menu, options: options}, {:select, id}) do
    case Enum.find_index(options, &(&1.id == to_string(id))) do
      nil -> :unsupported
      idx -> {:ok, String.duplicate("\e[B", idx) <> "\r"}
    end
  end

  def encode(%{kind: :menu} = c, :approve), do: encode(c, {:select, first_option_id(c)})
  def encode(%{kind: :prompt}, :approve), do: {:ok, "y\r"}
  def encode(%{kind: :prompt}, :deny), do: {:ok, "n\r"}
  def encode(%{kind: :input}, {:answer, text}), do: {:ok, to_string(text) <> "\r"}
  def encode(%{kind: :idle}, {:prompt, text}), do: {:ok, to_string(text) <> "\r"}
  def encode(_class, :interrupt), do: {:ok, "\e"}
  def encode(_class, _command), do: :unsupported

  @impl true
  # After answering a modal prompt the terminal must leave that modal state —
  # a coarse but reliable prediction. Anything finer would be a guess about a
  # build of a harness mitos does not control.
  def transition(%{kind: k}, command)
      when k in [:menu, :prompt, :input] and
             (command == :approve or command == :deny or
                (is_tuple(command) and elem(command, 0) in [:select, :answer])),
      do: {:ok, {:all, [{:not_kind, k}, :changed]}}

  def transition(%{kind: :idle}, {:prompt, _}), do: {:ok, :changed}
  def transition(_class, :interrupt), do: {:ok, :changed}
  def transition(_class, _command), do: :unknown

  # --- shape recognizers ---

  # "1. Yes", "2) No", "❯ Yes" — a numbered or marked option list.
  defp menu_options(lines) do
    lines
    |> Enum.map(&option_of/1)
    |> Enum.reject(&is_nil/1)
    |> Enum.uniq_by(& &1.id)
  end

  defp option_of(line) do
    line = strip_marker(line)

    case Regex.run(~r/^\s*(\d+)[.)]\s+(\S.*)$/u, line) do
      [_, n, label] -> %{id: n, label: String.trim(label)}
      nil -> nil
    end
  end

  defp selected_index(lines, options) do
    marked =
      lines
      |> Enum.filter(&marked?/1)
      |> Enum.map(&option_of/1)
      |> Enum.reject(&is_nil/1)
      |> List.first()

    marked && Enum.find_index(options, &(&1.id == marked.id))
  end

  defp marked?(line), do: Enum.any?(@markers, &String.starts_with?(String.trim_leading(line), &1))

  defp strip_marker(line) do
    trimmed = String.trim_leading(line)

    case Enum.find(@markers, &String.starts_with?(trimmed, &1)) do
      nil -> line
      m -> trimmed |> String.replace_prefix(m, "") |> String.trim_leading()
    end
  end

  defp question(lines) do
    Enum.find(lines, &String.contains?(&1, "?")) || List.first(lines)
  end

  defp yes_no?(lines) do
    last = List.last(lines) || ""
    Regex.match?(~r/\((y(es)?\/n(o)?|Y\/n|y\/N)\)|\[(y\/N|Y\/n|y\/n)\]/iu, last)
  end

  defp free_input?(lines) do
    last = String.trim_trailing(List.last(lines) || "")
    Regex.match?(~r/[?:>]\s*$/u, last) and String.length(last) > 2 and not shell_prompt?(last)
  end

  defp busy?(lines) do
    last = List.last(lines) || ""
    Enum.any?(@spinners, &String.contains?(last, &1)) or Regex.match?(@ascii_spinner, String.trim_leading(last))
  end

  defp shell_idle?(lines), do: shell_prompt?(String.trim_trailing(List.last(lines) || ""))

  defp shell_prompt?(line), do: Regex.match?(~r/[$#%>]\s*$/u, line) and String.length(line) < 80

  defp error?(lines) do
    Enum.any?(Enum.take(lines, -3), &Regex.match?(~r/^\s*(error|fatal|panic|traceback)\b/iu, &1))
  end

  defp first_option_id(%{options: [%{id: id} | _]}), do: id
  defp first_option_id(_), do: nil
end
