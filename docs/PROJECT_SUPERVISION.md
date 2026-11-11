# Project supervision

Open a project by confirming any existing local directory. That directory is the
project workspace; the server's `--workspace-root` supplies a default, not a
restriction on which project can be opened. Opening a new project displays its
supervisor setup. Existing project profiles remain available through **Project profile**.

1. Select **Analyze project** and choose the installed agent and environment profile.
   You may provide a work goal now or leave it for review.
2. Select **Analyze project**. That same agent runs in a fresh background conversation
   to inspect the repository and propose project context and working rules. It does
   not launch workers or become the working supervisor. Progress and the draft survive
   navigation. The overview shows live, read-only analysis output, the selected
   agent, elapsed time, and cancellation or retry controls. Failures retain their
   output here so installation, sign-in, and response errors can be inspected.
   The Activity view groups command output into collapsible branches and formats
   the completed profile into context sections and working rules. Expansion is
   preserved during refreshes; Expand all, Collapse all, and Raw log are available.
   Markdown documents and responses are rendered, source blocks are highlighted,
   and token usage is displayed as metadata. The log collapses after completion.
3. Select **Review context & set goal**. Review the proposed context and supply a
   concrete work goal. Git, push, merge, review, disk and other policies are under
   **Working rules & limits**. Select **Continue** to reach the final launch step.
   These edits survive reloads in the current browser tab; the saved project profile
   remains unchanged until launch.
4. Select **Start supervisor** from the final step.
   The reviewed profile is saved at its expected revision and a fresh supervisor
   conversation receives your edited context and goal. Retrying the request finds the
   recorded launch instead of creating a duplicate supervisor. A model-proposed work
   goal is never substituted for an absent operator goal. The supervisor's terminal
   opens after launch; the Supervision tab returns to its graph. The supervisor is
   itself a coding agent, with a separate terminal link in the sidebar.
5. The supervisor delegates with `"$MITOS_BIN" desk start-session --agent ID --prompt TASK`.
   These are real Mitos sessions with a recorded `supervisor_id`. Every working agent
   requires a nonblank goal, including launches from the CLI/API.
6. Select graph edges or assignment buttons to inspect the objective, workspace,
   parent, policy revision and attention transport. Select a node to open its terminal.
   The supervisor stays separate from agent navigation.

Background analysis supports Codex (`exec`, ephemeral, read-only sandbox), Claude
(print mode, no session persistence, Read/Glob/Grep tools) and Pi (print mode, no
session, read/grep/find/ls tools). The selected native credentials and environment
profile are preserved. No supervisor conversation is resumed or shared. Other native
adapters are explicitly marked unavailable for analysis until supported. Custom agent
adapters can declare `context_args`, an explicit noninteractive invocation that receives
`-- PROMPT` and returns a profile JSON object on stdout; its author is responsible for
read-only tool configuration. This does not add an OS confinement guarantee for custom
adapters or operator-configured native integrations.

A draft may run for up to eight minutes; at most four analyses run concurrently and
one per project. The profile response is limited to 1 MiB. Diagnostic progress uses
a rolling 128 KiB tail; verbose stderr does not cancel analysis. Completed diagnostics are stored privately under
`desk/profile-drafts/<id>/`, and interrupted analyses are never silently replayed on
restart. There is a 256-draft history bound. Malformed output, failed processes,
cancellation or stale-profile conflicts never apply an unreviewed draft. Starting the
supervisor requires an explicit review action. Ordinary shells and applications can
still open without a work goal; coding agents cannot.

The API retains `role: "coordinator"` for compatibility. One active supervisor is
allowed per project. Agents launched directly by the operator attach to the active
supervisor; already-unassigned sessions remain visibly unassigned. Supervisor
resume/handover transfers existing parent relationships to the replacement.

## Context and workspaces

Prompt-capable coding agents receive the project profile and role instructions in
their actual launch prompt. `MITOS_COORDINATION` also contains a reference copy.
The supervisor workflow is **plan → delegate → monitor → review**. New launches
are instructed to publish a plan and assign implementation to at least one Mitos
worker, even for a single task. Supervisors inspect, coordinate and independently
validate results; implementation edits require an explicit operator request to
take over. A failed worker launch must be reported rather than silently replaced
by solo implementation. These are native-agent instructions, not an enforced
scheduler or a guarantee of model compliance. Existing sessions retain their
original launch instructions.
Supervisors are instructed to stay in a supervision loop while workers run, using
`desk inbox --wait` (up to 20 seconds) between updates. This also lets hook-based
adapters wait for worker reports without an idle wakeup API.
Workers are instructed to publish evidence to the topic board, notify the supervisor
of completion/blockers, and request additional workers through it. Native CLI
subagent delegation is explicitly disallowed by the supplied instructions.
Agents without an initial-prompt adapter cannot be selected as supervisor.

Profile writes require an expected revision. Changes generate durable attention
requests directing active sessions to `desk profile`. Existing workspaces remain
in place; new launches use the revised layout. Free-form Git, path, push, merge,
review and cleanup policies guide trusted agents and are not OS access controls.
Mitos enforces the active-worker limit and disk reserve at launch.

With separate worktrees, new agents get a real Git branch `mitos/<session-id>` and
checkout under the data directory's `desk/worktrees/`. They start from committed
HEAD. The session/worktree intent is persisted before Git creates the checkout.
Creation failures remain recorded. Resume, fork and handover retain the source
checkout, including uncommitted work; **fork does not snapshot files**. Worktrees
are retained for review and are never automatically removed. Shared managed Cargo
jobs and the Changes view use the project checkout, so their results do not verify
edits in an agent's separate worktree. Run those checks in `MITOS_WORKSPACE`.

## Board, attention and breakpoints

The topic board remains discoverable, pull-based context. Urgent messages use a
separate persisted inbox:

```sh
"$MITOS_BIN" desk broadcast --body "Hold the shared index while I update it" --pause
"$MITOS_BIN" desk supervision
"$MITOS_BIN" desk inbox
"$MITOS_BIN" desk acknowledge notice_ID
"$MITOS_BIN" desk release-notice notice_ID
```

Broadcast targets active project sessions except the author. A new session joins
any active breakpoint. `pending`, `context_queued` and `acknowledged` are distinct:
queue acceptance or hook delivery does not count as an acknowledgment. Read alone
does not acknowledge. An acknowledged pause stays active until released by its
author, the active supervisor or the operator. Wait for **all** target acknowledgments
before critical work. Already-running commands may finish; a pause does not kill
processes or prove the absence of external Git operations.

| Adapter | Urgent delivery | Pause behavior |
|---|---|---|
| Pi | Invocation-scoped extension calls native `sendMessage` with steering and turn wakeup | Checks the persisted inbox before each new tool; allows inbox/ack/release control commands |
| Claude | Invocation-scoped prompt/pre-tool/post-tool hooks supply additional context | Pre-tool hook denies new tools during a pause, except inbox/ack/release control commands; idle sessions receive context at their next boundary |
| Codex | Native `codex queue` targets the exact linked conversation | Cooperative instruction, not tool interception; unsupported queue commands remain pending |
| Other/custom sessions | `desk inbox` | Cooperative reading and acknowledgment |

Codex agents are instructed to link `CODEX_THREAD_ID` through
`desk link-session --native-session "$CODEX_THREAD_ID" "$MITOS_SESSION_ID"` when
available. No most-recent-conversation guess is used. Linking delivers pending
notices. Releases are separately delivered, including a Pi wakeup. Adapter receipts
never replace an explicit agent acknowledgment. Hooks/extensions are scoped to the
launch and do not modify the operator's global CLI settings. Claude/Pi tool checks
fail closed if the local attention service is unavailable. Session exit notifies
its supervisor to inspect evidence; an exit code is not a verified task result.

Older running sessions do not gain new prompts or native adapters retroactively.
Their inbox remains available; restart or hand over to install the new launch
context. Session IDs identify participants on this trusted, local interface; they
are not authentication credentials against other processes belonging to the user.
Attention history currently has a bounded 2,048-record capacity.

## API additions

All routes use the existing local Host/origin checks and bounded JSON requests.

| Route | Contract |
|---|---|
| `POST /api/projects/{id}/profile-drafts` | `{key,agent_id,profile_id,prompt}`; begin idempotent background analysis |
| `GET /api/projects/{id}/profile-drafts/{draft}` | Persisted analysis status and proposed profile |
| `GET /api/projects/{id}/profile-drafts/{draft}/output` | Live or saved bounded output tail, with truncation indicator |
| `POST /api/projects/{id}/profile-drafts/{draft}/cancel` | Cancel analysis or discard an unstarted draft |
| `POST /api/projects/{id}/profile-drafts/{draft}/start` | Explicitly review `{profile,revision}` and start with its goal; retry-safe |
| `GET /api/projects/{id}/profile` | Effective profile with defaults |
| `POST /api/projects/{id}/profile` | `{profile, revision}`; rejects stale revision |
| `GET /api/projects/{id}/supervision` | Profile, sessions, parent relationships and attention receipts |
| `POST /api/projects/{id}/attention` | `{author, body, pause}`; default author is operator |
| `GET /api/sessions/{id}/inbox` | Unacknowledged messages, active pauses and pending releases; optional `wait=true` |
| `POST /api/sessions/{id}/inbox/{notice}/delivered` | Native delivery receipt; `{release:true}` records release delivery separately |
| `POST /api/sessions/{id}/inbox/{notice}/acknowledge` | Explicit target acknowledgment |
| `POST /api/attention/{notice}/release` | `{author}`; default operator |

## Verification

`go test -race ./...` includes profile revision/persistence, supervisor lineage,
worker limits, actual worktree creation and continuation, attention receipt/release
persistence, and native Claude/Pi pause behavior. Pi adapter tests use Node when
available. `npm run test:supervision` in `tools/terminal-eval` exercises project setup,
background repository analysis by a deterministic fixture, draft recovery after navigation,
editable review, empty-goal rejection, real CLI delegation, graph assignments, CLI acknowledgments,
release, terminal access and mobile profile editing. These tests do not make model
calls or assert that an arbitrary agent will comply with prose policy.
