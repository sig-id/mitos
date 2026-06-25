<script lang="ts">
  import StatusPill from "../lib/StatusPill.svelte";
  import { formatRelative } from "../lib/format";
  import type { RunSummary } from "../lib/api";

  let {
    runs = [],
    onrun = (_id: string) => {}
  }: {
    runs: RunSummary[];
    onrun?: (id: string) => void;
  } = $props();
</script>

<div class="page">
  <div class="head">
    <h1>Runs</h1>
    <p>All runs — manual launches, connector signals, and scheduled triggers.</p>
  </div>
  <div class="table">
    <div class="thr">
      <span>Run</span>
      <span>Source</span>
      <span>Status</span>
      <span>Last event</span>
      <span>Events</span>
    </div>
    {#each runs as r}
      <button class="trow" onclick={() => onrun(r.run_id)}>
        <span class="nm">{r.task || r.run_id}</span>
        <span class="muted">{r.provider || "—"}</span>
        <span><StatusPill status={r.status} /></span>
        <span class="muted">{formatRelative(r.last_event_ms || r.created_at_ms)}</span>
        <span class="muted">{r.events || 0}</span>
      </button>
    {:else}
      <div class="empty">No runs yet.</div>
    {/each}
  </div>
</div>

<style>
  .page {
    padding: 28px 40px;
    max-width: 1200px;
    margin: 0 auto;
  }
  .head {
    margin-bottom: 22px;
  }
  h1 {
    font-size: 22px;
    font-weight: 600;
    letter-spacing: -0.01em;
    margin: 0;
  }
  p {
    color: var(--mt-text-muted);
    font-size: 13px;
    margin-top: 3px;
  }
  .table {
    border: 1px solid var(--mt-border);
    border-radius: var(--mt-radius-lg);
    overflow: hidden;
  }
  .thr,
  .trow {
    display: grid;
    grid-template-columns: 2fr 1fr 1fr 1fr 0.7fr;
    gap: 10px;
    align-items: center;
    padding: 12px 18px;
  }
  .thr {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--mt-text-faint);
    background: var(--mt-bg-elev);
    border-bottom: 1px solid var(--mt-border);
  }
  .trow {
    width: 100%;
    text-align: left;
    border-bottom: 1px solid var(--mt-border-soft);
    font-size: 13px;
    transition: background 0.12s;
  }
  .trow:last-child {
    border-bottom: none;
  }
  .trow:hover {
    background: var(--mt-bg-elev);
  }
  .nm {
    font-weight: 500;
    word-break: break-word;
  }
  .muted {
    color: var(--mt-text-muted);
    font-size: 12px;
  }
  .empty {
    padding: 40px;
    text-align: center;
    color: var(--mt-text-muted);
  }
</style>
