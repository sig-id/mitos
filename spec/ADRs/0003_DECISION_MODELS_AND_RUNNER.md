# 0003 – Typed decision inference within a deterministic runner

Status: accepted. Research checked 2026-11-11.

## Context

The batch runner already has segments, verification, checkpoints, and bounded
recovery, but these mechanisms did not name their runtime model. A decision-model
integration needs both a provider interface and a precise account of who owns
control flow, authority, persistence, and recovery.

The provider research establishes a shared semantic pattern, with two wire families:

| Source | Interface finding |
|---|---|
| [TypeSafe Jev introduction](https://docs.typesafe.ai/introduction) and [Choice reference](https://docs.typesafe.ai/primitives/choice) | State plus keyed typed questions; Choice selects from criteria and returns probabilities and confidence. Noul and Score are other primitives. |
| [Laya author's model card](https://huggingface.co/convaiinnovations/laya#self-hosting-jev-compatible-http-server) and [source repository](https://github.com/NandhaKishorM/laya) | The self-hosted server explicitly implements Jev-compatible `POST /v1/systemone`. Authentication is configurable. |
| [OpenAI Decisions guide](https://developers.openai.com/api/docs/guides/decisions) and [API reference](https://developers.openai.com/api/reference/resources/decisions/methods/create) | `POST /v1/decisions` uses input and an array of named questions. Choice uses value/description options and returns a probability array. Predicate and Score are also available; individual questions may be refused. |

Our conclusion: System One is a practical shared compatibility profile for Jev and
Laya, not evidence of a universal decision-model standard. OpenAI has similar typed
semantics but a distinct wire format. Chat Completions, function calling, and free-form
JSON generation are not substitutes for either contract. Hosted resellers found in
search were not used to define the integration.

For example, a System One request has this shape (the application supplies state
and option descriptions):

```json
{
  "model": "MODEL_ID",
  "state": {"signal": "predicate_failed"},
  "questions": {
    "action": {
      "type": "choice",
      "instructions": "Choose an eligible recovery or ask for review.",
      "criteria": {"rewind": "Use the passing checkpoint", "escalate": "Ask the operator"}
    }
  }
}
```

Its answer is addressed as `answers.action`, with a `choice`, `confidence`, and
`probabilities` keyed by option. OpenAI expresses the same question as:

```json
{
  "model": "MODEL_ID",
  "input": "{\"signal\":\"predicate_failed\"}",
  "questions": [{
    "name": "action",
    "type": "choice",
    "instructions": "Choose an eligible recovery or ask for review.",
    "choices": [
      {"value": "rewind", "description": "Use the passing checkpoint"},
      {"value": "escalate", "description": "Ask the operator"}
    ]
  }]
}
```

Its answer is a named item in `answers`, with probabilities represented as
`[{"value":"rewind","probability":0.9}, ...]`. The caller still decides whether
that distribution justifies taking an action. For the other shared primitives,
Jev/Laya call the boolean-probability question `noul`; OpenAI calls it `predicate`.
Both have a rubric-based `score`. Similar concepts do not imply identical schemas,
confidence semantics, or calibration.

## Decision

Name the current runtime `batch-segments/v1`: one installed command step, sequential
segments, independently checked results, and bounded checkpoint recovery. Keep service
activation and inter-task dependencies above it. Keep interactive adapters distinct
until their admission and recovery contracts actually converge.

Add optional Choice inference at `before_segment` and `before_recovery`. Normalize
both provider families into a small typed answer carrying probabilities, confidence,
model identity, and usage. Keep runtime identity, request receipts, deadlines, and
effect eligibility outside vendor schemas. Request only the question needed at the
boundary. No provider SDK dependency is needed for these bounded JSON HTTP calls.

The runtime constructs options and operands. Provider output is evidence used by
policy, not authority. Deterministic eligibility precedes inference; validation and
threshold policy precede effects. Human decisions and result acceptance remain
separate. A model cannot introduce a new action, enlarge a grant, choose a command,
or turn a failing predicate into acceptance.

Persist requests before dispatch, count all requests against an attempt allowance,
and persist the outcome before starting the next segment or committing recovery.
Replaying the journal is pure state reconstruction. Unknown inference is reviewed,
not retried; local correlation IDs do not imply vendor idempotency. Keep generic
provider errors and validated answers rather than raw remote diagnostics.

## Consequences and alternatives

Existing applications retain their behavior and require no API key. Local Laya and
hosted APIs use the same runtime boundaries. Small adapters accommodate wire changes
without replacing the execution model. Probability and confidence thresholds need
application-specific evaluation; their numerical values are not portable accuracy
guarantees across models. Tests establish protocol and runtime behavior, not model
quality or paid endpoint access.

A universal vendor schema was rejected because these two APIs differ. A command-only
adapter was considered before research and superseded by direct typed HTTP adapters.
A model-owned execution loop was rejected because it would obscure admission,
verification, and restart authority. A generic DAG interpreter and arbitrary model
branches are deferred: they require step identity, join/wait semantics, cancellation
scope, and effect receipts beyond the current single-step runtime.

The initial implementation intentionally excludes Predicate/Noul and Score routing,
model-driven human approval, token/dollar reservations, automatic provider fallback,
and UI policy editing. Those are explicit scope limits, not implied capabilities.

The owning contracts are [runtime](../system/02_RUNTIME.md#runner-runtime-and-optional-decision-inference)
and [interfaces](../system/05_INTERFACES_AND_INTEGRATIONS.md#decision-model-provider-profile).
Delivery limits remain in [conformance](../system/06_DELIVERY_AND_CONFORMANCE.md).

## Follow-on graph composition

[ADR 0004](0004_ADAPTIVE_WORK_GRAPHS.md) defines the target adaptive composition above
this initial batch runtime and supersedes only the deferral of specifying graph
semantics. It does not widen the implemented hooks, change provider wire formats,
or make a model answer authoritative. Its graph/controller lifecycle requires the
separate delivery gates and retains this adapter's compatibility limits.
