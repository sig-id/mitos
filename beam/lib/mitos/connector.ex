defmodule Mitos.Connector do
  @moduledoc """
  The ingress behaviour (10 §3.2) — a platform-level adapter that turns an
  external signal into a unit of mitos work.

  A connector never runs a capability directly: it normalizes an inbound
  delivery into an **intake** (`%{intent, inputs, workspace?, reply_ref?,
  idempotency_key}`) and hands it to `Mitos.IO.ingest/3`, which dedups on the
  idempotency key, selects a pipeline (10 §3.3), enqueues a Task, and starts the
  run. One pool, many provenances; `task.source` carries `connector:<name>`
  (`01 §8.7`).

  Connector credentials live in the **control-plane vault** (`01 §7`), the same
  custody rule as the broker — they are platform secrets and **never enter the
  sandbox**. A connector is distinct from a trigger (`06 §6.1`): it is a *source*
  that produces Tasks/events the service runtime acts on.

  The web app is one connector of class `manual`/`web` (10 §6) — not privileged.
  """

  @type handle :: term
  @type signal :: term
  @type intake :: %{
          required(:intent) => String.t(),
          required(:inputs) => map,
          optional(:workspace) => term,
          optional(:reply_ref) => term,
          optional(:reply_to) => term,
          required(:idempotency_key) => String.t()
        }

  @doc "Bind credentials (control-plane vault), endpoints, scope. Idempotent."
  @callback mount(config :: map) :: {:ok, handle} | {:error, term}

  @doc "Parse + authenticate an inbound delivery into zero or more signals."
  @callback receive(handle, raw :: term) :: {:ok, [signal]} | {:error, term}

  @doc "Normalize a signal into the canonical intake (10 §3.2). MUST be deterministic."
  @callback normalize(signal) :: {:ok, intake} | {:error, term}

  @doc "Protocol-level ack to the source (HTTP 200, Slack ack, …). `outcome` is the ingest result."
  @callback ack(handle, signal, outcome :: term) :: :ok | {:ok, term}

  @doc "Declares ingress class: `round_trip?` true ⇒ can back a `reply` sink (10 §5.4)."
  @callback capabilities() :: %{
            required(:ingress) => true,
            required(:round_trip?) => boolean,
            optional(:auth) => term,
            optional(:scopes) => term
          }

  @optional_callbacks [mount: 1, ack: 3]
end
