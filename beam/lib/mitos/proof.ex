defmodule Mitos.Proof do
  @moduledoc """
  The buyer-facing proof page (08 §5) — a redacted render of the run's Effect
  excerpt. **Generated from Effect events only**: Decision `rationale` is
  audit-only (01 §8.1) and leaks recipe, so Decisions are excluded wholesale.

  Shows what happened: timeline, egress crossings (dest only), token/charge
  totals, artifact manifest, wall time, log-hash anchor.
  Hides the recipe: skill/prompts/rationale, tool source, dataset contents,
  **which harness ran** (01 §7 princ. 7), seller keys (never on the log).

  The deliverable (08 §5 DELIVERY) is the sibling read: harvested diff +
  RESULT + registered artifacts.
  """

  alias Mitos.Event.Effect

  # Effect types whose occurrence is buyer-visible on the timeline.
  @timeline_types ~w(
    run_started capability_started iteration_started iteration_finished
    egress_allowed egress_denied approval_requested approval_resolved
    artifact_registered capability_finished budget_charged run_finished
    delivery_sent
  )a

  @doc "Render the proof for a run. `charge` (a Mitos.Meter fold) is optional."
  def render(run_id, charge \\ nil) do
    effects = effects(run_id)

    %{
      "run_id" => run_id,
      "status" => status(effects),
      "timeline" => timeline(effects),
      "egress" => egress(effects),
      "tools_brokered" => Enum.count(effects, &(&1.type == :tool_brokered)),
      "token_totals" => token_totals(effects),
      "artifacts" => artifact_manifest(effects),
      "wall_ms" => wall_ms(effects),
      "result" => result(effects),
      "charge" => charge && public_charge(charge),
      # replay/audit anchor: recompute-able by a third party given the excerpt
      "log_hash" => log_hash(effects)
    }
  end

  @doc "The delivery set (08 §5): workspace diff + RESULT + registered artifacts."
  def deliverable(run_id) do
    effects = effects(run_id)

    %{
      "run_id" => run_id,
      "status" => status(effects),
      "result" => result(effects),
      "diff" => diff_patch(run_id, effects),
      "artifacts" => artifact_manifest(effects)
    }
  end

  @doc "Read one registered artifact's bytes from the run's content-addressed store."
  def artifact(run_id, sha256) do
    if sha256 =~ ~r/\A[a-f0-9]{64}\z/ do
      File.read(Path.join([Mitos.run_dir(run_id), "artifacts", sha256]))
    else
      {:error, :bad_ref}
    end
  end

  # --- renders (each a pure fold over Effects) ---

  defp effects(run_id) do
    run_id |> Mitos.Log.read() |> Enum.filter(&match?(%Effect{}, &1))
  end

  defp status(effects) do
    effects
    |> Enum.reverse()
    |> Enum.find_value("running", fn
      %Effect{type: :run_finished, payload: p} -> p["status"]
      _ -> nil
    end)
  end

  defp timeline(effects) do
    effects
    |> Enum.filter(&(&1.type in @timeline_types))
    |> Enum.map(fn e ->
      %{"seq" => e.seq, "ts" => e.ts, "type" => to_string(e.type)}
      |> put_if("stage", e.ref["stage_id"])
      |> put_if("dest", e.ref["dest"])
      |> put_if("sink", e.ref["sink"])
    end)
  end

  defp egress(effects) do
    for %Effect{type: t} = e when t in [:egress_allowed, :egress_denied] <- effects do
      %{
        "ts" => e.ts,
        "dest" => e.ref["dest"],
        "outcome" => to_string(t),
        "bytes" => e.payload["bytes"] || 0
      }
    end
  end

  defp token_totals(effects) do
    effects
    |> Enum.filter(&(&1.type == :budget_charged))
    |> Enum.reduce(%{"tokens" => 0, "usd" => 0.0, "wall_ms" => 0}, fn e, acc ->
      %{
        "tokens" => acc["tokens"] + (e.payload["tokens"] || 0),
        "usd" => acc["usd"] + (e.payload["usd"] || 0.0),
        "wall_ms" => acc["wall_ms"] + (e.payload["wall_ms"] || 0)
      }
    end)
  end

  defp artifact_manifest(effects) do
    for %Effect{type: :artifact_registered} = e <- effects do
      %{
        "artifact" => e.ref["artifact"],
        "sha256" => e.ref["sha256"],
        "path" => e.ref["path"],
        "bytes" => e.payload["bytes"]
      }
    end
  end

  defp wall_ms(effects) do
    with [first | _] <- effects,
         last <- List.last(effects),
         {:ok, t0, _} <- DateTime.from_iso8601(first.ts),
         {:ok, t1, _} <- DateTime.from_iso8601(last.ts) do
      DateTime.diff(t1, t0, :millisecond)
    else
      _ -> 0
    end
  end

  # The RESULT ships to the buyer (it is the deliverable's core, 08 §5) — it is
  # capability-authored output, not recipe.
  defp result(effects) do
    effects
    |> Enum.reverse()
    |> Enum.find_value(fn
      %Effect{type: :capability_finished, payload: p} -> p["result"]
      _ -> nil
    end)
  end

  defp diff_patch(run_id, effects) do
    effects
    |> Enum.filter(&(&1.type == :artifact_registered and &1.ref["kind"] == "diff"))
    |> Enum.map(& &1.ref["sha256"])
    |> Enum.map(fn sha ->
      case artifact(run_id, sha) do
        {:ok, patch} -> patch
        _ -> ""
      end
    end)
    |> Enum.join("\n")
  end

  # Buyer sees the money line, never the split internals beyond their charge.
  defp public_charge(charge) do
    %{
      "buyer_charge_usd" => charge.buyer_charge,
      "delivered" => charge.delivered?,
      "refund_usd" => charge.buyer_refund,
      "compute_ms" => charge.compute_ms
    }
  end

  # sha256 over the serialized Effect excerpt — the on-chain-anchorable hash
  # (08 §7): given the excerpt, any third party recomputes this.
  defp log_hash(effects) do
    effects
    |> Enum.map(&Mitos.Event.to_line/1)
    |> Enum.join()
    |> Mitos.sha256()
  end

  defp put_if(map, _k, nil), do: map
  defp put_if(map, k, v), do: Map.put(map, k, v)
end
