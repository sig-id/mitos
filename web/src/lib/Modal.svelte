<script lang="ts">
  import { fade, scale } from "svelte/transition";

  let {
    open = false,
    title = "",
    width = "640px",
    onclose = () => {},
    children,
    footer
  }: {
    open?: boolean;
    title?: string;
    width?: string;
    onclose?: () => void;
    children?: import("svelte").Snippet;
    footer?: import("svelte").Snippet;
  } = $props();

  function onKey(e: KeyboardEvent) {
    if (e.key === "Escape") onclose();
  }

  $effect(() => {
    if (open) {
      document.addEventListener("keydown", onKey);
      document.body.style.overflow = "hidden";
    } else {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = "";
    }
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = "";
    };
  });
</script>

{#if open}
  <div
    class="backdrop"
    transition:fade={{ duration: 150 }}
    role="presentation"
    onclick={(e) => {
      if (e.target === e.currentTarget) onclose();
    }}
  >
    <div class="modal" style="--w:{width}" transition:scale={{ duration: 180, start: 0.96 }}>
      <div class="head">
        <h3>{title}</h3>
        <button class="x" onclick={onclose} aria-label="Close">✕</button>
      </div>
      <div class="body">
        {@render children?.()}
      </div>
      {#if footer}
        <div class="foot">
          {@render footer()}
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    z-index: 100;
    background: rgba(0, 0, 0, 0.6);
    backdrop-filter: blur(6px);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 32px;
  }
  .modal {
    width: var(--w);
    max-width: 100%;
    max-height: 88vh;
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    border-radius: var(--mt-radius-lg);
    box-shadow: var(--mt-shadow-lg);
    display: flex;
    flex-direction: column;
    overflow: hidden;
  }
  .head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 18px 20px;
    border-bottom: 1px solid var(--mt-border-soft);
  }
  .head h3 {
    margin: 0;
    font-size: 15px;
    font-weight: 600;
    letter-spacing: -0.01em;
  }
  .x {
    width: 28px;
    height: 28px;
    border-radius: var(--mt-radius-sm);
    color: var(--mt-text-muted);
    font-size: 13px;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: all 0.15s;
  }
  .x:hover {
    background: var(--mt-bg-hover);
    color: var(--mt-text);
  }
  .body {
    overflow-y: auto;
    padding: 20px;
    flex: 1;
  }
  .foot {
    padding: 14px 20px;
    border-top: 1px solid var(--mt-border-soft);
    display: flex;
    justify-content: flex-end;
    gap: 10px;
    background: rgba(0, 0, 0, 0.15);
  }
</style>
