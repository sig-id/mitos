defmodule Mitos.Connector.Webhook do
  @moduledoc """
  The `webhook` connector (10 §3.1) — an inbound HTTP POST becomes a Task. This
  is the generic, platform-level ingress class for a paid job (10 §8).

  The connector parses and (optionally) authenticates the delivery; the HTTP
  transport lives in `Mitos.Web.Router` (the `POST /ingest/...` surface), which
  calls `Mitos.IO.ingest/3`. Authentication is an HMAC-SHA256 over the raw body
  with a shared secret held in the control-plane vault (`01 §7`); a binding with
  no secret is unauthenticated (dev only).
  """

  @behaviour Mitos.Connector

  @impl true
  def mount(config), do: {:ok, Map.new(config)}

  @impl true
  def receive(handle, %{body: body, headers: headers} = delivery) do
    # HMAC verification is optional but, when a secret is configured on the
    # binding (handle), rejecting here is the ingress trust gate (10 §8 Q3).
    with :ok <- verify(Map.merge(delivery, %{secret: handle && handle[:secret]})) do
      {:ok, [%{body: body, headers: headers, delivery_id: delivery[:id]}]}
    end
  end

  def receive(_handle, body) when is_map(body), do: {:ok, [%{body: body, headers: %{}}]}

  @impl true
  def normalize(%{body: body, headers: headers, delivery_id: id}) do
    intent = body["intent"] || body["x"] || "webhook request"
    inputs = body["inputs"] || Map.drop(body, ["intent"])

    key =
      headers["x-idempotency-key"] || headers["X-Idempotency-Key"] || id ||
        Mitos.sha256(Mitos.JSON.encode!(body))

    {:ok,
     %{
       intent: intent,
       inputs: inputs,
       workspace: body["workspace"],
       reply_ref: body["reply_ref"],
       idempotency_key: key
     }}
  end

  @impl true
  def ack(_handle, _signal, _outcome), do: :ok

  @impl true
  def capabilities,
    do: %{ingress: true, round_trip?: false, auth: :hmac_optional, scopes: [:webhook]}

  # No secret ⇒ unauthenticated (dev only). A configured secret ⇒ the caller
  # must present `x-mitos-signature` = hex HMAC-SHA256(secret, canonical body).
  defp verify(%{secret: nil}), do: :ok

  defp verify(%{secret: secret, body: body, headers: headers}) do
    expected = hmac(secret, body)
    given = headers["x-mitos-signature"] || headers["X-Mitos-Signature"]
    if given && Plug.Crypto.secure_compare(expected, given), do: :ok, else: {:error, :bad_signature}
  end

  defp verify(_), do: :ok

  defp hmac(secret, body), do: :crypto.mac(:hmac, :sha256, secret, Mitos.canonical_json(body)) |> Base.encode16(case: :lower)
end
