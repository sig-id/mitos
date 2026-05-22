defmodule Mitos.Adapter.ProvidersTest do
  use ExUnit.Case, async: true

  alias Mitos.Adapter.{Claude, Codex, Command, Fake, Gemini, Generic, Task}
  alias Mitos.PromptDetector.Rules

  defp task(text \\ "Summarize this repository"), do: %Task{text: text}

  describe "claude.pty" do
    test "passes the task as a positional after --, mirroring the Zig pty command" do
      assert {:ok, %Command{} = cmd} = Claude.build(task(), [])
      assert cmd.argv == ["claude", "--", "Summarize this repository"]
      assert cmd.provider == "claude"
      assert cmd.adapter_id == "claude.pty"
      assert cmd.mode == :pty
      assert cmd.initial_input == nil
      assert cmd.prompt_rules == Rules.for_provider("claude")
    end
  end

  describe "codex.pty" do
    test "disables alt screen and pins --cd to the workspace" do
      assert {:ok, cmd} = Codex.build(task(), cwd: "/work")

      assert cmd.argv == [
               "codex",
               "--no-alt-screen",
               "--cd",
               "/work",
               "Summarize this repository"
             ]

      assert cmd.adapter_id == "codex.pty"
    end

    test "defaults --cd to . when no cwd is given" do
      assert {:ok, cmd} = Codex.build(task(), [])
      assert cmd.argv == ["codex", "--no-alt-screen", "--cd", ".", "Summarize this repository"]
    end
  end

  describe "gemini.pty" do
    test "launches the bare REPL and delivers the task as initial stdin" do
      assert {:ok, cmd} = Gemini.build(task("Review this code"), [])
      assert cmd.argv == ["gemini"]
      assert cmd.initial_input == "Review this code\r"
      assert cmd.adapter_id == "gemini.pty"
    end
  end

  describe "generic.pty" do
    test "passes through caller argv and writes the task to stdin" do
      assert {:ok, cmd} = Generic.build(task("hi"), argv: ["bash", "-i"])
      assert cmd.argv == ["bash", "-i"]
      assert cmd.initial_input == "hi"
    end

    test "requires argv" do
      assert {:error, :missing_argv} = Generic.build(task(), [])
    end

    test "omits initial input for an empty task" do
      assert {:ok, cmd} = Generic.build(%Task{text: ""}, argv: ["bash"])
      assert cmd.initial_input == nil
    end
  end

  describe "fake.pty" do
    test "runs a shell stand-in and carries the task via MITOS_TASK env" do
      assert {:ok, cmd} = Fake.build(task("do the thing"), [])
      assert [bin, "-c", _script] = cmd.argv
      assert bin == "/bin/sh"
      assert {"MITOS_TASK", "do the thing"} in cmd.env
      assert cmd.prompt_rules == Rules.for_provider("fake")
    end
  end

  test "build honors cols/rows/idle_ms/cwd from opts" do
    assert {:ok, cmd} = Claude.build(task(), cols: 120, rows: 40, idle_ms: 100, cwd: "/here")
    assert cmd.cols == 120
    assert cmd.rows == 40
    assert cmd.idle_ms == 100
    assert cmd.cwd == "/here"
  end
end
