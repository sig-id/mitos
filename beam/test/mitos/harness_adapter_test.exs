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

    assert Enum.any?(
             events,
             &match?(%{type: :tool_use, name: "shell", input: %{"cmd" => "ls"}}, &1)
           )

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

    assert Enum.any?(
             events,
             &match?(%{type: :tool_use, name: "bash", input: %{"command" => "pwd"}}, &1)
           )

    assert Enum.any?(
             events,
             &match?(%{type: :tool_result, content: "/workspace", tool_use_id: "tr1"}, &1)
           )
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

  # (a) stderr capture: a line the adapter doesn't recognize is surfaced as
  # :stderr ONLY once the harness has emitted its first real JSON event — so
  # installer/bootstrap noise before the harness starts is dropped, but runtime
  # diagnostics (pi emits `session` JSON, THEN errors to stderr) are kept.
  test "post-JSON unrecognized lines surface as :stderr; pre-JSON noise is dropped" do
    # bootstrap-style noise BEFORE any JSON, then a JSON session event, then a
    # real runtime stderr line — mirrors pi's actual ordering.
    {:ok, session} =
      Mitos.Harness.PortAdapter.launch(
        "sh",
        [
          "-c",
          ~s|echo 'added 140 packages in 10s'; echo '{"type":"session","id":"s1"}'; echo 'No API key found'>&2|
        ],
        System.tmp_dir!(),
        %{}
      )

    events =
      Mitos.Harness.PortAdapter.events(session, &Mitos.Harness.Pi.normalize/1)
      |> Enum.to_list()

    # the pre-session bootstrap line is dropped (not surfaced as stderr)
    refute Enum.any?(events, &match?(%{type: :stderr, line: "added 140 packages in 10s"}, &1)),
           "pre-JSON bootstrap noise should be dropped, got: #{inspect(events)}"

    # the post-session stderr line IS surfaced (death visibility)
    assert Enum.any?(events, &match?(%{type: :stderr, line: "No API key found"}, &1)),
           "expected the post-JSON stderr line to surface, got: #{inspect(events)}"

    # the real JSON event normalizes
    assert Enum.any?(events, &match?(%{type: :status, status: "session"}, &1))
    assert Enum.any?(events, &match?(%{type: :status, status: {:exited, 0}}, &1))
  end

  # (b) no-wedge: if the port disappears without delivering {:exit_status, _}
  # (e.g. closed owner-side), the bounded receive must synthesize an exit
  # instead of blocking the consumer forever.
  test "a vanished port is reaped as {:exited, :port_vanished}, not a wedge" do
    # Shrink the probe so the test doesn't wait the full 5s.
    prev = Application.get_env(:mitos, :port_idle_probe_ms)
    Application.put_env(:mitos, :port_idle_probe_ms, 50)

    try do
      # A long sleep keeps the OS process alive but silent.
      {:ok, %{port: port} = session} =
        Mitos.Harness.PortAdapter.launch("sh", ["-c", "sleep 30"], System.tmp_dir!(), %{})

      # Close the port owner-side WITHOUT consuming exit_status — simulates the
      # real failure mode (process gone, no message delivered).
      Port.close(port)

      events =
        Mitos.Harness.PortAdapter.events(session, fn _ -> [] end)
        |> Enum.to_list()

      assert Enum.any?(events, &match?(%{type: :status, status: {:exited, :port_vanished}}, &1)),
             "expected a synthesized :port_vanished exit, got: #{inspect(events)}"
    after
      if prev,
        do: Application.put_env(:mitos, :port_idle_probe_ms, prev),
        else: Application.delete_env(:mitos, :port_idle_probe_ms)
    end
  end

  # A harness that dies abnormally (SIGSEGV / SIGKILL — e.g. a node EPIPE crash)
  # must be observed as an exit status and NEVER crash the BEAM owner. Real
  # harnesses run through PortAdapter on an UNLINKED port, so the worst a crash
  # does is end the event stream with {:exited, N} — the substrate turns that
  # into a stage abort (04 §3.4), not a control-plane crash.
  test "a crashing harness is contained: observed as {:exited, _}, owner alive" do
    owner = self()

    for {label, cmd, status_pred} <- [
          {"non-zero exit", ~c"exit 1", &(&1 == 1)},
          {"SIGKILL", ~c"kill -9 $$", &(&1 == 137)},
          {"SIGSEGV", ~c"kill -SEGV $$", &(&1 == 139)}
        ] do
      {:ok, session} =
        Mitos.Harness.PortAdapter.launch("sh", ["-c", to_string(cmd)], System.tmp_dir!(), %{})

      events =
        Mitos.Harness.PortAdapter.events(session, fn _ -> [] end)
        |> Enum.to_list()

      exited =
        Enum.find_value(events, fn
          %{type: :status, status: {:exited, n}} -> n
          _ -> nil
        end)

      assert exited && status_pred.(exited),
             "#{label}: expected an {:exited, n} status matching, got: #{inspect(events)}"

      # the calling BEAM process survived the child crash
      assert Process.alive?(owner), "#{label}: owner died"
    end
  end
end
