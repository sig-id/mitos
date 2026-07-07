defmodule Mitos.Web.HTML do
  @moduledoc """
  Server-rendered HTML: layout, escaping, shared fragments. No build step, no
  client framework — a surface is `render(fold(events), cut)` (11 §1) and the
  render happens on the BEAM (01 §7). The only "live" mechanism is a
  meta-refresh while a run is non-terminal: the log is the state; the page
  re-reads it.

  Identity is a mock role cookie (operator | seller | buyer) standing in for
  an IdP; it selects the redaction cut and the navigation — never a different
  data source (08 §2: readers differ by what is cut, not where it comes from).
  """

  @doc "HTML-escape any term."
  def esc(nil), do: ""

  def esc(term) do
    term |> to_string() |> Plug.HTML.html_escape()
  end

  @roles %{
    "operator" => %{label: "Operator", class: "r-operator"},
    "seller" => %{label: "Seller", class: "r-seller"},
    "buyer" => %{label: "Buyer", class: "r-buyer"}
  }

  def roles, do: @roles

  def layout(title, body, opts \\ []) do
    role = opts[:role]

    refresh =
      case opts[:refresh] do
        nil -> ""
        secs -> ~s(<meta http-equiv="refresh" content="#{secs}">)
      end

    # highlight.js is pulled in only by surfaces that show code/JSON (the inner
    # trace). Served from a CDN; if offline, code simply renders unhighlighted.
    hljs =
      if opts[:hljs],
        do:
          ~s|<link rel="stylesheet" href="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.9.0/build/styles/atom-one-dark.min.css">| <>
            ~s|<script src="https://cdn.jsdelivr.net/gh/highlightjs/cdn-release@11.9.0/build/highlight.min.js" defer></script>| <>
            ~S|<script>window.addEventListener("DOMContentLoaded",function(){if(window.hljs){hljs.configure({ignoreUnescapedHTML:true});hljs.highlightAll()}})</script>|,
        else: ""

    """
    <!DOCTYPE html>
    <html lang="en">
    <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    #{refresh}
    <title>#{esc(title)} · mitos</title>
    #{hljs}
    <style>#{css()}</style>
    </head>
    <body>
    <header>
      <a class="brand" href="/web">mitos<span class="brand-dot">.</span></a>
      <nav>#{nav(role, opts[:active])}</nav>
      <div class="who">#{role_chip(role)}</div>
    </header>
    <main>
    #{body}
    </main>
    <footer>surfaces are readers of the log — render(fold(events), cut)</footer>
    </body>
    </html>
    """
  end

  # Navigation follows the persona (11 §3), not the other way around.
  defp nav(nil, _active), do: ""

  defp nav(role, active) do
    items =
      case role do
        "operator" ->
          [
            {"runs", "/web"},
            {"applications", "/web/apps"},
            {"endpoints", "/web/capabilities"},
            {"attribution", "/web/attribution"}
          ]

        "seller" ->
          [{"attribution", "/web/attribution"}, {"endpoints", "/web/capabilities"}]

        "buyer" ->
          [{"order", "/web/capabilities"}]

        _ ->
          []
      end

    Enum.map_join(items, "", fn {label, href} ->
      current = if active == label, do: ~s( class="current"), else: ""
      ~s(<a href="#{href}"#{current}>#{label}</a>)
    end)
  end

  defp role_chip(nil), do: ~s(<a class="chip chip-link" href="/web/login">sign in</a>)

  defp role_chip(role) do
    %{label: label, class: class} = Map.get(@roles, role, %{label: role, class: ""})

    """
    <span class="chip #{class}"><span class="dot"></span>#{esc(label)}</span>
    <a class="chip chip-link" href="/web/logout" title="mock identity — an IdP replaces this">switch</a>
    """
  end

  def status_badge(status) do
    s = to_string(status)
    ~s(<span class="pill s-#{esc(s)}"><span class="dot"></span>#{esc(s)}</span>)
  end

  @doc "A row of stat tiles: [{label, value, sub | nil}]."
  def stat_tiles(tiles) do
    inner =
      Enum.map_join(tiles, "\n", fn {label, value, sub} ->
        """
        <div class="tile">
          <div class="tile-label">#{esc(label)}</div>
          <div class="tile-value">#{value}</div>
          #{if sub, do: ~s(<div class="tile-sub">#{esc(sub)}</div>), else: ""}
        </div>
        """
      end)

    ~s(<div class="tiles">#{inner}</div>)
  end

  @doc "Section eyebrow — a labelled rule, not a heading."
  def eyebrow(text), do: ~s(<div class="eyebrow">#{esc(text)}</div>)

  def usd(nil), do: "$0.00"
  def usd(n) when is_number(n), do: "$" <> :erlang.float_to_binary(n / 1 * 1.0, decimals: 2)

  def ts_short(ts) when is_binary(ts), do: ts |> String.slice(0, 19) |> String.replace("T", " ")
  def ts_short(_), do: ""

  def ts_time(ts) when is_binary(ts), do: String.slice(ts, 11, 8)
  def ts_time(_), do: ""

  def run_link(run_id), do: "/web/runs/#{run_id}"

  def mono(text), do: ~s(<span class="mono">#{esc(text)}</span>)

  @doc "Mono wrapper for pre-built, already-escaped HTML (e.g. a link)."
  def mono_raw(html), do: ~s(<span class="mono">#{html}</span>)

  @doc "Shorten a content hash for display."
  def digest(nil), do: ""
  def digest(hash), do: String.slice(hash, 0, 12) <> "…"

  defp css do
    """
    :root {
      --bg:#0e1116; --panel:#151a22; --panel-2:#1a202b; --line:#262d39; --line-soft:#1f2530;
      --fg:#e8ebf0; --dim:#98a2b3; --faint:#5c6676;
      --accent:#d9a53f; --accent-soft:#3a3122;
      --ok:#4cc38a; --warn:#e5b454; --bad:#e5654f; --info:#6ca0f0;
      --sans:system-ui,-apple-system,"Segoe UI",sans-serif;
      --mono:ui-monospace,"SF Mono","Cascadia Code",Menlo,Consolas,monospace;
    }
    * { box-sizing:border-box; }
    body { margin:0; background:var(--bg); color:var(--fg); font:14px/1.55 var(--sans); }
    .mono, td.num, .tile-value { font-family:var(--mono); font-variant-numeric:tabular-nums; }

    header { display:flex; align-items:center; gap:2rem; padding:0 1.4rem; height:52px;
             border-bottom:1px solid var(--line); background:var(--panel);
             position:sticky; top:0; z-index:10; }
    .brand { font-family:var(--mono); font-weight:700; font-size:15px; letter-spacing:.04em;
             color:var(--fg); text-decoration:none; }
    .brand-dot { color:var(--accent); }
    nav { display:flex; gap:.2rem; flex:1; }
    nav a { color:var(--dim); text-decoration:none; padding:.35rem .7rem; border-radius:6px;
            font-size:13.5px; }
    nav a:hover { color:var(--fg); background:var(--panel-2); }
    nav a.current { color:var(--fg); background:var(--panel-2); }
    .who { display:flex; gap:.5rem; align-items:center; }
    .chip { display:inline-flex; align-items:center; gap:.4rem; font-size:12.5px;
            padding:.2rem .65rem; border-radius:99px; border:1px solid var(--line);
            color:var(--dim); text-decoration:none; }
    .chip .dot { width:7px; height:7px; border-radius:99px; background:var(--faint); }
    .chip.r-operator .dot { background:var(--info); }
    .chip.r-seller .dot { background:var(--accent); }
    .chip.r-buyer .dot { background:var(--ok); }
    .chip-link:hover { color:var(--fg); border-color:var(--dim); }

    main { max-width:1140px; margin:0 auto; padding:1.6rem 1.4rem 3rem; }
    footer { color:var(--faint); text-align:center; padding:1.2rem; font-size:12px;
             border-top:1px solid var(--line-soft); }
    h1 { font-size:19px; font-weight:650; letter-spacing:-.01em; margin:.2rem 0 .3rem;
         display:flex; align-items:center; gap:.7rem; flex-wrap:wrap; text-wrap:balance; }
    .sub { color:var(--dim); font-size:13px; margin:0 0 1.2rem; }
    .sub a { color:var(--info); }
    a { color:var(--info); text-decoration:none; }
    a:hover { text-decoration:underline; }
    :focus-visible { outline:2px solid var(--accent); outline-offset:2px; }

    .eyebrow { display:flex; align-items:center; gap:.8rem; margin:1.8rem 0 .7rem;
               color:var(--dim); font-size:11.5px; font-weight:600; letter-spacing:.12em;
               text-transform:uppercase; }
    .eyebrow::after { content:""; flex:1; height:1px; background:var(--line-soft); }

    .card { background:var(--panel); border:1px solid var(--line); border-radius:8px;
            padding:1rem 1.2rem; margin:.7rem 0; }
    .table-wrap { overflow-x:auto; border:1px solid var(--line); border-radius:8px;
                  background:var(--panel); margin:.7rem 0; }
    table { border-collapse:collapse; width:100%; font-size:13.5px; }
    th { text-align:left; color:var(--dim); font-weight:500; font-size:11.5px;
         letter-spacing:.08em; text-transform:uppercase; }
    td, th { padding:.55rem .9rem; border-bottom:1px solid var(--line-soft); vertical-align:top; }
    tr:last-child td { border-bottom:0; }
    tbody tr:hover td, tr.hoverable:hover td { background:var(--panel-2); }
    td.num { text-align:right; white-space:nowrap; color:var(--dim); }

    .pill { display:inline-flex; align-items:center; gap:.38rem; padding:.1rem .6rem;
            border-radius:99px; font-size:12px; font-weight:500; border:1px solid var(--line);
            color:var(--dim); white-space:nowrap; }
    .pill .dot { width:6px; height:6px; border-radius:99px; background:var(--faint); }
    .s-finished .dot, .s-done .dot { background:var(--ok); } .s-finished, .s-done { color:var(--ok); }
    .s-running .dot, .s-opened .dot { background:var(--info); } .s-running, .s-opened { color:var(--info); }
    .s-aborted .dot, .s-failed .dot { background:var(--bad); } .s-aborted, .s-failed { color:var(--bad); }
    .s-parked .dot, .s-blocked .dot { background:var(--warn); } .s-parked, .s-blocked { color:var(--warn); }

    .tiles { display:grid; grid-template-columns:repeat(auto-fit, minmax(150px, 1fr));
             gap:.7rem; margin:.7rem 0; }
    .tile { background:var(--panel); border:1px solid var(--line); border-radius:8px;
            padding:.75rem .95rem; }
    .tile-label { color:var(--dim); font-size:11.5px; letter-spacing:.08em; text-transform:uppercase; }
    .tile-value { font-size:20px; font-weight:600; margin-top:.15rem; }
    .tile-sub { color:var(--faint); font-size:12px; margin-top:.1rem; }

    .timeline { list-style:none; padding:0; margin:.4rem 0; font-family:var(--mono); font-size:12.8px; }
    .timeline li { display:flex; gap:.8rem; padding:.34rem .5rem; border-radius:6px;
                   align-items:baseline; }
    .timeline li:hover { background:var(--panel); }
    .timeline .t { color:var(--faint); font-variant-numeric:tabular-nums; flex-shrink:0; }
    .timeline .mk { flex-shrink:0; width:8px; height:8px; border-radius:99px;
                    background:var(--faint); align-self:center; }
    .timeline .k { color:var(--fg); }
    .timeline .d { color:var(--dim); overflow-wrap:anywhere; }
    .timeline li.decision { background:var(--accent-soft); }
    .timeline li.decision:hover { background:#453a26; }
    .timeline li.decision .mk { border-radius:1px; transform:rotate(45deg); background:var(--accent); }
    .timeline li.decision .k { color:var(--accent); font-weight:600; }
    .timeline .rationale { display:block; color:var(--warn); font-size:12px; opacity:.85; }
    .mk-ok { background:var(--ok) !important; } .mk-bad { background:var(--bad) !important; }
    .mk-info { background:var(--info) !important; } .mk-warn { background:var(--warn) !important; }

    pre { background:#0a0d12; border:1px solid var(--line); border-radius:8px;
          padding:.9rem 1rem; overflow-x:auto; font:12.5px/1.55 var(--mono);
          max-height:30rem; }
    pre .dadd { color:var(--ok); } pre .ddel { color:var(--bad); } pre .dmeta { color:var(--faint); }
    code { font-family:var(--mono); font-size:.93em; background:var(--panel-2);
           padding:.08rem .35rem; border-radius:4px; }
    pre code { background:none; padding:0; }

    .kv { display:grid; grid-template-columns:max-content 1fr; gap:.3rem 1.4rem; margin:0; }
    .kv dt { color:var(--dim); } .kv dd { margin:0; overflow-wrap:anywhere; }

    form label { display:block; color:var(--dim); margin:.9rem 0 .3rem; font-size:12.5px; }
    input[type=text], textarea { width:100%; background:#0a0d12; color:var(--fg);
      border:1px solid var(--line); border-radius:6px; padding:.55rem .7rem;
      font:13px/1.5 var(--mono); }
    input[type=text]:focus, textarea:focus { border-color:var(--accent); outline:none; }
    textarea { min-height:6.5rem; resize:vertical; }
    button, .btn { display:inline-block; background:var(--accent); color:#141005; border:0;
             border-radius:6px; padding:.55rem 1.3rem; font:600 13.5px var(--sans);
             cursor:pointer; margin-top:1rem; text-decoration:none; }
    button:hover, .btn:hover { filter:brightness(1.08); text-decoration:none; }

    .dim { color:var(--dim); } .faint { color:var(--faint); }
    .empty { color:var(--dim); text-align:center; padding:2.5rem 1rem;
             border:1px dashed var(--line); border-radius:8px; margin:.7rem 0; }

    /* login / role picker */
    .login { max-width:820px; margin:7vh auto 0; text-align:center; }
    .login .brand-big { font-family:var(--mono); font-size:34px; font-weight:700;
                        letter-spacing:.02em; }
    .login .tagline { color:var(--dim); margin:.4rem 0 2.4rem; }
    .role-grid { display:grid; grid-template-columns:repeat(auto-fit, minmax(220px, 1fr));
                 gap:1rem; text-align:left; }
    .role-card { display:block; background:var(--panel); border:1px solid var(--line);
                 border-radius:10px; padding:1.3rem 1.3rem 1.2rem; color:var(--fg);
                 text-decoration:none; transition:border-color .12s, transform .12s; }
    .role-card:hover { border-color:var(--accent); text-decoration:none; transform:translateY(-2px); }
    .role-card h3 { margin:.6rem 0 .35rem; font-size:16px; }
    .role-card p { margin:0; color:var(--dim); font-size:13px; }
    .role-card .cut { display:block; margin-top:.9rem; color:var(--faint); font-size:11.5px;
                      letter-spacing:.08em; text-transform:uppercase; }
    .role-card .dot { width:9px; height:9px; border-radius:99px; display:block; }
    .login .idp-note { color:var(--faint); font-size:12px; margin-top:2.2rem; }

    /* inner trace (dev cut) — a transcript of turns, each collapsible */
    .trace { display:flex; flex-direction:column; gap:.5rem; margin:.4rem 0; }
    .trace-setup { font-size:12px; color:var(--dim); border:1px dashed var(--line);
      border-radius:8px; padding:.5rem .8rem; }
    .trace-setup .k { color:var(--faint); }
    details.turn { background:var(--panel); border:1px solid var(--line);
      border-radius:8px; overflow:hidden; }
    details.turn > summary { list-style:none; cursor:pointer; padding:.6rem .9rem;
      display:flex; align-items:center; gap:.7rem; font-size:13px; user-select:none; }
    details.turn > summary::-webkit-details-marker { display:none; }
    details.turn > summary::before { content:"▸"; color:var(--faint); font-size:10px;
      transition:transform .12s; }
    details.turn[open] > summary::before { transform:rotate(90deg); }
    details.turn > summary:hover { background:var(--panel-2); }
    .turn-idx { font-family:var(--mono); font-weight:600; color:var(--fg); }
    .turn-meta { color:var(--dim); font-size:12px; display:flex; gap:.7rem; flex-wrap:wrap; }
    .turn-meta .b { background:var(--panel-2); border:1px solid var(--line);
      border-radius:99px; padding:.05rem .55rem; }
    .turn-meta .b-tools { color:var(--info); border-color:var(--line); }
    .turn-meta .b-sig-stop { color:var(--ok); }
    .turn-body { padding:.2rem .9rem .8rem; display:flex; flex-direction:column; gap:.5rem; }
    details.blk { border:1px solid var(--line-soft); border-radius:7px; background:#0a0d12; }
    details.blk > summary { list-style:none; cursor:pointer; padding:.45rem .7rem;
      font-family:var(--mono); font-size:12.5px; display:flex; align-items:center;
      gap:.5rem; user-select:none; }
    details.blk > summary::-webkit-details-marker { display:none; }
    details.blk > summary::before { content:"▸"; color:var(--faint); font-size:9px; }
    details.blk[open] > summary::before { transform:rotate(90deg); }
    details.blk > summary:hover { color:var(--fg); }
    .blk-think > summary { color:var(--warn); }
    .blk-think > summary::before { color:var(--warn); }
    .blk-think[open] > summary { border-bottom:1px solid var(--line-soft); }
    .blk-tool > summary { color:var(--info); }
    .blk-tool > summary .tname { font-weight:600; }
    .blk-tool .ok { color:var(--ok); } .blk-tool .err { color:var(--bad); }
    .blk-tool .seg { margin:0; border:0; border-top:1px solid var(--line-soft);
      border-radius:0; max-height:22rem; }
    .blk-tool .lbl { color:var(--faint); font-size:11px; letter-spacing:.08em;
      text-transform:uppercase; padding:.4rem .7rem 0; }
    .blk-text > summary { color:var(--fg); }
    .blk-text[open] > summary { border-bottom:1px solid var(--line-soft); }
    .trace pre, .blk pre { margin:0; border-radius:0; border:0; max-height:26rem; }
    details.blk > pre { border-top:1px solid var(--line-soft); }
    .mut { color:var(--faint); font-size:11px; }
    .trace-exit { font-size:12px; color:var(--dim); padding:.4rem .2rem; }
    @media (prefers-reduced-motion: reduce) { .role-card { transition:none; } details > summary::before { transition:none; } }
    """
  end
end
