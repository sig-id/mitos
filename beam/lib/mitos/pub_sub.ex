defmodule Mitos.PubSub do
  @moduledoc """
  Tiny fan-out backed by a `Registry` with duplicate keys. Each subscriber
  registers under the session id; broadcasts walk the registry and `send/2`
  one message per subscriber.

  We use this rather than Phoenix.PubSub because (a) we don't want the
  Phoenix dep yet, and (b) raw PTY traffic only ever fans out to the
  WebSocket(s) attached to one specific session — a global pub/sub matrix is
  overkill. Migration to Phoenix.PubSub is a topic-name swap when the
  orchestration grows cross-session broadcasts.
  """

  @registry Mitos.PubSub.Registry

  @doc "Subscribe the calling process to a session's raw + event stream."
  def subscribe(session_id) when is_binary(session_id) do
    {:ok, _} = Registry.register(@registry, session_id, nil)
    :ok
  end

  @doc "Broadcast a message to every subscriber of `session_id`."
  def broadcast(session_id, message) when is_binary(session_id) do
    Registry.dispatch(@registry, session_id, fn entries ->
      for {pid, _} <- entries, do: send(pid, message)
    end)
  end
end
