# Agent request decisions

Project setup's **Autonomy & approvals** panel stores operator settings independently
of AGENTS.md. `manual` leaves questions and approvals to the operator. `delegated`
enables automatic handling within configured authority. Rule matching compares the
complete command and exact relative working directory; it does not parse shell
prefixes. A rule delegates the command's effects, not just its spelling. Additional
network/filesystem permission requests are not implicitly included.

A `decision_application` references an installed application's existing
`decision_model` configuration (OpenAI Decisions or System One). Endpoints and API
credential references are not accepted from agents. Existing batch and graph
choices retain their behavior.

The runtime first applies explicit rules. A matching `allow` or `deny` rule gives a
direct answer. A `review` rule delegates a bounded approval choice. Missing authority
always routes to the operator. Ambiguous questions use the configured Choice API
as a classifier selecting `human`, `decision`, or, when configured, `investigate`.
The decision route is available only for supported fixed answer options.

Classification does not answer the request. Answer selection is a separate call,
with a separate receipt and the same request allowance. Failed/uncertain inference
uses the configured fallback. Policy changes are rechecked before recording routes
and before applying answers. Exact rules skip classification.

Bridge clients may explicitly request routing with
`POST /api/agent-sessions/{session}/requests/{request}/route`, authenticated with the
session token. Native answer delivery still follows the original request lifecycle.
Every route records its policy revision and whether it came from a rule, classifier
or fallback. Model calls journal intent, immutable inputs/configuration, deadline,
outcome and usage before effects. Pending calls after restart become unknown
consumption and are not resent. Revising policy does not reset the call allowance.

The supervision model has a separate responsibility: examine boundaries and hold or
stop work. Neither the request router nor the coordinator establishes confinement.
