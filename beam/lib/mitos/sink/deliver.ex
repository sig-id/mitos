defmodule Mitos.Sink.Deliver do
  @moduledoc """
  The `deliver` sink (10 §5.2) — POST the artifact payload `{result, artifacts,
  proof_excerpt}` to a webhook URL. This is the machine-facing egress for a paid
  job: the buyer's callback receives the finished work.

  The destination URL + any auth header are configured on the binding (creds in
  the control-plane vault, `01 §7`); the payload is the Effect-only deliverable
  fold (`Mitos.Proof.deliverable/1`) plus a proof excerpt. Redaction is at the
  data layer by construction (01 §4): nothing in the payload is re-derived from
  Decisions or `rationale`.
  """

  @behaviour Mitos.Sink

  @impl true
  def bind(config), do: {:ok, Map.new(config)}

  @impl true
  def match(%Mitos.Event.Effect{type: t}), do: t in [:capability_finished, :run_finished]

  @impl true
  def render(%{run_id: run_id, cut: cut} = ctx, _binding) do
    deliverable = Mitos.Proof.deliverable(run_id)
    proof = Mitos.Sink.commercial_render(run_id, ctx[:charge])

    payload = %{
      run_id: run_id,
      status: deliverable["status"],
      result: deliverable["result"],
      artifacts: deliverable["artifacts"],
      proof_excerpt: Map.take(proof, ["timeline", "egress", "token_totals", "log_hash"]),
      cut: cut
    }

    {:ok, payload}
  end

  @impl true
  def deliver(handle, payload) do
    url = handle[:url] || handle["url"]
    post(url, payload, handle)
  end

  @impl true
  def capabilities, do: %{egress: true, round_trip?: false, retries: 3}

  defp post(nil, _payload, _handle), do: {:error, :no_url}

  defp post(url, payload, handle) do
    body = Mitos.JSON.encode!(payload)
    ct = String.to_charlist("application/json")
    headers = [{String.to_charlist("content-type"), ct} | auth_header(handle)]

    case :httpc.request(
           :post,
           {String.to_charlist(url), headers, ct, body},
           [{:timeout, 15_000}],
           body_format: :binary
         ) do
      {:ok, {{_, status, _}, _resp_headers, resp_body}} when status in 200..299 ->
        {:ok, %{url: url, status: status, receipt: resp_body}}

      {:ok, {{_, status, _}, _resp_headers, resp_body}} ->
        {:error, {:http, status, resp_body}}

      {:error, reason} ->
        {:error, {:transport, reason}}
    end
  end

  defp auth_header(handle) do
    case handle[:token] || handle["token"] do
      nil -> []
      token -> [{String.to_charlist("authorization"), String.to_charlist("Bearer " <> token)}]
    end
  end
end
