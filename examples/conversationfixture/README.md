# Personal conversation and private negotiation fixture

This independent Go module owns its SQLite database and uses only the authenticated
application HTTPS API. It saves dispatch and input intents, checkpoints a private
negotiation, delivers a synthetic answer, and passes a decoded typed outcome into
a personal conversation. Candidate/company source context stays in the negotiation
request. The personal request contains only that outcome and its explicit
preference snapshot.

With an application principal, TLS authority and compatible `mitos worker`:

```sh
MITOS_APPLICATION_TOKEN=... go run . \
  --mitos https://localhost:4747 --ca-file authority.pem \
  --database conversation.db --conversation personal-one \
  --candidate-context 'private candidate data' \
  --company-context 'private company data'
```

The authority uses `MITOS_LOCAL_STRUCTURED_WORKERS=off` for the separate-worker
profile. The worker needs bubblewrap and a dedicated model API key, as described
in the [worker protocol](../../docs/worker-protocol.md). `--timeout` bounds one
application invocation; rerunning the same database reconciles its saved intents
and returns the saved reply. `--preferences-version 1` saves a preference update
and its version-CAS intent together before dispatching a new personal snapshot.
Replies are deduplicated per conversation/version, and publication also checks the
application's current preference version transactionally.

Run the root conformance test:

```sh
go test -race ./internal/application \
  -run '^TestPersonalNegotiationConfinementAndConsistency$' -count=1 -timeout=3m
```

The authority, worker and independent coordinator run in separate processes. A
deterministic native stand-in follows the real confined stdio/tool path. File
barriers in that fixture force the ordering; they are absent from application
flows and the real worker protocol. Cases cover:

* An answer committed after context preparation and before wait registration.
* Authority restart while dormant, followed by one input/wake.
* Coordinator/worker incarnation replacement while its responsibility is dormant.
* A preference advance while an old personal result is blocked, with the new
  update queued behind the old holder and the old candidate then rejected.

Every case verifies one durable negotiation outcome and personal reply. Candidate
and company canaries are checked against the actual personal prompt by the native
stand-in, every personal event/artifact, and real MQTT hint payloads. The native
process also attempts to open the coordinator database outside its mounts. Only
the schema-valid typed outcome crosses into the personal request. The gate tests
protocol consistency and confinement; production model quality is measured
separately.

The declared control-plane scale workload and its observed numbers are in the
[S4 benchmark report](../../docs/benchmarks/s4-dormant-scale.md).
