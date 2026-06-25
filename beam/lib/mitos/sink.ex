defmodule Mitos.Sink do
  @moduledoc """
  Platform-level egress adapter behaviour — the output side of the I/O plane
  (`spec/10_IO_PLANE.md` §5).

  A sink delivers a run's outcome to an external destination, triggered by a
  run/Task lifecycle Effect. Sinks read **folded run state + harvested artifacts
  only** (the same source as the proof page, `08 §7`) and run in the **BEAM
  control plane**, never inside the sandbox (`10 §5.1`). Sink credentials live
  in the control-plane vault (`01 §8.5`).

  A sink is **not** the in-sandbox egress broker (`05 §5.3`): the broker governs
  a capability's own network calls *during* the run; a sink governs delivery of
  finished work *after* harvest.

  ## Callbacks

  * `capabilities/0`   — static declaration (`round_trip?`, `retries`).
  * `bind/1`           — bind destination + credentials into an opaque handle.
  * `deliver/2`        — perform the delivery; return a receipt or an error.

  Rendering (`render/2`) is centralized in `Mitos.IOPlane` for v1 so every sink
  ships the same canonical `{result, artifacts, proof_excerpt}` payload; a sink
  MAY override presentation inside `deliver/2`. `Mitos.Sink.Web` is the
  reference no-op sink (the web app is its own surface, `10 §6`).

  Every delivery becomes an Effect on the log — `delivery_attempted` is written
  by `Mitos.IOPlane` before delivery; on success it writes `delivery_sent` with
  the sink's receipt, on failure `delivery_failed` (`10 §5.3`). Delivery is
  at-least-once and idempotent: a `delivery_sent` keyed by `(run_id, sink, dest)`
  suppresses duplicates on re-drive.
  """

  @type handle :: term()
  @type payload :: map()
  @type receipt :: map()

  @callback capabilities() :: %{
              required(:egress) => true,
              required(:round_trip?) => boolean(),
              optional(:retries) => non_neg_integer()
            }

  @callback bind(binding :: map()) :: {:ok, handle} | {:error, term()}

  @callback deliver(handle, payload) :: {:ok, receipt} | {:error, term()}

  @doc "Resolve a sink name to its adapter module, or `nil` when unregistered."
  def lookup(name) when is_binary(name) do
    Map.get(registry(), normalize_name(name))
  end

  def lookup(_name), do: nil

  @doc "Registered sink names."
  def names, do: Map.keys(registry()) |> Enum.sort()

  defp registry do
    %{
      "web" => Mitos.Sinks.Web,
      "deliver" => Mitos.Sinks.Deliver,
      "notify" => Mitos.Sinks.Notify,
      "reply" => Mitos.Sinks.Reply
    }
  end

  defp normalize_name(name) do
    name |> to_string() |> String.downcase() |> String.trim()
  end
end
