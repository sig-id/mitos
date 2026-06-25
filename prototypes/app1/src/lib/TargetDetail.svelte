<script>
  import { createEventDispatcher } from 'svelte'
  import Modal from './Modal.svelte'
  import { traces, getAgent, getHarness } from './data.js'
  import HarnessBadge from './HarnessBadge.svelte'
  import ConnectorChip from './ConnectorChip.svelte'
  import { apps } from './data.js'
  import { formatTime } from './format.js'
  import { get } from 'svelte/store'

  export let open = false
  export let target = null

  const dispatch = createEventDispatcher()

  let steerText = ''
  let steerLog = []
  let advanced = false

  $: app = target ? get(apps).find(a => a.id === target.appId) : null
  $: agent = app ? getAgent(app.agentId) : null
  $: trace = target ? get(traces)[target.id] || [] : []

  function sendSteer() {
    if (!steerText.trim()) return
    steerLog = [...steerLog, { id: Date.now(), text: steerText, ts: Date.now() }]
    steerText = ''
  }

  $: if (!open) { steerText=''; steerLog=[]; advanced=false }
</script>

<Modal {open} title="Target detail" width="640px"
       on:close={() => dispatch('close')}>
  {#if target && app && agent}
    <div class="hd">
      <div class="ic" style="--c:{agent.color}">{agent.icon}</div>
      <div class="grow">
        <div class="ttl">{target.title}</div>
        <div class="row" style="gap:10px;margin-top:6px;flex-wrap:wrap">
          <span class="k">{target.kind}</span>
          <span class="src"><ConnectorChip id={target.source} size={16} /> {target.refs}</span>
          <span class="ago">{target.age} old</span>
          {#if target.attention}
            <span class="attn">● needs attention</span>
          {/if}
        </div>
      </div>
    </div>

    {#if target.note}
      <div class="note">{target.note}</div>
    {/if}

    <div class="meta-row">
      <div class="m">
        <span>Application</span>
        <b>{app.name}</b>
      </div>
      <div class="m">
        <span>Harness</span>
        <b><HarnessBadge id={app.harness} /></b>
      </div>
      <div class="m">
        <span>Stage</span>
        <b>{target.stage}</b>
      </div>
    </div>

    <!-- Trace -->
    <div class="sec">
      <div class="sec-h">
        <span>Live trace</span>
        <button class="adv" class:on={advanced} on:click={() => advanced = !advanced}>
          {advanced ? 'Hide advanced' : 'Show advanced'}
        </button>
      </div>
      <div class="trace">
        {#each trace as s, i}
          <div class="t-step" class:live={s.status==='live'}>
            <div class="rail">
              <span class="dot"></span>
              {#if i < trace.length - 1}<span class="line"></span>{/if}
            </div>
            <div class="body">
              <div class="row between">
                <span class="lbl">{s.label}</span>
                <span class="dur mono">{s.status==='live' ? '…' : (s.dur>1000 ? (s.dur/1000).toFixed(1)+'s' : s.dur+'ms')}</span>
              </div>
              <div class="ph">{s.phase}</div>
              {#if advanced}
                <div class="x mono">step.{s.id} · {formatTime(s.ts)}</div>
              {/if}
            </div>
          </div>
        {:else}
          <div class="empty">No trace yet.</div>
        {/each}
      </div>
    </div>

    <!-- Steering (advanced) -->
    {#if advanced}
      <div class="sec steer">
        <div class="sec-h"><span>Steer the agent</span></div>
        <p class="hint">Inject an additional prompt mid-run. The agent will incorporate this on its next step.</p>
        <textarea rows="2" bind:value={steerText} placeholder="e.g. Pay extra attention to error handling in the auth flow."></textarea>
        <div class="row" style="justify-content:flex-end;margin-top:8px">
          <button class="btn primary sm" on:click={sendSteer} disabled={!steerText.trim()}>Send steer</button>
        </div>
        {#if steerLog.length}
          <div class="log">
            {#each steerLog as l}
              <div class="le"><span class="lt mono">{formatTime(l.ts)}</span> {l.text}</div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}
  {/if}
</Modal>

<style>
  .hd { display:flex; gap:14px; align-items:flex-start; }
  .ic { width:42px;height:42px;border-radius:10px; display:flex;align-items:center;justify-content:center;
        font-size:20px; background: color-mix(in srgb, var(--c) 18%, transparent); flex:none; }
  .ttl { font-weight:600; font-size:15px; line-height:1.35; }
  .k, .src, .ago { font-size:11px; padding:3px 8px; border-radius:999px; background: rgba(255,255,255,0.04);
                   border:1px solid var(--border); color: var(--text-dim); display:inline-flex; align-items:center; gap:5px; }
  .attn { font-size:11px; color: var(--yellow); }
  .note { margin-top:14px; padding:10px 12px; background: var(--yellow-soft); border:1px solid rgba(251,191,36,.25);
          border-radius: var(--radius-sm); font-size:13px; color: #fde68a; }
  .meta-row { display:grid; grid-template-columns: repeat(3,1fr); gap:12px; margin-top:18px;
              padding:14px 0; border-top:1px solid var(--border-soft); border-bottom:1px solid var(--border-soft); }
  .m span { display:block; font-size:11px; color: var(--text-muted); margin-bottom:3px; }
  .m b { font-size:13px; font-weight:500; }

  .sec { margin-top:20px; }
  .sec-h { display:flex; align-items:center; justify-content:space-between; margin-bottom:10px;
           font-size:12px; font-weight:600; color: var(--text-dim); text-transform: uppercase; letter-spacing:.06em; }
  .adv { font-size:11px; color: var(--accent); padding:3px 8px; border-radius: var(--radius-sm);
         text-transform: none; letter-spacing: 0; }
  .adv:hover { background: var(--accent-soft); }
  .adv.on { background: var(--accent-soft); }

  .trace { display:flex; flex-direction:column; }
  .t-step { display:flex; gap:12px; padding-bottom:14px; }
  .rail { display:flex; flex-direction:column; align-items:center; flex:none; padding-top:2px; }
  .dot { width:8px;height:8px;border-radius:50%; background: var(--accent); flex:none; }
  .t-step.live .dot { background: var(--green); box-shadow: 0 0 0 3px var(--green-soft); animation: bp 1.4s infinite; }
  .line { width:2px; flex:1; background: var(--border); margin-top:2px; min-height:14px; }
  @keyframes bp { 0%,100%{ box-shadow:0 0 0 3px var(--green-soft);} 50%{ box-shadow:0 0 0 6px transparent;} }
  .body { flex:1; min-width:0; }
  .lbl { font-size:13px; }
  .dur { font-size:11px; color: var(--text-muted); }
  .ph { font-size:10px; text-transform: uppercase; letter-spacing:.06em; color: var(--text-faint); margin-top:2px; }
  .x { font-size:10px; color: var(--text-faint); margin-top:4px; }
  .empty { color: var(--text-muted); font-size:13px; padding:12px 0; }

  .steer textarea { width:100%; background: var(--bg); border:1px solid var(--border); color: var(--text);
                    padding:10px; border-radius: var(--radius-sm); resize: vertical; outline:none; }
  .steer textarea:focus { border-color: var(--accent); }
  .hint { font-size:12px; color: var(--text-muted); margin-bottom:10px; }
  .btn.sm { padding:6px 12px; font-size:12px; }
  .btn.primary { background: var(--accent); color:#fff; border-radius: var(--radius-sm); }
  .btn.primary:disabled { opacity:.4; cursor:not-allowed; }
  .log { margin-top:12px; display:flex; flex-direction:column; gap:6px; }
  .le { font-size:12px; color: var(--text-dim); padding:6px 10px; background: var(--bg-elev-2);
        border:1px solid var(--border); border-radius: var(--radius-sm); }
  .lt { color: var(--text-muted); margin-right:8px; }
</style>
