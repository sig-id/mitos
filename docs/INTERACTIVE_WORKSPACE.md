# Interactive project workspace

The default page (`/` or `/workspace`) hosts actual local CLI sessions. The
earlier batch execution interface remains at `/overview`, `/work`, and its
existing routes. The two paths have different execution contracts.

## Workspace navigation

The sidebar's workspace selector switches projects. Its supervisor is pinned separately from the agent list. The graph is the default
project view; projects and sessions are not parallel navigation lists.
Open the selector to choose another project or open an existing directory.

Right-click the selected workspace or a project in the selector for Open, Open in
new tab, New session, New task, and Copy project path. Right-click any session for
Open, Resume, Fork, Hand over, Interrupt, Stop/Close, and Copy session ID. Actions
apply to the clicked session, even when a different terminal is selected. Unavailable
actions are disabled according to current session state.

Focus a workspace or session and press Shift+F10 (or the Context Menu key) to open
the same menu. Arrow keys, Home/End, and typing a label's first letter move through
actions; Enter activates them. Escape closes the menu and restores focus.

## Sessions

Open any existing local project directory, then create a session. Absolute paths
can be outside `--workspace-root`; relative paths are resolved against it.
Symlinks resolve to their target so the same directory opens as one project.
The name is suggested from the directory and remains editable. Selecting
**Open project** confirms the directory; entering a path alone opens nothing.
Opening a project also makes it available to the task runner. **New task** in
the project sidebar uses that project's directory. Tasks accept the default
workspace or an explicitly opened project and its subdirectories; another
directory must first be opened as a project. Choose a detected agent or application
and an optional environment profile. A working coding agent requires an explicit first
goal. Supervisor setup first generates an editable project-context draft in a separate
background conversation; work starts only after review and a goal. The adapter launches its executable directly
in a PTY. Bash is an explicit shell choice. Names come from the first launch or
API prompt and remain stable; raw terminal keystrokes are not interpreted as
a naming prompt. See [agent adapters and fleet architecture](AGENT_CLI_ABSTRACTION.md). The browser uses
vendored xterm.js, with real terminal input and resize rather than a reconstruction
of a model conversation. Interrupt sends Ctrl+C to the terminal foreground group;
Stop terminates observed processes in its OS session and closes the PTY.
The terminal takes keyboard focus when its visible view connects. Click it to return focus and
type directly, including Ctrl+C and Ctrl+D. The compact **Keys** menu provides
arrow, Tab, Esc, Enter, Ctrl+C, Ctrl+D, Ctrl+Z, and Ctrl+L shortcuts when needed.
Type and paste directly into the terminal; there is no separate session text box.
Stop session terminates a running session. After a process exits or fails,
Close session marks it stopped while retaining its output and exit details.

The **Session ▾** menu also provides resume, fork, and handover. Each creates a
new execution linked to its source. Native conversation IDs restore the same CLI;
portable handoff documents transfer available context to another agent. See
[session continuation](SESSION_CONTINUATION.md) for behavior, limits, and APIs.

Sessions use the operator's environment, home/configuration, and credentials. They
modify the selected directory directly, and multiple sessions can share it. This
is trusted local execution, not the confined batch provider or Nucleus. Shell
operations can bypass coordination; escaped descendants cannot be guaranteed to
stop through process sampling. No unattended grant or automatic decision approval
is implied. The existing batch provider continues to use private workspace copies.

One active supervisor (API role `coordinator`) is allowed per project. Its initial
prompt includes the saved project profile and explicit Mitos delegation commands.
Workers record their supervisor ID and assignments; the overview shows clickable
connections separately from terminal navigation. Profiles select a shared checkout
or separate Git worktrees for new workers. See [project supervision](PROJECT_SUPERVISION.md)
for attention delivery, breakpoint semantics, project setup and APIs.

Input requests are serialized, with a bounded in-memory duplicate-key window.
The browser does not automatically retry ambiguous input. Output streams over a WebSocket using byte offsets and base64 to preserve terminal
bytes. The browser acknowledges rendered frames and reconnects from its last
rendered offset. See [transport and renderer evaluation](TERMINAL_RENDERER_EVALUATION.md). It is saved separately
from metadata, capped at 16 MiB per session/job; excess output stops execution.
Leaving the page does not stop the process. Draft board replies remain in browser session storage. Terminal output can include sensitive CLI output and is
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
- `MITOS_GOAL_ID`, `MITOS_TASK_ID`, `MITOS_REQUESTED_BY`, `MITOS_HANDOFF_TOPIC_ID`: optional fleet assignment references.
- `MITOS_COORDINATION`: a local Markdown file describing the tools and role.
- `MITOS_SUPERVISOR_ID`: recorded supervisor at launch.
- `MITOS_WORKSPACE`: actual checkout, including an agent worktree when configured.
- `TMPDIR`, `MITOS_SCRATCH`: a private session scratch directory.
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
metadata, and a side-by-side Changes view with syntax colors, added/deleted lines,
intraline changes, and old/new line numbers. Renames, binary changes and missing
final newlines are labeled; output exceeding the 128 KiB capture limit is marked
partial. Untracked files appear in file status, outside the HEAD comparison.
The resource view also shows
sampled process identity, RSS, cumulative CPU time (with raw ticks retained in the API), handle counts,
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
| `GET /api/agents` | Discover adapters and installation metadata; optional `project` |
| `GET /api/agents/{id}` | Inspect selected `profile` and `project`, without credential values |
| `POST /api/agents/refresh` | Refresh executable/version discovery |
| `GET/POST /api/projects/{id}/sessions` | List or launch `{agent_id,profile_id,prompt,role,assignment}` |
| `GET /api/sessions/{id}` | Session lifecycle and command |
| `WS /api/sessions/{id}/stream?offset=N` | Push `{data,next,status}`; acknowledge rendered frames with `{next}` |
| `GET /api/sessions/{id}/output?offset=N` | Up to 64 KiB of terminal bytes and next offset |
| `POST /api/sessions/{id}/prompt` | `{key,text,bracketed}` submits a prompt and establishes the first stable name |
| `POST /api/sessions/{id}/input` | `{key,data}` to the existing PTY |
| `POST /api/sessions/{id}/resize` | `{rows,cols}` |
| `POST /api/sessions/{id}/stop` | Stop live execution |
| `POST /api/sessions/{id}/{resume,fork,handover}` | Continue with native conversation or portable context; required request `key` |
| `POST /api/sessions/{id}/native-session` | Link an exact `native_session_id` |
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
