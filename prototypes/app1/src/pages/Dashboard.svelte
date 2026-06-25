<script>
  import { apps, targets, getAgent } from '../lib/data.js'
  import { go } from '../lib/router.js'
  import StatusPill from '../lib/StatusPill.svelte'
  import HarnessBadge from '../lib/HarnessBadge.svelte'
  import ConnectorChip from '../lib/ConnectorChip.svelte'
  import { createEventDispatcher } from 'svelte'
  import { formatRelative } from '../lib/format.js'
  import { get } from 'svelte/store'

  const dispatch = createEventDispatcher()

  // Filtering
  let appFilter = 'all'   // all | appId
  $: visibleApps = get(apps)
  $: filteredTargets = get(targets).filter(t =>
      appFilter === 'all' ? true : t.appId === appFilter)

  // Always-on / loop / periodic live in the top strip
  $: loopApps = visibleApps.filter(a => a.mode === 'loop' || a.mode === 'periodic')

  // One-off targets + running shards of oneshot apps go to kanban
  // Actually: every TARGET is a kanban card. Apps in the strip are the "always-on" workers.
  $: kanbanTargets = filteredTargets

  export let onTarget = (t) => dispatch('target', t)

  import { stages } from '../lib/data.js'

  $: appOf = (id) => get(apps).find(a => a.id === id)
  $: agentOf = (app) => app ? getAgent(app.agentId) : null
</script>

<div class="dash">
  <!-- Top: always-on strip -->
  <section class="loop">
    <div class="loop-h">
      <div>
        <h2>Always-on</h2>
        <p>Continuous and scheduled agents currently managed by the service layer.</p>
      </div>
      <button class="new" on:click={() => dispatch('launch')}>
        <span class="plus">＋</span> Launch agent
      </button>
    </div>

    <div class="loop-strip">
      {#each loopApps as a (a.id)}
        {@const ag = agentOf(a)}
        <button class="loop-card" on:click={() => go('apps/' + a.id)}>
          <div class="top">
            <div class="ic" style="--c:{ag.color}">{ag.icon}</div>
            <StatusPill status={a.status} />
          </div>
          <div class="nm">{a.name}</div>
          <div class="ag">{ag.name}</div>
          <div class="stats">
            {#if a.mode === 'periodic' && a.nextRun}
              <span class="stat"><b>Next</b> {formatRelative(a.nextRun)}</span>
            {:else}
              <span class="stat"><b>{a.stats.active}</b> active</span>
            {/if}
            <span class="stat"><b>{a.stats.done}</b> done</span>
          </div>
          <div class="conns">
            {#each a.connectors.slice(0,4) as c}
              <ConnectorChip id={c.id} size={18} />
            {/each}
          </div>
        </button>
      {:else}
        <div class="loop-empty">
          No always-on agents yet.
        </div>
      {/each}
    </div>
  </section>

  <!-- Filter bar -->
  <div class="filters">
    <div class="row" style="gap:8px;flex-wrap:wrap">
      <button class="f" class:on={appFilter==='all'} on:click={() => appFilter='all'}>All targets</button>
      {#each visibleApps as a}
        {@const ag = agentOf(a)}
        <button class="f" class:on={appFilter===a.id} on:click={() => appFilter=a.id}>
          <span class="fd" style="--c:{ag.color}">{ag.icon}</span>
          {a.name}
        </button>
      {/each}
    </div>
  </div>

  <!-- Kanban -->
  <section class="kanban">
    {#each stages as col}
      {@const items = kanbanTargets.filter(t => t.stage === col.id)}
      <div class="col">
        <div class="col-h tone-{col.tone}">
          <span class="dot"></span>
          <span class="cn">{col.name}</span>
          <span class="ct">{items.length}</span>
        </div>
        <div class="col-body">
          {#each items as t (t.id)}
            {@const a = appOf(t.appId)}
            {@const ag = agentOf(a)}
            <button class="card" class:attn={t.attention} on:click={() => onTarget(t)}>
              <div class="row between">
                <span class="kind">{t.kind}</span>
                {#if t.stage === 'running'}
                  <span class="live"><span class="pd"></span>working</span>
                {/if}
              </div>
              <div class="ttl">{t.title}</div>
              {#if t.note}
                <div class="nt">{t.note}</div>
              {/if}
              <div class="foot">
                <span class="src"><ConnectorChip id={t.source} size={15} /> {t.refs}</span>
                <span class="ago">{t.age}</span>
              </div>
            </button>
          {:else}
            <div class="empty">Nothing here.</div>
          {/each}
        </div>
      </div>
    {/each}
  </section>
</div>

<style>
  .dash { padding: 24px 32px 40px; max-width: 1500px; margin: 0 auto; }

  /* Always-on strip */
  .loop { margin-bottom: 28px; }
  .loop-h { display:flex; align-items:flex-end; justify-content:space-between; margin-bottom:14px; }
  .loop-h h2 { font-size:15px; font-weight:600; letter-spacing:-0.01em; }
  .loop-h p { font-size:12px; color: var(--text-muted); margin-top:2px; }
  .new { display:inline-flex; align-items:center; gap:6px; padding:8px 14px; border-radius: var(--radius-sm);
         background: var(--accent); color:#fff; font-size:13px; font-weight:500; transition: background .15s; }
  .new:hover { background: var(--accent-hover); }
  .plus { font-size:14px; line-height:1; }
  .loop-strip { display:grid; grid-template-columns: repeat(auto-fill, minmax(240px,1fr)); gap:12px; }
  .loop-card { text-align:left; padding:14px; border-radius: var(--radius-lg); background: var(--bg-elev);
               border:1px solid var(--border); transition: all .15s; display:flex; flex-direction:column; gap:8px; }
  .loop-card:hover { border-color: var(--border-strong); transform: translateY(-1px); box-shadow: var(--shadow); }
  .top { display:flex; align-items:center; justify-content:space-between; }
  .ic { width:36px;height:36px;border-radius:10px; display:flex;align-items:center;justify-content:center;
        font-size:18px; background: color-mix(in srgb, var(--c) 18%, transparent); }
  .nm { font-weight:600; font-size:13px; }
  .ag { font-size:11px; color: var(--text-muted); margin-top:-3px; }
  .stats { display:flex; gap:14px; font-size:11px; color: var(--text-muted); }
  .stat b { color: var(--text); font-weight:600; margin-right:3px; }
  .conns { display:flex; gap:5px; }
  .loop-empty { padding:28px; text-align:center; color: var(--text-muted); border:1px dashed var(--border); border-radius: var(--radius-lg); }

  /* Filters */
  .filters { margin-bottom:18px; }
  .f { display:inline-flex; align-items:center; gap:6px; padding:6px 12px; border-radius:999px;
       background: var(--bg-elev); border:1px solid var(--border); font-size:12px; color: var(--text-dim);
       transition: all .15s; }
  .f:hover { border-color: var(--border-strong); color: var(--text); }
  .f.on { background: var(--accent-soft); border-color: var(--accent); color: var(--accent-hover); }
  .fd { font-size:12px; }

  /* Kanban */
  .kanban { display:grid; grid-template-columns: repeat(4, 1fr); gap:14px; min-height: 50vh; }
  .col { background: rgba(255,255,255,0.015); border:1px solid var(--border-soft); border-radius: var(--radius-lg);
         display:flex; flex-direction:column; min-height: 280px; }
  .col-h { display:flex; align-items:center; gap:8px; padding:12px 14px; font-size:12px; font-weight:600; }
  .col-h .dot { width:7px; height:7px; border-radius:50%; }
  .tone-muted .dot { background: var(--text-muted); }
  .tone-live  .dot { background: var(--green); }
  .tone-warn  .dot { background: var(--yellow); }
  .tone-good  .dot { background: var(--blue); }
  .cn { flex:1; }
  .ct { font-size:11px; color: var(--text-muted); background: rgba(255,255,255,0.04);
        padding:1px 7px; border-radius:999px; }
  .col-body { padding:6px 10px 12px; display:flex; flex-direction:column; gap:8px; flex:1; }

  .card { text-align:left; padding:12px; border-radius: var(--radius); background: var(--bg-elev);
          border:1px solid var(--border); transition: all .15s; display:flex; flex-direction:column; gap:8px; }
  .card:hover { border-color: var(--accent); box-shadow: var(--shadow-sm); }
  .card.attn { border-color: rgba(251,191,36,.35); background: linear-gradient(180deg, rgba(251,191,36,0.04), var(--bg-elev)); }
  .kind { font-size:10px; font-weight:600; text-transform:uppercase; letter-spacing:.05em; color: var(--text-muted); }
  .live { display:inline-flex; align-items:center; gap:5px; font-size:11px; color: var(--green); }
  .pd { width:6px;height:6px;border-radius:50%; background: var(--green); animation: bp 1.4s infinite; }
  @keyframes bp { 0%,100%{opacity:1;} 50%{opacity:.3;} }
  .ttl { font-size:13px; line-height:1.4; font-weight:500; }
  .nt { font-size:11px; color: var(--yellow); padding:5px 8px; background: var(--yellow-soft);
        border-radius: var(--radius-sm); }
  .foot { display:flex; align-items:center; justify-content:space-between; gap:8px; font-size:11px; color: var(--text-muted); }
  .src { display:inline-flex; align-items:center; gap:5px; }
  .ago { white-space:nowrap; }
  .empty { padding:18px; text-align:center; font-size:12px; color: var(--text-faint); }
</style>
