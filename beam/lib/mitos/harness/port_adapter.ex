defmodule Mitos.Harness.PortAdapter do
  @moduledoc false
  # Shared subprocess machinery for harness adapters that observe a CLI through
  # its structured stdout (01 §8.6 "fire-and-drain"): spawn the CLI with a
  # hermetic env, stream newline-delimited JSON, and let the adapter own the
  # per-line normalization. PTY/interactive steering stays out of scope (09 §2).

  @doc "Spawn a harness CLI. `env` is the FULL process env (Hermetic.env/1 applied)."
  def launch(exe, args, cwd, env) do
    if exe = System.find_executable(exe) do
      port_env =
        Enum.map(env, fn
          {k, nil} -> {String.to_charlist(k), false}
          {k, v} -> {String.to_charlist(k), String.to_charlist(v)}
        end)

      # The harness CLIs are all non-interactive print modes (pi -p, claude
      # -p, codex exec, opencode run): they take the prompt as an argv element,
      # never stdin. A bare :spawn_executable leaves the child's stdin as an
      # open pipe with no writer and no EOF, and several (pi at least) block
      # reading piped stdin before they ever process the prompt — so a run
      # wedges forever (1s CPU, no sockets, do_epoll_wait). Erlang ports can't
      # half-close stdin, so route it from /dev/null via sh: the child sees
      # immediate EOF and proceeds with its prompt arg. `exec "$0" "$@"`
      # forwards args verbatim (no shell quoting/escaping), and exec preserves
      # the real exit status for :exit_status.
      sh = System.find_executable("sh") || "/bin/sh"

      port =
        Port.open({:spawn_executable, sh}, [
          :binary,
          :exit_status,
          :hide,
          # Capture the harness's stderr (auth errors, stack traces, installer
          # noise) into the data stream. Adapter normalize/1 only matches
          # structured JSON, so stderr lines would otherwise be dropped;
          # events/2 surfaces them as :stderr so a harness death isn't a black
          # box. Caveat: with merge on, a >PIPE_BUF stdout write could in
          # principle interleave with stderr and split a JSON line — rare for
          # line-buffered CLIs, and the worst case is one dropped event.
          :stderr_to_stdout,
          args: ["-c", ~s(exec "$0" "$@" </dev/null), exe | args],
          cd: cwd,
          env: port_env
        ])

      {:ok, %{port: port}}
    else
      {:error, :cli_not_found}
    end
  end

  # How long to wait for the next port message before probing liveness. Bounded
  # so a port whose OS process vanished without delivering {:exit_status, _}
  # (closed owner-side, or killed via a path Erlang didn't observe) can't wedge
  # the consumer in `receive` forever — the probe synthesizes an exit instead.
  # Configurable so tests can shrink the window.
  defp idle_probe_ms,
    do: Application.get_env(:mitos, :port_idle_probe_ms, 5_000)

  # Caps on captured non-JSON (stderr/bootstrap) lines so a chatty harness or
  # its installer can't balloon the trace. Non-JSON lines are only surfaced once
  # the harness has emitted its first real (adapter-recognized) JSON event —
  # that drops the pre-session installer/bootstrap noise (npm, etc.) while still
  # capturing runtime diagnostics (e.g. pi's "No API key found" arrives AFTER its
  # `session` JSON). Each surfaced line is \r-collapsed (progress bars) + truncated.
  @stderr_cap 200
  @stderr_line_cap 1_000

  @doc """
  A line-stream of normalized events. `normalize` is `(line :: binary) -> [event]`.
  Non-JSON lines are surfaced as `%{type: :stderr, line: raw}` **only after** the
  harness has emitted its first real JSON event (so installer/bootstrap noise
  before the harness starts is dropped, but runtime diagnostics are kept), capped
  at `@stderr_cap` lines and truncated to `@stderr_line_cap`. Emits a final
  `%{type: :status, status: {:exited, code}}` on process exit, or
  `{:exited, :port_vanished}` if the port disappears without delivering an exit
  status (probed after `idle_probe_ms/0` of silence).
  """
  def events(%{port: port}, normalize) do
    Stream.resource(
      fn -> {port, "", false, 0} end,
      fn
        :halt ->
          {:halt, :closed}

        {port, buf, seen_json, n} ->
          receive do
            {^port, {:data, data}} ->
              {lines, rest} = split_lines(buf <> data)
              {events, seen_json2, n2} = norm_lines(lines, normalize, seen_json, n)
              {events, {port, rest, seen_json2, n2}}

            {^port, {:exit_status, status}} ->
              {tail, _, _} = norm_lines((if buf == "", do: [], else: [buf]), normalize, seen_json, n)
              {tail ++ [%{type: :status, status: {:exited, status}}], :halt}
          after
            idle_probe_ms() ->
              if Port.info(port) == nil do
                {tail, _, _} = norm_lines((if buf == "", do: [], else: [buf]), normalize, seen_json, n)
                {tail ++ [%{type: :status, status: {:exited, :port_vanished}}], :halt}
              else
                # Port alive but idle (model thinking, slow tool) — keep waiting.
                {[], {port, buf, seen_json, n}}
              end
          end
      end,
      fn
        :closed -> :ok
        {port, _, _, _} -> safe_close(port)
      end
    )
  end

  # Normalize a batch of lines, threading whether we've seen real JSON yet and a
  # running count of surfaced stderr lines. Recognized JSON always passes through
  # and flips seen_json true; non-JSON is dropped pre-session, surfaced (capped +
  # truncated) post-session.
  defp norm_lines(lines, normalize, seen_json, count) do
    Enum.reduce(lines, {[], seen_json, count}, fn line, {acc, sj, n} ->
      case normalize.(line) do
        [] ->
          cond do
            line == "" ->
              {acc, sj, n}

            # A line the adapter didn't surface but which IS valid JSON is
            # harness protocol (pi's thinking_start/message_start/turn_end
            # frames, claude-code's preamble, …), NOT stderr. Drop it silently
            # and flip seen_json — the harness has started its protocol, so
            # any later non-JSON is a runtime diagnostic worth keeping. Without
            # this, every unrecognized JSON frame was misfiled as :stderr and a
            # token-by-token reasoning stream could balloon the trace by GBs.
            match?({:ok, %{}}, Mitos.JSON.decode(line)) ->
              {acc, true, n}

            not sj ->
              {acc, sj, n}

            n >= @stderr_cap ->
              {acc, sj, n + 1}

            true ->
              {acc ++ [%{type: :stderr, line: clean_stderr(line)}], sj, n + 1}
          end

        events ->
          {acc ++ events, true, n}
      end
    end)
  end

  # Collapse \r-delimited progress frames (npm/TTY progress bars write many frames
  # between newlines) to their final frame, then truncate.
  defp clean_stderr(line) do
    line
    |> String.split("\r", trim: true)
    |> List.last()
    |> Kernel.||("")
    |> String.slice(0, @stderr_line_cap)
  end

  def safe_close(port) do
    if Port.info(port), do: Port.close(port)
    :ok
  catch
    :error, :badarg -> :ok
  end

  def split_lines(buf) do
    parts = String.split(buf, "\n")
    {rest, lines} = List.pop_at(parts, -1)
    {Enum.reject(lines, &(&1 == "")), rest || ""}
  end
end
