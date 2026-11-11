# Resume, fork, and handover

Status: Normative local behavior and API contract. See [specification authority](../README.md)
for precedence and [implementation evidence](IMPLEMENTATION_STATUS.md)
for current coverage. Examples and dated validation are informative.

Open the **Session ▾** menu in a session's header. It contains resume, fork,
handover, interrupt, and stop. Terminal input stays in the terminal.

| Action | Source | Result |
|---|---|---|
| Resume | Must have exited, failed, or stopped | A new execution continues the work |
| Fork | May keep running | A new execution branches from the source |
| Hand over | Stops after the replacement process launches | A selected agent receives portable context using its own environment profile |

Each action creates a new Mitos session ID and a link back to its source. Previous
output and exit details remain available. Goal/task assignment references are
preserved, and `requested_by` identifies the source. A coordinator's fork is a
worker; handover retains the coordinator role. Native CLI subagents are separate
from these Mitos executions.

These actions share the existing project directory. A fork does not copy files,
create a Git worktree, or isolate edits. Native conversation forks branch the
saved conversation at the point the CLI reads it.

## Native conversation or portable context

**Native conversation** invokes the same agent and environment profile using an
exact native conversation ID. Mitos never selects the most recent conversation
or guesses an ID from terminal text. The native CLI must still have the saved
conversation; Mitos does not verify its storage, completed initialization, or
successful model-level restoration merely because a process started.

Current built-in mappings:

| Agent | Resume | Fork | Identity for new sessions |
|---|---|---|---|
| Codex | `codex resume ID` | `codex fork ID` | Enter or link the exact UUID |
| Claude Code | `claude --resume ID` | `claude --resume ID --fork-session` | Assigned with `--session-id UUID` |
| OpenCode | `opencode --session ID` | `opencode --session ID --fork` | Enter or link the exact `ses_…` ID |
| Pi | `pi --session ID` | `pi --fork ID` | Assigned with `--session-id UUID` |

Mappings were checked against the installed CLIs' help on 2026-09-20, including
`codex resume --help`, `codex fork --help`, and `pi --help`. See the
[Claude CLI reference](https://code.claude.com/docs/en/cli-reference),
[OpenCode CLI reference](https://opencode.ai/docs/cli/), and
[Pi CLI documentation](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/README.md).
There are no older-version flag fallbacks. Native forks choose their own new
conversation IDs; link the new ID before a subsequent native continuation.

**Portable handoff** starts a fresh conversation with a prompt directing the target
agent to a private handover document. It contains:

- The recorded initial/API naming prompt and the next direction.
- Supplied handoff notes and a selected board topic's posts, authors, and revision.
- Up to 32 project board summaries, plus instructions to discover more.
- A frozen copy of the source's retained PTY output and a cleaned 16 KiB tail.
- Source identity, workspace, and a reference to any preceding handover document.

The package is a controller reconstruction of available evidence. It does not
claim to be an agent-authored summary or the full native model conversation.
Ask the source agent to write a concise handoff topic containing the objective,
current state, decisions, verification, blockers, and next steps, then select that
topic. Mitos snapshots its content rather than passing only a mutable reference.
Oversized handoff topics are rejected rather than silently truncated.

Raw terminal typing is not independently captured as a chat transcript. The
recording may include prompt echoes, redraws, and sensitive terminal output.
Native configuration, credentials, skills, and memory files are not copied or
translated. Target profiles use the target agent's current native setup.
Applications without an initial-prompt mapping cannot receive a portable handoff.

Handover snapshots the source while it may still be running. After the target
process starts, Mitos stops the source. A validation or process-launch failure
leaves the source running. Process launch does not prove the target authenticated,
read the document, or accepted the work; inspect its terminal. Interrupted source
processes must be inspected and settled before replacement.

## API and fleet tools

`POST /api/sessions/{source}/{resume|fork|handover}` accepts:

```json
{
  "key": "handover-checkout-1",
  "mode": "context",
  "agent_id": "claude",
  "profile_id": "work",
  "prompt": "Finish the checkout tests",
  "notes": "Preserve the public API",
  "handoff_topic_id": "topic_ID"
}
```

`key` is required. Repeating the same key and body for a source returns the same
execution, including after a restart. Reusing it for a different action or body
returns 409. A persisted failed launch also retains its key; use a new key to
explicitly attempt another launch. This deduplication covers continuation
requests, not general launches or arbitrary PTY writes.

For native mode, use `native_session_id` when the source has no recorded ID.
Resume and fork preserve the source agent/profile. Handover chooses a target and
always uses context mode. Omitting `mode` selects native when a source ID is
recorded, otherwise context; handover always defaults to context. Native mode
accepts an optional prompt; notes and a handoff topic belong to context mode.

`POST /api/sessions/{id}/native-session` with `{"native_session_id":"UUID"}`
links an exact ID. An existing ID cannot be rebound to a different conversation.
This validates the ID shape and prevents another live Mitos execution from owning
that ID in the same agent/profile; it does not inspect native conversation storage
or detect writers outside Mitos.

```sh
mitos desk link-session --native-session UUID session_SOURCE
mitos desk resume-session --key resume-1 --mode native session_SOURCE
mitos desk fork-session --key fork-1 --mode context --prompt 'Explore another approach' session_SOURCE
mitos desk handover-session --key handover-1 --agent claude --profile work --handoff-topic topic_ID --notes-file handoff.md session_SOURCE
```

Flags precede the source ID. Prompts and notes are each limited to 32 KiB.
Continuations expose `MITOS_PARENT_SESSION_ID` and, for portable context,
`MITOS_HANDOVER`. Packages live beside session metadata in
`.mitos/desk/session_ID.context/` with private directory/file permissions. They
remain available after scratch cleanup and server restart.
