defmodule Mitos.Connectors.Webhook do
  @moduledoc """
  Generic inbound-HTTP connector — the I/O plane's first real ingress class
  (`spec/10_IO_PLANE.md` §3, §8 "first cut: `webhook` + `manual`/`web`").

  An external system POSTs an arbitrary JSON payload to a bound webhook path
  (served by `Mitos.Web.Router` at `/hooks/...`). This connector authenticates
  nothing by default (the platform front-ends TLS + optional shared-secret
  verification at the route), parses the body, and normalizes it into the Task
  intake shape.

  ## Normalization contract

  The body is accepted in either of two shapes:

  1. **Explicit mitos shape** — `{inputs, parameters?, workspace?, reply_ref?,
     idempotency_key?}`. Passed through verbatim; this is the shape the web app
     and internal callers use.
  2. **Opaque webhook payload** — any other JSON object. Known fields are mapped
     (`event`/`type`/`action` → intent, `data`/`payload`/`inputs` → inputs, a
     `callback_url`/`reply_url` → reply_ref) and an idempotency key is derived
     from a delivery/event id header or field, else from the source + a hash of
     the body so re-delivery dedupes.

  The idempotency key guarantees a re-POSTed webhook does not create a duplicate
  Task (`10 §3.2`); `Mitos.Applications.run_connector/4` enforces it on enqueue.
  """

  @behaviour Mitos.Connector

  @idempotency_headers ~w(x-mitos-delivery-id x-delivery-id x-event-id webhook-id delivery-id)
  @idempotency_fields ~w(id delivery_id event_id idempotency_key message_id)
  @reply_fields ~w(callback_url reply_url response_url webhook_url)

  @impl true
  def capabilities do
    %{ingress: true, round_trip?: true, auth: :shared_secret, scopes: ["webhook:receive"]}
  end

  @impl true
  def receive(_handle, raw, _opts) when is_map(raw), do: {:ok, raw}
  def receive(_handle, raw, _opts) when is_binary(raw), do: {:ok, raw}
  def receive(_handle, _raw, _opts), do: {:error, {:invalid_webhook_body, "expected JSON object"}}

  @impl true
  def normalize(signal, opts) when is_map(signal) do
    headers = Keyword.get(opts, :headers, %{}) |> normalize_headers()
    source = Keyword.get(opts, :source)

    cond do
      explicit_mitos_shape?(signal) ->
        {:ok,
         %{
           "intent" => signal["intent"],
           "inputs" => signal["inputs"],
           "parameters" => signal["parameters"],
           "workspace" => signal["workspace"],
           "reply_ref" => signal["reply_ref"],
           "idempotency_key" => idempotency_key(signal, headers, source)
         }}

      true ->
        {:ok,
         %{
           "intent" => intent_from(signal),
           "inputs" => inputs_from(signal),
           "parameters" => nil,
           "workspace" => nil,
           "reply_ref" => reply_ref_from(signal, headers),
           "idempotency_key" => idempotency_key(signal, headers, source)
         }}
    end
    |> normalize_result()
  end

  def normalize(_signal, _opts), do: {:error, {:invalid_webhook_body, "expected JSON object"}}

  @impl true
  def ack(_handle, _signal, _outcome), do: :ok

  defp explicit_mitos_shape?(signal) do
    # The internal/contract shape carries an `inputs` object or an `intent`; an
    # opaque payload relying only on `data`/`payload` is treated as foreign.
    is_map(signal["inputs"]) or is_binary(signal["intent"])
  end

  defp intent_from(signal) do
    signal["intent"] || signal["event"] || signal["type"] || signal["action"] ||
      "webhook signal"
  end

  defp inputs_from(signal) do
    cond do
      is_map(signal["inputs"]) -> signal["inputs"]
      is_map(signal["data"]) -> signal["data"]
      is_map(signal["payload"]) -> signal["payload"]
      is_map(signal["body"]) -> signal["body"]
      true -> drop_meta(signal)
    end
  end

  defp drop_meta(signal) do
    signal
    |> Map.drop([
      "intent",
      "event",
      "type",
      "action",
      "callback_url",
      "reply_url",
      "response_url",
      "webhook_url"
    ])
    |> (fn map -> if map_size(map) == 0, do: nil, else: map end).()
  end

  defp reply_ref_from(signal, headers) do
    Enum.find_value(@reply_fields, fn field -> clean(signal[field]) end) ||
      clean(headers["x-mitos-reply-ref"]) ||
      clean(headers["x-reply-to"])
  end

  defp idempotency_key(signal, headers, source) do
    header_key =
      Enum.find_value(@idempotency_headers, fn name -> clean(headers[name]) end)

    field_key =
      Enum.find_value(@idempotency_fields, fn field -> clean(signal[field]) end)

    cond do
      key = header_key || field_key -> "webhook:#{key}"
      true -> derive_key(signal, source)
    end
  end

  defp derive_key(signal, source) do
    body = :erlang.phash2(signal, 2 ** 32)
    suffix = Integer.to_string(body, 36)
    "webhook:#{source || "anon"}:#{suffix}"
  end

  defp normalize_headers(headers) when is_map(headers) do
    Map.new(headers, fn {key, value} ->
      {key |> to_string() |> String.downcase(), to_string(value)}
    end)
  end

  defp normalize_headers(headers) when is_list(headers) do
    Map.new(headers, fn {key, value} ->
      {key |> to_string() |> String.downcase(), to_string(value)}
    end)
  end

  defp normalize_headers(_headers), do: %{}

  defp clean(value) when is_binary(value) do
    case String.trim(value) do
      "" -> nil
      trimmed -> trimmed
    end
  end

  defp clean(_value), do: nil

  defp normalize_result({:ok, intake}) do
    {:ok,
     intake
     |> Enum.reject(fn {_key, value} -> is_nil(value) end)
     |> Map.new()}
  end

  defp normalize_result(other), do: other
end
