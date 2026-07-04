defmodule Mitos.Sink.Reply do
  @moduledoc """
  The `reply` sink (10 §5.2, §5.4) — round-trip back to the originating
  connector: deliver the result into the same Slack thread / email / webhook
  callback the signal arrived on.

  The `reply_ref` carried at ingress (10 §3.2) is what this sink delivers back
  to. A connector whose `capabilities().round_trip?` is true MAY back a reply
  sink; the binding (10 §4) pairs them. The payload is the Effect-only result
  summary (commercial cut).
  """

  @behaviour Mitos.Sink

  @impl true
  def bind(config), do: {:ok, Map.new(config)}

  @impl true
  def match(%Mitos.Event.Effect{type: t}), do: t in [:capability_finished, :run_finished]

  @impl true
  def render(%{run_id: run_id, state: state, cut: cut} = ctx, binding) do
    proof = Mitos.Sink.commercial_render(run_id, ctx[:charge])

    # reply_ref resolves from the run's intake (stored on the task/run config),
    # or the binding's static ref.
    reply_ref = ctx[:reply_ref] || binding[:reply_ref]

    {:ok,
     %{
       reply_ref: reply_ref,
       status: state.status,
       result: proof["result"],
       summary: "run #{run_id}: #{state.status}",
       cut: cut
     }}
  end

  @impl true
  def deliver(_handle, %{reply_ref: nil}),
    do: {:error, :no_reply_ref}

  def deliver(_handle, payload) do
    # The reply transport is the originating connector's round-trip path; the
    # MVP delivers to a pluggable reply destination keyed by reply_ref.
    Mitos.Sink.Reply.Destination.reply(payload.reply_ref, payload)
    {:ok, %{dest: payload.reply_ref, kind: :reply}}
  end

  @impl true
  def capabilities, do: %{egress: true, round_trip?: true, retries: 2}
end

defmodule Mitos.Sink.Reply.Destination do
  @moduledoc false
  # Round-trip delivery transport: in-process for the MVP (chat/email webhook
  # breadth is demand-gated, 10 §8). Keyed by the connector's `reply_ref`.
  use Agent

  def start_link(_ \\ []), do: Agent.start_link(fn -> %{} end, name: __MODULE__)

  def reply(reply_ref, payload) do
    ensure_started()
    Agent.update(__MODULE__, fn m -> Map.update(m, reply_ref, [payload], &[payload | &1]) end)
  end

  @doc "All replies delivered to `reply_ref`, oldest-first."
  def collect(reply_ref) do
    ensure_started()
    Agent.get(__MODULE__, fn m -> (Map.get(m, reply_ref, []) |> Enum.reverse()) end)
  end

  def clear do
    ensure_started()
    Agent.update(__MODULE__, fn _ -> %{} end)
  end

  defp ensure_started do
    case Process.whereis(__MODULE__) do
      nil -> start_link()
      _ -> :ok
    end
  end
end
