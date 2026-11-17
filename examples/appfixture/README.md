# Independent application fixture

This separate Go module owns `application.db`. It imports no Mitos runtime code,
uses only the TLS HTTP application API, and owns its message/dispatch and
accepted-result/publication transactions. Reusing a message identity with changed
text is refused. A reply is one durable row keyed by message identity; stdout
reads that saved reply and is not a second external-publication guarantee.

With a configured application TLS listener and scoped token:

```sh
MITOS_APPLICATION_TOKEN=... go run . --mitos https://localhost:4747 \
  --ca-file server.pem --database application.db --message-id once --text Hello
```

The runtime needs `MITOS_AGENT_CONFINEMENT=bwrap`,
`MITOS_AGENT_NETWORK=unrestricted`, an installed Codex runtime and a dedicated
`OPENAI_API_KEY` for application execution. Local operator/native-login work keeps
its existing profile. `--timeout` bounds an invocation; restarting with the same
database/message reconciles pending intent and returns the saved reply.

The root test `go test -race ./internal/application -run
TestIndependentApplicationCrashMatrix` builds both subprocesses, injects real
application process death, kills/restarts the runtime, and verifies one logical
Task and one reply. Its deterministic model stand-in runs through the actual
confined provider and checks that it cannot open the runtime database. Failpoints
require both `MITOS_ENABLE_FAILPOINTS=1` and `MITOS_APPFIXTURE_FAILPOINT` equal to
`before_admission`, `after_admission`, `before_publication`, or
`after_publication`. The selected boundary exits 86 without cleanup.
