<script>
  import { apps, getAgent } from '../lib/data.js'
  import StatusPill from '../lib/StatusPill.svelte'
  import HarnessBadge from '../lib/HarnessBadge.svelte'
  import { describeMode, formatRelative } from '../lib/format.js'
  import { go } from '../lib/router.js'
  import { get } from 'svelte/store'

  $: all = get(apps)
</script>

<div class="page">
  <div class="head">
    <h1>Runs</h1>
    <p>All applications launched into the service layer — past and present.</p>
  </div>
  <div class="table">
    <div class="thr">
      <span>Application</span>
      <span>Harness</span>
      <span>Mode</span>
      <span>Status</span>
      <span>Started</span>
      <span>Activity</span>
    </div>
    {#each all as a}
      {@const ag = getAgent(a.agentId)}
      <button class="trow" on:click={() => go('apps/'+a.id)}>
        <span class="nm">
          <span class="ic" style="--c:{ag.color}">{ag.icon}</span>
          {a.name}
        </span>
        <span><HarnessBadge id={a.harness} /></span>
        <span class="muted">{describeMode(a.mode)}</span>
        <span><StatusPill status={a.status} /></span>
        <span class="muted">{formatRelative(a.createdAt)}</span>
        <span class="muted">{a.stats.done} done · {a.stats.active} active</span>
      </button>
    {/each}
  </div>
</div>

<style>
  .page { padding: 28px 40px; max-width: 1200px; margin: 0 auto; }
  .head { margin-bottom: 22px; }
  h1 { font-size: 22px; font-weight: 600; letter-spacing: -0.01em; }
  p { color: var(--text-muted); font-size: 13px; margin-top: 3px; }

  .table { border:1px solid var(--border); border-radius: var(--radius-lg); overflow:hidden; }
  .thr, .trow { display:grid; grid-template-columns: 2fr 1.2fr 1fr 1fr 1fr 1.4fr; gap:10px; align-items:center; padding:12px 18px; }
  .thr { font-size:11px; text-transform:uppercase; letter-spacing:.06em; color: var(--text-faint);
         background: var(--bg-elev); border-bottom:1px solid var(--border); }
  .trow { width:100%; text-align:left; border-bottom:1px solid var(--border-soft); font-size:13px;
          transition: background .12s; }
  .trow:last-child { border-bottom:none; }
  .trow:hover { background: var(--bg-elev); }
  .nm { display:flex; align-items:center; gap:10px; font-weight:500; }
  .ic { width:24px;height:24px;border-radius:6px; display:flex;align-items:center;justify-content:center;
        font-size:13px; background: color-mix(in srgb, var(--c) 18%, transparent); flex:none; }
  .muted { color: var(--text-muted); font-size:12px; }
</style>
