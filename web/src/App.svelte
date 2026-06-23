<script lang="ts">
  import { onMount } from "svelte";
  import { themeContract, themeSwatches, type ThemeMode } from "./theme";

  type JsonMap = Record<string, any>;
  type DrawerKind = "none" | "run" | "pipeline" | "capability" | "task" | "advanced";
  type ViewKey = "overview" | "workflows" | "runs" | "capabilities" | "work-pool" | "evidence" | "settings";

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

  type Pipeline = {
    id: string;
    title?: string;
    description?: string;
    provider?: string;
    pipeline_id?: string;
    workflow?: JsonMap;
    tasks?: JsonMap;
    executions?: JsonMap[];
    recent_runs?: string[];
    template?: string;
    cwd?: string;
    approvals?: string;
    transport_order?: string[];
    env_passthrough?: string[];
  };

  type Capability = {
    id: string;
    title?: string;
    description?: string;
    provider?: string;
    capability?: JsonMap;
    runtime?: JsonMap;
    setup?: JsonMap;
    files?: JsonMap[];
    env?: JsonMap;
    sandbox?: JsonMap;
    work?: JsonMap;
    template?: string;
    timeout_ms?: number;
  };

  type Task = {
    id: string;
    status?: string;
    pool?: string;
    pipeline?: string;
    source?: string;
    priority?: number;
    runs?: string[];
    payload?: JsonMap;
    status_reason?: string;
    created_at?: string;
    updated_at?: string;
  };

  type StepDraft = {
    id: string;
    name: string;
    capability_id: string;
  };

  type CapabilityDraft = {
    id: string;
    title: string;
    description: string;
    provider: string;
    cwd: string;
    template: string;
    timeout_ms: string;
    approvals: string;
    transport_order: string;
    env_passthrough: string;
    trust_tier: string;
    skill: string;
    result_json: string;
  };

  const navItems: Array<{ key: ViewKey; label: string; short: string }> = [
    { key: "overview", label: "Command", short: "CM" },
    { key: "workflows", label: "Workflows", short: "WF" },
    { key: "runs", label: "Runs", short: "RN" },
    { key: "capabilities", label: "Capabilities", short: "CP" },
    { key: "work-pool", label: "Work pool", short: "WP" },
    { key: "evidence", label: "Evidence", short: "EV" },
    { key: "settings", label: "Settings", short: "ST" }
  ];

  const statusOrder = ["blocked", "queued", "scheduled", "running", "parked", "done", "failed", "cancelled"];
  const defaultPipelineDraft = {
    id: "repo-review",
    title: "Repository review",
    description: "Agent workflow for a concrete repo task",
    provider: "fake",
    cwd: "",
    template: "Work on {{objective}}. Return {{expected_output}}.",
    env_passthrough: "",
    timeout_ms: "120000"
  };

  const defaultPipelineSteps: StepDraft[] = [
    { id: "inspect", name: "Inspect", capability_id: "repo-review" },
    { id: "summarize", name: "Summarize", capability_id: "repo-review" }
  ];

  const defaultCapabilityDraft: CapabilityDraft = {
    id: "repo-review",
    title: "Repository review",
    description: "Smallest runnable runtime wrapper",
    provider: "fake",
    cwd: "",
    template: "Work on {{objective}}. Return {{expected_output}}.",
    timeout_ms: "120000",
    approvals: "deny",
    transport_order: "stdio-json, pty",
    env_passthrough: "",
    trust_tier: "T1",
    skill: "",
    result_json: ""
  };

  let activeView: ViewKey = routeFromHash();
  let activeTheme: ThemeMode = (localStorage.getItem("mitos-theme") as ThemeMode) || "light";
  let search = "";
  let loading = true;
  let toast = "";
  let error = "";
  let drawerKind: DrawerKind = "none";
  let drawerTab = "summary";
  let selectedRunId = "";
  let selectedPipelineId = "";
  let selectedCapabilityId = "";
  let selectedTaskId = "";
  let selectedRunDetail: JsonMap | null = null;
  let runDetailLoading = false;
  let runInputJson = '{\n  "objective": "Run the smallest testable wrapper",\n  "expected_output": "Evidence and a concise result"\n}';
  let executionInputJson = '{\n  "objective": "Execute the workflow with traceable evidence",\n  "expected_output": "Step results and proof events"\n}';
  let pipelineDraft = { ...defaultPipelineDraft };
  let pipelineSteps: StepDraft[] = defaultPipelineSteps.map((step) => ({ ...step }));
  let capabilityDraft: CapabilityDraft = { ...defaultCapabilityDraft };

  let runs: RunSummary[] = [];
  let pipelines: Pipeline[] = [];
  let capabilities: Capability[] = [];
  let board: JsonMap = { statuses: statusOrder, columns: {}, tasks: [], totals: {} };
  let setupTotals: JsonMap = {};
  let pipelineTotals: JsonMap = {};

  $: filteredRuns = runs.filter((run) => matchesQuery([run.run_id, run.provider, run.task, run.status], search));
  $: filteredPipelines = pipelines.filter((pipeline) =>
    matchesQuery([pipeline.id, pipeline.title, pipeline.description, pipeline.provider], search)
  );
  $: filteredCapabilities = capabilities.filter((capability) =>
    matchesQuery([capability.id, capability.title, capability.description, capability.provider, capability.setup?.status], search)
  );
  $: selectedPipeline = pipelines.find((pipeline) => pipeline.id === selectedPipelineId) || pipelines[0] || null;
  $: selectedCapability = capabilities.find((capability) => capability.id === selectedCapabilityId) || capabilities[0] || null;
  $: selectedTask = (board.tasks || []).find((task: Task) => task.id === selectedTaskId) || null;
  $: workflowSteps = workflowStepsFor(selectedPipeline);
  $: selectedWorkflowSteps = workflowSteps.length > 0 ? workflowSteps : pipelineSteps;
  $: capabilityOptionIds = capabilities.map((capability) => capability.id).filter(Boolean);
  $: activeExecutions = selectedPipeline?.executions || [];
  $: boardStatuses = board.statuses || statusOrder;
  $: dashboardTotals = summarize(runs, pipelines, capabilities, board, setupTotals, pipelineTotals);
  $: document.documentElement.dataset.theme = activeTheme;

  onMount(() => {
    document.documentElement.dataset.theme = activeTheme;
    window.addEventListener("hashchange", syncRoute);
    void refreshAll();

    return () => window.removeEventListener("hashchange", syncRoute);
  });

  function routeFromHash(): ViewKey {
    const key = window.location.hash.replace(/^#\/?/, "") || "overview";
    return navItems.some((item) => item.key === key) ? (key as ViewKey) : "overview";
  }

  function syncRoute() {
    activeView = routeFromHash();
  }

  function navigate(view: ViewKey) {
    activeView = view;
    window.location.hash = view === "overview" ? "#/" : `#/${view}`;
  }

  async function requestJson<T = JsonMap>(pathName: string, init: RequestInit = {}): Promise<T> {
    const headers = new Headers(init.headers || {});
    if (!headers.has("content-type") && init.body) {
      headers.set("content-type", "application/json");
    }

    const response = await fetch(pathName, { ...init, headers });
    const body = await response.text();
    const parsed = body ? JSON.parse(body) : {};

    if (!response.ok) {
      throw new Error(parsed.error || `${response.status} ${response.statusText}`);
    }

    return parsed as T;
  }

  async function refreshAll() {
    loading = true;
    error = "";

    const results = await Promise.allSettled([
      loadRuns(),
      loadPipelines(),
      loadCapabilities(),
      loadWorkPool()
    ]);

    const failed = results.find((result) => result.status === "rejected") as PromiseRejectedResult | undefined;
    if (failed) {
      error = failed.reason instanceof Error ? failed.reason.message : String(failed.reason);
    }

    loading = false;
  }

  async function loadRuns() {
    const data = await requestJson<{ runs: RunSummary[] }>("/runs");
    runs = data.runs || [];
  }

  async function loadPipelines() {
    const data = await requestJson<JsonMap>("/pipelines");
    pipelines = data.pipelines || [];
    pipelineTotals = data.totals || {};
    if (!selectedPipelineId && pipelines[0]) {
      selectedPipelineId = pipelines[0].id;
      copyPipelineToDraft(pipelines[0]);
    }
  }

  async function loadCapabilities() {
    const data = await requestJson<JsonMap>("/capabilities/setup");
    capabilities = data.capabilities || [];
    setupTotals = data.totals || {};
    if (!selectedCapabilityId && capabilities[0]) {
      selectedCapabilityId = capabilities[0].id;
      copyCapabilityToDraft(capabilities[0]);
    }
  }

  async function loadWorkPool() {
    board = await requestJson<JsonMap>("/work-pool");
  }

  async function openRun(run: RunSummary | string) {
    const runId = typeof run === "string" ? run : run.run_id;
    drawerKind = "run";
    drawerTab = "summary";
    selectedRunId = runId;
    selectedRunDetail = null;
    runDetailLoading = true;

    try {
      selectedRunDetail = await requestJson<JsonMap>(`/runs/${encodeURIComponent(runId)}`);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    } finally {
      runDetailLoading = false;
    }
  }

  function openPipeline(pipeline: Pipeline, tab = "summary") {
    selectedPipelineId = pipeline.id;
    copyPipelineToDraft(pipeline);
    drawerKind = "pipeline";
    drawerTab = tab;
  }

  function openCapability(capability: Capability, tab = "summary") {
    selectedCapabilityId = capability.id;
    copyCapabilityToDraft(capability);
    drawerKind = "capability";
    drawerTab = tab;
  }

  function selectCapability(capability: Capability) {
    selectedCapabilityId = capability.id;
    copyCapabilityToDraft(capability);
  }

  function openTask(task: Task) {
    selectedTaskId = task.id;
    drawerKind = "task";
    drawerTab = "summary";
  }

  function openAdvanced() {
    drawerKind = "advanced";
    drawerTab = "sandbox";
  }

  function closeDrawer() {
    drawerKind = "none";
  }

  async function savePipeline() {
    try {
      const payload = pipelinePayload();
      const exists = pipelines.some((pipeline) => pipeline.id === payload.id);
      const pathName = exists ? `/pipelines/${encodeURIComponent(payload.id)}` : "/pipelines";
      const method = exists ? "PUT" : "POST";
      const data = await requestJson<JsonMap>(pathName, {
        method,
        body: JSON.stringify(payload)
      });
      const pipeline = data.pipeline || data.capability;
      selectedPipelineId = pipeline?.id || payload.id;
      toast = exists ? "Workflow updated" : "Workflow created";
      await refreshAll();
      const saved = pipelines.find((item) => item.id === selectedPipelineId);
      if (saved) copyPipelineToDraft(saved);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function executePipeline(pipeline = selectedPipeline) {
    if (!pipeline) return;

    try {
      const inputs = parseJsonInput(executionInputJson);
      const result = await requestJson<JsonMap>(`/pipelines/${encodeURIComponent(pipeline.id)}/execute`, {
        method: "POST",
        body: JSON.stringify({ inputs })
      });
      toast = `Execution ${result.execution_id || "started"}`;
      await refreshAll();
      openPipeline(pipeline, "executions");
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function executeCapability(capability = selectedCapability) {
    if (!capability) return;

    try {
      const inputs = parseJsonInput(runInputJson);
      await requestJson<JsonMap>(`/workloads/${encodeURIComponent(capability.id)}/execute`, {
        method: "POST",
        body: JSON.stringify({ inputs })
      });
      toast = `Wrapper ${capability.id} executed`;
      await refreshAll();
      openCapability(capability, "work");
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  async function saveCapabilitySetup() {
    try {
      const payload = capabilityPayload();
      const exists = capabilities.some((capability) => capability.id === payload.id);
      const pathName = exists ? `/capabilities/setup/${encodeURIComponent(payload.id)}` : "/capabilities/setup";
      const method = exists ? "PUT" : "POST";
      const data = await requestJson<JsonMap>(pathName, {
        method,
        body: JSON.stringify(payload)
      });
      selectedCapabilityId = data.capability?.id || payload.id;
      toast = exists ? "Runtime wrapper updated" : "Runtime wrapper created";
      await refreshAll();
      const saved = capabilities.find((capability) => capability.id === selectedCapabilityId);
      if (saved) copyCapabilityToDraft(saved);
    } catch (err) {
      error = err instanceof Error ? err.message : String(err);
    }
  }

  function newCapabilitySetup() {
    capabilityDraft = { ...defaultCapabilityDraft, id: uniqueDraftId("wrapper") };
    selectedCapabilityId = "";
  }

  function pipelinePayload() {
    const steps = pipelineSteps
      .map((step, index) => normalizeStepDraft(step, index))
      .filter((step) => step.capability_id);

    if (steps.length === 0) {
      throw new Error("Add at least one workflow step with a capability");
    }

    return {
      id: pipelineDraft.id.trim(),
      title: pipelineDraft.title.trim(),
      description: pipelineDraft.description.trim(),
      provider: pipelineDraft.provider.trim(),
      cwd: pipelineDraft.cwd.trim() || undefined,
      template: pipelineDraft.template,
      timeout_ms: Number(pipelineDraft.timeout_ms) || undefined,
      env_passthrough: pipelineDraft.env_passthrough
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean),
      workflow: {
        trigger: { type: "manual" },
        allowed_capabilities: [...new Set(steps.map((step) => step.capability_id))],
        steps
      },
      capability: {
        name: pipelineDraft.title.trim() || pipelineDraft.id.trim(),
        trust_tier: "T1",
        harnesses: [pipelineDraft.provider.trim()].filter(Boolean)
      }
    };
  }

  function capabilityPayload() {
    const id = capabilityDraft.id.trim();
    if (!id) throw new Error("Capability id is required");

    const provider = capabilityDraft.provider.trim();
    const result = parseOptionalJsonObject(capabilityDraft.result_json, "Result schema");
    const capability: JsonMap = {
      name: capabilityDraft.title.trim() || id,
      harnesses: provider ? [provider] : [],
      trust_tier: capabilityDraft.trust_tier.trim() || "T1"
    };

    if (capabilityDraft.skill.trim()) capability.skill = capabilityDraft.skill.trim();
    if (result) capability.result = result;

    return {
      id,
      title: capabilityDraft.title.trim(),
      description: capabilityDraft.description.trim(),
      provider,
      cwd: capabilityDraft.cwd.trim() || undefined,
      template: capabilityDraft.template,
      timeout_ms: Number(capabilityDraft.timeout_ms) || undefined,
      approvals: capabilityDraft.approvals,
      transport_order: splitList(capabilityDraft.transport_order),
      env_passthrough: splitList(capabilityDraft.env_passthrough),
      capability
    };
  }

  function copyPipelineToDraft(pipeline: Pipeline) {
    pipelineDraft = {
      id: pipeline.id || "",
      title: pipeline.title || pipeline.id || "",
      description: pipeline.description || "",
      provider: pipeline.provider || "fake",
      cwd: pipeline.cwd || "",
      template: pipeline.template || "Work on {{objective}}. Return {{expected_output}}.",
      env_passthrough: (pipeline.env_passthrough || []).join(", "),
      timeout_ms: String(pipeline.tasks?.timeout_ms || "120000")
    };
    pipelineSteps = draftStepsFromPipeline(pipeline);
  }

  function copyCapabilityToDraft(capability: Capability) {
    capabilityDraft = {
      id: capability.id || "",
      title: capability.title || capability.capability?.name || capability.id || "",
      description: capability.description || "",
      provider: capability.runtime?.provider || capability.provider || "fake",
      cwd: capability.runtime?.cwd || "",
      template: capability.template || "Work on {{objective}}. Return {{expected_output}}.",
      timeout_ms: String(capability.runtime?.timeout_ms || capability.timeout_ms || "120000"),
      approvals: capability.runtime?.approvals || "deny",
      transport_order: (capability.runtime?.transport_order || ["stdio-json", "pty"]).join(", "),
      env_passthrough: (capability.env?.passthrough || []).map((item: JsonMap) => item.name).join(", "),
      trust_tier: capability.capability?.trust_tier || capability.sandbox?.trust_tier || "T1",
      skill: capability.capability?.skill || "",
      result_json: capability.capability?.result ? JSON.stringify(capability.capability.result, null, 2) : ""
    };
  }

  function draftStepsFromPipeline(pipeline: Pipeline): StepDraft[] {
    const steps = workflowStepsFor(pipeline)
      .map((step, index) => ({
        id: String(step.id || `step-${index + 1}`),
        name: String(step.name || step.id || step.capability_id || `Step ${index + 1}`),
        capability_id: String(step.capability_id || step.id || pipeline.id || "")
      }))
      .filter((step) => step.capability_id || step.id || step.name);

    if (steps.length > 0) return steps;

    return [
      {
        id: "run",
        name: "Run",
        capability_id: pipeline.id || pipelineDraft.id || ""
      }
    ];
  }

  function normalizeStepDraft(step: StepDraft, index: number) {
    const capabilityId = step.capability_id.trim();
    const id = (step.id.trim() || capabilityId || `step-${index + 1}`).replace(/\s+/g, "-");

    return {
      id,
      name: step.name.trim() || humanize(id),
      capability_id: capabilityId
    };
  }

  function updatePipelineStep(index: number, key: keyof StepDraft, value: string) {
    pipelineSteps = pipelineSteps.map((step, stepIndex) =>
      stepIndex === index ? { ...step, [key]: value } : step
    );
  }

  function addPipelineStep() {
    const nextIndex = pipelineSteps.length + 1;
    const fallbackCapability = pipelineSteps.at(-1)?.capability_id || selectedCapability?.id || pipelineDraft.id;
    pipelineSteps = [
      ...pipelineSteps,
      {
        id: `step-${nextIndex}`,
        name: `Step ${nextIndex}`,
        capability_id: fallbackCapability || ""
      }
    ];
  }

  function removePipelineStep(index: number) {
    if (pipelineSteps.length <= 1) return;
    pipelineSteps = pipelineSteps.filter((_step, stepIndex) => stepIndex !== index);
  }

  function parseJsonInput(raw: string) {
    try {
      const parsed = JSON.parse(raw || "{}");
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error("JSON input must be an object");
      }
      return parsed;
    } catch (err) {
      throw new Error(err instanceof Error ? err.message : String(err));
    }
  }

  function parseOptionalJsonObject(raw: string, label: string) {
    const trimmed = raw.trim();
    if (!trimmed) return undefined;

    try {
      const parsed = JSON.parse(trimmed);
      if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
        throw new Error(`${label} must be a JSON object`);
      }
      return parsed;
    } catch (err) {
      throw new Error(err instanceof Error ? err.message : String(err));
    }
  }

  function updateDraft(key: keyof typeof pipelineDraft, value: string) {
    pipelineDraft = { ...pipelineDraft, [key]: value };
  }

  function updateCapabilityDraft(key: keyof CapabilityDraft, value: string) {
    capabilityDraft = { ...capabilityDraft, [key]: value };
  }

  function splitList(value: string) {
    return value
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean);
  }

  function uniqueDraftId(prefix: string) {
    return `${prefix}-${Date.now().toString(36).slice(-6)}`;
  }

  function setTheme(mode: ThemeMode) {
    activeTheme = mode;
    localStorage.setItem("mitos-theme", mode);
    document.documentElement.dataset.theme = mode;
  }

  function inputValue(event: Event) {
    return (event.currentTarget as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement).value;
  }

  function matchesQuery(values: Array<string | number | boolean | undefined | null>, query: string) {
    const needle = query.trim().toLowerCase();
    if (!needle) return true;
    return values.some((value) => String(value || "").toLowerCase().includes(needle));
  }

  function workflowStepsFor(pipeline: Pipeline | null) {
    const steps = pipeline?.workflow?.steps;
    return Array.isArray(steps) ? steps : [];
  }

  function summarize(
    runList: RunSummary[],
    pipelineList: Pipeline[],
    capabilityList: Capability[],
    workBoard: JsonMap,
    capabilityTotals: JsonMap,
    authoredPipelineTotals: JsonMap
  ) {
    return {
      runs: runList.length,
      running: runList.filter((run) => run.live || run.status === "running").length,
      pipelines: authoredPipelineTotals.pipelines || pipelineList.length,
      activePipelines: authoredPipelineTotals.active || 0,
      capabilities: capabilityTotals.capabilities || capabilityList.length,
      readyCapabilities: capabilityTotals.runnable || 0,
      queued: workBoard.totals?.queued || 0,
      activeTasks: workBoard.totals?.active || 0,
      failedTasks: workBoard.totals?.failed || 0
    };
  }

  function statusColor(status?: string) {
    if (["completed", "done", "ready"].includes(status || "")) return "success";
    if (["running", "scheduled", "queued"].includes(status || "")) return "info";
    if (["blocked", "needs_env", "parked"].includes(status || "")) return "warning";
    if (["failed", "error", "cancelled"].includes(status || "")) return "danger";
    return "ghost";
  }

  function displayStatus(status?: string) {
    return (status || "unknown").replaceAll("_", " ");
  }

  function formatDate(value?: string | number) {
    if (!value) return "n/a";
    const date = typeof value === "number" ? new Date(value) : new Date(value);
    if (Number.isNaN(date.getTime())) return String(value);
    return date.toLocaleString();
  }

  function compactId(value?: string) {
    if (!value) return "n/a";
    return value.length > 22 ? `${value.slice(0, 10)}...${value.slice(-8)}` : value;
  }

  function humanize(value?: string) {
    return (value || "")
      .replace(/[-_]/g, " ")
      .replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  function jsonPreview(value: unknown) {
    return JSON.stringify(value || {}, null, 2);
  }

  function columnTasks(status: string): Task[] {
    return board.columns?.[status] || [];
  }

  function runById(id: string) {
    return runs.find((run) => run.run_id === id);
  }

  function totalTasks() {
    return board.totals?.all || (board.tasks || []).length || 0;
  }

  function blockedTasks() {
    return (board.totals?.blocked || 0) + (board.totals?.failed || 0);
  }

  function queueHealthPercent() {
    const total = totalTasks();
    if (!total) return 100;
    return Math.max(0, Math.round(((total - blockedTasks()) / total) * 100));
  }

  function taskLatestRun(task: Task) {
    const latestRunId = task.runs?.[task.runs.length - 1];
    return latestRunId ? runById(latestRunId) : undefined;
  }

  function taskTimestamp(task: Task) {
    return formatDate(task.updated_at || task.created_at);
  }
</script>

<svelte:head>
  <title>Mitos Operations Console</title>
</svelte:head>

<sg-sidebar-layout class="app-shell" data-drawer-open={drawerKind !== "none"}>
  <sg-sidebar-layout-sidebar class="layout-sidebar">
  <sg-sidebar-root class="sidebar" aria-label="Primary navigation" width="248px" collapsed-width="76px">
    <sg-sidebar-header class="brand">
      <div class="brand-mark">MT</div>
      <div>
        <strong>Mitos</strong>
        <span>Operations Console</span>
      </div>
    </sg-sidebar-header>

    <sg-sidebar-content class="nav-list">
      <sg-sidebar-section title="Operations">
      {#each navItems as item}
        <sg-sidebar-item
          class="nav-item"
          class:is-active={activeView === item.key}
          active={activeView === item.key ? true : undefined}
          aria-current={activeView === item.key ? "page" : undefined}
          onclick={() => navigate(item.key)}
        >
          <span class="sg-sidebar-item-icon">{item.short}</span>
          <span class="sg-sidebar-item-label">{item.label}</span>
        </sg-sidebar-item>
      {/each}
      </sg-sidebar-section>
    </sg-sidebar-content>

    <sg-sidebar-footer class="sidebar-status">
      <div>
        <span>Runtime</span>
        <strong>{dashboardTotals.running > 0 ? "Active" : "Idle"}</strong>
      </div>
      <sg-badge color={dashboardTotals.failedTasks > 0 ? "danger" : "success"} size="sm">
        {dashboardTotals.failedTasks > 0 ? `${dashboardTotals.failedTasks} failed` : "healthy"}
      </sg-badge>
    </sg-sidebar-footer>
  </sg-sidebar-root>
  </sg-sidebar-layout-sidebar>

  <sg-sidebar-layout-content class="workspace">
    <sg-stack class="topbar" direction="horizontal" gap="grouped" align="center" justify="space-between">
      <div>
        <span class="eyebrow">{activeView === "overview" ? "Command" : humanize(activeView)}</span>
        <sg-heading level={1} size="2xl">{activeView === "overview" ? "Agent Operations" : navItems.find((item) => item.key === activeView)?.label}</sg-heading>
      </div>
      <sg-stack class="topbar-actions" direction="horizontal" gap="related" align="center" justify="end" wrap>
        <div class="sg-search-input-wrapper search-wrapper" data-max-width="full">
          <input
            class="sg-search-input search-input"
            type="search"
            placeholder="Search runs, workflows, capabilities"
            aria-label="Search runs, workflows, capabilities"
            value={search}
            oninput={(event) => (search = inputValue(event))}
          />
          {#if search}
            <button class="sg-search-clear" type="button" aria-label="Clear search" onclick={() => (search = "")}>x</button>
          {/if}
        </div>
        <button class="sg-button" data-color="outline" type="button" onclick={refreshAll} disabled={loading}>
          {loading ? "Loading" : "Refresh"}
        </button>
        <button
          class="theme-toggle"
          type="button"
          aria-label="Toggle theme"
          onclick={() => setTheme(activeTheme === "light" ? "dark" : "light")}
        >
          {activeTheme === "light" ? "Dark" : "Light"}
        </button>
      </sg-stack>
    </sg-stack>

    {#if error}
      <sg-callout class="notice danger" role="alert">
        <strong>Request failed</strong>
        <sg-text color="secondary">{error}</sg-text>
        <button type="button" onclick={() => (error = "")}>Dismiss</button>
      </sg-callout>
    {/if}

    {#if toast}
      <sg-callout class="notice success" role="status">
        <strong>{toast}</strong>
        <button type="button" onclick={() => (toast = "")}>Dismiss</button>
      </sg-callout>
    {/if}

    {#if activeView === "overview"}
      <sg-stack class="dashboard-grid" gap="grouped">
        <sg-grid class="metric-strip" columns="4" gap="grouped" collapse="md">
          <sg-card elevation={0}>
            <sg-card-body>
              <span>Runs</span>
              <strong>{dashboardTotals.runs}</strong>
              <small>{dashboardTotals.running} running</small>
            </sg-card-body>
          </sg-card>
          <sg-card elevation={0}>
            <sg-card-body>
              <span>Workflows</span>
              <strong>{dashboardTotals.pipelines}</strong>
              <small>{dashboardTotals.activePipelines} active</small>
            </sg-card-body>
          </sg-card>
          <sg-card elevation={0}>
            <sg-card-body>
              <span>Capabilities</span>
              <strong>{dashboardTotals.readyCapabilities}/{dashboardTotals.capabilities}</strong>
              <small>ready</small>
            </sg-card-body>
          </sg-card>
          <sg-card elevation={0}>
            <sg-card-body>
              <span>Work queue</span>
              <strong>{dashboardTotals.activeTasks}</strong>
              <small>{dashboardTotals.queued} queued</small>
            </sg-card-body>
          </sg-card>
        </sg-grid>

        <sg-card class="surface main-panel" elevation={0}>
          <sg-card-header class="section-head">
            <div>
              <span class="eyebrow">Live surface</span>
              <sg-card-title as="h2">Runs and workflow executions</sg-card-title>
            </div>
            <button class="sg-button" data-color="primary" type="button" onclick={() => navigate("workflows")}>
              New workflow
            </button>
          </sg-card-header>

          <sg-card-body>
          <sg-grid class="split-layout" columns="2" gap="grouped" collapse="lg">
            <sg-stack class="run-list" gap="related">
              {#each filteredRuns.slice(0, 8) as run}
                <button class="run-row" type="button" onclick={() => openRun(run)}>
                  <div>
                    <strong>{run.task || run.run_id}</strong>
                    <span>{compactId(run.run_id)} / {run.provider || "provider"}</span>
                  </div>
                  <sg-badge color={statusColor(run.status)} size="sm">{displayStatus(run.status)}</sg-badge>
                </button>
              {:else}
                <sg-empty class="empty-state" title="No runs recorded" description="Run traces will appear here."></sg-empty>
              {/each}
            </sg-stack>

            <sg-card class="execution-panel" elevation={0}>
              <sg-card-header>
                <sg-card-title as="h3">{selectedPipeline?.title || selectedPipeline?.id || "Workflow"}</sg-card-title>
              </sg-card-header>
              <sg-card-body>
              <sg-stack class="step-list" gap="related">
                {#each workflowSteps as step, index}
                  <div class="step-row">
                    <span>{index + 1}</span>
                    <div>
                      <strong>{step.name || step.id}</strong>
                      <small>{step.capability_id || step.id}</small>
                    </div>
                  </div>
                {:else}
                  <sg-empty class="empty-state" title="No steps" description="Define workflow steps to execute a pipeline."></sg-empty>
                {/each}
              </sg-stack>
              </sg-card-body>
              <sg-card-footer class="execution-actions">
                <button class="sg-button" data-color="primary" type="button" onclick={() => executePipeline()}>
                  Execute
                </button>
                <button class="sg-button" data-color="outline" type="button" onclick={() => selectedPipeline && openPipeline(selectedPipeline)}>
                  Details
                </button>
              </sg-card-footer>
            </sg-card>
          </sg-grid>
          </sg-card-body>
        </sg-card>

        <sg-card class="surface queue-panel" elevation={0}>
          <sg-card-header class="section-head compact">
            <div>
              <span class="eyebrow">Work pool</span>
              <sg-card-title as="h2">Queue health</sg-card-title>
            </div>
            <button class="link-button" type="button" onclick={() => navigate("work-pool")}>Open board</button>
          </sg-card-header>
          <sg-card-body>
          <sg-scroller class="queue-scroller compact" axis="x" label="Queue health lanes">
            <div class="queue-columns compact-board">
              {#each boardStatuses.slice(0, 5) as status}
                <sg-card class="queue-column compact-column" elevation={0}>
                  <div class="column-title">
                    <span>{humanize(status)}</span>
                    <strong>{columnTasks(status).length}</strong>
                  </div>
                  {#each columnTasks(status).slice(0, 3) as task}
                    {@const latest = taskLatestRun(task)}
                    <button class="task-card compact-task" type="button" onclick={() => openTask(task)}>
                      <div class="task-card-head">
                        <strong>{compactId(task.id)}</strong>
                        <sg-badge color={statusColor(latest?.status || task.status)} size="sm">{displayStatus(latest?.status || task.status)}</sg-badge>
                      </div>
                      <span>{task.pipeline || task.pool || "interactive"}</span>
                    </button>
                  {:else}
                    <sg-empty class="empty-lane" title="Empty"></sg-empty>
                  {/each}
                </sg-card>
              {/each}
            </div>
          </sg-scroller>
          </sg-card-body>
        </sg-card>
      </sg-stack>
    {:else if activeView === "workflows"}
      <sg-grid class="workflow-layout" columns="2" gap="grouped" collapse="lg">
        <sg-card class="surface workflow-builder" elevation={0}>
          <sg-card-header class="section-head">
            <div>
              <span class="eyebrow">Workflow builder</span>
              <sg-card-title as="h2">Pipeline definition</sg-card-title>
            </div>
            <sg-button-group class="button-row">
              <button class="sg-button" data-color="outline" type="button" onclick={openAdvanced}>
                Advanced
              </button>
              <button class="sg-button" data-color="primary" type="button" onclick={savePipeline}>
                Save
              </button>
            </sg-button-group>
          </sg-card-header>

          <sg-card-body>
          <form class="definition-form" onsubmit={(event) => { event.preventDefault(); void savePipeline(); }}>
            <sg-field label="ID" html-for="pipeline-id">
              <input id="pipeline-id" class="sg-input" value={pipelineDraft.id} oninput={(event) => updateDraft("id", inputValue(event))} />
            </sg-field>
            <sg-field label="Title" html-for="pipeline-title">
              <input id="pipeline-title" class="sg-input" value={pipelineDraft.title} oninput={(event) => updateDraft("title", inputValue(event))} />
            </sg-field>
            <sg-field class="wide" label="Description" html-for="pipeline-description">
              <input id="pipeline-description" class="sg-input" value={pipelineDraft.description} oninput={(event) => updateDraft("description", inputValue(event))} />
            </sg-field>
            <sg-field label="Provider" html-for="pipeline-provider">
              <input id="pipeline-provider" class="sg-input" value={pipelineDraft.provider} oninput={(event) => updateDraft("provider", inputValue(event))} />
            </sg-field>
            <sg-field label="Working directory" html-for="pipeline-cwd">
              <input id="pipeline-cwd" class="sg-input" value={pipelineDraft.cwd} oninput={(event) => updateDraft("cwd", inputValue(event))} />
            </sg-field>
            <sg-field label="Timeout ms" html-for="pipeline-timeout">
              <input id="pipeline-timeout" class="sg-input" type="number" value={pipelineDraft.timeout_ms} oninput={(event) => updateDraft("timeout_ms", inputValue(event))} />
            </sg-field>
            <sg-field label="Host env keys" html-for="pipeline-env" description="Comma-separated environment keys passed through to the runner.">
              <input id="pipeline-env" class="sg-input" value={pipelineDraft.env_passthrough} oninput={(event) => updateDraft("env_passthrough", inputValue(event))} />
            </sg-field>
            <sg-field class="wide" label="Prompt template" html-for="pipeline-template" description="Use execution input keys such as objective and expected_output.">
              <textarea id="pipeline-template" class="sg-textarea" rows="5" value={pipelineDraft.template} oninput={(event) => updateDraft("template", inputValue(event))}></textarea>
            </sg-field>
            <fieldset class="sg-fieldset workflow-step-field wide">
              <legend class="sg-legend">Workflow steps</legend>
              <sg-stack class="step-editor" gap="related">
                {#each pipelineSteps as step, index}
                  <sg-grid class="step-editor-row" columns="4" gap="related" collapse="md">
                    <sg-field label="Step ID" html-for={`workflow-step-${index}-id`}>
                      <input
                        id={`workflow-step-${index}-id`}
                        class="sg-input mono"
                        value={step.id}
                        oninput={(event) => updatePipelineStep(index, "id", inputValue(event))}
                      />
                    </sg-field>
                    <sg-field label="Name" html-for={`workflow-step-${index}-name`}>
                      <input
                        id={`workflow-step-${index}-name`}
                        class="sg-input"
                        value={step.name}
                        oninput={(event) => updatePipelineStep(index, "name", inputValue(event))}
                      />
                    </sg-field>
                    <sg-field label="Capability" html-for={`workflow-step-${index}-capability`}>
                      <input
                        id={`workflow-step-${index}-capability`}
                        class="sg-input mono"
                        list="workflow-capability-options"
                        value={step.capability_id}
                        oninput={(event) => updatePipelineStep(index, "capability_id", inputValue(event))}
                      />
                    </sg-field>
                    <div class="step-editor-actions">
                      <sg-badge color="ghost" size="sm">Step {index + 1}</sg-badge>
                      <button
                        class="sg-button"
                        data-color="outline"
                        data-size="sm"
                        type="button"
                        onclick={() => removePipelineStep(index)}
                        disabled={pipelineSteps.length <= 1}
                      >
                        Remove
                      </button>
                    </div>
                  </sg-grid>
                {/each}
                <button class="sg-button add-step-button" data-color="outline" type="button" onclick={addPipelineStep}>
                  Add step
                </button>
              </sg-stack>
              <datalist id="workflow-capability-options">
                {#each capabilityOptionIds as capabilityId}
                  <option value={capabilityId}></option>
                {/each}
              </datalist>
            </fieldset>
          </form>
          </sg-card-body>
        </sg-card>

        <sg-card class="surface workflow-side" elevation={0}>
          <sg-card-header class="section-head compact">
            <div>
              <span class="eyebrow">Registry</span>
              <sg-card-title as="h2">Defined pipelines</sg-card-title>
            </div>
          </sg-card-header>
          <sg-card-body>
          <sg-stack class="pipeline-list" gap="related">
            {#each filteredPipelines as pipeline}
              <button
                class="pipeline-row"
                class:is-active={selectedPipelineId === pipeline.id}
                type="button"
                onclick={() => openPipeline(pipeline)}
              >
                <div>
                  <strong>{pipeline.title || pipeline.id}</strong>
                  <span>{pipeline.provider || "provider"} / {pipeline.tasks?.active || 0} active</span>
                </div>
                <sg-badge color={pipeline.tasks?.failed ? "danger" : pipeline.tasks?.active ? "info" : "ghost"} size="sm">
                  {pipeline.executions?.length || 0} exec
                </sg-badge>
              </button>
            {:else}
              <sg-empty class="empty-state" title="No matching pipelines" description="Create or refine the search to see workflows."></sg-empty>
            {/each}
          </sg-stack>

          <sg-section class="selected-pipeline-section" title="Selected workflow" level="3" gap="related">
            {#if selectedPipeline}
              <sg-grid class="pipeline-summary-grid" columns="2" gap="related" collapse="sm">
                <div>
                  <span>Provider</span>
                  <strong>{selectedPipeline.provider || pipelineDraft.provider || "n/a"}</strong>
                </div>
                <div>
                  <span>Executions</span>
                  <strong>{selectedPipeline.executions?.length || 0}</strong>
                </div>
                <div>
                  <span>Active tasks</span>
                  <strong>{selectedPipeline.tasks?.active || 0}</strong>
                </div>
                <div>
                  <span>Failed tasks</span>
                  <strong>{selectedPipeline.tasks?.failed || 0}</strong>
                </div>
              </sg-grid>
              <sg-stack class="selected-step-list" gap="related">
                {#each selectedWorkflowSteps as step, index}
                  <div class="selected-step-row">
                    <sg-badge color="ghost" size="sm">{index + 1}</sg-badge>
                    <div>
                      <strong>{step.name || step.id}</strong>
                      <span>{step.capability_id || "capability"}</span>
                    </div>
                  </div>
                {:else}
                  <sg-empty class="empty-state" title="No workflow steps" description="The selected pipeline needs at least one step."></sg-empty>
                {/each}
              </sg-stack>
            {:else}
              <sg-empty class="empty-state" title="No workflow selected" description="Select or save a pipeline before executing."></sg-empty>
            {/if}
          </sg-section>

          <sg-section class="launch-box" title="Execute workflow" level="3" gap="grouped">
            <sg-field label="Execution input" html-for="pipeline-execution-input" description="JSON object sent to the selected pipeline.">
              <textarea id="pipeline-execution-input" class="sg-textarea mono" rows="8" value={executionInputJson} oninput={(event) => (executionInputJson = inputValue(event))}></textarea>
            </sg-field>
            <button class="sg-button" data-color="primary" type="button" onclick={() => executePipeline()}>
              Execute selected
            </button>
          </sg-section>
          </sg-card-body>
        </sg-card>
      </sg-grid>
    {:else if activeView === "runs"}
      <sg-card class="surface" elevation={0}>
        <sg-card-header class="section-head">
          <div>
            <span class="eyebrow">Runs overview</span>
            <sg-card-title as="h2">Trace-backed runs</sg-card-title>
          </div>
          <sg-badge color="info">{filteredRuns.length} shown</sg-badge>
        </sg-card-header>
        <sg-card-body>
        <sg-data-table-root class="ops-table" density="compact" page-size="12" sort-column="created" sort-direction="desc" striped aria-label="Trace-backed runs">
          <sg-data-table-header>
            <sg-data-table-row>
              <sg-data-table-head column="run">Run</sg-data-table-head>
              <sg-data-table-head column="provider">Provider</sg-data-table-head>
              <sg-data-table-head column="status">Status</sg-data-table-head>
              <sg-data-table-head column="events">Events</sg-data-table-head>
              <sg-data-table-head column="approvals">Approvals</sg-data-table-head>
              <sg-data-table-head column="created">Created</sg-data-table-head>
            </sg-data-table-row>
          </sg-data-table-header>
          <sg-data-table-body>
            {#each filteredRuns as run}
              <sg-data-table-row class="clickable" onclick={() => openRun(run)}>
                <sg-data-table-cell>
                    <strong>{run.task || compactId(run.run_id)}</strong>
                    <span>{compactId(run.run_id)}</span>
                </sg-data-table-cell>
                <sg-data-table-cell>{run.provider || "n/a"}</sg-data-table-cell>
                <sg-data-table-cell><sg-badge color={statusColor(run.status)} size="sm">{displayStatus(run.status)}</sg-badge></sg-data-table-cell>
                <sg-data-table-cell>{run.events || 0}</sg-data-table-cell>
                <sg-data-table-cell>{run.approvals || 0}</sg-data-table-cell>
                <sg-data-table-cell>{formatDate(run.created_at_ms)}</sg-data-table-cell>
              </sg-data-table-row>
            {:else}
              <sg-data-table-row>
                <sg-data-table-cell class="empty-table-cell">
                  <sg-empty title="No runs recorded" description="Run traces will appear after an agent or wrapper executes."></sg-empty>
                </sg-data-table-cell>
              </sg-data-table-row>
            {/each}
          </sg-data-table-body>
          <sg-data-table-pagination aria-label="Runs pagination"></sg-data-table-pagination>
        </sg-data-table-root>
        </sg-card-body>
      </sg-card>
    {:else if activeView === "capabilities"}
      <sg-grid class="capability-grid" columns="2" gap="grouped" collapse="lg">
        <sg-card class="surface" elevation={0}>
          <sg-card-header class="section-head">
            <div>
              <span class="eyebrow">Runtime wrappers</span>
              <sg-card-title as="h2">Capability setup</sg-card-title>
            </div>
            <sg-badge color={setupTotals.needs_setup ? "warning" : "success"}>
              {setupTotals.runnable || 0} ready
            </sg-badge>
          </sg-card-header>

          <sg-card-body>
          <sg-stack class="capability-list" gap="related">
            {#each filteredCapabilities as capability}
              <button
                class="capability-card"
                class:is-active={selectedCapabilityId === capability.id}
                type="button"
                onclick={() => selectCapability(capability)}
              >
                <div>
                  <strong>{capability.title || capability.id}</strong>
                  <span>{capability.runtime?.provider || capability.provider || "provider"} / {capability.sandbox?.trust_tier || "T1"}</span>
                </div>
                <sg-badge color={statusColor(capability.setup?.status)} size="sm">
                  {displayStatus(capability.setup?.status)}
                </sg-badge>
              </button>
            {:else}
              <sg-empty class="empty-state" title="No matching capabilities" description="Runtime wrappers will appear here."></sg-empty>
            {/each}
          </sg-stack>
          </sg-card-body>
        </sg-card>

        <sg-card class="surface runner-panel" elevation={0}>
          <sg-card-header class="section-head compact">
            <div>
              <span class="eyebrow">Smallest runnable unit</span>
              <sg-card-title as="h2">{selectedCapability?.title || selectedCapability?.id || "Capability"}</sg-card-title>
            </div>
            <sg-button-group class="button-row">
              <button class="sg-button" data-color="outline" type="button" onclick={newCapabilitySetup}>
                New
              </button>
              <button class="sg-button" data-color="primary" type="button" onclick={saveCapabilitySetup}>
                Save setup
              </button>
            </sg-button-group>
          </sg-card-header>

          <sg-card-body>
          <sg-grid class="readiness-list" columns="3" gap="related" collapse="md">
            <div>
              <span>Definition</span>
              <strong>{selectedCapability?.files?.find((file) => file.kind === "workload_definition")?.present ? "Present" : "Missing"}</strong>
            </div>
            <div>
              <span>Host env</span>
              <strong>{selectedCapability?.env?.missing_passthrough?.length ? "Needs keys" : "Ready"}</strong>
            </div>
            <div>
              <span>Sandbox</span>
              <strong>{displayStatus(selectedCapability?.sandbox?.status)}</strong>
            </div>
          </sg-grid>

          <sg-section class="setup-editor-section" title="Setup definition" level="3" gap="grouped">
            <form class="capability-setup-form" onsubmit={(event) => { event.preventDefault(); void saveCapabilitySetup(); }}>
              <sg-field label="ID" html-for="capability-id">
                <input id="capability-id" class="sg-input mono" value={capabilityDraft.id} oninput={(event) => updateCapabilityDraft("id", inputValue(event))} />
              </sg-field>
              <sg-field label="Title" html-for="capability-title">
                <input id="capability-title" class="sg-input" value={capabilityDraft.title} oninput={(event) => updateCapabilityDraft("title", inputValue(event))} />
              </sg-field>
              <sg-field class="wide" label="Description" html-for="capability-description">
                <input id="capability-description" class="sg-input" value={capabilityDraft.description} oninput={(event) => updateCapabilityDraft("description", inputValue(event))} />
              </sg-field>
              <sg-field label="Provider" html-for="capability-provider">
                <input id="capability-provider" class="sg-input mono" value={capabilityDraft.provider} oninput={(event) => updateCapabilityDraft("provider", inputValue(event))} />
              </sg-field>
              <sg-field label="Trust tier" html-for="capability-trust-tier">
                <select id="capability-trust-tier" class="sg-native-select" value={capabilityDraft.trust_tier} oninput={(event) => updateCapabilityDraft("trust_tier", inputValue(event))}>
                  <option value="T1">T1</option>
                  <option value="T2">T2</option>
                  <option value="T3">T3</option>
                </select>
              </sg-field>
              <sg-field label="Approvals" html-for="capability-approvals">
                <select id="capability-approvals" class="sg-native-select" value={capabilityDraft.approvals} oninput={(event) => updateCapabilityDraft("approvals", inputValue(event))}>
                  <option value="deny">deny</option>
                  <option value="ask">ask</option>
                  <option value="allow">allow</option>
                </select>
              </sg-field>
              <sg-field label="Timeout ms" html-for="capability-timeout">
                <input id="capability-timeout" class="sg-input" type="number" value={capabilityDraft.timeout_ms} oninput={(event) => updateCapabilityDraft("timeout_ms", inputValue(event))} />
              </sg-field>
              <sg-field class="wide" label="Prompt template" html-for="capability-template" description="Template variables are bound from the JSON input below.">
                <textarea id="capability-template" class="sg-textarea" rows="4" value={capabilityDraft.template} oninput={(event) => updateCapabilityDraft("template", inputValue(event))}></textarea>
              </sg-field>
              <sg-field label="Working directory" html-for="capability-cwd">
                <input id="capability-cwd" class="sg-input" value={capabilityDraft.cwd} oninput={(event) => updateCapabilityDraft("cwd", inputValue(event))} />
              </sg-field>
              <sg-field label="Transport order" html-for="capability-transport" description="Comma-separated transport preference.">
                <input id="capability-transport" class="sg-input mono" value={capabilityDraft.transport_order} oninput={(event) => updateCapabilityDraft("transport_order", inputValue(event))} />
              </sg-field>
              <sg-field label="Host env keys" html-for="capability-env" description="Comma-separated host env names. Secret values stay outside the UI.">
                <input id="capability-env" class="sg-input mono" value={capabilityDraft.env_passthrough} oninput={(event) => updateCapabilityDraft("env_passthrough", inputValue(event))} />
              </sg-field>
              <sg-field label="Skill reference" html-for="capability-skill">
                <input id="capability-skill" class="sg-input mono" value={capabilityDraft.skill} oninput={(event) => updateCapabilityDraft("skill", inputValue(event))} />
              </sg-field>
              <sg-field class="wide" label="Result schema" html-for="capability-result">
                <textarea id="capability-result" class="sg-textarea mono" rows="4" value={capabilityDraft.result_json} oninput={(event) => updateCapabilityDraft("result_json", inputValue(event))}></textarea>
              </sg-field>
            </form>
          </sg-section>

          {#if selectedCapability}
            <sg-section class="required-files-section" title="Required files" level="3" gap="related">
              <div class="file-list compact">
                {#each selectedCapability.files || [] as file}
                  <div>
                    <strong>{file.label || file.kind}</strong>
                    <span>{file.path}</span>
                    <sg-badge color={file.present ? "success" : file.required ? "danger" : "ghost"} size="sm">
                      {file.present ? "present" : "missing"}
                    </sg-badge>
                  </div>
                {/each}
              </div>
            </sg-section>
          {/if}

          <sg-section class="wrapper-run-section" title="Run test" level="3" gap="grouped">
            <sg-field label="Wrapper input" html-for="capability-run-input" description="JSON object sent to the selected runtime wrapper.">
              <textarea id="capability-run-input" class="sg-textarea mono" rows="7" value={runInputJson} oninput={(event) => (runInputJson = inputValue(event))}></textarea>
            </sg-field>
            <sg-button-group class="button-row full">
              <button class="sg-button" data-color="primary" type="button" onclick={() => executeCapability()}>
                Execute wrapper
              </button>
              <button class="sg-button" data-color="outline" type="button" onclick={() => selectedCapability && openCapability(selectedCapability)}>
                Details
              </button>
            </sg-button-group>
          </sg-section>
          </sg-card-body>
        </sg-card>
      </sg-grid>
    {:else if activeView === "work-pool"}
      <sg-card class="surface board-surface" elevation={0}>
        <sg-card-header class="section-head">
          <div>
            <span class="eyebrow">Kanban queue</span>
            <sg-card-title as="h2">Work pool</sg-card-title>
          </div>
          <sg-badge color="info">{totalTasks()} tasks</sg-badge>
        </sg-card-header>

        <sg-card-body>
        <sg-grid class="board-summary-grid" columns="4" gap="related" collapse="md">
          <div>
            <span>Queued</span>
            <strong>{board.totals?.queued || 0}</strong>
          </div>
          <div>
            <span>Active</span>
            <strong>{board.totals?.active || 0}</strong>
          </div>
          <div>
            <span>Blocked / failed</span>
            <strong>{blockedTasks()}</strong>
          </div>
          <div class="board-health">
            <span id="queue-health-label">Queue health</span>
            <strong>{queueHealthPercent()}%</strong>
            <progress
              class="sg-progress"
              data-size="sm"
              data-color={queueHealthPercent() < 75 ? "warning" : "success"}
              value={queueHealthPercent()}
              max="100"
              aria-labelledby="queue-health-label"
            ></progress>
          </div>
        </sg-grid>

        <sg-scroller class="queue-scroller" axis="x" label="Work pool lanes">
          <div class="queue-columns">
            {#each boardStatuses as status}
              <sg-card class="queue-column" elevation={0}>
                <div class="column-title">
                  <span>{humanize(status)}</span>
                  <strong>{columnTasks(status).length}</strong>
                </div>
                {#each columnTasks(status) as task}
                  {@const latest = taskLatestRun(task)}
                  <button class="task-card" type="button" onclick={() => openTask(task)}>
                    <div class="task-card-head">
                      <strong>{compactId(task.id)}</strong>
                      <sg-badge color={statusColor(latest?.status || task.status)} size="sm">
                        {displayStatus(latest?.status || task.status)}
                      </sg-badge>
                    </div>
                    <span>{task.pipeline || task.pool || "interactive"}</span>
                    <div class="task-meta-grid">
                      <small>Priority <strong>{task.priority ?? "n/a"}</strong></small>
                      <small>Source <strong>{task.source || "manual"}</strong></small>
                      <small>Runs <strong>{task.runs?.length || 0}</strong></small>
                      <small>Updated <strong>{taskTimestamp(task)}</strong></small>
                    </div>
                  </button>
                {:else}
                  <sg-empty class="empty-lane" title="Empty"></sg-empty>
                {/each}
              </sg-card>
            {/each}
          </div>
        </sg-scroller>
        </sg-card-body>
      </sg-card>
    {:else if activeView === "evidence"}
      <sg-card class="surface" elevation={0}>
        <sg-card-header class="section-head">
          <div>
            <span class="eyebrow">Proof and evidence</span>
            <sg-card-title as="h2">Run artifacts</sg-card-title>
          </div>
        </sg-card-header>

        <sg-card-body>
        <sg-grid class="evidence-grid" columns="auto-fill" gap="grouped">
          {#each runs as run}
            <button class="evidence-card" type="button" onclick={() => openRun(run)}>
              <div>
                <strong>{run.task || compactId(run.run_id)}</strong>
                <span>{compactId(run.run_id)}</span>
              </div>
              <div class="evidence-stats">
                <sg-badge color={statusColor(run.status)} size="sm">{displayStatus(run.status)}</sg-badge>
                <span>{run.events || 0} events</span>
                <span>{run.approvals || 0} approvals</span>
              </div>
            </button>
          {:else}
            <sg-empty class="empty-state" title="No evidence artifacts" description="Proof pages are generated from run traces."></sg-empty>
          {/each}
        </sg-grid>
        </sg-card-body>
      </sg-card>
    {:else if activeView === "settings"}
      <sg-grid class="settings-grid" columns="2" gap="grouped" collapse="lg">
        <sg-card class="surface" elevation={0}>
          <sg-card-header class="section-head">
            <div>
              <span class="eyebrow">Theme</span>
              <sg-card-title as="h2">GitHub preset tokens</sg-card-title>
            </div>
            <sg-button-group class="segmented">
              <button class="sg-button" class:is-active={activeTheme === "light"} data-color="outline" type="button" onclick={() => setTheme("light")}>Light</button>
              <button class="sg-button" class:is-active={activeTheme === "dark"} data-color="outline" type="button" onclick={() => setTheme("dark")}>Dark</button>
            </sg-button-group>
          </sg-card-header>

          <sg-card-body>
          <sg-stack class="swatch-grid" gap="related">
            {#each themeSwatches(activeTheme) as swatch}
              <div class="swatch-row">
                <span style={`--swatch:${swatch.value}`}></span>
                <strong>{swatch.name}</strong>
                <code>{swatch.value}</code>
              </div>
            {/each}
          </sg-stack>
          </sg-card-body>
        </sg-card>

        <sg-card class="surface" elevation={0}>
          <sg-card-header class="section-head compact">
            <div>
              <span class="eyebrow">SigUI contract</span>
              <sg-card-title as="h2">Resolved system</sg-card-title>
            </div>
          </sg-card-header>
          <sg-card-body>
          <pre class="json-block">{jsonPreview({
            preset: themeContract.theme?.name,
            density: themeContract.density,
            depth: themeContract.depth,
            shape: themeContract.shape,
            motion: themeContract.motion
          })}</pre>
          </sg-card-body>
        </sg-card>
      </sg-grid>
    {/if}
  </sg-sidebar-layout-content>

  <sg-sheet-root class="detail-drawer-root" open={drawerKind !== "none" ? true : undefined} side="right" size="md">
  <sg-sheet-content class="detail-drawer" aria-hidden={drawerKind === "none"}>
    <sg-sheet-header class="drawer-header">
      <div>
        <span class="eyebrow">{humanize(drawerKind)}</span>
        <sg-sheet-title>
          {#if drawerKind === "run"}
            {compactId(selectedRunId)}
          {:else if drawerKind === "pipeline"}
            {selectedPipeline?.title || selectedPipeline?.id || "Pipeline"}
          {:else if drawerKind === "capability"}
            {selectedCapability?.title || selectedCapability?.id || "Capability"}
          {:else if drawerKind === "task"}
            {compactId(selectedTask?.id)}
          {:else if drawerKind === "advanced"}
            Advanced execution
          {:else}
            Details
          {/if}
        </sg-sheet-title>
      </div>
      <sg-sheet-close placement="corner">
        <button class="drawer-close" type="button" aria-label="Close drawer" onclick={closeDrawer}>Close</button>
      </sg-sheet-close>
    </sg-sheet-header>

    {#if drawerKind !== "none"}
      <sg-tabs-root class="drawer-tabs" value={drawerTab} oninput={(event) => (drawerTab = (event.currentTarget as HTMLElement).getAttribute("value") || drawerTab)}>
        <sg-tabs-list aria-label="Detail sections">
        {#each drawerTabs(drawerKind) as tab}
          <sg-tabs-trigger value={tab} class:is-active={drawerTab === tab} onclick={() => (drawerTab = tab)}>
            {humanize(tab)}
          </sg-tabs-trigger>
        {/each}
        </sg-tabs-list>
      </sg-tabs-root>
    {/if}

    <sg-scroll-area-root class="drawer-scroll" type="hover">
    <sg-scroll-area-viewport>
    <sg-stack class="drawer-body" gap="grouped">
      {#if drawerKind === "run"}
        {#if runDetailLoading}
          <sg-empty class="empty-state" title="Loading run detail"></sg-empty>
        {:else if selectedRunDetail}
          {#if drawerTab === "summary"}
            <dl class="detail-list">
              <div><dt>Status</dt><dd><sg-badge color={statusColor(selectedRunDetail.status)}>{displayStatus(selectedRunDetail.status)}</sg-badge></dd></div>
              <div><dt>Events</dt><dd>{selectedRunDetail.event_count || 0}</dd></div>
              <div><dt>Live</dt><dd>{selectedRunDetail.live ? "Yes" : "No"}</dd></div>
              <div><dt>Provider</dt><dd>{selectedRunDetail.manifest?.provider || runById(selectedRunId)?.provider || "n/a"}</dd></div>
            </dl>
            <pre class="json-block">{jsonPreview(selectedRunDetail.manifest)}</pre>
          {:else if drawerTab === "evidence"}
            <pre class="json-block">{jsonPreview(selectedRunDetail.proof)}</pre>
          {:else if drawerTab === "trace"}
            <div class="event-list">
              {#each selectedRunDetail.events || [] as event}
                <pre>{jsonPreview(event)}</pre>
              {/each}
            </div>
          {:else}
            <pre class="json-block">{jsonPreview(selectedRunDetail.approvals)}</pre>
          {/if}
        {:else}
          <sg-empty class="empty-state" title="Run detail unavailable"></sg-empty>
        {/if}
      {:else if drawerKind === "pipeline" && selectedPipeline}
        {#if drawerTab === "summary"}
          <dl class="detail-list">
            <div><dt>ID</dt><dd>{selectedPipeline.id}</dd></div>
            <div><dt>Provider</dt><dd>{selectedPipeline.provider || "n/a"}</dd></div>
            <div><dt>Tasks</dt><dd>{selectedPipeline.tasks?.total || 0}</dd></div>
            <div><dt>Active</dt><dd>{selectedPipeline.tasks?.active || 0}</dd></div>
          </dl>
          <pre class="json-block">{jsonPreview(selectedPipeline.workflow)}</pre>
        {:else if drawerTab === "executions"}
          <div class="execution-list">
            {#each activeExecutions as execution}
              <div class="execution-card">
                <div>
                  <strong>{compactId(execution.id)}</strong>
                  <sg-badge color={statusColor(execution.status)} size="sm">{displayStatus(execution.status)}</sg-badge>
                </div>
                {#each execution.steps || [] as step}
                  <div class="mini-step">
                    <span>{step.name || step.id}</span>
                    <sg-badge color={statusColor(step.status)} size="sm">{displayStatus(step.status)}</sg-badge>
                  </div>
                {/each}
              </div>
            {:else}
              <sg-empty class="empty-state" title="No executions" description="Execute the workflow to populate this tab."></sg-empty>
            {/each}
          </div>
        {:else}
          <pre class="json-block">{jsonPreview(selectedPipeline)}</pre>
        {/if}
      {:else if drawerKind === "capability" && selectedCapability}
        {#if drawerTab === "summary"}
          <dl class="detail-list">
            <div><dt>Status</dt><dd><sg-badge color={statusColor(selectedCapability.setup?.status)}>{displayStatus(selectedCapability.setup?.status)}</sg-badge></dd></div>
            <div><dt>Provider</dt><dd>{selectedCapability.runtime?.provider || selectedCapability.provider || "n/a"}</dd></div>
            <div><dt>Sandbox</dt><dd>{displayStatus(selectedCapability.sandbox?.status)}</dd></div>
            <div><dt>Trust tier</dt><dd>{selectedCapability.sandbox?.trust_tier || "T1"}</dd></div>
          </dl>
          <pre class="json-block">{jsonPreview(selectedCapability.setup)}</pre>
        {:else if drawerTab === "files"}
          <div class="file-list">
            {#each selectedCapability.files || [] as file}
              <div>
                <strong>{file.label || file.kind}</strong>
                <span>{file.path}</span>
                <sg-badge color={file.present ? "success" : file.required ? "danger" : "ghost"} size="sm">
                  {file.present ? "present" : "missing"}
                </sg-badge>
              </div>
            {/each}
          </div>
        {:else if drawerTab === "work"}
          <pre class="json-block">{jsonPreview(selectedCapability.work)}</pre>
        {:else}
          <pre class="json-block">{jsonPreview(selectedCapability)}</pre>
        {/if}
      {:else if drawerKind === "task" && selectedTask}
        <dl class="detail-list">
          <div><dt>Status</dt><dd><sg-badge color={statusColor(selectedTask.status)}>{displayStatus(selectedTask.status)}</sg-badge></dd></div>
          <div><dt>Pool</dt><dd>{selectedTask.pool || "n/a"}</dd></div>
          <div><dt>Pipeline</dt><dd>{selectedTask.pipeline || "n/a"}</dd></div>
          <div><dt>Runs</dt><dd>{selectedTask.runs?.length || 0}</dd></div>
        </dl>
        <pre class="json-block">{jsonPreview(selectedTask)}</pre>
      {:else if drawerKind === "advanced"}
        {#if drawerTab === "sandbox"}
          <dl class="detail-list">
            <div><dt>Provider</dt><dd>{selectedCapability?.sandbox?.provider || "policy selected"}</dd></div>
            <div><dt>Trust tier</dt><dd>{selectedCapability?.sandbox?.trust_tier || "T1"}</dd></div>
            <div><dt>Status</dt><dd>{displayStatus(selectedCapability?.sandbox?.status || "ready")}</dd></div>
          </dl>
        {:else if drawerTab === "authority"}
          <pre class="json-block">{jsonPreview({ approvals: pipelineDraft.provider === "fake" ? "none" : "provider default", host_env: pipelineDraft.env_passthrough })}</pre>
        {:else if drawerTab === "evidence"}
          <pre class="json-block">{jsonPreview({ proof_endpoint: "/runs/:id/proof", proof_html: "/runs/:id/proof.html", trace_store: ".runs/<run-id>/events.jsonl" })}</pre>
        {:else}
          <pre class="json-block">{jsonPreview({ timeout_ms: pipelineDraft.timeout_ms, retry: "manual", queue: "interactive" })}</pre>
        {/if}
      {:else}
        <sg-empty class="empty-state" title="Select an item" description="Details open here."></sg-empty>
      {/if}
    </sg-stack>
    </sg-scroll-area-viewport>
    <sg-scroll-area-scrollbar orientation="vertical" />
    </sg-scroll-area-root>
  </sg-sheet-content>
  </sg-sheet-root>
</sg-sidebar-layout>

<script lang="ts" context="module">
  function drawerTabs(kind: string) {
    if (kind === "run") return ["summary", "evidence", "trace", "approvals"];
    if (kind === "pipeline") return ["summary", "executions", "definition"];
    if (kind === "capability") return ["summary", "files", "work", "runtime"];
    if (kind === "advanced") return ["sandbox", "authority", "evidence", "retry"];
    return ["summary"];
  }
</script>
