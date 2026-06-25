<script lang="ts">
  import Modal from "./Modal.svelte";
  import ConnectorChip from "./ConnectorChip.svelte";
  import HarnessBadge from "./HarnessBadge.svelte";
  import { harnesses, runEntrypoint, type Application, type Entrypoint } from "./api";
  import { humanize } from "./format";

  let {
    open = $bindable(false),
    apps = [],
    onlaunched = () => {}
  }: {
    open?: boolean;
    apps: Application[];
    onlaunched?: (runId?: string) => void;
  } = $props();

  let step = $state(0); // 0 choose app/entrypoint, 1 inputs, 2 review
  let appId = $state<string>("");
  let entrypointId = $state<string>("");
  let workspaceRef = $state("");
  let inputValues = $state<Record<string, string>>({});
  let parameterValues = $state<Record<string, string>>({});
  let error = $state("");
  let launching = $state(false);

  const steps = ["Application", "Inputs", "Review"];

  let app = $derived(apps.find((a) => a.id === appId));
  let entrypoint = $derived(
    app?.entrypoints?.find((e) => e.id === entrypointId) || app?.entrypoints?.[0]
  );

  function reset() {
    step = 0;
    appId = "";
    entrypointId = "";
    workspaceRef = "";
    inputValues = {};
    parameterValues = {};
    error = "";
  }

  function choose(a: Application, e: Entrypoint) {
    appId = a.id;
    entrypointId = e.id;
    workspaceRef = "";
    inputValues = {};
    parameterValues = {};
    for (const [name, schema] of inputFields(e)) {
      inputValues[name] = "";
    }
    for (const p of e.parameters || []) {
      parameterValues[p.name] = paramDefault(p);
    }
    step = 1;
  }

  function inputFields(e?: Entrypoint): Array<[string, any]> {
    const schema = e?.inputs?.schema || (e?.inputs as any) || {};
    const props = schema.properties || schema.schema || schema;
    if (!props || typeof props !== "object" || Array.isArray(props)) return [];
    return Object.entries(props).filter(
      ([k]) => !["type", "required", "additionalProperties", "description", "title"].includes(k)
    );
  }

  function requiredInputs(e?: Entrypoint): string[] {
    const schema = e?.inputs?.schema || (e?.inputs as any) || {};
    return Array.isArray(schema.required) ? schema.required.map(String) : [];
  }

  function paramDefault(p: any): string {
    const v = p.default;
    if (v === undefined || v === null) return "";
    return typeof v === "boolean" ? (v ? "true" : "false") : String(v);
  }

  function fieldKind(type?: string): "number" | "boolean" | "text" {
    if (type === "integer" || type === "int" || type === "number") return "number";
    if (type === "boolean" || type === "bool") return "boolean";
    return "text";
  }

  function cast(raw: string, type?: string): any {
    switch (fieldKind(type)) {
      case "number": {
        if (raw.trim() === "") return undefined;
        const n = Number(raw);
        return Number.isFinite(n) ? n : undefined;
      }
      case "boolean":
        return raw === "true" || raw === "1";
      default:
        return raw;
    }
  }

  let canNext = $derived(
    step === 0
      ? false
      : step === 1
        ? requiredInputs(entrypoint).every((n) => (inputValues[n] ?? "").trim() !== "")
        : true
  );

  function next() {
    if (step < 2) step++;
  }
  function back() {
    if (step > 0) step--;
  }

  async function launch() {
    if (!app || !entrypoint) return;
    launching = true;
    error = "";
    try {
      const inputs: Record<string, any> = {};
      for (const [name, schema] of inputFields(entrypoint)) {
        const v = cast(inputValues[name] ?? "", schema?.type);
        if (v !== undefined) inputs[name] = v;
      }
      const parameters: Record<string, any> = {};
      for (const p of entrypoint.parameters || []) {
        const v = cast(parameterValues[p.name] ?? "", p.type);
        if (v !== undefined) parameters[p.name] = v;
      }
      const workspace =
        entrypoint && typeof entrypoint.workspace === "object" && entrypoint.workspace?.kind !== "none"
          ? { kind: entrypoint.workspace?.kind, ...(workspaceRef.trim() ? { ref: workspaceRef.trim() } : {}) }
          : undefined;
      const result = await runEntrypoint(app.id, entrypoint.id, {
        inputs,
        parameters,
        workspace,
        connector: "web"
      });
      const runId = (result as any).app_run_id || (result as any).run_id;
      reset();
      open = false;
      onlaunched(runId);
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      launching = false;
    }
  }

  $effect(() => {
    if (open && appId === "") step = 0;
  });

  function workspaceKind(e?: Entrypoint): string {
    const w = e?.workspace;
    if (!w) return "none";
    if (typeof w === "string") return w;
    return w.kind || "none";
  }
</script>

<Modal
  bind:open
  title={step === 0 ? "Run an agent" : `Run · ${app?.name || ""}`}
  width="720px"
  onclose={() => {
    reset();
    open = false;
  }}
>
  {#snippet footer()}
    {#if step > 0}
      <button class="btn ghost" onclick={back}>Back</button>
    {/if}
    <button
      class="btn ghost"
      onclick={() => {
        reset();
        open = false;
      }}>Cancel</button
    >
    {#if step < 2}
      <button class="btn primary" disabled={!canNext} onclick={next}>Continue</button>
    {:else}
      <button class="btn primary" disabled={launching} onclick={launch}>
        {launching ? "Running…" : "Run agent"}
      </button>
    {/if}
  {/snippet}

  {#if step > 0}
    <div class="steps">
      {#each steps as s, i}
        <div class="step" class:done={i < step} class:active={i === step}>
          <span class="num">{i < step ? "✓" : i + 1}</span>
          <span class="lbl">{s}</span>
        </div>
        {#if i < steps.length - 1}<span class="sep"></span>{/if}
      {/each}
    </div>
  {/if}

  {#if error}
    <div class="err">{error}</div>
  {/if}

  {#if step === 0}
    <div class="app-grid">
      {#each apps as a}
        {#each a.entrypoints || [] as e}
          <button class="app-card" onclick={() => choose(a, e)}>
            <div class="ic">{(a.entrypoints || []).length}</div>
            <div class="meta">
              <div class="nm">{a.name}</div>
              <div class="tg">{e.title || e.id}</div>
              <div class="row" style="gap:6px;margin-top:8px">
                <span class="pill">{e.pipeline || e.id}</span>
                <span class="pill">{humanize(workspaceKind(e))}</span>
              </div>
            </div>
          </button>
        {/each}
      {:else}
        <div class="empty">No applications installed. Add one from the Applications page.</div>
      {/each}
    </div>
  {:else if step === 1}
    <div class="cfg">
      <div class="field">
        <label>Application</label>
        <div class="readonly">{app?.name} · {entrypoint?.title || entrypoint?.id}</div>
      </div>

      {#if entrypoint?.description}
        <p class="lead">{entrypoint.description}</p>
      {/if}

      {#if workspaceKind(entrypoint) !== "none"}
        <div class="field">
          <label>Workspace {workspaceKind(entrypoint) === "dataset" ? "/ dataset" : "repository"}</label>
          <input
            bind:value={workspaceRef}
            placeholder="git URL or local path (resolved server-side)"
          />
          <div class="hint">Cloned and checked out by the backend — no git login needed here.</div>
        </div>
      {/if}

      {#if inputFields(entrypoint).length > 0}
        <div class="sub">Inputs</div>
        {#each inputFields(entrypoint) as [name, schema] (name)}
          <div class="field">
            <label>{name}{requiredInputs(entrypoint).includes(name) ? " *" : ""}</label>
            {#if fieldKind(schema?.type) === "number"}
              <input
                type="number"
                bind:value={inputValues[name]}
                placeholder={schema?.description || ""}
              />
            {:else if fieldKind(schema?.type) === "boolean"}
              <select bind:value={inputValues[name]}>
                <option value="false">false</option>
                <option value="true">true</option>
              </select>
            {:else}
              <input bind:value={inputValues[name]} placeholder={schema?.description || ""} />
            {/if}
          </div>
        {/each}
      {/if}

      {#if (entrypoint?.parameters || []).length > 0}
        <div class="sub">Parameters</div>
        {#each entrypoint?.parameters || [] as p (p.name)}
          <div class="field">
            <label>{p.name}</label>
            {#if fieldKind(p.type) === "number"}
              <input type="number" bind:value={parameterValues[p.name]} />
            {:else if fieldKind(p.type) === "boolean"}
              <select bind:value={parameterValues[p.name]}>
                <option value="false">false</option>
                <option value="true">true</option>
              </select>
            {:else}
              <input bind:value={parameterValues[p.name]} placeholder={p.description || ""} />
            {/if}
            {#if p.description}
              <div class="hint">{p.description}</div>
            {/if}
          </div>
        {/each}
      {/if}
    </div>
  {:else}
    <div class="review">
      <div class="rv-card">
        <div>
          <div class="rv-name">{app?.name}</div>
          <div class="rv-sub">{entrypoint?.title || entrypoint?.id}</div>
        </div>
      </div>
      <div class="rv-rows">
        <div class="rv-row"><span>Pipeline</span><b>{entrypoint?.pipeline}</b></div>
        <div class="rv-row"><span>Workspace</span><b>{workspaceKind(entrypoint)}</b></div>
        {#if app?.requirements?.harnesses?.length}
          <div class="rv-row">
            <span>Harnesses</span>
            <b class="row" style="gap:6px">
              {#each app.requirements.harnesses as h}<HarnessBadge id={h} />{/each}
            </b>
          </div>
        {/if}
        {#if app?.io?.inputs?.length}
          <div class="rv-row">
            <span>Inputs</span>
            <b class="row" style="gap:6px">
              {#each app.io.inputs as b}<ConnectorChip id={b.connector || "manual"} size={20} />{/each}
            </b>
          </div>
        {/if}
        {#if app?.io?.outputs?.length}
          <div class="rv-row">
            <span>Outputs</span>
            <b class="row" style="gap:6px">
              {#each app.io.outputs as b}<span class="kv">{b.sink || "web"}</span>{/each}
            </b>
          </div>
        {/if}
      </div>
    </div>
  {/if}
</Modal>

<style>
  .steps {
    display: flex;
    align-items: center;
    gap: 8px;
    margin-bottom: 24px;
  }
  .step {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--mt-text-muted);
    font-size: 12px;
  }
  .step.active {
    color: var(--mt-text);
  }
  .step.done {
    color: var(--mt-accent);
  }
  .num {
    width: 20px;
    height: 20px;
    border-radius: 50%;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    border: 1px solid var(--mt-border-strong);
    font-size: 11px;
  }
  .step.active .num {
    border-color: var(--mt-accent);
    color: var(--mt-accent);
    background: var(--mt-accent-soft);
  }
  .step.done .num {
    border-color: var(--mt-accent);
    background: var(--mt-accent);
    color: #fff;
  }
  .sep {
    flex: 1;
    height: 1px;
    background: var(--mt-border-soft);
  }

  .app-grid {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 12px;
  }
  .app-card {
    text-align: left;
    display: flex;
    gap: 14px;
    padding: 16px;
    border-radius: var(--mt-radius);
    background: var(--mt-bg-elev-2);
    border: 1px solid var(--mt-border);
    transition: all 0.15s;
  }
  .app-card:hover {
    border-color: var(--mt-accent);
    transform: translateY(-1px);
  }
  .ic {
    width: 40px;
    height: 40px;
    border-radius: 10px;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 18px;
    font-weight: 600;
    background: var(--mt-accent-soft);
    color: var(--mt-accent);
    flex: none;
  }
  .nm {
    font-weight: 600;
    font-size: 14px;
  }
  .tg {
    color: var(--mt-text-dim);
    font-size: 12px;
    margin-top: 2px;
  }
  .pill {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: 999px;
    background: rgba(255, 255, 255, 0.05);
    color: var(--mt-text-dim);
    border: 1px solid var(--mt-border);
  }
  .empty {
    grid-column: 1 / -1;
    padding: 28px;
    text-align: center;
    color: var(--mt-text-muted);
  }

  .cfg {
  }
  .lead {
    color: var(--mt-text-dim);
    font-size: 13px;
    margin: 0 0 16px;
  }
  .field {
    margin-bottom: 16px;
  }
  .field > label {
    display: block;
    font-size: 12px;
    font-weight: 500;
    color: var(--mt-text-dim);
    margin-bottom: 6px;
  }
  .field input,
  .field select {
    width: 100%;
    background: var(--mt-bg);
    border: 1px solid var(--mt-border);
    color: var(--mt-text);
    padding: 9px 12px;
    border-radius: var(--mt-radius-sm);
    outline: none;
    transition: border 0.15s;
  }
  .field input:focus,
  .field select:focus {
    border-color: var(--mt-accent);
  }
  .readonly {
    padding: 9px 12px;
    background: var(--mt-bg);
    border: 1px solid var(--mt-border);
    border-radius: var(--mt-radius-sm);
    font-size: 13px;
  }
  .hint {
    font-size: 11px;
    color: var(--mt-text-muted);
    margin-top: 4px;
  }
  .sub {
    font-size: 12px;
    font-weight: 600;
    color: var(--mt-text-dim);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin: 18px 0 12px;
  }
  .err {
    padding: 10px 12px;
    background: var(--mt-red-soft);
    border: 1px solid rgba(248, 113, 113, 0.25);
    border-radius: var(--mt-radius-sm);
    font-size: 13px;
    color: #fca5a5;
    margin-bottom: 16px;
  }

  .review {
  }
  .rv-card {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 16px;
    background: var(--mt-bg-elev-2);
    border: 1px solid var(--mt-border);
    border-radius: var(--mt-radius);
    margin-bottom: 20px;
  }
  .rv-name {
    font-weight: 600;
    font-size: 15px;
  }
  .rv-sub {
    color: var(--mt-text-dim);
    font-size: 12px;
    margin-top: 2px;
  }
  .rv-rows {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .rv-row {
    display: flex;
    justify-content: space-between;
    align-items: center;
    font-size: 13px;
    gap: 16px;
  }
  .rv-row > span {
    color: var(--mt-text-muted);
  }
  .rv-row > b {
    font-weight: 500;
    text-align: right;
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    justify-content: flex-end;
  }
  .kv {
    font-size: 11px;
    padding: 2px 8px;
    border-radius: var(--mt-radius-sm);
    background: var(--mt-bg);
    border: 1px solid var(--mt-border);
    color: var(--mt-text-dim);
  }

  .btn {
    padding: 8px 16px;
    border-radius: var(--mt-radius-sm);
    font-size: 13px;
    font-weight: 500;
    border: 1px solid transparent;
    transition: all 0.15s;
  }
  .btn.ghost {
    color: var(--mt-text-dim);
    border-color: var(--mt-border);
  }
  .btn.ghost:hover {
    background: var(--mt-bg-hover);
    color: var(--mt-text);
  }
  .btn.primary {
    background: var(--mt-accent);
    color: #fff;
  }
  .btn.primary:hover:not(:disabled) {
    background: var(--mt-accent-hover);
  }
  .btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
</style>
