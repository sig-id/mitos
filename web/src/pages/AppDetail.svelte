<script lang="ts">
  import StatusPill from "../lib/StatusPill.svelte";
  import HarnessBadge from "../lib/HarnessBadge.svelte";
  import ConnectorChip from "../lib/ConnectorChip.svelte";
  import { formatRelative, compactId, humanize } from "../lib/format";
  import { go } from "../lib/router.svelte";
  import type { Application, RunSummary } from "../lib/api";

  let {
    app,
    runs = [],
    onlaunch = () => {},
    onrun = (_id: string) => {}
  }: {
    app: Application | null;
    runs?: RunSummary[];
    onlaunch?: () => void;
    onrun?: (id: string) => void;
  } = $props();

  let appRuns = $derived(
    runs.filter((r) => (r.task || r.run_id || "").includes(app?.id || "\0"))
  );
</script>

<div class="page">
  {#if !app}
    <div class="missing">Application not found. <a href="#/dashboard">Back</a></div>
  {:else}
    <div class="head">
      <button class="back" onclick={() => go("dashboard")}>← Dashboard</button>
      <div class="ic">{(app.entrypoints || []).length}</div>
      <div class="grow">
        <h1>{app.name}</h1>
        <div class="sub">{app.description || compactId(app.app_id)}</div>
      </div>
      <button class="btn primary" onclick={onlaunch}>Run entrypoint</button>
    </div>

    <div class="grid">
      <div class="card">
        <div class="ch">Overview</div>
        <div class="cb">
          <div class="r"><span>Version</span><b>{app.version || "local"}</b></div>
          <div class="r"><span>Pipelines</span><b>{app.pipelines?.length || 0}</b></div>
          <div class="r"><span>Capabilities</span><b>{app.capabilities?.length || 0}</b></div>
          <div class="r"><span>Trust tier</span><b>{app.requirements?.trust_tier || "—"}</b></div>
        </div>
      </div>

      <div class="card">
        <div class="ch">I/O bindings</div>
        <div class="cb">
          <div class="sub2">Inputs</div>
          {#each app.io?.inputs || [] as b}
            <div class="conn">
              <ConnectorChip id={b.connector || "manual"} size={22} />
              <span class="ci">{b.connector} → {b.entrypoint || "—"}</span>
            </div>
          {:else}
            <div class="muted2">Manual / web only.</div>
          {/each}
          <div class="sub2">Outputs</div>
          {#each app.io?.outputs || [] as b}
            <div class="conn">
              <span class="sink-pill">{b.sink || "web"}</span>
              <span class="ci">on {b.on || "run.done"}</span>
            </div>
          {:else}
            <div class="muted2">Web receipt.</div>
          {/each}
        </div>
      </div>

      <div class="card span2">
        <div class="ch">Harnesses</div>
        <div class="cb">
          {#each app.requirements?.harnesses || [] as h}
            <div class="h-row"><HarnessBadge id={h} /></div>
          {:else}
            <div class="muted2">Not declared.</div>
          {/each}
        </div>
      </div>
    </div>

    <div class="card">
      <div class="ch">Entrypoints</div>
      <div class="ent-list">
        {#each app.entrypoints || [] as e}
          <div class="ent">
            <div class="grow">
              <div class="et">{e.title || e.id}</div>
              <div class="ed muted">{e.description || "An application entrypoint."}</div>
              <div class="meta">
                <span class="pill">pipeline: {e.pipeline}</span>
                <span class="pill">workspace: {humanize(typeof e.workspace === "object" ? e.workspace?.kind : e.workspace)}</span>
              </div>
            </div>
            <button class="btn ghost sm" onclick={onlaunch}>Run</button>
          </div>
        {/each}
      </div>
    </div>

    <div class="card">
      <div class="ch">Recent runs ({appRuns.length})</div>
      <div class="run-list">
        {#each appRuns.slice(0, 12) as r}
          <button class="run-row" onclick={() => onrun(r.run_id)}>
            <span class="rt mono">{r.task || compactId(r.run_id)}</span>
            <StatusPill status={r.status} />
            <span class="muted">{formatRelative(r.last_event_ms || r.created_at_ms)}</span>
          </button>
        {:else}
          <div class="muted2">No runs yet.</div>
        {/each}
      </div>
    </div>
  {/if}
</div>

<style>
  .page {
    padding: 28px 40px;
    max-width: 1100px;
    margin: 0 auto;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 14px;
    margin-bottom: 28px;
    flex-wrap: wrap;
  }
  .back {
    color: var(--mt-text-muted);
    font-size: 13px;
  }
  .back:hover {
    color: var(--mt-text);
  }
  .ic {
    width: 44px;
    height: 44px;
    border-radius: 12px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 20px;
    font-weight: 600;
    background: var(--mt-accent-soft);
    color: var(--mt-accent);
  }
  h1 {
    font-size: 22px;
    font-weight: 600;
    letter-spacing: -0.01em;
    margin: 0;
  }
  .sub {
    color: var(--mt-text-dim);
    font-size: 13px;
    margin-top: 2px;
  }
  .btn {
    padding: 8px 16px;
    border-radius: var(--mt-radius-sm);
    font-size: 13px;
    font-weight: 500;
  }
  .btn.ghost {
    color: var(--mt-text-dim);
    border: 1px solid var(--mt-border);
  }
  .btn.ghost:hover {
    background: var(--mt-bg-hover);
    color: var(--mt-text);
  }
  .btn.ghost.sm {
    padding: 6px 12px;
    font-size: 12px;
  }
  .btn.primary {
    background: var(--mt-accent);
    color: #fff;
  }
  .btn.primary:hover {
    background: var(--mt-accent-hover);
  }

  .grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 16px;
    margin-bottom: 16px;
  }
  .span2 {
    grid-column: span 2;
  }
  .card {
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    border-radius: var(--mt-radius-lg);
    overflow: hidden;
    margin-bottom: 16px;
  }
  .ch {
    padding: 14px 18px;
    border-bottom: 1px solid var(--mt-border-soft);
    font-size: 12px;
    font-weight: 600;
    color: var(--mt-text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .cb {
    padding: 12px 18px;
  }
  .r {
    display: flex;
    justify-content: space-between;
    padding: 6px 0;
    font-size: 13px;
    border-bottom: 1px solid var(--mt-border-soft);
  }
  .r:last-child {
    border-bottom: none;
  }
  .r span {
    color: var(--mt-text-muted);
  }
  .r b {
    font-weight: 500;
  }
  .sub2 {
    font-size: 11px;
    color: var(--mt-text-faint);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    margin: 8px 0 6px;
  }
  .conn {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 4px 0;
    font-size: 13px;
  }
  .ci {
    color: var(--mt-text-dim);
  }
  .sink-pill {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: var(--mt-radius-sm);
    background: var(--mt-blue-soft);
    color: var(--mt-blue);
  }
  .muted2 {
    color: var(--mt-text-muted);
    font-size: 12px;
    padding: 4px 0;
  }
  .h-row {
    padding: 4px 0;
  }

  .ent-list {
    padding: 4px 18px 12px;
  }
  .ent {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 14px 0;
    border-bottom: 1px solid var(--mt-border-soft);
  }
  .ent:last-child {
    border-bottom: none;
  }
  .et {
    font-weight: 600;
    font-size: 14px;
  }
  .ed {
    font-size: 12px;
    margin-top: 2px;
  }
  .meta {
    display: flex;
    gap: 6px;
    margin-top: 8px;
    flex-wrap: wrap;
  }
  .pill {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 999px;
    background: rgba(255, 255, 255, 0.04);
    border: 1px solid var(--mt-border);
    color: var(--mt-text-dim);
  }
  .run-list {
    padding: 4px 18px 12px;
  }
  .run-row {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 10px 0;
    border-bottom: 1px solid var(--mt-border-soft);
    font-size: 13px;
    width: 100%;
    text-align: left;
  }
  .run-row:last-child {
    border-bottom: none;
  }
  .run-row:hover {
    background: var(--mt-bg-hover);
  }
  .rt {
    flex: 1;
    word-break: break-word;
  }
  .muted {
    color: var(--mt-text-muted);
    font-size: 12px;
  }
  .missing {
    padding: 60px;
    text-align: center;
    color: var(--mt-text-muted);
  }
</style>
