# Agent handoffs

Mitos uses `mitos.handoff/v1` for native coordinated workers and
`mitos.cooperation/v2` for requests. Scheduling, graph acceptance and native
approval decisions remain separate runtime authorities. Markdown and stdout are
human-readable evidence, never commands or implicit result submissions.

## Result lifecycle

A native worker registers the files supporting its findings, calls `result_submit`,
and then finishes its ordinary final response. The submission includes `version`,
an idempotency `key`, a summary of at most 2 KiB, artifact IDs/roles, checks,
limitations and unresolved request IDs. Check outcomes are `passed`, `failed` or
`not_run`; these are the agent's claims, with references to its registered evidence.
Roles are `report`, `log`, `file`, `patch`, `test_evidence` and `publication_receipt`.

The runtime supplies project, task, run, task revision, result ID, journal sequence,
time and digest. The tool returns a compact durable receipt; `result_get` returns
its content in bounded pages. Exactly one immutable result is permitted per attempt.
Identical retries return the original receipt, including after restart; a different
payload conflicts. A lost response must be retried with the identical payload.

Submission is incomplete evidence until execution succeeds and settles. Successful
native workers require a valid submission, intact referenced artifacts, unchanged
dependency input copies and answered blocking requests. Failure/cancellation keeps
registered files and harvested output; it does not turn a submission into a
successful candidate. Acceptance additionally checks current revisions, exact
producer run, integrity and request settlement. Its receipt binds `result_id`.
An orchestrator still returns the independently validated `OrchestratorPlan`.

Once a submission is recorded, the attempt's remaining time budget is only a
bounded wrap-up window (60 seconds). When the window or the attempt's own
deadline ends the session, the run settles with the submitted result instead of
failing finished work and holding a worker slot for wrap-up; `deadline_after_submit`
evidence records the submission sequence and how long after it the budget ended.
A restart in the same window recovers to the same settled state: the leftover
workspace changes are harvested and persisted, and the bound dependency input
copies are re-verified against their recorded fingerprint, before the result is
collected (`restart_after_submit`); an attempt whose workspace or input copies
cannot be recovered unchanged keeps its interruption decision for the operator
instead.

## Proposals and yield

Native subagents are disabled in both harnesses. A worker that needs
other agents asks for them in the `proposals` field of `result_submit`, as part of
ending its attempt. It never creates tasks, and there is no separate propose tool.
Each proposal has:

| Field | Meaning |
| --- | --- |
| `id` | worker-local key, unique within the result |
| `rationale` | why the work is needed (1–4000 bytes) |
| `blocking` | `true`: the attempt yields and waits for the work; `false`: follow-up work, the result is final |
| `tasks` | 1–8 application task specs in the orchestrator's `add` shape (suggestions; dependencies may only name tasks in the same proposal or existing graph tasks). Native agent tasks declare `traits` `{nature, size, intelligence}`; requesters never choose models, and the execution policy may route the work to a different harness than the proposer's |
| `artifacts` | registered artifact IDs of this run, as evidence |

The receipt's `proposal_ids` holds one kernel ID per proposal, in order.
`result_get` lists each proposal as a `proposal` entry with that ID and its review
outcome (`pending`, `accepted` or `rejected`); a yielded result's state is
`yielded`. Idempotency is the result's: an identical retry returns the same
receipt and IDs.

Proposals are refused with a typed failure when:

- the attempt was not granted `work.propose` (`capability_not_granted`). Without
  the grant, the field is also absent from the advertised schema.
- the graph has no review path (`defined` mode), or the caller is an orchestrator
  (`proposals_unsupported`).
- a proposal is malformed (`invalid_proposal`, with the proposal index as
  `position`) or cites an unknown artifact (`artifact_missing`).
- a native task's traits are missing or unsupported by its role
  (`invalid_proposal_traits`), or no permitted harness could run it, including an
  unpermitted `harness` override (`proposal_unroutable`).
- the proposer's own task is `context` or `review` work and the proposal is
  blocking: the submission is judgment-gated rather than silently refused
  (`proposal_blocking_not_permitted` when refused; otherwise it is held with
  `held=kernel_judgment` in the receipt and the attempt parks with the decision);
  follow-up proposals remain allowed.
- the task would exceed 4 proposals across its attempts (`proposal_limit_exceeded`),
  or 2 yields (`yield_limit_exceeded`), or the proposed tasks exceed the graph's
  remaining task allowance (`proposal_over_budget`).

Unknown fields and other versions are refused like the rest of the submission.
`GET /api/handoff` publishes these limits under `proposals`.

A proposal is not admitted work. When the attempt finishes, its proposals are
journaled in the same record as its outcome trigger, so the orchestrator reviews
them with that trigger. It answers each one with `accept_proposal` or
`reject_proposal` (see [adaptive-runtime.md](adaptive-runtime.md#yield-and-continuation)),
and any accepted work passes normal graph validation and budgets. A blocking
proposal's outcome reaches the proposer when it resumes: accepted work as
dependency inputs, rejections as the reviewer's rationale.

## Tools and HTTP

`GET /api/handoff` exposes the version, canonical tool schemas and implemented
limits. The Codex app-server adapter advertises the same schemas as dynamic tools:

| Tool | Input | Output |
| --- | --- | --- |
| `artifact_register` | relative workspace `path`, `role` | immutable descriptor |
| `result_submit` | typed result submission, optionally with proposals | durable receipt with `proposal_ids` |
| `result_get` | `run_id`, `cursor`, `limit` | result metadata, entry page, next cursor |
| `artifact_list` | `run_id`, `cursor`, `limit` | descriptor page, next cursor |
| `artifact_read` | `run_id`, `artifact_id`, byte `offset`, byte `limit` | descriptor, base64 `data`, next offset, complete flag |
| `artifact_search` | `run_id`, `artifact_id`, literal `query`, byte `cursor`, match `limit` | matching byte offsets, next cursor, complete flag |
| `cooperation_submit` | v2 message without producer identity | journal receipt |
| `cooperation_list` | sequence `cursor`, `limit` | visible messages and projected states |

Agent HTTP calls use
`POST /api/agent-sessions/{session}/handoff/{tool}` with the leased session bearer.
The session binds the producing run and workspace; payloads cannot override them.
Native calls use this same authenticated engine operation. Unsupported versions,
unknown fields and malformed operations are refused.

The local operator/frontend calls
`POST /api/projects/{project}/runs/{run}/evidence/{tool}` for `result_get`,
`artifact_list`, `artifact_read` and `artifact_search`. This is a read-only,
project/run-scoped projection with the same bounds and integrity checks. Operator
reads do not spend the model's allowance. Existing graph discovery and run APIs
expose the same receipts; backend-created work needs no frontend database insertion.
The attempt inspector shows typed results, check claims, limitations, artifact
availability and bounded previews, plus paginated cooperation requests. Pending
requests also appear as informational notifications, without approval controls.

Artifact lookup uses a run ID to select an exact manifest; descriptors carry the
producing result ID when submitted. Result IDs do not authorize arbitrary lookup.
Workers can read referenced result evidence and explicitly staged artifact names
from their selected **data** dependency witnesses. Control-only dependencies grant
no file access. Requests explicitly grant their cited artifact IDs to named resolver
attempts while pending/answered, without exposing the producer’s entire result. Orchestrators receive frozen review-run grants from the graph;
they cannot switch to a newer producer attempt. The native prompt includes bounded
summary/manifest metadata and directs the agent to retrieve evidence, rather than
requiring an evidence-copying worker when a patch exceeds prefetch limits.

## Exact bytes, storage and budgets

Descriptors include an opaque ID, producing task/run/result, display name, role,
media type, size, SHA-256, availability and retention policy. Native `report.md`
and a registered workspace `report.md` have distinct IDs. Registered files are
copied into the runtime's immutable artifact store before the receipt is committed;
scratch directories are not needed for later reads. Symlinks and unsafe paths are
refused. Reads verify the retained file's regular-file type, length and SHA-256.
Acceptance rechecks every referenced artifact.

Limits are distinct:

- Registered evidence: 64 MiB/file, 128 MiB/attempt; 4,096 total retained objects
  per attempt, including runtime logs/manifests. Storage failure is explicit.
- Proposals: 4 per task, 8 task specs per proposal, 16 KiB per proposal, 2 yields
  per task.
- Result summary: 2 KiB. Lists: at most 32 artifacts or 16 result entries per page,
  reduced further to keep metadata below 48 KiB after JSON escaping.
- Read data: at most 32 KiB/call. Exact bytes are base64 encoded; UTF-8 split across
  reads and arbitrary binary bytes reconstruct without loss. A null continuation
  and `complete: true` identify EOF. A range is never presented as a whole file.
- Search: literal UTF-8 query of 1–256 bytes, at most 32 matches/call, scanning
  32 KiB plus query overlap. Matches are byte offsets; follow the continuation
  even when no matches were returned. Overlap does not duplicate matches.
- Native retrieval: 2 MiB of read/search bytes per attempt, persisted in the
  journal. Repeated reads count again. Exhaustion reports
  `retrieval_budget_exhausted` and the unchanged byte position; metadata stays
  discoverable. Lists, reads and searches expose `retrieval_bytes_used` and
  `retrieval_bytes_limit`. Resume work with a new authorized attempt to obtain a new budget.
- Tool response envelopes: at most 64 KiB. These are **not token limits**.
  Exact cumulative model-token enforcement is advertised as unsupported.

Retention is `until_store_deleted`; this version has no expiry/eviction mechanism.
Missing files are `unavailable`, changed files are `integrity_failure`, and unsafe
paths are refused. Large files remain listed and readable within the call/attempt
budget. Nothing is silently represented as a complete truncated deliverable.
The older patch-prefetch threshold still bounds staging for registered command
applications; native typed-result review uses the artifact tools instead.

## Versioning and history

New native attempts require typed submissions; there is no prose parser or dual
worker-result implementation. Existing journal entries are preserved verbatim;
new worker attempts freeze `handoff_protocol` at creation, and old runs have no
contract marker or typed receipt. Already completed candidates remain reviewable;
receipt enforcement is not retroactive. Replayed cooperation records take their
sequence from the enclosing journal event. No journal rewrite is needed. New cooperation writes accept v2 only. Historical v1
messages are inspectable but project as `unsupported_version` for settlement;
old pending requests cannot unblock new work. There is no automatic adoption or
legacy write handler. See [cooperation.md](cooperation.md) for v2 lifetime rules.

This contract does not implement arbitrary remote-worker identity, automatic
permission grants, exact model-token accounting, artifact TTLs, or workflow-level
request adoption. Those capabilities are deliberately unsupported, rather than
inferred from message prose. Existing final-answer Markdown/file-path citations
remain a separate contract; typed result references use artifact IDs.
