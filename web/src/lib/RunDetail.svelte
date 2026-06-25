<script lang="ts">
  import Modal from "./Modal.svelte";
  import HarnessBadge from "./HarnessBadge.svelte";
  import ConnectorChip from "./ConnectorChip.svelte";
  import { steer, getRun, type RunDetail } from "./api";
  import { formatTime, humanize } from "./format";

  let {
    open = $bindable(false),
    runId = "",
    onclose = () => {}
  }: {
    open?: boolean;
    runId?: string;
    onclose?: () => void;
  } = $props();

  let run = $state<RunDetail | null>(null);
  let loading = $state(false);
  let error = $state("");
  let advanced = $state(false);
  let steerText = $state("");

  async function load(id: string) {
    if (!id) {
      run = null;
      return;
    }
    loading = true;
    error = "";
    try {
      run = await getRun(id);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
      run = null;
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (open && runId) load(runId);
    if (!open) {
      advanced = false;
      steerText = "";
    }
  });

  let events = $derived(normalizeEvents(run?.events || []));
  let timelineRows = $derived(events.map(timelineRow));
  let innerRows = $derived(
    events.filter((e) =>
      ["assistant_text", "tool_use", "tool_result", "token_usage", "status"].includes(
        e.type || e.event_type
      )
    )
  );

  function normalizeEvents(events: any[]) {
    return events.map((r) => r.event || r);
  }

  function timelineRow(e: any) {
    const type = e.type || e.event_type || "event";
    const payload = e.payload || e.params || e;
    return {
      seq: e.seq ?? e.sequence ?? "?",
      type,
      label: labelFor(type, payload),
      detail:
        e.rationale ||
        payload.reason ||
        payload.status ||
        payload.summary ||
        payload.pipeline ||
        payload.entrypoint ||
        "",
      status: statusFor(type, payload),
      e
    };
  }

  function labelFor(type: string, p: any): string {
    if (type === "task_enqueued") return `${p.id || p.task_id || "Task"} queued`;
    if (type === "task_status_changed")
      return `${p.task_id || p.id || "Task"} ${humanize(p.status || "")}`;
    if (type === "capability_started")
      return `${p.capability_id || p.capability_ref || "Capability"} started`;
    if (type === "capability_finished")
      return `${p.capability_id || p.capability_ref || "Capability"} finished`;
    if (type === "signal_received") return `${p.connector || "Connector"} signal`;
    if (type === "delivery_sent") return `${p.sink || (p.receipt && p.receipt.sink) || "Sink"} delivery`;
    if (type === "delivery_failed") return `${p.sink || "Sink"} delivery failed`;
    if (type === "verification_recorded") return "Verification recorded";
    return humanize(type);
  }

  function statusFor(type: string, p: any): string {
    if (type.includes("failed") || type.includes("denied") || p.status === "failed") return "error";
    if (type.includes("approval") || type.includes("parked") || p.status === "parked") return "parked";
    if (type.includes("finished") || type.includes("done") || type.includes("sent"))
      return "done";
    if (type.includes("started") || type.includes("running")) return "running";
    return "idle";
  }

  async function sendSteer(interaction: string) {
    if (!runId) return;
    error = "";
    try {
      await steer(runId, interaction, steerText.trim() ? { text: steerText.trim() } : {});
      steerText = "";
      await load(runId);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }
</script>

<Modal {open} title="Run trace" width="720px" onclose={() => (open = false)}>
  {#if loading}
    <div class="muted">Loading…</div>
  {/if}
  {#if error}
    <div class="err">{error}</div>
  {/if}

  {#if run}
    <div class="hd">
      <div class="grow">
        <div class="ttl mono">{runId}</div>
        <div class="row" style="gap:10px;margin-top:6px;flex-wrap:wrap">
          <span class="k">{run.manifest?.provider || "run"}</span>
          <span class="ago">{run.event_count || events.length} events</span>
        </div>
      </div>
    </div>

    <div class="sec">
      <div class="sec-h">
        <span>Outer pipeline timeline</span>
        <button
          class="adv"
          class:on={advanced}
          onclick={() => (advanced = !advanced)}
        >
          {advanced ? "Hide steering" : "Steer"}
        </button>
      </div>
      <div class="trace">
        {#each timelineRows as r}
          <div class="t-step" class:live={r.status === "running"}>
            <div class="rail">
              <span class="dot" class:ok={r.status === "done"} class:warn={r.status === "parked"}></span>
              <span class="line"></span>
            </div>
            <div class="body">
              <div class="row between">
                <span class="lbl">{r.label}</span>
                <span class="seq mono">#{r.seq}</span>
              </div>
              <div class="ph">{r.type}{r.detail ? " · " + r.detail : ""}</div>
            </div>
          </div>
        {:else}
          <div class="empty">No events.</div>
        {/each}
      </div>
    </div>

    {#if advanced}
      <div class="sec steer">
        <div class="sec-h"><span>Steer the agent</span></div>
        <div class="steer-btns">
          <button class="btn ghost sm" onclick={() => sendSteer("approve")}>Approve</button>
          <button class="btn ghost sm" onclick={() => sendSteer("interrupt")}>Stop</button>
        </div>
        <textarea
          rows="2"
          bind:value={steerText}
          placeholder="Answer or instruction for the agent"
        ></textarea>
        <div class="row" style="justify-content:flex-end;margin-top:8px">
          <button
            class="btn primary sm"
            onclick={() => sendSteer("prompt")}
            disabled={!steerText.trim()}>Send</button
          >
        </div>
      </div>
    {/if}

    {#if innerRows.length > 0}
      <div class="sec">
        <div class="sec-h"><span>Inner harness trace</span></div>
        <div class="inner">
          {#each innerRows as e}
            <pre class="mono">{JSON.stringify(e, null, 2)}</pre>
          {/each}
        </div>
      </div>
    {/if}
  {/if}
</Modal>

<style>
  .hd {
    display: flex;
    gap: 14px;
    align-items: flex-start;
  }
  .ttl {
    font-weight: 600;
    font-size: 14px;
    line-height: 1.35;
  }
  .k,
  .ago {
    font-size: 11px;
    padding: 3px 8px;
    border-radius: 999px;
    background: rgba(255, 255, 255, 0.04);
    border: 1px solid var(--mt-border);
    color: var(--mt-text-dim);
    display: inline-flex;
    align-items: center;
    gap: 5px;
  }
  .err {
    padding: 10px 12px;
    background: var(--mt-red-soft);
    border: 1px solid rgba(248, 113, 113, 0.25);
    border-radius: var(--mt-radius-sm);
    font-size: 13px;
    color: #fca5a5;
    margin-bottom: 14px;
  }
  .sec {
    margin-top: 20px;
  }
  .sec-h {
    display: flex;
    align-items: center;
    justify-content: space-between;
    margin-bottom: 10px;
    font-size: 12px;
    font-weight: 600;
    color: var(--mt-text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .adv {
    font-size: 11px;
    color: var(--mt-accent);
    padding: 3px 8px;
    border-radius: var(--mt-radius-sm);
    text-transform: none;
    letter-spacing: 0;
  }
  .adv:hover,
  .adv.on {
    background: var(--mt-accent-soft);
  }
  .trace {
    display: flex;
    flex-direction: column;
  }
  .t-step {
    display: flex;
    gap: 12px;
    padding-bottom: 14px;
  }
  .rail {
    display: flex;
    flex-direction: column;
    align-items: center;
    flex: none;
    padding-top: 2px;
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--mt-accent);
    flex: none;
  }
  .dot.ok {
    background: var(--mt-blue);
  }
  .dot.warn {
    background: var(--mt-yellow);
  }
  .t-step.live .dot {
    background: var(--mt-green);
    box-shadow: 0 0 0 3px var(--mt-green-soft);
    animation: bp 1.4s infinite;
  }
  .line {
    width: 2px;
    flex: 1;
    background: var(--mt-border);
    margin-top: 2px;
    min-height: 14px;
  }
  @keyframes bp {
    0%,
    100% {
      box-shadow: 0 0 0 3px var(--mt-green-soft);
    }
    50% {
      box-shadow: 0 0 0 6px transparent;
    }
  }
  .body {
    flex: 1;
    min-width: 0;
  }
  .lbl {
    font-size: 13px;
  }
  .seq {
    font-size: 11px;
    color: var(--mt-text-muted);
  }
  .ph {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--mt-text-faint);
    margin-top: 2px;
  }
  .empty {
    color: var(--mt-text-muted);
    font-size: 13px;
    padding: 12px 0;
  }
  .steer textarea {
    width: 100%;
    background: var(--mt-bg);
    border: 1px solid var(--mt-border);
    color: var(--mt-text);
    padding: 10px;
    border-radius: var(--mt-radius-sm);
    resize: vertical;
    outline: none;
    margin-top: 8px;
  }
  .steer textarea:focus {
    border-color: var(--mt-accent);
  }
  .steer-btns {
    display: flex;
    gap: 8px;
  }
  .btn.sm {
    padding: 6px 12px;
    font-size: 12px;
  }
  .btn.primary {
    background: var(--mt-accent);
    color: #fff;
    border-radius: var(--mt-radius-sm);
  }
  .btn.primary:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .inner {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-height: 280px;
    overflow-y: auto;
  }
  .inner pre {
    margin: 0;
    padding: 8px;
    background: var(--mt-bg);
    border: 1px solid var(--mt-border-soft);
    border-radius: var(--mt-radius-sm);
    font-size: 11px;
    color: var(--mt-text-dim);
    overflow-x: auto;
  }
</style>
