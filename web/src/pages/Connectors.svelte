<script lang="ts">
  import ConnectorChip from "../lib/ConnectorChip.svelte";
  import type { ConnectorClass, SinkClass } from "../lib/api";

  let {
    connectors = [],
    sinks = []
  }: {
    connectors: ConnectorClass[];
    sinks: SinkClass[];
  } = $props();

  function describeConnector(name: string): string {
    return (
      {
        webhook: "Inbound HTTP POST — generic ingress for any external system.",
        manual: "Operator UI / CLI / direct API call.",
        web: "The web app surface, as an input.",
        chat: "Slack / Discord messages, slash-commands, mentions.",
        email: "Inbound messages to an address.",
        tracker: "GitHub / Linear / kanban issues and cards."
      }[name] || "Platform ingress connector."
    );
  }
  function describeSink(name: string): string {
    return (
      {
        web: "The web app — live trace + receipt card. No network call.",
        deliver: "POST the run's result and artifacts to a webhook URL.",
        notify: "A human-facing 'your job is done' message.",
        reply: "Round-trip the result back to the originating connector."
      }[name] || "Platform egress sink."
    );
  }
</script>

<div class="page">
  <div class="head">
    <h1>Connectors &amp; Sinks</h1>
    <p>
      The I/O layer. Connectors bring signals in; sinks carry results back out. The web app is
      itself one connector + one sink.
    </p>
  </div>

  <h2>Inputs · Connectors</h2>
  <div class="grid">
    {#each connectors as c}
      <div class="card">
        <div class="row between">
          <ConnectorChip id={c.name} size={32} />
          <div class="dirs">
            {#if c.capabilities.ingress}<span class="dir in">IN</span>{/if}
            {#if c.capabilities.round_trip}<span class="dir rt">RT</span>{/if}
          </div>
        </div>
        <div class="nm">{c.name}</div>
        <p class="ds">{describeConnector(c.name)}</p>
      </div>
    {:else}
      <div class="empty">No connectors registered.</div>
    {/each}
  </div>

  <h2>Outputs · Sinks</h2>
  <div class="grid">
    {#each sinks as s}
      <div class="card">
        <div class="row between">
          <span class="sink-glyph">↗</span>
          <div class="dirs">
            <span class="dir out">OUT</span>
            {#if s.capabilities.round_trip}<span class="dir rt">RT</span>{/if}
          </div>
        </div>
        <div class="nm">{s.name}</div>
        <p class="ds">{describeSink(s.name)}</p>
      </div>
    {:else}
      <div class="empty">No sinks registered.</div>
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
  h2 {
    font-size: 13px;
    font-weight: 600;
    color: var(--mt-text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin: 28px 0 14px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
    gap: 14px;
  }
  .card {
    padding: 18px;
    border-radius: var(--mt-radius-lg);
    background: var(--mt-bg-elev);
    border: 1px solid var(--mt-border);
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .dirs {
    display: flex;
    gap: 4px;
  }
  .dir {
    font-size: 9px;
    font-weight: 700;
    padding: 3px 6px;
    border-radius: 4px;
    letter-spacing: 0.05em;
  }
  .dir.in {
    background: var(--mt-blue-soft);
    color: var(--mt-blue);
  }
  .dir.out {
    background: var(--mt-green-soft);
    color: var(--mt-green);
  }
  .dir.rt {
    background: var(--mt-pink-soft);
    color: var(--mt-pink);
  }
  .nm {
    font-weight: 600;
    font-size: 14px;
    margin-top: 6px;
  }
  .ds {
    font-size: 12px;
    color: var(--mt-text-dim);
    line-height: 1.5;
    margin: 0;
  }
  .sink-glyph {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    background: var(--mt-green-soft);
    color: var(--mt-green);
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 18px;
  }
  .empty {
    grid-column: 1 / -1;
    padding: 28px;
    text-align: center;
    color: var(--mt-text-muted);
    border: 1px dashed var(--mt-border);
    border-radius: var(--mt-radius-lg);
  }
</style>
