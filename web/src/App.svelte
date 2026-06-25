<script lang="ts">
  import { createRoute, go } from "./lib/router.svelte";
  import {
    listApps,
    listRuns,
    listConnectors,
    listSinks,
    addApp,
    type Application,
    type RunSummary,
    type ConnectorClass,
    type SinkClass,
    type Entrypoint
  } from "./lib/api";
  import Dashboard from "./pages/Dashboard.svelte";
  import Applications from "./pages/Applications.svelte";
  import Connectors from "./pages/Connectors.svelte";
  import Runs from "./pages/Runs.svelte";
  import AppDetail from "./pages/AppDetail.svelte";
  import LaunchWizard from "./lib/LaunchWizard.svelte";
  import RunDetail from "./lib/RunDetail.svelte";

  const route = createRoute();

  let apps = $state<Application[]>([]);
  let runs = $state<RunSummary[]>([]);
  let connectors = $state<ConnectorClass[]>([]);
  let sinks = $state<SinkClass[]>([]);
  let loading = $state(true);
  let error = $state("");
  let toast = $state("");

  let launchOpen = $state(false);
  let runDetailOpen = $state(false);
  let runDetailId = $state("");
  let poll = $state<ReturnType<typeof setInterval> | null>(null);

  async function refresh() {
    error = "";
    try {
      const [a, r, c, s] = await Promise.all([
        listApps(),
        listRuns(),
        listConnectors().catch(() => []),
        listSinks().catch(() => [])
      ]);
      apps = a;
      runs = r;
      connectors = c;
      sinks = s;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    refresh();
    poll = setInterval(refresh, 4000);
    return () => {
      if (poll) clearInterval(poll);
    };
  });

  let r = $derived(route.value);
  let view = $derived(r[0] || "dashboard");
  let selectedApp = $derived(view === "apps" && r[1] ? apps.find((a) => a.id === r[1]) || null : null);

  function openRun(id: string) {
    runDetailId = id;
    runDetailOpen = true;
  }

  async function onAdd(source: string) {
    try {
      const res = await addApp(source);
      toast = `Added ${(res as any).install?.app?.name || (res as any).install?.install_id || "application"}`;
      await refresh();
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    }
  }

  function launchFromApp(_a: Application, _e: Entrypoint) {
    launchOpen = true;
  }

  let activeNav = $derived(["dashboard", "agents", "connectors", "runs"].includes(view) ? view : "dashboard");
</script>

<svelte:head>
  <title>Mitos</title>
</svelte:head>

<div class="shell">
  <aside class="side">
    <div class="brand">
      <div class="logo">◇</div>
      <div>
        <div class="bn">Mitos</div>
        <div class="bs">Control plane</div>
      </div>
    </div>

    <nav>
      <button class="nav" class:on={activeNav === "dashboard"} onclick={() => go("dashboard")}>
        <span class="ni">▦</span> Dashboard
      </button>
      <button class="nav" class:on={activeNav === "agents"} onclick={() => go("agents")}>
        <span class="ni">✦</span> Applications
      </button>
      <button class="nav" class:on={activeNav === "connectors"} onclick={() => go("connectors")}>
        <span class="ni">⇄</span> Connectors
      </button>
      <button class="nav" class:on={activeNav === "runs"} onclick={() => go("runs")}>
        <span class="ni">⟳</span> Runs
      </button>
    </nav>

    <div class="section-label">Applications</div>
    <div class="app-list">
      {#each apps as a}
        <button class="app" onclick={() => go("apps/" + a.id)}>
          <span class="ad"></span>
          <span class="an">{a.name}</span>
        </button>
      {/each}
    </div>

    <div class="side-foot">
      <button class="launch" onclick={() => (launchOpen = true)}>
        <span>＋</span> Run agent
      </button>
      <div class="status">
        {#if loading}
          <span class="muted">Connecting…</span>
        {:else}
          <span class="ok"></span>
          <span class="muted">{apps.length} apps · {runs.length} runs</span>
        {/if}
      </div>
    </div>
  </aside>

  <main class="main">
    {#if error}
      <div class="banner danger">
        <strong>Request failed</strong>
        <span>{error}</span>
        <button onclick={() => (error = "")}>Dismiss</button>
      </div>
    {/if}
    {#if toast}
      <div class="banner success">
        <strong>{toast}</strong>
        <button onclick={() => (toast = "")}>Dismiss</button>
      </div>
    {/if}

    {#if activeNav === "dashboard"}
      <Dashboard {apps} {runs} onlaunch={() => (launchOpen = true)} onrun={openRun} />
    {:else if activeNav === "agents"}
      <Applications {apps} onlaunch={launchFromApp} onadd={onAdd} />
    {:else if activeNav === "connectors"}
      <Connectors {connectors} {sinks} />
    {:else if activeNav === "runs"}
      <Runs {runs} onrun={openRun} />
    {:else if view === "apps" && r[1]}
      <AppDetail
        app={selectedApp}
        {runs}
        onlaunch={() => (launchOpen = true)}
        onrun={openRun}
      />
    {:else}
      <Dashboard {apps} {runs} onlaunch={() => (launchOpen = true)} onrun={openRun} />
    {/if}
  </main>
</div>

<LaunchWizard bind:open={launchOpen} {apps} onlaunched={(runId) => { if (runId) openRun(runId); }} />

<RunDetail bind:open={runDetailOpen} bind:runId={runDetailId} />

<style>
  .shell {
    display: flex;
    height: 100%;
    overflow: hidden;
  }
  .side {
    width: 240px;
    flex: none;
    background: var(--mt-bg-elev);
    border-right: 1px solid var(--mt-border);
    display: flex;
    flex-direction: column;
    padding: 18px 14px;
    gap: 6px;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 6px 8px 18px;
  }
  .logo {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    background: linear-gradient(135deg, var(--mt-accent), #b06cff);
    display: flex;
    align-items: center;
    justify-content: center;
    color: #fff;
    font-size: 16px;
    font-weight: 700;
    box-shadow: 0 4px 12px var(--mt-accent-glow);
  }
  .bn {
    font-weight: 700;
    font-size: 14px;
    letter-spacing: -0.01em;
  }
  .bs {
    font-size: 10px;
    color: var(--mt-text-muted);
  }
  nav {
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .nav {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 8px 10px;
    border-radius: var(--mt-radius-sm);
    font-size: 13px;
    color: var(--mt-text-dim);
    transition: all 0.12s;
    text-align: left;
  }
  .nav:hover {
    background: var(--mt-bg-hover);
    color: var(--mt-text);
  }
  .nav.on {
    background: var(--mt-accent-soft);
    color: var(--mt-accent-hover);
  }
  .ni {
    width: 16px;
    text-align: center;
    font-size: 13px;
    opacity: 0.85;
  }
  .section-label {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--mt-text-faint);
    margin: 18px 10px 6px;
  }
  .app-list {
    display: flex;
    flex-direction: column;
    gap: 1px;
    overflow-y: auto;
    flex: 1;
  }
  .app {
    display: flex;
    align-items: center;
    gap: 9px;
    padding: 7px 10px;
    border-radius: var(--mt-radius-sm);
    font-size: 12px;
    color: var(--mt-text-dim);
    transition: all 0.12s;
    text-align: left;
  }
  .app:hover {
    background: var(--mt-bg-hover);
    color: var(--mt-text);
  }
  .ad {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--mt-text-faint);
    flex: none;
  }
  .an {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .side-foot {
    margin-top: auto;
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .launch {
    display: flex;
    align-items: center;
    justify-content: center;
    gap: 6px;
    padding: 10px;
    border-radius: var(--mt-radius);
    background: var(--mt-accent);
    color: #fff;
    font-weight: 500;
    font-size: 13px;
    transition: background 0.15s;
    box-shadow: 0 4px 14px var(--mt-accent-glow);
  }
  .launch:hover {
    background: var(--mt-accent-hover);
  }
  .status {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 10px;
    font-size: 11px;
  }
  .ok {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--mt-green);
  }
  .main {
    flex: 1;
    overflow-y: auto;
  }
  .banner {
    margin: 16px 32px 0;
    padding: 10px 14px;
    border-radius: var(--mt-radius);
    display: flex;
    align-items: center;
    gap: 12px;
    font-size: 13px;
  }
  .banner.danger {
    background: var(--mt-red-soft);
    border: 1px solid rgba(248, 113, 113, 0.25);
    color: #fca5a5;
  }
  .banner.success {
    background: var(--mt-green-soft);
    border: 1px solid rgba(74, 222, 128, 0.25);
    color: #86efac;
  }
  .banner span {
    flex: 1;
  }
  .banner button {
    font-size: 12px;
    color: inherit;
    opacity: 0.8;
  }
  .banner button:hover {
    opacity: 1;
  }
  .muted {
    color: var(--mt-text-muted);
  }
</style>
