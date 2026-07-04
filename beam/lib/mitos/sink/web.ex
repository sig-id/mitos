defmodule Mitos.Sink.Web do
  @moduledoc """
  The `web` sink (10 §5.2, §6) — the web app surface *as a sink*: the live run
  trace and the deliverable page (`11 §4`, `11 §5`). Treating the web app as I/O
  (not a privileged client) keeps `01 §10.11` true and means the watch/report
  experience is a rendering of the same plane a webhook POST uses.

  The web sink does not POST anywhere — its "delivery" is the URL at which the
  outcome is already served (`/runs/{run_id}/deliverable`, `/web/runs/{run_id}/
  report`). Recording a `delivery_sent` here puts "the web surface delivered
  this" on the log, so every sink class — including the web app — is uniformly
  auditable.
  """

  @behaviour Mitos.Sink

  @impl true
  def bind(config), do: {:ok, Map.new(config)}

  @impl true
  def match(%Mitos.Event.Effect{type: t}), do: t in [:capability_finished, :run_finished]

  @impl true
  def render(%{run_id: run_id, cut: cut}, _binding) do
    deliverable = Mitos.Proof.deliverable(run_id)

    {:ok,
     %{
       run_id: run_id,
       deliverable_url: "/runs/#{run_id}/deliverable",
       proof_url: "/runs/#{run_id}/proof",
       status: deliverable["status"],
       cut: cut
     }}
  end

  @impl true
  def deliver(_handle, payload),
    do: {:ok, %{kind: :web, urls: [payload[:deliverable_url], payload[:proof_url]]}}

  @impl true
  def capabilities, do: %{egress: true, round_trip?: true, retries: 0}
end
