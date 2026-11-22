# ADR 0010 – Decisions API as the default workflow policy

## Decision

When the orchestrator has discretion, the Decisions API chooses among actions the runtime can carry out. The component encountering the choice supplies the goal, relevant evidence, and the consequences of each candidate. The runtime records the request before contacting the provider. A selection references that request and the policy revision under which it was made.

The provider comes from the graph's explicit decision application, the project's connection, or the system connection, in that order. Provider errors, insufficient confidence, and exhausted inference allowances go to the operator. The operator sees the evidence and the same eligible choices. Waiting consumes the original deadline. Changing the policy or replacing the owning attempt invalidates outstanding choices. An explicit stop from either resolver holds the work for review.

## Where choices take effect

| Boundary | Evidence and alternatives | Effect |
| --- | --- | --- |
| Workspace preparation | Indexed submodules, availability of local checkouts, and number of paths in each layout | Include the local dependency sources, omit and disclose them, or stop |
| Workspace capacity | Measured file and byte counts, proposed capacity | Allocate the larger bounded private copy or stop |
| Native and registered planning | Exact proposed amendment, user goal, graph revision, and local admission result | Apply an eligible proposal or hold it for review |
| Plan repair | Validation error and the number of repairs already used | Spend an available planning attempt on repair or hold |
| Worker follow-up | Submitted work, agent role, and a proposed blocking dependency | Permit the yield or refuse it and return feedback |
| Native questions | Supplied answer options, project guidance, and review delegation | Route and answer through the configured Decisions API, subject to operator authority |

The planner generates candidate assignments, dependencies, reviews, retries, and completion. A single decision covers the submitted plan as an atomic amendment. New worker tasks become eligible only after the selected amendment commits. A model review runs independently of the scheduler tick. The commit checks current graph evidence and authority again. A recorded model evaluation binds the exact inputs and choices so replay can reuse a completed answer. An interrupted call with unknown consumption requires review.

## Reasons to keep an operation deterministic

- **Explicit instructions.** An operator's selected action, a declared application procedure, a configured harness binding, a manual approval policy, and an exact permission rule already specify the outcome. Reinterpreting them could change the authority granted.
- **Eligibility and integrity.** A digest, authenticated principal, safe path, dependency witness, compatible executable, schema, graph revision, and available resource allowance can each be checked directly. The result determines which choices the runtime may offer.
- **Mechanical scheduling.** Dispatching an admitted task under the configured pool discipline, acquiring a claim, delivering an acknowledged response, and replaying a committed change have a prescribed outcome. Repeating inference here would break reproducibility.
- **Implementation capacity.** The private-copy adapter holds file contents and archives in memory. Its larger eligible allocation is bounded at 512 MiB and 32,000 files. An engine that supports bigger copies needs a storage implementation with an appropriate memory bound.
- **Authority and missing information.** A person supplies an external grant, secret, or private preference when the configured authority or evidence cannot settle the question. Silence leaves the choice unresolved.

New discretionary behavior should expose a bounded choice at its point of use. A deterministic exception should identify the instruction, invariant, or implementation constraint that fixes its outcome. Tests should exercise an alternative selection, a stale or uncertain answer, and the side effect that must remain behind admission.

## Workspace representation

Capture uses checked-out working files, including local edits and non-ignored additions. A selected submodule becomes ordinary files in the private snapshot. Its Git metadata stays in the source checkout. Changes to those files are private workspace patches. Publishing a new submodule commit and updating the parent gitlink are separate operations. An omitted dependency is listed in the workspace record and worker context.

The standard allocation is 64 MiB and 10,000 files. An approved larger allocation travels with the workspace through harvest, verification, retained archive reads, and continuation. Old workspace records use the standard allocation. Installed verification material and service condition probes retain their declared bounded-copy contract.
