<script lang="ts">
  import { onMount } from "svelte";

  type JsonMap = Record<string, any>;
  type ThemeMode = "light" | "dark";
  type SurfaceKey = "run" | "build" | "fleet";

  type WorkspaceDecl = { kind?: string; ref?: string; refspec?: string } | string | undefined;

  type Parameter = {
    name: string;
    type?: string;
    default?: unknown;
    description?: string;
  };

  type Entrypoint = {
    id: string;
    pipeline?: string;
    title?: string;
    description?: string;
    workspace?: WorkspaceDecl;
    inputs?: JsonMap;
    parameters?: Parameter[];
    defaults?: JsonMap;
  };

  type Application = {
    id: string;
    app_id?: string;
    name: string;
    version?: string;
    description?: string;
    authors?: string[];
    license?: string;
    entrypoints?: Entrypoint[];
    pipelines?: JsonMap[];
    capabilities?: JsonMap[];
    io?: JsonMap;
    services?: JsonMap;
    requirements?: JsonMap;
  };

  type RunSummary = {
    run_id: string;
    provider?: string;
    task?: string;
    status?: string;
    live?: boolean;
    events?: number;
    approvals?: number;
    created_at_ms?: number;
    last_event_ms?: number;
  };

  type TimelineRow = {
    seq: number | string;
    kind: string;
    type: string;
    label: string;
    detail: string;
    status: string;
    event: JsonMap;
  };

  type VerificationRow = {
    seq: number | string;
    phase: string;
    tier: string;
    verdict: string;
    reason: string;
    failedHooks: string;
    criteria: string;
  };

  const surfaces: Array<{ key: SurfaceKey; label: string; short: string; deck: string }> = [
    {
      key: "run",
      label: "Run & Watch",
      short: "RW",
      deck: "Run an application entrypoint and follow the pipeline fold."
    },
    {
      key: "build",
      label: "Build & Test",
      short: "BT",
      deck: "Mirror the local authoring contract, harness checks, and publish step."
    },
    {
      key: "fleet",
      label: "Fleet",
      short: "FL",
      deck: "Operate applications, I/O bindings, services, and task lifecycle."
    }
  ];

  let activeSurface: SurfaceKey = routeFromHash();
  let activeTheme: ThemeMode = (localStorage.getItem("mitos-theme") as ThemeMode) || "light";
  let search = "";
  let loading = true;
  let error = "";
  let toast = "";

  let apps: Application[] = [];
  let runs: RunSummary[] = [];
  let appTotals: JsonMap = {};
  let activationPlans: Record<string, JsonMap> = {};

  let selectedAppId = "";
  let selectedEntrypointId = "";
  let selectedRunId = "";
  let selectedRunDetail: JsonMap | null = null;
  let selectedTimelineSeq: number | string | null = null;
  let workspaceRef = "";
  let inputValues: Record<string, string> = {};
  let parameterValues: Record<string, string> = {};
  let promptText = "";
  let selectedOption = "1";
  let appSource = "";
  let packageTag = "";

  $: surface = surfaces.find((item) => item.key === activeSurface) || surfaces[0];
  $: filteredApps = apps.filter((app) =>
    matchesQuery([app.name, app.id, app.description, app.version, app.app_id], search)
  );
  $: appRuns = runs.filter(isAppRunSummary);
  $: filteredRuns = appRuns.filter((run) =>
    matchesQuery([run.run_id, run.task, run.provider, run.status], search)
  );
  $: selectedApp = apps.find((app) => app.id === selectedAppId) || apps[0] || null;
  $: selectedEntrypoint =
    selectedApp?.entrypoints?.find((entrypoint) => entrypoint.id === selectedEntrypointId) ||
    selectedApp?.entrypoints?.[0] ||
    null;
  $: selectedRunSummary = appRuns.find((run) => run.run_id === selectedRunId) || appRuns[0] || null;
  $: selectedAppPipelines = selectedApp?.pipelines || [];
  $: selectedAppCapabilities = selectedApp?.capabilities || [];
  $: runEvents = normalizeEvents(selectedRunDetail?.events || []);
  $: timelineRows = runEvents.map(timelineRowFor);
  $: selectedProof = selectedRunDetail?.proof || {};
  $: proofDelivery = selectedProof.delivery || {};
  $: proofResult = proofDelivery.result || {};
  $: proofArtifacts = proofArtifactsFor(selectedProof);
  $: proofTimeline = Array.isArray(selectedProof.timeline) ? selectedProof.timeline : [];
  $: proofUrl = selectedRunDetail?.run_id ? `/runs/${encodeURIComponent(selectedRunDetail.run_id)}/proof.html` : "";
  $: verificationRows = runEvents.filter(isVerificationEvent).map(verificationRowFor);
  $: selectedTimelineRow =
    timelineRows.find((row) => row.seq === selectedTimelineSeq) ||
    timelineRows.find((row) => row.type === "capability_started") ||
    timelineRows[0] ||
    null;
  $: innerTraceRows = innerTraceFor(runEvents, selectedTimelineRow);
  $: selectedActivation = selectedApp ? activationPlans[selectedApp.id] : null;
  $: ioActivity = timelineRows.filter((row) =>
    ["signal_received", "delivery_sent", "delivery_skipped", "egress_attempted", "egress_allowed", "egress_denied"].includes(row.type)
  );
  $: dashboard = {
    apps: appTotals.apps || apps.length,
    entrypoints: apps.reduce((count, app) => count + (app.entrypoints?.length || 0), 0),
    runs: appRuns.length,
    running: appRuns.filter((run) => run.live || run.status === "running").length,
    blocked: appRuns.filter((run) => ["failed", "error", "interrupted"].includes(run.status || "")).length
  };
  $: document.documentElement.dataset.theme = activeTheme;

  onMount(() => {
    document.documentElement.dataset.theme = activeTheme;
    window.addEventListener("hashchange", syncRoute);
    void refreshAll();

    return () => window.removeEventListener("hashchange", syncRoute);
  });

  function routeFromHash(): SurfaceKey {
    const key = window.location.hash.replace(/^#\/?/, "") || "run";
    return surfaces.some((item) => item.key === key) ? (key as SurfaceKey) : "run";
  }

  function syncRoute() {
    activeSurface = routeFromHash();
  }

  function navigate(key: SurfaceKey) {
    activeSurface = key;
    window.location.hash = key === "run" ? "#/" : `#/${key}`;
  }

  async function requestJson<T = JsonMap>(pathName: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers || {});
    if (!headers.has("content-type") && init.body) {
      headers.set("content-type", "application/json");
    }

    const response = await fetch(pathName, { cache: "no-store", ...init, headers });
    const raw = await response.text();
    const parsed = raw ? JSON.parse(raw) : {};

    if (!response.ok) {
      throw new Error(parsed.error || parsed.detail || `${response.status} ${response.statusText}`);
    }

    return parsed as T;
  }

  async function refreshAll() {
    loading = true;
    error = "";

    const results = await Promise.allSettled([
      loadApps(),
      loadRuns(),
    ]);

    const failed = results.find((result) => result.status === "rejected") as PromiseRejectedResult | undefined;
    if (failed) {
      error = failed.reason instanceof Error ? failed.reason.message : String(failed.reason);
    }

    const selectableRuns = runs.filter(isAppRunSummary);
    if (!selectedRunId || !selectableRuns.some((run) => run.run_id === selectedRunId)) {
      selectedRunId = selectableRuns[0]?.run_id || "";
    }
    if (selectedRunId) await loadRunDetail(selectedRunId);
    if (!selectedRunId) selectedRunDetail = null;
    loading = false;
  }

  async function loadApps() {
    const data = await requestJson<JsonMap>("/apps");
    apps = data.apps || [];
    appTotals = data.totals || {};

    if (!selectedAppId && apps[0]) {
      chooseApp(apps[0], false);
    }
  }

  async function loadRuns() {
    const data = await requestJson<{ runs: RunSummary[] }>("/runs");
    runs = data.runs || [];
  }

  async function loadRunDetail(runId: string) {
    if (!runId) return;

    try {
      selectedRunDetail = await requestJson<JsonMap>(`/runs/${encodeURIComponent(runId)}`);
      selectedTimelineSeq = null;
    } catch (err) {
      selectedRunDetail = null;
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function openRun(runId: string) {
    selectedRunId = runId;
    await loadRunDetail(runId);
  }

  function chooseApp(app: Application, resetInput = true) {
    selectedAppId = app.id;
    selectedEntrypointId = app.entrypoints?.[0]?.id || "";
    packageTag = app.name || app.id || "";

    if (resetInput) {
      resetRunForm(app.entrypoints?.[0] || null);
    }
  }

  function chooseEntrypoint(entrypoint: Entrypoint) {
    selectedEntrypointId = entrypoint.id;
    resetRunForm(entrypoint);
  }

  async function runEntrypoint() {
    if (!selectedApp || !selectedEntrypoint) return;

    try {
      const inputs: JsonMap = {};
      for (const [name, schema] of entrypointInputFields(selectedEntrypoint)) {
        const value = castFieldValue(inputValues[name] ?? "", schema?.type);
        if (value !== undefined) inputs[name] = value;
      }

      const parameters: JsonMap = {};
      for (const param of selectedEntrypoint.parameters || []) {
        const value = castFieldValue(parameterValues[param.name] ?? "", param.type);
        if (value !== undefined) parameters[param.name] = value;
      }

      const workspace = workspacePayload(selectedEntrypoint, workspaceRef);

      const result = await requestJson<JsonMap>(
        `/apps/${encodeURIComponent(selectedApp.id)}/entrypoints/${encodeURIComponent(selectedEntrypoint.id)}/runs`,
        {
          method: "POST",
          body: JSON.stringify({
            inputs,
            parameters,
            workspace,
            connector: "web",
            reply_ref: "web"
          })
        }
      );

      const runId = result.app_run_id || result.pipeline?.execution_id || result.run_id;
      toast = `Run ${runId || result.task_id || "accepted"}`;
      await refreshAll();
      if (runId) await openRun(runId);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function resolveActivation(app = selectedApp) {
    if (!app) return;

    try {
      const data = await requestJson<JsonMap>(`/apps/${encodeURIComponent(app.id)}/activations`, {
        method: "POST",
        body: JSON.stringify({})
      });
      activationPlans = { ...activationPlans, [app.id]: data.activation };
      toast = `Active set resolved for ${app.name || app.id}`;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function publishApp(app = selectedApp) {
    if (!app) return;

    try {
      const data = await requestJson<JsonMap>(`/apps/${encodeURIComponent(app.id)}/packages`, {
        method: "POST",
        body: JSON.stringify({ tag: packageTag || app.name || app.id })
      });
      toast = `Package ${data.package?.package_id || data.package?.digest || "created"}`;
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function redriveDeliveries() {
    const runId = selectedRunDetail?.run_id || selectedRunSummary?.run_id;
    if (!runId) return;

    try {
      const data = await requestJson<JsonMap>(`/app-runs/${encodeURIComponent(runId)}/deliveries/redrive`, {
        method: "POST"
      });
      toast = `Delivery redrive: ${data.redrive?.new_deliveries || 0} new`;
      await loadRunDetail(runId);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function steer(interaction: string) {
    const runId = selectedRunSummary?.run_id;
    if (!runId) return;

    try {
      const option = Number(selectedOption);
      await requestJson<JsonMap>(`/sessions/${encodeURIComponent(runId)}/steer`, {
        method: "POST",
        body: JSON.stringify({
          interaction,
          text: promptText || undefined,
          option: interaction === "select_option" && Number.isFinite(option) ? option : undefined
        })
      });
      toast = `${displayStatus(interaction)} sent`;
      if (interaction !== "select_option") promptText = "";
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function refreshRegistryFromSource() {
    const source = appSource.trim();

    if (!source) {
      toast = "Application registry refreshed";
      await refreshAll();
      return;
    }

    try {
      const data = await requestJson<JsonMap>("/apps", {
        method: "POST",
        body: JSON.stringify({ source })
      });
      const installId = data.install?.install_id || data.install?.app?.id;
      toast = `Application ${installId || "installed"}`;
      appSource = "";
      await refreshAll();

      const installed = installId ? apps.find((app) => app.id === installId) : null;
      if (installed) chooseApp(installed);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  function setTheme(mode: ThemeMode) {
    activeTheme = mode;
    localStorage.setItem("mitos-theme", mode);
    document.documentElement.dataset.theme = mode;
  }

  function inputValue(event: Event) {
    return (event.currentTarget as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement).value;
  }

  function entrypointWorkspaceKind(entrypoint: Entrypoint | null | undefined): string {
    const workspace = entrypoint?.workspace;
    if (!workspace) return "none";
    if (typeof workspace === "string") return workspace;
    return workspace.kind || "none";
  }

  function workspacePayload(entrypoint: Entrypoint, ref: string): JsonMap | undefined {
    const kind = entrypointWorkspaceKind(entrypoint);
    if (kind === "none") return undefined;
    const payload: JsonMap = { kind };
    if (ref.trim()) payload.ref = ref.trim();
    return payload;
  }

  function entrypointInputFields(entrypoint: Entrypoint | null | undefined): Array<[string, JsonMap]> {
    const schema = entrypoint?.inputs?.schema || entrypoint?.inputs || {};
    const properties = schema.properties || schema.schema || schema;
    if (!properties || typeof properties !== "object" || Array.isArray(properties)) return [];
    return Object.entries(properties).filter(
      ([key]) => !["type", "required", "additionalProperties", "description", "title"].includes(key)
    );
  }

  function requiredInputs(entrypoint: Entrypoint | null | undefined): string[] {
    const schema = entrypoint?.inputs?.schema || entrypoint?.inputs || {};
    const required = schema.required;
    return Array.isArray(required) ? required.map(String) : [];
  }

  function fieldKind(type: string | undefined): "number" | "boolean" | "text" {
    if (type === "integer" || type === "int" || type === "number") return "number";
    if (type === "boolean" || type === "bool") return "boolean";
    return "text";
  }

  function castFieldValue(raw: string, type: string | undefined): unknown {
    switch (fieldKind(type)) {
      case "number": {
        if (raw.trim() === "") return undefined;
        const value = Number(raw);
        return Number.isFinite(value) ? value : undefined;
      }
      case "boolean":
        return raw === "true" || raw === "1";
      default:
        return raw;
    }
  }

  function parameterDefault(param: Parameter): string {
    const value = param.default;
    if (value === undefined || value === null) return "";
    return typeof value === "boolean" ? (value ? "true" : "false") : String(value);
  }

  function resetRunForm(entrypoint: Entrypoint | null) {
    workspaceRef = "";
    const inputs: Record<string, string> = {};

    for (const [name, schema] of entrypointInputFields(entrypoint)) {
      inputs[name] = "";
    }

    inputValues = inputs;

    const parameters: Record<string, string> = {};
    for (const param of entrypoint?.parameters || []) {
      parameters[param.name] = parameterDefault(param);
    }
    parameterValues = parameters;
  }

  function normalizeEvents(events: JsonMap[]) {
    return events.map((record) => record.event || record);
  }

  function timelineRowFor(event: JsonMap): TimelineRow {
    const kind = event.kind || (event.event ? "event" : "event");
    const type = event.type || event.event_type || "event";
    const payload = event.payload || event.params || event;
    const seq = event.seq ?? event.sequence ?? "?";
    const status = eventStatus(type, payload);
    const label = labelForEvent(event, payload);
    const detail = detailForEvent(event, payload);

    return { seq, kind, type, label, detail, status, event };
  }

  function labelForEvent(event: JsonMap, payload: JsonMap) {
    if (event.kind === "decision") return `${humanize(event.type)} decision`;
    if (event.type === "task_status_changed") return `${payload.task_id || payload.id || "Task"} ${displayStatus(payload.status)}`;
    if (event.type === "task_enqueued") return `${payload.id || payload.task_id || "Task"} queued`;
    if (event.type === "capability_started") return `${payload.capability_id || payload.capability_ref || "Capability"} started`;
    if (event.type === "capability_finished") return `${payload.capability_id || payload.capability_ref || "Capability"} finished`;
    if (event.type === "signal_received") return `${payload.connector || "Connector"} signal`;
    if (event.type === "delivery_sent") return `${payload.sink || "Sink"} delivery`;
    if (event.type === "verification_recorded") return "Verification recorded";
    return humanize(event.type || event.event_type || "event");
  }

  function detailForEvent(event: JsonMap, payload: JsonMap) {
    const detail =
      event.rationale ||
      payload.reason ||
      payload.status ||
      payload.pipeline_id ||
      payload.pipeline ||
      payload.entrypoint ||
      payload.summary ||
      event.detail ||
      "";

    return typeof detail === "string" ? detail : JSON.stringify(detail);
  }

  function eventStatus(type: string, payload: JsonMap) {
    if (type === "verification_recorded") return statusClass(payload.verdict || payload.status);
    if (type.includes("failed") || type.includes("denied") || payload.status === "failed") return "danger";
    if (type.includes("approval") || type.includes("parked") || payload.status === "parked") return "warning";
    if (type.includes("finished") || type.includes("done") || type.includes("sent") || payload.status === "done") return "success";
    if (type.includes("started") || type.includes("running") || payload.status === "running") return "info";
    return "neutral";
  }

  function innerTraceFor(events: JsonMap[], row: TimelineRow | null) {
    if (!row) return [];
    const seq = row.event.seq ?? row.event.sequence;
    const caused = events.filter((event) => event.caused_by === seq);
    const sidecar = events.filter((event) =>
      ["assistant_text", "tool_use", "tool_result", "token_usage", "status", "terminal_snapshot"].includes(
        event.type || event.event_type
      )
    );

    return caused.length ? caused : sidecar.length ? sidecar : [row.event];
  }

  function proofArtifactsFor(proof: JsonMap) {
    const artifacts = proof?.artifacts;
    if (Array.isArray(artifacts)) return artifacts;

    const delivered = proof?.delivery?.artifacts;
    return Array.isArray(delivered) ? delivered : [];
  }

  function proofResultSummary(result: JsonMap) {
    if (!result || typeof result !== "object") return "No structured result recorded";
    return result.summary || result.signal || result.path || result.status || "Structured result recorded";
  }

  function isVerificationEvent(event: JsonMap) {
    return (event.type || event.event_type) === "verification_recorded";
  }

  function verificationRowFor(event: JsonMap): VerificationRow {
    const payload = event.payload || {};
    const ref = event.ref || payload.ref || {};
    const failedHooks = listText(payload.failed_hooks || payload.failures || payload.errors);
    const criteria = listText(payload.success_criteria || payload.criteria || payload.expectations);
    const reason = listText(payload.reason || payload.summary || payload.detail) || failedHooks || criteria;

    return {
      seq: event.seq ?? event.sequence ?? "?",
      phase: ref.phase || payload.phase || "post",
      tier: payload.tier || ref.tier || "verification",
      verdict: payload.verdict || payload.status || payload.result || "recorded",
      reason,
      failedHooks,
      criteria
    };
  }

  function listText(value: unknown) {
    if (!value) return "";
    if (Array.isArray(value)) return value.map(listText).filter(Boolean).join(", ");
    if (typeof value === "object") return JSON.stringify(value);
    return String(value);
  }

  function isAppRunSummary(run: RunSummary) {
    return run.provider === "app" || run.run_id?.startsWith("app-");
  }

  function activationUnits(plan: JsonMap | null) {
    const active = plan?.services?.active_set;
    if (Array.isArray(active)) return active;
    if (active && typeof active === "object") return Object.values(active);
    return [];
  }

  function ioBindings(app: Application | null, side: "inputs" | "outputs") {
    const values = app?.io?.[side];
    return Array.isArray(values) ? values : [];
  }

  function requirementList(app: Application | null, key: string) {
    const values = app?.requirements?.[key];
    return Array.isArray(values) ? values : values ? [values] : [];
  }

  function statusClass(status?: string) {
    const value = (status || "").toLowerCase();
    if (["completed", "done", "ready", "success", "verified", "passed", "pass"].includes(value)) return "success";
    if (["running", "scheduled", "queued", "info"].includes(value)) return "info";
    if (["parked", "warning", "needs setup", "needs_env"].includes(value)) return "warning";
    if (["failed", "blocked", "cancelled", "danger", "error", "unverified", "rejected"].includes(value)) return "danger";
    return "neutral";
  }

  function matchesQuery(values: Array<string | number | boolean | undefined | null>, query: string) {
    const needle = query.trim().toLowerCase();
    if (!needle) return true;
    return values.some((value) => String(value || "").toLowerCase().includes(needle));
  }

  function formatDate(value?: string | number) {
    if (!value) return "n/a";
    const date = typeof value === "number" ? new Date(value) : new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return date.toLocaleString();
  }

  function compactId(value?: string) {
    if (!value) return "n/a";
    return value.length > 24 ? `${value.slice(0, 11)}...${value.slice(-8)}` : value;
  }

  function humanize(value?: string) {
    return (value || "")
      .replace(/[-_]/g, " ")
      .replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  function displayStatus(value?: string) {
    return humanize(value || "unknown");
  }

  function jsonPreview(value: unknown) {
    return JSON.stringify(value || {}, null, 2);
  }
</script>

<svelte:head>
  <title>Mitos</title>
</svelte:head>

<div class="app-shell">
  <aside class="sidebar" aria-label="Primary navigation">
    <div class="brand">
      <div class="brand-mark">MT</div>
      <div>
        <strong>Mitos</strong>
        <span>Agentic applications</span>
      </div>
    </div>

    <nav class="nav-list" aria-label="Surfaces">
      {#each surfaces as item}
        <button
          class:active={activeSurface === item.key}
          type="button"
          aria-current={activeSurface === item.key ? "page" : undefined}
          onclick={() => navigate(item.key)}
        >
          <span>{item.short}</span>
          <strong>{item.label}</strong>
        </button>
      {/each}
    </nav>

    <div class="sidebar-meter">
      <span>Local runner</span>
      <strong>{dashboard.running > 0 ? "Active" : "Idle"}</strong>
      <small>{dashboard.apps} apps / {dashboard.runs} app runs</small>
    </div>
  </aside>

  <main class="workspace">
    <header class="topbar">
      <div>
        <span class="eyebrow">{surface.label}</span>
        <h1>{surface.deck}</h1>
      </div>
      <div class="topbar-actions">
        <label class="search-box">
          <span>Search</span>
          <input
            type="search"
            placeholder="Apps, runs, pipelines"
            value={search}
            oninput={(event) => (search = inputValue(event))}
          />
        </label>
        <button class="icon-button" type="button" title="Refresh" aria-label="Refresh" onclick={refreshAll} disabled={loading}>
          {loading ? "..." : "R"}
        </button>
        <button
          class="icon-button"
          type="button"
          title="Toggle theme"
          aria-label="Toggle theme"
          onclick={() => setTheme(activeTheme === "light" ? "dark" : "light")}
        >
          {activeTheme === "light" ? "D" : "L"}
        </button>
      </div>
    </header>

    {#if error}
      <div class="notice danger" role="alert">
        <strong>Request failed</strong>
        <span>{error}</span>
        <button type="button" onclick={() => (error = "")}>Dismiss</button>
      </div>
    {/if}

    {#if toast}
      <div class="notice success" role="status">
        <strong>{toast}</strong>
        <button type="button" onclick={() => (toast = "")}>Dismiss</button>
      </div>
    {/if}

    <section class="metric-strip" aria-label="Runtime summary">
      <div>
        <span>Applications</span>
        <strong>{dashboard.apps}</strong>
      </div>
      <div>
        <span>Entrypoints</span>
        <strong>{dashboard.entrypoints}</strong>
      </div>
      <div>
        <span>Runs</span>
        <strong>{dashboard.runs}</strong>
      </div>
      <div>
        <span>Run health</span>
        <strong>{dashboard.blocked ? `${dashboard.blocked} blocked` : "clear"}</strong>
      </div>
    </section>

    {#if activeSurface === "run"}
      <section class="surface-grid run-grid">
        <div class="panel app-picker">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Applications</span>
              <h2>Pick an app</h2>
            </div>
          </div>

          <form class="add-source" onsubmit={(event) => { event.preventDefault(); void refreshRegistryFromSource(); }}>
            <input
              aria-label="Application source"
              placeholder="git URL or local app directory"
              value={appSource}
              oninput={(event) => (appSource = inputValue(event))}
            />
            <button type="submit">Refresh</button>
          </form>

          <div class="list-stack">
            {#each filteredApps as app}
              <button class:active={selectedApp?.id === app.id} class="app-card" type="button" onclick={() => chooseApp(app)}>
                <div>
                  <strong>{app.name || app.id}</strong>
                  <span>{app.version || "local"} / {app.entrypoints?.length || 0} entrypoints</span>
                </div>
                <span class="badge neutral">{app.pipelines?.length || 0} pipelines</span>
              </button>
            {:else}
              <div class="empty-state">
                <strong>No applications installed</strong>
                <span>Local manifests from the application registry will appear here.</span>
              </div>
            {/each}
          </div>
        </div>

        <div class="panel launch-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Run</span>
              <h2>{selectedApp?.name || "Application entrypoint"}</h2>
            </div>
            {#if selectedApp}
              <span class="badge info">{compactId(selectedApp.app_id || selectedApp.id)}</span>
            {/if}
          </div>

          {#if selectedApp}
            <p class="muted">{selectedApp.description || "An agentic application bundle with runnable entrypoints."}</p>

            <div class="entrypoint-tabs" role="tablist" aria-label="Entrypoints">
              {#each selectedApp.entrypoints || [] as entrypoint}
                <button
                  class:active={selectedEntrypoint?.id === entrypoint.id}
                  type="button"
                  onclick={() => chooseEntrypoint(entrypoint)}
                >
                  {entrypoint.title || entrypoint.id}
                </button>
              {:else}
                <span class="muted">No entrypoints declared.</span>
              {/each}
            </div>

            <div class="entrypoint-detail">
              <div>
                <span>Pipeline</span>
                <strong>{selectedEntrypoint?.pipeline || "n/a"}</strong>
              </div>
              <div>
                <span>Workspace</span>
                <strong>{entrypointWorkspaceKind(selectedEntrypoint)}</strong>
              </div>
              <div>
                <span>Budget</span>
                <strong>{selectedEntrypoint?.defaults?.budget ? jsonPreview(selectedEntrypoint.defaults.budget) : "tenant default"}</strong>
              </div>
            </div>

            <div class="run-form">
              {#if entrypointWorkspaceKind(selectedEntrypoint) !== "none"}
                <label class="json-field">
                  <span>Repository {entrypointWorkspaceKind(selectedEntrypoint) === "dataset" ? "/ dataset" : ""}</span>
                  <input
                    type="text"
                    placeholder="git URL or local path (resolved server-side)"
                    value={workspaceRef}
                    oninput={(event) => (workspaceRef = inputValue(event))}
                  />
                  <small class="muted">Cloned and checked out by the backend — no git login needed in the browser.</small>
                </label>
              {/if}

              {#if entrypointInputFields(selectedEntrypoint).length > 0}
                <div class="form-section">
                  <span class="eyebrow">Inputs</span>
                  {#each entrypointInputFields(selectedEntrypoint) as [name, schema] (name)}
                    <label class="json-field">
                      <span>{name}{requiredInputs(selectedEntrypoint).includes(name) ? " *" : ""}</span>
                      {#if fieldKind(schema?.type) === "number"}
                        <input
                          type="number"
                          value={inputValues[name] ?? ""}
                          oninput={(event) => (inputValues = { ...inputValues, [name]: inputValue(event) })}
                        />
                      {:else if fieldKind(schema?.type) === "boolean"}
                        <select
                          value={inputValues[name] ?? "false"}
                          oninput={(event) => (inputValues = { ...inputValues, [name]: inputValue(event) })}
                        >
                          <option value="false">false</option>
                          <option value="true">true</option>
                        </select>
                      {:else}
                        <input
                          type="text"
                          placeholder={schema?.description || ""}
                          value={inputValues[name] ?? ""}
                          oninput={(event) => (inputValues = { ...inputValues, [name]: inputValue(event) })}
                        />
                      {/if}
                      {#if schema?.description}<small class="muted">{schema.description}</small>{/if}
                    </label>
                  {/each}
                </div>
              {/if}

              {#if (selectedEntrypoint?.parameters || []).length > 0}
                <div class="form-section">
                  <span class="eyebrow">Parameters</span>
                  {#each selectedEntrypoint?.parameters || [] as param (param.name)}
                    <label class="json-field">
                      <span>{param.name}</span>
                      {#if fieldKind(param.type) === "number"}
                        <input
                          type="number"
                          value={parameterValues[param.name] ?? ""}
                          oninput={(event) => (parameterValues = { ...parameterValues, [param.name]: inputValue(event) })}
                        />
                      {:else if fieldKind(param.type) === "boolean"}
                        <select
                          value={parameterValues[param.name] ?? "false"}
                          oninput={(event) => (parameterValues = { ...parameterValues, [param.name]: inputValue(event) })}
                        >
                          <option value="false">false</option>
                          <option value="true">true</option>
                        </select>
                      {:else}
                        <input
                          type="text"
                          placeholder={param.description || ""}
                          value={parameterValues[param.name] ?? ""}
                          oninput={(event) => (parameterValues = { ...parameterValues, [param.name]: inputValue(event) })}
                        />
                      {/if}
                      {#if param.description}
                        <small class="muted">{param.description}{param.default !== undefined && param.default !== "" ? ` (default: ${parameterDefault(param)})` : ""}</small>
                      {/if}
                    </label>
                  {/each}
                </div>
              {/if}
            </div>

            <div class="button-row">
              <button class="primary" type="button" onclick={runEntrypoint} disabled={!selectedEntrypoint}>Run entrypoint</button>
              <button type="button" onclick={() => selectedEntrypoint && resetRunForm(selectedEntrypoint)}>
                Reset
              </button>
            </div>
          {:else}
            <div class="empty-state">
              <strong>Add an application to run a pipeline.</strong>
              <span>The frontend expects the app manifest hierarchy from the application spec.</span>
            </div>
          {/if}
        </div>

        <div class="panel watch-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Watch</span>
              <h2>{selectedRunSummary?.task || selectedRunSummary?.run_id || "Pipeline timeline"}</h2>
            </div>
            <span class={`badge ${statusClass(selectedRunSummary?.status)}`}>{displayStatus(selectedRunSummary?.status)}</span>
          </div>

          <div class="watch-layout">
            <div class="run-rail">
              {#each filteredRuns.slice(0, 12) as run}
                <button class:active={selectedRunId === run.run_id} class="run-row" type="button" onclick={() => openRun(run.run_id)}>
                  <strong>{run.task || compactId(run.run_id)}</strong>
                  <span>{run.events || 0} events / {formatDate(run.last_event_ms || run.created_at_ms)}</span>
                </button>
              {:else}
                <div class="empty-state compact">
                  <strong>No runs</strong>
                  <span>Completed and live traces will appear here.</span>
                </div>
              {/each}
            </div>

            <div class="timeline">
              <div class="timeline-head">
                <strong>Outer pipeline timeline</strong>
                <span>{selectedRunDetail?.event_count || timelineRows.length} events</span>
              </div>
              {#each timelineRows as row}
                <button
                  class:active={selectedTimelineRow?.seq === row.seq}
                  class="timeline-row"
                  type="button"
                  onclick={() => (selectedTimelineSeq = row.seq)}
                >
                  <span class={`event-dot ${statusClass(row.status)}`}></span>
                  <span class="seq">{row.seq}</span>
                  <div>
                    <strong>{row.label}</strong>
                    <span>{row.kind} / {row.detail || row.type}</span>
                  </div>
                </button>
              {:else}
                <div class="empty-state compact">
                  <strong>No event log loaded</strong>
                  <span>Select a run or start an entrypoint.</span>
                </div>
              {/each}
            </div>

            <div class="inner-trace">
              <div class="timeline-head">
                <strong>Inner capability trace</strong>
                <span>{selectedTimelineRow?.type || "idle"}</span>
              </div>
              <div class="trace-actions">
                <button type="button" onclick={() => steer("approve")} disabled={!selectedRunSummary?.live}>Approve</button>
                <button type="button" onclick={() => steer("interrupt")} disabled={!selectedRunSummary?.live}>Stop</button>
              </div>
              <label class="prompt-box">
                <span>Steer prompt</span>
                <input value={promptText} oninput={(event) => (promptText = inputValue(event))} placeholder="Answer or instruction" />
              </label>
              <div class="trace-actions">
                <button type="button" onclick={() => steer("answer")} disabled={!selectedRunSummary?.live || !promptText.trim()}>Answer</button>
                <button type="button" onclick={() => steer("prompt")} disabled={!selectedRunSummary?.live || !promptText.trim()}>Prompt</button>
              </div>
              <label class="prompt-box">
                <span>Select option</span>
                <input value={selectedOption} inputmode="numeric" oninput={(event) => (selectedOption = inputValue(event))} />
              </label>
              <button type="button" onclick={() => steer("select_option")} disabled={!selectedRunSummary?.live || !selectedOption.trim()}>
                Select
              </button>
              <div class="trace-list">
                {#each innerTraceRows as event}
                  <pre>{jsonPreview(event)}</pre>
                {:else}
                  <span class="muted">No inner trace events for this selection.</span>
                {/each}
              </div>
            </div>
          </div>

          <div class="output-receipt">
            <div class="panel-head">
              <div>
                <span class="eyebrow">Output</span>
                <h2>Result and proof receipt</h2>
              </div>
              {#if proofUrl}
                <a class="text-link" href={proofUrl} target="_blank" rel="noreferrer">Proof page</a>
              {/if}
            </div>

            <div class="receipt-grid">
              <div>
                <span>Result</span>
                <strong>{proofResultSummary(proofResult)}</strong>
              </div>
              <div>
                <span>Effects</span>
                <strong>{selectedProof.effect_count || proofTimeline.length || 0}</strong>
              </div>
              <div>
                <span>Proof hash</span>
                <strong>{selectedProof.effect_log_sha256 || "pending"}</strong>
              </div>
            </div>

            <div class="receipt-split">
              <div>
                <h3>Artifacts</h3>
                <div class="artifact-list">
                  {#each proofArtifacts as artifact}
                    <div class="artifact-row">
                      <strong>{artifact.path || artifact.artifact || artifact.kind || "artifact"}</strong>
                      <span>{artifact.sha256 || artifact.bytes || artifact.missing || "recorded"}</span>
                    </div>
                  {:else}
                    <span class="muted">No artifacts recorded for this run.</span>
                  {/each}
                </div>
              </div>

              <div>
                <h3>Result payload</h3>
                <pre class="code-panel">{jsonPreview(proofResult)}</pre>
              </div>
            </div>
          </div>
        </div>
      </section>
    {:else if activeSurface === "build"}
      <section class="surface-grid build-grid">
        <div class="panel contract-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">CLI-first</span>
              <h2>Application contract</h2>
            </div>
          </div>

          <div class="contract-grid">
            <div>
              <h3>Developer provides</h3>
              <ul>
                <li>Anchored skill markdown</li>
                <li>Typed tools and optional data</li>
                <li>Manifest entrypoints and result schema</li>
                <li>Declared harness, trust tier, egress</li>
              </ul>
            </div>
            <div>
              <h3>Runtime guarantees</h3>
              <ul>
                <li>Injected harness</li>
                <li>Workspace RW and data snapshot RO</li>
                <li>Deny-all except broker egress</li>
                <li>Event log, harvest, proof delivery</li>
              </ul>
            </div>
          </div>
        </div>

        <div class="panel manifest-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Manifest mirror</span>
              <h2>{selectedApp?.name || "No selected app"}</h2>
            </div>
            {#if selectedApp}
              <button type="button" onclick={() => publishApp()}>Publish local OCI</button>
            {/if}
          </div>

          {#if selectedApp}
            <div class="publish-row">
              <label>
                <span>Package tag</span>
                <input value={packageTag} oninput={(event) => (packageTag = inputValue(event))} />
              </label>
              <span class="badge neutral">{selectedApp.license || "unlicensed"}</span>
            </div>

            <div class="hierarchy">
              <div>
                <span>Application</span>
                <strong>{selectedApp.name}</strong>
                <small>{compactId(selectedApp.app_id)}</small>
              </div>
              <div>
                <span>Pipelines</span>
                <strong>{selectedApp.pipelines?.length || 0}</strong>
                <small>{(selectedApp.pipelines || []).map((item) => item.id).join(", ") || "none"}</small>
              </div>
              <div>
                <span>Capabilities</span>
                <strong>{selectedApp.capabilities?.length || 0}</strong>
                <small>{(selectedApp.capabilities || []).map((item) => item.as || item.ref).join(", ") || "none"}</small>
              </div>
            </div>

            <div class="requirements">
              {#each ["harnesses", "connectors", "egress"] as key}
                <div>
                  <span>{humanize(key)}</span>
                  <strong>{requirementList(selectedApp, key).join(", ") || "not declared"}</strong>
                </div>
              {/each}
              <div>
                <span>Trust tier</span>
                <strong>{selectedApp.requirements?.trust_tier || "per capability"}</strong>
              </div>
            </div>

            <pre class="code-panel">{jsonPreview(selectedApp)}</pre>
          {:else}
            <div class="empty-state">
              <strong>No application manifest loaded</strong>
              <span>The build mirror activates once a local app is present.</span>
            </div>
          {/if}
        </div>

        <div class="panel harness-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Registry mirror</span>
              <h2>Harness declarations</h2>
            </div>
          </div>

          <div class="capability-grid">
            {#each requirementList(selectedApp, "harnesses") as harness}
              <div class="capability-card">
                <div>
                  <strong>{harness}</strong>
                  <span>{selectedApp?.name || "application"} compatible harness</span>
                </div>
                <span class="badge neutral">declared</span>
              </div>
            {:else}
              <div class="empty-state">
                <strong>No harness declarations</strong>
                <span>Select an application with declared runtime requirements.</span>
              </div>
            {/each}
          </div>

          <div class="pipeline-registry">
            <h3>Application pipelines</h3>
            {#each selectedAppPipelines as pipeline}
              <div class="registry-row">
                <strong>{pipeline.id}</strong>
                <span>{pipeline.file || "bundled markdown"} / {compactId(pipeline.sha256)}</span>
              </div>
            {:else}
              <span class="muted">No bundled application pipelines.</span>
            {/each}
          </div>

          <div class="pipeline-registry">
            <h3>Capability refs</h3>
            {#each selectedAppCapabilities as capability}
              <div class="registry-row">
                <strong>{capability.as || "capability"}</strong>
                <span>{capability.ref || "local ref"}</span>
              </div>
            {:else}
              <span class="muted">No capability refs declared.</span>
            {/each}
          </div>
        </div>

        <div class="panel verdict-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Judge trace</span>
              <h2>Test verdicts</h2>
            </div>
            <div class="panel-actions">
              {#if selectedApp && selectedEntrypoint}
                <button type="button" onclick={runEntrypoint}>Run entrypoint</button>
              {/if}
              {#if proofUrl}
                <a class="text-link" href={proofUrl} target="_blank" rel="noreferrer">Open proof</a>
              {/if}
            </div>
          </div>

          <div class="verdict-layout">
            <div class="test-run-list">
              <h3>Recent runs</h3>
              {#each filteredRuns.slice(0, 8) as run}
                <button class:active={selectedRunSummary?.run_id === run.run_id} class="test-run-row" type="button" onclick={() => openRun(run.run_id)}>
                  <div>
                    <strong>{compactId(run.run_id)}</strong>
                    <span>{run.task || run.provider || "application run"}</span>
                  </div>
                  <span class={`badge ${statusClass(run.status)}`}>{displayStatus(run.status)}</span>
                </button>
              {:else}
                <div class="empty-state compact">
                  <strong>No runs recorded</strong>
                  <span>Run history is empty.</span>
                </div>
              {/each}
            </div>

            <div class="verdict-detail">
              <div class="verdict-grid">
                <div>
                  <span>Selected run</span>
                  <strong>{compactId(selectedRunSummary?.run_id)}</strong>
                </div>
                <div>
                  <span>Run status</span>
                  <strong>{displayStatus(selectedRunSummary?.status)}</strong>
                </div>
                <div>
                  <span>Verdicts</span>
                  <strong>{verificationRows.length}</strong>
                </div>
                <div>
                  <span>Proof hash</span>
                  <strong>{selectedProof.effect_log_sha256 || "pending"}</strong>
                </div>
              </div>

              <div class="verdict-list">
                {#each verificationRows as row}
                  <div class="verdict-row">
                    <div class="verdict-row-head">
                      <span class={`event-dot ${statusClass(row.verdict)}`}></span>
                      <div>
                        <strong>{displayStatus(row.verdict)}</strong>
                        <span>{row.phase} / {row.tier} / seq {row.seq}</span>
                      </div>
                    </div>
                    {#if row.reason}
                      <p>{row.reason}</p>
                    {/if}
                    {#if row.failedHooks}
                      <small>Failed hooks: {row.failedHooks}</small>
                    {/if}
                    {#if row.criteria}
                      <small>Criteria: {row.criteria}</small>
                    {/if}
                  </div>
                {:else}
                  <div class="empty-state compact">
                    <strong>No verification event</strong>
                    <span>{selectedRunSummary ? "Selected run has no recorded verdict." : "No selected run."}</span>
                  </div>
                {/each}
              </div>
            </div>
          </div>
        </div>
      </section>
    {:else}
      <section class="surface-grid fleet-grid">
        <div class="panel fleet-apps">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Fleet</span>
              <h2>Applications doing work</h2>
            </div>
            {#if selectedApp}
              <button type="button" onclick={() => resolveActivation()}>Resolve active set</button>
            {/if}
          </div>

          <div class="fleet-list">
            {#each filteredApps as app}
              <button class:active={selectedApp?.id === app.id} class="fleet-row" type="button" onclick={() => chooseApp(app, false)}>
                <div>
                  <strong>{app.name || app.id}</strong>
                  <span>{ioBindings(app, "inputs").length} inputs / {ioBindings(app, "outputs").length} outputs</span>
                </div>
                <span class="badge neutral">{app.services?.requires?.length || 0} requires</span>
              </button>
            {:else}
              <div class="empty-state">
                <strong>No fleet applications</strong>
                <span>Installed applications will appear as operable units.</span>
              </div>
            {/each}
          </div>
        </div>

        <div class="panel io-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">I/O</span>
              <h2>{selectedApp?.name || "Bindings"}</h2>
            </div>
            <button type="button" onclick={redriveDeliveries} disabled={!selectedRunSummary}>Redrive sink</button>
          </div>

          <div class="binding-columns">
            <div>
              <h3>Inputs</h3>
              {#each ioBindings(selectedApp, "inputs") as binding}
                <div class="binding-row">
                  <strong>{binding.connector || "manual"}</strong>
                  <span>{binding.entrypoint || "entrypoint"} / {binding.path || "web"}</span>
                </div>
              {:else}
                <span class="muted">Manual web input only.</span>
              {/each}
            </div>
            <div>
              <h3>Outputs</h3>
              {#each ioBindings(selectedApp, "outputs") as binding}
                <div class="binding-row">
                  <strong>{binding.sink || "web"}</strong>
                  <span>{binding.on || "run.done"} / {binding.entrypoint || "all"}</span>
                </div>
              {:else}
                <span class="muted">Web receipt output only.</span>
              {/each}
            </div>
          </div>

          <div class="activity-list">
            <h3>Recent I/O activity</h3>
            {#each ioActivity as row}
              <div class="activity-row">
                <span class={`event-dot ${statusClass(row.status)}`}></span>
                <strong>{row.label}</strong>
                <span>{row.detail || row.type}</span>
              </div>
            {:else}
              <span class="muted">Select an app run with connector or delivery effects.</span>
            {/each}
          </div>
        </div>

        <div class="panel service-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Why is it on</span>
              <h2>Services and active set</h2>
            </div>
          </div>

          <div class="service-summary">
            <div>
              <span>Requires</span>
              <strong>{selectedActivation?.services?.requires?.join(", ") || selectedApp?.services?.requires?.join(", ") || "none"}</strong>
            </div>
            <div>
              <span>Wants</span>
              <strong>{selectedActivation?.services?.wants?.join(", ") || selectedApp?.services?.wants?.join(", ") || "none"}</strong>
            </div>
          </div>

          <div class="active-set">
            {#each activationUnits(selectedActivation) as unit}
              <div class="unit-row">
                <strong>{unit.id || unit.name || jsonPreview(unit)}</strong>
                <span>{unit.kind || unit.type || unit.reason || "active"}</span>
              </div>
            {:else}
              <div class="empty-state compact">
                <strong>No active-set projection</strong>
                <span>Resolve the selected app to inspect service dependencies.</span>
              </div>
            {/each}
          </div>
        </div>

        <div class="panel board-panel">
          <div class="panel-head">
            <div>
              <span class="eyebrow">Default app registry</span>
              <h2>Installed applications</h2>
            </div>
            <span class="badge neutral">{filteredApps.length} apps</span>
          </div>

          <div class="registry-grid">
            {#each filteredApps as app}
              <button class:active={selectedApp?.id === app.id} class="registry-app-row" type="button" onclick={() => chooseApp(app, false)}>
                <div>
                  <strong>{app.name || app.id}</strong>
                  <span>{app.description || app.app_id || "local application"}</span>
                </div>
                <div>
                  <span>Entrypoints</span>
                  <strong>{(app.entrypoints || []).map((entrypoint) => entrypoint.id).join(", ") || "none"}</strong>
                </div>
                <div>
                  <span>Pipelines</span>
                  <strong>{(app.pipelines || []).map((pipeline) => pipeline.id).join(", ") || "none"}</strong>
                </div>
                <div>
                  <span>Harnesses</span>
                  <strong>{requirementList(app, "harnesses").join(", ") || "not declared"}</strong>
                </div>
              </button>
            {:else}
              <div class="empty-state compact">
                <strong>No applications in registry</strong>
                <span>Add an app source or check the default registry directory.</span>
              </div>
            {/each}
          </div>
        </div>
      </section>
    {/if}
  </main>
</div>
