defmodule Mitos.Session.PromptWatcher do
  @moduledoc """
  Watches one `Mitos.Session` and turns its screen into semantic prompt events.

  This is the live wiring of `Mitos.PromptDetector` into a running session: the
  watcher subscribes to the session's `Mitos.PubSub` stream and, each time the
  sidecar reports the PTY has gone quiet (`{:event, %{"type" => "idle"}}`),
  asks for a structured snapshot, reassembles the visible text, and runs the
  provider's detection rules. New prompt events are broadcast back onto the same
  session topic so the orchestration brain / approval controller / browser can
  consume them:

      {:mitos, session_id, {:prompt, event}}

  Like the Zig prompt detector, it never injects terminal input — it only
  emits events. The watcher monitors the session pid and shuts down (`:normal`)
  when the session goes away, so its supervisor (`Mitos.Watchers`) does not
  restart it past the session's lifetime.
  """
  use GenServer
  require Logger

  alias Mitos.PromptDetector

  defstruct [:session_id, :session_pid, :rules, :monitor_ref, last_events: nil]

  def child_spec(opts) do
    %{
      id: {__MODULE__, Keyword.fetch!(opts, :session_id)},
      start: {__MODULE__, :start_link, [opts]},
      restart: :transient,
      shutdown: 5_000
    }
  end

  def start_link(opts), do: GenServer.start_link(__MODULE__, opts)

  @impl true
  def init(opts) do
    session_id = Keyword.fetch!(opts, :session_id)
    rules = Keyword.fetch!(opts, :rules)

    case Registry.lookup(Mitos.SessionRegistry, session_id) do
      [{pid, _}] ->
        Mitos.PubSub.subscribe(session_id)
        ref = Process.monitor(pid)

        {:ok,
         %__MODULE__{
           session_id: session_id,
           session_pid: pid,
           rules: rules,
           monitor_ref: ref
         }}

      [] ->
        # Session never started or already gone — nothing to watch.
        :ignore
    end
  end

  @impl true
  def handle_info(
        {:mitos, session_id, {:event, %{"type" => "idle"}}},
        %{session_id: session_id} = state
      ) do
    {:noreply, detect_and_broadcast(state)}
  end

  # The session died — our reason to exist is gone. Stop normally so the
  # one_for_one supervisor leaves us down.
  def handle_info({:DOWN, ref, :process, _pid, _reason}, %{monitor_ref: ref} = state) do
    {:stop, :normal, state}
  end

  # Raw bytes, snapshots (including the ones we requested), our own prompt
  # broadcasts, and other events are not our concern here.
  def handle_info(_msg, state), do: {:noreply, state}

  # -- internals ------------------------------------------------------------

  defp detect_and_broadcast(state) do
    case safe_snapshot(state.session_pid) do
      {:ok, snapshot} ->
        events = snapshot |> PromptDetector.visible_text() |> PromptDetector.detect(state.rules)

        if events == [] or events == state.last_events do
          %{state | last_events: events}
        else
          for event <- events do
            Mitos.PubSub.broadcast(
              state.session_id,
              {:mitos, state.session_id, {:prompt, event}}
            )
          end

          %{state | last_events: events}
        end

      {:error, _reason} ->
        state
    end
  end

  defp safe_snapshot(pid) do
    Mitos.Session.snapshot(pid)
  catch
    :exit, _ -> {:error, :session_unavailable}
  end
end
