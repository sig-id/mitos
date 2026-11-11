# 13 – Applications and integrations

## Application contract

An Application packages an entrypoint, execution specification, input contract,
required capabilities, completion criteria, and optional service definitions.
It MAY invoke an agent or an ordinary program. Applications MUST use the common
runtime for admission, accounting, observation, decisions, and acceptance.

A packaged Capability supplies data, skills, tools, or hooks. Installing it MUST
NOT grant requested authority. Dependencies and required provider capabilities
MUST be validated before execution. Unsupported strict requirements MUST be
refused rather than silently replaced with the local development posture.

Application and policy revisions MUST be frozen for an admitted task. Updating
an installation MUST NOT change the verifier, workspace policy, or requested
authority of work already running. Installation contents are executable input and
MUST be treated according to the provider's trust boundary.

## Local applications

The current local format is `app.json` with identity, description, entrypoint,
command argv, and optional acceptance, compute, observation, and supervision
configuration. [Runnable examples](../../examples/local-app/app.json) are the
starting point for concrete fields; compatibility is governed by
[API and compatibility](14_API_AND_COMPATIBILITY.md).

The runner receives its run identity, brief, and execution workspace through the
existing JSON stdin contract. Batch commands use isolated HOME/TMPDIR, controlled
Git configuration, and no inherited provider credentials. Combined output is
bounded to 2 MiB per command. These measures do not restrict same-user host access.

The provider MUST preserve output and partial changes after failure. Verification
MUST use the saved application snapshot and independently reconstructed result.
Applications without explicit strict compute requirements receive the recorded
trusted local posture; they MUST NOT acquire an unattended grant by omission.

## Connectors and sinks

A connector brings external input into the runtime. A sink delivers results,
notifications, or external effects. Both MUST identify their configured scope,
authentication, payload limits, retry behavior, and operation receipts.

Incoming data MUST NOT override grants, application selection, workspace location,
or execution policy unless a separately authorized operation explicitly permits
that mutation. Outgoing effects MUST be bound to the admitted action and its budget.

Retries MUST preserve operation identity. If an external system provides no
idempotency or query capability, an ambiguous result MUST remain unresolved until
reconciliation. Successful transport does not prove successful business action.
See [durability and recovery](04_DURABILITY_AND_RECOVERY.md).

## Integration boundaries

Agent harnesses integrate through [adapters](06_AGENTS_AND_SUPERVISION.md), remote
workers through the [process-host contract](02_SERVER_AND_CLIENTS.md), and human
interfaces through the [client API](14_API_AND_COMPATIBILITY.md). Integrations MUST
advertise the capabilities they implement rather than inherit unrelated claims.

Service ingress uses the bounded, idempotent occurrence contract in
[services and events](08_SERVICES_AND_EVENTS.md). Optional client packaging and
hardware provisioning remain implementation concerns; they do not add another
execution authority. Marketplaces, commercial settlement, and replicated hosting
require separate product decisions and are outside the initial application contract.
