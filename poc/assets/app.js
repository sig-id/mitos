/* mitos run viewer.
 *
 * Surfaces (spec/01_PRODUCT.md §7.2, §9.4; spec/07_GENTIX_FACILITATOR.md §6):
 *   #/runs       — run lifecycle list over GET /runs (live + historical)
 *   #/runs/:id   — evidence viewer: terminal (live WS), timeline, approvals, report
 *   #/new        — launch surface: POST /agents (provider) or POST /sessions (raw argv)
 *   #/workloads  — packaged capabilities: GET /workloads, POST /workloads/:id/execute
 *
 * The run is the primary noun; the terminal is one tab — one transport view,
 * not the spine.
 */

// ---- tiny DOM + format helpers ---------------------------------------------

function el(tag, attrs = {}, children = []) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = value;
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else node.setAttribute(key, value);
  }
  for (const child of [].concat(children)) {
    if (child == null) continue;
    node.append(child.nodeType ? child : document.createTextNode(child));
  }
  return node;
}

function fmtAge(ms) {
  if (!ms) return "";
  const delta = Date.now() - ms;
  if (delta < 0) return "now";
  const s = Math.floor(delta / 1000);
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function fmtClock(ms) {
  return ms ? new Date(ms).toLocaleTimeString() : "";
}

function compact(value, max = 200) {
  if (value === undefined || value === null) return "";
  const text = typeof value === "string" ? value : JSON.stringify(value);
  return text.length > max ? text.slice(0, max) + "…" : text;
}

async function api(path, options) {
  const resp = await fetch(path, options);
  const text = await resp.text();
  let data = {};
  try { data = text ? JSON.parse(text) : {}; } catch (_) {}
  if (!resp.ok) throw new Error(data.error || `request failed: ${resp.status}`);
  return data;
}

function postJson(path, body) {
  return api(path, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });
}

// ---- event taxonomy ----------------------------------------------------------

const LIFECYCLE = new Set([
  "hello", "RUN_STARTED", "PROCESS_SPAWNED", "PROCESS_EXITED", "exit",
  "sidecar_exit", "SNAPSHOT", "SANDBOX_EVENT",
]);
const OUTPUT = new Set(["STRUCTURED_OUTPUT", "FINAL_OUTPUT", "TERMINAL_OUTPUT"]);
const POLICY = new Set(["ACTION_REQUESTED", "AUTHORITY_VERDICT"]);

function eventGroup(event) {
  const type = event.type || "";
  if (type === "ERROR") return "error";
  if (type === "RAW_REF") return "raw";
  if (POLICY.has(type)) return "policy";
  if (type === "PROMPT_DETECTED") {
    return /approval|permission|question/.test(event.prompt_kind || "") ? "policy" : "output";
  }
  if (OUTPUT.has(type)) return "output";
  if (LIFECYCLE.has(type)) return "lifecycle";
  return "lifecycle";
}

function eventDetail(event) {
  switch (event.type) {
    case "RUN_STARTED":
      return [event.provider, event.transport, event.codec].filter(Boolean).join(" · ");
    case "PROCESS_SPAWNED":
      return `pid=${event.pid ?? "n/a"} cwd=${event.cwd ?? "."}`;
    case "PROCESS_EXITED":
    case "exit":
      return event.code !== undefined ? `code=${event.code}` : `signal=${event.signal ?? "n/a"}`;
    case "RAW_REF":
      return `offset=${event.offset} len=${event.len} ${event.path || ""}`.trim();
    case "PROMPT_DETECTED":
      return `${event.prompt_kind || "prompt"}: ${compact(event.text)}`.trim();
    case "ACTION_REQUESTED":
      return `${event.action || "action"} ${compact(event.context)}`.trim();
    case "AUTHORITY_VERDICT":
      return `${event.decision || "verdict"} ${compact(event.reason)}`.trim();
    case "STRUCTURED_OUTPUT":
      return `${event.kind || event.stream || "structured"} ${compact(event.bytes ?? event.text ?? "")}`;
    case "FINAL_OUTPUT":
    case "TERMINAL_OUTPUT":
      return compact(event.text, 400);
    case "SNAPSHOT":
      return `seq=${event.seq ?? 0} ${event.cols ?? "?"}x${event.rows ?? "?"}`;
    case "ERROR":
      return `${event.kind || "error"} ${compact(event.detail)}`.trim();
    default:
      return compact(event);
  }
}

function statusChipClass(status) {
  if (status === "running") return "chip warn";
  if (status === "completed") return "chip good";
  if (status === "failed" || status === "error") return "chip bad";
  return "chip";
}

// ---- app shell / router --------------------------------------------------------

const viewRoot = document.getElementById("view");
let cleanups = [];

function onCleanup(fn) { cleanups.push(fn); }

function navigateRender() {
  for (const fn of cleanups.splice(0)) {
    try { fn(); } catch (_) {}
  }
  viewRoot.replaceChildren();

  const hash = location.hash || "#/runs";
  const parts = hash.slice(2).split("/").filter(Boolean);
  const [section, arg] = parts;

  document.querySelectorAll("#nav a").forEach((a) => {
    a.classList.toggle("active", a.dataset.nav === (section || "runs"));
  });

  if (section === "runs" && arg) renderRunDetail(decodeURIComponent(arg));
  else if (section === "new") renderNewRun();
  else if (section === "workloads") renderWorkloads();
  else renderRuns();
}

window.addEventListener("hashchange", navigateRender);

// daemon health, sidebar footer
async function pollDaemon() {
  const dot = document.getElementById("daemon-dot");
  const text = document.getElementById("daemon-text");
  try {
    await fetch("/healthz");
    dot.className = "dot good";
    text.textContent = `daemon · ${location.host}`;
  } catch (_) {
    dot.className = "dot bad";
    text.textContent = "daemon unreachable";
  }
}
setInterval(pollDaemon, 10_000);
pollDaemon();

// ---- view: runs list -------------------------------------------------------------

function renderRuns() {
  const list = el("div", { class: "run-table" });
  const page = el("div", { class: "page" }, [
    el("div", { class: "page-head" }, [
      el("h1", { text: "Runs" }),
      el("span", { class: "muted", text: "every run is a durable, auditable trace" }),
      el("span", { class: "spacer" }),
      el("a", { href: "#/new" }, el("button", { class: "primary", text: "New run" })),
    ]),
    list,
  ]);
  viewRoot.append(page);

  async function refresh() {
    let runs;
    try {
      ({ runs } = await api("/runs"));
    } catch (error) {
      list.replaceChildren(el("div", { class: "empty", text: `failed to load runs: ${error.message}` }));
      return;
    }

    if (!runs.length) {
      list.replaceChildren(el("div", { class: "empty" }, [
        "No runs yet. ",
        el("a", { href: "#/new", text: "Launch the first one" }),
        " — the trace lands in .runs/ either way.",
      ]));
      return;
    }

    list.replaceChildren(...runs.map((run) =>
      el("a", { class: "run-row", href: `#/runs/${encodeURIComponent(run.run_id)}` }, [
        el("span", { class: `dot ${run.live ? "live" : run.status === "completed" ? "good" : ""}` }),
        el("span", { class: `status ${run.status}`, text: run.status }),
        el("span", { class: "rid", text: run.run_id }),
        el("span", { class: "task", text: run.task || "(no task)" }),
        el("span", { class: "counts", text: `${run.provider || "?"} · ${run.events} ev · ${run.approvals} appr` }),
        el("span", { class: "age", text: fmtAge(run.last_event_ms || run.created_at_ms) }),
      ])));
  }

  refresh();
  const timer = setInterval(refresh, 4000);
  onCleanup(() => clearInterval(timer));
}

// ---- view: run detail --------------------------------------------------------------

function renderRunDetail(runId) {
  const state = {
    runId,
    live: false,
    status: "loading",
    events: [],            // [{timestamp_ms, event}]
    approvals: new Map(),  // request_id -> {request, verdict}
    filters: new Set(["lifecycle", "output", "policy", "error"]),
    socket: null,
    term: null,
    tab: null,
  };

  // header ------------------------------------------------------------------
  const statusChip = el("span", { class: "chip", text: "loading" });
  const transportChip = el("span", { class: "chip", text: "" });
  transportChip.style.display = "none";
  const taskSpan = el("span", { class: "task" });
  const interruptBtn = el("button", { class: "small", text: "Interrupt", onclick: () => steer({ interaction: "interrupt" }) });
  const stopBtn = el("button", { class: "small danger", text: "Stop", onclick: () => wsSend({ type: "signal", signum: 15 }) });

  const head = el("div", { class: "run-head" }, [
    el("a", { href: "#/runs", class: "dim", text: "← runs" }),
    el("h1", { text: runId }),
    statusChip,
    transportChip,
    taskSpan,
    interruptBtn,
    stopBtn,
  ]);

  // tabs ----------------------------------------------------------------------
  const approvalsBadge = el("span", { class: "badge", text: "0" });
  approvalsBadge.style.display = "none";
  const tabBar = el("div", { class: "tabs" });
  const body = el("div", { class: "run-body" });

  const panels = {
    terminal: buildTerminalPanel(),
    timeline: buildTimelinePanel(),
    approvals: buildApprovalsPanel(),
    report: buildReportPanel(),
  };

  const tabButtons = {};
  for (const [key, label] of [
    ["terminal", "Terminal"],
    ["timeline", "Timeline"],
    ["approvals", "Approvals"],
    ["report", "Report"],
  ]) {
    tabButtons[key] = el("button", { text: label, onclick: () => selectTab(key) });
    if (key === "approvals") tabButtons[key].append(approvalsBadge);
    tabBar.append(tabButtons[key]);
  }

  function selectTab(key) {
    state.tab = key;
    for (const [k, btn] of Object.entries(tabButtons)) btn.classList.toggle("active", k === key);
    body.replaceChildren(panels[key].node);
    panels[key].show?.();
  }

  viewRoot.append(el("div", { class: "run-page" }, [head, tabBar, body]));

  // panels --------------------------------------------------------------------

  function buildTerminalPanel() {
    const holder = el("div", { id: "terminal-holder" });
    const placeholder = el("div", {
      class: "empty",
      style: "margin: 14px",
      text: "no raw terminal stream yet — this run is on a structured transport; the typed event stream is in Timeline",
    });
    state.onFirstBytes = () => { placeholder.style.display = "none"; };
    const steerInput = el("input", { placeholder: "steer the agent — instruction or answer…" });
    const steerKind = el("select", {}, [
      el("option", { value: "prompt", text: "prompt" }),
      el("option", { value: "answer", text: "answer" }),
    ]);
    const sendBtn = el("button", {
      class: "primary small",
      text: "Send",
      onclick: () => {
        const text = steerInput.value.trim();
        if (!text) return;
        steer({ interaction: steerKind.value, text });
        steerInput.value = "";
      },
    });
    steerInput.addEventListener("keydown", (ev) => { if (ev.key === "Enter") sendBtn.click(); });

    const bar = el("div", { class: "steer-bar" }, [
      el("button", { class: "good small", text: "Approve", onclick: () => steer({ interaction: "approve" }) }),
      el("button", { class: "danger small", text: "Deny", onclick: () => steer({ interaction: "deny" }) }),
      steerKind,
      steerInput,
      sendBtn,
      el("div", { class: "steer-note", text: "Semantic steering goes through the runtime's per-provider keymap (POST /sessions/:id/steer); typing in the terminal sends raw bytes." }),
    ]);

    state.updateSteerBar = () => {
      const active = state.live && state.status === "running";
      for (const control of bar.querySelectorAll("button, input, select")) control.disabled = !active;
    };

    const node = el("div", { class: "tab-panel fill" }, [
      el("div", { style: "min-height:0;display:grid;grid-template-rows:auto minmax(0,1fr)" }, [placeholder, holder]),
      bar,
    ]);

    return {
      node,
      show() {
        if (!state.live) return;
        if (!state.term) {
          state.term = new Terminal({
            cursorBlink: true,
            fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
            fontSize: 13,
            theme: {
              background: "#0b0a09",
              foreground: "#ece7dd",
              cursor: "#d7a64a",
              selectionBackground: "#3a2b12",
            },
            allowProposedApi: true,
          });
          state.fit = new FitAddon.FitAddon();
          state.term.loadAddon(state.fit);
          state.term.open(holder);
          state.term.onData((data) => {
            if (state.socket?.readyState === 1) state.socket.send(new TextEncoder().encode(data));
          });
          if (state.pendingBytes?.length) state.onFirstBytes?.();
          for (const chunk of state.pendingBytes ?? []) state.term.write(chunk);
          state.pendingBytes = [];
        }
        requestAnimationFrame(() => refit());
      },
    };
  }

  function refit() {
    if (!state.term || !state.fit) return;
    try { state.fit.fit(); } catch (_) { return; }
    wsSend({ type: "resize", cols: state.term.cols, rows: state.term.rows });
  }
  window.addEventListener("resize", refit);
  onCleanup(() => window.removeEventListener("resize", refit));

  function buildTimelinePanel() {
    const listNode = el("ol", { class: "timeline" });
    const groups = ["lifecycle", "output", "policy", "error", "raw"];
    const filterRow = el("div", { class: "filter-row" },
      groups.map((group) => {
        const btn = el("button", {
          text: group,
          onclick: () => {
            state.filters.has(group) ? state.filters.delete(group) : state.filters.add(group);
            btn.classList.toggle("on", state.filters.has(group));
            redrawTimeline();
          },
        });
        btn.classList.toggle("on", state.filters.has(group));
        return btn;
      }));

    const node = el("div", { class: "tab-panel" }, [filterRow, listNode]);

    function redrawTimeline() {
      const shown = state.events.filter((rec) => state.filters.has(eventGroup(rec.event)));
      listNode.replaceChildren(...shown.slice(-400).map(timelineItem));
      if (!shown.length) listNode.replaceChildren(el("div", { class: "empty", text: "no events match the filters" }));
    }

    return { node, show: redrawTimeline, redraw: redrawTimeline };
  }

  function timelineItem(record) {
    const event = record.event || {};
    return el("li", { class: `tl-event ${eventGroup(event)}` }, [
      el("div", { class: "tl-top" }, [
        el("span", { class: "tl-type", text: event.type || "EVENT" }),
        el("span", { class: "tl-time", text: fmtClock(record.timestamp_ms) }),
      ]),
      el("div", { class: "tl-detail", text: eventDetail(event) }),
    ]);
  }

  function buildApprovalsPanel() {
    const listNode = el("div", { class: "tab-panel" });

    function redraw() {
      const cards = [...state.approvals.values()].reverse().map(approvalCard);
      listNode.replaceChildren(
        ...(cards.length ? cards : [el("div", { class: "empty", text: "no approval requests in this run" })]),
      );
      const pending = pendingApprovals().length;
      approvalsBadge.textContent = String(pending);
      approvalsBadge.style.display = pending ? "" : "none";
    }

    return { node: listNode, show: redraw, redraw };
  }

  function pendingApprovals() {
    return [...state.approvals.values()].filter((a) => !a.verdict);
  }

  // Approval card per spec §9.4: action, source, risk, raw context, decision.
  function approvalCard({ request, verdict }) {
    const id = request.request_id || request.action_id;
    const resolved = Boolean(verdict);
    const risk = (request.risk || "medium").toLowerCase();

    const card = el("div", { class: `approval-card${resolved ? " resolved" : ""}` }, [
      el("div", { class: "approval-head" }, [
        el("strong", { text: request.action || "action" }),
        el("span", { class: `risk ${risk}`, text: risk }),
        resolved
          ? el("span", { class: verdict.decision === "allow" ? "chip good" : "chip bad", text: verdict.decision || "resolved" })
          : el("span", { class: "chip warn", text: "pending" }),
      ]),
      el("div", { class: "approval-meta" }, [
        el("span", { class: "k", text: "source" }), el("span", { class: "v", text: request.origin || request.source || "unknown" }),
        el("span", { class: "k", text: "method" }), el("span", { class: "v", text: request.method || "—" }),
        el("span", { class: "k", text: "request id" }), el("span", { class: "v", text: id || "—" }),
        ...(resolved && verdict.reason
          ? [el("span", { class: "k", text: "reason" }), el("span", { class: "v", text: compact(verdict.reason, 300) })]
          : []),
      ]),
      request.context ? el("pre", { class: "block", text: compact(request.context, 1200) }) : null,
    ]);

    if (!resolved && state.live && id) {
      card.append(el("div", { class: "actions" }, [
        el("button", { class: "good", text: "Allow once", onclick: () => wsSend({ type: "authorize", action_id: id, token: "ui-" + Date.now() }) }),
        el("button", { class: "danger", text: "Deny", onclick: () => wsSend({ type: "deny", action_id: id, reason: "denied from viewer" }) }),
        el("button", { class: "danger", text: "Terminate run", onclick: () => wsSend({ type: "signal", signum: 15 }) }),
      ]));
    }
    return card;
  }

  function buildReportPanel() {
    const node = el("div", { class: "tab-panel" });

    function redraw() {
      const finals = state.events.filter((r) => r.event?.type === "FINAL_OUTPUT");
      const counts = { events: state.events.length, approvals: state.approvals.size };
      const manifest = state.manifest || {};

      node.replaceChildren(
        el("div", { class: "metric-row" }, [
          metric("status", state.status),
          metric("events", counts.events),
          metric("approvals", counts.approvals),
          metric("provider", manifest.provider || "?"),
        ]),
        el("div", { class: "card" }, [
          el("h2", { text: "Final output" }),
          finals.length
            ? el("pre", { class: "block", text: finals.map((r) => r.event.text || "").join("\n---\n") })
            : el("span", { class: "dim", text: state.live ? "run still in progress" : "no final output captured" }),
        ]),
        el("div", { class: "card" }, [
          el("h2", { text: "Manifest" }),
          el("div", { class: "kv" }, Object.entries(manifest).flatMap(([k, v]) => [
            el("span", { class: "k", text: k }),
            el("span", { class: "v", text: String(v) }),
          ])),
        ]),
        el("div", { class: "card" }, [
          el("h2", { text: "Trace artifacts" }),
          el("div", { class: "kv" }, [
            el("span", { class: "k", text: "directory" }),
            el("span", { class: "v", text: `.runs/${runId}/` }),
            el("span", { class: "k", text: "files" }),
            el("span", { class: "v", text: "run.yaml · events.jsonl · approvals.jsonl · commands.jsonl · terminal.snapshots.jsonl · test_results.jsonl · artifacts/" }),
          ]),
        ]),
      );
    }

    function metric(k, v) {
      return el("div", { class: "metric" }, [
        el("div", { class: "k", text: k }),
        el("div", { class: "v", text: String(v) }),
      ]);
    }

    return { node, show: redraw, redraw };
  }

  // data + live wiring -------------------------------------------------------------

  function ingestEvent(record) {
    state.events.push(record);
    const event = record.event || {};
    if (event.type === "ACTION_REQUESTED") {
      const id = event.request_id || event.action_id || `anon-${state.events.length}`;
      state.approvals.set(id, { request: event, verdict: state.approvals.get(id)?.verdict });
    } else if (event.type === "AUTHORITY_VERDICT") {
      const id = event.request_id || event.action_id;
      if (id) state.approvals.set(id, { request: state.approvals.get(id)?.request || event, verdict: event });
    }
    if (event.transport) {
      transportChip.textContent = event.transport;
      transportChip.style.display = "";
    }
    if (event.type === "exit" || event.type === "PROCESS_EXITED") {
      setStatus(event.code === 0 ? "completed" : "failed");
    }
    if (event.type === "ERROR") setStatus("error");
  }

  function setStatus(status) {
    state.status = status;
    statusChip.textContent = status;
    statusChip.className = statusChipClass(status);
    const live = state.live && status === "running";
    interruptBtn.style.display = live ? "" : "none";
    stopBtn.style.display = live ? "" : "none";
    state.updateSteerBar?.();
  }

  // The disk trace is canonical (the orchestrator is the canonical trace
  // writer); a live WS only carries events broadcast after it subscribed.
  // Re-reading the trace closes the fetch→subscribe gap and settles the
  // final state when the run ends.
  async function resync() {
    let run;
    try {
      run = await api(`/runs/${encodeURIComponent(runId)}`);
    } catch (_) {
      return;
    }
    state.manifest = run.manifest || {};
    state.events = [];
    state.approvals.clear();
    for (const record of run.events || []) ingestEvent(record);
    if (!state.live) setStatus(run.status);
    redrawActive();
  }

  function redrawActive() {
    panels.timeline.redraw?.();
    panels.approvals.redraw?.();
    if (state.tab === "report") panels.report.redraw();
  }

  function wsSend(payload) {
    if (state.socket?.readyState === 1) state.socket.send(JSON.stringify(payload));
  }

  async function steer(body) {
    try {
      await postJson(`/sessions/${encodeURIComponent(runId)}/steer`, body);
    } catch (error) {
      ingestEvent({ timestamp_ms: Date.now(), event: { type: "ERROR", kind: "steer", detail: error.message } });
      redrawActive();
    }
  }

  function connect() {
    const proto = location.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(`${proto}//${location.host}/ws/${encodeURIComponent(runId)}`);
    socket.binaryType = "arraybuffer";
    state.socket = socket;
    state.pendingBytes = [];

    socket.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        let msg;
        try { msg = JSON.parse(ev.data); } catch (_) { return; }
        if (msg.type === "event") {
          ingestEvent({ timestamp_ms: Date.now(), event: msg.event || {} });
          redrawActive();
        }
      } else {
        state.onFirstBytes?.();
        if (state.term) state.term.write(new Uint8Array(ev.data));
        else state.pendingBytes.push(new Uint8Array(ev.data));
      }
    };
    socket.onopen = () => setTimeout(resync, 800);
    socket.onclose = () => {
      state.live = false;
      setStatus(state.status === "running" ? "interrupted" : state.status);
      // The final trace appends can land just after the socket dies; resync
      // once soon and once more after things settle.
      setTimeout(resync, 600);
      setTimeout(resync, 2500);
    };
    onCleanup(() => { socket.onclose = null; try { socket.close(); } catch (_) {} });
  }

  onCleanup(() => { state.term?.dispose(); });

  (async () => {
    let run;
    try {
      run = await api(`/runs/${encodeURIComponent(runId)}`);
    } catch (error) {
      body.replaceChildren(el("div", { class: "page" },
        el("div", { class: "empty", text: `run not found: ${error.message}` })));
      setStatus("unknown");
      return;
    }

    state.live = run.live;
    state.manifest = run.manifest || {};
    taskSpan.textContent = state.manifest.task || "";
    for (const record of run.events || []) ingestEvent(record);
    setStatus(run.live ? "running" : run.status);

    if (run.live) connect();
    else tabButtons.terminal.disabled = true;

    selectTab(run.live ? "terminal" : "report");
    panels.approvals.redraw();
  })();
}

// ---- view: new run -------------------------------------------------------------------

function renderNewRun() {
  let mode = "agent";
  const errorLine = el("div", { class: "form-error" });

  // agent form
  const provider = el("select", {}, ["claude", "codex", "gemini", "fake"].map((p) => el("option", { value: p, text: p })));
  const task = el("textarea", { placeholder: "What should the agent do? The runtime owns the lifecycle; the agent proposes." });
  const cwd = el("input", { value: "." });
  const transportStdio = el("input", { type: "checkbox", checked: "" });
  const transportPty = el("input", { type: "checkbox", checked: "" });
  const envJson = el("textarea", { class: "code", spellcheck: "false", text: "{}" });

  // raw form
  const argvJson = el("textarea", { class: "code", spellcheck: "false", text: '["/bin/sh","-i"]' });
  const rawCwd = el("input", { value: "." });
  const rawEnvJson = el("textarea", { class: "code", spellcheck: "false", text: "{}" });

  const agentFields = el("div", { class: "form-grid" }, [
    el("div", { class: "grid-2" }, [
      el("label", { class: "field" }, ["Provider", provider]),
      el("label", { class: "field" }, ["Working directory", cwd]),
    ]),
    el("label", { class: "field" }, ["Task", task]),
    el("label", { class: "field" }, [
      "Transport preference (structured first; PTY is the compatibility fallback)",
      el("div", { class: "checks" }, [
        el("label", { class: "check" }, [transportStdio, "stdio-json"]),
        el("label", { class: "check" }, [transportPty, "pty"]),
      ]),
    ]),
    el("label", { class: "field" }, ["Extra env (JSON object)", envJson]),
  ]);

  const rawFields = el("div", { class: "form-grid" }, [
    el("label", { class: "field" }, ["argv (JSON array) — raw escape hatch, no provider knowledge", argvJson]),
    el("div", { class: "grid-2" }, [
      el("label", { class: "field" }, ["Working directory", rawCwd]),
      el("label", { class: "field" }, ["Extra env (JSON object)", rawEnvJson]),
    ]),
  ]);
  rawFields.style.display = "none";

  const agentBtn = el("button", { class: "segmented-agent active", text: "Agent run" });
  const rawBtn = el("button", { text: "Raw command" });
  agentBtn.addEventListener("click", () => setMode("agent"));
  rawBtn.addEventListener("click", () => setMode("raw"));

  function setMode(next) {
    mode = next;
    agentBtn.classList.toggle("active", mode === "agent");
    rawBtn.classList.toggle("active", mode === "raw");
    agentFields.style.display = mode === "agent" ? "" : "none";
    rawFields.style.display = mode === "raw" ? "" : "none";
  }

  const launchBtn = el("button", { class: "primary", text: "Launch run" });
  launchBtn.addEventListener("click", async () => {
    errorLine.textContent = "";
    launchBtn.disabled = true;
    try {
      let result;
      if (mode === "agent") {
        const order = [
          ...(transportStdio.checked ? ["stdio-json"] : []),
          ...(transportPty.checked ? ["pty"] : []),
        ];
        result = await postJson("/agents", {
          provider: provider.value,
          task: task.value,
          cwd: cwd.value || ".",
          env: JSON.parse(envJson.value || "{}"),
          transport_order: order.length ? order : undefined,
          cols: 120,
          rows: 32,
        });
      } else {
        const argv = JSON.parse(argvJson.value || "[]");
        if (!Array.isArray(argv) || !argv.length) throw new Error("argv must be a non-empty JSON array");
        result = await postJson("/sessions", {
          argv,
          cwd: rawCwd.value || ".",
          env: JSON.parse(rawEnvJson.value || "{}"),
          cols: 120,
          rows: 32,
        });
      }
      location.hash = `#/runs/${encodeURIComponent(result.session_id)}`;
    } catch (error) {
      errorLine.textContent = error.message;
    } finally {
      launchBtn.disabled = false;
    }
  });

  viewRoot.append(el("div", { class: "page" }, [
    el("div", { class: "page-head" }, [
      el("h1", { text: "New run" }),
      el("span", { class: "muted", text: "sandboxed, policied, traced — the agent executes, the runtime decides" }),
    ]),
    el("div", { class: "form-grid" }, [
      el("div", { class: "segmented" }, [agentBtn, rawBtn]),
      agentFields,
      rawFields,
      errorLine,
      el("div", { class: "actions" }, [launchBtn]),
    ]),
  ]));
}

// ---- view: workloads -----------------------------------------------------------------

function renderWorkloads() {
  const grid = el("div", { class: "workload-grid" });
  viewRoot.append(el("div", { class: "page" }, [
    el("div", { class: "page-head" }, [
      el("h1", { text: "Workloads" }),
      el("span", { class: "muted", text: "packaged capabilities: the recipe stays server-side, only outputs leave" }),
    ]),
    grid,
  ]));

  (async () => {
    let workloads;
    try {
      ({ workloads } = await api("/workloads"));
    } catch (error) {
      grid.replaceChildren(el("div", { class: "empty", text: `failed to load workloads: ${error.message}` }));
      return;
    }
    if (!workloads.length) {
      grid.replaceChildren(el("div", { class: "empty", text: "No workloads defined. Drop a definition JSON into workloads/." }));
      return;
    }
    grid.replaceChildren(...workloads.map(workloadCard));
  })();

  function workloadCard(workload) {
    const inputs = {};
    const fields = (workload.placeholders || []).map((name) => {
      inputs[name] = name === "objective" || name === "expected_output"
        ? el("textarea", { class: "code", placeholder: name })
        : el("input", { placeholder: name });
      return el("label", { class: "field" }, [name, inputs[name]]);
    });

    const resultHolder = el("div");
    const runBtn = el("button", { class: "primary", text: "Execute" });

    runBtn.addEventListener("click", async () => {
      runBtn.disabled = true;
      runBtn.textContent = "Running… (sandboxed agent job)";
      resultHolder.replaceChildren();
      try {
        const bindings = Object.fromEntries(Object.entries(inputs).map(([k, node]) => [k, node.value]));
        const result = await postJson(`/workloads/${encodeURIComponent(workload.id)}/execute`, { inputs: bindings });
        resultHolder.replaceChildren(proofPanel(result));
      } catch (error) {
        resultHolder.replaceChildren(el("div", { class: "form-error", text: error.message }));
      } finally {
        runBtn.disabled = false;
        runBtn.textContent = "Execute";
      }
    });

    return el("div", { class: "card workload-card" }, [
      el("div", { class: "meta" }, [
        el("span", { class: "chip", text: workload.id }),
        el("span", { class: "chip", text: workload.provider }),
        el("span", { class: "chip", text: `${Math.round((workload.timeout_ms || 0) / 1000)}s cap` }),
      ]),
      el("strong", { text: workload.title || workload.id }),
      el("div", { class: "desc", text: workload.description || "" }),
      ...fields,
      el("div", { class: "actions" }, [runBtn]),
      resultHolder,
    ]);
  }

  // Deliverable + evidence — the seed of the buyer-facing proof page.
  function proofPanel(result) {
    const evidence = result.evidence || {};
    return el("div", { class: "proof" }, [
      el("h3", { text: "Deliverable" }),
      el("pre", { class: "block", text: result.deliverable?.result || result.deliverable_summary || "(empty)" }),
      el("h3", { text: "Evidence" }),
      el("div", { class: "kv" }, [
        el("span", { class: "k", text: "run" }),
        el("span", { class: "v" }, evidence.run_id
          ? el("a", { href: `#/runs/${encodeURIComponent(evidence.run_id)}`, text: evidence.run_id })
          : "—"),
        el("span", { class: "k", text: "exit code" }),
        el("span", { class: "v", text: String(evidence.exit_code ?? "—") }),
        el("span", { class: "k", text: "errors" }),
        el("span", { class: "v", text: (evidence.errors || []).join("; ") || "none" }),
        el("span", { class: "k", text: "executed by" }),
        el("span", { class: "v", text: result.model || "—" }),
      ]),
    ]);
  }
}

// ---- boot ---------------------------------------------------------------------------

navigateRender();
