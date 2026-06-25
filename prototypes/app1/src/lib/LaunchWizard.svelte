<script>
  import { createEventDispatcher } from 'svelte'
  import Modal from './Modal.svelte'
  import ConnectorChip from './ConnectorChip.svelte'
  import HarnessBadge from './HarnessBadge.svelte'
  import { agents, harnesses, connectors, apps } from './data.js'
  import { formatCron, describeMode } from './format.js'

  export let open = false
  const dispatch = createEventDispatcher()

  let step = 0          // 0 agent, 1 params, 2 io, 3 review
  let agentId = null
  let harness = null
  let mode = null
  let params = {}
  let selectedConnectors = []
  let name = ''

  // navigation
  const steps = ['Agent', 'Configure', 'Connectors', 'Review']

  $: agent = agents.find(a => a.id === agentId)
  $: canNext = step === 0 ? !!agentId
              : step === 1 ? true
              : step === 2 ? selectedConnectors.length > 0
              : false

  function chooseAgent(a) {
    agentId = a.id
    harness = a.defaultHarness
    mode = a.defaultMode
    params = {}
    for (const p of a.params) params[p.key] = p.default
    selectedConnectors = a.connectors.map(id => ({ id, config: {} }))
    name = a.name
    step = 1
  }

  function toggleConnector(c) {
    const i = selectedConnectors.findIndex(x => x.id === c.id)
    if (i >= 0) selectedConnectors.splice(i, 1)
    else selectedConnectors.push({ id: c.id, config: {} })
    selectedConnectors = [...selectedConnectors]
  }

  function setConn(oi, key, val) {
    if (oi < 0) return
    selectedConnectors[oi].config[key] = val
    selectedConnectors = [...selectedConnectors]
  }

  function next() {
    if (step < 3) step++
    else launch()
  }
  function back() { if (step > 0) step-- }

  function launch() {
    const id = 'app_' + Math.random().toString(36).slice(2, 7)
    apps.update(list => [{
      id,
      agentId,
      name: name || agent.name,
      harness,
      mode,
      status: 'running',
      connectors: selectedConnectors,
      params: { ...params },
      createdAt: Date.now(),
      stats: { done: 0, active: 0, queued: 0 },
    }, ...list])
    reset()
    dispatch('launched')
    dispatch('close')
  }

  function reset() {
    step = 0; agentId = null; harness = null; mode = null
    params = {}; selectedConnectors = []; name = ''
  }

  $: if (open && agentId === null) step = 0

  function paramInput(p, e) {
    const v = e.target.type === 'checkbox' ? e.target.checked : e.target.value
    params = { ...params, [p.key]: p.type === 'number' ? +v : v }
  }
  function setTag(p, v) {
    const arr = new Set(params[p.key] || [])
    if (arr.has(v)) arr.delete(v); else arr.add(v)
    params = { ...params, [p.key]: [...arr] }
  }

  const SAMPLE_TAGS = ['security','perf','deps','style','docs','tests','api','ui','bug','feature','question','flaky']
</script>

<Modal {open} title={step === 0 ? 'Launch an agent' : `Launch · ${agent?.name || ''}`}
       width="720px" on:close={() => { reset(); dispatch('close') }}>

  <!-- Stepper -->
  {#if step > 0}
    <div class="steps">
      {#each steps as s, i}
        <div class="step" class:done={i<step} class:active={i===step}>
          <span class="num">{i<step ? '✓' : i+1}</span>
          <span class="lbl">{s}</span>
        </div>
        {#if i < steps.length - 1}<span class="sep"></span>{/if}
      {/each}
    </div>
  {/if}

  <!-- Step 0: choose agent -->
  {#if step === 0}
    <div class="agent-grid">
      {#each agents as a}
        <button class="agent-card" on:click={() => chooseAgent(a)}>
          <div class="ic" style="--c:{a.color}">{a.icon}</div>
          <div class="meta">
            <div class="nm">{a.name}</div>
            <div class="tg">{a.tagline}</div>
            <div class="row" style="gap:6px;margin-top:8px">
              <span class="pill">{describeMode(a.defaultMode)}</span>
              {#each a.connectors as cid}
                <ConnectorChip id={cid} size={18} />
              {/each}
            </div>
          </div>
        </button>
      {/each}
    </div>

  {:else if step === 1}
    <div class="cfg">
      <div class="field">
        <label>Application name</label>
        <input bind:value={name} placeholder="e.g. Code Review · my-org/repo" />
      </div>

      <div class="dual">
        <div class="field">
          <label>Agent harness</label>
          <div class="harness-grid">
            {#each harnesses as h}
              <button class="hc" class:on={harness===h.id} on:click={() => harness = h.id}>
                <span class="hi" style="--c:{h.color}">{h.icon}</span>
                <span class="hn">{h.name}</span>
                <span class="hv">{h.vendor}</span>
              </button>
            {/each}
          </div>
        </div>
        <div class="field">
          <label>Run mode</label>
          <div class="modes">
            {#each agent.modes as m}
              <button class="mc" class:on={mode===m} on:click={() => mode = m}>
                <span class="mt">{describeMode(m)}</span>
                <span class="md">
                  {m==='loop' ? 'Always-on' : m==='periodic' ? 'On a schedule' : 'Single run'}
                </span>
              </button>
            {/each}
          </div>
        </div>
      </div>

      {#if agent.params.length}
        <div class="params">
          <div class="sub">Parameters</div>
          {#each agent.params as p}
            <div class="param">
              <label>{p.label}</label>
              {#if p.type === 'select'}
                <select value={params[p.key]} on:change={e => paramInput(p,e)}>
                  {#each p.options as o}<option value={o}>{o}</option>{/each}
                </select>
              {:else if p.type === 'number'}
                <div class="row">
                  <input type="range" min={p.min} max={p.max} value={params[p.key]} on:input={e => paramInput(p,e)} class="grow" />
                  <span class="badge mono">{params[p.key]}</span>
                </div>
              {:else if p.type === 'slider'}
                <div class="row">
                  <input type="range" min={p.min} max={p.max} value={params[p.key]} on:input={e => paramInput(p,e)} class="grow" />
                  <span class="badge mono">{params[p.key]}%</span>
                </div>
              {:else if p.type === 'toggle'}
                <button class="toggle" class:on={params[p.key]} on:click={() => params = {...params,[p.key]:!params[p.key]}}>
                  <span class="knob"></span>
                </button>
              {:else if p.type === 'tags'}
                <div class="tags">
                  {#each SAMPLE_TAGS as t}
                    <button class="tag" class:on={(params[p.key]||[]).includes(t)} on:click={() => setTag(p,t)}>{t}</button>
                  {/each}
                </div>
              {:else if p.type === 'cron'}
                <input value={params[p.key]} on:input={e => paramInput(p,e)} class="mono" />
                <div class="hint">{formatCron(params[p.key])}</div>
              {:else}
                <input value={params[p.key]} on:input={e => paramInput(p,e)} />
              {/if}
            </div>
          {/each}
        </div>
      {/if}
    </div>

  {:else if step === 2}
    <div class="io">
      <p class="lead">Connectors bring input to the agent and carry its output back out. Pick the sources and destinations.</p>
      <div class="conn-grid">
        {#each connectors as c, ci}
          {@const on = selectedConnectors.find(x => x.id === c.id)}
          {@const oi = selectedConnectors.findIndex(x => x.id === c.id)}
          <button class="conn" class:on={!!on} on:click={() => toggleConnector(c)}>
            <div class="row between">
              <ConnectorChip id={c.id} size={28} />
              <div class="dirs">
                {#if c.kinds.includes('input')}<span class="dir">IN</span>{/if}
                {#if c.kinds.includes('output')}<span class="dir">OUT</span>{/if}
              </div>
            </div>
            <div class="cnm">{c.name}</div>
            <div class="cds">{c.desc}</div>
            {#if on}
              <div class="cflds" on:click|stopPropagation>
                {#if c.id === 'github'}
                  <input placeholder="org/repo" value={selectedConnectors[oi]?.config.repo || ''} on:input={e => setConn(oi, 'repo', e.target.value)} />
                {:else if c.id === 'slack' || c.id === 'discord'}
                  <input placeholder="#channel" value={selectedConnectors[oi]?.config.channel || ''} on:input={e => setConn(oi, 'channel', e.target.value)} />
                {:else if c.id === 'email'}
                  <input placeholder="folder / label" value={selectedConnectors[oi]?.config.folder || ''} on:input={e => setConn(oi, 'folder', e.target.value)} />
                {:else if c.id === 'linear' || c.id === 'jira'}
                  <input placeholder="project key" value={selectedConnectors[oi]?.config.team || ''} on:input={e => setConn(oi, 'team', e.target.value)} />
                {:else if c.id === 'gdrive'}
                  <input placeholder="folder id" value={selectedConnectors[oi]?.config.folder || ''} on:input={e => setConn(oi, 'folder', e.target.value)} />
                {:else if c.id === 'webhook'}
                  <input placeholder="POST path" value={selectedConnectors[oi]?.config.path || ''} on:input={e => setConn(oi, 'path', e.target.value)} />
                {/if}
              </div>
            {/if}
          </button>
        {/each}
      </div>
    </div>

  {:else}
    <div class="review">
      <div class="rv-card">
        <div class="ic" style="--c:{agent.color}">{agent.icon}</div>
        <div>
          <div class="rv-name">{name}</div>
          <div class="rv-sub">{agent.name} · {describeMode(mode)}</div>
        </div>
      </div>
      <div class="rv-rows">
        <div class="rv-row"><span>Harness</span><b><HarnessBadge id={harness} /></b></div>
        <div class="rv-row"><span>Mode</span><b>{describeMode(mode)}</b></div>
        {#if mode === 'periodic' && params.schedule}
          <div class="rv-row"><span>Schedule</span><b class="mono">{params.schedule} · {formatCron(params.schedule)}</b></div>
        {/if}
        <div class="rv-row"><span>Connectors</span>
          <b class="row" style="gap:6px">
            {#each selectedConnectors as sc}<ConnectorChip id={sc.id} size={20} label={sc.id} />{/each}
          </b>
        </div>
        <div class="rv-row"><span>Parameters</span>
          <b class="row" style="gap:6px;flex-wrap:wrap">
            {#each Object.entries(params) as [k,v]}
              <span class="kv">{k}=<span>{Array.isArray(v)?v.join(','):v}</span></span>
            {/each}
          </b>
        </div>
      </div>
    </div>
  {/if}

  <svelte:fragment slot="footer">
    {#if step > 0}
      <button class="btn ghost" on:click={back}>Back</button>
    {/if}
    <button class="btn ghost" on:click={() => { reset(); dispatch('close') }}>Cancel</button>
    <button class="btn primary" disabled={!canNext} on:click={next}>
      {step < 3 ? 'Continue' : 'Launch agent'}
    </button>
  </svelte:fragment>
</Modal>

<style>
  /* Steps */
  .steps { display:flex; align-items:center; gap:8px; margin-bottom:24px; }
  .step { display:flex; align-items:center; gap:8px; color: var(--text-muted); font-size:12px; }
  .step.active { color: var(--text); }
  .step.done { color: var(--accent); }
  .num { width:20px;height:20px;border-radius:50%; display:inline-flex; align-items:center;justify-content:center;
         border:1px solid var(--border-strong); font-size:11px; }
  .step.active .num { border-color: var(--accent); color: var(--accent); background: var(--accent-soft); }
  .step.done .num { border-color: var(--accent); background: var(--accent); color:#fff; }
  .sep { flex:1; height:1px; background: var(--border-soft); }

  /* Agent grid */
  .agent-grid { display:grid; grid-template-columns: 1fr 1fr; gap:12px; }
  .agent-card { text-align:left; display:flex; gap:14px; padding:16px; border-radius: var(--radius);
    background: var(--bg-elev-2); border:1px solid var(--border); transition: all .15s; }
  .agent-card:hover { border-color: var(--accent); transform: translateY(-1px); }
  .ic { width:40px;height:40px;border-radius:10px; display:flex;align-items:center;justify-content:center;
        font-size:20px; background: color-mix(in srgb, var(--c) 18%, transparent); flex:none; }
  .nm { font-weight:600; font-size:14px; }
  .tg { color: var(--text-dim); font-size:12px; margin-top:2px; }
  .pill { font-size:11px; padding:2px 8px; border-radius:999px; background: rgba(255,255,255,0.05);
          color: var(--text-dim); border:1px solid var(--border); }

  /* Config */
  .field { margin-bottom: 18px; }
  .field > label { display:block; font-size:12px; font-weight:500; color: var(--text-dim); margin-bottom:8px; }
  .dual { display:grid; grid-template-columns: 1.2fr 1fr; gap:20px; }
  input, select {
    width:100%; background: var(--bg); border:1px solid var(--border); color: var(--text);
    padding:9px 12px; border-radius: var(--radius-sm); outline:none; transition: border .15s;
  }
  input:focus, select:focus { border-color: var(--accent); }
  input[type="range"] { padding:0; height:4px; appearance:none; background: var(--border-strong); border:none; }
  input[type="range"]::-webkit-slider-thumb {
    appearance:none; width:14px;height:14px;border-radius:50%; background: var(--accent);
    border:2px solid var(--bg); cursor:pointer;
  }

  .harness-grid { display:grid; grid-template-columns: repeat(auto-fill, minmax(110px,1fr)); gap:8px; }
  .hc { text-align:left; padding:10px; border-radius: var(--radius-sm); background: var(--bg);
        border:1px solid var(--border); transition: all .15s; }
  .hc:hover { border-color: var(--border-strong); }
  .hc.on { border-color: var(--accent); background: var(--accent-soft); }
  .hi { display:inline-flex; width:22px;height:22px;border-radius:6px; align-items:center;justify-content:center;
        background: color-mix(in srgb, var(--c) 20%, transparent); color: var(--c); font-size:13px; }
  .hn { display:block; font-size:12px; font-weight:500; margin-top:6px; }
  .hv { display:block; font-size:10px; color: var(--text-muted); }

  .modes { display:flex; flex-direction:column; gap:8px; }
  .mc { text-align:left; padding:10px 12px; border-radius: var(--radius-sm); background: var(--bg);
        border:1px solid var(--border); transition: all .15s; }
  .mc:hover { border-color: var(--border-strong); }
  .mc.on { border-color: var(--accent); background: var(--accent-soft); }
  .mt { display:block; font-size:13px; font-weight:500; }
  .md { display:block; font-size:11px; color: var(--text-muted); margin-top:1px; }

  .params { margin-top: 24px; border-top: 1px solid var(--border-soft); padding-top:20px; }
  .sub { font-size:12px; font-weight:600; color: var(--text-dim); text-transform: uppercase; letter-spacing:.06em; margin-bottom:14px; }
  .param { margin-bottom:14px; }
  .param label { display:block; font-size:12px; color: var(--text-dim); margin-bottom:6px; }
  .badge { padding:2px 8px; border-radius: var(--radius-sm); background: var(--bg); border:1px solid var(--border); font-size:11px; min-width:36px; text-align:center; }
  .toggle { width:38px; height:22px; border-radius:999px; background: var(--border-strong); position:relative; transition: background .2s; }
  .toggle.on { background: var(--accent); }
  .knob { position:absolute; top:2px; left:2px; width:18px;height:18px; border-radius:50%; background:#fff;
          transition: transform .2s; }
  .toggle.on .knob { transform: translateX(16px); }
  .tags { display:flex; flex-wrap:wrap; gap:6px; }
  .tag { padding:4px 10px; border-radius:999px; background: var(--bg); border:1px solid var(--border);
         font-size:11px; color: var(--text-dim); transition: all .15s; }
  .tag.on { background: var(--accent-soft); border-color: var(--accent); color: var(--accent-hover); }
  .hint { font-size:11px; color: var(--text-muted); margin-top:4px; }

  /* Connectors */
  .lead { color: var(--text-dim); font-size:13px; margin-bottom:16px; }
  .conn-grid { display:grid; grid-template-columns: 1fr 1fr; gap:12px; }
  .conn { text-align:left; padding:14px; border-radius: var(--radius); background: var(--bg-elev-2);
          border:1px solid var(--border); transition: all .15s; }
  .conn:hover { border-color: var(--border-strong); }
  .conn.on { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent-glow); }
  .dirs { display:flex; gap:4px; }
  .dir { font-size:9px; font-weight:700; padding:2px 5px; border-radius:3px;
         background: rgba(255,255,255,0.05); color: var(--text-muted); letter-spacing:.05em; }
  .cnm { font-weight:600; margin-top:10px; }
  .cds { font-size:12px; color: var(--text-muted); margin-top:3px; line-height:1.45; }
  .cflds { margin-top:10px; }

  /* Review */
  .rv-card { display:flex; align-items:center; gap:14px; padding:16px; background: var(--bg-elev-2);
             border:1px solid var(--border); border-radius: var(--radius); margin-bottom:20px; }
  .rv-name { font-weight:600; font-size:15px; }
  .rv-sub { color: var(--text-dim); font-size:12px; margin-top:2px; }
  .rv-rows { display:flex; flex-direction:column; gap:14px; }
  .rv-row { display:flex; justify-content:space-between; align-items:center; font-size:13px; gap:16px; }
  .rv-row > span { color: var(--text-muted); }
  .rv-row > b { font-weight:500; text-align:right; display:flex; flex-wrap:wrap; gap:6px; justify-content:flex-end; }
  .kv { font-size:11px; padding:2px 8px; border-radius: var(--radius-sm); background: var(--bg);
        border:1px solid var(--border); color: var(--text-dim); font-family: var(--font-mono); }
  .kv span { color: var(--accent-hover); }

  /* Buttons */
  .btn { padding:8px 16px; border-radius: var(--radius-sm); font-size:13px; font-weight:500;
         border:1px solid transparent; transition: all .15s; }
  .btn.ghost { color: var(--text-dim); border-color: var(--border); }
  .btn.ghost:hover { background: var(--bg-hover); color: var(--text); }
  .btn.primary { background: var(--accent); color:#fff; }
  .btn.primary:hover:not(:disabled) { background: var(--accent-hover); }
  .btn.primary:disabled { opacity:.4; cursor:not-allowed; }
</style>
