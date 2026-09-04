# 14 — Observation Plane (Three Channels, One Clock)

How mitos knows what a harness is doing. This doc owns the **observation
contract**: the three channels, their trust ranking, the quiescence clock that
makes discrete reads possible, and the terminal's bounded, re-admitted role.

It supersedes the observation half of `01 §8.6` and `05 §5.2`; it does not
redefine the harness adapter's field names (`01 §8.6` remains canonical) and it
introduces no new authority path — every channel here is **read-only
observation plus mitos-initiated control**, never an agent callback (`01 §8.5`).

RFC 2119 keywords are normative.

---

## 0. The problem this doc fixes

`01 §8.6` bet the whole observation story on **structured output**: run the
harness in a non-interactive mode that emits a machine-readable stream, drain it
per iteration. That bet holds for `Market` (`02 §1`) and breaks for `Studio`:

> Running a harness in its structured-output mode **is** running it in a
> different mode — non-interactive, no permission prompts, no TUI, different
> defaults, different failure behaviour. An operator who wants their own agent
> observed does not want it observed *as a different program*.

The prior implementation reached for the terminal and failed there too. The
failure is worth naming precisely, because the wrong lesson was recorded:

> **The defect was streaming, not the PTY.** A PTY carrying a TUI is a *display*
> protocol, not a data protocol. Parsing it continuously means sampling a
> surface mid-repaint: torn frames, escape sequences split across read
> boundaries, spinner rows read as new content, wrapped-line ambiguity, and —
> the fatal one — **no way to distinguish new information from the same
> information redrawn.** Every error class the prior attempt hit follows from
> continuous parsing, and none of them follow from the PTY itself.

This plane keeps the ban on continuous screen parsing and re-admits the terminal
under conditions that make a screen read a *replayable function of a byte
prefix* — and therefore auditable, meterable, and safe to base control on.

---

## 1. Three channels, ranked by trust

| # | Channel | Answers | Trust | Available |
|---|---|---|---|---|
| **C1** | **Effect surface** — fs / exec / net crossings, mediated at the sandbox edge (`05 §5`) | *what actually happened* | ground truth; unforgeable by the agent | always |
| **C2** | **Session artifacts** — the harness's own structured record (Claude Code `$CLAUDE_CONFIG_DIR/projects/**/*.jsonl`, equivalents elsewhere) | *what the model said, planned, and called* | high — the harness's own record, not our reading of a screen | when the adapter declares it (§4) |
| **C3** | **Terminal** — the PTY grid | *what is this terminal waiting for, right now* | low; bounded questions only | interactive runs only |

Three normative rules bind them:

1. **Run semantics MUST come from C1 and C2.** Progress, tool calls, token
   usage, results, and every input to the orchestrator (`04 §3.1`) are read from
   C1/C2. C3 is never a semantic source.
2. **C3 answers exactly one class of question** — the *modal state* of the
   terminal (§3.2): is something being waited on, of what shape, with what
   options. This is a bounded, enumerable classification, not open-ended content
   extraction.
3. **Where C2 can answer a question, C2 wins.** C3 is consulted only for
   questions C2 structurally cannot answer, i.e. the harness's own interactive
   prompt state, which by construction is not in its session record until after
   it is answered.

### 1.1 Why C2 is the unlock

`05 §6` already redirects `HOME` and the harness's native config/session store
to ephemeral, **mitos-controlled** paths inside the sandbox, for hermeticity.
The consequence was not previously drawn:

> Because mitos chooses where the harness writes its session record, mitos can
> **tail that record**. A modern harness emits a structured, append-only session
> log *in interactive mode*. That is a structured stream from an unmodified
> interactive session — the thing `01 §8.6` assumed required a mode change.

C2 therefore satisfies the Studio constraint (no mode change) with Market's
data quality (structured, no scraping). The adapter declares its shape (§4);
the substrate tails it and normalizes into the same event vocabulary
(`01 §8.6`: `assistant_text | tool_use | tool_result | token_usage | status |
final_result`). **One normalized stream, two possible sources** — headless
stdout for Market, session artifacts for Studio — and nothing downstream of the
adapter knows which.

---

## 2. The quiescence clock

Everything discrete in mitos happens on the same edge. Define it once.

```
quiet(tree, T)  ≡  for the last T ms, in the run's process tree:
                     no bytes written to the PTY master   (if any)
                   ∧ no writes under the workspace
                   ∧ no new *distinct* exec argv
```

A **quiescence edge** is the transition from not-`quiet` to `quiet`. Every edge
is an Effect (`01 §8.2` open list):

```
quiescent { for_ms, pty_offset, workspace_writes_since, distinct_execs_since, tree_size }
```

`distinct` is load-bearing: an agent spinning `sleep 1; curl …` executes
constantly and progresses never. Repeating an argv already seen in the window
does not reset the clock. This closes the polling-loop false negative that makes
naive idle detection useless.

### 2.1 Three thresholds, one mechanism

| Threshold | Default | Used for |
|---|---|---|
| `T_snap` | 200 ms | the grid has finished repainting → a snapshot is stable (§3.1) |
| `T_steer` | 750 ms | it is safe to write control bytes (§3.3) |
| `T_block` | 45 s | nothing is happening that will resume on its own → `blocked_on_human` (`16 §2`) |

`T_snap < T_steer < T_block` MUST hold. All three are per-capability tunable;
`T_block` is per-harness (a harness that thinks for minutes needs a longer one)
and MUST be derived from observed think-time percentiles, not guessed.

### 2.2 What the clock serves

One mechanism, five consumers — this is why it is specified here and not
inside any of them:

| Consumer | Uses the edge to |
|---|---|
| terminal snapshot (§3.1) | pick a moment when the grid is a document, not a stream |
| steer window (§3.3) | write only when the TUI is not mid-render |
| `blocked_on_human` (`16 §2`) | detect a stall no iteration boundary will ever report |
| checkpointing (`16 §3`) | choose a consistent moment to commit + snapshot |
| the watch surface (`11 §4` inner zoom) | sample frames so the UI never shows a torn screen either |

---

## 3. The terminal, re-admitted

Three separations make C3 safe. Each removes one error class from §0.

### 3.1 Separation 1 — parse continuously, materialize on demand

The VT parser MUST be fed **every** byte (it is a state machine; skipped bytes
desynchronize it) and MUST emit **nothing**. It maintains a grid; no consumer
reads that grid except by explicit call.

```
spool(session)             # continuous: raw bytes → append-only artifact, first-pass redaction
snapshot(session)          # on demand:  {:ok, grid} | {:error, :unstable}
```

`snapshot/1` MUST refuse (`:unstable`) unless the parser is at a consistent
boundary — no pending escape sequence, no partial UTF-8 — **and** the session is
at a quiescence edge of at least `T_snap`. This is what kills torn frames and
split escapes: they are not handled, they are *not observed*.

```
Grid {
  rows, cols, cells[], cursor {row, col, visible},
  alt_screen : bool,
  stable_at  : iso8601,
  pty_offset : u64,          # byte offset in the spool this grid corresponds to
  digest     : sha256        # of the rendered grid
}
```

**Replayability (the property that makes C3 auditable).** `snapshot` is a pure
function of the spool prefix `[0, pty_offset)`. Given the spooled artifact, any
later reader — a replay (`04 §7`), an audit, a proof (`08 §5`) — recomputes the
identical grid and verifies `digest`. A screen read is therefore evidence, not
an unrepeatable observation. **This is the precise reason the `01 §8.6` ban is
lifted:** the objection was that PTY did not compose with audit/proof/metering,
and a replayable, digested, offset-anchored snapshot does.

### 3.2 Separation 2 — a bounded question, and `:unknown` is an answer

The grid is consumed only by a per-harness **modal classifier** with a closed
output type:

```
classify(grid) :: %{
  kind      : :idle | :prompt | :menu | :input | :busy | :error | :unknown,
  options   : [%{id, label}],     # for :menu / :prompt
  selected  : index | nil,
  prompt_text : string | nil,     # verbatim, for the decision card only (15 §3)
  digest    : sha256              # of the grid it classified
}
```

- The classifier is **declared by the harness adapter** (§4), versioned with it,
  and covered by contract tests (§5).
- `:unknown` MUST escalate to a human (`15`), never guess. A classifier that
  guesses converts a display change into an unlogged authority decision.
- No run-control decision may depend on a screen read alone: a Decision whose
  trigger was a classification MUST carry the grid `digest` and `pty_offset`
  in its `inputs_hash` preimage (`01 §8.1`), so replay can prove what was on
  screen when the decision was taken.

### 3.3 Separation 3 — control is a verified transaction, never a keystroke

Writing to the PTY master is stable; firing and forgetting is not. Every
control write is a transaction on a quiescence edge:

```
steer(session, command):
  wait for quiescence edge ≥ T_steer            # else :busy, retry or escalate
  before ← snapshot(session)                    # MUST be :ok
  expected ← transition(before, command)        # declared by the classifier
  write(session, encode(command))
  after ← snapshot(session) at the next edge    # bounded wait, else :timeout
  if matches(after, expected):  EMIT steer_applied{before.digest, after.digest, command}
  else:                         EMIT steer_rejected{...}; send ESC; escalate (15)
```

- `command` is the `01 §8.6` vocabulary — `approve | answer | interrupt |
  select | prompt` — unchanged.
- A `steer_rejected` MUST NOT be retried automatically. A screen that did not go
  where the classifier predicted means the classifier is wrong about this build
  of this harness; retrying blind is how a wrong keystroke lands in a prompt.
- Every steer is a Decision→Effect pair on the log like any other authority act.
  Mitos-initiated, `05 §5.2`; the agent never calls home.

### 3.4 What C3 is still not

- Not a conversation channel. `01 §8.6`'s ban on live-conversation PTY steering
  **stands**. `steer` answers the harness's own modal prompts and injects
  operator-authored corrections at step boundaries; it does not carry a chat.
- Not a progress source. `no_progress` (`04 §4.3`) MUST be computed from C1/C2.
- Not required. A run with `interactive: false` never opens a PTY, and every
  mechanism above degrades to C1+C2 with no loss except modal-state visibility —
  which a non-interactive harness does not have.

---

## 4. The observation capability matrix

`01 §8.6`'s `capabilities()` is extended with an **observation** block. The
adapter declares what can be observed; the substrate never probes or assumes.

```
observation: %{
  structured_output : :stream | :artifact | :none,
  session_artifacts : %{ path_template, format, schema_version, tail: bool } | nil,
  interactive       : bool,
  modal_classifier  : module | nil,
  pty_control       : bool,
  resume            : :session_id | :none,
  think_time_p95_ms : int          # seeds T_block (§2.1)
}
```

**Degradation is explicit, never assumed.** The substrate computes the
observable set from this block and refuses configurations it cannot observe:

| `structured_output` | `session_artifacts` | Studio (T0/T1) | Market (T2) |
|---|---|---|---|
| `:stream` | any | usable headless | **preferred** |
| `:none` / `:artifact` | present, `tail: true` | **preferred** — interactive, C2 tail | usable |
| `:none` | absent | C1 + C3 only — MUST warn; MUST NOT run unattended (`16`) | **MUST refuse** |

A harness observable only through C1+C3 can be watched by a human but MUST NOT
be granted an unattended horizon (`16 §6`), and MUST NOT serve a Market run —
because a buyer's proof (`08 §5`) cannot be built from a screen.

---

## 5. Contract tests — the maintenance cost, made visible

Harness churn is the largest ongoing cost in this design (`02 §8` R1). It MUST
be detected by CI, not by a stalled overnight run.

- Every adapter ships a **contract suite** asserting its `observation` block
  against the real upstream binary: does the session artifact appear at the
  declared path, in the declared format; does the classifier classify the
  harness's actual prompts; does each `steer` command produce its declared
  transition.
- The suite runs **nightly against upstream `@latest`**, not only against the
  pinned version.
- A failing contract **quarantines** the adapter: Studio warns and drops the
  run's unattended horizon to zero (human-watched only); Market refuses to
  schedule it. Quarantine is an Effect (`harness_quarantined`), so the reason a
  fleet went quiet is on the log.

---

## 6. What this plane owns vs delegates

| Owns | Delegates to |
|---|---|
| the three channels and their trust ranking | the normalized event vocabulary — `01 §8.6` |
| the VT state machine, and its purity over the byte prefix | the pty itself — the engine (`01 §7`); mitos ships a `script(1)`-backed local transport so the channel is demonstrable without it |
| the quiescence clock and its Effects | the signals computed from it — `04 §4.3`, `16 §2` |
| terminal spool / snapshot / classify / steer transaction | authority and secrets — `05 §5` (unchanged) |
| the observation capability matrix + contract tests | trust-tier → provider/posture — `05 §4` |
| replayability of a screen read | replay of the run — `04 §7` |
| what happens on `:unknown` / `steer_rejected` (escalate) | how a human is asked — `15` |

## Cross-references

- Harness adapter contract, Decision/Effect schema: `01 §8.1`, `§8.2`, `§8.6`.
- Authority boundary, hermeticity (why C2 is mitos-controlled): `05 §5`, `§6`.
- Escalation signals that consume the clock: `04 §4.3`, `16 §2`.
- Asking a human: `15_DECISION_PLANE`.
- Unattended operation, checkpoints, horizon: `16_AUTONOMY`.
- Inner-zoom watch surface: `11 §4`.
