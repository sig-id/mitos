defmodule Mitos.HarnessAdapterTest do
  @moduledoc """
  01 §8.6: each harness adapter owns the normalization of its CLI's structured
  output into the one event stream. These tests pin the per-vendor JSONL shapes
  (captured from real `codex exec --json`, `opencode run --format json`, and
  `pi -p --mode json` runs) so schema drift surfaces as a test failure, not a
  silent mis-observation.
  """
  use ExUnit.Case, async: true

  defp norm(mod, lines) do
    lines
    |> Enum.flat_map(&mod.normalize/1)
  end

  test "codex: thread/turn lifecycle, assistant text, tool calls, completion" do
    lines = [
      ~s<{"type":"thread.started","thread_id":"t1"}>,
      ~s<{"type":"turn.started"}>,
      ~s<{"type":"message","role":"assistant","content":[{"type":"text","text":"planning"}],"usage":{"input_tokens":100,"output_tokens":4}}>,
      ~s<{"type":"function_call","name":"shell","arguments":"{\\"cmd\\":\\"ls\\"}"}>,
      ~s<{"type":"function_call_output","output":"file.txt"}>,
      ~s<{"type":"turn.completed","usage":{"input_tokens":110,"output_tokens":10}}>
    ]

    events = norm(Mitos.Harness.Codex, lines)

    assert Enum.any?(events, &match?(%{type: :status, status: "thread.started"}, &1))
    assert Enum.any?(events, &match?(%{type: :assistant_text, text: "planning"}, &1))
    assert Enum.any?(events, &match?(%{type: :tool_use, name: "shell", input: %{"cmd" => "ls"}}, &1))
    assert Enum.any?(events, &match?(%{type: :tool_result, content: "file.txt"}, &1))
    assert Enum.any?(events, &match?(%{type: :token_usage, tokens: 104}, &1))
    assert Enum.any?(events, &match?(%{type: :final_result, ok: true}, &1))
  end

  test "codex: a failed turn normalizes to a non-ok final_result" do
    lines = [
      ~s<{"type":"turn.started"}>,
      ~s<{"type":"turn.failed","error":{"message":"usage limit exceeded"}}>,
      ~s<{"type":"error","message":"usage limit exceeded"}>
    ]

    events = norm(Mitos.Harness.Codex, lines)
    assert Enum.any?(events, &match?(%{type: :final_result, ok: false, subtype: "error"}, &1))
    assert Enum.any?(events, &match?(%{type: :status, status: {:error, _}}, &1))
  end

  test "opencode: text + step_finish with tokens/cost" do
    lines = [
      ~s<{"type":"step_start","part":{"type":"step-start"}}>,
      ~s<{"type":"text","part":{"type":"text","text":"PONG"}}>,
      ~s<{"type":"step_finish","part":{"type":"step-finish","reason":"stop","tokens":{"total":7415,"input":627,"output":4},"cost":0}}>
    ]

    events = norm(Mitos.Harness.Opencode, lines)

    assert Enum.any?(events, &match?(%{type: :assistant_text, text: "PONG"}, &1))
    assert Enum.any?(events, &match?(%{type: :token_usage, tokens: 7415}, &1))
    assert Enum.any?(events, &match?(%{type: :final_result, ok: true, subtype: "stop"}, &1))
  end

  test "opencode: tool_call + tool_result normalize to tool_use/tool_result" do
    lines = [
      ~s<{"type":"tool_call","part":{"tool":"bash","input":{"command":"pwd"}}}>,
      ~s<{"type":"tool_result","part":{"id":"tr1","output":"/workspace"}}>
    ]

    events = norm(Mitos.Harness.Opencode, lines)
    assert Enum.any?(events, &match?(%{type: :tool_use, name: "bash", input: %{"command" => "pwd"}}, &1))
    assert Enum.any?(events, &match?(%{type: :tool_result, content: "/workspace", tool_use_id: "tr1"}, &1))
  end

  test "pi: session lifecycle, text deltas, message_end final + usage" do
    lines = [
      ~s<{"type":"session","id":"s1"}>,
      ~s<{"type":"turn_start"}>,
      ~s<{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"PON"}}>,
      ~s<{"type":"message_update","assistantMessageEvent":{"type":"text_delta","delta":"G"}}>,
      ~s<{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"PONG"}],"usage":{"totalTokens":1294,"cost":{"total":0.001}},"stopReason":"stop"}}>
    ]

    events = norm(Mitos.Harness.Pi, lines)

    texts = for %{type: :assistant_text, text: t} <- events, do: t
    assert texts == ["PON", "G", "PONG"]
    assert Enum.any?(events, &match?(%{type: :token_usage, tokens: 1294, usd: 0.001}, &1))
    assert Enum.any?(events, &match?(%{type: :final_result, ok: true, subtype: "stop"}, &1))
  end

  test "claude-code adapter still normalizes its stream-json shape (parity)" do
    # claude_code keeps its own private normalize; verify via the public path
    # it shares (the adapter is exercised end-to-end in run_test). Here we only
    # assert the adapter module + capabilities are wired.
    assert Mitos.Harness.ClaudeCode.capabilities().structured_output
    assert Application.get_env(:mitos, :harnesses)["codex"] == Mitos.Harness.Codex
    assert Application.get_env(:mitos, :harnesses)["opencode"] == Mitos.Harness.Opencode
    assert Application.get_env(:mitos, :harnesses)["pi"] == Mitos.Harness.Pi
  end
end
