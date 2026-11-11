# ADR 0002: Batch console as the initial Studio surface

Status: superseded as primary product/navigation by [Workspace](../product/WORKSPACE.md)
and [ADR 0004](0004_DURABLE_AGENT_OS.md), 2026-11-11.

## Decision and consequences

The initial local slice prioritized bounded command runs, evidence, and explicit
operator decisions. That made batch execution inspectable before hosted sessions
existed. Project workspaces now supply the primary interaction contract; batch
reports and acceptance evidence remain useful views. Historical requirements below
do not prohibit chat, supervisor graphs, or remote interfaces.

## Historical rationale

Section numbers and product claims in this historical excerpt refer to the earlier
specification revision; use [the current catalog](../README.md) for normative requirements.

## Product decision

The first product is a place to hand off bounded work and come back to
evidence. Its customer is the operator in `02_PRODUCT §3`, and its eventual
acceptance gate is the overnight run in `02 §6.1`. The user should spend less
time supervising, not get better at watching agent sessions.

Studio precedes Market because verifiable unattended execution is their shared
hard dependency. There is no storefront, mock revenue dashboard, role picker,
chat composer, or decorative fleet graph in this build. Market remains in the
target contracts; it is not represented as an available service.

## Information architecture

| Place | User question | Primary action |
|---|---|---|
| Overview | Is anything waiting on me, and what moved forward? | Resolve a decision or start work |
| Work | What did I ask for, and what happened? | Open an attempt, inspect evidence, retry |
| Decisions | What choice unblocks this? | Choose a consequence |
| Applications | What can I put to work? | Launch an installed entrypoint |

Work is the human-facing container of intent. Internally it retains the
spec's Task → 0..N Runs distinction: a retry is another attempt, not another
unrelated job. The UI lists attempts individually and links predecessors.
Pool disciplines, dependency graphs, and service supervisors remain runtime
breadth rather than top-level navigation in this first slice.

## Journeys

**Start:** choose an application → name the outcome → provide the brief and
workspace → set a time limit → start. The form shows choices the runtime can
enforce. It does not offer imaginary models, budget metering, integrations, or
autonomy modes. Installed applications declare one entrypoint in the local
manifest dialect; richer application manifests remain a target.

**Leave:** the durable record and execution survive navigating away. There is
no dependency on a browser connection. The overview puts decisions before
recent work; work remains searchable by title, app, or workspace.

**Decide:** the same card appears on desktop, phone, and run detail. Context,
consequences, and cost of a wrong choice precede the buttons. The implemented
external recovery card has no default or countdown. Restart in a new workspace
or close the work. Phone notifications are not implemented; a responsive local
page does not imply remote access.

**Return:** the report starts with the result and acceptance status, then offers
downloadable evidence. The execution record is one link deeper. Failures name
the next useful inspection. Rerunning preserves the brief, source workspace,
time limit, and task identity without re-entering the order.

## Visual language

Warm paper, white work surfaces, dark green actions, olive details, and amber
decisions. Color carries state alongside text and symbols. Overview metrics
are counts and elapsed time from recorded runs; no unmeasured autonomy
percentages, savings, costs, or artificial activity charts. Example history
is opt-in and labelled.

The sidebar has four destinations. Content uses generous page spacing, compact
work rows, and right-hand context panels. Phone layouts use a sticky navigation
row, stacked cards, and large decision targets. Reports print independently.
The outcome is the main visual unit; command output is a downloadable artifact.

## Interaction implementation

Go `html/template` owns rendering and escaping. HTMX enhances links and forms.
Search replaces a result fragment; active runs poll every three seconds.
Navigation returns a complete HTML document, and history restores through the
server. Validation preserves inputs, submissions disable their buttons, and
network failures show a message. Reduced motion, keyboard focus, skip navigation,
and semantic labels are part of the baseline.

The report is local and for the operator. It is not the commercial proof cut
in `08`: there is no public sharing action exposing briefs, paths, or command
output. A future commercial report must pass through an explicit allowlist
before it reaches the renderer.

## Next meaningful increment

Integrate a trusted harness with structured observation, bounded checkpoints,
and an inaccessible acceptance oracle. That makes the Studio gate measurable.
Provider isolation and authority/credential brokering precede untrusted apps
and hosting. Market admission and the commercial evidence reader follow them.
