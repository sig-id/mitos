# Handheld A2UI surfaces and shortcuts

Status: proposed backend work, 2026-09-22. Not implemented by the current device
API. The current esp-notify firmware consumes the strict v1 decision snapshot;
its native Options panel and context disclosure work without this extension.
This proposal extends [the handheld connector](17_HANDHELD_DECISION_CONNECTOR.md).

## Product behavior

On a 368 x 448 handheld, the question and offered answers must be accessible
without scrolling through pages of agent context. Keep Options, context
disclosure, microphone, and Review in predictable positions. A shortcut fills a
local draft; it never silently approves an action. Show the selected answer and
its consequence in an automatically opened review, followed by one explicit
tap on Send. Do not require an extra Review tap or a confirmation hold.

Examples:

| Source request | Handheld presentation | Shortcut effect |
| --- | --- | --- |
| Choose one offered answer | Single-select options with full consequences | Select its canonical option ID |
| Approve one tool execution | Approve once / Reject, as actually offered | Stage that choice for review |
| Name a workspace | Voice draft plus explicit save option | Stage the server-validated text suggestion, if supplied |
| Request with lengthy evidence | Three-line context preview and disclosure | Expand/fold locally, preserving the complete text |
| Running or completed agent | Status and concise progress | Open the associated authorized request/run |

Only surface actions the source adapter and canonical decision actually offer.
Do not manufacture retry, skip, abort, approve-all, or blanket permission options.
More than the current 2-3 choices, per-option input requirements, optional text,
and grouped questions require a new negotiated profile; never squeeze them into
v1 by dropping choices or changing their semantics.

## Protocol and ownership

Use a pinned A2UI v0.9.1 schema and an explicitly negotiated custom catalog
`urn:mitos:a2ui:handheld:1`. A2UI separates component descriptions from data and
supports custom catalogs and client-side input binding. See the
[official protocol](https://github.com/a2ui-project/a2ui/blob/main/specification/v0_9_1/docs/a2ui_protocol.md) and
[actions reference](https://a2ui.org/concepts/actions/).

Mitos owns projection, authorization, schema validation and revision binding.
The device owns font sizes, spacing, touch targets, fixed controls and the final
confirmation gesture. Agent-provided text is content, never executable layout,
an arbitrary URL, a device permission grant or a replacement for confirmation.
Raw model-generated A2UI must pass Mitos validation before publication.

First catalog:

- `Column`, `Row`, `Card`, `Text`: fixed native styling and bounded plain text.
- `ChoicePicker`: mutually exclusive canonical option IDs, bound to a local
  draft. Labels and consequences come from the same immutable decision revision.
- `Button`: a typed local shortcut or an explicitly registered server event.
- Custom `Disclosure`: full context plus a marked, clipped preview; expanded
  state belongs to the device. It must not discard evidence from the model.
- Custom `RunStatus`: canonical state plus progress text, distinct from receipt
  acceptance and application status.

Unsupported components, expressions, catalogs or required inputs fall back to
the canonical v1 surface when it can represent the complete decision; otherwise
show Open in Mitos. Do not partially render an actionable approval. No embedded
web pages, scripts, arbitrary network fetches, template execution or eval.

## Backend additions

1. Add capability discovery on the existing authenticated TLS listener. Proposed
   `GET /api/device/v2/capabilities` returns supported protocol/catalog versions,
   action names and limits. Old clients continue to use v1 unchanged: its parser
   rejects unknown fields, so do not insert A2UI fields into v1 responses.
2. Add `GET /api/device/v2/surfaces?cursor=...&catalog=...`. Each bounded page
   contains canonical decisions plus an ordered batch of A2UI create/update/data/
   delete envelopes. Bind the complete batch to a sequence, surface ID, decision
   ID, run ID and revision. Publish it atomically after validation; intermediate
   components cannot become actionable. Preserve authorization and pagination
   guarantees of v1. A missing sequence requires a complete re-fetch.
3. Retain v1 voice drafts and idempotent decision resolution for representable
   decisions. A2UI input changes update only the device draft. Only the trusted
   review/explicit-Send path creates and journals a resolution command, carrying
   the same canonical IDs, revision, selected option and validated value.
4. Add a backend projector from canonical decisions and run status to the
   catalog. Recommended options may have badges but are never preselected.
   Suggested text values need provenance, source authorization, per-option
   schema validation and a new revision when changed.
5. Add explicit action registration for any future server shortcut. Check scope,
   principal, source support, current state, revision and idempotency before
   dispatch. Read-only navigation can be immediate. Any action that changes an
   agent/run/workspace must become a canonical reviewable decision.

Local action allowlist: open options, set draft choice, set validated draft text,
expand/fold context, open review, discard draft, refresh and open a known run.
Opening the microphone requires a real touch/BOOT gesture; a remote surface
update must never begin recording. Remote data cannot write draft choice/value
over the user's edits, read credentials, change pairing, or control the journal.

Budget targets: at most 48 KiB per page, 8 decisions/runs, 64 components per
surface, nesting depth 8, 3 shortcuts in a fixed area, and one active detail
surface. Use the current v1 text limits initially. Validate unique IDs, acyclic
reachable trees, bindings and complete required fields before swapping views.
Measure native LVGL heap usage; reduce page/component limits if necessary.
Serve complete batches over bounded HTTP first; streaming is a later transport
optimization, not a prerequisite for useful shortcuts.

## Draft and disclosure continuity

Stable component IDs preserve scroll, selection, recording and fold state during
status refresh. A decision revision change invalidates its draft and any armed Send
press. A removed/resolved request closes the option panel. Offline state disables
submission; browsing cached context may remain possible. Accepted/applied states
come from durable receipts, not optimistic UI transitions.

Text-required choices must explain the missing text and offer voice input;
selecting an option must not appear to do nothing. Choice selection supports
short or deliberate longer presses, fires once on release, and is cancelled by
a scroll. Context can be folded from a fixed control even at its final line.

## Acceptance and rollout

- Validate generated envelopes against vendored upstream schemas plus the exact
  custom catalog; pin the upstream commit in the implementation change.
- Test live Codex/Claude adapters with selection, voice + choice, long context,
  unsupported inputs and source option changes. Never test with real effects.
- Reject fabricated choice IDs, stale revisions, cross-tenant surfaces,
  unknown server actions, malformed bindings, cycles and oversized batches.
- Replay refresh/reconnect without losing a local draft or duplicating effects;
  test received versus applied receipts and interrupted submission recovery.
- Keep all existing v1 device tests passing. Add capability/fallback tests with
  old firmware and renderer tests within the actual handheld memory budget.
- Implement backend projection and fixtures first; then the bounded native A2UI
  renderer. Mark support shipped only after a real board round trip succeeds.

No backend A2UI support is claimed by the current esp-notify UI changes.
