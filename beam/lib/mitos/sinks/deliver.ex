defmodule Mitos.Sinks.Deliver do
  @moduledoc """
  The `deliver` sink — POST the run's outcome `{result, artifacts}` to a webhook
  URL (`spec/10_IO_PLANE.md` §5.2).

  Destination comes from the binding (`url`, `path`, or `destination`). The
  payload rendered by `Mitos.IOPlane` is JSON-encoded and POSTed with a bounded
  timeout. The receipt records the HTTP status and the (redacted) destination.
  A non-2xx response is a delivery failure (`delivery_failed` Effect); a network
  error is also a failure. Re-delivery is handled by the idempotent redrive path
  (`Mitos.Applications.redrive_deliveries/2`).
  """

  @behaviour Mitos.Sink

  @timeout_ms 5_000

  @impl true
  def capabilities, do: %{egress: true, round_trip?: false, retries: 1}

  @impl true
  def bind(binding) when is_map(binding) do
    case destination(binding) do
      nil -> {:error, :no_sink_destination}
      dest -> {:ok, %{destination: dest, headers: headers(binding)}}
    end
  end

  @impl true
  def deliver(%{destination: destination, headers: headers}, payload) do
    body = Jason.encode!(payload)

    case Mitos.Http.post(destination, body, headers, timeout_ms: @timeout_ms) do
      {:ok, %{status: status, body: resp_body}} when status in 200..299 ->
        {:ok,
         %{
           "sink" => "deliver",
           "destination" => redact(destination),
           "status" => "sent",
           "http_status" => status,
           "response_bytes" => byte_size(resp_body)
         }}

      {:ok, %{status: status}} ->
        {:error, %{"sink" => "deliver", "destination" => redact(destination), "http_status" => status}}

      {:error, reason} ->
        {:error, %{"sink" => "deliver", "destination" => redact(destination), "reason" => inspect(reason)}}
    end
  end

  def deliver(_handle, _payload), do: {:error, :sink_not_bound}

  defp destination(binding) do
    clean(binding["url"]) || clean(binding["destination"]) ||
      webhook_path(binding["path"])
  end

  defp webhook_path(path) when is_binary(path) do
    cond do
      String.starts_with?(path, ["http://", "https://"]) -> path
      true -> nil
    end
  end

  defp webhook_path(_path), do: nil

  defp headers(binding) do
    case binding["headers"] do
      headers when is_list(headers) -> headers
      headers when is_map(headers) -> Map.to_list(headers)
      _other -> []
    end
  end

  defp clean(value) when is_binary(value) do
    case String.trim(value) do
      "" -> nil
      trimmed -> trimmed
    end
  end

  defp clean(_value), do: nil

  defp redact(destination) do
    uri = URI.parse(destination)

    URI.to_string(%{uri | userinfo: nil})
  end
end
