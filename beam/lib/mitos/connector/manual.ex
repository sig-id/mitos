defmodule Mitos.Connector.Manual do
  @moduledoc """
  The `manual`/`web` connector (10 §3.1, §6) — an operator UI / API / CLI
  stating intent + inputs becomes a Task. The web app's order form is one
  instance of this connector: the web app is *not* a privileged client, just an
  I/O surface (`01 §10.11`).

  `raw` is already the intake fields (`%{intent, inputs, workspace?, reply_to?}`);
  the connector adds a deterministic idempotency key when the caller did not.
  """

  @behaviour Mitos.Connector

  @impl true
  def mount(_config), do: {:ok, nil}

  @impl true
  def receive(nil, raw) when is_map(raw), do: {:ok, [raw]}

  @impl true
  def normalize(raw) when is_map(raw) do
    {:ok,
     %{
       intent: raw[:intent] || raw["intent"] || "manual request",
       inputs: raw[:inputs] || raw["inputs"] || %{},
       workspace: raw[:workspace] || raw["workspace"],
       reply_ref: raw[:reply_ref] || raw["reply_ref"],
       reply_to: raw[:reply_to] || raw["reply_to"],
       idempotency_key:
         raw[:idempotency_key] || raw["idempotency_key"] ||
           Mitos.sha256(Mitos.canonical_json(raw))
     }}
  end

  @impl true
  def ack(nil, _signal, _outcome), do: :ok

  @impl true
  def capabilities, do: %{ingress: true, round_trip?: true, auth: :session, scopes: [:web, :cli]}
end
