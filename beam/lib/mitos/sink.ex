defmodule Mitos.Sink do
  @moduledoc """
  The egress behaviour (10 §5.3) — delivers a run's outcome to an external
  destination, triggered by a run/Task lifecycle Effect (`01 §8.2`).

  A sink is **not** the in-sandbox egress broker (10 §5.1): the broker carries a
  capability's own network calls *during* the run, from inside the sandbox, with
  secrets attached at the edge; a sink carries the run's **outcome** *after*
  harvest, from the BEAM control plane, reading the folded log + harvested
  artifacts. A sink never reaches into the sandbox and is never an authority path
  for the capability.

  Redaction is enforced at the data layer (01 §4): `render/2` reads a strict
  Effect-subset fold (`Mitos.Proof`, commercial cut) for buyer-facing renders —
  `rationale` and harness identity are physically incapable of reaching it.
  Dev/platform renders MAY read more.

  Every delivery is an Effect (`delivery_attempted | delivery_sent |
  delivery_failed`) on the run's log, and delivery is **at-least-once with
  idempotency**: a `delivery_sent` keyed by `(run_id, sink, dest)` suppresses
  duplicates on re-drive (10 §5.3, cf. `04 §7.1`).
  """

  @type handle :: term

  @doc "Bind credentials (control-plane vault), destination, scope. Idempotent."
  @callback bind(config :: map) :: {:ok, handle} | {:error, term}

  @doc "Does this Effect trigger delivery? (e.g. `capability_finished`, `run_finished`)"
  @callback match(effect :: Mitos.Event.Effect.t()) :: boolean

  @doc """
  Fold the log + fetch artifacts into a destination-shaped payload. `ctx`
  carries the folded run state, the redaction `cut` (`:commercial | :dev`), and
  the binding. Commercial renders MUST be re-derivable from Effects only.
  """
  @callback render(ctx :: map, binding :: map) :: {:ok, term} | {:error, term}

  @doc "Perform the outbound delivery; returns a receipt for the `delivery_sent` Effect."
  @callback deliver(handle, term) :: {:ok, receipt :: term} | {:error, reason :: term}

  @callback capabilities() :: %{
              required(:egress) => true,
              required(:round_trip?) => boolean,
              required(:retries) => non_neg_integer
            }

  @optional_callbacks [bind: 1]

  @doc """
  Helper for sinks: the buyer-facing (commercial) render source — a fold over
  Effects only, redacted by construction (`Mitos.Proof`). Sinks call this for
  `:commercial` cuts and never re-implement redaction.
  """
  def commercial_render(run_id, charge),
    do: Mitos.Proof.render(run_id, charge)
end
