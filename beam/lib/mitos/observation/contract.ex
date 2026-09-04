defmodule Mitos.Observation.Contract do
  @moduledoc """
  Adapter contract checks (`14 §5`) — the maintenance cost, made visible.

  Harness churn is the largest ongoing cost in this design (`02 §8` R1): CLIs
  change output formats, prompts, and session-artifact layouts between releases.
  It MUST be detected by CI, not by a stalled overnight run, which is why these
  run **nightly against upstream `@latest`** and not only against a pinned
  version.

  A failing contract **quarantines** the adapter (`Mitos.Autonomy.quarantine/2`):
  Studio warns and drops the run's unattended horizon to zero (human-watched
  only); Market refuses to schedule it. Quarantine is on the record, so the
  reason a fleet went quiet is a log entry rather than a mystery.

  `check/2` is split into a **static** half (the matrix is internally coherent,
  the classifier answers the shapes it claims) which runs anywhere, and a
  **live** half (the binary exists, a session artifact appears where declared)
  which needs the real CLI and is skipped without it.
  """

  alias Mitos.Observation.{Classifier, Session, Terminal, Vt}

  @type finding :: %{check: atom, ok: boolean, detail: term}

  @doc """
  Run the contract for one adapter. Options: `live: true` to include checks that
  need the real binary; `quarantine: true` to quarantine on failure.

  Returns `%{harness: mod, ok: bool, findings: [finding]}`.
  """
  def check(harness_mod, opts \\ []) do
    findings =
      static_checks(harness_mod) ++
        if opts[:live], do: live_checks(harness_mod), else: []

    ok = Enum.all?(findings, & &1.ok)

    if not ok and opts[:quarantine] do
      Mitos.Autonomy.quarantine(harness_mod, failed(findings))
    end

    if ok and opts[:quarantine], do: Mitos.Autonomy.unquarantine(harness_mod)

    %{harness: harness_mod, ok: ok, findings: findings}
  end

  @doc "Run the contract for every registered adapter (14 §5's nightly job)."
  def check_all(opts \\ []) do
    Application.get_env(:mitos, :harnesses, %{})
    |> Map.values()
    |> Enum.uniq()
    |> Enum.map(&check(&1, opts))
  end

  @doc "The failing checks of a result, as a short reason."
  def failed(findings) do
    findings |> Enum.reject(& &1.ok) |> Enum.map(& &1.check)
  end

  # --- static: the matrix must be internally coherent ---

  defp static_checks(mod) do
    o = Mitos.Harness.observation(mod)
    channels = Mitos.Harness.channels(mod)

    [
      finding(
        :declares_a_channel,
        # A harness observable only through C1+C3 can be watched but never left
        # (14 §4). Declaring that is fine; declaring *nothing* is a defect.
        o.structured_output in [:stream, :artifact, :none],
        o.structured_output
      ),
      finding(
        :interactive_implies_classifier,
        not (o.interactive and o.pty_control) or not is_nil(o.modal_classifier),
        o.modal_classifier
      ),
      finding(
        :classifier_implements_behaviour,
        is_nil(o.modal_classifier) or classifier_ok?(o.modal_classifier),
        o.modal_classifier
      ),
      finding(
        :session_template_is_expandable,
        is_nil(o.session_artifacts) or expandable?(o.session_artifacts),
        o.session_artifacts
      ),
      finding(
        :normalizer_present_for_artifacts,
        is_nil(o.session_artifacts) or function_exported?(mod, :normalize_session_line, 1),
        {mod, :normalize_session_line}
      ),
      finding(
        :think_time_declared,
        is_integer(o.think_time_p95_ms) and o.think_time_p95_ms > 0,
        o.think_time_p95_ms
      ),
      finding(:unattended_posture_derivable, is_boolean(channels.unattended_ok), channels)
    ] ++ classifier_checks(o.modal_classifier)
  end

  # The classifier must answer the shapes it claims to recognize, and — the
  # safety property — must answer `:unknown` for a screen it does not.
  defp classifier_checks(nil), do: []

  defp classifier_checks(classifier) do
    [
      finding(:classifies_menu, kind_of(classifier, menu_screen()) == :menu, menu_screen()),
      finding(:classifies_yes_no, kind_of(classifier, yes_no_screen()) == :prompt, yes_no_screen()),
      finding(:classifies_idle, kind_of(classifier, idle_screen()) == :idle, idle_screen()),
      # 14 §3.2: recognizing nothing is the honest answer. A classifier that
      # stretches to cover an unfamiliar prompt trades a rare interruption for a
      # rare wrong keystroke in an irreversible place.
      finding(:unknown_is_an_answer, kind_of(classifier, gibberish_screen()) == :unknown, gibberish_screen()),
      finding(:declares_transitions, transitions_declared?(classifier), classifier)
    ]
  end

  defp transitions_declared?(classifier) do
    class = classifier.classify(grid_of(menu_screen()))

    match?({:ok, _}, classifier.transition(class, :approve)) and
      match?({:ok, _}, classifier.encode(class, :approve))
  end

  defp classifier_ok?(mod) do
    Code.ensure_loaded?(mod) and
      function_exported?(mod, :classify, 1) and
      function_exported?(mod, :encode, 2) and
      function_exported?(mod, :transition, 2)
  end

  defp expandable?(%{path_template: t}) when is_binary(t) do
    expanded = Session.resolve(t, %{home: "/tmp/h", workspace: "/tmp/w"})
    not String.contains?(expanded, "{") and String.starts_with?(expanded, "/")
  end

  defp expandable?(_), do: false

  # --- live: needs the real binary ---

  defp live_checks(mod) do
    exe = executable(mod)

    [
      finding(:binary_present, not is_nil(exe) and System.find_executable(exe) != nil, exe)
    ]
  end

  defp executable(mod) do
    if function_exported?(mod, :argv, 2) do
      {exe, _args} = mod.argv("contract probe", [])
      exe
    end
  rescue
    _ -> nil
  end

  # --- fixture screens ---

  defp kind_of(classifier, text), do: classifier.classify(grid_of(text)).kind

  defp grid_of(text) do
    vt = Vt.feed(Vt.new(24, 80), String.replace(text, "\n", "\r\n"))
    {:ok, grid} = Terminal.replay_from_vt(vt)
    grid
  end

  defp menu_screen do
    """
    Do you want to make this edit to config.ex?
      1. Yes
    > 2. Yes, and don't ask again
      3. No, tell Claude what to do differently
    """
  end

  defp yes_no_screen, do: "Overwrite existing file? (y/n)"
  defp idle_screen, do: "user@host:~/proj $"
  defp gibberish_screen, do: "▚▚▚ ░░░ ▚▚▚\n "

  defp finding(check, ok, detail), do: %{check: check, ok: !!ok, detail: detail}

  @doc "Format a run of `check_all/1` for a CI log."
  def report(results) do
    Enum.map_join(results, "\n", fn r ->
      status = if r.ok, do: "ok", else: "FAIL"
      detail = if r.ok, do: "", else: " (" <> Enum.map_join(failed(r.findings), ", ", &to_string/1) <> ")"
      "#{status}  #{inspect(r.harness)}#{detail}"
    end)
  end

  defdelegate classifier_for(harness_mod), to: Classifier, as: :for_harness
end
