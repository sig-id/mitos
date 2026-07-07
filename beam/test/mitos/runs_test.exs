defmodule Mitos.RunsTest do
  use ExUnit.Case, async: true

  alias Mitos.Runs

  # A synthetic inner-trace file built programmatically (JSON-in-JSON must be
  # encoded, not hand-escaped) and exercised through Runs.trace/3 without
  # spawning a harness. Covers the three read-time behaviors:
  #   * legacy re-hydration — a raw pi frame nested as a :stderr `line` is
  #     decoded and run through the fixed adapter, recovering tools/results/
  #     usage/reasoning the pre-fix adapter dropped.
  #   * legacy drop — the buggy assistant_text/token_usage/final_result the old
  #     adapter emitted are removed in legacy mode (the re-hydrated frames are
  #     authoritative) so nothing is duplicated.
  #   * text coalescing — per-token assistant_text deltas fold into one block.
  test "trace/3 re-hydrates legacy stderr blobs, drops redundant events, coalesces text" do
    run_id = "test-org/#{Mitos.id()}"
    stage = "webcap"
    path = Path.join([Mitos.run_dir(run_id), "trace", stage <> ".jsonl"])
    File.mkdir_p!(Path.dirname(path))

    enc = &Mitos.JSON.encode!/1

    # a raw pi frame nested as a :stderr line (the pre-fix adapter's shape)
    blob =
      fn m ->
        %{"iteration" => 1, "type" => "stderr", "line" => enc.(m)} |> enc.()
      end

    events = [
      # session/turn lifecycle (recognized even by the old adapter → real events)
      enc.(%{"iteration" => 1, "type" => "status", "status" => "session"}),
      enc.(%{"iteration" => 1, "type" => "status", "status" => "turn_start"}),
      # reasoning streamed as thinking_delta — two blobs that must coalesce
      blob.(%{"type" => "message_update", "assistantMessageEvent" => %{"type" => "thinking_delta", "delta" => "Let me"}}),
      blob.(%{"type" => "message_update", "assistantMessageEvent" => %{"type" => "thinking_delta", "delta" => " think."}}),
      # a tool call + result that ONLY exist as raw frames the old adapter
      # missed (misfiled as stderr) — re-hydration must recover them.
      blob.(%{"type" => "tool_execution_start", "toolCallId" => "c1", "toolName" => "bash",
              "args" => %{"command" => "ls"}}),
      blob.(%{"type" => "tool_execution_end", "toolCallId" => "c1", "toolName" => "bash",
              "result" => %{"content" => [%{"type" => "text", "text" => "a.txt"}]}, "isError" => false}),
      # the answer prose + per-turn usage land on turn_end
      blob.(%{"type" => "turn_end",
              "message" => %{"role" => "assistant", "stopReason" => "stop",
                             "usage" => %{"totalTokens" => 42},
                             "content" => [%{"type" => "text", "text" => "done"}]}}),
      # genuine non-JSON stderr (auth error) survives
      enc.(%{"iteration" => 1, "type" => "stderr", "line" => "No API key found"}),
      # a buggy final_result the old adapter emitted — dropped in legacy mode
      enc.(%{"iteration" => 1, "type" => "final_result", "ok" => true, "subtype" => "STALE"})
    ]

    File.write!(path, Enum.join(events, "\n") <> "\n")

    %{events: got} = Runs.trace(run_id, stage)

    # the two thinking_delta blobs coalesced into one reasoning block
    thinking = for %{"type" => "assistant_text", "thinking" => true, "text" => t} <- got, do: t
    assert thinking == ["Let me think."]
    # the answer prose recovered from turn_end
    assert Enum.any?(got, &match?(%{"type" => "assistant_text", "text" => "done"}, &1))
    assert Enum.any?(got, &match?(%{"type" => "token_usage", "tokens" => 42}, &1))

    # the tool call + result were recovered from the raw frames
    assert Enum.any?(got, &match?(%{"type" => "tool_use", "name" => "bash", "input" => %{"command" => "ls"}}, &1))
    assert Enum.any?(got, &match?(%{"type" => "tool_result", "content" => "a.txt"}, &1))

    # genuine stderr kept; the stale final_result dropped (re-hydration owns it)
    assert Enum.any?(got, &match?(%{"type" => "stderr", "line" => "No API key found"}, &1))
    refute Enum.any?(got, &match?(%{"type" => "final_result", "subtype" => "STALE"}, &1))
  end

  # A non-legacy trace (no stderr-JSON blobs) passes events through untouched —
  # the legacy drop rule must never engage on a clean trace.
  test "trace/3 leaves a clean (non-legacy) trace untouched" do
    run_id = "clean-org/#{Mitos.id()}"
    stage = "webcap"
    path = Path.join([Mitos.run_dir(run_id), "trace", stage <> ".jsonl"])
    File.mkdir_p!(Path.dirname(path))

    enc = &Mitos.JSON.encode!/1

    File.write!(path, Enum.join([
      enc.(%{"type" => "status", "status" => "session"}),
      enc.(%{"type" => "assistant_text", "text" => "hi"}),
      enc.(%{"type" => "final_result", "ok" => true, "subtype" => "stop"}),
      enc.(%{"type" => "token_usage", "tokens" => 5, "usd" => 0.0})
    ], "\n") <> "\n")

    %{events: got} = Runs.trace(run_id, stage)

    types = Enum.map(got, & &1["type"])
    assert types == ["status", "assistant_text", "final_result", "token_usage"]
  end
end
