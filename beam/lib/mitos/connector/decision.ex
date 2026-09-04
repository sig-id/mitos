defmodule Mitos.Connector.Decision do
  @moduledoc """
  The `decision` connector (`10 §3.1`, `15 §5.2`) — the return half of the card
  round trip.

  It is the one connector that does **not** produce a Task. Every other
  connector turns an external signal into new work; this one applies a
  resolution to work that already exists and is parked. So `normalize/1`
  produces a resolution rather than an intake, and `ingest/2` routes it to
  `Mitos.Decision.resolve/2` rather than to the pool.

  The transport is untrusted. A button press carries no authority: the card's
  authority lives on the log, the resolver is authenticated against the run's
  tenant, and the substrate re-checks the act against the run's grants before
  executing. A forged press therefore cannot widen `blast_radius` — it can only
  be rejected, on the record, as `decision_rejected`.
  """

  @behaviour Mitos.Connector

  @impl true
  def mount(config), do: {:ok, Map.new(config)}

  @impl true
  def receive(_handle, raw) when is_map(raw), do: {:ok, [raw]}
  def receive(_handle, raw) when is_list(raw), do: {:ok, raw}
  def receive(_handle, _raw), do: {:error, :unrecognized_delivery}

  @impl true
  def normalize(signal) do
    s = Map.new(signal, fn {k, v} -> {to_string(k), v} end)

    case s["decision_id"] do
      nil ->
        {:error, :missing_decision_id}

      id ->
        {:ok,
         %{
           # `intent` and `inputs` keep the connector intake shape so the I/O
           # plane's dedup and provenance machinery applies unchanged.
           intent: "resolve decision #{id}",
           inputs: %{},
           decision_id: id,
           resolution: %{
             resolver: s["resolver"],
             tenant: s["tenant"],
             choice: s["choice"],
             value: s["value"],
             note: s["note"],
             via: s["via"] || "decision-connector"
           },
           idempotency_key: "decision:" <> to_string(id)
         }}
    end
  end

  @impl true
  def ack(_handle, _signal, _outcome), do: :ok

  @impl true
  def capabilities, do: %{ingress: true, round_trip?: true, auth: :resolver, scopes: [:resolve]}

  @doc """
  Apply an inbound resolution. Idempotent on `decision_id` — a re-delivered
  press finds the card already resolved and returns `{:error,
  :unknown_decision}` rather than applying twice.
  """
  def ingest(handle \\ %{}, raw) do
    with {:ok, [signal | _]} <- __MODULE__.receive(handle, raw),
         {:ok, intake} <- normalize(signal) do
      case Mitos.Decision.resolve(intake.decision_id, intake.resolution) do
        :ok -> {:ok, intake.decision_id}
        {:error, reason} -> {:error, reason}
      end
    end
  end
end
