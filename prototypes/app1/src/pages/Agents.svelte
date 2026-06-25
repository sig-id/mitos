<script>
  import { agents } from '../lib/data.js'
  import ConnectorChip from '../lib/ConnectorChip.svelte'
  import { describeMode } from '../lib/format.js'
  import { createEventDispatcher } from 'svelte'

  const dispatch = createEventDispatcher()
  let q = ''
  let cat = 'all'
  $: cats = ['all', ...new Set(agents.map(a => a.category))]
  $: filtered = agents.filter(a =>
      (cat === 'all' || a.category === cat) &&
      (q === '' || (a.name + a.tagline + a.category).toLowerCase().includes(q.toLowerCase())))
</script>

<div class="page">
  <div class="head">
    <div>
      <h1>Agent library</h1>
      <p>Templates your agents can run. Pick one to launch a new application.</p>
    </div>
    <button class="new" on:click={() => dispatch('launch')}>＋ Launch custom</button>
  </div>

  <div class="bar">
    <input bind:value={q} placeholder="Search agents…" />
    <div class="cats">
      {#each cats as c}
        <button class="c" class:on={cat===c} on:click={() => cat=c}>
          {c === 'all' ? 'All' : c}
        </button>
      {/each}
    </div>
  </div>

  <div class="grid">
    {#each filtered as a}
      <button class="card" on:click={() => dispatch('pick', a)}>
        <div class="ic" style="--c:{a.color}">{a.icon}</div>
        <div class="nm">{a.name}</div>
        <div class="ct">{a.category}</div>
        <p class="tg">{a.tagline}</p>
        <div class="row" style="gap:6px;margin-top:10px">
          <span class="pill">{describeMode(a.defaultMode)}</span>
          {#each a.connectors as cid}<ConnectorChip id={cid} size={18} />{/each}
        </div>
        <div class="arrow">Launch →</div>
      </button>
    {/each}
  </div>
</div>

<style>
  .page { padding: 28px 40px; max-width: 1200px; margin: 0 auto; }
  .head { display:flex; align-items:flex-end; justify-content:space-between; margin-bottom:20px; }
  h1 { font-size:22px; font-weight:600; letter-spacing:-0.01em; }
  p { color: var(--text-muted); font-size:13px; margin-top:3px; }
  .new { padding:8px 14px; border-radius: var(--radius-sm); background: var(--accent); color:#fff;
         font-size:13px; font-weight:500; }
  .new:hover { background: var(--accent-hover); }
  .bar { display:flex; gap:14px; align-items:center; margin-bottom:20px; flex-wrap:wrap; }
  .bar input { flex:1; min-width:200px; padding:9px 14px; background: var(--bg-elev); border:1px solid var(--border);
               border-radius: var(--radius-sm); color: var(--text); outline:none; }
  .bar input:focus { border-color: var(--accent); }
  .cats { display:flex; gap:6px; }
  .c { padding:6px 12px; border-radius:999px; background: var(--bg-elev); border:1px solid var(--border);
       font-size:12px; color: var(--text-dim); }
  .c.on { background: var(--accent-soft); border-color: var(--accent); color: var(--accent-hover); }

  .grid { display:grid; grid-template-columns: repeat(auto-fill, minmax(260px,1fr)); gap:14px; }
  .card { text-align:left; padding:20px; border-radius: var(--radius-lg); background: var(--bg-elev);
          border:1px solid var(--border); transition: all .15s; display:flex; flex-direction:column; gap:3px; }
  .card:hover { border-color: var(--accent); transform: translateY(-2px); box-shadow: var(--shadow); }
  .ic { width:44px;height:44px;border-radius:12px; display:flex;align-items:center;justify-content:center;
        font-size:22px; background: color-mix(in srgb, var(--c) 18%, transparent); margin-bottom:8px; }
  .nm { font-weight:600; font-size:15px; }
  .ct { font-size:11px; color: var(--text-muted); }
  .tg { font-size:12px; color: var(--text-dim); margin-top:8px; line-height:1.5; }
  .pill { font-size:11px; padding:2px 8px; border-radius:999px; background: rgba(255,255,255,0.05);
          border:1px solid var(--border); color: var(--text-dim); }
  .arrow { margin-top:14px; font-size:12px; color: var(--accent); font-weight:500; }
</style>
