defmodule Mitos.Web.Pages do
  @moduledoc """
  The v1 surfaces (11 §10): run history + watch + deliverable pages, the
  entrypoint order form, the seller's flat attribution list, and the paid
  endpoints. Every page is a read of the log at a cut (11 §1); the **one
  renderer, two cuts** rule (11 §5) is `report/3` — the dev cut adds
  `rationale` and inner-trace links, the commercial cut is exactly the 08 §5
  proof page plus the receipt.

  `role` (mock identity, IdP later) only selects navigation and the cut —
  never a different data source (08 §2).
  """

  import Mitos.Web.HTML

  alias Mitos.Event.{Decision, Effect}
  alias Mitos.Run.State
  alias Mitos.{Catalog, Facilitator, Log, Proof, Runs}

  # --- sign-in: the mock role picker (an IdP replaces this) ---

  def login do
    cards = [
      {"operator", "var(--info)", "Operator",
       "Run pipelines against your own code, watch every decision and inner trace, steer.",
       "dev cut — full fidelity"},
      {"seller", "var(--accent)", "Seller",
       "Publish capabilities as paid endpoints, price them, read revenue and operations.",
       "attribution cut — never buyer data"},
      {"buyer", "var(--ok)", "Buyer",
       "Order a completed job with a wallet, watch read-only, receive artifacts + proof.",
       "commercial cut — never the recipe"}
    ]

    grid =
      Enum.map_join(cards, "\n", fn {role, color, label, blurb, cut} ->
        """
        <a class="role-card" href="/web/login/#{role}">
          <span class="dot" style="background:#{color}"></span>
          <h3>#{label}</h3>
          <p>#{blurb}</p>
          <span class="cut">#{cut}</span>
        </a>
        """
      end)

    body = """
    <div class="login">
      <div class="brand-big">mitos<span class="brand-dot">.</span></div>
      <p class="tagline">packaged capabilities · agent-driven pipelines · every decision on the record</p>
      <div class="role-grid">#{grid}</div>
      <p class="idp-note">mock identity — role selects a redaction cut of the same log; an IdP takes this seat later</p>
    </div>
    """

    layout("sign in", body)
  end

  # --- dashboard: run history (the v1 operate surface, 11 §8) ---

  def dashboard(role) do
    runs = Runs.list()

    finished = Enum.count(runs, &(&1.status == :finished))
    aborted = Enum.count(runs, &(&1.status in [:aborted, :failed]))
    tokens = runs |> Enum.map(& &1.spent.tokens) |> Enum.sum()
    spend = runs |> Enum.map(& &1.spent.usd) |> Enum.sum()

    tiles =
      stat_tiles([
        {"runs", length(runs), nil},
        {"finished", finished, nil},
        {"aborted", aborted, nil},
        {"tokens", tokens, nil},
        {"model spend", usd(spend), "fold of budget_charged"}
      ])

    rows =
      runs
      |> Enum.take(100)
      |> Enum.map_join("\n", fn r ->
        stages =
          Enum.map_join(r.stages, " ", fn {id, st} ->
            ~s(<span class="pill s-#{esc(st)}"><span class="dot"></span>#{esc(id)}</span>)
          end)

        """
        <tr>
          <td class="num">#{ts_short(r.started)}</td>
          <td>#{mono_raw(~s(<a href="#{run_link(r.run_id)}">#{esc(r.run_id)}</a>))}</td>
          <td>#{esc(r.pipeline)}</td>
          <td>#{status_badge(r.status)}</td>
          <td>#{stages}</td>
          <td class="num">#{r.spent.tokens} tok · #{usd(r.spent.usd)}</td>
        </tr>
        """
      end)

    table =
      if runs == [] do
        ~s(<div class="empty">no runs yet — start one from an application entrypoint or <code>mitos run</code></div>)
      else
        """
        <div class="table-wrap"><table>
          <thead><tr><th>started</th><th>run</th><th>pipeline</th><th>status</th><th>stages</th><th>spent</th></tr></thead>
          <tbody>#{rows}</tbody>
        </table></div>
        """
      end

    body = """
    <h1>Runs</h1>
    <p class="sub">every row is fold(events) of one run log — nothing is stored beside it</p>
    #{tiles}
    #{table}
    """

    layout("runs", body, role: role, active: "runs")
  end

  # --- applications: list + entrypoint order forms (11 §4 entry, 12 §4) ---

  def apps(role) do
    apps = Catalog.apps()

    rows =
      Enum.map_join(apps, "\n", fn {app_id, app} ->
        eps =
          Enum.map_join(app.entrypoints, " · ", fn ep ->
            ~s(<a href="/web/apps/#{app_id}/entrypoints/#{esc(ep.id)}">#{esc(ep.title || ep.id)}</a>)
          end)

        """
        <tr>
          <td>#{esc(app.name)} <span class="faint">v#{esc(app.version)}</span></td>
          <td>#{mono(digest(app_id))}</td>
          <td>#{eps}</td>
        </tr>
        """
      end)

    table =
      if apps == %{} do
        ~s(<div class="empty">no applications registered — <code>mitos serve --app &lt;dir&gt;</code></div>)
      else
        """
        <div class="table-wrap"><table>
          <thead><tr><th>application</th><th>app_id</th><th>entrypoints</th></tr></thead>
          <tbody>#{rows}</tbody>
        </table></div>
        """
      end

    body = """
    <h1>Applications</h1>
    <p class="sub">a user runs a pipeline named by an entrypoint — never a bare capability</p>
    #{table}
    """

    layout("applications", body, role: role, active: "applications")
  end

  def order_form(app_id, ep_id, role) do
    with app when not is_nil(app) <- Catalog.find_app(app_id),
         ep when not is_nil(ep) <- Mitos.App.Manifest.entrypoint(app, ep_id) do
      params =
        Enum.map_join(ep.parameters, "\n", fn p ->
          name = p["name"]

          """
          <label>#{esc(name)} <span class="faint">#{esc(p["type"] || "string")}</span></label>
          <input type="text" name="param_#{esc(name)}" value="#{esc(p["default"])}">
          """
        end)

      workspace_field =
        case ep.workspace do
          %{kind: "none"} ->
            ""

          %{kind: kind} ->
            """
            <label>workspace <span class="faint">#{esc(kind)} — resolved server-side, never in the browser</span></label>
            <input type="text" name="workspace" placeholder="path or ref">
            """
        end

      input_fields =
        case input_properties(ep.inputs) do
          [] ->
            """
            <label>inputs <span class="faint">JSON</span></label>
            <textarea name="inputs">#{esc(Mitos.JSON.encode!(if ep.defaults == %{}, do: %{}, else: ep.defaults))}</textarea>
            """

          props ->
            Enum.map_join(props, "\n", fn {name, spec} ->
              type = spec["type"] || "string"
              req = if spec_required?(spec), do: " required", else: ""

              """
              <label>#{esc(name)} <span class="faint">#{esc(type)}#{if spec_required?(spec), do: " · required", else: ""}</span></label>
              <input type="text" name="input_#{esc(name)}" placeholder="#{esc(type)}"#{req}>
              """
            end)
        end

      body = """
      <h1>#{esc(ep.title || ep.id)}</h1>
      <p class="sub">#{esc(app.name)} · pipeline <code>#{esc(ep.pipeline)}</code></p>
      <div class="card">
      <form method="post" action="/web/apps/#{app_id}/entrypoints/#{esc(ep.id)}/run">
        #{workspace_field}
        #{input_fields}
        #{params}
        <button type="submit">Run</button>
      </form>
      </div>
      """

      {:ok, layout("order · #{ep.id}", body, role: role, active: "applications")}
    else
      _ -> :not_found
    end
  end

  # --- Run & Watch: outer zoom (11 §4) ---

  def watch(run_id, cut, role) do
    state = State.fold(run_id)

    if state.events == [] do
      :not_found
    else
      refresh = if State.terminal?(state), do: nil, else: 2

      stage_cards =
        Enum.map_join(state.stage_order, "\n", fn id ->
          s = state.stages[id]
          signal = s.result && s.result["signal"]

          trace_link =
            if cut == :dev and Runs.trace(run_id, id, limit: 1).events != [],
              do: ~s( · <a href="/web/runs/#{run_id}/trace/#{esc(id)}">inner trace ↓</a>),
              else: ""

          """
          <div class="card">#{esc(id)} #{status_badge(s.status)}
            #{if signal, do: ~s(· signal <b>#{esc(signal)}</b>), else: ""}#{trace_link}
          </div>
          """
        end)

      timeline =
        case cut do
          :dev -> dev_timeline(run_id)
          :commercial -> commercial_timeline(run_id)
        end

      report_href =
        if cut == :dev,
          do: "/web/runs/#{run_id}/report",
          else: "/web/runs/#{run_id}/proof"

      live_note =
        if State.terminal?(state),
          do: ~s(<a href="#{report_href}">→ deliverable</a>),
          else: ~s(<span class="pill s-running"><span class="dot"></span>live — refreshing</span>)

      body = """
      <h1>#{mono(run_id)} #{status_badge(state.status)}</h1>
      <p class="sub">#{cut_note(cut)} · #{live_note}</p>
      #{stage_cards}
      #{eyebrow("timeline — the run as decision / effect events")}
      <ul class="timeline">#{timeline}</ul>
      """

      {:ok, layout("watch · #{run_id}", body, role: role, refresh: refresh, active: "runs")}
    end
  end

  # --- inner zoom: one capability's harness trace (11 §4, dev cut only) ---

  def trace(run_id, stage, role) do
    %{events: events, total_shown: shown, truncated: truncated, bytes: bytes} = Runs.trace(run_id, stage)

    if events == [] and bytes == 0 do
      :not_found
    else
      rows =
        Enum.map_join(events, "\n", fn e ->
          case e["type"] do
            "assistant_text" ->
              ~s(<li><span class="mk mk-info"></span><span class="k">assistant</span></li><pre>#{esc(e["text"])}</pre>)

            "tool_use" ->
              ~s(<li><span class="mk mk-warn"></span><span class="k">tool_use</span><span class="d">#{esc(e["name"])} #{esc(Mitos.JSON.encode!(e["input"] || %{}))}</span></li>)

            "tool_result" ->
              ~s(<li><span class="mk"></span><span class="k">tool_result</span><span class="d">#{esc(String.slice(to_string(e["content"] || ""), 0, 400))}</span></li>)

            "token_usage" ->
              ~s(<li><span class="mk"></span><span class="k">tokens</span><span class="d">#{esc(e["tokens"])} · #{usd(e["usd"] || 0.0)}</span></li>)

            other ->
              ~s(<li><span class="mk"></span><span class="k">#{esc(other)}</span><span class="d">#{esc(e["status"] || e["subtype"])}</span></li>)
          end
        end)

      # Surface that the on-disk trace is huge / truncated — it's bounded at read
      # time so a runaway trace (e.g. a harness that flooded stderr) can't OOM
      # the page; the raw file is still on disk for offline inspection.
      banner =
        cond do
          truncated ->
            ~s|<p class="sub warn">⚠ showing the first #{shown} events of a larger trace (#{mb(bytes)} MB on disk). The file is bounded at read time; inspect <code>trace/#{esc(stage)}.jsonl</code> directly for the full record.</p>|

          bytes > 5_000_000 ->
            ~s(<p class="sub">#{shown} events · #{mb(bytes)} MB on disk</p>)

          true ->
            ""
        end

      body = """
      <h1>inner trace · #{esc(stage)}</h1>
      <p class="sub">the harness's own steps inside one capability — an observability surface,
      never an orchestrator input · <a href="#{run_link(run_id)}">← run</a></p>
      #{banner}
      <ul class="timeline">#{rows}</ul>
      """

      {:ok, layout("trace · #{stage}", body, role: role, active: "runs")}
    end
  end

  defp mb(bytes), do: :erlang.float_to_binary(bytes / 1_048_576, [:compact, decimals: 1])

  # --- the Deliverable: one renderer, two cuts (11 §5) ---

  def report(run_id, cut, role) do
    state = State.fold(run_id)

    if state.events == [] do
      :not_found
    else
      deliverable = Proof.deliverable(run_id)
      proof = Proof.render(run_id, settled_charge(run_id))

      header = report_header(run_id, state, cut)
      timeline = if cut == :dev, do: dev_timeline(run_id), else: commercial_timeline(run_id)
      receipt = if cut == :commercial, do: receipt_section(run_id, proof), else: ""
      inner = if cut == :dev, do: inner_trace_links(run_id, state), else: ""

      totals =
        stat_tiles([
          {"tokens", proof["token_totals"]["tokens"], nil},
          {"model spend", usd(proof["token_totals"]["usd"]), "seller's own key"},
          {"wall", "#{proof["wall_ms"]} ms", nil},
          {"egress crossings", length(proof["egress"]), "brokered, logged"},
          {"artifacts", length(proof["artifacts"]), "content-addressed"}
        ])

      body = """
      <h1>#{if cut == :dev, do: "Run report", else: "Proof of work"} #{status_badge(proof["status"])}</h1>
      <p class="sub">#{cut_note(cut)}</p>
      #{header}
      #{eyebrow("deliverables")}
      #{result_section(deliverable)}
      #{artifacts_section(run_id, deliverable["artifacts"])}
      #{diff_section(deliverable["diff"])}
      #{eyebrow("totals — fold of budget_charged")}
      #{totals}
      <div class="card"><dl class="kv">
        <dt>log hash</dt><dd>#{mono(proof["log_hash"])}
          <span class="faint">— recompute-able from the excerpt; the replay/audit anchor</span></dd>
      </dl></div>
      #{receipt}
      #{inner}
      #{eyebrow("timeline")}
      <ul class="timeline">#{timeline}</ul>
      """

      {:ok,
       layout("#{if cut == :dev, do: "report", else: "proof"} · #{run_id}", body,
         role: role,
         active: "runs"
       )}
    end
  end

  # --- endpoints (offers) + buy instructions (11 §9: buy is a URL) ---

  def capabilities(role) do
    caps = Catalog.capabilities()

    rows =
      Enum.map_join(caps, "\n", fn {id, m} ->
        offer = Catalog.get_offer(id)

        """
        <tr>
          <td>#{esc(m.name)} <span class="faint">v#{esc(m.version)}</span></td>
          <td>#{mono(digest(id))}</td>
          <td>#{esc(m.trust_tier)}</td>
          <td class="num">#{if offer, do: usd(offer.price_usd), else: ~s(<span class="faint">unpublished</span>)}</td>
          <td>#{if offer, do: ~s(<a href="/web/capabilities/#{id}">order →</a>), else: ""}</td>
        </tr>
        """
      end)

    table =
      if caps == %{} do
        ~s(<div class="empty">nothing published — <code>mitos serve &lt;cap_dir&gt; --price … --payout …</code></div>)
      else
        """
        <div class="table-wrap"><table>
          <thead><tr><th>capability</th><th>identity</th><th>tier</th><th>flat price</th><th></th></tr></thead>
          <tbody>#{rows}</tbody>
        </table></div>
        """
      end

    body = """
    <h1>Paid endpoints</h1>
    <p class="sub">a completed job for a flat price — executed in a sandbox, delivered with proof</p>
    #{table}
    """

    layout("endpoints", body, role: role, active: active_for(role))
  end

  def buy(capability_id, role) do
    case Catalog.get_offer(capability_id) do
      nil ->
        :not_found

      offer ->
        endpoint = "#{base_url()}/capabilities/#{capability_id}/runs"

        quote_cmd =
          "curl -s -X POST #{endpoint} \\\n" <>
            "  -H 'content-type: application/json' -d '{\"inputs\":{}}'"

        pay_cmd =
          "curl -s -X POST #{endpoint} \\\n" <>
            "  -H 'content-type: application/json' \\\n" <>
            "  -H 'x-payment: {\"quote_id\":\"<quote_id>\",\"amount_usdc\":#{offer.price_usd},\"payer\":\"0xYOU\"}' \\\n" <>
            "  -d '{\"inputs\":{}}'"

        body = """
        <h1>#{esc(offer.name)} <span class="pill"><span class="dot"></span>#{usd(offer.price_usd)} per job</span></h1>
        <p class="sub">no account, no install — a wallet and a URL (x402)</p>
        <div class="card">
        #{eyebrow("1 · request a quote — the endpoint answers 402")}
        <pre>#{esc(quote_cmd)}</pre>
        #{eyebrow("2 · pay the quote — repeat with the x-payment proof")}
        <pre>#{esc(pay_cmd)}</pre>
        #{eyebrow("3 · receive")}
        <p class="dim">the response carries your <code>status_url</code> and <code>proof_url</code>;
        watch read-only at <code>/web/runs/&lt;run_id&gt;/watch</code>, and the deliverable —
        artifacts, diff, receipt — stands alone at <code>/web/runs/&lt;run_id&gt;/proof</code>.</p>
        </div>
        """

        {:ok, layout("order · #{offer.name}", body, role: role, active: active_for(role))}
    end
  end

  # --- seller attribution: the flat revenue list (11 §7 MVP, 08 §6) ---

  def attribution(role) do
    settlements = Facilitator.settlements()

    total_net =
      settlements |> Enum.map(fn {_r, s} -> s.charge.seller_net end) |> Enum.sum()

    total_gmv =
      settlements
      |> Enum.filter(fn {_r, s} -> s.charge.delivered? end)
      |> Enum.map(fn {_r, s} -> s.charge.buyer_charge end)
      |> Enum.sum()

    tiles =
      stat_tiles([
        {"settled runs", map_size(settlements), nil},
        {"gmv", usd(total_gmv), "delivered buyer charges"},
        {"net revenue", usd(total_net), "after take + surcharge"}
      ])

    sections =
      settlements
      |> Enum.group_by(fn {_run, s} -> s.capability_id end)
      |> Enum.map_join("\n", fn {cap_id, entries} ->
        m = Catalog.get_capability(cap_id)
        net = entries |> Enum.map(fn {_r, s} -> s.charge.seller_net end) |> Enum.sum()

        rows =
          Enum.map_join(entries, "\n", fn {run_id, s} ->
            """
            <tr>
              <td>#{mono_raw(~s(<a href="/web/runs/#{run_id}/proof">#{esc(run_id)}</a>))}</td>
              <td>#{if s.charge.delivered?, do: status_badge(:finished), else: status_badge(:aborted)}</td>
              <td class="num">#{usd(s.charge.buyer_charge)}</td>
              <td class="num">#{usd(s.charge.seller_net)}</td>
              <td class="num">#{usd(s.charge.provider_cost)} own-key</td>
              <td class="num">#{s.charge.compute_ms} ms · #{s.charge.egress_units} crossings</td>
            </tr>
            """
          end)

        """
        #{eyebrow("#{(m && m.name) || digest(cap_id)} · #{digest(cap_id)} — net #{usd(net)}")}
        <div class="table-wrap"><table>
          <thead><tr><th>run</th><th>outcome</th><th>charge</th><th>net</th><th>provider cost</th><th>operations</th></tr></thead>
          <tbody>#{rows}</tbody>
        </table></div>
        """
      end)

    body = """
    <h1>Attribution</h1>
    <p class="sub">the same log, cut the seller's way: revenue per capability, own-key spend,
    operations — never the buyer's data</p>
    #{tiles}
    #{if sections == "", do: ~s(<div class="empty">no settled runs yet — publish an endpoint and let a stranger pay</div>), else: sections}
    """

    layout("attribution", body, role: role, active: "attribution")
  end

  # --- shared fragments ---

  @mk_classes %{
    egress_allowed: "mk-ok",
    egress_denied: "mk-bad",
    capability_started: "mk-info",
    capability_finished: "mk-ok",
    run_finished: "mk-ok",
    approval_requested: "mk-warn",
    hook_ran: "mk-warn"
  }

  defp dev_timeline(run_id) do
    run_id
    |> Log.read()
    |> Enum.map_join("\n", fn
      %Decision{} = d ->
        """
        <li class="decision"><span class="t">#{ts_time(d.ts)}</span><span class="mk"></span>
          <span class="k">#{esc(d.type)}</span><span class="d">#{esc(d.target)}
          <span class="rationale">#{esc(d.rationale)}</span></span></li>
        """

      %Effect{} = e ->
        detail =
          [e.ref["stage_id"], e.ref["dest"], e.payload["status"]]
          |> Enum.reject(&is_nil/1)
          |> Enum.join(" · ")

        ~s(<li><span class="t">#{ts_time(e.ts)}</span><span class="mk #{mk_class(e.type)}"></span><span class="k">#{esc(e.type)}</span><span class="d">#{esc(detail)}</span></li>)
    end)
  end

  defp commercial_timeline(run_id) do
    Proof.render(run_id)["timeline"]
    |> Enum.map_join("\n", fn row ->
      detail = [row["stage"], row["dest"]] |> Enum.reject(&is_nil/1) |> Enum.join(" · ")
      type = row["type"]

      ~s(<li><span class="t">#{ts_time(row["ts"])}</span><span class="mk #{mk_class(String.to_existing_atom(type))}"></span><span class="k">#{esc(type)}</span><span class="d">#{esc(detail)}</span></li>)
    end)
  end

  defp mk_class(type), do: Map.get(@mk_classes, type, "")

  defp report_header(run_id, state, cut) do
    caps =
      state.effects
      |> Enum.filter(&(&1.type == :capability_started))
      |> Enum.map(&(&1.ref["capability_ref"] || ""))
      |> Enum.uniq()
      |> Enum.map_join(", ", &digest/1)

    pipeline =
      case Mitos.Run.Config.load(run_id) do
        {:ok, config} -> Mitos.Pipeline.parse(config.pipeline).name
        _ -> nil
      end

    """
    <div class="card"><dl class="kv">
      <dt>run</dt><dd>#{mono(run_id)}</dd>
      #{if pipeline, do: "<dt>pipeline</dt><dd>#{esc(pipeline)}</dd>", else: ""}
      <dt>capability ids</dt><dd>#{mono(caps)}</dd>
      #{if cut == :dev, do: ~s(<dt>watch</dt><dd><a href="#{run_link(run_id)}">outer timeline + inner traces</a></dd>), else: ""}
    </dl></div>
    """
  end

  defp result_section(deliverable) do
    case deliverable["result"] do
      nil ->
        ~s(<div class="empty">no RESULT harvested</div>)

      r ->
        """
        <div class="card"><dl class="kv">
          <dt>signal</dt><dd><b>#{esc(r["signal"])}</b> <span class="faint">soft, advisory — never drives control flow</span></dd>
          <dt>confidence</dt><dd>#{esc(r["confidence"])}</dd>
          <dt>summary</dt><dd>#{esc(r["summary"])}</dd>
          #{if r["metrics"] not in [%{}, nil], do: "<dt>metrics</dt><dd><code>#{esc(Mitos.JSON.encode!(r["metrics"]))}</code></dd>", else: ""}
        </dl></div>
        """
    end
  end

  defp artifacts_section(run_id, artifacts) do
    rows =
      Enum.map_join(artifacts || [], "\n", fn a ->
        """
        <tr>
          <td><a href="/runs/#{run_id}/artifacts/#{esc(a["sha256"])}">#{esc(a["path"])}</a></td>
          <td class="num">#{a["bytes"]} B</td>
          <td>#{mono(digest(a["sha256"]))}</td>
        </tr>
        """
      end)

    if rows == "",
      do: "",
      else: """
      <div class="table-wrap"><table>
        <thead><tr><th>artifact</th><th>size</th><th>sha256</th></tr></thead>
        <tbody>#{rows}</tbody>
      </table></div>
      """
  end

  defp diff_section(""), do: ""
  defp diff_section(nil), do: ""

  defp diff_section(diff) do
    highlighted =
      diff
      |> String.slice(0, 20_000)
      |> String.split("\n")
      |> Enum.map_join("\n", fn
        "+" <> _ = line -> ~s(<span class="dadd">#{esc(line)}</span>)
        "-" <> _ = line -> ~s(<span class="ddel">#{esc(line)}</span>)
        "@@" <> _ = line -> ~s(<span class="dmeta">#{esc(line)}</span>)
        "diff " <> _ = line -> ~s(<span class="dmeta">#{esc(line)}</span>)
        "index " <> _ = line -> ~s(<span class="dmeta">#{esc(line)}</span>)
        line -> esc(line)
      end)

    "#{eyebrow("workspace diff")}<pre>#{highlighted}</pre>"
  end

  defp receipt_section(run_id, proof) do
    case Facilitator.settlement(run_id) do
      nil ->
        if proof["charge"], do: "", else: ""

      s ->
        txs =
          Enum.map_join(s.transfers, "\n", fn t ->
            ~s(<dt>#{esc(t.kind)}</dt><dd>#{usd(t.amount)} · #{mono(t.tx)}</dd>)
          end)

        """
        #{eyebrow("receipt — settlement of the meter's fold")}
        <div class="card"><dl class="kv">
          <dt>quote</dt><dd>#{mono(s.quote_id)}</dd>
          <dt>charge</dt><dd>#{usd(s.charge.buyer_charge)}</dd>
          #{if s.charge.buyer_refund > 0, do: "<dt>refund</dt><dd>#{usd(s.charge.buyer_refund)}</dd>", else: ""}
          #{txs}
        </dl></div>
        """
    end
  end

  # Dev cut only (11 §5): links into the inner zoom for each stage that has a
  # persisted harness trace.
  defp inner_trace_links(run_id, state) do
    links =
      state.stage_order
      |> Enum.filter(&(Runs.trace(run_id, &1, limit: 1).events != []))
      |> Enum.map_join(" · ", fn stage ->
        ~s(<a href="/web/runs/#{run_id}/trace/#{esc(stage)}">#{esc(stage)}</a>)
      end)

    if links == "", do: "", else: "#{eyebrow("inner traces")}<div class=\"card\">#{links}</div>"
  end

  defp settled_charge(run_id) do
    case Facilitator.settlement(run_id) do
      %{charge: charge} -> charge
      _ -> nil
    end
  end

  defp cut_note(:dev),
    do: "dev cut — adds rationale + inner traces; the proof page is the same render, redacted"

  # Buyer-side copy: say what they get, not what the system withheld.
  defp cut_note(:commercial),
    do:
      "what happened, verifiable from the event-log excerpt — the seller's method stays server-side"

  defp active_for("buyer"), do: "order"
  defp active_for(_), do: "endpoints"

  defp base_url, do: "http://localhost:#{Application.fetch_env!(:mitos, :http_port)}"

  # The declared input properties for the order form (12 §4). Supports the
  # JSON-Schema {properties: {...}} spelling and the flat {field: type} shorthand.
  defp input_properties(%{"schema" => %{"properties" => props}}) when is_map(props),
    do: Enum.to_list(props)

  defp input_properties(%{"schema" => schema}) when is_map(schema),
    do: schema |> Map.drop(["required", "type", "$schema"]) |> Enum.to_list()

  defp input_properties(%{"properties" => props}) when is_map(props), do: Enum.to_list(props)
  defp input_properties(_), do: []

  defp spec_required?(%{"required" => true}), do: true
  defp spec_required?(%{"required" => "true"}), do: true
  defp spec_required?(_), do: false
end
