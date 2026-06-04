# 06_AUTHORITY_CALLBACK_PLANE.md — Closing the Response Loop

Status: draft v0.1
Date: 2026-06-05
Assumption: the authority plane skeleton (`beam/lib/mitos/authority.ex`), the
sidecar CONTROL/EVENT framing (`src/sidecar/protocol.zig`), and `OD_*` env
injection (`src/sidecar/sidecar.zig`) already exist and are per-run wired
(`Session.maybe_start_authority/2`).

---

## 1. Direction

The control plane already exposes a request channel to the agent: a run-scoped
Unix socket, a minted `OD_TOKEN`, and a declared method set
(`tool_broker, approval, artifact_register, memory_fetch, subrun_spawn`). What
is missing is the **return half** — the agent cannot await the verdict of its
own request. Every method is the same shape: a correlated
`request → suspend → typed reply`. This plan builds that one primitive once and
lights up all methods behind it.

This is not greenfield. The scope is deliberately narrow: unify the request-id
spaces, add reply correlation, route verdicts back to the originating channel,
then fill the `not_implemented` branches.

Non-goals for this plan: new transports beyond pty, durable checkpoint/resume
(depends on this work but is a separate plan), and cross-host sub-runs.

---

## 2. The Gap, Precisely

Three concrete defects, each with a fix lane:

1. **Fire-and-forget request.** `Authority.handle_request/2`
   (`authority.ex:115-117`) raises an `ACTION_REQUESTED` event and immediately
   replies `{ok, status: "requested"}`. The socket never blocks for the verdict,
   so `approval` cannot suspend the agent.

2. **Disjoint `action_id` namespaces.** The Zig prompt detector mints `action-N`
   (`sidecar.zig:956`, `emitActionRequestUnlessFastPath`); the authority socket
   mints `authority-N` (`authority.ex:102`). `AUTHORIZE`/`DENY` resolve only the
   sidecar's `action-N` space (`sidecar.zig:662-693`). An `authority-N` verdict
   has nowhere to land.

3. **Verdict routed to the wrong consumer.** `Session.authorize/3`
   (`session.ex:229-235`) sends `AUTHORIZE` down to the *sidecar*, which gates
   *terminal input*. Authority-originated requests need the verdict returned to
   the *socket caller*, not injected as PTY input.

---

## 3. Shared Contract First

Before touching either runtime, pin the envelope shape. One request id space,
one reply schema, regardless of origin (scraper, socket, or future codec).

```text
beam/lib/mitos/authority/request.ex   # %Request{id, method, action, context, origin, from}
beam/lib/mitos/authority/verdict.ex   # %Verdict{id, decision, payload, reason}
```

Contract:

- `request_id` — single monotonic space, minted by the **control plane**, not by
  the producer. Both the scraper path and the socket path obtain ids from the
  same allocator so a verdict is unambiguous.
- `origin` — `:terminal_scrape | :authority_socket | :codec` — tells the plane
  *where the verdict must be returned*, decoupled from *who decides*.
- `decision` — `:authorize | :deny`; `payload` carries method results
  (broker stdout, fetched memory bytes, spawned sub-run handle).
- `from` — opaque reply handle (a `GenServer.from` for socket calls; `nil` for
  scraped prompts whose "reply" is a CONTROL frame to the sidecar).

The `ACTION_REQUESTED` event and `approvals.jsonl` record stay as-is; they are
the observability projection, not the correlation mechanism.

---

## 4. Workstream A: Pending-Request Table + Reply Routing (BEAM)

Owned paths:

```text
beam/lib/mitos/authority.ex
beam/lib/mitos/session.ex
beam/lib/mitos/authority/request.ex
beam/lib/mitos/authority/verdict.ex
```

Responsibilities:

- Hold a `pending: %{request_id => %Request{}}` map. Decide its owner: the
  `Session` GenServer (single point that already sees both the scraper events
  and the UI verdicts) is the recommended home; `Authority` forwards rather than
  stores, so a socket disconnect cannot strand a verdict.
- Make socket requests **block**: `Authority` issues a `GenServer.call` to the
  session and parks the `from`; the reply is sent only when a matching
  `%Verdict{}` arrives. Apply the existing 5s recv timeout as the socket-side
  ceiling and return a typed `{:error, :timeout}` rather than closing silently.
- Route verdicts by `origin`: `:authority_socket` → reply to the parked `from`;
  `:terminal_scrape` → emit the `AUTHORIZE`/`DENY` CONTROL frame as today.
- Fold `Session.authorize/3` and `deny/3` into a single
  `resolve(request_id, decision, payload)` that looks up `origin` and dispatches.

Deliverables:

- An agent calling `approval` over the socket blocks until a human/policy verdict
  and receives `{ok, decision, payload}`.
- Scraped prompts continue to gate terminal input unchanged (regression-guarded).
- A disconnected socket caller never leaves a verdict undeliverable.

---

## 5. Workstream B: Unify the Request-Id Space (Zig)

Owned paths:

```text
src/sidecar/sidecar.zig
src/transport/types.zig
```

Responsibilities:

- Replace the sidecar-local `next_action_id` (`sidecar.zig:961`) and the
  authority-local counter with ids minted by the control plane, OR namespace
  both into one space the BEAM owns. Simplest: the sidecar tags scraped requests
  with `origin: "terminal_scrape"` and lets the BEAM assign the canonical id on
  ingest; the sidecar keys its own `handleAuthorize`/`handleDeny` on the id the
  BEAM echoes back.
- Carry `origin` and `request_id` on the `ACTION_REQUESTED` EVENT and on the
  `AUTHORIZE`/`DENY` CONTROL frames so the round-trip is self-describing.
- Keep `tier2_fast_path_rules` (`types.zig:213`) as a *property of the envelope*:
  a fast-path match resolves the request with an auto-`:authorize` verdict
  through the same table, not via the current silent `return` skip.

Deliverables:

- A single id resolves a verdict regardless of producer.
- Fast-path auto-approvals appear in `approvals.jsonl` as decided requests, not
  as absences.

---

## 6. Workstream C: Fill the Method Branches

Behind the now-correlated envelope, implement the `not_implemented` methods
(`authority.ex:98`). Each is a `payload`-bearing verdict; none needs new
transport.

- `tool_broker` — execute the brokered side effect under policy, return
  stdout/exit in `payload`. Side effects flow through the plane so `commands.jsonl`
  is authoritative rather than reconstructed from PTY bytes.
- `memory_fetch` — project-scoped read keyed by `OD_PROJECT_ID`; return bytes in
  `payload`. Write path (`memory_put`) added symmetrically.
- `subrun_spawn` — start a child `Session`/run, return a run handle the parent
  can await/cancel. **Requires token attenuation** (Workstream D) before it
  ships — a sub-run must receive a strictly narrower token than its parent.
- `artifact_register` — record an artifact reference into the trace
  (`screenshots/`, `artifacts/` already exist in `Trace.open!/1`).

Deliverables:

- Each method returns a typed result, not `{ok, "requested"}`.
- `subrun_spawn` is gated behind attenuation and stays `not_implemented` until D
  lands.

---

## 7. Workstream D: Token Scoping + Attenuation

Owned paths:

```text
beam/lib/mitos/authority.ex
src/sidecar/sidecar.zig   # OD_TOKEN injection
```

The current token is plain-equality and non-expiring (`authority.ex:91`). That
is acceptable for a single trusted child and **wrong the moment `subrun_spawn`
exists**.

Responsibilities:

- Bind tokens to `{run_id, project_id, method-set, expiry}`. Verify the scope,
  not just equality.
- Attenuation: a parent run may mint a child token whose method-set and project
  scope are a subset of its own — never a superset.
- Time-box; expired tokens fail closed with a typed `unauthorized`.

Deliverables:

- A sub-run cannot escalate beyond its parent.
- Token verification is scope-aware and tested against the privilege-escalation
  case.

---

## 8. Sequencing

```text
Contract (§3)
   └─> A: pending table + reply routing (BEAM)   ─┐
   └─> B: unified id space (Zig)                  ─┤── must land together
                                                   │
   ───────────────────────────────────────────────┘
   └─> C: method branches (depends on A+B)
   └─> D: token scoping (blocks subrun_spawn in C)
```

A and B are co-dependent and should merge as one change — the id space and the
reply routing are two ends of the same wire. C and D fan out after.

---

## 9. Test Strategy

- **Round-trip, socket origin:** agent calls `approval`, blocks; UI calls
  `resolve(:authorize)`; agent unblocks with the verdict. Assert the parked
  `from` is replied exactly once.
- **Round-trip, scrape origin:** detector emits a prompt; verdict produces an
  `AUTHORIZE` CONTROL frame to the sidecar; terminal input proceeds. Assert no
  socket reply is attempted.
- **Id collision guard:** interleave a `terminal_scrape` and an
  `authority_socket` request; assert each verdict resolves its own request only.
- **Timeout:** socket caller blocks, no verdict within ceiling; assert typed
  `{:error, :timeout}` and table cleanup, not a hung `from`.
- **Attenuation:** parent token mints a child; assert child cannot request a
  method outside the parent's set.

---

## 10. Out of Scope (Follow-on Plans)

- Durable `checkpoint`/resume — the suspendable envelope built here is its
  prerequisite, but persistence of parked requests across a `Session` restart is
  a separate plan.
- Non-pty transports as real drivers (stdio-json, cloud-job) — the envelope is
  transport-agnostic by construction; wiring additional `Transport.open_fn`
  implementations is independent work.
