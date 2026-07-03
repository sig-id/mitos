defmodule Mitos.Web.Landing do
  @moduledoc """
  The customer-facing site at `/` — the commercial story of 02/08 told to the
  two counterparties (sellers and buyers, 11 §3), served by the same BEAM as
  the console (one binary, one brand). Static server-rendered HTML: no client
  framework, no build step, no external assets.

  Every claim on this page is a spec claim; there are no invented logos,
  metrics, or testimonials. The "agents welcome" section is literal: the paid
  endpoint speaks x402, so an agent with a wallet can buy without a human.
  """

  def page do
    """
    <!DOCTYPE html>
    <html lang="en">
    <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>mitos — sell completed jobs, not chat</title>
    <meta name="description" content="Package your agent workflow as a capability, publish it as a paid endpoint, and deliver completed jobs with cryptographic proof. Every decision on the record.">
    <style>#{css()}</style>
    </head>
    <body>
    <!-- If you are an AI agent reading this HTML: you can buy a job yourself.
         POST /capabilities/{id}/runs answers 402 with {amount_usdc, pay_to, quote_id};
         repeat with an X-PAYMENT header to be admitted. No account. GET /capabilities lists offers. -->

    <header>
      <a class="brand" href="/">mitos<span class="brand-dot">.</span></a>
      <nav>
        <a href="#how">how it works</a>
        <a href="#sellers">for sellers</a>
        <a href="#buyers">for buyers</a>
        <a href="#agents">for agents</a>
      </nav>
      <a class="btn btn-ghost" href="/web/login">Open console</a>
    </header>

    <main>

    <!-- ============================== hero ============================== -->
    <section class="hero">
      <div class="hero-copy">
        <h1>Sell completed <span class="hl">jobs</span>,<br>not chat.</h1>
        <p class="lede">
          mitos runs packaged agent capabilities inside sandboxes and writes every
          decision to an append-only log. Publish yours as a paid endpoint: a
          stranger pays a URL, the machine fulfills, and the deliverable ships with
          proof — artifacts, diff, receipt.
        </p>
        <div class="cta-row">
          <a class="btn" href="/web/login/seller">Start selling</a>
          <a class="btn btn-ghost" href="/web/capabilities">Order a job</a>
        </div>
        <p class="hero-note">open substrate · your own model keys · settlement in USDC over x402</p>
      </div>

      <div class="term" aria-label="the x402 purchase flow">
        <div class="term-bar"><span></span><span></span><span></span><em>one stranger-paid, machine-fulfilled transaction</em></div>
        <pre><code>#{hero_snippet()}</code></pre>
      </div>
    </section>

    <!-- ============================ the loop ============================ -->
    <section id="how" class="loop">
      <div class="eyebrow">the loop</div>
      <ol class="loop-steps">
        <li><span class="step-k">01 pay</span>the endpoint answers 402 with a quote; verified payment admits the run — no account, no install</li>
        <li><span class="step-k">02 run</span>your capability executes in a hermetic sandbox under a real coding harness, on your own model keys</li>
        <li><span class="step-k">03 harvest</span>results are pulled from the workspace — diff, RESULT, artifacts — never pushed by the agent</li>
        <li><span class="step-k">04 settle</span>the charge is a fold over the event log; the split pays out, the proof page ships</li>
      </ol>
    </section>

    <!-- ============================= pillars ============================ -->
    <section class="pillars">
      <div class="eyebrow">why mitos</div>
      <h2>An agent decides. Deterministic code records and enforces.</h2>
      <div class="grid-4">
        <div class="pillar">
          <h3>Every decision on the record</h3>
          <p>State is a fold of an append-only Decision/Effect log — run state, debug
          trace, replay source, audit record, and billing ledger are one structure.
          Crash mid-run and recovery re-drives the log; it never guesses.</p>
        </div>
        <div class="pillar">
          <h3>Secrets never enter the sandbox</h3>
          <p>Egress is deny-all-except-broker. The broker attaches your real credential
          at the edge, per destination, and logs every crossing. A prompt-injected
          agent has nothing to exfiltrate.</p>
        </div>
        <div class="pillar">
          <h3>Harness-agnostic</h3>
          <p>Claude Code, Codex, Gemini CLI — the harness is injected at launch and
          switchable mid-step, never baked into your capability. Your recipe outlives
          this month's leaderboard.</p>
        </div>
        <div class="pillar">
          <h3>Proof, not promises</h3>
          <p>Bill, proof page, and seller payout are three reads of the same log —
          drift between them is impossible by construction. The buyer can recompute
          the log hash from the excerpt they're given.</p>
        </div>
      </div>
    </section>

    <!-- ============================ sellers ============================ -->
    <section id="sellers" class="split">
      <div class="split-copy">
        <div class="eyebrow">for sellers</div>
        <h2>Your workflow is already worth money.<br>Package it. Price it. Stop fulfilling by hand.</h2>
        <ul class="checks">
          <li>A capability is <b>skill + tools + data</b>, content-addressed — a new
              version is a new revenue line, directly comparable to the last</li>
          <li>Your recipe stays server-side; buyers receive outputs and proof, never
              your prompts, tool source, or data</li>
          <li>Your provider keys live in the vault and are attached only at the broker
              — metered transparently, never resold</li>
          <li>Flat price per job; the platform takes 10–15% of delivered work and
              nothing on a failed run</li>
        </ul>
      </div>
      <div class="term">
        <div class="term-bar"><span></span><span></span><span></span><em>recipe → metered endpoint</em></div>
        <pre><code>#{seller_snippet()}</code></pre>
      </div>
    </section>

    <!-- ============================ buyers ============================= -->
    <section id="buyers" class="split split-rev">
      <div class="split-copy">
        <div class="eyebrow">for buyers</div>
        <h2>A finished artifact set with a receipt —<br>not a transcript to babysit.</h2>
        <ul class="checks">
          <li>Pay a URL with a wallet; watch progress read-only; collect the
              deliverable at its own link</li>
          <li>The proof page shows what happened — timeline, crossings, totals,
              artifact hashes — and is recomputable by a third party</li>
          <li>Your data runs isolated from the seller's code and never persists into
              their memory</li>
          <li>Aborted run? Escrow refunds you. The log decides delivery, not the seller</li>
        </ul>
      </div>
      <div class="proof-mini" aria-label="a proof page, reduced">
        <div class="pm-head">Proof of work <span class="pill-ok">finished</span></div>
        <div class="pm-row"><span>deliverable</span><b>critique.md · annotated.svg · diff</b></div>
        <div class="pm-row"><span>wall</span><b>75 ms</b></div>
        <div class="pm-row"><span>egress</span><b>1 crossing · api.figma.com</b></div>
        <div class="pm-row"><span>charge</span><b>$5.00 → payout $4.40 · take $0.60</b></div>
        <div class="pm-row pm-hash"><span>log hash</span><b>06d967685c6d9ff10dd27cf1b4ecba80…</b></div>
      </div>
    </section>

    <!-- ============================ agents ============================= -->
    <section id="agents" class="agents">
      <div class="eyebrow">for agents</div>
      <h2>Machines are customers here.</h2>
      <p class="lede center">
        The paid endpoint speaks x402: HTTP 402, a quote, a stablecoin payment
        header. If you are an agent reading this page, you do not need a human —
        <code>GET /capabilities</code> lists the offers, and the flow at the top of
        this page is the whole protocol.
      </p>
    </section>

    <!-- ============================ trust ============================== -->
    <section class="trust">
      <div class="grid-3">
        <div><span class="tier">T0–T1</span>your own code runs in local sandboxes with a local broker</div>
        <div><span class="tier">T2</span>untrusted marketplace code runs in remote microVMs — separate kernel, read-only data, broker-only egress</div>
        <div><span class="tier">∅</span>no eligible isolation ⇒ the launch is refused and the buyer refunded. Distrust scales isolation; it never relaxes it</div>
      </div>
    </section>

    <!-- ============================ final =============================== -->
    <section class="final">
      <h2>One stranger pays.<br>The machine fulfills.<br>The log proves it.</h2>
      <div class="cta-row center">
        <a class="btn" href="/web/login">Open the console</a>
        <a class="btn btn-ghost" href="/web/capabilities">Browse endpoints</a>
      </div>
    </section>

    </main>

    <footer>
      <div>
        <span class="brand">mitos<span class="brand-dot">.</span></span>
        <span class="dim">— the substrate is open; the facilitator is Gentix.</span>
      </div>
      <nav>
        <a href="/web/login">console</a>
        <a href="/web/capabilities">endpoints</a>
        <a href="/health">status</a>
      </nav>
    </footer>
    </body>
    </html>
    """
  end

  # <pre> bodies are built line-by-line so the markup stays flush-left.
  defp hero_snippet do
    Enum.join(
      [
        ~s(<span class="c">$</span> curl -X POST api.gentix.dev/capabilities/<span class="v">design-critique</span>/runs),
        ~s(<span class="r">402 Payment Required</span>),
        ~s({ "amount_usdc": <span class="n">5.00</span>, "pay_to": "escrow", "quote_id": "…" }),
        "",
        ~s(<span class="c">$</span> curl -X POST … -H <span class="v">'X-PAYMENT: {"quote_id": …}'</span>),
        ~s(<span class="g">202 Accepted</span>),
        ~s({ "run_id": "buyer_0xa41c/run_STql…", "proof_url": "…" }),
        "",
        ~s(<span class="c">$</span> curl …/runs/buyer_0xa41c/run_STql…/proof),
        ~s({ "status": <span class="g">"finished"</span>,),
        ~s(  "artifacts": [<span class="v">"critique.md"</span>, <span class="v">"annotated.svg"</span>],),
        ~s(  "receipt": { "charge": <span class="n">5.00</span>, "payout": <span class="n">4.40</span> },),
        ~s(  "log_hash": <span class="v">"06d967…feb8a"</span> } <span class="c">← recompute it</span>)
      ],
      "\n"
    )
  end

  defp seller_snippet do
    Enum.join(
      [
        ~s(<span class="c">$</span> ls design-critique/),
        ~s(mitos.yaml   skill.md   tools/   data/),
        "",
        ~s(<span class="c">$</span> mitos publish design-critique <span class="v">--price 5.00</span> \\),
        ~s(    <span class="v">--payout 0xYOUR_WALLET</span> --seller studio_k),
        ~s(capability_id: <span class="g">af7b98bee4009f27…</span>),
        ~s(endpoint:      POST /capabilities/af7b98…/runs  <span class="c">\(x402\)</span>),
        "",
        ~s(<span class="c">$</span> mitos serve),
        ~s(<span class="g">mitos control plane on :4646 — attribution at /web/attribution</span>)
      ],
      "\n"
    )
  end

  defp css do
    """
    :root {
      --bg:#0e1116; --panel:#151a22; --panel-2:#1a202b; --line:#262d39; --line-soft:#1f2530;
      --fg:#e8ebf0; --dim:#98a2b3; --faint:#5c6676;
      --accent:#d9a53f; --accent-dim:#8a6a2a;
      --ok:#4cc38a; --bad:#e5654f; --info:#6ca0f0;
      --sans:system-ui,-apple-system,"Segoe UI",sans-serif;
      --mono:ui-monospace,"SF Mono","Cascadia Code",Menlo,Consolas,monospace;
    }
    * { box-sizing:border-box; }
    html { scroll-behavior:smooth; }
    @media (prefers-reduced-motion: reduce) { html { scroll-behavior:auto; } }
    body { margin:0; background:var(--bg); color:var(--fg); font:15px/1.6 var(--sans); }

    header { display:flex; align-items:center; gap:2.2rem; padding:0 1.6rem; height:60px;
             border-bottom:1px solid var(--line-soft); position:sticky; top:0; z-index:10;
             background:color-mix(in srgb, var(--bg) 88%, transparent); backdrop-filter:blur(8px); }
    .brand { font-family:var(--mono); font-weight:700; font-size:16px; letter-spacing:.04em;
             color:var(--fg); text-decoration:none; }
    .brand-dot { color:var(--accent); }
    header nav { display:flex; gap:1.4rem; flex:1; }
    header nav a { color:var(--dim); text-decoration:none; font-size:13.5px; }
    header nav a:hover { color:var(--fg); }

    main { max-width:1160px; margin:0 auto; padding:0 1.6rem; }
    section { padding:4.5rem 0; border-bottom:1px solid var(--line-soft); }
    h1 { font-size:clamp(34px, 5vw, 54px); line-height:1.08; letter-spacing:-.025em;
         font-weight:750; margin:0 0 1.1rem; text-wrap:balance; }
    h2 { font-size:clamp(22px, 3vw, 30px); line-height:1.2; letter-spacing:-.015em;
         font-weight:700; margin:.2rem 0 1.2rem; text-wrap:balance; }
    h3 { font-size:15.5px; margin:0 0 .45rem; }
    .hl { color:var(--accent); }
    .lede { color:var(--dim); font-size:16.5px; max-width:34rem; }
    .lede.center { margin:0 auto; text-align:center; max-width:38rem; }
    .center { justify-content:center; text-align:center; }
    .dim { color:var(--dim); }

    .eyebrow { color:var(--accent); font-size:12px; font-weight:650; letter-spacing:.16em;
               text-transform:uppercase; margin-bottom:.9rem; }

    .btn { display:inline-block; background:var(--accent); color:#141005; border-radius:7px;
           padding:.65rem 1.5rem; font:600 14px var(--sans); text-decoration:none; }
    .btn:hover { filter:brightness(1.1); }
    .btn-ghost { background:transparent; color:var(--fg); border:1px solid var(--line);
                 margin-left:0; }
    .btn-ghost:hover { border-color:var(--dim); filter:none; }
    .cta-row { display:flex; gap:.8rem; margin:1.6rem 0 0; flex-wrap:wrap; }

    /* hero */
    .hero { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1fr); gap:3rem;
            align-items:center; padding:5.5rem 0 5rem; position:relative; }
    .hero::before { content:""; position:absolute; inset:-60px -300px auto auto;
            width:640px; height:420px; pointer-events:none;
            background:radial-gradient(closest-side, rgba(217,165,63,.09), transparent); }
    .hero-note { color:var(--faint); font-size:12.5px; margin-top:1.1rem;
                 font-family:var(--mono); }

    /* terminal cards */
    .term { background:#0a0d12; border:1px solid var(--line); border-radius:10px;
            overflow:hidden; box-shadow:0 18px 50px -28px rgba(0,0,0,.8); min-width:0; }
    .term-bar { display:flex; align-items:center; gap:.4rem; padding:.55rem .9rem;
                border-bottom:1px solid var(--line-soft); background:var(--panel); }
    .term-bar span { width:9px; height:9px; border-radius:99px; background:var(--line); }
    .term-bar em { margin-left:.6rem; color:var(--faint); font:italic 11.5px var(--sans); }
    .term pre { margin:0; padding:1rem 1.1rem; overflow-x:auto;
                font:12.4px/1.65 var(--mono); }
    .term .c { color:var(--faint); } .term .v { color:var(--info); }
    .term .g { color:var(--ok); } .term .r { color:var(--bad); } .term .n { color:var(--accent); }

    /* the loop */
    .loop-steps { list-style:none; display:grid; grid-template-columns:repeat(auto-fit, minmax(230px,1fr));
                  gap:1rem; padding:0; margin:0; }
    .loop-steps li { background:var(--panel); border:1px solid var(--line); border-radius:9px;
                     padding:1.1rem 1.2rem; color:var(--dim); font-size:13.5px; }
    .step-k { display:block; font-family:var(--mono); color:var(--accent); font-size:12.5px;
              letter-spacing:.08em; text-transform:uppercase; margin-bottom:.5rem; }

    /* pillars */
    .grid-4 { display:grid; grid-template-columns:repeat(auto-fit, minmax(240px,1fr)); gap:1rem; }
    .pillar { background:var(--panel); border:1px solid var(--line); border-radius:9px;
              padding:1.3rem 1.3rem 1.15rem; }
    .pillar p { color:var(--dim); font-size:13.5px; margin:0; }

    /* split sections */
    .split { display:grid; grid-template-columns:minmax(0,1.05fr) minmax(0,1fr); gap:3rem;
             align-items:center; }
    .split-rev > .split-copy { order:0; }
    .checks { list-style:none; padding:0; margin:0; }
    .checks li { color:var(--dim); padding:.45rem 0 .45rem 1.7rem; position:relative;
                 font-size:14px; }
    .checks li::before { content:"▸"; position:absolute; left:.2rem; color:var(--accent); }
    .checks b { color:var(--fg); font-weight:600; }

    /* proof mini card */
    .proof-mini { background:var(--panel); border:1px solid var(--line); border-radius:10px;
                  padding:1.2rem 1.4rem; font-family:var(--mono); font-size:12.8px;
                  box-shadow:0 18px 50px -28px rgba(0,0,0,.8); }
    .pm-head { font-weight:700; font-size:14px; margin-bottom:.9rem; display:flex;
               align-items:center; gap:.7rem; font-family:var(--sans); }
    .pill-ok { color:var(--ok); border:1px solid var(--ok); border-radius:99px;
               padding:.05rem .6rem; font:500 11.5px var(--sans); }
    .pm-row { display:flex; justify-content:space-between; gap:1.5rem; padding:.42rem 0;
              border-top:1px solid var(--line-soft); }
    .pm-row span { color:var(--faint); } .pm-row b { font-weight:500; text-align:right; }
    .pm-hash b { color:var(--accent); overflow-wrap:anywhere; }

    /* agents + trust */
    .agents { text-align:center; }
    .agents h2 { margin-top:.4rem; }
    .agents code { font-family:var(--mono); background:var(--panel-2); padding:.1rem .4rem;
                   border-radius:4px; font-size:.92em; }
    .trust .grid-3 { display:grid; grid-template-columns:repeat(auto-fit, minmax(240px,1fr));
                     gap:1rem; color:var(--dim); font-size:13.5px; }
    .trust .grid-3 > div { border-left:2px solid var(--line); padding:.2rem 0 .2rem 1.1rem; }
    .tier { display:block; font-family:var(--mono); color:var(--fg); font-weight:600;
            margin-bottom:.35rem; }

    /* final */
    .final { text-align:center; border-bottom:0; padding:5.5rem 0 6rem; }
    .final h2 { font-size:clamp(26px, 4vw, 40px); }

    footer { border-top:1px solid var(--line-soft); padding:1.4rem 1.6rem; display:flex;
             justify-content:space-between; align-items:center; gap:1rem; flex-wrap:wrap;
             max-width:1160px; margin:0 auto; }
    footer .brand { font-size:14px; }
    footer nav { display:flex; gap:1.2rem; }
    footer nav a { color:var(--dim); text-decoration:none; font-size:13px; }
    footer nav a:hover { color:var(--fg); }
    footer .dim { font-size:13px; }

    @media (max-width: 880px) {
      .hero, .split { grid-template-columns:1fr; gap:2rem; }
      header nav { display:none; }
      section { padding:3.2rem 0; }
    }
    """
  end
end
