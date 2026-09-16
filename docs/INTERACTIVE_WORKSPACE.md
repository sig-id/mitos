# Interactive project workspace

The default page (`/` or `/workspace`) hosts actual local CLI sessions. The
earlier batch execution interface remains at `/overview`, `/work`, and its
existing routes. The two paths have different execution contracts.

## Sessions

Open a project directory beneath `--workspace-root`, then create a session. An
empty command starts interactive Bash; a supplied command runs through Bash's
login shell in a PTY. It can launch any installed coding CLI. The browser uses
vendored xterm.js, with real terminal input and resize rather than a reconstruction
of a model conversation. Interrupt sends Ctrl+C to the terminal foreground group;
Stop terminates observed processes in its OS session and closes the PTY.

Sessions use the operator's environment, home/configuration, and credentials. They
modify the selected directory directly, and multiple sessions can share it. This
is trusted local execution, not the confined batch provider or Nucleus. Shell
operations can bypass coordination; escaped descendants cannot be guaranteed to
stop through process sampling. No unattended grant or automatic decision approval
is implied. The existing batch provider continues to use private workspace copies.

One active coordinator is allowed per project. It is a user-selected CLI session
with a visible role and coordination instructions; Mitos does not start a separate
model service or inject terminal keystrokes to imitate a model protocol. The user
can direct that agent to read the supplied instructions and use the project tools.

Input requests are serialized, with a bounded in-memory duplicate-key window.
The browser does not automatically retry ambiguous input. Output is polled by byte
offset and transported as base64 to preserve terminal bytes. It is saved separately
from metadata, capped at 16 MiB per session/job; excess output stops execution.
Leaving the page does not stop the process. Draft prompts and replies remain in
browser session storage. Terminal output can include sensitive CLI output and is
kept locally with private filesystem permissions.

Metadata lives in `.mitos/desk/state.json`, written by atomic replacement and
fsync under a separate exclusive lock. It is not part of the batch evidence hash
chain and must not be represented as acceptance evidence. Output files are retained
in the same directory. Normal server shutdown stops sessions and managed jobs.
After a crash, active records become interrupted and are never replayed. Before
starting a replacement session in that project, the operator must inspect and
stop surviving processes and explicitly settle the interrupted session. Managed
jobs retain a separate interruption hold with the same inspection requirement.

## Topic board

Topic indexes return title, short summary, relevant paths, revision, and update
time. They omit full posts. Thread reads return at most 50 posts after a cursor;
the caller advances using the last returned sequence, not the total revision.
Following a topic stores an explicit acknowledgment cursor. Reading alone never
acknowledges content. Posts require the expected revision, rejecting stale replies
and duplicate resubmission at the old revision.

The board is discoverable shared context, not a lock or authorization system.
There is no automatic injection of board contents into a CLI's context, and no
claim of automatic dependency invalidation from prose. Agents choose relevant
topics and retrieve details. The UI follows as the operator; agents follow under
their session IDs. Local session IDs identify participants but are not credentials
or an isolation boundary against same-user processes.

Sessions receive:

- `MITOS_BIN`: absolute path to the running Mitos binary.
- `MITOS_URL`, `MITOS_PROJECT_ID`, `MITOS_SESSION_ID`: project tool context.
- `MITOS_COORDINATION`: a local Markdown file describing the tools and role.
- `TMPDIR`: a private session scratch directory.
- `GIT_TRACE2_EVENT`: a trace file in scratch for observed Git command provenance.

For example, inside a hosted session:

```sh
"$MITOS_BIN" desk topics --query checkout
"$MITOS_BIN" desk create-topic --title 'Checkout API' --body 'Schema version 2'
"$MITOS_BIN" desk topic --after 0 topic_ID
"$MITOS_BIN" desk post --topic topic_ID --revision 1 --body 'UI will consume v2'
"$MITOS_BIN" desk follow --topic topic_ID --after 2
"$MITOS_BIN" desk topics --following
"$MITOS_BIN" desk jobs
"$MITOS_BIN" desk build -- --release
"$MITOS_BIN" desk join job_ID
"$MITOS_BIN" desk release job_ID
"$MITOS_BIN" desk resources
```

Outside a hosted session, use `--project PROJECT_ID --url http://127.0.0.1:4646`.
Flags precede positional IDs. All responses are JSON.

## Managed builds and resources

The Cargo adapter starts `cargo build` with a bounded allowlist of flags, without
shell interpolation. It serializes managed jobs per project. A new request queues
a fresh build; another participant can explicitly join a known job's output.
Releasing one participant does not stop a job with other consumers. No automatic
equivalence or cached-result reuse is asserted for a mutable shared directory.
Exit 0 is the command's result, not proof that every participant's latest edits
were tested. Build deadlines are 30 minutes.

Cargo configuration and the CLI's environment still determine tool behavior.
Commands run outside this adapter are not intercepted. Cross-project caches and
arbitrary Git writes are not protected by these project build holds. Resource
mediation and verified snapshot-based result sharing remain provider integration
work, described in [the shared-workspace proposal](SHARED_WORKSPACE_COORDINATION.md).

The inspection views show real Git status, tracked diff against HEAD, worktree
metadata, sampled process identity, RSS, cumulative CPU time (with raw ticks retained in the API), handle counts,
observed workspace handles, and a bounded directory allocation scan. RSS can share
pages across processes; allocated bytes can double-count hardlinks or shared
storage. These are labeled measurements, not precise billing or reclaimable-space
estimates. File authorship stays unknown. Git Trace2 command names have session
provenance, but tracing can be disabled and does not establish line attribution.
Reads of Git trace tails are bounded; trace files themselves are retained in
session scratch until explicitly removed.

Scratch inspection covers the most recent 64 sessions. Cleanup can only remove
a selected session's managed scratch directory, with no active or interrupted
session or managed job in that project. This is an explicit operator action;
there is no automatic deletion of source files, worktrees, branches, ignored files,
or Cargo outputs. A sampled empty handle list alone does not authorize deletion.

## API

These endpoints share the server's loopback Host and cross-origin checks. Mutations
accept JSON and bounded bodies. The deployment remains trusted and single-user.

| Endpoint | Behavior |
|---|---|
| `GET/POST /api/projects` | List or open project `{name,directory}` |
| `GET/POST /api/projects/{id}/sessions` | List or launch `{name,role,command}` |
| `GET /api/sessions/{id}` | Session lifecycle and command |
| `GET /api/sessions/{id}/output?offset=N` | Up to 64 KiB of terminal bytes and next offset |
| `POST /api/sessions/{id}/input` | `{key,data}` to the existing PTY |
| `POST /api/sessions/{id}/resize` | `{rows,cols}` |
| `POST /api/sessions/{id}/stop` | Stop live execution |
| `POST /api/sessions/{id}/settle` | Confirm interrupted processes stopped with `{confirmed_stopped:true}` |
| `POST /api/sessions/{id}/scratch/clean` | Explicit cleanup after holds settle |
| `GET/POST /api/projects/{id}/topics` | Search summaries (`q`, `follower`) or create `{title,summary,paths,author}` |
| `GET /api/topics/{id}?after=N` | Bounded thread delta |
| `POST /api/topics/{id}/posts` | `{body,revision,author}` |
| `POST /api/topics/{id}/follow` | `{session,cursor}` |
| `GET /api/projects/{id}/resources?details=true` | Process, Git, scratch, and filesystem inspection |
| `GET/POST /api/projects/{id}/jobs` | List jobs or queue `{args,author}` |
| `POST /api/jobs/{id}/join` | Add `{author}` as consumer |
| `POST /api/jobs/{id}/release` | Release `{author}`; interrupted jobs require operator inspection confirmation |
| `GET /api/jobs/{id}/output?offset=N` | Bounded shared command output |
