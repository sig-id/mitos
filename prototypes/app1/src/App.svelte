<script>
  import { route, go } from './lib/router.js'
  import Dashboard from './pages/Dashboard.svelte'
  import Agents from './pages/Agents.svelte'
  import Connectors from './pages/Connectors.svelte'
  import Runs from './pages/Runs.svelte'
  import AppDetail from './pages/AppDetail.svelte'
  import LaunchWizard from './lib/LaunchWizard.svelte'
  import TargetDetail from './lib/TargetDetail.svelte'
  import { apps } from './lib/data.js'
  import { get } from 'svelte/store'

  let launchOpen = false
  let selectedTarget = null
  let targetOpen = false

  $: r = $route
  $: allApps = get(apps)

  function openLaunch() { launchOpen = true }
  function onTarget(t) { selectedTarget = t; targetOpen = true }
</script>

<div class="shell">
  <aside class="side">
    <div class="brand">
      <div class="logo">◇</div>
      <div>
        <div class="bn">Mitos</div>
        <div class="bs">Control Plane</div>
      </div>
    </div>

    <nav>
      <button class="nav" class:on={r[0] == null || r[0]==='dashboard'} on:click={() => go('dashboard')}>
        <span class="ni">▦</span> Dashboard
      </button>
      <button class="nav" class:on={r[0]==='agents'} on:click={() => go('agents')}>
        <span class="ni">✦</span> Agent library
      </button>
      <button class="nav" class:on={r[0]==='connectors'} on:click={() => go('connectors')}>
        <span class="ni">⇄</span> Connectors
      </button>
      <button class="nav" class:on={r[0]==='runs'} on:click={() => go('runs')}>
        <span class="ni">⟳</span> Runs
      </button>
    </nav>

    <div class="section-label">Live applications</div>
    <div class="app-list">
      {#each allApps as a}
        <button class="app" on:click={() => go('apps/'+a.id)}>
          <span class="ad" class:live={a.status==='running'}></span>
          <span class="an">{a.name}</span>
        </button>
      {/each}
    </div>

    <div class="side-foot">
      <button class="launch" on:click={openLaunch}>
        <span>＋</span> Launch agent
      </button>
      <div class="user">
        <div class="av">M</div>
        <div class="ui">
          <div class="un">Maya</div>
          <div class="ue">maya@mitos.dev</div>
        </div>
      </div>
    </div>
  </aside>

  <main class="main">
    {#if r[0] == null || r[0] === 'dashboard'}
      <Dashboard on:launch={openLaunch} on:target={e => onTarget(e.detail)} />
    {:else if r[0] === 'agents'}
      <Agents on:launch={openLaunch} on:pick={() => openLaunch()} />
    {:else if r[0] === 'connectors'}
      <Connectors />
    {:else if r[0] === 'runs'}
      <Runs />
    {:else if r[0] === 'apps' && r[1]}
      <AppDetail appId={r[1]} />
    {:else}
      <Dashboard on:launch={openLaunch} on:target={e => onTarget(e.detail)} />
    {/if}
  </main>
</div>

<LaunchWizard open={launchOpen}
              on:close={() => launchOpen = false}
              on:launched={() => launchOpen = false} />

{#if selectedTarget}
  <TargetDetail open={targetOpen} target={selectedTarget}
                on:close={() => targetOpen = false} />
{/if}

<style>
  .shell { display:flex; height:100vh; overflow:hidden; }

  .side { width:240px; flex:none; background: var(--bg-elev); border-right:1px solid var(--border);
          display:flex; flex-direction:column; padding:18px 14px; gap:6px; }
  .brand { display:flex; align-items:center; gap:10px; padding:6px 8px 18px; }
  .logo { width:32px;height:32px;border-radius:8px; background: linear-gradient(135deg, var(--accent), #b06cff);
          display:flex;align-items:center;justify-content:center; color:#fff; font-size:16px; font-weight:700;
          box-shadow: 0 4px 12px var(--accent-glow); }
  .bn { font-weight:700; font-size:14px; letter-spacing:-0.01em; }
  .bs { font-size:10px; color: var(--text-muted); }

  nav { display:flex; flex-direction:column; gap:2px; }
  .nav { display:flex; align-items:center; gap:10px; padding:8px 10px; border-radius: var(--radius-sm);
         font-size:13px; color: var(--text-dim); transition: all .12s; text-align:left; }
  .nav:hover { background: var(--bg-hover); color: var(--text); }
  .nav.on { background: var(--accent-soft); color: var(--accent-hover); }
  .ni { width:16px; text-align:center; font-size:13px; opacity:.85; }

  .section-label { font-size:10px; text-transform:uppercase; letter-spacing:.08em; color: var(--text-faint);
                   margin:18px 10px 6px; }
  .app-list { display:flex; flex-direction:column; gap:1px; overflow-y:auto; flex:1; }
  .app { display:flex; align-items:center; gap:9px; padding:7px 10px; border-radius: var(--radius-sm);
         font-size:12px; color: var(--text-dim); transition: all .12s; text-align:left; }
  .app:hover { background: var(--bg-hover); color: var(--text); }
  .ad { width:6px;height:6px;border-radius:50%; background: var(--text-faint); flex:none; }
  .ad.live { background: var(--green); box-shadow: 0 0 6px var(--green); }
  .an { white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }

  .side-foot { margin-top:auto; display:flex; flex-direction:column; gap:12px; }
  .launch { display:flex; align-items:center; justify-content:center; gap:6px; padding:10px;
            border-radius: var(--radius); background: var(--accent); color:#fff; font-weight:500; font-size:13px;
            transition: background .15s; box-shadow: 0 4px 14px var(--accent-glow); }
  .launch:hover { background: var(--accent-hover); }
  .user { display:flex; align-items:center; gap:10px; padding:8px; border-radius: var(--radius);
          background: var(--bg); border:1px solid var(--border); }
  .av { width:28px;height:28px;border-radius:50%; background: linear-gradient(135deg,#f472b6,#7c6cff);
        display:flex;align-items:center;justify-content:center; color:#fff; font-weight:600; font-size:12px; }
  .un { font-size:12px; font-weight:500; }
  .ue { font-size:10px; color: var(--text-muted); }

  .main { flex:1; overflow-y:auto; }
</style>
