defmodule Mitos.PipelineTest do
  @moduledoc "04 §8: both canonical examples parse; nothing is ever rejected for shape."
  use ExUnit.Case, async: true

  alias Mitos.Pipeline

  @structured """
  # Pipeline: add-feature

  Goal: implement the GitHub issue in `$ISSUE_URL` on this repo, with tests,
  and open a PR. Prefer the smallest change that satisfies the issue.

  ```yaml
  budget: { usd: 8, wall_ms: 3600000 }
  constraints:
    - no force-push, no history rewrite
    - branch from main; never commit to main
    - "network: broker-only (T2)"
  allowed_capabilities: [planner, code-edit, test-runner, pr-author]
  success_criteria:
    - test-runner reports all tests green
    - a PR exists targeting main
  checkpoints: [after:test-runner]
  ```

  ## Stages
  1. **planner** — read the issue + repo, write a short plan. (read-only)
  2. **code-edit** — implement the plan. Iterate until tests pass locally.
  3. **test-runner** — run the suite; on failure feed diffs back to code-edit.
  4. **pr-author** — open a PR summarizing the change and linking the issue.

  Notes: if the issue is ambiguous, request_approval before large edits.
  """

  @prose """
  # Pipeline: brand-landing

  Goal: produce three distinct landing-page design directions for the brief in
  `brief.md`, then refine the one the reviewer picks into a responsive mockup.

  constraints: stay within the brand palette in `/data/brand/tokens.json`.
  budget: 30 USD. allowed: [moodboard, layout-gen, design-critic, mockup-builder].
  success: a reviewer-approved mockup artifact + a one-page rationale.

  Stages (prose):
  First run **moodboard** to fix visual language from the brief and brand tokens.
  Then **layout-gen** three times for three directions. **design-critic** scores
  them against the brief. Finally **mockup-builder** refines the pick.
  """

  test "the structured (yaml-fenced) example parses fully" do
    p = Pipeline.parse(@structured)

    assert p.name == "add-feature"
    assert p.goal =~ "implement the GitHub issue"
    assert p.budget == %{"usd" => 8, "wall_ms" => 3_600_000}
    assert length(p.constraints) == 3
    assert p.allowed_capabilities == ["planner", "code-edit", "test-runner", "pr-author"]
    assert p.checkpoints == ["after:test-runner"]

    assert Enum.map(p.stages, & &1.name) == ["planner", "code-edit", "test-runner", "pr-author"]
    assert hd(p.stages).intent =~ "read the issue"
    assert p.sha256 == Mitos.sha256(@structured)
  end

  test "the prose example still yields goal + ordered stages" do
    p = Pipeline.parse(@prose)

    assert p.name == "brand-landing"
    assert p.goal =~ "three distinct landing-page design directions"

    names = Enum.map(p.stages, & &1.name)
    assert "moodboard" == hd(names)
    assert "mockup-builder" in names
    assert "design-critic" in names
  end

  test "a shapeless doc never fails to parse (04 §8: never rejected for shape)" do
    p = Pipeline.parse("just do the thing, you know the one")
    assert p.stages == []
    assert p.goal == nil
    assert p.body =~ "the thing"
  end
end
