# 12 — Application (Packaging & Manifest)

Status: draft v0.1 · Date: 2026-06-24

The **agentic application** is the *shipping unit* (`11 §2`): what a developer
publishes and a user adds. This document fixes its **manifest**, **identity**,
**packaging**, and how it *references* — never redefines — the capability
(`03`), pipeline (`04`), I/O (`10`), and service (`06`) contracts. The canonical
skeleton lives in `01 §8.8`; this doc elaborates it, exactly as `03` elaborates
the capability manifest (`01 §8.3`).

Where this doc and `01` disagree, `01` wins. Field names from `01 §8` are
canonical and MUST NOT be redefined here. RFC 2119 keywords are normative.
Sections are **[TARGET]** unless marked **[I]**.

---

## 1. What an application is

> An **application** is a content-addressed bundle of one or more **pipelines**
> (`04`) + the **capabilities** (`03`) they reference + an **application manifest**
> that names the runnable **entrypoints**, the **default I/O bindings** (`10`), and
> the **service composition** (`06`). It is packaged as an OCI artifact (§8).

```
   APPLICATION  (the shipping unit, OCI artifact, immutable §7)
   ┌──────────────────────────────────────────────────────────────┐
   │  app.yaml  (this manifest)                                     │
   │   ├─ entrypoints ───────► which pipelines a user/connector runs│
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
runs a **pipeline** (an entrypoint), never a bare capability. The simplest valid
application is one entrypoint → one single-step pipeline → one capability.

---

## 2. The manifest (annotated)

A worked **image-resize** application, continuing the `resize-and-deliver`
example of `06 §13` / `10 §4`. The manifest is structured (YAML/JSON) — unlike the
*pipelines* it ships, which stay loose markdown (`04 §8`). Field skeleton: `01 §8.8`.

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

entrypoints:                           # §4 — the runnable surface + input contract
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
    - { sink: web,   on: run.done, entrypoint: resize }                       # live trace + receipt

services:                              # §6 — composition root (06 §9–10)
  requires: [ render-pool ]            # activating the app pulls these in
  wants:    [ usage-meter ]

requirements:                          # §7.? install-time transparency / admission
  harnesses:  [ claude-code, codex ]   # union across capabilities; must be available
  connectors: [ webhook, chat ]        # what the io block needs bound
  egress:     [ "api.figma.com" ]      # union of capability egress dests (dest only, no creds)
  trust_tier: T1                       # declared aggregate (informational; per-cap authority wins §9)
  resources:  { cpu: 2, mem: 4Gi }     # aggregate hint
```

Every field references an existing contract; the manifest **wires**, it does not
re-specify. Secrets never appear in it (connector/sink/provider credentials are
tenant-side, control-plane vault — `01 §8.5`, `10 §7`).

---

## 3. Capabilities — the pinned atoms

`capabilities[]` lists the atoms the app's pipelines compose, each with:

- `ref` — a **capability reference**: a content-addressed `oci://…@sha256` (a
  pinned `capability_id`, `03 §9`) when published, or a **local path** during dev
  (resolved to a digest at publish, §7).
- `as` — a stable **alias** the pipelines use, so a pipeline's `allowed_capabilities`
  / stage names (`04 §8.1`) bind to a pinned atom regardless of registry path.

An app **pins** exact capability versions; a capability upgrade is a new app
version with a new digest (§7). The same capability MAY be referenced by many
apps (content-addressed dedup) — it is not embedded, only referenced.

---

## 4. Entrypoints — the runnable surface + input contract

Not every pipeline is user-facing; some are internal (e.g. invoked as subtasks,
`06 §5`). `entrypoints[]` declares the pipelines a user or connector MAY run, and
**how to collect their input** — the contract a Run & Watch form (`11 §4`) or a
connector payload (`10 §3`) satisfies.

An entrypoint maps directly onto a **Task** (`01 §8.7`) at run time. Its input
contract is **three kinds of thing a caller supplies**, kept distinct because
they are collected, defaulted, and trusted differently:

```
run(entrypoint=resize, workspace=W, inputs=I, parameters=P)  ⇒  Task {
    pipeline : <entrypoint.pipeline>,                           # 01 §8.7 pipeline
    payload  : { intent, workspace: W, inputs: I, parameters: P,   # 01 §8.7 payload
                 constraints, success_criteria },
    budget   : <entrypoint.defaults.budget | tenant default>,
    source   : human | connector:<name> | trigger:<id>        # who ran it (10 §3)
}  ⇒ dispatch ⇒ run (04)
```

| Field | Is | Supplied by | Defaulted? |
|---|---|---|---|
| **`workspace`** | the **data substrate** the run operates on — a repo or dataset | the caller (a git URL/path + optional refspec), resolved + cloned by `prepare` (`01 §7`, `05 §5.4`) | no (when `kind != none`) |
| **`inputs`** | the run's **subject data** — *what* to work on (a kanban path + task id, a diff command, a file) | the caller, validated against `inputs.schema` | per JSON Schema `required` |
| **`parameters`** | per-run **knobs** — *how* to run (worker parallelism, severity floor, output format) | the caller, validated against `parameters[]` | **yes — each has a `default`** |

- `inputs.schema` is **JSON Schema** (aligning with the tool I/O dialect
  question, `03 §11.4`); it lets the platform render a form, validate a
  connector payload, and reject a malformed run before any model spend.
- `parameters[]` is a flat array of `{ name, type, default?, description? }`
  using the same type vocabulary as `inputs.schema`. A caller MAY omit any
  parameter and the entrypoint's `default` is applied; this is what a Run form
  renders as labeled controls independent of the data (§4.1).
- `workspace: { kind, ref?, refspec? }` matches `01 §8.7`'s payload workspace.
  `kind` is declared by the entrypoint (`repo | dataset | none`); `ref`/`refspec`
  are supplied at run time. Because resolution (clone/checkout) happens **server-
  side in `prepare`**, the browser/connector only ever passes a ref — **it never
  handles git credentials** (creds stay in the control-plane vault, `01 §8.5`,
  `10 §7`). A bare `kind` string (`workspace: repo`) is accepted as shorthand for
  `{ kind: repo }`.
- `defaults` seed the run's `budget`/`constraints`; a caller MAY override within
  tenant limits (`08 §3` budget ceilings). `parameters` defaults are a separate,
  narrower mechanism for per-run knobs and do not overlap with `defaults`.

### 4.1 Why data and parameters are separate

Conflating them (one flat `inputs` bag) hides the distinction a Run form and a
connector both need: the **subject data** is required, run-specific, and rarely
has a sane default (which repo? which task?), while **parameters** are optional
knobs with sensible defaults the user tweaks per run. Splitting them lets the
frontend render a *workspace picker*, a *data* section, and a *parameters*
section — and lets a connector omit parameters entirely and inherit defaults,
supplying only the data. At execution the two are merged into the capability's
template bindings, so a pipeline/`{{placeholder}}` resolves from either.

---

## 5. I/O bindings — ship defaults, bind at install (two phase)

The `io` block is the `10 §4` binding, scoped per `entrypoint` and shipped as
**defaults**. Binding is two-phase so an app can be portable while credentials
stay tenant-side:

| Phase | Who | What |
|---|---|---|
| **ship** | app author | declares *which connector/sink classes* and *how to render/select* (`io` block) — **no credentials, no tenant specifics** |
| **bind** | tenant at install | supplies the concrete connector instances + credentials (control-plane vault, `10 §7`), MAY override/disable any default binding |

An application with an empty `io` block is still valid — it is runnable manually
(the `manual`/`web` connector, `10 §6`) with the `web` sink, and an operator can
add bindings later. `io` defaults are **suggestions**, never authority.

---

## 6. Services — the application as a composition root

`services` lets the app declare the units/targets it needs (`06 §9–10`).
**Activating an application acts as a target** (`06 §10` pipeline-as-target,
lifted to the app): its `requires`/`wants` union with its pipelines' own
`requires` blocks (`06 §10`) and feed the active-set resolver (`06 §9.5`).
Conflicts and the "why is it on" view (`06 §9.3–9.4`) apply to that union. The
app never authors raw `internal` units (the tenancy boundary, `06 §11`); it
references `pipeline`/`capability` services and named targets only.

---

## 7. Identity, immutability, dev → publish

**Identity is content, not the label** — mirroring `03 §9`:

```
app_id = H( canonical(app_manifest) ‖ sorted(capability_id …) ‖ sorted(pipeline_sha256 …) )
```

- `version` (e.g. `1.2.0`) is a human label; `app_id` is what the event log,
  install records, and replay key on. Same label, different content ⇒ **different
  application**.
- A published application MUST be **immutable**: pinned capability digests +
  pipeline file hashes + a frozen manifest. A change to any member is a new
  `app_id`. This is what makes an installed app reproducible and a run replayable
  (`01 §4`).

**Dev → publish resolution** (`11 §5`):

```
DEV (directory)                         PUBLISH (one freeze point)
 app.yaml + pipelines/*.md       ──►    build each capability dir → OCI image (03 §9) ⇒ capability_id
 capabilities/<name>/ (a 03 dir)        resolve capabilities[].ref  path → @sha256 digest
 capabilities[].ref = ./path            hash pipelines, canonicalize manifest ⇒ app_id
 (run/test from disk, no build)         push the app as an OCI artifact (§8)
```

Locally a capability `ref` MAY be a path; **publish resolves every path to a
digest** and computes `app_id`. Identity exists only after publish; the daily
dev loop runs from disk (`11 §5`, `11 §6`).

---

## 8. Packaging onto OCI

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
manifest (`01 §8.3 data.snapshot`), not re-pinned here. Pulling an app pulls its
manifest + pipelines and (lazily) the referenced capability images.

---

## 9. Trust & authority

The application manifest is **not an authority surface**. Authority stays where
`01` puts it:

- **Per-capability `trust_tier`** drives the launch policy compiler (`05 §4`);
  `requirements.trust_tier` here is an **aggregate declaration** for install-time
  transparency, never an override. A capability pinned at T2 runs T2 even if the
  app says T1.
- **Egress** is per-capability `egress_policy` enforced at the broker (`05 §5.3`);
  `requirements.egress` is the **union of destinations** surfaced for transparency
  ("what will this app talk to"), not a grant.
- **Secrets** never live in the manifest (`01 §8.5`); connector/sink/provider
  credentials are tenant-side (`10 §7`, `08 §4`).
- **Now:** local T1 (`05 §3` Nucleus/Docker). **Later:** T2 remote (E2B/Daytona)
  and Gentix publish, which forces T2 at the marketplace boundary (`08 §6`) — out
  of scope for the local product (`11 §9`).

---

## 10. What this doc owns vs delegates

| Owns (this doc) | Delegates |
|---|---|
| the application manifest, `app_id` identity, OCI bundling, entrypoints + input→Task mapping | the capability contract (`03`); the pipeline format (`04 §8`) |
| ship-vs-bind I/O default semantics | the connector/sink mechanism (`10`); the broker (`05`) |
| app-as-composition-root | pools/services/disciplines/resolver (`06`) |

---

## 11. Open questions

1. **Inputs dialect:** ~~JSON Schema for `entrypoints[].inputs` vs a lighter typed
   DSL~~ — decided: JSON Schema for `inputs`, and a flat `{name,type,default?}`
   array for `parameters` (§4). Both MUST keep aligning with the tool I/O dialect
   decision (`03 §11.4`). Workspace supply is now `{kind, ref?, refspec?}`,
   resolved server-side (§4). Remaining sub-question: enum/`oneOf` support for
   parameters (e.g. a fixed policy set) — currently a free `string` with the
   options named in `description`.
2. **OCI artifact shape:** referenced capability images via OCI `subject`/referrers
   vs an index image embedding them — affects pull cost and registry support.
3. **Versioning UX:** when a referenced capability has a newer digest, is bumping
   the app version manual, or is there an assisted "update pins" flow?
4. **Partial install:** if a `requires` service or a needed `connector` is
   unavailable at install, fail closed (cf. `05 §4`) or install in a degraded,
   manual-only state? (Lean: fail closed for `requires`, degrade for `wants`.)
