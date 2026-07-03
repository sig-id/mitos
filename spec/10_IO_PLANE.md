# 10 — I/O Plane (Connectors In · Sinks Out)

This document specifies the **I/O plane** — the previously-missing layer that
gives mitos a *world-facing input and output*. The substrate had **sources-in**
for developer trackers (`06_WORK_POOL §6`) but **no first-class outputs**, and
ingress was limited to GitHub/Linear/kanban shapes. The I/O plane makes
**input and output first-class, platform-level, and application-agnostic**.

One line:

> **Connectors bring the world in; sinks send results back out; the web app is
> just one connector + one sink.**

It elaborates `01_ARCHITECTURE` §3 (layer stack) and §8.2/§8.7 (Effects, Task),
reuses `06_WORK_POOL` (triggers, services, multi-tenancy) for scheduling, and is
careful **not** to be confused with the in-sandbox egress broker (`05`, §5 below).
Where this doc and `01` disagree, `01` wins. Field names from `01` §8 are canonical
and MUST NOT be renamed. RFC 2119 keywords are normative. Connectors and sinks are external integrations behind their respective behaviours (`01` §0).

---

## 1. The gap and the thesis

mitos today can *run* work and *record* it, but it cannot be *reached* by the
world or *deliver* to it except through a developer poking the API. Three things
are unspecified:

1. **Ingress beyond dev trackers.** Chat apps, email, calendar events, generic
   webhooks/events — the everyday ways a non-developer would *start* work.
2. **A scheduling/service surface a user actually operates.** `06 §8` has the
   systemd-like runtime; what was missing is the *I/O around it*.
3. **Any output at all.** A run harvests a `{diff, result, artifacts}` (`05 §5.4`)
   and stops. Nothing carries that outcome back to a human or a system —
   no notification, no delivery, no reply.

The I/O plane closes all three. Crucially, **I/O lives at the platform, not
inside each application.** A capability's pillars stay `(DATA + SKILL + TOOLS)`
(`03`); connectors and sinks are *bindings around* an application, so the same
pipeline can be triggered by Slack today and by email tomorrow without editing
the recipe. (An application MAY ship a *usage-specific* connector — e.g. a CAD
tool feed — but the generic ingress/egress classes are platform-provided; §3.4.)

---

## 2. Position in the layer stack

The I/O plane wraps the existing stack (`01 §3`) with an **ingress** edge before
the work pool and an **egress** edge after harvest:

```
        ┌──────────────────────── I/O PLANE (this doc) ────────────────────────┐
        │ INGRESS  connectors: chat · email · calendar · webhook · event · web  │
        │   normalize → Task (01 §8.7) / trigger event (06 §6.1)   [§3]         │
        ▼                                                                        │
  WORK POOL (06) ─► DISPATCH ─► ORCHESTRATOR (04) ─► CAPABILITY in sandbox (05) │
        │                                              harvest {diff,result,art} │
        ▼                                                       │                 │
        │ EGRESS  sinks: notify · deliver · reply · web   ◄─────┘   [§5]         │
        │   read event log + artifacts → render → deliver → log delivery Effect  │
        └───────────────────────────────────────────────────────────────────────┘
```

- **Ingress** is the input side of `01 §3`'s TRIGGERS/SOURCES row, generalized.
- **Egress** is *new*: a control-plane delivery step keyed off the run's
  terminal Effects. It is **not** the in-sandbox broker (§5.1).
- Both edges run in the **BEAM control plane** (`01 §7`); neither runs inside the
  sandbox, and neither puts secrets in it (`01 §8.5`).

---

## 3. Ingress — connectors & triggers

### 3.1 What a connector is

A **connector** is a platform-level adapter that turns an external signal into a
unit of mitos work. It is the generalization of `06 §6.2`'s tracker connectors
(GitHub/Linear/kanban) into a full ingress class.

| Connector | External signal | Becomes |
|---|---|---|
| `webhook` | inbound HTTP POST | a Task (`01 §8.7`) or trigger event (`06 §6.1`) |
| `chat` (Slack/Discord/…) | message / slash-command / mention | a Task; thread ref kept for round-trip reply (§5.4) |
| `email` | inbound message to an address | a Task; message-id kept for reply |
| `calendar` | event start / reminder | a trigger event |
| `event` | an Effect on the log matches a subscription | a trigger event (`06 §6.1` `event`) |
| `tracker` (GitHub/Linear/kanban) | issue/card created/updated | a Task (`06 §6.2`, KEPT) |
| `manual` / `web` | operator UI / API / CLI | a Task (the web app is a connector, §6) |

Every connector resolves to the **same durable unit — a Task** — enqueued into a
pool, never run directly (`06 §6`). One pool, many provenances; `task.source`
already carries `connector:<name>` / `trigger:<id>` (`01 §8.7`).

### 3.2 Connector behaviour

```elixir
@behaviour Mitos.Connector

mount(config)                 :: {:ok, handle}    # bind credentials (control-plane vault), endpoints, scope
receive(handle, raw)          :: {:ok, [signal]}  # parse + authenticate an inbound delivery
normalize(signal)             :: {:ok, intake}    # → %{intent, inputs, workspace?, reply_ref?, idempotency_key}
ack(handle, signal, outcome)  :: :ok              # protocol-level ack to the source (e.g. 200, Slack ack)
capabilities()                :: %{ingress: true, round_trip?: bool, auth: _, scopes: _}
```

- `normalize/1` MUST produce an **idempotency key** from the source delivery id;
  re-delivery of the same signal MUST NOT create a duplicate Task (dedup on
  enqueue, cf. `06 §6.3`).
- `reply_ref` (a chat thread, an email message-id, a webhook callback URL) is
  carried onto the Task so a **sink** can round-trip the result back (§5.4).
- Connector credentials live in the **control-plane vault** (BEAM, `01 §7`), the
  same custody rule as the broker (`01 §8.5`): they are platform secrets and
  **never enter the sandbox**.

### 3.3 The selection step — intent → pipeline [OPEN]

Ingress produces an *intent + inputs*; something must choose **which pipeline**
runs (the user always runs a pipeline, never a bare capability — `11 §2`).
Selection is a **pluggable step** in the input layer:

| Mode | How the pipeline is chosen | Default |
|---|---|---|
| `explicit` | the connector binding names the pipeline (e.g. this webhook → `resize-and-deliver`) | **yes** |
| `smart` | a classifier/orchestrator picks from the application's entry pipelines by intent | opt-in |

This mirrors `06 §2`'s `agent_pick` discipline: fuzzy/agentic selection is
**opt-in**, never the only path. `explicit` is the v1 default; `smart` is an
experiment to validate before committing (an open question, §8).

### 3.4 Where connectors live — platform vs application

| Connector class | Lives at | Examples |
|---|---|---|
| generic ingress | **platform** (shared, per-tenant scoped) | chat, email, calendar, webhook, web |
| usage-specific ingress | **the application** (shipped with it) | a CAD feed, a proprietary device stream |

Generic connectors are configured as **bindings** (§7), not baked into a
capability. An application MAY declare a usage-specific connector it needs; the
platform still mediates credentials and enqueue (no secrets in the sandbox).

### 3.5 Scheduling & services — reuse `06`, don't re-spec it

Connectors feed the existing trigger types (`06 §6.1`: `cron · interval ·
continuous · event · webhook · external_signal · manual`); triggers + schedules
drive **services** (`06 §8`, systemd-like units); services run pipelines. The I/O
plane owns *the connectors and the bindings*; `06` owns *the pools, services,
disciplines, and the drainer*. A connector is **not** a trigger — it is a source
that produces Tasks/events the service runtime acts on (`06 §1`'s rejected
"triggers-in-pipeline" note holds).

---

## 4. The application's I/O binding

An I/O binding wires an application/pipeline to connectors and sinks **without
touching its capabilities**. Bindings are declared at the **application or
service** level (`11 §2` hierarchy), per-tenant scoped (`06 §12`):

```yaml
# app-level (or service-level) I/O binding — NOT inside a capability
io:
  inputs:
    - connector: webhook
      path: /apps/resize/runs
      select: { mode: explicit, pipeline: resize-and-deliver }   # §3.3
    - connector: chat
      channel: "#design-requests"
      select: { mode: smart, from: [resize-and-deliver, critique] }
  outputs:
    - sink: chat                 # round-trip: reply in the same thread (§5.4)
      on: capability_finished
      reply_to: input.reply_ref
      render: result.summary + artifacts
    - sink: webhook
      on: run.done
      url: $CALLBACK
      payload: { result, artifacts, proof_excerpt }
```

The binding consumes only canonical contracts: connectors/sinks by name, the
pipeline ref (`04`), the Task `reply_ref` (§3.2), run/Effect lifecycle (`01 §8.2`).

---

## 5. Egress — sinks & notifications (the genuinely new part)

### 5.1 A sink is NOT the egress broker — keep them apart

This is the load-bearing distinction. Two different outbound mechanisms, two
different trust postures:

| | **Egress broker** (`05 §5.3`) | **Sink** (this plane) |
|---|---|---|
| When | *during* the run | *after* harvest (or on lifecycle Effects) |
| Who calls out | the capability, from **inside** the sandbox | the **control plane** (BEAM), outside the sandbox |
| Carries | the capability's own network calls (e.g. it hits `api.figma.com`) | the run's **outcome** (result, artifacts, notification) |
| Secrets | attached at the sandbox edge per `egress_policy` | sink credentials in the control-plane vault |
| Governs | exfiltration risk of untrusted code | delivery of finished work to the world |

A sink **never** reaches into the sandbox and is **never** an authority path for
the capability. It reads the **event log + harvested artifacts** and delivers.

### 5.2 What a sink is

A **sink** delivers a run's outcome to an external destination, triggered by a
run/Task lifecycle Effect (`01 §8.2`).

| Sink | Delivers | Example |
|---|---|---|
| `notify` | a human-facing message | "your resize job is done" to chat / email / push |
| `deliver` | the artifact payload | POST `{result, artifacts}` to a webhook; write to object store |
| `reply` | round-trip back to the originating connector | reply in the same Slack thread / email (§5.4) |
| `web` | the web app surface | live trace + the deliverable page (`11 §4`, `11 §5`) |

### 5.3 Sink behaviour

```elixir
@behaviour Mitos.Sink

bind(config)                  :: {:ok, handle}    # credentials (control-plane vault), destination, scope
match(effect)                 :: bool             # does this Effect trigger delivery? (e.g. capability_finished)
render(run_state, binding)    :: {:ok, payload}   # fold log + fetch artifacts → destination-shaped payload
deliver(handle, payload)      :: {:ok, receipt} | {:error, reason}
capabilities()                :: %{egress: true, round_trip?: bool, retries: _}
```

- `render/2` reads **folded run state + harvested artifacts** only — the same
  source as the proof page (`08 §5`). Buyer-facing renders MUST redact recipe
  (`08 §5`: `rationale` is audit-only); platform/dev renders MAY show more.
- **Every delivery is an Effect** (extends the open list of `01 §8.2`):
  `delivery_attempted | delivery_sent | delivery_failed`, with `caused_by` the
  terminal Decision/Effect and `ref` the receipt. Delivery is therefore on the
  log — auditable, replayable, and metered like every other crossing.
- Delivery is **at-least-once with idempotency**: a `delivery_sent` keyed by
  `(run_id, sink, dest)` suppresses duplicates on re-drive (cf. `04 §7.1`).

### 5.4 Round-trip (connector ⇄ sink)

A connector and a sink can be two ends of one conversation. Slack message → run
→ reply in the same thread:

```
chat connector: message in #design-requests  ──► Task{ reply_ref: thread_ts }
   ─► select pipeline (§3.3) ─► run (04) ─► harvest (05 §5.4)
   ─► reply sink: render result ─► deliver to thread_ts ─► delivery_sent Effect
```

The `reply_ref` carried at ingress (§3.2) is what the `reply` sink delivers back
to. A connector whose `capabilities().round_trip?` is true MAY back a `reply`
sink; the binding (§4) pairs them.

---

## 6. The web app is one connector + one sink

The web app is **not special** — it is an I/O surface like the others:

- as a **connector** (`manual`/`web`): state intent + inputs → a Task; trigger a
  run; the fastest path to "do this now."
- as a **sink** (`web`): the live run trace (`11 §4`), receipts, and result
  cards; it *is* the notification/delivery surface for a human at a screen.

Treating the web app as I/O (not as a privileged client) keeps `01 §10.11` true —
UI, CLI, API, and connectors all call the same backend ops — and means the
operate/watch experience (`11 §8`) is a *rendering of the same plane* a Slack
reply or a webhook POST uses.

---

## 7. Multi-tenancy, custody, metering

- **Scoping.** Connectors, sinks, and bindings are **per (org, project)**
  (`06 §12`, `08 §3.1`). A tenant's binding is invisible to others.
- **Custody.** Connector and sink credentials live in the **control-plane vault**
  (`01 §7`), never in the sandbox (`01 §8.5`). Same rule as the broker; different
  vault scope (`(tenant, connector|sink, dest)`).
- **Metering & audit.** `signal_received`, `task_enqueued` (ingress) and
  `delivery_attempted|sent|failed` (egress) are Effects on the log — the I/O plane
  adds **no parallel ledger**; deliveries fold into the same billing/audit view as
  everything else (`08 §4`).

---

## 8. Scope and open questions

The I/O plane's scope is exactly what a **paid job** needs: `webhook` + `manual`/`web` ingress and `reply`/`deliver`/`notify`/`web` sinks around the `08` flow. Chat/email/calendar connector breadth is commodity (Eve ships channels free, `13 §1`) and is built only against a paying buyer's concrete demand, never for parity. Remote T2 delivery context runs through the T2 `SandboxProvider` (`05 §3`). An imported Eve agent's `channels/*` (`03 §10.1`) maps here as a *binding* (§4), not as new connector code.



| What this plane owns | Delegates |
|---|---|
| connectors (ingress), sinks (egress), bindings, the selection step | pools/services/disciplines/drainer (`06`); run/orchestration (`04`) |
| control-plane credential custody for I/O | in-sandbox authority/broker (`05`); take/metering fold (`08`) |
| the web-app-as-I/O framing | the web app's *presentation* (`11`) |

**Open questions:**
1. **Selection (§3.3):** is `smart` worth it, or does `explicit` + many bindings
   cover real use? Decide by trying `explicit` first and measuring.
2. **Delivery guarantees (§5.3):** at-least-once + idempotency is the floor; do
   any sinks need exactly-once (and is that even achievable for chat/email)?
3. **Connector auth model:** per-tenant OAuth app vs bring-your-own-token, per
   connector class — pairs with the broker vault design (`05`, `08 §3.2`).
4. **Application-shipped connectors (§3.4):** the sandbox/trust posture for a
   usage-specific connector an untrusted app ships — likely must run control-plane
   side, not in-sandbox; confirm against `05`'s T2 constraints.
