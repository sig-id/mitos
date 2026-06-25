<script lang="ts">
  import ConnectorChip from "../lib/ConnectorChip.svelte";
  import HarnessBadge from "../lib/HarnessBadge.svelte";
  import { humanize } from "../lib/format";
  import type { Application, Entrypoint } from "../lib/api";

  let {
    apps = [],
    onlaunch = (_a: Application, _e: Entrypoint) => {},
    onadd = (_src: string) => {}
  }: {
    apps: Application[];
    onlaunch?: (a: Application, e: Entrypoint) => void;
    onadd?: (source: string) => void;
  } = $props();

  let q = $state("");
  let source = $state("");
  let filtered = $derived(
    apps.filter((a) =>
      q === ""
        ? true
        : (a.name + " " + (a.description || "") + " " + (a.id || ""))
            .toLowerCase()
            .includes(q.toLowerCase())
    )
  );
</script>

<div class="page">
  <div class="head">
    <div>
      <h1>Applications</h1>
      <p>Worker definitions installed in this control plane. Run one to launch an agent.</p>
    </div>
  </div>

  <form
    class="add"
    onsubmit={(e) => {
      e.preventDefault();
      if (source.trim()) {
        onadd(source.trim());
        source = "";
      }
    }}
  >
    <input bind:value={source} placeholder="git URL or local app directory" />
    <button type="submit">Add</button>
  </form>

  <div class="bar">
    <input bind:value={q} placeholder="Search applications…" />
  </div>

  <div class="grid">
    {#each filtered as a}
      {@const entry = a.entrypoints?.[0]}
      <div class="card">
        <div class="ic">{(a.entrypoints || []).length}</div>
        <div class="nm">{a.name}</div>
        <div class="ct">{a.version || "local"}</div>
        <p class="tg">{a.description || "An agentic application."}</p>
        <div class="row" style="gap:6px;margin-top:10px;flex-wrap:wrap">
          {#each a.entrypoints || [] as e}
            <span class="pill">{e.pipeline || e.id}</span>
          {/each}
        </div>
        <div class="conns">
          {#each (a.io?.inputs || []).slice(0, 4) as b}
            <ConnectorChip id={b.connector || "manual"} size={18} />
          {/each}
        </div>
        {#if entry}
          <button class="launch" onclick={() => onlaunch(a, entry)}>Run →</button>
        {/if}
      </div>
    {:else}
      <div class="empty">No applications. Add one above.</div>
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
    margin-bottom: 20px;
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
  .add {
    display: flex;
    gap: 8px;
    margin-bottom: 16px;
  }
  .add input {
    flex: 1;
    padding: 9px 14px;
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    border-radius: var(--mt-radius-sm);
    color: var(--mt-text);
    outline: none;
  }
  .add input:focus {
    border-color: var(--mt-accent);
  }
  .add button {
    padding: 9px 16px;
    border-radius: var(--mt-radius-sm);
    background: var(--mt-accent);
    color: #fff;
    font-size: 13px;
    font-weight: 500;
  }
  .add button:hover {
    background: var(--mt-accent-hover);
  }
  .bar {
    display: flex;
    margin-bottom: 20px;
  }
  .bar input {
    flex: 1;
    padding: 9px 14px;
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    border-radius: var(--mt-radius-sm);
    color: var(--mt-text);
    outline: none;
  }
  .bar input:focus {
    border-color: var(--mt-accent);
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 14px;
  }
  .card {
    padding: 20px;
    border-radius: var(--mt-radius-lg);
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    display: flex;
    flex-direction: column;
    gap: 3px;
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
    margin-bottom: 8px;
  }
  .nm {
    font-weight: 600;
    font-size: 15px;
  }
  .ct {
    font-size: 11px;
    color: var(--mt-text-muted);
  }
  .tg {
    font-size: 12px;
    color: var(--mt-text-dim);
    margin-top: 8px;
    line-height: 1.5;
  }
  .pill {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 999px;
    background: rgba(255, 255, 255, 0.05);
    border: 1px solid var(--mt-border);
    color: var(--mt-text-dim);
  }
  .conns {
    display: flex;
    gap: 5px;
    margin-top: 10px;
    flex-wrap: wrap;
  }
  .launch {
    margin-top: 14px;
    font-size: 13px;
    color: var(--mt-accent);
    font-weight: 500;
    text-align: left;
  }
  .launch:hover {
    text-decoration: underline;
  }
  .empty {
    grid-column: 1 / -1;
    padding: 40px;
    text-align: center;
    color: var(--mt-text-muted);
  }
</style>
