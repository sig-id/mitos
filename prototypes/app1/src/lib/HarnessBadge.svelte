<script>
  import { getHarness } from './data.js'
  export let id
  $: h = getHarness(id)
</script>

{#if h}
  <span class="h" style="--c:{h.color}">
    <span class="dot">{h.icon}</span>
    <span class="n">{h.name}</span>
  </span>
{/if}

<style>
  .h { display:inline-flex; align-items:center; gap:6px; font-size:12px; color:var(--text-dim); }
  .dot {
    display:inline-flex; align-items:center; justify-content:center;
    width:16px; height:16px; border-radius:4px;
    background: color-mix(in srgb, var(--c) 20%, transparent);
    color: var(--c);
    font-size: 10px;
  }
  .n { font-weight: 500; }
</style>
