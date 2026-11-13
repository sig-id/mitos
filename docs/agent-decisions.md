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

Automatic workers now run through Codex app-server under the compute provider's
process ownership. Supported questions and approvals share the explicit-session
Inbox and resolution service. The process deadline remains active while a question
is pending. Unsupported native requests fail visibly instead of receiving invented
answers. Final reports are captured from final agent-message items, and failed or
interrupted turns cannot be accepted as successful attempts.

The investigation route starts one bounded read-only Codex app-server investigation.
It cannot ask recursive questions, and requested escalations are denied. Its result
must match the original cards and current policy; uncertainty returns to the user.
This uses the native runtime's read-only sandbox, not independent OS confinement.

Structured asynchronous Codex question items create nonblocking Inbox questions.
Their answers use turn/steer with the original active-turn precondition and become
applied only after the source acknowledges delivery. Turn completion invalidates
outstanding replies. Terminal input during an active turn also uses turn/steer;
ordinary question prose is never parsed as an approval request.

Independent supervision runs before resolution and can veto an otherwise eligible
route. See [configuration, incident handling and coverage](independent-supervision.md).
With supervision enabled, an explicit denied operation opens a stop incident instead
of merely delivering a declined answer.
