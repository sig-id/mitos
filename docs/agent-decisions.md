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
direct answer. A `review` rule delegates a bounded approval choice. Without an exact grant or explicit project decision-review delegation, requests
route to the operator. Ambiguous questions use the configured Choice API
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
notification and resolution service. The process deadline remains active while a question
is pending. Unsupported native requests fail visibly instead of receiving invented
answers. Final reports are captured from final agent-message items, and failed or
interrupted turns cannot be accepted as successful attempts.

The investigation route starts one bounded read-only Codex app-server investigation.
It cannot ask recursive questions, and requested escalations are denied. Its result
must match the original cards and current policy; uncertainty returns to the user.
This uses the native runtime's read-only sandbox, not independent OS confinement.

Structured asynchronous Codex question items create nonblocking notifications.
Their answers use turn/steer with the original active-turn precondition and become
applied only after the source acknowledges delivery. Turn completion invalidates
outstanding replies. Terminal input during an active turn also uses turn/steer;
ordinary question prose is never parsed as an approval request.

Independent supervision runs before resolution and can veto an otherwise eligible
route. See [configuration, incident handling and coverage](independent-supervision.md).
With supervision enabled, an explicit denied operation opens a stop incident instead
of merely delivering a declined answer.

## Notifications and API setup

Requests undergoing automatic review show **Reviewing automatically** and do not raise a human notification. The reviewer runs independently of the coordinator and worker; supported requests use the configured Decisions API. If a supported routine request has no provider configured, **Configure Decisions API** opens the connection and approval-boundary form directly. The workflow header also links to **Decisions API & approvals**.

Immediate questions and approvals needing the operator appear in **Notifications** on every workspace
surface and in a persistent action banner. Review and answer them without leaving
current work. Workflow nodes and goals show **Waiting for an answer** while an
associated native request is pending. Inbox collects reports and project summaries;
it is no longer the queue for runtime approval cards. Desktop notifications are
opt-in and work while a Mitos page is open.

In **Project setup → Decisions API & approvals**, select **Configure API connection**
and enter the protocol, full endpoint, model, credential environment-variable name,
timeout and confidence thresholds. **Test API connection** makes one small inference
request; it does not save settings or approve any work. Credentials must already be
available in the server environment. The project policy stores only their variable
name, never their value. Existing installed decision applications remain supported.

Choose **Decisions API reviews commands within my boundaries** and write the allowed
scope in **Approval boundaries** to explicitly delegate supported command approval
review. Exact deny rules still win. Unsupported requests and additional permission
scopes still require the operator; insufficient evidence or failed inference uses
the configured fallback. Saving the policy does not mean every command is approved.
Existing projects retain their previous approval policy. Automatic planning alone
does not grant automatic approval authority.

Large native tool results are represented in supervision evidence with a bounded
preview, original byte count and SHA-256 digest. Native clients still receive the
original events. Command inputs, tool arguments and changed paths are not truncated;
metadata that cannot fit is rejected with a specific size error.
