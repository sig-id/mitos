<script>
  import { createEventDispatcher, afterUpdate, beforeUpdate, tick } from 'svelte'
  import { fade, scale } from 'svelte/transition'
  export let open = false
  export let title = ''
  export let width = '640px'
  const dispatch = createEventDispatcher()
  let trap

  function onKey(e) {
    if (e.key === 'Escape') dispatch('close')
  }

  $: if (open) {
    document.addEventListener('keydown', onKey)
    document.body.style.overflow = 'hidden'
  } else {
    document.removeEventListener('keydown', onKey)
    document.body.style.overflow = ''
  }
</script>

{#if open}
  <div class="backdrop" transition:fade={{ duration: 150 }} on:click|self={() => dispatch('close')}>
    <div class="modal" style="--w:{width}" transition:scale={{ duration: 180, start: 0.96 }}>
      <div class="head">
        <h3>{title}</h3>
        <button class="x" on:click={() => dispatch('close')} aria-label="Close">✕</button>
      </div>
      <div class="body">
        <slot />
      </div>
      {#if $$slots.footer}
        <div class="foot">
          <slot name="footer" />
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed; inset: 0; z-index: 100;
    background: rgba(0,0,0,0.6);
    backdrop-filter: blur(6px);
    display: flex; align-items: center; justify-content: center;
    padding: 32px;
  }
  .modal {
    width: var(--w); max-width: 100%; max-height: 88vh;
    background: var(--bg-elev);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow-lg);
    display: flex; flex-direction: column;
    overflow: hidden;
  }
  .head {
    display: flex; align-items: center; justify-content: space-between;
    padding: 18px 20px;
    border-bottom: 1px solid var(--border-soft);
  }
  .head h3 { font-size: 15px; font-weight: 600; letter-spacing: -0.01em; }
  .x {
    width: 28px; height: 28px; border-radius: var(--radius-sm);
    color: var(--text-muted); font-size: 13px;
    display: flex; align-items: center; justify-content: center;
    transition: all .15s;
  }
  .x:hover { background: var(--bg-hover); color: var(--text); }
  .body { overflow-y: auto; padding: 20px; flex: 1; }
  .foot {
    padding: 14px 20px; border-top: 1px solid var(--border-soft);
    display: flex; justify-content: flex-end; gap: 10px;
    background: rgba(0,0,0,0.15);
  }
</style>
