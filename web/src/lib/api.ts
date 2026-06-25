// Real backend client for the mitos control plane.
//
// Maps the prototype's agent-centric vocabulary onto the backend's
// application ⊃ pipeline ⊃ capability model:
//   Agent  ≈ application      (GET /apps)        — a worker definition/template
//   Target ≈ work-pool Task   (GET /work-pool)   — a unit of work
//   Run    ≈ app run          (GET /runs/:id)    — an execution + its trace
//   Connector/Sink ≈ I/O plane (GET /io/connectors, /io/sinks)
// All calls go through the same backend ops the CLI and connectors use.

export type Json = Record<string, any>;

export type Harness = { id: string; name: string; vendor: string; icon: string; color: string };

export type ConnectorClass = {
  name: string;
  capabilities: { ingress?: boolean; egress?: boolean; round_trip?: boolean };
};

export type SinkClass = {
  name: string;
  capabilities: { egress?: boolean; round_trip?: boolean };
};

export type Application = {
  id: string;
  app_id?: string;
  name: string;
  version?: string;
  description?: string;
  authors?: string[];
  license?: string;
  entrypoints?: Entrypoint[];
  pipelines?: Json[];
  capabilities?: Json[];
  io?: { inputs?: Json[]; outputs?: Json[] };
  services?: Json;
  requirements?: { harnesses?: string[]; connectors?: string[]; egress?: string[]; trust_tier?: string };
};

export type Entrypoint = {
  id: string;
  pipeline?: string;
  title?: string;
  description?: string;
  workspace?: { kind?: string; ref?: string; refspec?: string } | string;
  inputs?: { schema?: Json };
  parameters?: Parameter[];
  defaults?: Json;
};

export type Parameter = {
  name: string;
  type?: string;
  default?: unknown;
  description?: string;
};

export type RunSummary = {
  run_id: string;
  provider?: string;
  task?: string;
  status?: string;
  live?: boolean;
  events?: number;
  created_at_ms?: number;
  last_event_ms?: number;
};

export type Task = {
  id: string;
  status?: string;
  pipeline?: string;
  source?: string;
  payload?: Json;
  provenance?: Json;
};

export type RunDetail = {
  run_id: string;
  manifest?: Json;
  events?: Json[];
  event_count?: number;
  proof?: Json;
  [key: string]: any;
};

// ---- harness catalog (static for v1; mirrors the prototype's set) ----

export const harnesses: Harness[] = [
  { id: "claude", name: "Claude Code", vendor: "Anthropic", icon: "✶", color: "#d97757" },
  { id: "codex", name: "Codex", vendor: "OpenAI", icon: "⬡", color: "#10a37f" },
  { id: "gemini", name: "Gemini CLI", vendor: "Google", icon: "◆", color: "#4285f4" },
  { id: "cursor", name: "Cursor Agent", vendor: "Cursor", icon: "◢", color: "#a855f7" },
  { id: "local", name: "Local Runtime", vendor: "Self-hosted", icon: "◇", color: "#6b6b78" },
  { id: "opencode", name: "OpenCode", vendor: "SST", icon: "◈", color: "#5b6cff" }
];

export function harness(id?: string): Harness | undefined {
  return harnesses.find((h) => h.id === id);
}

// ---- fetch helpers ----

async function getJson<T = Json>(path: string): Promise<T> {
  const res = await fetch(path, { cache: "no-store" });
  const text = await res.text();
  const parsed = text ? JSON.parse(text) : {};
  if (!res.ok) {
    throw new Error((parsed as Json).error || `${res.status} ${res.statusText}`);
  }
  return parsed as T;
}

async function postJson<T = Json>(path: string, body?: Json): Promise<T> {
  const res = await fetch(path, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
    cache: "no-store"
  });
  const text = await res.text();
  const parsed = text ? JSON.parse(text) : {};
  if (!res.ok) {
    throw new Error((parsed as Json).error || `${res.status} ${res.statusText}`);
  }
  return parsed as T;
}

// ---- applications ----

export async function listApps(): Promise<Application[]> {
  const data = await getJson<{ apps: Application[] }>("/apps");
  return data.apps || [];
}

export async function getApp(id: string): Promise<Application> {
  const data = await getJson<{ app: Application }>(`/apps/${encodeURIComponent(id)}`);
  return data.app;
}

export async function addApp(source: string): Promise<Json> {
  return postJson<Json>("/apps", { source });
}

export async function runEntrypoint(
  appId: string,
  entrypointId: string,
  payload: {
    inputs?: Json;
    parameters?: Json;
    workspace?: Json;
    connector?: string;
    reply_ref?: string;
  }
): Promise<Json> {
  return postJson<Json>(
    `/apps/${encodeURIComponent(appId)}/entrypoints/${encodeURIComponent(entrypointId)}/runs`,
    payload
  );
}

// ---- I/O plane ----

export async function listConnectors(): Promise<ConnectorClass[]> {
  const data = await getJson<{ connectors: ConnectorClass[] }>("/io/connectors");
  return data.connectors || [];
}

export async function listSinks(): Promise<SinkClass[]> {
  const data = await getJson<{ sinks: SinkClass[] }>("/io/sinks");
  return data.sinks || [];
}

// ---- runs & work pool ----

export async function listRuns(): Promise<RunSummary[]> {
  const data = await getJson<{ runs: RunSummary[] }>("/runs");
  return data.runs || [];
}

export async function getRun(id: string): Promise<RunDetail> {
  return getJson<RunDetail>(`/runs/${encodeURIComponent(id)}`);
}

export async function steer(sessionId: string, interaction: string, body?: Json): Promise<Json> {
  return postJson<Json>(`/sessions/${encodeURIComponent(sessionId)}/steer`, {
    interaction,
    ...(body || {})
  });
}

export async function workPool(): Promise<{ tasks: Task[] }> {
  return getJson<{ tasks: Task[] }>("/work-pool");
}
