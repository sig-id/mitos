# 0007 – Shared sessions, explicit routing, independent supervision

Status: accepted. Design approved 2026-11-13; delivery tracked separately.

## Decision

Adopt [agent sessions and supervision](../system/08_AGENT_SESSIONS_AND_SUPERVISION.md)
as the owning contract for six increments: explicit bridge-backed sessions;
project autonomy and delegated approvals; portable cooperation; deterministic and
model-assisted routing; shared decision infrastructure; independent supervision.

Use coordinator for the planning role, worker for an assignee, supervisor for the
independent boundary monitor, and daemon only for a service process. Existing wire
names remain compatible. Flow control and recovery do not constitute supervision.

Routing does require classification when request metadata and explicit rules do
not determine the route. Use the existing typed decision adapters for that bounded
classification. A second bounded choice may answer the request, while a general
agent investigates open-ended problems. Deterministic authorization precedes both.

Keep soft guidance in AGENTS.md and optional cooperation guidance in COOPERATION.md;
keep executable grants in versioned project settings. Automatic resolution is a
delegated capability, not a permission inferred from prose or model confidence.

## Consequences

The existing bridge becomes a supported product entry point and the basis for
shared request handling. Current batch `codex exec` workers do not acquire live
interaction merely by adding an instruction file; their adapter must change.
The existing decision providers and graph choices are reused, not replaced.

A supervisor needs an independent policy, observations and stop authority. A
monitoring model cannot provide OS isolation. Strong prevention remains bounded by
the executor's actual interception and confinement capabilities.

This extends ADR 0003's initial exclusion of model-assisted approvals only when
explicitly delegated by project policy; it preserves runtime-owned eligibility,
durability, validation and the distinction between selection and acceptance.
