# 11 — Frontend (Surfaces as Readers of the Log)

Scope: **how mitos is represented to a user and how it should feel** — the
surfaces, the journeys, and the altitude of platform involvement. Functionality
is fixed by `01`–`08` and the I/O plane (`10`); this doc defines no new
canonical contracts. Where this doc and `01` disagree, `01` wins. RFC 2119
keywords are normative.

---

## 1. First principles

The frontend derives from five
principles, each anchored in a contract that already exists:

1. **A surface is a reader.** The frontend never invents state: every screen is
   `render(fold(events), cut)` — a read of the event log at a redaction cut
   (`01 §8.2`, `01 §10.11`, `08 §2`). What a persona may see is fixed by the
   reader contract (`08 §2`, invariants 2–3), never decided screen-by-screen.
2. **Personas follow the money.** The product's people are `02 §3`'s: the
   **seller** (authors, publishes, prices, attributes), the **buyer** (pays for
   a completed job), and the **operator** (runs the stack for themself — the
   secondary local mode, and the seller at work). a commerce layer has counterparties, not a framework audience.
3. **Jobs, not sessions** (`02 §1`). Every surface points at a deliverable:
   watching is *progress toward completion*; the terminal screen is *artifacts
   + proof*; nothing is chat-shaped. Free-form intent enters only where an
   entrypoint's order form or a connector admits it (`12 §4`, `10 §3`).
4. **One renderer, two cuts.** The local run report and the buyer's proof page
   are the *same render* at two redaction cuts (`08 §5`, `10 §5.3`): the dev
   cut adds `rationale`, the inner trace, and the judge's verdict; the
   commercial cut is exactly `08 §5`'s SHOWS column. Build the renderer once.
   There is no "proof page feature" separate from the run report.
5. **The platform does not author.** Authoring happens in the developer's own
   editor or coding agent; the platform supplies the contract, a scaffold, a
   test-with-judge, and a publish freeze — CLI-first. Watching and operating
   are screen work — web-first. All surfaces call the same backend operations
   (`01 §10.11`).

Corollary of (1)+(4): **the frontend is the rendering layer of `08 §2`'s read
models.** Adding a screen means choosing a reader, a cut, and an altitude — the
fold is already specified elsewhere.

---

## 2. The unit hierarchy — application ⊃ pipelines ⊃ capabilities

> **What ships — and is sold — is an *agentic application*. What a user runs —
> and a buyer pays for — is an *entrypoint* (a pipeline). What a pipeline
> composes is *capabilities*. A user never picks a bare capability — always a
> pipeline, even a single-step one.**

| Level | Is | Packaged as | Commerce role |
|---|---|---|---|
| **Agentic application** | pipelines + pinned capabilities + app manifest (`12`, `01 §8.8`) | OCI artifact (`12 §9`) | **unit of sale** (`12 §1`) |
| **Entrypoint → pipeline** (`04`) | a runnable pipeline + a typed order form (`12 §4`) | markdown in the app (`04 §8`) | **unit of purchase** — a paid run enters here (`08 §7`, `12 §4`) |
| **Capability** (`03`) | the `(DATA+SKILL+TOOLS)` atom under an injected harness | OCI image + RO snapshot | unit of execution & metering (`08 §4`); never picked directly |

Discovery/marketplace is Gentix (`08`) and is **not** part of this product's
surface. A tenant adds an application by pasting a git URL / OCI ref, or by
importing one (`12 §10`); the frontend lists only what the tenant has added.

---

## 3. Personas × surfaces

| Persona | Trust position | Journey | Surfaces |
|---|---|---|---|
| **Seller** | own tenant; cross-tenant reader of their sales | import/scaffold → author in own tools → test → publish → price → attribute | §6 → §7 |
| **Buyer** | stranger; pays per job | order (entrypoint form / x402) → watch (commercial cut, read-only) → receive the deliverable | §4 (buyer cut), §5 |
| **Operator / dev** | trusted, local (T1) | add app → run → watch (full) + steer → report; wire I/O (`10 §4`); fleet post-launch | §4, §5, §8 |

Load-bearing rule: **seller and buyer never share a live surface.** They meet
only through cuts of the log (`08 §2`, invariant 3): the buyer never sees the
recipe (no inner trace, no `rationale`); the seller does not watch a
marketplace run live (buyer-data cut, `08 §6` open question) — they read
attribution after the fact (§7).

---

## 4. Surface — Run & Watch (two zoom levels)

**Entry.** Pick an added app → pick an **entrypoint** → the platform renders
the order form from `12 §4`: a **workspace** picker (ref resolved server-side —
no git credentials in the browser), **inputs** (the subject data, JSON-Schema
validated), **parameters** (knobs, pre-filled with defaults) → run → watch →
receive the deliverable (§5) through the configured sinks (`10 §5`).

**The watch experience has TWO zoom levels** — because the log folds the outer
layers, but the inside of a capability is finer than the orchestrator ever reads:

| | **Outer — pipeline timeline** | **Inner — capability trace** |
|---|---|---|
| Shows | the run as Decision/Effect events: orchestrator moves (`advance/insert/retry/switch/skip…`), capability boundaries, harvests, deliveries | each step the harness takes inside one capability: `assistant_text · tool_use · tool_result · token_usage · status` (`01 §8.6`) + raw spool |
| Source | `fold(event log)` (`04 §6`) — what the orchestrator itself reads (`04 §3.1`) | the normalized harness stream + raw spool, owned by the **Zig sidecar** (`01 §7`, `05 §5.2`), streamed on drill-down |
| Altitude | whole pipeline | one running/finished capability |

The inner trace is a human observability surface, not an orchestrator input
(`04 §3.1`) — no contradiction with `04`.

**Cuts (who sees which zoom).**

| Viewer | Zoom | Cut |
|---|---|---|
| operator/dev (own run) | outer + inner + raw spool | full dev cut |
| buyer (marketplace run) | **outer only**, read-only | commercial cut (`08 §5`): no `rationale`, no inner trace, egress destinations only |
| seller (marketplace run) | none live | attribution afterward (§7, `08 §6`) |

**Steer** is an operator affordance on *own* runs: approve / answer / interrupt
/ select / prompt (`05 §5.2` `control`); `request_approval` parks for the user
(`04`). Marketplace runs have no human on the happy path (`02 §6`); the buyer's
watch is read-only by construction — a viewer, not a session.

**Feel.** A CI/build run whose rows are *agent decisions*, with a drill-down
into the harness's own steps, and a Stop/Approve button that actually works.
Never a chat transcript.

---

## 5. Surface — the Deliverable (run report ⇄ proof page)

The terminal surface and the product's last impression. One renderer (P4):

```
render(run) = header       (app_id · entrypoint · capability_ids)
            + timeline     (fold of Decision/Effect events)
            + deliverables (diff · RESULT · artifacts)          ← 05 harvest
            + totals       (budget_charged · wall · crossings)   ← 08 §4 fold
            + verdict      (judge, 04 §10) where one ran

dev cut         += rationale · inner-trace links · raw-spool refs · memory writes
commercial cut   = 08 §5 proof page + receipt (quote, settlement refs)
```

- The deliverable MUST stand alone at a URL (delivered by the `web` sink,
  `10 §5`–`§6`), MUST NOT require the live session, and MUST be replayable from
  the log excerpt + hashes. Locally it is "the proof page minus the money"
  (`08 §5`).
- Sinks deliver **this render** (or a slice of it), not a transcript
  (`10 §5.3` — `render/2` reads the same folded state + artifacts).

---

## 6. Surface — Author & Test (CLI-first: contract + scaffold + test)

The platform is thin here **by decision** (P5): it does not author. The real
authoring happens in the developer's editor or coding agent. The platform
provides exactly three things, CLI-first (`mitos …`), web UI as a read-only
mirror:

**(a) The contract — codified, bidirectional, in sync with the manifest.**

| The dev MUST provide (`01 §8.3`, `03`) | The platform GUARANTEES at runtime (`03 §6`) |
|---|---|
| SKILL — markdown know-how (common skills format accepted as-is; anchors RECOMMENDED, `03 §2`) | `/workspace` RW (input cloned in); `/data` RO snapshot |
| TOOLS — typed, deterministic CLI/py/js | `/skill/resolved.md`; `/tools` on PATH |
| DATA — RO corpus (optional) | a harness **injected** (never baked), switchable |
| manifest — harnesses, trust_tier, egress_policy, result schema+evidence, hooks | egress = deny-all-except-broker; **no secrets in env** |

**(b) A scaffold — and an importer.** `mitos init` emits a contract-valid
skeleton (skill, typed-tool template, manifest, `result.schema.json`, hooks).
`mitos import eve <dir>` converts an Eve agent into the same skeleton — the
capability cut is `03 §10.1`, the application cut is `12 §10`; the funnel move
of `02 §1`. Imported or scaffolded, the result faces the same contract and the
same test gate: import is a starting point, not a certification.

**(c) The test harness — local run + trace + orchestrator-as-judge.**
`mitos test` runs the capability/pipeline in a local T1 sandbox (`05 §3`,
Nucleus/Docker) under a **selectable** harness → streams the two-zoom trace
(§4) → harvests result+evidence → applies **deterministic checks always** + has
the **orchestrator/verifier judge** the soft result against the declared
`success_criteria` (`04 §10`), emitting a `verification_recorded` verdict. Two
payoffs:

- a fast "does it run, and is it any good?" loop with no marketplace wiring;
- the judge's verdict is the **same signal** that feeds verify→distill and the
  per-model skill-adapt seam (`03 §2.1`, `04 §10`) — testing produces the
  improvement signal, not a throwaway pass/fail.

**Cross-harness / nested-harness — the worry, answered.** A capability that
needs *another* agent's help MUST `invoke` a sub-capability (`06 §5`) — a
governed, separately sandboxed, metered call — not shell out to a second
harness ad hoc. `invoke_requested`/`subtask_returned` Effects show in the outer
trace; the judge can flag missing access. v1 runs **one selectable harness**;
re-running under each declared harness is manual; the auto cross-harness matrix
is deferred (explicit non-goal).

**Publish.** `mitos publish` is the single freeze point: directory → OCI, paths
→ digests, `app_id` minted (`12 §7`). No build step exists in the daily loop —
dev runs from disk; identity exists only after publish. Everything commercial
hangs off this point (§7).

---

## 7. Surface — Publish & Attribute (the seller's commercial surface)

What the seller gets after the freeze:

- **Offer attachment.** Price (flat | metered), payout address, key-vault
  binding — the Gentix publish steps (`08 §8`), CLI-first with a minimal web
  form. The offer is facilitator-side and **not part of `app_id`** (`12 §8`):
  repricing never mints a new application.
- **Attribution.** A render of the seller's read (`08 §6`): revenue per
  `capability_id`/`app_id`, own-key spend (`budget_charged` on their key),
  result signals / wall-time / crossing counts per run, and version-vs-version
  comparison — content-addressing makes digests directly comparable.
- **No live view of buyer runs** (§3). Dispute-grade access is post-launch
  (`08 §6` open question).
- **Launch scope** (`09 §2`): publish CLI + payout config + a flat revenue list. The
  gate needs one seller to see one settled dollar — not a dashboard.

---

## 8. Surface — Operate: the Fleet [post-launch, demand-gated]

The operate layer is **I/O + services made visible**, and it is explicitly
deferred: it renders `10` and `06`, and `09 §2` demand-gates that breadth (Eve
ships fleet-adjacent surfaces free — `13 §1`). When built, it renders:

- **Applications & pipelines** the tenant added (§2), each with its I/O
  bindings (`10 §4`): which connectors feed it, which sinks it emits to.
- **The fleet board** — running services/pipelines, pools and disciplines
  (`06 §2`), Task lifecycle (`06 §3`), health + circuit-breaker state (`06 §8`),
  the resolved "why is it on" active set (`06 §9.4`).
- **I/O activity** — ingress signals → Tasks (`10 §3`), egress deliveries
  (`10 §5`), each an Effect on the log.

**Feel:** a process supervisor for agents, framed as *applications doing work
for me*. **v1 ships without it**: the v1 operate surface is the app list, run
history, and deliverable pages (§5). The Fleet arrives when a paying tenant's
volume demands it, not before.

---

## 9. CLI vs web primacy

| Phase | Primary | Why |
|---|---|---|
| author / test / publish (seller, local) | **CLI** (`mitos init/import/run/test/publish`) | authoring lives in the dev's own tools; web mirrors read-only |
| run / watch / deliverable / operate | **web**, CLI parity | a screen experience |
| **buy** | **a URL** — no account, no install | x402 admission (`08 §7`): order form → watch link → proof link |

All call the same backend ops (`01 §10.11`); primacy is which surface is the
default for a phase, never capability gating.

---

## 10. Scope

The frontend owns the **read surfaces**: Run & Watch (two zooms + cuts, §4), the Deliverable (report ⇄ proof, §5), Author & Test (§6), and Publish & Attribute (§7). The **Fleet** operate surface (§8) is demand-gated — built when operating many sellers requires it, not for parity. Web surfaces are server-rendered folds over the log with no client framework; identity is provided by **sigid** (`01` §0).



---

## 11. What this spec owns vs delegates

| Owns (this doc) | Delegates |
|---|---|
| the five principles; the persona × surface map; the two-zoom watch and its cuts; the one-renderer/two-cuts rule; CLI/web/URL primacy | the event log + control model (`01`, `04`); the read-model contract (`08 §2`); the capability contract (`03`) |
| the author altitude (thin: contract + scaffold + import + test + publish) | sandbox/test execution (`05`); verify/judge (`04 §10`); import mappings (`03 §10.1`, `12 §10`) |
| Fleet *presentation* (demand-gated) | I/O mechanism (`10`); pools/services (`06`); metering/settlement/proof folds (`08`) |

---

## 12. Open questions

1. **Buyer live-watch default:** always-on trust surface, or offer-controlled?
   Even the commercial cut's timing/destination pattern can leak method.
2. **Inner-trace redaction for imported capabilities:** the dev cut of a
   T2-default import (`03 §10.1`) run locally — full spool or redacted?
3. **Attribution depth:** flat list first; charts only when a seller
   asks (`09 §2`).
4. **Web/CLI parity surface:** which operate actions need CLI parity for
   headless automation.
