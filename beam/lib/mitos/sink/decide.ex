defmodule Mitos.Sink.Decide do
  @moduledoc """
  The `decide` sink (`10 §5.2`, `15 §5.1`) — deliver a decision card to a
  destination that can express a structured choice: a push app, a chat with
  inline buttons, email with resolution links, the console, the CLI.

  This is why pushing decisions to a phone costs almost no new architecture: it
  is the `10 §5.4` round trip with a card as the payload, and `Mitos.Sink` (a
  render over folded state, an Effect per delivery, idempotent at-least-once)
  already describes exactly that.

  The render obeys `15 §3.1`'s 60-second rule structurally rather than by
  convention — question, options with consequences, cost of being wrong,
  default and countdown when one exists. There is no field through which a
  transcript could reach the card, so the anti-pattern in `15 §3.2` ("a card
  that asks the human to read the run and decide what to do") is not available
  to a caller who wanted it.
  """

  @behaviour Mitos.Sink

  alias Mitos.Decision.Card

  @impl true
  def bind(config), do: {:ok, Map.new(config)}

  @impl true
  # Cards are pushed by the decision driver when they are raised, not swept out
  # of the log on a lifecycle Effect — but declaring the match keeps the sink
  # usable from the ordinary delivery driver too.
  def match(%Mitos.Event.Effect{type: t}), do: t == :approval_requested

  @impl true
  def render(%{card: %Card{} = card} = ctx, binding) do
    {:ok,
     %{
       to: binding[:to],
       decision_id: card.decision_id,
       run_id: card.run_id,
       kind: card.kind,
       reversibility: card.reversibility,
       # One line, imperative, readable on a lock screen (11 §4a).
       question: card.question,
       # Folded state — what was attempted, what is blocked. Never a transcript.
       context: %{
         attempted: card.context.attempted,
         blocked_by: card.context.blocked_by,
         evidence: length(card.context.evidence_refs)
       },
       options: Enum.map(card.options, &option/1),
       # An `external` card says "waiting for you" and means it (15 §4).
       default: card.default,
       ttl_ms: card.ttl_ms,
       waiting_for_you: is_nil(card.default),
       cost_of_wrong: card.cost_of_wrong,
       blast_radius: card.blast_radius,
       cut: ctx[:cut] || :dev,
       text: text(card)
     }}
  end

  def render(_ctx, _binding), do: {:error, :not_a_card}

  @impl true
  def deliver(handle, payload) do
    dest = (handle && handle[:to]) || payload[:to] || "operator"
    Mitos.Sink.Decide.Destination.deliver(dest, payload)
    {:ok, %{dest: dest, kind: :decide, decision_id: payload.decision_id}}
  end

  @impl true
  def capabilities, do: %{egress: true, round_trip?: true, retries: 3}

  @doc "The card as plain text — the lowest-common-denominator transport (SMS, CLI, email)."
  def text(%Card{} = card) do
    options =
      Enum.map_join(card.options, "\n", fn o ->
        mark = if o.recommended, do: " (recommended)", else: ""
        default = if o.id == card.default, do: " [default]", else: ""
        "  [#{o.id}] #{o.label}#{mark}#{default}#{consequence(o)}"
      end)

    """
    #{card.question}

    Tried: #{card.context.attempted || "-"}
    Blocked: #{card.context.blocked_by || "-"}

    #{options}

    If wrong: #{cost(card)}
    #{footer(card)}
    """
  end

  defp consequence(%{consequence: ""}), do: ""
  defp consequence(%{consequence: c}), do: " — #{c}"

  defp cost(%Card{cost_of_wrong: c}) do
    recoverable = if c.recoverable, do: "recoverable", else: "NOT recoverable"
    "#{div(c.wasted_wall_ms, 60_000)}m and $#{Float.round(c.wasted_usd * 1.0, 2)} of work, #{recoverable}"
  end

  defp footer(%Card{default: nil}), do: "Waiting for you — nothing happens until you answer."

  defp footer(%Card{default: d, ttl_ms: ttl}),
    do: "No answer in #{div(ttl, 60_000)}m defaults to [#{d}]."

  defp option(o), do: %{id: o.id, label: o.label, consequence: o.consequence, recommended: o.recommended}
end

defmodule Mitos.Sink.Decide.Destination do
  @moduledoc false
  # Pluggable card transport, mirroring `Sink.Notify.Destination`. Tests read the
  # collector; a deployment wires a push app or a chat client with inline
  # buttons. The protocol is the product; the app is a client (15 §5.1).
  use Agent

  def start_link(_ \\ []), do: Agent.start_link(fn -> %{} end, name: __MODULE__)

  def deliver(dest, payload) do
    ensure_started()
    Agent.update(__MODULE__, fn m -> Map.update(m, dest, [payload], &[payload | &1]) end)
  end

  @doc "Cards delivered to `dest`, oldest-first."
  def collect(dest) do
    ensure_started()
    Agent.get(__MODULE__, fn m -> Map.get(m, dest, []) |> Enum.reverse() end)
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
