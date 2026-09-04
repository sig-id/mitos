defmodule Mitos.Observation.Clock do
  @moduledoc """
  The quiescence clock (14 §2) — the one discrete edge everything else hangs on.

      quiet(tree, T) = for the last T ms:
                         no bytes written to the PTY master
                         AND no writes under the workspace
                         AND no new *distinct* exec argv

  A **quiescence edge** is the transition from not-quiet into quiet. One
  mechanism, five consumers (14 §2.2): terminal snapshot timing, the safe steer
  window, `blocked_on_human` (16 §2), checkpoint boundaries, and the watch
  surface's frame sampling.

  `distinct` is load-bearing: an agent spinning `sleep 1; curl ...` executes
  constantly and progresses never, so a repeat of an argv already seen in the
  current window does **not** reset the clock. Without it, naive idle detection
  reports a polling loop as busy forever — the false negative that makes
  `blocked_on_human` useless.

  Thresholds (14 §2.1) MUST satisfy `snap < steer < block`; each is crossed at
  most once per window and emits a `quiescent` Effect carrying the level.
  """

  use GenServer

  @levels [:snap, :steer, :block]

  defstruct run_id: nil,
            stage_id: nil,
            thresholds: %{},
            on_effect: nil,
            on_block: nil,
            # monotonic ms of the last *resetting* activity
            last_activity: 0,
            # byte offset in the PTY spool at the last activity
            pty_offset: 0,
            # counters since the current window opened
            writes_since: 0,
            distinct_execs_since: 0,
            session_appends_since: 0,
            # argvs seen in the current window (repeats do not reset)
            seen_execs: MapSet.new(),
            # levels already emitted for this window
            emitted: MapSet.new(),
            timer: nil

  # --- client ---

  @doc """
  Start a clock for one open step. `on_effect` is `fun(fields)` appending an
  Effect through the run's single writer; `on_block` is called once per window
  when the `:block` threshold is crossed (16 §2).
  """
  def start_link(opts) do
    GenServer.start_link(__MODULE__, opts, Keyword.take(opts, [:name]))
  end

  @doc """
  Record activity. `kind` is `:pty | :write | :exec | :session`; repeats of a
  seen argv do not reset. `:session` is an append to the harness's own session
  artifact (C2, 14 §1.1) — it is the only activity signal a headless run has
  besides workspace writes and execs, so it must reset the window or a thinking
  harness reads as blocked.
  """
  def bump(clock, kind, detail \\ nil), do: GenServer.cast(clock, {:bump, kind, detail})

  @doc "Current window read: `%{quiet_for_ms, pty_offset, writes_since, distinct_execs_since}`."
  def read(clock), do: GenServer.call(clock, :read)

  @doc "Is the session quiet for at least `ms`? (pure read; no waiting)"
  def quiet?(clock, ms), do: read(clock).quiet_for_ms >= ms

  @doc """
  Block until the session has been quiet for `min_quiet_ms`, or `timeout_ms`
  elapses. Returns the window read at the edge. This is how a snapshot or a
  steer picks its moment (14 §3.1/§3.3) rather than sampling a moving surface.
  """
  def await_edge(clock, min_quiet_ms, timeout_ms \\ 60_000) do
    deadline = System.monotonic_time(:millisecond) + timeout_ms
    do_await(clock, min_quiet_ms, deadline)
  end

  defp do_await(clock, min_quiet_ms, deadline) do
    r = read(clock)

    cond do
      r.quiet_for_ms >= min_quiet_ms ->
        {:ok, r}

      System.monotonic_time(:millisecond) >= deadline ->
        {:error, :timeout}

      true ->
        remaining = max(deadline - System.monotonic_time(:millisecond), 0)
        Process.sleep(max(min(min_quiet_ms - r.quiet_for_ms, remaining), 5))
        do_await(clock, min_quiet_ms, deadline)
    end
  end

  @doc "Threshold map from config, merged with per-capability overrides (14 §2.1)."
  def thresholds(overrides \\ %{}) do
    base =
      Application.get_env(:mitos, :observation, [])
      |> Keyword.get(:thresholds, snap: 200, steer: 750, block: 45_000)
      |> Map.new()

    t = Map.merge(base, Map.new(overrides))

    # 14 §2.1: snap < steer < block MUST hold. A misordered config would make a
    # steer fire inside a repaint, so it is refused rather than reordered.
    unless t.snap < t.steer and t.steer < t.block do
      raise ArgumentError, "14 §2.1 violated: require snap < steer < block, got #{inspect(t)}"
    end

    t
  end

  # --- server ---

  @impl true
  def init(opts) do
    st = %__MODULE__{
      run_id: opts[:run_id],
      stage_id: opts[:stage_id],
      thresholds: thresholds(opts[:thresholds] || %{}),
      on_effect: opts[:on_effect],
      on_block: opts[:on_block],
      last_activity: System.monotonic_time(:millisecond)
    }

    {:ok, schedule(st)}
  end

  @impl true
  def handle_cast({:bump, kind, detail}, st) do
    st =
      case kind do
        :exec -> bump_exec(st, detail)
        :pty -> reset(%{st | pty_offset: st.pty_offset + byte_count(detail)})
        :write -> reset(%{st | writes_since: st.writes_since + 1})
        :session -> reset(%{st | session_appends_since: st.session_appends_since + 1})
      end

    {:noreply, st}
  end

  @impl true
  def handle_call(:read, _from, st), do: {:reply, window(st), st}

  @impl true
  def handle_info(:tick, st), do: {:noreply, st |> cross_thresholds() |> schedule()}

  # A repeated argv is not progress: it advances nothing and MUST NOT reset the
  # window (14 §2). A distinct one opens a fresh window.
  defp bump_exec(st, argv) do
    key = exec_key(argv)

    if MapSet.member?(st.seen_execs, key) do
      st
    else
      st
      |> reset()
      |> Map.merge(%{
        seen_execs: MapSet.put(st.seen_execs, key),
        distinct_execs_since: st.distinct_execs_since + 1
      })
    end
  end

  defp reset(st) do
    schedule(%{st | last_activity: System.monotonic_time(:millisecond), emitted: MapSet.new()})
  end

  defp window(st) do
    %{
      quiet_for_ms: System.monotonic_time(:millisecond) - st.last_activity,
      pty_offset: st.pty_offset,
      writes_since: st.writes_since,
      distinct_execs_since: st.distinct_execs_since,
      session_appends_since: st.session_appends_since
    }
  end

  # Emit each threshold at most once per window; the block crossing additionally
  # fires the `blocked_on_human` callback (16 §2).
  defp cross_thresholds(st) do
    w = window(st)

    Enum.reduce(@levels, st, fn level, acc ->
      threshold = Map.fetch!(acc.thresholds, level)

      if w.quiet_for_ms >= threshold and not MapSet.member?(acc.emitted, level) do
        emit(acc, level, w)
        if level == :block and acc.on_block, do: acc.on_block.(w)
        %{acc | emitted: MapSet.put(acc.emitted, level)}
      else
        acc
      end
    end)
  end

  defp emit(%{on_effect: nil}, _level, _w), do: :ok

  defp emit(st, level, w) do
    st.on_effect.(
      type: :quiescent,
      ref: %{"stage_id" => st.stage_id, "level" => to_string(level)},
      payload: %{
        "for_ms" => w.quiet_for_ms,
        "pty_offset" => w.pty_offset,
        "workspace_writes_since" => w.writes_since,
        "distinct_execs_since" => w.distinct_execs_since,
        "session_appends_since" => w.session_appends_since
      }
    )
  end

  # Wake at the next uncrossed threshold rather than polling on a fixed tick.
  defp schedule(st) do
    if st.timer, do: Process.cancel_timer(st.timer, async: true, info: false)
    elapsed = System.monotonic_time(:millisecond) - st.last_activity

    next =
      @levels
      |> Enum.reject(&MapSet.member?(st.emitted, &1))
      |> Enum.map(&(Map.fetch!(st.thresholds, &1) - elapsed))
      |> Enum.min(fn -> nil end)

    case next do
      nil -> %{st | timer: nil}
      ms -> %{st | timer: Process.send_after(self(), :tick, max(ms, 1))}
    end
  end

  defp exec_key(argv) when is_list(argv), do: Enum.join(argv, " ")
  defp exec_key(argv) when is_binary(argv), do: argv
  defp exec_key(other), do: inspect(other)

  defp byte_count(n) when is_integer(n), do: n
  defp byte_count(b) when is_binary(b), do: byte_size(b)
  defp byte_count(_), do: 0
end
