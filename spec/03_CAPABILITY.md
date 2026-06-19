# 03 — Capability

Status: draft v0.2 · Date: 2026-06-18

Elaborates `01_ARCHITECTURE` §5 (the atom), §8.3 (manifest), §8.6 (harness
adapter). Where this doc and `01` disagree, `01` wins. RFC 2119 keywords are
normative. Field names come from `01` §8 and MUST NOT be redefined here.

Status legend: **[T]** TARGET (specced, not built) · **[I]** IMPLEMENTED in mitos
· **[CW]** exists in chief-wiggum today (being replaced/borrowed).

---

## 1. What a capability is

> A **capability** is a content-addressed bundle of `(DATA + SKILL + TOOLS)`,
> packaged as an OCI image plus a read-only DATA snapshot, run under an
> **injected** harness inside a sandbox. It is the atom of execution (`04`) and
> the atom of monetization (`08`).

The capability ships *know-how and machinery*; mitos supplies *the model, the
isolation, the authority, and the record*. A capability MUST NOT bake in a
harness, a model, secrets, or the input it operates on — those are injected at
launch. Everything a capability needs to be reasoned about is declared in its
**manifest** (§7, `01` §8.3).

```
                        ┌─────────────────── capability (immutable) ───────────────────┐
   INJECTED (per run)   │  OCI image                                  RO DATA snapshot  │
   ─────────────────    │  ┌───────────────┬───────────────┐         ┌────────────────┐│
   harness  ───────────►│  │ SKILL         │ TOOLS         │         │ corpus / refs  ││
   model/effort ──────► │  │ skill.md      │ typed CLI/    │         │ vector DB /    ││
   workspace[input] ──► │  │ + adapt/*.md  │ py / js       │         │ design library ││
   context slices ────► │  └───────────────┴───────────────┘         └────────────────┘│
   broker creds (edge)  └──────────────────────────────────────────────────────────────┘
                              mounted /workspace (RW)   /skill /tools (image)   /data (RO)
```

| Pillar | What | Mutable? | Mount | `01` ref |
|---|---|---|---|---|
| **SKILL** | formalized markdown know-how | image-immutable; `resolved = base ⊕ adapt(model)` at launch | `/skill` (RO) | §5, §8.3 `skill` |
| **TOOLS** | deterministic CLI/Python/JS, typed | image-immutable | `/tools` on `PATH` | §5, §8.3 `tools` |
| **DATA** | persistent corpus/refs/DB (NOT input) | snapshot-immutable | `/data` (RO) | §5, §8.3 `data` |
| *harness* | the coding agent | injected, switchable mid-step (`04`) | process | §5, §8.6 |
| *workspace* | the input being worked on | RW, harvested as diff | `/workspace` | §8.5 |

The pillar split maps cleanly onto chief-wiggum's two agent formats: a CW
`*.md` agent **[CW]** becomes the SKILL pillar (formalized), a CW `*.sh`
imperative agent **[CW]** becomes one or more typed TOOLS, and DATA is **new** —
chief-wiggum has no per-agent persistent corpus.

---

## 2. SKILL — formalized know-how with a per-model seam

The SKILL is the prompt-shaped intelligence of the capability: domain rules,
the procedure to follow, and the success criteria. It is authored as **anchored
markdown** so a deterministic layer can address its sections:

```markdown
<!-- @skill:role -->        ROLE and stance (who the agent is being)
<!-- @skill:rules -->       domain invariants / constraints (MUST/SHOULD)
<!-- @skill:procedure -->   the ordered method (phases, steps)
<!-- @skill:success -->     definition of done + what evidence to emit
```

The base skill **MUST be model-agnostic**. It never says "as Claude…" or tunes
phrasing to one model's quirks. Model-specific shaping lives in a separate,
composable layer.

### 2.1 The per-model adaptation seam

```
resolved_skill = base_skill ⊕ per_model_adapt(model)
```

- `base_skill` — the anchored markdown in the image (`/skill/skill.md`).
- `per_model_adapt(model)` — an **adaptation overlay**: a set of typed directives
  keyed to base anchors. Sources, resolved at launch in priority order:
  1. baked overlay `/skill/adapt/<model_family>.md` (capability-authored),
  2. memory-served overlay distilled by the verify→distill loop (`04`/`07`),
  3. **none** ⇒ overlay is `∅`.
- `⊕` — a **pure, deterministic** merge (no model call). It applies overlay
  directives to base anchors in declaration order:

```
@adapt replace skill:procedure   …new body…     # swap a section
@adapt append  skill:rules       …extra rules…   # add after
@adapt prepend skill:success     …guard…         # add before
```

**Identity law (MUST hold):** `base_skill ⊕ ∅ = base_skill`. A model with no
adaptation runs the base verbatim. The merge is associative per `(op, anchor)`
with last-writer-wins; the resolved text is materialized to
`/skill/resolved.md` and its `sha256` is recorded as an Effect (`01` §8.2) so a
run's exact instructions are replayable.

**Build the seam now; the GENERATOR is out of scope.** This doc specifies the
overlay format and `⊕`. *Producing* overlay entries (mining the verify loop for
"model M keeps skipping phase 3 → inject a guard") is owned by `04` (verify→
distill) and `07` (memory). Until a generator exists, every capability runs
`base ⊕ ∅` and is fully functional. **[T]**

> Why a seam, not per-model skills? One model-agnostic base stays authorable and
> reviewable; adaptations are small, attributable patches that the system can
> grow automatically without forking the skill. chief-wiggum has no equivalent —
> its prompts are static markdown with `{{var}}` interpolation only **[CW]**.

---

## 3. TOOLS — deterministic, typed machinery

TOOLS are the deterministic code the agent invokes: CLIs, Python, JS, baked into
the image. **The rules of *when/how* to use a tool live in the SKILL; the *code*
is deterministic and lives here.** This is the clean refactor of chief-wiggum's
`*.sh` agents, which tangled imperative logic and orchestration together **[CW]**.

Every tool is **typed** — `name` + I/O schema — so the harness adapter can expose
it (as native tools or MCP, §8.6) and the substrate can validate calls:

```yaml
tools:
  - name: resolve_lockfile_conflicts          # ← was git-conflict-resolver.sh logic
    exec: /tools/resolve_lockfiles.py
    input:  { paths: [string] }               # JSON Schema (abbrev.)
    output: { resolved: [string], skipped: [string] }
    deterministic: true                        # MUST be pure wrt inputs+workspace
    side_effects: [workspace_write]            # declared; broker-gated if egress
```

Rules:
- A tool MUST be deterministic with respect to `(declared inputs, workspace,
  /data)`. Non-determinism (network, clocks) MUST be declared as `side_effects`
  and is broker-gated (`01` §8.5).
- Tools MUST NOT hold secrets; outbound calls go through the broker.
- Tools are versioned with the image (content-addressed); see §8.

---

## 4. DATA — the persistent capability corpus

DATA is the capability's own long-lived knowledge — a reference corpus, embedded
vector DB, lookup tables, golden examples. **It is NOT the input workspace.**

| | DATA (`/data`) | workspace (`/workspace`) |
|---|---|---|
| Origin | capability author / curated | the buyer's/user's input, injected per run |
| Lifetime | versioned with the capability | one run |
| Mode | **RO** (provider snapshot/volume) | RW, harvested as `diff` |
| Example | 200 reference designs + a vector index | the repo under critique |

DATA is mounted RO via a SandboxProvider snapshot or volume (`05`). For T2
(untrusted) the snapshot is the *only* corpus access and is immutable for the run
(`01` §6). A capability MAY ship with no DATA. **New vs chief-wiggum** — CW
agents carry no persistent per-agent data; their only "corpus" is whatever the
prompt embeds **[CW]**.

---

## 5. Harness injection

The capability declares a **compatible set** of harnesses; mitos injects exactly
one at launch and MAY switch it mid-step.

```
manifest.harnesses: [claude-code, codex, gemini-cli]   # capability declares compat
        │
        ▼ (launch policy, 05)
   mitos injects ONE  ──► harness adapter (01 §8.6) ──► normalized events
        │                  launch/events/steer/result/hermeticity/capabilities
        ▼ (loop-manager, 04)
   switch mid-step on escalation: switch → {model, effort, harness}   (Decision, 01 §8.1)
```

- The image MUST NOT bake the harness binary or pin a model. Pinning a harness
  is a manifest *compatibility* statement, not an embedding.
- The adapter normalizes harness output to `assistant_text | tool_use |
  tool_result | token_usage | status | final_result` (`01` §8.6) and enforces
  **hermeticity**: native cross-run memory (HOME, config, session store,
  auto-loaded project memory) is redirected/neutralized unless the capability
  opts into reuse. mitos owns memory (`07`), not the harness.
- A capability SHOULD declare the harness `capabilities` it relies on
  (`structured_output`, `mcp_tools`, `effort_levels`); the launch policy refuses
  an incompatible harness rather than degrading silently. **[T]**

---

## 6. Entry/exit contract + deterministic hooks

A capability run is a bracket: deterministic `hooks.pre` → agentic loop under the
harness → deterministic `hooks.post` → harvest. Hooks run **deterministic code**
(no model), with access to `/workspace`, `/data`, and TOOLS — the place for git
worktree setup, environment prep, and validation gates.

```
        ENTRY  (runtime MUST guarantee)            EXIT  (runtime harvests → 01 §8.4)
        ────────────────────────────────           ─────────────────────────────────
 prepare  /workspace RW (input cloned in)    │      harvest → {diff, result, artifacts}
 (05)     /data RO snapshot mounted          │        diff      = workspace git diff
          /skill/resolved.md = base ⊕ adapt  │        result    = RESULT artifact (§7? see below)
          /tools on PATH                     │        artifacts = registered evidence
          harness injected, NOT started      │
          egress = deny-all-except-broker    │      hooks.post (det.): commit, gate, teardown
          NO secrets in env                  │        ── each Effect-logged (01 §8.2)
        ───────────── hooks.pre (det.) ──────┼────── hooks.post (det.) ──────────────
          git worktree/branch setup          │        git add+commit on broker identity
          env/dirs setup                     │        validation gate (build/lint/tests)
          pre-flight validation gate         │        artifact registration, env teardown
        ─────────────────────────────────────┘
                       ▼ harness loop (agentic, 04 loop-manager) ▲
```

Hook contract:

```yaml
hooks:
  pre:  [ {name, run: <tool|argv>, on_fail: block|warn|skip} ]
  post: [ {name, run: <tool|argv>, on_fail: block|warn|skip} ]
```

- Hooks MUST be deterministic and side-effect-declared. `on_fail: block` at a
  `pre` gate aborts the step before any model spend; at a `post` gate it marks
  the step's harvest unverified (the orchestrator decides, `04`).
- Each hook execution is recorded as an Effect (`commit`, `artifact_registered`,
  `egress_*`, …). chief-wiggum performs the analogous git-commit-on-behalf and
  prereq checks imperatively inside `agent_run`/lifecycle hooks **[CW]**; here
  they are first-class, typed, and logged.

---

## 7. Soft result + evidence

A capability does **not** emit a rigid control token. It emits a **soft result
signal + evidence**; the orchestrator interprets `result + evidence + trace`
(`01` §5). This deliberately replaces chief-wiggum's
`gate_result: PASS|FAIL|FIX|SKIP` and its fixed result→jump table **[CW]**
(AGENT_SPEC §7.2), which forced every agent into four buckets and hard-coded the
next move.

The capability writes a `RESULT` artifact to a known workspace path; the runtime
harvests it as `01` §8.4's `result`. Its shape is declared by the manifest's
`result.schema`, and its evidence paths by `result.evidence` (`01` §8.3):

```jsonc
// RESULT artifact — harness-agnostic, harvested at exit
{
  "signal":     "ship | revise | block",   // SOFT, capability-defined; ADVISORY only
  "confidence": 0.0,                         // 0..1, self-reported
  "summary":    "string",                    // NL, for humans + orchestrator context
  "metrics":    { "...": 0 },               // capability-defined scalars (e.g. n_findings)
  "evidence":   [                            // typed pointers, content-addressed
    { "kind": "report",   "path": "critique.md",        "sha256": "…" },
    { "kind": "diff",     "path": "<harvested>",        "sha256": "…" },
    { "kind": "log",      "artifact": "art://run/…",    "sha256": "…" }
  ],
  "needs":      [ "human_review" ]           // OPTIONAL advisory: what it thinks comes next
}
```

- `signal` is **soft**: it MUST NOT directly drive control flow. The orchestrator
  weighs `signal` together with `evidence`, `metrics`, and the run `trace` before
  emitting a Decision (`01` §8.1). A capability MAY define its own `signal`
  vocabulary in `result.schema`; there is no global enum.
- `evidence` entries MUST be content-addressed (`sha256`) so a Decision's inputs
  are replayable (`01` §8.1 `inputs_hash`).
- Missing/invalid RESULT ⇒ the runtime harvests `diff + artifacts` and a synthetic
  `signal: "unknown"`; the orchestrator handles it, never the substrate.

---

## 8. The capability manifest (annotated)

Elaboration of `01` §8.3 — a realistic **design-critique** capability (rich DATA).
Field names are exactly those of §8.3.

```yaml
name: design-critique
version: 1.4.0                                  # human label; identity = digest (§9)
image: oci://registry.mitos/design-critique@sha256:9f3c…   # SKILL + TOOLS baked in

data:                                            # §4 — RO corpus, NOT the input
  snapshot: snap://designs/ref-200@2026-05-01    # 200 reference designs + vector index
  mount: /data
  ro: true

harnesses: [claude-code, codex]                  # §5 — compat set; mitos injects one
skill: ./skill.md                                # §2 — base; resolved = base ⊕ adapt(model)

tools:                                            # §3 — typed, deterministic
  - { name: nn_search,   exec: /tools/nn_search.py,  input: {query_png: string, k: int},
                                                      output: {neighbors: [object]},
                                                      deterministic: true, side_effects: [data_read] }
  - { name: heuristics,  exec: /tools/heuristics.py, input: {svg: string},
                                                      output: {violations: [object]},
                                                      deterministic: true }

trust_tier: T2                                   # §6/01 — untrusted seller on buyer data ⇒ remote µVM
resources: { cpu: 2, mem: 4Gi, timeout: 900s }

egress_policy:                                   # consumed by broker, 01 §8.5
  - { dest: "api.figma.com", credential: figma_pat, requires_approval: false }
  # default: deny-all-except-broker; secrets live ONLY at the broker

result:                                          # §7 — soft result + evidence
  schema: ./result.schema.json                   # declares signal vocab: ship|revise|block
  evidence: [ "critique.md", "annotated.svg", "scores.json" ]

hooks:                                           # §6 — deterministic entry/exit
  pre:  [ { name: snapshot_input, run: [git, -C, /workspace, stash, --include-untracked],
            on_fail: warn } ]
  post: [ { name: gate, run: /tools/validate_critique.py, on_fail: block },
          { name: commit, run: [git, -C, /workspace, commit, -am, "design-critique"],
            on_fail: warn } ]
```

A **software-engineer** capability differs mainly in pillars, not shape: `data`
is often absent, `tools` wrap build/test/lint, `result.schema` defines a
`signal` like `done|needs_work|blocked`, and `egress_policy` is typically empty
(hermetic). See §10 for the worked port.

---

## 9. Versioning, immutability, trust tiers

**Identity is content, not the label.** A capability's true identity is:

```
capability_id = H( image_digest ‖ data_snapshot_ref ‖ skill_sha256 ‖ tools_manifest_sha256 )
```

- `version` (e.g. `1.4.0`) is a human-facing label; `capability_id` is what the
  event log, metering (`08`), and replay key on. Two builds with the same label
  but different `image_digest` are **different** capabilities.
- A published capability MUST be **immutable**: image and DATA snapshot are
  content-addressed and never mutated in place. A change is a new version/digest.
  This is what makes a run replayable and a seller's revenue attributable (`08`).
- The per-model `⊕` overlay does **not** change `capability_id`; the *resolved*
  skill's `sha256` is recorded per-run (Effect, §2.1) so the exact instructions
  are still replayable without forking identity.

**Trust-tier implications** (`01` §6) — the tier in the manifest selects launch
posture, compiled by the Tier-1 policy compiler (`05`):

| Tier | Typical author | Sandbox | DATA / workspace | Egress | Manifest enforcement |
|---|---|---|---|---|---|
| **T0** | first-party (verifiers, orchestrator helpers) | Nucleus (gVisor) | bind/RO | local broker / unix socket | trusted; hooks may bind-mount |
| **T1** | local user on own code | Nucleus / Docker | bind workspace | local broker | egress_policy advisory→enforced |
| **T2** | **untrusted tenant/seller on buyer data** | **remote µVM (E2B/Daytona)** | **RO snapshot only; no bind** | **broker-only, deny-all-else** | egress_policy STRICTLY enforced; no secrets in env (`01` §8.5) |

A T2 capability that declares a bind mount, a baked secret, or an undeclared
egress MUST be rejected at launch, not at runtime.

---

## 10. Authoring guide — porting chief-wiggum's `software-engineer`

How to write a capability, worked against CW's `software-engineer.md` **[CW]**.

**Step 1 — Split the CW agent into pillars.** CW's `software-engineer.md` is one
markdown file mixing role, procedure, git rules, and a PASS/FAIL gate. Decompose:

| CW source (software-engineer.md) | → mitos pillar / field |
|---|---|
| `<WIGGUM_SYSTEM_PROMPT>` "SENIOR SOFTWARE ENGINEER ROLE", principles | SKILL `@skill:role`, `@skill:rules` |
| `<WIGGUM_USER_PROMPT>` Phases 1–4.5, Quality Checklist | SKILL `@skill:procedure` |
| "Final Result" `<result>PASS/FAIL</result>` | SKILL `@skill:success` → soft `result` (§7) |
| `## Git Restrictions` (no `git commit`/`add`) | **TOOLS + hooks.post** (commit is a deterministic hook, not a prompt rule) |
| `completion_check: status_file:prd.md` | `hooks.post` gate `run: /tools/prd_complete.py` |
| `{{workspace}}`, `../prd.md`, `{{plan_file}}` | injected context slices + `/workspace` (`05`) |
| `readonly`, `supervisor_interval: 2` | NOT here — supervision is the loop-manager's job (`04`) |

**Step 2 — Write the base skill** (model-agnostic; CW's phases map 1:1):

```markdown
<!-- @skill:role -->
Senior software engineer implementing one task from a PRD to production quality.
<!-- @skill:rules -->
- Trace every change to spec/PRD/intent. Match existing patterns. Handle errors at boundaries.
- Do NOT run git commit/add/reset — the runtime commits via hooks.post.   ← was "Git Restrictions"
<!-- @skill:procedure -->
1. Read PRD, pick first `- [ ]`.  2. Study architecture + patterns.
3. Implement + unit tests.        4. Build + run tests.
<!-- @skill:success -->
All PRD items done AND build/tests pass. Emit RESULT {signal, evidence:[diff, test_log]}.
```

**Step 3 — Promote imperative `.sh` logic to typed TOOLS.** CW's
`git-conflict-resolver.sh` `_auto_resolve_trivial_conflicts` (accept-theirs for
lockfiles, then stage) **[CW]** becomes a deterministic tool
`resolve_lockfile_conflicts` (§3). The *rule* "resolve stale conflict markers
immediately" stays in `@skill:rules`; the *mechanism* is the tool.

**Step 4 — Define the soft result.** Replace `valid_results: [PASS, FAIL]` with
`result.schema` defining `signal: done|needs_work|blocked` plus
`evidence: [diff, test_log, prd_state]`. The orchestrator interprets it — no
fixed PASS→next / FAIL→abort table.

**Step 5 — Optional DATA.** software-engineer usually needs none. A variant could
mount a `/data` snapshot of the project's API docs (CW does this ad hoc via
`wdoc`/`.ralph/docs` **[CW]** — here it is a declared RO snapshot, §4).

**Step 6 — Hooks + manifest.** `hooks.pre`: create git worktree/branch.
`hooks.post`: run the build/test gate (`on_fail: block`) then commit
(`on_fail: warn`). Fill the §8 manifest, set `trust_tier` (`T1` for own code).
Build → content-address → publish (immutable, §9).

The capability is now harness-agnostic, hermetic, replayable, meterable, and —
unlike the CW agent — improvable without editing the base skill, via the §2.1
adaptation seam.

---

## 11. Open questions (to resolve with `04`/`05`/`07`)

1. **Overlay precedence** — when a baked `/skill/adapt/<model>.md` and a
   memory-served overlay both target `skill:procedure`, which wins? (Proposed:
   memory > baked, since memory is the live distillation; needs `07` sign-off.)
2. **Hook execution locus** — do hooks run inside the sandbox (T2-safe, but
   git-identity for commit?) or at the boundary? (Proposed: inside sandbox using
   workspace; commit identity is a non-secret injected value; broker signs if
   pushed — confirm in `05`.)
3. **RESULT trust at T2** — the RESULT is written by an untrusted capability;
   `metrics`/`confidence` are self-reported. The orchestrator MUST treat them as
   claims and lean on independent verifier capabilities (`04`).
4. **Tool I/O schema dialect** — JSON Schema vs a lighter typed DSL; must align
   with how the harness adapter exposes tools (native vs MCP, `01` §8.6).
```
