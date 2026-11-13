# Agent sessions, cooperation, decisions, and supervision

This contract covers six related delivery slices. Requirements are targets until
their delivery evidence is recorded; an instruction file or classifier is not an
enforcement boundary.

## Vocabulary and ownership

| Term | Responsibility |
| --- | --- |
| Runtime | Durable state, process ownership, scheduling, budgets, permission enforcement and stop delivery |
| Coordinator | Plans and revises assignments, integrates results, requests eligible work; planning is an activity of this role |
| Worker | Performs an assignment and submits evidence |
| Router | Selects the resolver for a particular request |
| Decision model | Classifies evidence or selects among supplied eligible choices |
| Supervisor | Independently examines actions and observations against project boundaries and can deny or stop work |
| Daemon | A long-lived service process; not a model role |

The obsolete project `/supervision` coordination endpoint and `desk supervision`
command are removed. Coordination uses `/coordination`, `desk coordination`, and
`coordinator_id`; no public compatibility aliases remain. Persisted execution
identifiers such as `mitos.planner` still identify saved historical work.
Documentation and UI MUST distinguish coordination, recovery control, and
independent boundary supervision. The coordinator MUST NOT be the
sole judge of whether its own actions comply with policy.

## 1. Explicit single-agent sessions

Projects MUST expose **Start agent session** alongside **Start coordinated work**.
The first creates one bridge-backed CLI session with an assignment and selected
runtime/profile. The second creates coordinated work. Both use the same request
contract even when executor lifecycles differ. Ordinary native terminals remain an
explicit separate compatibility path.

A session MUST retain its project, task/session identity, original request IDs,
profile, output, pending requests and resolution receipts. Launch MUST be
idempotent. Selected executables and environments MUST survive wrapping. Questions
and approvals MUST be reachable from the session and global Inbox. A reply is not
applied until the source acknowledges it. Unsupported request capabilities MUST
be visible, never silently interpreted as approval or completion.

Blocking questions, asynchronous clarifications and permission requests are
distinct. A pending clarification MAY coexist with ongoing independent work.
Only the dependent action is blocked by an unanswered request. Reply delivery MUST
check request lifetime and session/turn identity; cancellation and turn completion
invalidate obsolete replies. A CLI lacking mid-turn input MUST advertise that
limitation instead of accepting undeliverable steering.

## 2. Project autonomy preferences and delegated approvals

`AGENTS.md` supplies soft autonomy guidance. Project settings separately define
autonomy mode, eligible automated approval rules, a configured decision provider,
and fallback behavior. Effective settings and document revisions MUST be visible
and bound to execution. Editing prose MUST NOT enlarge executable permissions.

An approval rule MUST identify the supported request kind and exact operation
scope. The initial local implementation MAY use exact command and working-directory
matching; prefix or natural-language matching MUST NOT imply arbitrary authority.
Unknown effects and unsupported permission requests remain pending for an authorized
operator. Automatic decisions MUST carry their resolver, policy revision, evidence,
and reason. Live revocation MUST prevent application of a previously eligible answer.

The runtime permits automatic resolution only within delegated authority. A CLI
approval gate can therefore be answered automatically without repeatedly asking
the user, while the project boundaries continue to apply. Neither an LLM nor a
decision model can grant itself additional authority.

## 3. Portable cooperation contract

`AGENTS.md` remains guidance for every agent. An optional root `COOPERATION.md`
contains project cooperation conventions and SHOULD be linked from `AGENTS.md`.
Mitos MUST inject a bounded, immutable snapshot into coordinated assignments and
show its origin/revision in project setup. This filename is a proposed convention,
not a claim that existing third-party tools discover it automatically.

The `mitos.cooperation/v1` protocol defines structured assignments, help/delegation
requests, questions, approval requests, observations, handoffs and result submissions.
Each message binds a unique ID, task/session, causal request where applicable,
revision, kind, payload and timestamp. Requests identify blocking dependencies and
permitted resolvers. Responses reference the originating request and expire with
it. Duplicate delivery MUST NOT duplicate effects; conflicts are rejected.

Assignment scope, dependencies and completion criteria MUST be explicit. Results
reference artifacts/evidence; submission is distinct from acceptance. Shared-file
conflicts require coordination or isolated workspaces. The runtime supplies its
available capabilities and transport; prose cannot invent tools or delegate grants.
The contract SHOULD support later mapping to A2A tasks/messages/artifacts without
requiring A2A as the local transport.

## 4. Concrete routing and classification

Routing is a runtime-owned pipeline, not an implicit coordinator judgment:

1. Validate the native/structured request and bind its identity and current policy.
2. Apply deterministic exclusions and explicit rules. Known permission violations
   go to supervision; missing authority goes to an authorized operator. An exact
   delegated approval rule may answer directly without inference.
3. Where the route is ambiguous and automated handling is enabled, call the
   configured Decisions API Choice adapter as a **classification model**. Supply
   bounded evidence and routes `decision`, `investigate`, `human`; unavailable or
   unauthorized routes are excluded. There is no recursive classifier selection.
4. `decision` selects from a frozen set of eligible answers; `investigate` asks a
   general LLM agent to gather evidence or generate alternatives; `human` creates
   or retains an Inbox request. Free-form answers require investigation or a human.
5. Revalidate policy, request lifetime and evidence before applying an answer.

The classifier and answer selector are separate logical decisions even if they
use the same provider. Requests with known routes skip classification. Investigations
have bounded attempts/deadlines and MUST NOT loop indefinitely through classification.
Unavailable providers, refusal, low confidence and exhausted allowances use the
configured permitted fallback. Model confidence never overrides a hard boundary.
Every route records whether it came from a rule, classification or fallback.

## 5. Shared decision service

The existing OpenAI Decisions and System One Choice adapters MUST be reused.
Batch segment/recovery decisions, graph candidate selection, session routing and
automated resolution share provider validation and outcome semantics. Each call
MUST reserve its allowance and persist intent before dispatch, and persist its
outcome before an effect. Unknown consumption after interruption MUST NOT be
silently resent. Classification and answer selection both count against budgets.

The provider sees only bounded disclosed evidence, eligible choices and criteria.
It cannot return executable commands outside the recorded choices. Credential
references remain operator configuration. Automated workers MUST use the same
question/approval service as explicit sessions; headless attempts MUST not fabricate
answers or treat an unanswered question as successful completion.

## 6. Independent boundary supervision

Supervision is independent of task planning, request routing and result acceptance.
It watches the coordinator and workers. Inputs include trusted proposed actions,
native tool requests, execution events, policy revisions, resource consumption and
agent observations. Self-reports are evidence, not proof of compliance.

Deterministic checks enforce explicit boundaries synchronously before an effect
where the adapter can intercept it. A separately configured supervision model
examines contextual deviations and returns `allow`, `hold`, or `stop` with evidence.
Its decision cannot expand permissions or accept task completion. Requests to the
supervision model use the same bounded, durable decision infrastructure but a
separate role, policy and budget from the coordinator/router.

On a violation, the runtime MUST durably record the incident, deny the pending
action, hold new admissions for the affected scope, and request cancellation of
active work. It MUST retain evidence and report whether process termination was
confirmed. Model uncertainty can hold work for operator review; timeout/failure
MUST NOT silently permit an action requiring supervision. Resume requires an
explicit operator resolution and revalidation, not a coordinator restart.

An observation-only adapter cannot guarantee prevention of an action already
executed. Same-user processes can have effects outside intercepted requests.
Capabilities MUST distinguish preventive checks, observation and process stop;
the UI MUST NOT describe monitoring alone as sandbox confinement. Strong hostile
agent isolation requires an enforcing execution substrate.

## Delivery evidence

The six implementation issues/PRs MUST separately demonstrate: session launch and
reply delivery; preference/grant separation; cooperation snapshot/protocol behavior;
classification routing and fallback; real decision-service integration; and
independent incident/hold/stop behavior. Tests MUST cover stale replies, conflicting
resolutions, revocation, model failure, replay, and supervisor stop races where those
behaviors are introduced. Unsupported capabilities remain explicit.

Initial delivery is tracked in issues [#2](https://github.com/sig-id/mitos/issues/2),
[#3](https://github.com/sig-id/mitos/issues/3), [#4](https://github.com/sig-id/mitos/issues/4),
[#5](https://github.com/sig-id/mitos/issues/5), [#6](https://github.com/sig-id/mitos/issues/6)
and [#7](https://github.com/sig-id/mitos/issues/7), respectively. The implementation
uses project-scoped incidents and holds. The first supervisor reviews intercepted
requests and Codex command/file/tool events, not every host effect; see the
[operational coverage](../../docs/independent-supervision.md). Physical confinement,
continuous host auditing and a separate general-LLM supervisor backend remain outside
this initial implementation. The supervisor model currently uses the bounded
Decisions API Choice adapter.
