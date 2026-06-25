<script>
  export let status // running | idle | done | error
  $: cls = 's s-' + (status || 'idle')
</script>

<span class={cls}>
  <span class="pulse"></span>
  <span class="t">
    {status === 'running' ? 'Live' :
     status === 'idle' ? 'Idle' :
     status === 'done' ? 'Done' :
     status === 'error' ? 'Error' : status}
  </span>
</span>

<style>
  .s { display:inline-flex; align-items:center; gap:6px; font-size:11px; font-weight:500;
       padding:3px 8px; border-radius:999px; border:1px solid transparent; }
  .pulse { width:6px; height:6px; border-radius:50%; flex:none; }
  .s-running { color: var(--green); background: var(--green-soft); }
  .s-running .pulse { background: var(--green); box-shadow: 0 0 0 0 var(--green); animation: p 1.8s infinite; }
  .s-idle    { color: var(--text-muted); background: rgba(255,255,255,0.03); }
  .s-idle .pulse { background: var(--text-muted); }
  .s-done    { color: var(--blue); background: var(--blue-soft); }
  .s-done .pulse { background: var(--blue); }
  .s-error   { color: var(--red); background: var(--red-soft); }
  .s-error .pulse { background: var(--red); }
  @keyframes p {
    0% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--green) 60%, transparent); }
    70%{ box-shadow: 0 0 0 6px transparent; }
    100%{ box-shadow: 0 0 0 0 transparent; }
  }
</style>
