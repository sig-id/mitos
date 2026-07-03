# 12 — Application (Packaging, Manifest, and the Unit of Sale)

The **agentic application** is the *shipping unit* (`11 §2`) — and, post the
commerce repositioning (`02 §1`), the **unit of sale**: what a developer
publishes, what a tenant adds, what a seller prices. This document fixes its
**manifest**, **identity**, **packaging**, the **identity/offer boundary**, and
the **import path** — and it *references*, never redefines, the capability
(`03`), pipeline (`04`), I/O (`10`), service (`06`), and facilitator (`08`)
contracts. The canonical skeleton lives in `01 §8.8`; this doc elaborates it,
exactly as `03` elaborates the capability manifest (`01 §8.3`).

Where this doc and `01` disagree, `01` wins. Field names from `01 §8` are
canonical and MUST NOT be redefined here. RFC 2119 keywords are normative.

---

## 1. What an application is — four units, one bundle

First principles. The spec set already fixes three units; the application
closes the commercial loop with a fourth:

| Unit | Unit of | Fixed by |
|---|---|---|
| **capability** | execution + metering | `01 §1`, `03`, `08 §4` |
| **pipeline** | composition | `04` |
| **application** | **shipping + sale** | this doc; `08 §8` |
| **entrypoint** | **invocation + purchase** | §4; `08 §7` |

> An **application** is a content-addressed bundle of one or more **pipelines**
> (`04`) + the **capabilities** (`03`) they reference + an **application
> manifest** that names the runnable **entrypoints**, the **default I/O
> bindings** (`10`), and the **service composition** (`06`). It is packaged as
> an OCI artifact (§9), and it is what a seller publishes and a buyer's paid
> run enters (§4, §8).

```
   APPLICATION  (the shipping unit & unit of sale, OCI artifact, immutable §7)
   ┌──────────────────────────────────────────────────────────────┐
   │  app.yaml  (this manifest)                                     │
   │   ├─ entrypoints ───────► which pipelines a user/buyer runs    │
   │   ├─ io (default) ──────► connectors in / sinks out  (10 §4)   │
   │   └─ services ──────────► composition: requires/wants (06 §9)  │
   │                                                                │
   │  pipelines/*.md  (04 §8, loose markdown)                       │
   │   └─ compose ▼                                                 │
   │  capabilities[]  (03, pinned by capability_id / digest §7)     │
   │   └─ (DATA + SKILL + TOOLS) atoms under an injected harness     │
   └──────────────────────────────────────────────────────────────┘
```

The hierarchy is **application ⊃ pipelines ⊃ capabilities** (`11 §2`). A user
runs a **pipeline** named by an entrypoint, never a bare capability. The
simplest valid application is one entrypoint → one single-step pipeline → one
capability.

**Alignment with `08`.** The facilitator's endpoint
(`/capabilities/{id}/runs`, `08 §8`) is exactly this degenerate form — one
capability wrapped in a one-step pipeline behind one entrypoint. The general
published paid surface is an **application entrypoint**; the flow and contracts
(Task `01 §8.7`, x402 admission `08 §7`) are unchanged either way.

---

## 2. The manifest (annotated)

A worked **image-resize** application, continuing the `resize-and-deliver`
example of `06 §13` / `10 §4`. The manifest is structured (YAML/JSON) — unlike
the *pipelines* it ships, which stay loose markdown (`04 §8`). Field skeleton:
`01 §8.8`. Note what is **absent**: no price, no payout, no credentials — the
offer is facilitator-side (§8), secrets are tenant-side (`01 §8.5`, `10 §7`).

```yaml
name: image-resize-app
version: 1.2.0                         # human label; identity = app_id digest (§7)
description: Resize + watermark an image and deliver it with a proof page.
authors:  [ "design-tools@acme" ]      # metadata only
license:  Apache-2.0                   # optional

capabilities:                          # §3 — the pinned atoms (03 §9 identity)
  - { ref: oci://reg.mitos/image-resize@sha256:a1b2…,   as: image-resize }
  - { ref: oci://reg.mitos/watermark@sha256:c3d4…,      as: watermark }
  - { ref: oci://reg.mitos/deliver-proof@sha256:e5f6…,  as: deliver-proof }

pipelines:                             # §1 — markdown pipelines (04 §8), bundled
  - { id: resize-and-deliver, file: ./pipelines/resize-and-deliver.md }

entrypoints:                           # §4 — the runnable surface + order form
  - id: resize
    pipeline: resize-and-deliver
    title: Resize an image
    description: Upload an image and target dimensions; get a resized, watermarked file.
    workspace: { kind: dataset }        # {kind: repo|dataset|none, ref?, refspec?} (01 §8.7 payload.workspace)
    inputs:                            # JSON Schema (abbrev.) → Task payload.inputs  (the SUBJECT data)
      schema: { image: { type: file }, width: { type: int }, height: { type: int } }
    parameters:                        # §4.1 — per-run KNOBS (how to run), typed, with defaults
      - { name: format, type: string, default: webp, description: "Output format: webp|png|jpg." }
      - { name: keep_original, type: boolean, default: false, description: "Retain the unresized input." }
    defaults:                          # optional run defaults (04 §8.1 / 01 §8.7)
      budget: { usd: 2, wall_ms: 300000 }
      constraints: [ "stay within sRGB" ]

io:                                    # §5 — DEFAULT bindings (10 §4); tenant overrides at install
  inputs:
    - { connector: webhook, path: /apps/resize/runs, entrypoint: resize,
        select: { mode: explicit } }
    - { connector: chat, entrypoint: resize, select: { mode: explicit } }   # reply_ref kept (10 §3.2)
  outputs:
    - { sink: reply, on: capability_finished, entrypoint: resize,
        reply_to: input.reply_ref, render: "result.summary + artifacts" }
    - { sink: web,   on: run.done, entrypoint: resize }                       # deliverable page (11 §5)

services:                              # §6 — composition root (06 §9–10)
  requires: [ render-pool ]            # activating the app pulls these in
  wants:    [ usage-meter ]

requirements:                          # install-time transparency / admission (§11)
  harnesses:  [ claude-code, codex ]   # union across capabilities; must be available
  connectors: [ webhook, chat ]        # what the io block needs bound
  egress:     [ "api.figma.com" ]      # union of capability egress dests (dest only, no creds)
  trust_tier: T1                       # declared aggregate (informational; per-cap authority wins §11)
  resources:  { cpu: 2, mem: 4Gi }     # aggregate hint
```

Every field references an existing contract; the manifest **wires**, it does
not re-specify.

---

## 3. Capabilities — the pinned atoms

`capabilities[]` lists the atoms the app's pipelines compose, each with:

- `ref` — a **capability reference**: a content-addressed `oci://…@sha256` (a
  pinned `capability_id`, `03 §9`) when published, or a **local path** during
  dev (resolved to a digest at publish, §7).
- `as` — a stable **alias** the pipelines use, so a pipeline's
  `allowed_capabilities` / stage names (`04 §8.1`) bind to a pinned atom
  regardless of registry path.

An app **pins** exact capability versions; a capability upgrade is a new app
version with a new digest (§7). The same capability MAY be referenced by many
apps (content-addressed dedup) — it is not embedded, only referenced. An
imported capability (`03 §10.1`) is a local directory like any other and is
pinned the same way at publish.

---

## 4. Entrypoints — the runnable surface + the order form

Not every pipeline is user-facing; some are internal (e.g. invoked as subtasks,
`06 §5`). `entrypoints[]` declares the pipelines a user or connector MAY run —
and **how to collect their input**. An entrypoint is one contract worn three
ways:

1. the **Run form** a screen renders (`11 §4`);
2. the **connector payload** an I/O binding validates (`10 §3`);
3. the **order form of a purchase** — what a Gentix quote validates and prices
   before admission (`08 §7`).

An entrypoint maps directly onto a **Task** (`01 §8.7`) at run time. Its input
contract is **three kinds of thing a caller supplies**, kept distinct because
they are collected, defaulted, and trusted differently:

```
run(entrypoint=resize, workspace=W, inputs=I, parameters=P)  ⇒  Task {
    pipeline : <entrypoint.pipeline>,                           # 01 §8.7 pipeline
    payload  : { intent, workspace: W, inputs: I, parameters: P,   # 01 §8.7 payload
                 constraints, success_criteria },
    budget   : <entrypoint.defaults.budget | tenant default>,
    source   : human | connector:<name> | trigger:<id> | purchase:<id>   # who ran it (10 §3, 08 §7)
}  ⇒ dispatch ⇒ run (04)
```

| Field | Is | Supplied by | Defaulted? |
|---|---|---|---|
| **`workspace`** | the **data substrate** the run operates on — a repo or dataset | the caller (a git URL/path + optional refspec), resolved + cloned by `prepare` (`01 §7`, `05 §5.4`) | no (when `kind != none`) |
| **`inputs`** | the run's **subject data** — *what* to work on (a file, a task id, a diff command) | the caller, validated against `inputs.schema` | per JSON Schema `required` |
| **`parameters`** | per-run **knobs** — *how* to run (output format, severity floor, parallelism) | the caller, validated against `parameters[]` | **yes — each has a `default`** |

- `inputs.schema` is **JSON Schema** (aligned with the tool I/O dialect
  question, `03 §11.4`); it lets the platform render a form, validate a
  connector payload, and reject a malformed run before any model spend.
- **Validate before quote, quote before run.** A payload that fails
  `inputs.schema`/`parameters` MUST be rejected **before** the 402 quote
  (`08 §7`): no escrow is taken for an un-runnable order, and no model is spent
  on a malformed one. The typed order form is what makes a price computable
  up front — flat directly, metered as an escrow ceiling seeded from
  `defaults.budget`.
- `parameters[]` is a flat array of `{ name, type, default?, description? }`
  using the same type vocabulary as `inputs.schema`. A caller MAY omit any
  parameter; the entrypoint's `default` applies (§4.1).
- `workspace: { kind, ref?, refspec? }` matches `01 §8.7`'s payload workspace.
  `kind` is declared by the entrypoint (`repo | dataset | none`);
  `ref`/`refspec` are supplied at run time. Resolution (clone/checkout) happens
  **server-side in `prepare`**, so the browser/connector only ever passes a ref
  — **it never handles git credentials** (creds stay in the control-plane
  vault, `01 §8.5`, `10 §7`). A bare `kind` string (`workspace: repo`) is
  shorthand for `{ kind: repo }`.
- `defaults` seed the run's `budget`/`constraints`; a caller MAY override
  within tenant limits (`08 §3.1` budget ceilings). `parameters` defaults are a
  separate, narrower mechanism and do not overlap with `defaults`.

### 4.1 Why data and parameters are separate

Conflating them (one flat `inputs` bag) hides the distinction a Run form, a
connector, *and a quote* all need: the **subject data** is required,
run-specific, and rarely has a sane default (which repo? which image?), while
**parameters** are optional knobs with sensible defaults tweaked per run.
Splitting them lets the frontend render a *workspace picker*, a *data* section,
and a *parameters* section; lets a connector omit parameters entirely and
supply only the data; and gives the quote a stable shape to price against. At
execution the two merge into the capability's template bindings, so a
pipeline `{{placeholder}}` resolves from either.

---

## 5. I/O bindings — ship defaults, bind at install (two phase)

The `io` block is the `10 §4` binding, scoped per `entrypoint` and shipped as
**defaults**. Binding is two-phase so an app is portable while credentials stay
tenant-side:

| Phase | Who | What |
|---|---|---|
| **ship** | app author | declares *which connector/sink classes* and *how to render/select* (`io` block) — **no credentials, no tenant specifics** |
| **bind** | tenant at install | supplies concrete connector instances + credentials (control-plane vault, `10 §7`); MAY override/disable any default binding |

An application with an empty `io` block is still valid — runnable manually (the
`manual`/`web` connector, `10 §6`) with the `web` sink; an operator can add
bindings later. `io` defaults are **suggestions**, never authority.

---

## 6. Services — the application as a composition root

`services` lets the app declare the units/targets it needs (`06 §9–10`).
**Activating an application acts as a target** (`06 §10`, lifted to the app):
its `requires`/`wants` union with its pipelines' own `requires` blocks and feed
the active-set resolver (`06 §9.5`). Conflicts and the "why is it on" view
(`06 §9.3–9.4`) apply to that union. The app never authors raw `internal` units
(the tenancy boundary, `06 §11`); it references `pipeline`/`capability`
services and named targets only.

---

## 7. Identity, immutability, dev → publish

**Identity is content, not the label** — mirroring `03 §9`:

```
app_id = H( canonical(app_manifest) ‖ sorted(capability_id …) ‖ sorted(pipeline_sha256 …) )
```

- `version` (e.g. `1.2.0`) is a human label; `app_id` is what the event log,
  install records, attribution (`08 §6`), and replay key on. Same label,
  different content ⇒ **different application**.
- A published application MUST be **immutable**: pinned capability digests +
  pipeline file hashes + a frozen manifest. A change to any member is a new
  `app_id`. This is what makes an installed app reproducible, a run replayable
  (`01 §4`), and a seller's revenue attributable per version (`08 §6`).

**Dev → publish resolution** (`11 §6`):

```
DEV (directory)                         PUBLISH (one freeze point)
 app.yaml + pipelines/*.md       ──►    build each capability dir → OCI image (03 §9) ⇒ capability_id
 capabilities/<name>/ (a 03 dir)        resolve capabilities[].ref  path → @sha256 digest
 capabilities[].ref = ./path            hash pipelines, canonicalize manifest ⇒ app_id
 (run/test from disk, no build)         push the app as an OCI artifact (§9)
```

Locally a capability `ref` MAY be a path; **publish resolves every path to a
digest** and computes `app_id`. Identity exists only after publish; the daily
dev loop runs from disk (`11 §6`).

---

## 8. The offer — price is not content

The commercial boundary, first-principles: **identity answers "what runs";
the offer answers "what it costs."** They MUST NOT be conflated:

```
identity = app_id                                   # content-addressed (§7)
           what ran — the replay, metering, and attribution key
offer    = { price: flat | metered (08 §4.2),
             payout_addr, terms?, availability }    # facilitator-side, mutable
           what it costs — attached at Gentix publish (08 §8, steps 4/6)
```

- The offer **MUST NOT appear in the application manifest**. `01 §8.8` defines
  no such fields, and this doc keeps it that way: repricing MUST NOT mint a new
  `app_id`; changing content MUST.
- A quote (`08 §7`) binds `(app_id, entrypoint, offer_rev)`; the buyer's
  receipt/proof pins `app_id` + `capability_ids` — so a buyer can verify *what
  exactly ran* independent of any price history.
- Consequences: two digests MAY carry different offers (version pricing); a
  price experiment is an offer change, not a release; the attribution read
  (`08 §6`) compares digests, not offers.

---

## 9. Packaging onto OCI

An application publishes as an **OCI artifact** that **bundles** the manifest +
pipeline markdown and **references** capability images by digest (it does not
embed them — capabilities stay independently content-addressed and shareable,
`03 §9`):

```
oci://reg.mitos/image-resize-app@sha256:<app_id-derived>
  ├─ app.yaml                       (the manifest)
  ├─ pipelines/resize-and-deliver.md
  └─ subject/refs → capability images:  image-resize@…, watermark@…, deliver-proof@…
```

DATA snapshots referenced by a capability are pinned by that capability's own
manifest (`01 §8.3 data.snapshot`), not re-pinned here. Pulling an app pulls
its manifest + pipelines and (lazily) the referenced capability images.

---

## 10. Import — an Eve `agent/` becomes an application

`mitos import eve <dir>` produces a complete application scaffold.
`03 §10.1` owns the capability cut; this section owns the application cut:

| Eve source (`agent/`) | → application |
|---|---|
| the agent as a whole | one capability + one single-step pipeline + one entrypoint — the §1 degenerate form |
| `instructions.md` (title/summary) | entrypoint `title`/`description` |
| `channels/*` | `io.inputs` **defaults** (§5); credentials re-bound at install (`10 §7`) |
| `schedules/*` | `services` / triggers (§6, `06 §6`) |
| `evals/*` | test material for `mitos test` (`11 §6`, `04 §10`) |

**The message→order gap — the one mandatory manual step.** An Eve agent's
input is a free-form message: session-shaped. A mitos entrypoint requires a
typed order (§4): a workspace kind, an `inputs.schema`, parameters. Import
cannot synthesize that schema reliably from prose instructions; it MUST emit a
stub (`inputs: { message: string }`), mark the entrypoint `draft`, and refuse
publish until the author types the order form. This is not incidental
friction: **a typed order is exactly what makes the agent sellable** — the
quote and the refund rule (`08 §7`) need to know what "the job" is before it
runs. Sessions-vs-jobs (`02 §1`), made mechanical.

Imported capabilities default to **T2** (`03 §10.1`); `requirements` (§2) is
computed from the import (harnesses hint dropped, connectors from `channels/*`,
egress from `connections/*`).

---

## 11. Trust & authority

The application manifest is **not an authority surface**. Authority stays where
`01` puts it:

- **Per-capability `trust_tier`** drives the launch policy compiler (`05 §4`);
  `requirements.trust_tier` here is an **aggregate declaration** for
  install-time transparency, never an override. A capability pinned at T2 runs
  T2 even if the app says T1.
- **Egress** is per-capability `egress_policy` enforced at the broker
  (`05 §5.3`); `requirements.egress` is the **union of destinations** surfaced
  for transparency ("what will this app talk to"), not a grant.
- **Secrets** never live in the manifest (`01 §8.5`); connector/sink/provider
  credentials are tenant-side (`10 §7`, `08 §3.2`).
- **Marketplace boundary:** Gentix publish forces T2 (`08 §8`) regardless of
  the manifest's declared aggregate. **At launch:** local T1 (`05 §3`
  Nucleus/Docker). **Beyond launch:** T2 remote (E2B/Daytona) per `09 §2`
  (`11 §10`).

---

## 12. What this doc owns vs delegates

| Owns (this doc) | Delegates |
|---|---|
| the four-unit framing; the application manifest; `app_id` identity; OCI bundling | the capability contract (`03`); the pipeline format (`04 §8`) |
| entrypoints: input→Task mapping, the order-form/quote alignment | quote/settlement mechanics (`08 §7`); connector/sink mechanism (`10`) |
| the **identity/offer boundary** (§8) | offer storage, pricing math, payout (`08 §4`, `08 §8`) |
| the application cut of **import** (§10) | the capability cut (`03 §10.1`); test/judge (`04 §10`) |
| ship-vs-bind I/O default semantics; app-as-composition-root | pools/services/disciplines/resolver (`06`); the broker (`05`) |

---

## 13. Open questions

1. **Parameter enums:** enum/`oneOf` support for `parameters[]` (a fixed policy
   set) — currently a free `string` with options named in `description`. Must
   keep aligning with the tool I/O dialect decision (`03 §11.4`).
2. **OCI artifact shape:** referenced capability images via OCI
   `subject`/referrers vs an index image embedding them — pull cost vs registry
   support.
3. **Versioning UX:** when a referenced capability has a newer digest, is
   bumping the app manual, or is there an assisted "update pins" flow?
4. **Partial install:** if a `requires` service or needed `connector` is
   unavailable at install — fail closed for `requires`, degrade to manual-only
   for `wants` (lean), cf. `05 §4`.
5. **`offer_rev` semantics:** where offer history lives and how a quote pins it
   — likely `08`-owned; flagged here because the receipt renders both identity
   and offer.
6. **Schema synthesis on import (§10):** can the judge propose an
   `inputs.schema` from `instructions.md` + `evals/*` (`04 §10`)? Even if yes,
   the entrypoint stays `draft` until a human approves the order form.
