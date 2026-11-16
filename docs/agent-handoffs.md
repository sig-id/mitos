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

## Tools and HTTP

`GET /api/handoff` exposes the version, canonical tool schemas and implemented
limits. The Codex app-server adapter advertises the same schemas as dynamic tools:

| Tool | Input | Output |
| --- | --- | --- |
| `artifact_register` | relative workspace `path`, `role` | immutable descriptor |
| `result_submit` | typed result submission | durable receipt |
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
new fields are additive, and old runs simply have no typed receipt. No destructive
journal rewrite is needed. New cooperation writes accept v2 only. Historical v1
messages are inspectable but project as `unsupported_version` for settlement;
old pending requests cannot unblock new work. There is no automatic adoption or
legacy write handler. See [cooperation.md](cooperation.md) for v2 lifetime rules.

This contract does not implement arbitrary remote-worker identity, automatic
permission grants, exact model-token accounting, artifact TTLs, or workflow-level
request adoption. Those capabilities are deliberately unsupported, rather than
inferred from message prose. Existing final-answer Markdown/file-path citations
remain a separate contract; typed result references use artifact IDs.
