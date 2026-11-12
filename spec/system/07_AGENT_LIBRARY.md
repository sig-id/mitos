# 07 – Agent definitions and the library

Status: normative design, `agent-definition/v1`; local Codex workflow implemented as a subset.
The bundled [default library](../../agents/README.md) now runs through the native
Codex adapter, with automatic planning, graph-scoped specialization, and editable
worker revisions. This does not claim full profile conformance: multi-runtime
negotiation, knowledge/skill attachment, deprecation, and promotion remain future
work. See [implemented workflow](../../docs/adaptive-runtime.md#automatic-workflow).

## Purpose and ownership

The normal journey is a goal followed by automatic planning and execution. Users
MUST NOT need to author steps, choose application IDs, or configure a group and
completion criteria for every worker. The controller selects suitable definitions,
specializes them, or creates a task-specific definition when none fits. The graph
shows the resulting work and the agents assigned to it.

An agent definition captures expertise, instructions, knowledge needs, skills,
context policy, and execution requirements. It MUST NOT embed a fixed task graph,
required engineering phase, Git lifecycle, or framework response grammar. The same
agent can participate in different plans, and a plan can use multiple instances of
one definition. A specialist name is a description, not evidence of expertise.

| Object | Meaning and owner |
|---|---|
| Agent definition | A reusable, versioned configuration record in a library scope. No process or conversation is implied. |
| Specialization | An immutable task-specific derivative, with base revision, authored changes and provenance. |
| Agent binding | The Task's reference to a definition/specialization and its assigned role/context. Not another task scheduler or task outcome. |
| Runtime installation | An available harness and its registered adapter, model/account configuration and supported capabilities. |
| Attempt / Run | One admitted execution using a frozen binding and a resolved runtime. |
| Application persona | An application-owned identity, such as an Acegoat personal agent. It may reference definitions and memory without becoming a Mitos process. |

Definitions are configuration records; they do not add a kernel primitive. Task,
Attempt, Process and Session retain their existing identities. The library owns
versions and discovery; the controller owns selection/proposed specialization;
the kernel owns admission; the scheduler owns dispatch; adapters own native process
and observation handling. Application domain identities remain application-owned.

## Definition contract

The canonical authored package is `agent.json` plus UTF-8 `instructions.md`.
[`agents/definition.schema.json`](../../agents/definition.schema.json) defines the
v1 authoring shape. JSON supplies typed metadata; Markdown supplies ordinary role
instructions. There is no shell evaluation, templating language, environment-variable
expansion, or required XML prompt section. Importers MAY translate other authoring
formats into this shape before validation and registration.

| Field | Meaning |
|---|---|
| `profile`, `id`, `revision`, `name`, `description` | Schema, namespaced identity, positive revision, and user-facing purpose. |
| `kind` | `worker` or `planner`; identifies intended use, never grants control authority. |
| `match` | `use_for` and `avoid_for` hints used to rank eligible candidates. These are advisory, not hard permissions. |
| `instructions` | Package-relative text path and SHA-256 of its exact bytes. |
| `runtime` | Required adapter features and ordered runtime preferences; no executable paths or credential values. |
| `skills`, `knowledge` | Revision-pinned resource references, with required/optional status. Empty means no packaged dependency. |
| `context` | Required and optional input kinds, finite character budget, and overflow policy. |
| `memory` | `attempt` or `assignment` scope; whether a worker may propose knowledge publication. Neither enables implicit global writes. |
| `capabilities` | Required and optional capability names requested for the role. Actual grants come from admission policy. |
| `limits` | Finite proposed wall-time and turn ceilings, intersected with responsibility allowance and provider support. |
| `specialization` | Fields a derivative may change, immutable requirements it must retain, and maximum added instruction bytes. |
| `evidence` | Guidance for useful results. Not a mandatory reply format or an acceptance predicate. |
| `provenance` | Authorship and source references. Publication receipts establish who actually registered it. |

A revision is immutable after registration. Registering different content at an
existing identity/revision MUST conflict. The content digest MUST cover canonical
manifest content and exact referenced instructions, skill and knowledge digests;
publication assigns no mutable meaning to a revision. Canonicalization and its test
vectors MUST be published with the registration wire profile before claiming
interoperability; instruction byte hashes already have exact SHA-256 semantics. Authoring schema validation
is followed by semantic validation of references, compatibility, scope and policy.
Unknown schema versions/fields, duplicate keys, missing resources, digest mismatches,
path escapes and symlink escapes MUST fail registration. Package instructions are
bounded to 64 KiB; each manifest to 64 KiB. Interfaces MUST declare aggregate library,
query, context and generated-definition bounds as well.

Runtime preferences are advisory registered identifiers; an empty list allows any
eligible runtime. Missing a required adapter feature blocks that binding; missing a
preference alone does not. Source access can be optional, allowing structured tasks
without a repository. Hard resource/workspace prerequisites belong to the admitted
Task, not a universally required `workspace`/`prd.md` template path.

## Library identity, scope and lifecycle

A library entry is addressed by home, application/tenant scope where applicable,
visibility scope, definition ID, revision and content digest. Supported visibility
scopes are installation, project and graph. Search MUST respect those boundaries;
colliding short names MUST NOT silently shadow another definition. Operators may
configure explicit project defaults over installation defaults. The exact resolved
reference is recorded before admission, never looked up again as an unpinned name.

The default library ships general-purpose roles, with a planner and a generalist as
bootstrap choices. Importing files from a repository is an explicit registration
operation under project policy; finding a prompt file must not install a tool or
change authority. A directory of definitions and a configured native runtime are
separate setup concerns. Existing `agents.json` remains harness/profile discovery
configuration; it is not the new definition library.

Publication supports draft validation, immutable version registration, deprecation
and retirement. Deprecation affects new selection; retirement refuses new admission
under the effective policy. Existing attempts retain their frozen configuration;
revocation/cancellation follows runtime custody rules. Audit records remain readable.
Deleting a visible entry MUST NOT delete definitions retained by prior attempts.

Generated definitions default to graph scope and consume a finite generation quota.
They remain available for the graph's history and recovery. Publication for reuse in
a project or installation is a separate authorized library operation, not an
implicit side effect of completing a task. Policy may authorize automatic promotion,
but it must preserve provenance and validation; normal task execution must not ask
the user to approve every temporary specialization.

## Selection and creation on demand

1. Admit the user's goal and effective planning policy, preserving the original
   request and criteria. Bootstrap planning from an authorized planner definition
   and compatible installed runtime; never require a planner to invent itself.
2. Retrieve a bounded set of definitions using purpose, requirements and permitted
   sources. Deterministic filtering excludes inaccessible revisions, unavailable
   capabilities and incompatible runtimes. Ranking may use a model or code.
3. Reuse a suitable definition with assignment-specific context. Specialize it when
   task-specific expertise, knowledge selection or instructions improve the fit.
4. If no role fits, derive a new definition from an eligible generalist or another
   authorized base. Describe the new expertise and its supporting sources. Creation
   consumes planning/generation allowance and retains the producing attempt, source
   facts, base digest and reason. It cannot assert new verified knowledge by naming it.
5. Atomically admit the generated definition, agent binding and associated tasks in
   one graph amendment, or reject all of them. Definition references in that amendment
   may be proposal-local; only the commit receipt publishes stable references.
6. The scheduler creates fresh attempts for eligible tasks. Idle pre-existing agent
   processes are unnecessary. Busy capacity queues work; creating more role names
   cannot create execution slots or reset the graph's limits.
7. Revisit the plan from settled evidence or a meaningful change. Specializations
   never mutate an admitted attempt; new work or replacement retains old evidence.

For generated definitions, `provenance.origin` is `generated`; `base` binds its
scope-relative ID, revision and digest; `generation` binds the graph, producing
Task/Attempt and rationale. The owning home/project/tenant comes from authenticated
transaction context, never an unverified name. Server receipts validate these
references; self-authored provenance is not proof. The specialization record retains
the base-to-effective change set and generated instructions' digest. Generated
instruction bytes enter as bounded immutable content, not an arbitrary host path or
executable package installation. The same canonical definition can later be exported
in the authored package format.

Selection records MUST identify candidates actually considered, the chosen revision,
compatibility checks, rationale, generated changes and the applied graph receipt.
Optional typed decisions select/evaluate supplied candidates; they do not generate
arbitrary definitions. Candidate generation and evaluation remain separate operations
with independent accounting and the existing inference recovery rules.

| Condition | Required behavior |
|---|---|
| Suitable definition exists | Reuse or specialize it and create an instance for this assignment. |
| No suitable specialist exists | Generate a graph-scoped definition from an authorized base. |
| No idle instance exists | Create a new execution binding; queue if capacity is busy. |
| No compatible runtime is installed | Wait with a concrete setup reason. Generating a definition cannot install a harness or model. |
| Authentication fails or is unknown | Report the actual attempt/setup state; retain evidence and bound retries. Do not claim credentials were verified merely by discovery. |
| Required knowledge/tool is unavailable | Use an eligible alternative, request the missing prerequisite, or wait. Do not fabricate access. |
| Definition generation fails validation | Reject it and permit bounded correction under the same allowance; never silently fall back to a different grant. |

The product may show Planning only while planning is queued, admitted or running,
with the distinction inspectable. A bare goal with no planner is waiting for setup,
not active planning. Existing manually created goals need an explicit, idempotent
"Start automatic work" operation that binds the current policy and goal; merely
viewing or upgrading an old graph MUST NOT launch previously dormant work.

## Specialization, context and authority

A specialization binds one exact base; v1 does not support ambiguous multiple
inheritance. Editable fields are explicitly listed. It may add role instructions,
select accessible revision-pinned knowledge/skills, prefer an eligible runtime,
select optional context and tighten limits. Required capabilities, adapter features
and required context remain required.
A derivative may add required adapter features, promote an optional capability or
context input to required, and narrow optional requests. It cannot enlarge the
base capability ceiling. Changing kind, removing hard requirements or enlarging
allowed specialization fields requires a separately authorized base definition.
A newly invented specialist may start from
the generalist's broader optional ceiling, still intersected with current grants.

Effective capability authority is the intersection of definition requests,
project/application grants, controller scope, task restrictions, provider enforcement
and remaining allowance. Instructions, skill bodies, knowledge and claimed expertise
are not authority. Executable skills/tool adapters require independent registration;
a generated instruction cannot smuggle in an executable or credentials. Defaults
are not hard confinement. Any unenforceable requirement must be surfaced or rejected
according to declared policy, never advertised as enforced.

Context assembly binds the assignment, constraints, selected source revisions,
artifact witnesses, relevant prior findings, granted tools, memory and budget.
Include source kind, identity, digest, access scope and truncation/summarization
provenance. Required inputs cannot be silently dropped to fit the budget. Optional
inputs may be omitted or summarized according to policy; summaries cite the source
and record the interpreting attempt. Instructions are separated from quoted/source
content. A retrieval result cannot become an instruction or authorize another lookup.

Library definitions contain reusable knowledge references, not live credentials,
user-private transcripts, mutable workspace contents or personal memory snapshots.
Assignment memory is a scoped reference with explicit read/write ownership, revision
and retention policy. A replacement can recover authorized memory; unrelated tasks
cannot inherit it merely because they use the same agent definition. Publication of
worker findings creates a candidate knowledge record, not established shared truth.

## Binding and results

Before launch, the Task execution specification MUST carry the immutable definition
and specialization reference. The Attempt MUST freeze:

- Definition/base revisions and digests, effective instructions and skill versions.
- Assignment/criteria revision, selected knowledge and input witnesses, context
  assembly receipt, and memory binding.
- Resolved adapter/harness/model/profile identifiers and enforcement capabilities.
- Effective authority, resource claims, limits and remaining responsibility budget.
- Selection/generation receipts and creator/controller generation.

Secret values never enter these records. A template update cannot change an admitted
worker. Replacing a harness creates a new Attempt; retaining an application persona
or agent binding does not authorize resuming an unresolved process.

Workers return native prose, files, code changes or adapter-native events. They do
not emit PASS/FAIL/FIX, a universal JSON reply, or graph commands. A planner can use
a structured proposal tool because proposing is its assignment; this must not become
the ordinary worker interface. Only the kernel commits proposals. Findings and
completion claims remain distinct from checks and accepted results. Neither the
reviewer role nor the planner role can self-grant acceptance authority.

Agent instances are represented on the graph through their Tasks and Attempts.
The library is the source of reusable definitions, not a second list of "running"
processes. Users inspect an agent's role, assignment, runtime, knowledge sources,
origin (default or generated), state and results. Manual graph editing and application
executor forms are not required features of the normal goal workflow. Advanced
application clients may still submit deterministic work via existing contracts.

## Interface and delivery requirements

The agent-library profile MUST provide bounded discovery, exact revision reads,
draft validation, publication/deprecation, specialization preview, and binding
inspection. Mutations use authenticated scope, expected revision, idempotency key
and canonical digest, returning durable receipts. Generated definitions and new work
use graph transaction semantics; promotion is a separate publication operation.
New graph fields/operations require negotiated schema support. Current graph APIs
MUST continue rejecting unknown operations rather than pretending v1 is implemented.

Delivery order is definition validation/import, compatible runtime resolution,
frozen binding/context assembly, automatic goal planning, then generated definitions
and library promotion. No shipped-library badge may imply that any of these runtime
integrations already work. Conformance belongs to G4/G5 and additionally requires:

- A fresh installation with a configured supported runtime accepts a free-text goal
  and runs discovered work without manual steps, example applications or reply tags.
- A novel task with no exact library match produces a graph-scoped specialist and
  an actual admitted Attempt; its origin and selected knowledge are inspectable.
- No-runtime, auth failure, capacity wait and missing-input cases are distinguishable.
- Rejected/stale/duplicate proposals cannot leave orphan definitions, duplicate workers
  or reset allowance; restart preserves exact bindings and requires no model replay.
- Updates, retirement and cross-scope name collisions cannot retarget existing work.
- Creating a specialist cannot expand access, remove required checks, replace the
  goal, promote unreviewed knowledge, or bypass settlement and final acceptance.

For example, "audit spec/impl gap" may select a researcher, specialize a reviewer as
"runtime contract auditor", and add further investigation only where evidence calls
for it. The planner may finish with a report; it must not create implementation,
committing or merging steps solely because an engineering template exists.
