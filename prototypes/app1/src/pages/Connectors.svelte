<script>
  import { connectors } from '../lib/data.js'
  import ConnectorChip from '../lib/ConnectorChip.svelte'
</script>

<div class="page">
  <div class="head">
    <h1>Connectors</h1>
    <p>The IO layer. Sources feed targets in; sinks carry agent output back out.</p>
  </div>
  <div class="grid">
    {#each connectors as c}
      <div class="card">
        <div class="row between">
          <ConnectorChip id={c.id} size={32} />
          <div class="dirs">
            {#if c.kinds.includes('input')}<span class="dir in">IN</span>{/if}
            {#if c.kinds.includes('output')}<span class="dir out">OUT</span>{/if}
          </div>
        </div>
        <div class="nm">{c.name}</div>
        <p class="ds">{c.desc}</p>
        <div class="status">
          <span class="ok"></span> Connected
        </div>
      </div>
    {/each}
  </div>
</div>

<style>
  .page { padding: 28px 40px; max-width: 1200px; margin: 0 auto; }
  .head { margin-bottom: 22px; }
  h1 { font-size: 22px; font-weight: 600; letter-spacing: -0.01em; }
  p { color: var(--text-muted); font-size: 13px; margin-top: 3px; }
  .grid { display:grid; grid-template-columns: repeat(auto-fill, minmax(260px,1fr)); gap:14px; }
  .card { padding:18px; border-radius: var(--radius-lg); background: var(--bg-elev); border:1px solid var(--border);
          display:flex; flex-direction:column; gap:8px; }
  .dirs { display:flex; gap:4px; }
  .dir { font-size:9px; font-weight:700; padding:3px 6px; border-radius:4px; letter-spacing:.05em; }
  .dir.in { background: var(--blue-soft); color: var(--blue); }
  .dir.out { background: var(--green-soft); color: var(--green); }
  .nm { font-weight:600; font-size:14px; margin-top:6px; }
  .ds { font-size:12px; color: var(--text-dim); line-height:1.5; }
  .status { display:flex; align-items:center; gap:6px; font-size:11px; color: var(--text-muted); margin-top:6px; }
  .ok { width:6px;height:6px;border-radius:50%; background: var(--green); }
</style>
