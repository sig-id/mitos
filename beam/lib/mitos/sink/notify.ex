defmodule Mitos.Sink.Notify do
  @moduledoc """
  The `notify` sink (10 §5.2) — a human-facing "your job is done" message. The
  destination is a notifier handle (chat channel, email, push) configured on the
  binding; the payload is a short summary derived from the Effect-only fold.

  Credentials live in the control-plane vault (`01 §7`). The MVP realization
  dispatches through a pluggable in-process notifier so delivery is
  observable/testable without a chat platform — chat breadth is commodity and
  demand-gated (10 §8).
  """

  @behaviour Mitos.Sink

  @impl true
  def bind(config), do: {:ok, Map.new(config)}

  @impl true
  def match(%Mitos.Event.Effect{type: t}), do: t in [:capability_finished, :run_finished]

  @impl true
  def render(%{cut: cut, run_id: run_id, state: state, charge: charge}, binding) do
    proof = Mitos.Sink.commercial_render(run_id, charge)

    subject = binding[:subject] || "run #{run_id} finished"

    message =
      case state.status do
        :finished -> "#{subject}: #{summarize(proof)}"
        other -> "#{subject}: #{other}"
      end

    {:ok, %{to: binding[:to], subject: subject, body: message, cut: cut}}
  end

  defp summarize(proof) do
    "delivered #{length(proof["artifacts"])} artifact(s); #{proof["token_totals"]["tokens"]} tokens"
  end

  @impl true
  def deliver(handle, payload) do
    dest = (handle && handle[:to]) || payload[:to] || payload.to
    Mitos.Sink.Notify.Destination.notify(dest, payload)
    {:ok, %{dest: dest, kind: :notify}}
  end

  @impl true
  def capabilities, do: %{egress: true, round_trip?: false, retries: 2}
end

defmodule Mitos.Sink.Notify.Destination do
  @moduledoc false
  # Pluggable notifier dispatch. Tests register a collector; production wires a
  # chat/email/push client. This keeps the sink out of the delivery-transport
  # business (10 §8: chat breadth is demand-gated).
  use Agent

  def start_link(_ \\ []), do: Agent.start_link(fn -> %{} end, name: __MODULE__)

  def notify(dest, payload) do
    ensure_started()
    Agent.update(__MODULE__, fn m -> Map.update(m, dest, [payload], &[payload | &1]) end)
  end

  @doc "All notifications delivered to `dest`, oldest-first."
  def collect(dest) do
    ensure_started()
    Agent.get(__MODULE__, fn m -> (Map.get(m, dest, []) |> Enum.reverse()) end)
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
