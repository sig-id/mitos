defmodule Mitos.Web.Pages do
  @moduledoc """
  The console surfaces (11 §10, product shape 11 §13): home (launch + recent),
  run history, application detail (entrypoints, pipelines, I/O wiring), the
  entrypoint order form, watch/report with failure diagnosis + one-click
  re-run, sales (offers + attribution), and the storefront. Every page is a
  read of the log at a cut (11 §1); the **one renderer, two cuts** rule
  (11 §5) is `report/3` — the dev cut adds `rationale` and inner-trace links,
  the commercial cut is exactly the 08 §5 proof page plus the receipt.

  `role` (cookie; IdP later — locally defaults to operator, 11 §13.2) only
  selects navigation and the cut — never a different data source (08 §2). The
  copy rules are 11 §13.4: product language, benefits where earned, empty
  states that teach the next command.
  """

  import Mitos.Web.HTML

  alias Mitos.Event.{Decision, Effect}
  alias Mitos.Run.State
  alias Mitos.{Catalog, Facilitator, Log, Proof, Runs}

  # --- "view as": the cut preview picker (11 §13.2; an IdP replaces the cookie) ---

  def login(role \\ nil) do
    cards = [
      {"operator", "var(--info)", "Operator", "your console",
       "Everything: run entrypoints, watch every decision and inner trace, read your sales."},
      {"seller", "var(--accent)", "Seller", "preview",
       "What a seller sees of runs bought from them: revenue and operations — never the buyer's data."},
      {"buyer", "var(--ok)", "Buyer", "preview",
       "What a paying stranger sees: progress and the deliverable with its proof — never your method."}
    ]

    grid =
      Enum.map_join(cards, "\n", fn {role, color, label, kind, blurb} ->
        """
        <a class="role-card" href="/web/login/#{role}">
          <span class="dot" style="background:#{color}"></span>
          <h3>#{label} <span class="faint">· #{kind}</span></h3>
          <p>#{blurb}</p>
        </a>
        """
      end)

    body = """
    <div class="login">
      <div class="brand-big">mitos<span class="brand-dot">.</span></div>
      <p class="tagline">the workbench for agent pipelines</p>
      <div class="role-grid">#{grid}</div>
      <p class="idp-note">every view reads the same records, cut for a different party —
      useful for checking what you expose before you publish.
      Roles are a mock identity for now; hosted deployments put an IdP in this seat.</p>
    </div>
    """

    layout("view as", body, role: role)
  end

  # --- home: launch + recent (11 §13.3 — "what can I run, what just happened?") ---

  def home(role) do
    apps = Catalog.apps()
    recent = Runs.list() |> Enum.take(8)

    launcher =
      if apps == %{} do
        """
        <div class="callout">
          <p><b>No applications installed.</b> Serve one from a directory and its entrypoints appear here, runnable:</p>
          <pre>mitos serve --app path/to/app</pre>
          <p class="dim">An application bundles pipelines and capabilities behind typed entrypoints —
          have an Eve agent? <code>mitos import eve &lt;dir&gt;</code> converts it into one.</p>
        </div>
        """
      else
        Enum.map_join(apps, "\n", fn {app_id, app} -> app_launcher_card(app_id, app) end)
      end

    recent_table =
      if recent == [] do
        ~s(<div class="empty">nothing has run yet — order a job above, or <code>mitos run</code> from a terminal</div>)
      else
        runs_table(recent)
      end

    body = """
    #{eyebrow("run something")}
    #{launcher}
    #{eyebrow(~s(recent runs))}
    #{recent_table}
    <p class="sub" style="margin-top:.6rem"><a href="/web/runs">all runs →</a></p>
    """

    layout("home", body, role: role, active: "home")
  end

  defp app_launcher_card(app_id, app) do
    eps =
      Enum.map_join(app.entrypoints, "\n", fn ep ->
        action =
          if ep.draft,
            do: ~s(<span class="pill s-parked"><span class="dot"></span>draft</span>),
            else: ~s(<a class="btn btn-sm" href="/web/apps/#{app_id}/entrypoints/#{esc(ep.id)}">Run…</a>)

        """
        <div class="ep-row">
          <div class="ep-copy">
            <div class="ep-title">#{esc(ep.title || ep.id)}</div>
            #{if ep.description, do: ~s(<div class="ep-desc">#{esc(ep.description)}</div>), else: ""}
          </div>
          #{action}
        </div>
        """
      end)

    """
    <div class="app-card">
      <div class="app-head">
        <h3><a href="/web/apps/#{app_id}">#{esc(app.name)}</a></h3>
        <span class="faint">v#{esc(app.version)}</span>
      </div>
      #{if app.description, do: ~s(<div class="app-desc">#{esc(app.description)}</div>), else: ""}
      #{eps}
    </div>
    """
  end

  # --- runs: the full history (11 §13.3 — "what has this machine done?") ---

  def runs(role) do
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
        {"model spend", usd(spend), "across all runs"}
      ])

    table =
      if runs == [] do
        ~s(<div class="empty">no runs yet — start one from <a href="/web">Home</a> or <code>mitos run</code></div>)
      else
        runs_table(Enum.take(runs, 100))
      end

    body = """
    <h1>Runs</h1>
    <p class="sub">every run this control plane has executed — read straight from each run's on-disk record</p>
    #{tiles}
    #{table}
    """

    layout("runs", body, role: role, active: "runs")
  end

  defp runs_table(runs) do
    rows =
      Enum.map_join(runs, "\n", fn r ->
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

    """
    <div class="table-wrap"><table>
      <thead><tr><th>started</th><th>run</th><th>pipeline</th><th>status</th><th>stages</th><th>spent</th></tr></thead>
      <tbody>#{rows}</tbody>
    </table></div>
    """
  end

  # --- applications: what's installed, and how each one is wired (11 §13.3) ---

  def apps(role) do
    apps = Catalog.apps()

    cards =
      if apps == %{} do
        """
        <div class="callout">
          <p><b>No applications installed.</b> Serve one from a directory:</p>
          <pre>mitos serve --app path/to/app</pre>
          <p class="dim">An application bundles pipelines and capabilities behind typed,
          runnable entrypoints. <code>mitos import eve &lt;dir&gt;</code> converts an existing Eve agent.</p>
        </div>
        """
      else
        Enum.map_join(apps, "\n", fn {app_id, app} ->
          eps =
            Enum.map_join(app.entrypoints, " · ", fn ep ->
              ~s(<a href="/web/apps/#{app_id}/entrypoints/#{esc(ep.id)}">#{esc(ep.title || ep.id)}</a>)
            end)

          """
          <div class="app-card">
            <div class="app-head">
              <h3><a href="/web/apps/#{app_id}">#{esc(app.name)}</a></h3>
              <span class="faint">v#{esc(app.version)}</span>
              <span class="faint">#{mono(digest(app_id))}</span>
            </div>
            #{if app.description, do: ~s(<div class="app-desc">#{esc(app.description)}</div>), else: ""}
            <div class="dim" style="font-size:13px">
              #{length(app.entrypoints)} #{plural(length(app.entrypoints), "entrypoint")} — #{eps}
              · #{length(app.pipelines)} #{plural(length(app.pipelines), "pipeline")}
              · #{length(app.capabilities)} #{plural(length(app.capabilities), "capability", "capabilities")}
            </div>
          </div>
          """
        end)
      end

    body = """
    <h1>Applications</h1>
    <p class="sub">what's installed on this control plane — each bundles pipelines and capabilities behind runnable entrypoints</p>
    #{cards}
    """

    layout("applications", body, role: role, active: "applications")
  end

  # One application: entrypoints (runnable), the pipeline intent, the
  # capabilities it composes, and its I/O wiring — the "manage" surface.
  def app_detail(app_id, role) do
    case Catalog.find_app(app_id) do
      nil -> :not_found
      app -> {:ok, render_app_detail(app, role)}
    end
  end

  defp render_app_detail(app, role) do
    eps =
      Enum.map_join(app.entrypoints, "\n", fn ep ->
        params =
          Enum.map_join(ep.parameters, " · ", fn p ->
            default = if p["default"], do: ~s( = #{esc(p["default"])}), else: ""
            ~s(<code>#{esc(p["name"])}#{default}</code>)
          end)

        action =
          if ep.draft,
            do:
              ~s(<span class="pill s-parked"><span class="dot"></span>draft — needs a typed order form before it can run</span>),
            else: ~s(<a class="btn btn-sm" href="/web/apps/#{app.app_id}/entrypoints/#{esc(ep.id)}">Run…</a>)

        """
        <div class="ep-row">
          <div class="ep-copy">
            <div class="ep-title">#{esc(ep.title || ep.id)} <span class="faint">→ pipeline #{esc(ep.pipeline)}</span></div>
            #{if ep.description, do: ~s(<div class="ep-desc">#{esc(ep.description)}</div>), else: ""}
            #{if params != "", do: ~s(<div class="ep-desc">knobs: #{params}</div>), else: ""}
          </div>
          #{action}
        </div>
        """
      end)

    pipelines =
      Enum.map_join(app.pipelines, "\n", fn pl ->
        """
        <details class="fold">
          <summary>#{esc(pl.id)} <span class="faint">· #{esc(pl.file)}</span></summary>
          <pre>#{esc(pl.body)}</pre>
        </details>
        """
      end)

    caps =
      Enum.map_join(app.capabilities, "\n", fn c ->
        """
        <tr>
          <td>#{esc(c.as)}</td>
          <td>#{mono(c.ref)}</td>
          <td>#{mono(digest(c.capability_id))}</td>
        </tr>
        """
      end)

    body = """
    <h1>#{esc(app.name)} <span class="faint">v#{esc(app.version)}</span></h1>
    <p class="sub">#{if app.description, do: esc(app.description) <> " · ", else: ""}#{mono(digest(app.app_id))}</p>

    #{eyebrow("entrypoints — how this application is asked to work")}
    <div class="card">#{if eps == "", do: ~s(<span class="dim">none declared</span>), else: eps}</div>

    #{eyebrow("pipelines — the intent each entrypoint runs")}
    #{if pipelines == "", do: ~s(<div class="empty">no pipelines</div>), else: pipelines}

    #{eyebrow("capabilities — what the pipelines compose")}
    <div class="table-wrap"><table>
      <thead><tr><th>name</th><th>source</th><th>identity</th></tr></thead>
      <tbody>#{caps}</tbody>
    </table></div>

    #{io_section(app)}
    #{requirements_section(app)}
    """

    layout(app.name, body, role: role, active: "applications")
  end

  # The app's live I/O wiring (10 §4): how the world triggers it and where
  # results go. Webhook inputs get their POST URL — that's the wiring a
  # developer comes here to copy.
  defp io_section(app) do
    bindings =
      Mitos.IO.bindings()
      |> Enum.filter(&String.starts_with?(&1.name, app.name <> "."))

    inputs =
      bindings
      |> Enum.filter(&(&1.direction == :input))
      |> Enum.map_join("\n", fn b ->
        org = b.tenant["org"] || "local"

        trigger =
          case b.kind do
            :webhook -> ~s(<code>POST /ingest/#{esc(org)}/#{esc(b.name)}</code>)
            other -> ~s(<code>#{esc(other)}</code> connector)
          end

        ~s(<dt>#{esc(b.name)}</dt><dd>#{trigger}</dd>)
      end)

    outputs =
      bindings
      |> Enum.filter(&(&1.direction == :output))
      |> Enum.map_join("\n", fn b ->
        ~s(<dt>#{esc(b.name)}</dt><dd><code>#{esc(b.kind)}</code> sink, fires on <code>#{esc(b.on)}</code></dd>)
      end)

    declared = length(List.wrap(app.io["inputs"])) + length(List.wrap(app.io["outputs"]))

    gap_note =
      if declared > length(bindings) do
        ~s(<p class="dim" style="font-size:12.5px">the manifest declares #{declared} bindings; #{declared - length(bindings)} name connector/sink classes this control plane doesn't ship yet</p>)
      else
        ""
      end

    cond do
      inputs == "" and outputs == "" and declared == 0 ->
        ""

      inputs == "" and outputs == "" ->
        """
        #{eyebrow("i/o — triggers in, deliveries out")}
        <div class="card"><span class="dim">no live bindings</span>#{gap_note}</div>
        """

      true ->
        """
        #{eyebrow("i/o — triggers in, deliveries out")}
        <div class="card"><dl class="kv">#{inputs}#{outputs}</dl>#{gap_note}</div>
        """
    end
  end

  defp requirements_section(%{requirements: req}) when req == %{}, do: ""

  defp requirements_section(%{requirements: req}) do
    rows =
      [
        {"isolation", req["trust_tier"]},
        {"connectors", List.wrap(req["connectors"]) |> Enum.join(", ")},
        {"egress", List.wrap(req["egress"]) |> Enum.join(", ")}
      ]
      |> Enum.reject(fn {_, v} -> v in [nil, ""] end)
      |> Enum.map_join("\n", fn {k, v} -> ~s(<dt>#{esc(k)}</dt><dd>#{esc(v)}</dd>) end)

    if rows == "" do
      ""
    else
      """
      #{eyebrow("requires — declared before anything runs")}
      <div class="card"><dl class="kv">#{rows}</dl></div>
      """
    end
  end

  defp plural(1, singular), do: singular
  defp plural(_, singular), do: singular <> "s"
  defp plural(1, singular, _plural), do: singular
  defp plural(_, _singular, plural), do: plural

  def order_form(app_id, ep_id, role) do
    with app when not is_nil(app) <- Catalog.find_app(app_id),
         ep when not is_nil(ep) <- Mitos.App.Manifest.entrypoint(app, ep_id) do
      params =
        Enum.map_join(ep.parameters, "\n", fn p ->
          name = p["name"]
          hint = [p["type"] || "string", p["description"]] |> Enum.reject(&is_nil/1) |> Enum.join(" — ")

          """
          <label>#{esc(name)} <span class="faint">#{esc(hint)}</span></label>
          <input type="text" name="param_#{esc(name)}" value="#{esc(p["default"])}">
          """
        end)

      workspace_field =
        case ep.workspace do
          %{kind: "none"} ->
            ""

          %{kind: kind} ->
            """
            <label>workspace <span class="faint">#{esc(kind)} — fetched by the server; credentials never touch the browser</span></label>
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
      <p class="sub">#{esc(app.name)} · runs pipeline <code>#{esc(ep.pipeline)}</code>#{if ep.description, do: "<br>" <> esc(ep.description), else: ""}</p>
      <div class="card">
      <form method="post" action="/web/apps/#{app_id}/entrypoints/#{esc(ep.id)}/run">
        #{workspace_field}
        #{input_fields}
        #{params}
        <label>budget ceiling <span class="faint">USD · optional — the run stops rather than spend past it</span></label>
        <input type="text" name="budget_usd" placeholder="e.g. 5.00">
        <button type="submit">Run</button>
      </form>
      </div>
      <p class="sub">you'll land on the run as it executes — every decision shows up there as it's made</p>
      """

      {:ok, layout("run · #{ep.id}", body, role: role, active: "applications")}
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

      title =
        case {cut, pipeline_name(run_id)} do
          {:dev, name} when is_binary(name) -> esc(name)
          _ -> mono(run_id)
        end

      body = """
      <h1>#{title} #{status_badge(state.status)}</h1>
      <p class="sub">#{if cut == :dev, do: mono(run_id) <> " · ", else: ""}#{cut_note(cut)} · #{live_note}</p>
      #{if cut == :dev, do: failure_callout(run_id, state), else: ""}
      #{if cut == :dev and State.terminal?(state), do: run_actions(run_id, :watch), else: ""}
      #{stage_cards}
      #{if cut == :dev, do: order_panel(run_id), else: ""}
      #{eyebrow("timeline — every decision and its effects, in order")}
      <ul class="timeline">#{timeline}</ul>
      """

      {:ok, layout("watch · #{run_id}", body, role: role, refresh: refresh, active: "runs")}
    end
  end

  # --- run diagnosis + iteration (11 §13.3: "why did it fail?" → next run) ---

  # Surface the failure reason at the top instead of leaving it buried in the
  # timeline: the aborting decision's rationale, and any stage that reported a
  # failing signal (with its trace link — that's where the answer usually is).
  defp failure_callout(run_id, state) do
    aborted =
      if state.status == :aborted do
        case state.decisions |> Enum.filter(&(&1.type == :abort)) |> List.last() do
          %Decision{rationale: r} when is_binary(r) and r != "" ->
            ~s(<p><span class="kx">aborted</span> — #{esc(r)}</p>)

          _ ->
            ~s(<p><span class="kx">aborted</span> — no reason was recorded; the timeline below has the last decision taken</p>)
        end
      end

    failing =
      for id <- state.stage_order,
          s = state.stages[id],
          s.result && s.result["signal"] in ["fail", "failure", "error"] do
        trace =
          if Runs.trace(run_id, id, limit: 1).events != [],
            do: ~s( — <a href="/web/runs/#{run_id}/trace/#{esc(id)}">read its trace</a>),
            else: ""

        summary = if s.result["summary"], do: ": #{esc(s.result["summary"])}", else: ""
        ~s(<p><b>#{esc(id)}</b> reported <code>#{esc(s.result["signal"])}</code>#{summary}#{trace}</p>)
      end

    cond do
      aborted ->
        ~s(<div class="callout callout-bad">#{aborted}#{Enum.join(failing)}</div>)

      failing != [] ->
        ~s(<div class="callout callout-warn">#{Enum.join(failing)}</div>)

      true ->
        ""
    end
  end

  # The iterate step is one click (11 §13.3): re-start from the run's stored
  # order. Plus the two places a developer goes next, and the pre-publish check
  # of what a buyer would be shown.
  defp run_actions(run_id, current) do
    rerun = """
    <form method="post" action="/web/runs/#{run_id}/rerun">
      <button type="submit" title="start a fresh run from this run's stored order">Run again</button>
    </form>
    """

    links =
      [
        if(current != :watch, do: ~s(<a class="btn btn-ghost" href="/web/runs/#{run_id}">timeline</a>)),
        if(current != :report, do: ~s(<a class="btn btn-ghost" href="/web/runs/#{run_id}/report">report</a>)),
        ~s(<a class="btn btn-ghost" href="/web/runs/#{run_id}/proof" title="the redacted page a paying buyer would see">buyer's view</a>)
      ]
      |> Enum.reject(&is_nil/1)
      |> Enum.join("\n")

    ~s(<div class="actions">#{rerun}#{links}</div>)
  end

  # What this run was asked to do — inputs, knobs, workspace, ceiling. Reads
  # the stored order (config.json), so it works long after the run finished.
  defp order_panel(run_id) do
    case Mitos.Run.Config.load(run_id) do
      {:ok, c} ->
        rows =
          [
            {"inputs", if(c.inputs != %{}, do: Mitos.JSON.encode!(c.inputs))},
            {"parameters", if(c.parameters != %{}, do: Mitos.JSON.encode!(c.parameters))},
            {"workspace", c.workspace},
            {"budget", if(c.budget != %{}, do: Mitos.JSON.encode!(c.budget))}
          ]
          |> Enum.reject(fn {_, v} -> v in [nil, ""] end)
          |> Enum.map_join("\n", fn {k, v} -> ~s(<dt>#{esc(k)}</dt><dd><code>#{esc(v)}</code></dd>) end)

        if rows == "" do
          ""
        else
          """
          <details class="fold">
            <summary>the order — what this run was asked to do</summary>
            <div class="fold-body"><dl class="kv">#{rows}</dl></div>
          </details>
          """
        end

      _ ->
        ""
    end
  end

  defp pipeline_name(run_id) do
    case Mitos.Run.Config.load(run_id) do
      {:ok, config} -> Mitos.Pipeline.parse(config.pipeline).name
      _ -> nil
    end
  end

  # --- inner zoom: one capability's harness trace (11 §4, dev cut only) ---

  def trace(run_id, stage, role) do
    %{events: events, total_shown: shown, truncated: truncated, bytes: bytes} = Runs.trace(run_id, stage)

    if events == [] and bytes == 0 do
      :not_found
    else
      {setup, turns} = group_trace(events)

      banner = trace_banner(shown, truncated, bytes, stage)

      body = """
      <h1>inner trace · #{esc(stage)}</h1>
      <p class="sub">every step the agent took inside this capability — reasoning, tool calls,
      results · <a href="#{run_link(run_id)}">← run</a></p>
      #{banner}
      <div class="trace">
        #{render_trace_setup(setup)}
        #{Enum.map_join(Enum.with_index(turns, 1), "\n", fn {items, i} -> render_turn(i, items) end)}
      </div>
      """

      {:ok, layout("trace · #{stage}", body, role: role, active: "runs", hljs: true)}
    end
  end

  defp trace_banner(shown, truncated, bytes, stage) do
    cond do
      truncated ->
        ~s|<p class="sub warn">⚠ showing the first #{shown} entries of a larger trace (#{mb(bytes)} MB on disk). Bounded at read time; inspect <code>trace/#{esc(stage)}.jsonl</code> for the full record.</p>|

      bytes > 5_000_000 ->
        ~s|<p class="sub">#{shown} entries · #{mb(bytes)} MB on disk (read-time healed from raw frames)</p>|

      true ->
        ""
    end
  end

  # Group a flat event stream into [setup-items] and a list of turns. A
  # `turn_start` status opens a new turn; everything before the first one is the
  # session setup (session/agent_start, bootstrap stderr). Within a turn the
  # items arrive roughly in order: reasoning, answer text, tool calls + results,
  # per-turn usage, final stop-reason.
  defp group_trace(events) do
    {setup_rev, turns_rev} =
      Enum.reduce(events, {[], []}, fn e, {setup, turns} ->
        item = trace_item(e)

        case e do
          %{"type" => "status", "status" => "turn_start"} ->
            {setup, [[] | turns]}

          %{"type" => "status", "status" => s} when s in ["session", "agent_start"] ->
            {[{:status, s} | setup], turns}

          _ ->
            push_item(item, setup, turns)
        end
      end)

    {Enum.reverse(setup_rev), turns_rev |> Enum.reverse() |> Enum.map(&Enum.reverse/1)}
  end

  defp trace_item(%{"type" => "assistant_text"} = e) do
    if e["thinking"], do: {:think, e["text"] || ""}, else: {:text, e["text"] || ""}
  end

  defp trace_item(%{"type" => "tool_use"} = e), do: {:tool_use, e}
  defp trace_item(%{"type" => "tool_result"} = e), do: {:tool_result, e}
  defp trace_item(%{"type" => "token_usage"} = e), do: {:usage, e}
  defp trace_item(%{"type" => "final_result"} = e), do: {:final, e}
  defp trace_item(%{"type" => "stderr"} = e), do: {:stderr, e["line"] || ""}
  defp trace_item(%{"type" => "status"} = e), do: {:status, e["status"]}
  defp trace_item(_), do: nil

  defp push_item(nil, setup, turns), do: {setup, turns}
  defp push_item(item, setup, []), do: {[item | setup], []}
  defp push_item(item, setup, [cur | rest]), do: {setup, [[item | cur] | rest]}

  defp render_trace_setup([]), do: ""

  defp render_trace_setup(items) do
    inner =
      items
      |> Enum.map(fn
        {:stderr, line} -> ~s(<span class="k">stderr</span> #{esc(String.slice(line, 0, 300))}<br>)
        {:status, s} -> ~s(<span class="k">#{esc(s)}</span> )
        # A harness whose protocol isn't turn-based (e.g. the Fake test harness)
        # never opens a turn, so its real content lands here — render it with the
        # same blocks a turn body uses rather than dropping it.
        item -> render_trace_item(item)
      end)
      |> Enum.join("\n")

    ~s(<div class="trace-setup">#{inner}</div>)
  end

  # Render one turn as a <details> card. Summary carries the turn index and
  # quick stats (tool count, tokens, stop reason); the body is reasoning +
  # answer text + tool calls, each individually collapsible.
  defp render_turn(idx, items) do
    paired = pair_tools(items)

    ntools = Enum.count(paired, &match?({:tool, _, _}, &1))
    {:usage, usage} = Enum.find(paired, {:usage, nil}, &match?({:usage, _}, &1))
    {:final, final} = Enum.find(paired, {:final, nil}, &match?({:final, _}, &1))

    meta =
      [
        if(ntools > 0,
          do: ~s(<span class="b b-tools">#{ntools} #{if ntools == 1, do: "tool", else: "tools"}</span>),
          else: ""
        ),
        if(usage && usage["tokens"], do: ~s(<span class="b">#{esc(usage["tokens"])} tok</span>), else: ""),
        if(final && final["subtype"],
          do: ~s(<span class="b b-sig-#{sig_class(final["subtype"])}">#{esc(final["subtype"])}</span>),
          else: ""
        )
      ]
      |> Enum.reject(&(&1 == ""))
      |> Enum.join("\n")

    body = paired |> Enum.map(&render_trace_item/1) |> Enum.reject(&(&1 == "")) |> Enum.join("\n")

    """
    <details class="turn" open>
      <summary><span class="turn-idx">turn #{idx}</span><span class="turn-meta">#{meta}</span></summary>
      <div class="turn-body">#{body}</div>
    </details>
    """
  end

  # Pair each tool_use with its tool_result (by tool_use_id when present, else
  # by order) so a call and its output render as one block. Preserves the
  # position of the first of the pair.
  defp pair_tools(items) do
    {out, pending} =
      Enum.reduce(items, {[], []}, fn
        {:tool_use, e}, {out, pend} -> {out, pend ++ [e]}

        {:tool_result, e}, {out, pend} ->
          {tu, rest} = take_tool(pend, e["tool_use_id"])
          {out ++ [{:tool, tu, e}], rest}

        it, {out, pend} ->
          {out ++ [it], pend}
      end)

    out ++ for tu <- pending, do: {:tool, tu, nil}
  end

  defp take_tool([], _), do: {nil, []}
  defp take_tool([h | t], nil), do: {h, t}

  defp take_tool(pend, id) do
    case Enum.split_with(pend, &(&1["tool_use_id"] == id)) do
      {[h | _], rest} -> {h, rest}
      _ -> take_tool(pend, nil)
    end
  end

  defp render_trace_item({:think, text}) do
    ~s(<details class="blk blk-think"><summary>reasoning <span class="mut">~#{div(byte_size(text), 4)} tok</span></summary><pre>#{esc(String.slice(text, 0, 20_000))}</pre></details>)
  end

  defp render_trace_item({:text, text}) do
    ~s(<details class="blk blk-text" open><summary>assistant</summary><pre>#{esc(String.slice(text, 0, 20_000))}</pre></details>)
  end

  defp render_trace_item({:tool, tu, tr}) do
    name = (tu && tu["name"]) || (tr && tr["name"]) || "?"

    status =
      cond do
        is_nil(tr) -> ~s(<span class="mut">no result</span>)
        tr["isError"] -> ~s(<span class="err">error</span>)
        true -> ~s(<span class="ok">ok</span>)
      end

    input = if tu, do: tool_input_block(tu["name"], tu["input"] || %{}), else: ""
    result = if tr, do: tool_result_block(tr), else: ""

    """
    <details class="blk blk-tool">
      <summary><span class="tname">#{esc(name)}</span>#{status}</summary>
      #{input}
      #{result}
    </details>
    """
  end

  defp render_trace_item({:usage, _}), do: ""
  defp render_trace_item({:final, _}), do: ""

  defp render_trace_item({:stderr, line}) do
    ~s(<details class="blk blk-tool"><summary><span class="tname">stderr</span></summary><pre>#{esc(String.slice(line, 0, 4_000))}</pre></details>)
  end

  defp render_trace_item({:status, s}), do: ~s(<div class="mut">#{esc(inspect(s))}</div>)

  # Tool input: a bash/shell command renders as bash (much nicer than the JSON
  # wrapper); everything else as JSON.
  defp tool_input_block(name, input) when name in ["bash", "shell", "sh", "zsh"] do
    case input do
      %{"command" => cmd} when is_binary(cmd) ->
        ~s(<div class="lbl">command</div><pre><code class="language-bash">#{esc(cmd)}</code></pre>)

      _ ->
        json_block(input, "input")
    end
  end

  defp tool_input_block(_, input), do: json_block(input, "input")

  defp tool_result_block(tr) do
    content = to_string(tr["content"] || "")

    cond do
      content == "" ->
        ""

      match?({:ok, _}, Mitos.JSON.decode(content)) ->
        json_block(String.slice(content, 0, 20_000), "result")

      true ->
        ~s(<div class="lbl">result</div><pre>#{esc(String.slice(content, 0, 20_000))}</pre>)
    end
  end

  defp json_block(term, label) when is_binary(term) do
    ~s(<div class="lbl">#{esc(label)}</div><pre><code class="language-json">#{esc(term)}</code></pre>)
  end

  defp json_block(term, label) do
    ~s(<div class="lbl">#{esc(label)}</div><pre><code class="language-json">#{esc(Mitos.JSON.encode!(term))}</code></pre>)
  end

  defp sig_class("stop"), do: "stop"
  defp sig_class("toolUse"), do: "stop"
  defp sig_class(_), do: "stop"

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
          {"model spend", usd(proof["token_totals"]["usd"]),
           if(cut == :dev, do: "on your key", else: "on the seller's key")},
          {"wall", "#{proof["wall_ms"]} ms", nil},
          {"egress crossings", length(proof["egress"]), "brokered, logged"},
          {"artifacts", length(proof["artifacts"]), "content-addressed"}
        ])

      hash_note =
        if cut == :dev,
          do: "— recompute it from the event excerpt; the replay and audit anchor",
          else: "— recompute it from the event excerpt; a third party can verify this record"

      body = """
      <h1>#{if cut == :dev, do: "Run report", else: "Proof of work"} #{status_badge(proof["status"])}</h1>
      <p class="sub">#{cut_note(cut)}</p>
      #{if cut == :dev, do: failure_callout(run_id, state), else: ""}
      #{if cut == :dev, do: run_actions(run_id, :report), else: ""}
      #{header}
      #{eyebrow("deliverables")}
      #{result_section(deliverable)}
      #{artifacts_section(run_id, deliverable["artifacts"])}
      #{diff_section(deliverable["diff"])}
      #{eyebrow("totals")}
      #{totals}
      <div class="card"><dl class="kv">
        <dt>log hash</dt><dd>#{mono(proof["log_hash"])}
          <span class="faint">#{hash_note}</span></dd>
      </dl></div>
      #{receipt}
      #{if cut == :dev, do: order_panel(run_id), else: ""}
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

  # --- the storefront: what's for sale + buy instructions (11 §9: buy is a URL) ---

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
        if role in ["operator", "seller"] do
          """
          <div class="callout">
            <p><b>Nothing is for sale yet.</b> Publish a capability as a paid endpoint:</p>
            <pre>mitos serve path/to/capability --price 5.00 --payout 0xYOU --seller you</pre>
            <p class="dim">buyers then order it from this page — or from a script, with a wallet; no account needed</p>
          </div>
          """
        else
          ~s(<div class="empty">nothing is for sale on this control plane yet</div>)
        end
      else
        """
        <div class="table-wrap"><table>
          <thead><tr><th>what you get</th><th>identity</th><th>isolation</th><th>price per job</th><th></th></tr></thead>
          <tbody>#{rows}</tbody>
        </table></div>
        """
      end

    body = """
    <h1>Storefront</h1>
    <p class="sub">completed jobs at a flat price — each runs isolated and ships with proof of what happened#{if role in ["operator", "seller"], do: ". This is the page buyers see.", else: ""}</p>
    #{table}
    """

    layout("storefront", body, role: role, active: active_for(role))
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
        <p class="sub">no account, no install — a wallet and a URL. Three HTTP calls, whether you're a person or an agent.</p>
        <div class="card">
        #{eyebrow("1 · ask the price — the endpoint answers 402 with a quote")}
        <pre>#{esc(quote_cmd)}</pre>
        #{eyebrow("2 · pay it — repeat the call with the payment attached")}
        <pre>#{esc(pay_cmd)}</pre>
        #{eyebrow("3 · receive")}
        <p class="dim">the response carries your <code>status_url</code> and <code>proof_url</code>:
        watch progress read-only at <code>/web/runs/&lt;run_id&gt;/watch</code>, and collect the
        deliverable — artifacts, diff, receipt — at <code>/web/runs/&lt;run_id&gt;/proof</code>.
        If the run fails, you're refunded; delivery is decided by the run's own record, not by the seller.</p>
        </div>
        """

        {:ok, layout("order · #{offer.name}", body, role: role, active: active_for(role))}
    end
  end

  # --- sales: published offers + the flat revenue list (11 §7, 08 §6) ---

  def attribution(role) do
    settlements = Facilitator.settlements()

    total_net =
      settlements |> Enum.map(fn {_r, s} -> s.charge.seller_net end) |> Enum.sum()

    total_gmv =
      settlements
      |> Enum.filter(fn {_r, s} -> s.charge.delivered? end)
      |> Enum.map(fn {_r, s} -> s.charge.buyer_charge end)
      |> Enum.sum()

    offers =
      for {id, m} <- Catalog.capabilities(), offer = Catalog.get_offer(id), offer != nil do
        """
        <tr>
          <td>#{esc(m.name)} <span class="faint">v#{esc(m.version)}</span></td>
          <td>#{mono(digest(id))}</td>
          <td class="num">#{usd(offer.price_usd)}</td>
          <td><a href="/web/capabilities/#{id}">storefront page →</a></td>
        </tr>
        """
      end

    offers_section =
      if offers == [] do
        """
        <div class="callout">
          <p><b>Nothing published yet.</b> Turn a capability into a paid endpoint:</p>
          <pre>mitos serve path/to/capability --price 5.00 --payout 0xYOU --seller you</pre>
          <p class="dim">repricing never changes the capability's identity — the offer lives beside it</p>
        </div>
        """
      else
        """
        <div class="table-wrap"><table>
          <thead><tr><th>published</th><th>identity</th><th>price per job</th><th></th></tr></thead>
          <tbody>#{Enum.join(offers, "\n")}</tbody>
        </table></div>
        """
      end

    tiles =
      stat_tiles([
        {"settled runs", map_size(settlements), nil},
        {"delivered sales", usd(total_gmv), "what buyers paid"},
        {"net revenue", usd(total_net), "yours, after the platform take"}
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
              <td class="num">#{usd(s.charge.provider_cost)}</td>
              <td class="num">#{s.charge.compute_ms} ms · #{s.charge.egress_units} crossings</td>
            </tr>
            """
          end)

        """
        #{eyebrow("#{(m && m.name) || digest(cap_id)} · #{digest(cap_id)} — net #{usd(net)}")}
        <div class="table-wrap"><table>
          <thead><tr><th>run</th><th>outcome</th><th>buyer paid</th><th>your net</th><th>model cost (your key)</th><th>operations</th></tr></thead>
          <tbody>#{rows}</tbody>
        </table></div>
        """
      end)

    body = """
    <h1>Sales</h1>
    <p class="sub">what you've published and what it has earned — you see revenue and operations,
    never the buyer's data</p>
    #{eyebrow("published offers")}
    #{offers_section}
    #{eyebrow("settled")}
    #{tiles}
    #{if sections == "", do: ~s(<div class="empty">no settled sales yet — share a storefront link and let a stranger pay</div>), else: sections}
    """

    layout("sales", body, role: role, active: "sales")
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

    pipeline = pipeline_name(run_id)

    """
    <div class="card"><dl class="kv">
      <dt>run</dt><dd>#{mono(run_id)}</dd>
      #{if pipeline, do: "<dt>pipeline</dt><dd>#{esc(pipeline)}</dd>", else: ""}
      <dt>capability ids</dt><dd>#{mono(caps)}</dd>
      #{if cut == :dev, do: ~s(<dt>watch</dt><dd><a href="#{run_link(run_id)}">live timeline + inner traces</a></dd>), else: ""}
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
          <dt>signal</dt><dd><b>#{esc(r["signal"])}</b> <span class="faint">the capability's own assessment — advisory</span></dd>
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
    do: "your run, full fidelity — every decision with its reasoning, and each capability's inner trace"

  # Buyer-side copy: say what they get, not what the system withheld.
  defp cut_note(:commercial),
    do: "what happened, verifiable from this run's event record — the seller's method stays private"

  defp active_for("buyer"), do: "order"
  defp active_for(_), do: "storefront"

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
