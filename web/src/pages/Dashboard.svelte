<script lang="ts">
  import StatusPill from "../lib/StatusPill.svelte";
  import ConnectorChip from "../lib/ConnectorChip.svelte";
  import { formatRelative } from "../lib/format";
  import { go } from "../lib/router.svelte";
  import type { Application, RunSummary } from "../lib/api";

  let {
    apps = [],
    runs = [],
    onlaunch = () => {},
    onrun = (_id: string) => {}
  }: {
    apps: Application[];
    runs: RunSummary[];
    onlaunch?: () => void;
    onrun?: (id: string) => void;
  } = $props();

  let appFilter = $state("all");
  let filteredRuns = $derived(
    runs.filter((r) =>
      appFilter === "all" ? true : (r.run_id || "").startsWith(appFilter) || r.provider === "app"
    )
  );

  const stages = [
    { id: "running", name: "Running", tone: "live" },
    { id: "queued", name: "Queued", tone: "muted" },
    { id: "done", name: "Done", tone: "good" }
  ];

  function stageOf(r: RunSummary): string {
    const s = (r.status || "").toLowerCase();
    if (s.includes("run") && !s.includes("done")) return "running";
    if (s.includes("queue") || s.includes("sched")) return "queued";
    if (["done", "completed", "finished"].some((x) => s.includes(x))) return "done";
    return "running";
  }
</script>

<div class="dash">
  <section class="loop">
    <div class="loop-h">
      <div>
        <h2>Applications</h2>
        <p>Agentic applications installed in this control plane.</p>
      </div>
      <button class="new" onclick={onlaunch}><span class="plus">＋</span> Run agent</button>
    </div>

    <div class="loop-strip">
      {#each apps as a}
        <button class="loop-card" onclick={() => go("apps/" + a.id)}>
          <div class="top">
            <div class="ic">{(a.entrypoints || []).length || "·"}</div>
            <span class="badge">{a.pipelines?.length || 0} pipelines</span>
          </div>
          <div class="nm">{a.name}</div>
          <div class="ag">{a.version || "local"}</div>
          <div class="conns">
            {#each (a.io?.inputs || []).slice(0, 4) as b}
              <ConnectorChip id={b.connector || "manual"} size={18} />
            {/each}
            {#each (a.io?.outputs || []).slice(0, 2) as b}
              <span class="sink">{b.sink || "web"}</span>
            {/each}
          </div>
        </button>
      {:else}
        <div class="loop-empty">No applications installed yet.</div>
      {/each}
    </div>
  </section>

  <div class="filters">
    <div class="row" style="gap:8px;flex-wrap:wrap">
      <button class="f" class:on={appFilter === "all"} onclick={() => (appFilter = "all")}
        >All runs</button
      >
      {#each apps as a}
        <button class="f" class:on={appFilter === a.id} onclick={() => (appFilter = a.id)}>
          {a.name}
        </button>
      {/each}
    </div>
  </div>

  <section class="kanban">
    {#each stages as col}
      {@const items = filteredRuns.filter((r) => stageOf(r) === col.id)}
      <div class="col">
        <div class="col-h tone-{col.tone}">
          <span class="dot"></span>
          <span class="cn">{col.name}</span>
          <span class="ct">{items.length}</span>
        </div>
        <div class="col-body">
          {#each items as r}
            <button class="card" onclick={() => onrun(r.run_id)}>
              <div class="row between">
                <span class="kind">{r.provider || "run"}</span>
                {#if r.live}
                  <span class="live"><span class="pd"></span>live</span>
                {/if}
              </div>
              <div class="ttl">{r.task || r.run_id}</div>
              <div class="foot">
                <span class="ago">{formatRelative(r.last_event_ms || r.created_at_ms)}</span>
                <span class="ago">{r.events || 0} events</span>
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
  .dash {
    padding: 24px 32px 40px;
    max-width: 1500px;
    margin: 0 auto;
  }
  .loop {
    margin-bottom: 28px;
  }
  .loop-h {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    margin-bottom: 14px;
  }
  .loop-h h2 {
    font-size: 15px;
    font-weight: 600;
    letter-spacing: -0.01em;
    margin: 0;
  }
  .loop-h p {
    font-size: 12px;
    color: var(--mt-text-muted);
    margin-top: 2px;
  }
  .new {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    padding: 8px 14px;
    border-radius: var(--mt-radius-sm);
    background: var(--mt-accent);
    color: #fff;
    font-size: 13px;
    font-weight: 500;
    transition: background 0.15s;
  }
  .new:hover {
    background: var(--mt-accent-hover);
  }
  .loop-strip {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
    gap: 12px;
  }
  .loop-card {
    text-align: left;
    padding: 14px;
    border-radius: var(--mt-radius-lg);
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    transition: all 0.15s;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .loop-card:hover {
    border-color: var(--mt-border-strong);
    transform: translateY(-1px);
    box-shadow: var(--mt-shadow);
  }
  .top {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .ic {
    width: 36px;
    height: 36px;
    border-radius: 10px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 16px;
    font-weight: 600;
    background: var(--mt-accent-soft);
    color: var(--mt-accent);
  }
  .badge {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 999px;
    background: rgba(255, 255, 255, 0.04);
    border: 1px solid var(--mt-border);
    color: var(--mt-text-muted);
  }
  .nm {
    font-weight: 600;
    font-size: 13px;
  }
  .ag {
    font-size: 11px;
    color: var(--mt-text-muted);
    margin-top: -3px;
  }
  .conns {
    display: flex;
    gap: 5px;
    flex-wrap: wrap;
    align-items: center;
  }
  .sink {
    font-size: 10px;
    padding: 1px 6px;
    border-radius: 4px;
    background: var(--mt-blue-soft);
    color: var(--mt-blue);
  }
  .loop-empty {
    padding: 28px;
    text-align: center;
    color: var(--mt-text-muted);
    border: 1px dashed var(--mt-border);
    border-radius: var(--mt-radius-lg);
    grid-column: 1 / -1;
  }

  .filters {
    margin-bottom: 18px;
  }
  .f {
    padding: 6px 12px;
    border-radius: 999px;
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    font-size: 12px;
    color: var(--mt-text-dim);
    transition: all 0.15s;
  }
  .f:hover {
    border-color: var(--mt-border-strong);
    color: var(--mt-text);
  }
  .f.on {
    background: var(--mt-accent-soft);
    border-color: var(--mt-accent);
    color: var(--mt-accent-hover);
  }

  .kanban {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 14px;
    min-height: 50vh;
  }
  .col {
    background: rgba(255, 255, 255, 0.015);
    border: 1px solid var(--mt-border-soft);
    border-radius: var(--mt-radius-lg);
    display: flex;
    flex-direction: column;
    min-height: 280px;
  }
  .col-h {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 12px 14px;
    font-size: 12px;
    font-weight: 600;
  }
  .col-h .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
  }
  .tone-muted .dot {
    background: var(--mt-text-muted);
  }
  .tone-live .dot {
    background: var(--mt-green);
  }
  .tone-good .dot {
    background: var(--mt-blue);
  }
  .cn {
    flex: 1;
  }
  .ct {
    font-size: 11px;
    color: var(--mt-text-muted);
    background: rgba(255, 255, 255, 0.04);
    padding: 1px 7px;
    border-radius: 999px;
  }
  .col-body {
    padding: 6px 10px 12px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    flex: 1;
  }
  .card {
    text-align: left;
    padding: 12px;
    border-radius: var(--mt-radius);
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    transition: all 0.15s;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .card:hover {
    border-color: var(--mt-accent);
    box-shadow: var(--mt-shadow-sm);
  }
  .kind {
    font-size: 10px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--mt-text-muted);
  }
  .live {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-size: 11px;
    color: var(--mt-green);
  }
  .pd {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--mt-green);
    animation: bp 1.4s infinite;
  }
  @keyframes bp {
    0%,
    100% {
      opacity: 1;
    }
    50% {
      opacity: 0.3;
    }
  }
  .ttl {
    font-size: 13px;
    line-height: 1.4;
    font-weight: 500;
    word-break: break-word;
  }
  .foot {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    font-size: 11px;
    color: var(--mt-text-muted);
  }
  .ago {
    white-space: nowrap;
  }
  .empty {
    padding: 18px;
    text-align: center;
    font-size: 12px;
    color: var(--mt-text-faint);
  }
</style>
