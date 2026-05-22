defmodule Mitos.PromptDetectorTest do
  use ExUnit.Case, async: true

  alias Mitos.PromptDetector
  alias Mitos.PromptDetector.Rules

  # Same recorded provider screens the Zig detector tests use, asserting the
  # two runtimes classify them identically (see src/prompt/detectors.zig).
  @fixtures Path.expand("../../../test/fixtures/providers", __DIR__)

  defp fixture(name), do: File.read!(Path.join(@fixtures, name))

  describe "detect/2 against recorded provider screens" do
    test "claude approval prompt" do
      events = PromptDetector.detect(fixture("claude_approval.txt"), Rules.for_provider("claude"))
      assert [{:approval_requested, %{text: text}} | _] = events
      assert text =~ "Do you want to proceed?"
    end

    test "codex approval prompt infers npm install at medium risk" do
      events = PromptDetector.detect(fixture("codex_approval.txt"), Rules.for_provider("codex"))

      assert [{:approval_requested, %{risk: :medium, action: action}}] = events
      assert action == {:install_package, %{manager: "npm", package: "*"}}
    end

    test "codex error screen" do
      events = PromptDetector.detect(fixture("codex_error.txt"), Rules.for_provider("codex"))
      assert [{:error_detected, "authentication required"}] = events
    end

    test "fake final answer" do
      events = PromptDetector.detect(fixture("fake_final.txt"), Rules.for_provider("fake"))
      assert [{:final_answer_candidate, "all done"}] = events
    end

    test "gemini approval prompt" do
      events = PromptDetector.detect(fixture("gemini_approval.txt"), Rules.for_provider("gemini"))
      assert [{:approval_requested, %{risk: :medium}}] = events
    end
  end

  describe "detect/2 classification rules" do
    test "approval and final answer can both surface from one screen" do
      text = "Do you want to proceed?\nFinal answer: shipped"
      events = PromptDetector.detect(text, Rules.generic())

      assert [{:approval_requested, _}, {:final_answer_candidate, "shipped"}] = events
    end

    test "a bare question becomes question_asked when no approval marker matches" do
      assert [{:question_asked, %{text: "Which file should I edit?"}}] =
               PromptDetector.detect("Which file should I edit?", Rules.generic())
    end

    test "high-risk wording escalates the risk level" do
      assert [{:approval_requested, %{risk: :high}}] =
               PromptDetector.detect("Allow command? rm -rf /tmp/x", Rules.generic())
    end

    test "empty screen yields no events" do
      assert [] = PromptDetector.detect("", Rules.generic())
    end
  end

  describe "visible_text/1" do
    test "reassembles snapshot cells into rows ordered by y then x" do
      snapshot = %{
        "cols" => 5,
        "rows" => 2,
        "cells" => [
          %{"x" => 1, "y" => 0, "t" => "i"},
          %{"x" => 0, "y" => 0, "t" => "h"},
          %{"x" => 0, "y" => 1, "t" => "y"},
          %{"x" => 1, "y" => 1, "t" => "o"}
        ]
      }

      assert PromptDetector.visible_text(snapshot) == "hi\nyo"
    end

    test "trims trailing blank columns and rows" do
      snapshot = %{
        "cells" => [
          %{"x" => 0, "y" => 0, "t" => "a"},
          %{"x" => 1, "y" => 0, "t" => " "},
          %{"x" => 0, "y" => 1, "t" => " "}
        ]
      }

      assert PromptDetector.visible_text(snapshot) == "a"
    end

    test "tolerates a snapshot without cells" do
      assert PromptDetector.visible_text(%{"cols" => 80}) == ""
    end
  end
end
