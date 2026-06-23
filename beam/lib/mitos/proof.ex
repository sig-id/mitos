defmodule Mitos.Proof do
  @moduledoc """
  Buyer-facing proof fold over canonical Effect events.

  The proof surface is intentionally narrower than the full audit log: it uses
  Effect events only and never includes Decision rationale or prompt context.
  """

  @counted_effects ~w(
    tool_brokered
    egress_allowed
    egress_denied
    artifact_registered
    budget_charged
    commit
  )
  @post_finish_proof_effects ~w(budget_charged)

  @doc "Build buyer-facing proof for a run id from the trace store."
  def build(run_id, opts \\ []) when is_binary(run_id) do
    run_id
    |> Mitos.Trace.read_events(opts)
    |> from_events()
  end

  @doc "Render a buyer-facing proof page from an effect-only proof map."
  def render_html(%{} = proof) do
    """
    <!doctype html>
    <html lang="en">
    <head>
      <meta charset="utf-8">
      <meta name="viewport" content="width=device-width, initial-scale=1">
      <title>Mitos Proof #{escape(proof["run_id"])}</title>
      <style>
        :root { color-scheme: light dark; font-family: system-ui, sans-serif; }
        body { margin: 0; padding: 32px; line-height: 1.45; }
        main { max-width: 980px; margin: 0 auto; }
        h1, h2 { line-height: 1.15; }
        section { border-top: 1px solid color-mix(in srgb, currentColor 18%, transparent); padding: 20px 0; }
        table { border-collapse: collapse; width: 100%; }
        th, td { border-bottom: 1px solid color-mix(in srgb, currentColor 14%, transparent); padding: 8px; text-align: left; vertical-align: top; }
        code { word-break: break-word; }
        .muted { opacity: 0.72; }
        .summary { display: grid; gap: 12px; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); }
        .metric { border: 1px solid color-mix(in srgb, currentColor 18%, transparent); padding: 12px; border-radius: 6px; }
        pre { white-space: pre-wrap; overflow-wrap: anywhere; }
      </style>
    </head>
    <body>
      <main>
        <h1>Run Proof</h1>
        <p class="muted">Buyer-facing evidence generated from canonical Effect events only.</p>
        #{summary_html(proof)}
        #{delivery_html(proof["delivery"] || %{})}
        #{settlements_html(proof["settlements"] || [])}
        #{timeline_html(proof["timeline"] || [])}
        #{egress_html(proof["egress_crossings"] || [])}
        #{tools_html(proof["tools_brokered"] || [])}
        #{artifacts_html(proof["artifacts"] || [])}
        #{redaction_html(proof["redaction"] || %{})}
      </main>
    </body>
    </html>
    """
  end

  @doc "Build buyer-facing proof from decoded trace events."
  def from_events(events) when is_list(events) do
    effects =
      events
      |> Enum.flat_map(&canonical_effect/1)
      |> Enum.sort_by(&Map.get(&1, "seq", -1))
      |> proof_window()

    transcript = Enum.map(effects, &canonical_effect_record/1)
    effect_hash = sha256(Enum.map_join(transcript, "\n", &canonical_json/1))
    started = Enum.find(effects, &(Map.get(&1, "type") == "capability_started"))

    finished =
      effects |> Enum.reverse() |> Enum.find(&(Map.get(&1, "type") == "capability_finished"))

    %{
      "run_id" => run_id(effects),
      "effect_log_sha256" => effect_hash,
      "effect_count" => length(effects),
      "redaction" => %{
        "source" => "effect_events_only",
        "decision_events_excluded" => true,
        "rationale_redacted" => true
      },
      "timeline" => Enum.map(effects, &timeline_entry/1),
      "metering" => metering(effects, started, finished),
      "settlements" => settlement_lines(effects),
      "tools_brokered" => tools_brokered(effects),
      "egress_crossings" => egress_crossings(effects),
      "artifacts" => artifacts_manifest(effects, finished),
      "delivery" => delivery(finished)
    }
  end

  defp canonical_effect(%{"kind" => "effect"} = event), do: [event]
  defp canonical_effect(%{"event" => %{"kind" => "effect"} = event}), do: [event]
  defp canonical_effect(_event), do: []

  defp proof_window([]), do: []

  defp proof_window(effects) do
    start_seq =
      effects
      |> Enum.find(&(Map.get(&1, "type") == "capability_started"))
      |> seq()

    finish_seq =
      effects
      |> Enum.reverse()
      |> Enum.find(&(Map.get(&1, "type") == "capability_finished"))
      |> seq()

    Enum.filter(effects, fn effect ->
      effect_seq = Map.get(effect, "seq")

      cond do
        not is_integer(effect_seq) ->
          true

        Map.get(effect, "type") in @post_finish_proof_effects ->
          not is_integer(start_seq) or effect_seq >= start_seq

        is_integer(start_seq) and is_integer(finish_seq) ->
          effect_seq >= start_seq and effect_seq <= finish_seq

        is_integer(start_seq) ->
          effect_seq >= start_seq

        is_integer(finish_seq) ->
          effect_seq <= finish_seq

        true ->
          true
      end
    end)
  end

  defp canonical_effect_record(effect) do
    Map.take(effect, ["run_id", "seq", "ts", "type", "caused_by", "ref", "payload"])
  end

  defp run_id([%{"run_id" => run_id} | _]), do: run_id
  defp run_id(_effects), do: nil

  defp timeline_entry(%{"type" => type} = effect) do
    %{
      "seq" => Map.get(effect, "seq"),
      "ts" => Map.get(effect, "ts"),
      "type" => type,
      "caused_by" => Map.get(effect, "caused_by")
    }
    |> put_optional("dest", egress_dest(effect))
    |> put_optional("tool", tool_name(effect))
    |> put_optional("status", status(effect))
    |> put_optional("result_signal", result_signal(effect))
    |> put_optional("artifact", artifact_ref(effect))
    |> put_optional("commit", commit_ref(effect))
    |> put_optional("budget", budget_payload(effect))
  end

  defp metering(effects, started, finished) do
    counts =
      @counted_effects
      |> Map.new(fn type -> {type <> "_units", count_type(effects, type)} end)
      |> Map.put("effect_units", length(effects))

    counts
    |> Map.put("compute_ms", compute_ms(started, finished))
    |> Map.put("budget_charged", budget_totals(effects))
  end

  defp tools_brokered(effects) do
    effects
    |> Enum.filter(&(Map.get(&1, "type") == "tool_brokered"))
    |> Enum.map(fn effect ->
      %{
        "seq" => Map.get(effect, "seq"),
        "ts" => Map.get(effect, "ts")
      }
      |> put_optional("tool", tool_name(effect))
      |> put_optional("schema", value(ref(effect), "schema") || value(payload(effect), "schema"))
    end)
  end

  defp egress_crossings(effects) do
    effects
    |> Enum.filter(&(Map.get(&1, "type") in ["egress_allowed", "egress_denied"]))
    |> Enum.map(fn effect ->
      %{
        "seq" => Map.get(effect, "seq"),
        "ts" => Map.get(effect, "ts"),
        "decision" => effect["type"] |> String.replace_prefix("egress_", "")
      }
      |> put_optional("dest", egress_dest(effect))
    end)
  end

  defp settlement_lines(effects) do
    effects
    |> Enum.filter(&(Map.get(&1, "type") == "budget_charged"))
    |> Enum.map(&settlement_line/1)
    |> Enum.reject(&(&1 == %{}))
  end

  defp settlement_line(effect) do
    ref = ref(effect)
    payload = payload(effect)
    settlement = value(payload, "settlement", %{})

    line =
      %{
        "seq" => Map.get(effect, "seq"),
        "ts" => Map.get(effect, "ts")
      }
      |> put_optional("quote_id", value(ref, "quote_id") || value(settlement, "quote_id"))
      |> put_optional(
        "settlement_tx",
        value(ref, "settlement_tx") || value(settlement, "settlement_tx")
      )
      |> put_optional("resource", value(ref, "resource") || value(settlement, "resource"))
      |> put_optional("pricing_type", value(ref, "pricing_type"))
      |> put_optional("status", value(settlement, "status"))
      |> put_optional("currency", value(payload, "currency") || value(settlement, "currency"))
      |> put_optional("escrowed_usdc", value(settlement, "escrowed_usdc"))
      |> put_optional(
        "buyer_charge_usdc",
        value(settlement, "buyer_charge_usdc") || value(payload, "amount_usdc")
      )
      |> put_optional("refund_due_usdc", value(settlement, "refund_due_usdc"))
      |> put_optional("compute_surcharge_usdc", value(settlement, "compute_surcharge_usdc"))
      |> put_optional("platform_take_usdc", value(settlement, "platform_take_usdc"))
      |> put_optional("seller_net_usdc", value(settlement, "seller_net_usdc"))
      |> put_optional("pay_to", value(settlement, "pay_to"))

    if line["quote_id"] || line["settlement_tx"] || line["buyer_charge_usdc"] do
      line
    else
      %{}
    end
  end

  defp artifacts_manifest(effects, finished) do
    registered =
      effects
      |> Enum.filter(&(Map.get(&1, "type") == "artifact_registered"))
      |> Enum.map(&artifact_ref/1)
      |> Enum.reject(&is_nil/1)

    delivery = delivery(finished)
    delivered = value(delivery, "artifacts", []) |> List.wrap()
    result_evidence = delivery |> value("result", %{}) |> value("evidence", []) |> List.wrap()

    (registered ++ delivered ++ result_evidence)
    |> Enum.map(&artifact_summary/1)
    |> Enum.reject(&(&1 == %{}))
    |> Enum.uniq()
  end

  defp delivery(nil), do: %{"diff" => nil, "result" => nil, "artifacts" => []}

  defp delivery(%{} = finished) do
    payload = payload(finished)
    harvest = value(payload, "harvest", %{})

    %{
      "diff" => value(harvest, "diff"),
      "result" => value(harvest, "result") || value(payload, "result"),
      "artifacts" => value(harvest, "artifacts", [])
    }
  end

  defp count_type(effects, type), do: Enum.count(effects, &(Map.get(&1, "type") == type))

  defp budget_totals(effects) do
    effects
    |> Enum.filter(&(Map.get(&1, "type") == "budget_charged"))
    |> Enum.map(&payload/1)
    |> Enum.reduce(%{"tokens" => 0, "usd" => 0.0, "wall_ms" => 0}, fn payload, acc ->
      %{
        "tokens" => acc["tokens"] + numeric(payload["tokens"]),
        "usd" => acc["usd"] + numeric(payload["usd"]),
        "wall_ms" => acc["wall_ms"] + numeric(payload["wall_ms"])
      }
    end)
  end

  defp compute_ms(nil, _finished), do: nil
  defp compute_ms(_started, nil), do: nil

  defp compute_ms(started, finished) do
    with started_ms when is_integer(started_ms) <- timestamp_ms(started["ts"]),
         finished_ms when is_integer(finished_ms) <- timestamp_ms(finished["ts"]) do
      max(finished_ms - started_ms, 0)
    else
      _ -> nil
    end
  end

  defp timestamp_ms(ts) when is_binary(ts) do
    case DateTime.from_iso8601(ts) do
      {:ok, datetime, _offset} -> DateTime.to_unix(datetime, :millisecond)
      _ -> nil
    end
  end

  defp timestamp_ms(_), do: nil

  defp egress_dest(effect), do: value(ref(effect), "dest") || value(ref(effect), "destination")

  defp tool_name(effect) do
    value(ref(effect), "tool") ||
      value(ref(effect), "name") ||
      value(payload(effect), "tool") ||
      value(payload(effect), "name")
  end

  defp status(%{"type" => "capability_finished"} = effect), do: value(payload(effect), "status")

  defp status(%{"type" => "verification_recorded"} = effect),
    do: value(payload(effect), "verdict")

  defp status(%{"type" => "hook_executed"} = effect), do: value(payload(effect), "status")
  defp status(_effect), do: nil

  defp result_signal(%{"type" => "capability_finished"} = effect) do
    effect
    |> payload()
    |> value("result", %{})
    |> value("signal")
  end

  defp result_signal(_effect), do: nil

  defp artifact_ref(%{"type" => "artifact_registered"} = effect),
    do: artifact_summary(ref(effect))

  defp artifact_ref(_effect), do: nil

  defp commit_ref(%{"type" => "commit"} = effect) do
    effect
    |> ref()
    |> Map.take(["sha", "files"])
  end

  defp commit_ref(_effect), do: nil

  defp budget_payload(%{"type" => "budget_charged"} = effect) do
    effect
    |> payload()
    |> Map.take(["tokens", "usd", "wall_ms"])
  end

  defp budget_payload(_effect), do: nil

  defp artifact_summary(%{} = artifact) do
    artifact
    |> Map.take(["kind", "path", "artifact", "sha256", "bytes", "missing"])
  end

  defp artifact_summary(_artifact), do: %{}

  defp seq(nil), do: nil
  defp seq(%{"seq" => seq}) when is_integer(seq), do: seq
  defp seq(_effect), do: nil

  defp ref(%{"ref" => ref}) when is_map(ref), do: ref
  defp ref(_effect), do: %{}

  defp payload(%{"payload" => payload}) when is_map(payload), do: payload
  defp payload(_effect), do: %{}

  defp put_optional(map, _key, nil), do: map
  defp put_optional(map, _key, %{} = value) when map_size(value) == 0, do: map
  defp put_optional(map, _key, []), do: map
  defp put_optional(map, key, value), do: Map.put(map, key, value)

  defp value(map, key, default \\ nil)

  defp value(%{} = map, key, default),
    do: Map.get(map, key, Map.get(map, to_string(key), default))

  defp value(_other, _key, default), do: default

  defp numeric(value) when is_integer(value) or is_float(value), do: value
  defp numeric(_value), do: 0

  defp sha256(bytes) when is_binary(bytes) do
    digest =
      :crypto.hash(:sha256, bytes)
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end

  defp canonical_json(%{} = map) do
    body =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map_join(",", fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> body <> "}"
  end

  defp canonical_json(values) when is_list(values) do
    "[" <> Enum.map_join(values, ",", &canonical_json/1) <> "]"
  end

  defp canonical_json(value), do: Jason.encode!(value)

  defp summary_html(proof) do
    metering = proof["metering"] || %{}
    budget = metering["budget_charged"] || %{}

    """
    <section>
      <h2>Summary</h2>
      <div class="summary">
        #{metric("Run", proof["run_id"])}
        #{metric("Effect Log Hash", proof["effect_log_sha256"])}
        #{metric("Effects", proof["effect_count"])}
        #{metric("Compute ms", metering["compute_ms"])}
        #{metric("Budget USD", budget["usd"])}
        #{metric("Artifacts", length(proof["artifacts"] || []))}
      </div>
    </section>
    """
  end

  defp delivery_html(delivery) do
    result = delivery["result"] || %{}
    diff = delivery["diff"] || %{}
    evidence = result |> value("evidence", []) |> List.wrap() |> Enum.map(&artifact_summary/1)

    """
    <section>
      <h2>Delivery</h2>
      <div class="summary">
        #{metric("Result Signal", value(result, "signal"))}
        #{metric("Result Summary", value(result, "summary"))}
        #{metric("Result Path", value(result, "path"))}
        #{metric("Result Hash", value(result, "sha256"))}
        #{metric("Diff", value(diff, "sha256"))}
      </div>
      #{evidence_html(evidence)}
    </section>
    """
  end

  defp evidence_html([]), do: ""

  defp evidence_html(evidence) do
    """
    <h3>Result Evidence</h3>
    #{artifact_table(evidence)}
    """
  end

  defp settlements_html([]),
    do: "<section><h2>Settlement</h2><p class=\"muted\">No settlement recorded.</p></section>"

  defp settlements_html(settlements) do
    rows =
      Enum.map_join(settlements, "\n", fn settlement ->
        """
        <tr>
          <td>#{escape(settlement["seq"])}</td>
          <td>#{escape(settlement["quote_id"])}</td>
          <td>#{escape(settlement["settlement_tx"])}</td>
          <td>#{escape(settlement["buyer_charge_usdc"])}</td>
          <td>#{escape(settlement["refund_due_usdc"])}</td>
          <td>#{escape(settlement["platform_take_usdc"])}</td>
          <td>#{escape(settlement["seller_net_usdc"])}</td>
        </tr>
        """
      end)

    """
    <section>
      <h2>Settlement</h2>
      <table>
        <thead><tr><th>Seq</th><th>Quote</th><th>Tx</th><th>Charge</th><th>Refund</th><th>Take</th><th>Seller Net</th></tr></thead>
        <tbody>#{rows}</tbody>
      </table>
    </section>
    """
  end

  defp timeline_html([]),
    do: "<section><h2>Timeline</h2><p class=\"muted\">No proof events.</p></section>"

  defp timeline_html(timeline) do
    rows =
      Enum.map_join(timeline, "\n", fn entry ->
        """
        <tr>
          <td>#{escape(entry["seq"])}</td>
          <td>#{escape(entry["ts"])}</td>
          <td>#{escape(entry["type"])}</td>
          <td>#{escape(entry["status"] || entry["result_signal"] || entry["dest"] || entry["tool"] || "")}</td>
        </tr>
        """
      end)

    """
    <section>
      <h2>Timeline</h2>
      <table>
        <thead><tr><th>Seq</th><th>Time</th><th>Effect</th><th>Evidence</th></tr></thead>
        <tbody>#{rows}</tbody>
      </table>
    </section>
    """
  end

  defp egress_html([]),
    do: "<section><h2>Egress</h2><p class=\"muted\">No egress crossings recorded.</p></section>"

  defp egress_html(crossings) do
    rows =
      Enum.map_join(crossings, "\n", fn crossing ->
        "<tr><td>#{escape(crossing["seq"])}</td><td>#{escape(crossing["dest"])}</td><td>#{escape(crossing["decision"])}</td></tr>"
      end)

    "<section><h2>Egress</h2><table><thead><tr><th>Seq</th><th>Destination</th><th>Decision</th></tr></thead><tbody>#{rows}</tbody></table></section>"
  end

  defp tools_html([]),
    do:
      "<section><h2>Brokered Tools</h2><p class=\"muted\">No brokered tools recorded.</p></section>"

  defp tools_html(tools) do
    rows =
      Enum.map_join(tools, "\n", fn tool ->
        "<tr><td>#{escape(tool["seq"])}</td><td>#{escape(tool["tool"])}</td><td>#{escape(tool["schema"])}</td></tr>"
      end)

    "<section><h2>Brokered Tools</h2><table><thead><tr><th>Seq</th><th>Tool</th><th>Schema</th></tr></thead><tbody>#{rows}</tbody></table></section>"
  end

  defp artifacts_html([]),
    do: "<section><h2>Artifacts</h2><p class=\"muted\">No artifacts recorded.</p></section>"

  defp artifacts_html(artifacts) do
    "<section><h2>Artifacts</h2>#{artifact_table(artifacts)}</section>"
  end

  defp artifact_table(artifacts) do
    rows =
      Enum.map_join(artifacts, "\n", fn artifact ->
        """
        <tr>
          <td>#{escape(artifact["kind"])}</td>
          <td>#{escape(artifact["path"] || artifact["artifact"])}</td>
          <td><code>#{escape(artifact["sha256"])}</code></td>
          <td>#{escape(artifact_status(artifact))}</td>
        </tr>
        """
      end)

    "<table><thead><tr><th>Kind</th><th>Path</th><th>Hash</th><th>Status</th></tr></thead><tbody>#{rows}</tbody></table>"
  end

  defp artifact_status(%{"missing" => true}), do: "missing"
  defp artifact_status(%{"bytes" => bytes}) when is_integer(bytes), do: "#{bytes} bytes"
  defp artifact_status(_artifact), do: ""

  defp redaction_html(redaction) do
    """
    <section>
      <h2>Redaction</h2>
      <p>Source: #{escape(redaction["source"])}</p>
      <p>Decision events excluded: #{escape(redaction["decision_events_excluded"])}</p>
      <p>Rationale redacted: #{escape(redaction["rationale_redacted"])}</p>
    </section>
    """
  end

  defp metric(label, value) do
    "<div class=\"metric\"><strong>#{escape(label)}</strong><br><code>#{escape(value)}</code></div>"
  end

  defp escape(nil), do: ""

  defp escape(value) do
    value
    |> to_string()
    |> String.replace("&", "&amp;")
    |> String.replace("<", "&lt;")
    |> String.replace(">", "&gt;")
    |> String.replace("\"", "&quot;")
    |> String.replace("'", "&#39;")
  end
end
