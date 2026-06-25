defmodule Mitos.Sinks.Notify do
  @moduledoc """
  The `notify` sink — a human-facing "your job is done" message (`spec/10_IO_PLANE.md`
  §5.2).

  In v1 (local runner, no chat/email connectors yet) `notify` delivers a compact
  summary to a webhook URL when one is bound — identical mechanics to `deliver`
  but a presentation payload (title + summary + run link). Without a destination
  it records a control-plane notification receipt so the web app can surface it
  (`10 §6`). When real `chat`/`email` connectors land, this sink routes through
  the matching connector's round-trip path.
  """

  @behaviour Mitos.Sink

  @timeout_ms 5_000

  @impl true
  def capabilities, do: %{egress: true, round_trip?: false, retries: 1}

  @impl true
  def bind(binding) when is_map(binding), do: {:ok, binding}

  @impl true
  def deliver(handle, payload) do
    destination = destination(handle)

    message = %{
      "title" => title(handle, payload),
      "summary" => payload["summary"],
      "app" => payload["app_name"],
      "entrypoint" => payload["entrypoint"],
      "run_id" => payload["run_id"],
      "status" => payload["status"],
      "proof_excerpt" => payload["proof_excerpt"]
    }

    case destination do
      nil ->
        {:ok,
         %{
           "sink" => "notify",
           "destination" => "control-plane",
           "status" => "sent",
           "channel" => "web"
         }}

      dest ->
        body = Jason.encode!(message)

        case Mitos.Http.post(dest, body, [], timeout_ms: @timeout_ms) do
          {:ok, %{status: status}} when status in 200..299 ->
            {:ok,
             %{
               "sink" => "notify",
               "destination" => redact(dest),
               "status" => "sent",
               "http_status" => status
             }}

          {:ok, %{status: status}} ->
            {:error, %{"sink" => "notify", "destination" => redact(dest), "http_status" => status}}

          {:error, reason} ->
            {:error, %{"sink" => "notify", "destination" => redact(dest), "reason" => inspect(reason)}}
        end
    end
  end

  defp destination(binding) do
    clean(binding["url"]) || clean(binding["destination"]) || clean(binding["channel"])
  end

  defp title(handle, payload) do
    cond do
      is_binary(handle["title"]) -> handle["title"]
      is_binary(payload["app_name"]) -> "#{payload["app_name"]} run finished"
      true -> "Mitos run finished"
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
