defmodule Mitos.Sinks.Reply do
  @moduledoc """
  The `reply` sink — round-trip back to the originating connector
  (`spec/10_IO_PLANE.md` §5.4).

  A connector (chat, webhook, email) carries a `reply_ref` onto the Task at
  ingress; this sink delivers the run's result *back* to that ref.

  * **HTTP `reply_ref`** (set by the `webhook` connector from the inbound
    `callback_url`) — the result is POSTed to that URL, closing the
    connector⇄sink loop for real.
  * **Non-HTTP `reply_ref`** (a chat thread id / email message-id) — the
    round-trip is registered as `sent` with `transport: "pending"`: the ref is
    carried on the receipt so the matching chat/email connector can complete the
    delivery when it is bound (`10 §5.4`: a round-trip connector backs the reply
    sink). Until those connectors land, the receipt is the auditable record that
    the reply was accepted for round-trip.

  The destination is the Task's `reply_ref`, resolved by `Mitos.IOPlane` from
  `binding.reply_to == "input.reply_ref"`.
  """

  @behaviour Mitos.Sink

  @timeout_ms 5_000

  @impl true
  def capabilities, do: %{egress: true, round_trip?: true, retries: 1}

  @impl true
  def bind(binding) when is_map(binding), do: {:ok, binding}

  @impl true
  def deliver(handle, payload) do
    reply_ref = reply_ref(handle, payload)

    cond do
      reply_ref in [nil, ""] ->
        {:skipped, %{"sink" => "reply", "reason" => "no reply_ref"}}

      not http_ref?(reply_ref) ->
        {:ok,
         %{
           "sink" => "reply",
           "destination" => reply_ref,
           "status" => "sent",
           "transport" => "pending"
         }}

      true ->
        post_reply(reply_ref, payload)
    end
  end

  defp post_reply(reply_ref, payload) do
    body =
      Jason.encode!(%{
        "kind" => "reply",
        "app" => payload["app_name"],
        "entrypoint" => payload["entrypoint"],
        "run_id" => payload["run_id"],
        "summary" => payload["summary"],
        "status" => payload["status"],
        "result" => payload["result"],
        "proof_excerpt" => payload["proof_excerpt"]
      })

    case Mitos.Http.post(reply_ref, body, [], timeout_ms: @timeout_ms) do
      {:ok, %{status: status}} when status in 200..299 ->
        {:ok,
         %{
           "sink" => "reply",
           "destination" => redact(reply_ref),
           "status" => "sent",
           "transport" => "http",
           "http_status" => status
         }}

      {:ok, %{status: status}} ->
        {:error, %{"sink" => "reply", "destination" => redact(reply_ref), "http_status" => status}}

      {:error, reason} ->
        {:error, %{"sink" => "reply", "destination" => redact(reply_ref), "reason" => inspect(reason)}}
    end
  end

  defp reply_ref(handle, payload) do
    cond do
      handle["reply_to"] not in [nil, "", "input.reply_ref"] -> handle["reply_to"]
      true -> Map.get(payload, "reply_ref")
    end
  end

  defp http_ref?(ref) do
    String.starts_with?(ref, ["http://", "https://"])
  end

  defp redact(destination) do
    uri = URI.parse(destination)
    URI.to_string(%{uri | userinfo: nil})
  end
end
