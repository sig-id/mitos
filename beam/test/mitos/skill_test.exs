defmodule Mitos.SkillTest do
  use ExUnit.Case, async: true

  alias Mitos.Capability.Skill

  @anchored """
  <!-- @skill:role -->
  Senior engineer.

  <!-- @skill:rules -->
  - Match existing patterns.

  <!-- @skill:procedure -->
  1. Read. 2. Implement.

  <!-- @skill:success -->
  Tests pass.
  """

  test "identity law: base ⊕ ∅ = base (anchored)" do
    {resolved, sha} = Skill.resolve(@anchored, nil, "opus")
    assert resolved == @anchored
    assert sha == Mitos.sha256(@anchored)
  end

  test "identity law: base ⊕ ∅ = base (plain markdown, no anchors)" do
    plain = "Just do the thing.\nCarefully.\n"
    {resolved, _} = Skill.resolve(plain, "", "opus")
    assert resolved == plain
  end

  test "replace swaps a section body" do
    overlay = "@adapt replace skill:procedure\n1. Plan. 2. Read. 3. Implement. 4. Verify."
    {resolved, _} = Skill.resolve(@anchored, overlay, "opus")
    assert resolved =~ "1. Plan. 2. Read. 3. Implement. 4. Verify."
    refute resolved =~ "1. Read. 2. Implement."
    # untouched sections survive
    assert resolved =~ "Senior engineer."
    assert resolved =~ "Tests pass."
  end

  test "append adds after a section; prepend adds before" do
    overlay = """
    @adapt append skill:rules
    - Never force-push.
    @adapt prepend skill:success
    Definition of done follows.
    """

    {resolved, _} = Skill.resolve(@anchored, overlay, "codex")
    assert resolved =~ "- Match existing patterns."
    assert resolved =~ "- Never force-push."
    assert resolved =~ "Definition of done follows."
  end

  test "replace on a plain (unanchored) base is dropped; append degrades to document scope" do
    plain = "Base instructions.\n"
    overlay = "@adapt replace skill:procedure\nnope\n@adapt append skill:rules\nExtra rule."
    {resolved, _} = Skill.resolve(plain, overlay, "opus")
    assert resolved =~ "Base instructions."
    assert resolved =~ "Extra rule."
    refute resolved =~ "nope"
  end

  test "resolved sha256 changes when the overlay changes (replayability)" do
    {_, sha_base} = Skill.resolve(@anchored, nil, "opus")
    {_, sha_adapted} = Skill.resolve(@anchored, "@adapt append skill:rules\n- x", "opus")
    assert sha_base != sha_adapted
  end
end
