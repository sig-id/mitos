<script>
  import { apps, targets, getAgent, agents, harnesses } from '../lib/data.js'
  import { go } from '../lib/router.js'
  import StatusPill from '../lib/StatusPill.svelte'
  import HarnessBadge from '../lib/HarnessBadge.svelte'
  import ConnectorChip from '../lib/ConnectorChip.svelte'
  import { formatRelative } from '../lib/format.js'
  import { get } from 'svelte/store'

  export let appId
  $: app = get(apps).find(a => a.id === appId)
  $: agent = app && getAgent(app.agentId)
  $: appTargets = app ? get(targets).filter(t => t.appId === app.id) : []

  function stop() { apps.update(l => l.map(a => a.id===app.id ? {...a, status:'idle'} : a)) }
  function start() { apps.update(l => l.map(a => a.id===app.id ? {...a, status:'running'} : a)) }
</script>

<div class="page">
  {#if !app}
    <div class="missing">Application not found. <a href="#/dashboard">Back</a></div>
  {:else}
    <div class="head">
      <button class="back" on:click={() => go('dashboard')}>← Dashboard</button>
      <div class="row" style="gap:14px;flex:1">
        <div class="ic" style="--c:{agent.color}">{agent.icon}</div>
        <div>
          <h1>{app.name}</h1>
          <div class="sub">{agent.name} · {agent.category}</div>
        </div>
      </div>
      <StatusPill status={app.status} />
      {#if app.status === 'running'}
        <button class="btn ghost" on:click={stop}>Pause</button>
      {:else}
        <button class="btn primary" on:click={start}>Resume</button>
      {/if}
    </div>

    <div class="grid">
      <div class="card">
        <div class="ch">Overview</div>
        <div class="cb">
          <div class="r"><span>Agent</span><b>{agent.name}</b></div>
          <div class="r"><span>Harness</span><b><HarnessBadge id={app.harness} /></b></div>
          <div class="r"><span>Mode</span><b>{app.mode}</b></div>
          <div class="r"><span>Started</span><b>{formatRelative(app.createdAt)}</b></div>
          {#if app.nextRun}<div class="r"><span>Next run</span><b>{formatRelative(app.nextRun)}</b></div>{/if}
        </div>
      </div>

      <div class="card">
        <div class="ch">Connectors</div>
        <div class="cb">
          {#each app.connectors as c}
            <div class="conn">
              <ConnectorChip id={c.id} size={24} />
              <div class="ci">
                <div class="cn">{c.id}</div>
                <div class="cf mono">{Object.entries(c.config).map(([k,v])=>`${k}: ${v}`).join(' · ')}</div>
              </div>
            </div>
          {/each}
        </div>
      </div>

      <div class="card span2">
        <div class="ch">Parameters</div>
        <div class="cb params">
          {#each Object.entries(app.params) as [k,v]}
            <div class="p"><span class="pk mono">{k}</span>
              {#if Array.isArray(v)}
                <span class="pv">{v.join(', ')}</span>
              {:else if v === true}
                <span class="pv yes">on</span>
              {:else if v === false}
                <span class="pv no">off</span>
              {:else}
                <span class="pv">{v}</span>
              {/if}
            </div>
          {/each}
        </div>
      </div>
    </div>

    <div class="card">
      <div class="ch">Targets ({appTargets.length})</div>
      <div class="tgt-list">
        {#each appTargets as t}
          <div class="tgt">
            <span class="ti">{t.kind}</span>
            <span class="tt">{t.title}</span>
            <span class="ts pill">{t.stage}</span>
            <span class="ta muted">{t.age}</span>
          </div>
        {:else}
          <div class="muted">No targets.</div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .page { padding: 28px 40px; max-width: 1100px; margin: 0 auto; }
  .head { display:flex; align-items:center; gap:12px; margin-bottom:28px; flex-wrap:wrap; }
  .back { color: var(--text-muted); font-size:13px; margin-right:4px; }
  .back:hover { color: var(--text); }
  h1 { font-size: 22px; font-weight: 600; letter-spacing:-0.01em; }
  .sub { color: var(--text-dim); font-size:13px; margin-top:2px; }
  .ic { width:44px;height:44px;border-radius:12px; display:flex;align-items:center;justify-content:center;
        font-size:22px; background: color-mix(in srgb, var(--c) 18%, transparent); }
  .btn { padding:8px 16px; border-radius: var(--radius-sm); font-size:13px; font-weight:500; }
  .btn.ghost { color: var(--text-dim); border:1px solid var(--border); }
  .btn.ghost:hover { background: var(--bg-hover); color: var(--text); }
  .btn.primary { background: var(--accent); color:#fff; }
  .btn.primary:hover { background: var(--accent-hover); }

  .grid { display:grid; grid-template-columns: 1fr 1fr; gap:16px; margin-bottom:16px; }
  .span2 { grid-column: span 2; }
  .card { background: var(--bg-elev); border:1px solid var(--border); border-radius: var(--radius-lg);
          overflow:hidden; margin-bottom:16px; }
  .ch { padding:14px 18px; border-bottom:1px solid var(--border-soft); font-size:12px; font-weight:600;
        color: var(--text-dim); text-transform:uppercase; letter-spacing:.06em; }
  .cb { padding:16px 18px; }
  .r { display:flex; justify-content:space-between; padding:6px 0; font-size:13px; border-bottom:1px solid var(--border-soft); }
  .r:last-child { border-bottom:none; }
  .r span { color: var(--text-muted); }
  .r b { font-weight:500; }

  .conn { display:flex; align-items:center; gap:10px; padding:8px 0; }
  .conn + .conn { border-top:1px solid var(--border-soft); }
  .cn { font-weight:500; font-size:13px; text-transform:capitalize; }
  .cf { font-size:11px; color: var(--text-muted); margin-top:2px; }

  .params { display:grid; grid-template-columns: 1fr 1fr; gap:10px 20px; }
  .p { display:flex; align-items:center; justify-content:space-between; padding:6px 0; border-bottom:1px solid var(--border-soft); }
  .pk { font-size:12px; color: var(--text-muted); }
  .pv { font-size:13px; }
  .pv.yes { color: var(--green); }
  .pv.no { color: var(--text-faint); }

  .tgt-list { padding:4px 18px 12px; }
  .tgt { display:flex; align-items:center; gap:12px; padding:10px 0; border-bottom:1px solid var(--border-soft); font-size:13px; }
  .ti { font-size:10px; padding:2px 7px; border-radius: var(--radius-sm); background: var(--bg-hover); color: var(--text-dim); flex:none; }
  .tt { flex:1; }
  .pill { font-size:11px; padding:2px 8px; border-radius:999px; background: rgba(255,255,255,0.04); border:1px solid var(--border); }
  .ta { font-size:12px; }

  .missing { padding:60px; text-align:center; color: var(--text-muted); }
  a { color: var(--accent); }
</style>
