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
stop work. Neither the request router nor the orchestrator establishes confinement.

Automatic workers now run through Codex app-server under the compute provider's
process ownership. Supported questions and approvals share the explicit-session
notification and resolution service. The process deadline remains active while a question
is pending. If execution is interrupted with an unanswered request, the saved attempt
records `native_request_unanswered`. Automatic planning and replacement stop until
the operator addresses the prerequisite and resumes the workflow. Continuation
retains the task, saved conversation and harvested work; it does not grant the old
permission. Asynchronous clarifications allow independent work while the attempt is
running, but retain the missing-input reason if execution is interrupted.

Codex MCP connector elicitations use the same durable decision/outbox service.
Confirmation forms, up to three boolean or two-choice enum fields, and HTTPS
browser-step confirmations receive explicit human review, including when command
review is delegated. Defaults never authorize an answer. Unsupported forms and
verification challenges are cancelled at the tool boundary in headless workers,
allowing the agent to save its report and explain the unavailable native interaction.
Other unsupported native requests fail visibly instead of receiving invented
answers. Final reports are captured from final agent-message items, and failed or
interrupted turns cannot be accepted as successful attempts.

Successful supervised observations renew the active session lease after their
evidence is journaled. A large journal write cannot expire the session merely by
delaying its heartbeat; closed or already-expired sessions are not revived.

The investigation route starts one bounded read-only Codex app-server investigation.
It cannot ask recursive questions, and requested escalations are denied. Its result
must match the original cards and current policy; uncertainty returns to the user.
This uses the native runtime's read-only sandbox, not independent OS confinement.

Structured asynchronous Codex question items create nonblocking notifications.
Long questions use a shortened card title; the complete question and suggestions
remain in the saved request context and answer delivery. A question that cannot be
recorded fails visibly and stops automatic replacement instead of silently waiting
for an answer the user cannot provide.
Their answers use turn/steer with the original active-turn precondition and become
applied only after the source acknowledges delivery. Turn completion invalidates
outstanding replies. Terminal input during an active turn also uses turn/steer;
ordinary question prose is never parsed as an approval request.

Independent supervision runs before resolution and can veto an otherwise eligible
route. See [configuration, incident handling and coverage](independent-supervision.md).
With supervision enabled, an explicit denied operation opens a stop incident instead
of merely delivering a declined answer.

## Notifications and API setup

Requests undergoing automatic review show **Reviewing automatically** and do not raise a human notification. The reviewer runs independently of the orchestrator and worker; supported requests use the configured Decisions API. If a supported routine request has no provider configured, **Configure Decisions API** opens System Configuration. The workflow header also links to **Decisions API & approvals**.

Immediate questions and approvals needing the operator appear in **Notifications** on every workspace
surface and in a persistent action banner. Review and answer them without leaving
current work. Workflow nodes and goals show **Waiting for an answer** while an
associated native request is pending. Inbox collects reports and project summaries;
it is no longer the queue for runtime approval cards. Desktop notifications are
opt-in and work while a Mitos page is open.

In **System Configuration → Decisions API**, select **Configure API connection**
and use a provider button to fill the protocol, full endpoint, and model together.
These are dedicated decision APIs: they score a fixed set of choices and return
probabilities and confidence, without generating free-form text. Generic chat
completion models and prompted JSON decisions are not supported.

Choose **API key** to paste a key, or **Environment variable** to enter its name.
Pasted keys work without restarting; environment values must be set before starting
Mitos. Saving writes a pasted key to an owner-only (0600) file in the server data
directory's `credentials/` directory (0700), outside event logs and project files.
These files use filesystem permissions, not encryption. Settings and execution
plans hold opaque references; responses never return the key. Blank key fields keep
an existing reference; replacing or removing a key takes effect on save. Previous
key files remain for frozen execution plans. Saved keys are bound to the endpoint;
re-enter a key after changing that endpoint. Selecting a preset for another endpoint
clears the old credential from the form and fills the provider's environment name.

**Test API connection** makes one small inference request without saving settings
or credentials or approving work. Existing installed decision applications remain
supported.

Provider presets verified against primary documentation on 2026-11-14:

| Preset | Full endpoint | Model | Protocol |
| --- | --- | --- | --- |
| [TypeSafe Jev](https://api.typesafe.ai/openapi.json) | `https://api.typesafe.ai/v1/systemone` | `jev-latest` | System One |
| [OpenAI](https://developers.openai.com/api/docs/guides/decisions) | `https://api.openai.com/v1/decisions` | `gpt-6-luna` | OpenAI Decisions |
| [Gemini](https://aistudio.google.com/docs/decisions-api) | `https://generativelanguage.googleapis.com/v1beta/decisions` | `gemini-decision-1` | Gemini Decisions |
| [OpenRouter Jev](https://openrouter.ai/docs/guides/features/decisions) | `https://openrouter.ai/api/alpha/decisions` | `typesafe/jev-1.14` | OpenRouter Decisions |
| Cloudflare Clef (OpenRouter) | Same OpenRouter Decisions endpoint | `cloudflare/clef-flash` | OpenRouter Decisions |
| Mercury Decide (OpenRouter) | Same OpenRouter Decisions endpoint | `inception/mercury-decide` | OpenRouter Decisions |

All fields remain editable. OpenRouter's documented decision-model catalog supplies
the Clef and Mercury presets; these use an OpenRouter key. Provider access and model
availability depend on the account. Presets do not make network requests or save.

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

## System configuration and project overrides

`/settings` edits defaults for the current Mitos server. Choose a project under
**Configuration scope** to edit overrides (`/settings?project=PROJECT_ID`). A project
with no direct provider or decision application inherits the system connection.
Existing project providers are preserved as overrides. Select **Inherit system
default** to remove an override. Provider configuration alone does not enable
automatic approval; project autonomy, boundaries, exact rules and supervision still
control authority. No connection configured means model review falls back to the
operator (or the project's configured investigation fallback).

**Agent launch defaults** selects an existing environment profile and additional CLI
arguments for each native runtime. Arguments are a JSON array of literal strings,
not a shell command. An explicit session profile takes precedence over the default.
A project override replaces that agent's complete system default; an empty argument
array explicitly removes inherited flags. Uncheck the override to restore inheritance.
New native sessions use the current defaults. Resumes and forks preserve the source
environment profile, including the native environment, while applying current default
arguments. Retrying a keyed launch returns its original session even if defaults
changed or the new profile is unavailable. Coordinated app-server workers,
setup assistants and investigations retain their managed launch protocols. Agent
library role instructions and project execution/resource settings retain their own
editors. Environment profiles are defined by the existing agent registry configuration.

Storage and implementation:

- `internal/studio/system_configuration.go` validates and persists defaults as
  `system_configuration_changed` journal events. Project overrides remain in
  `project_agent_policy_changed` events. Both live in `<--data>/runtime.db`, normally
  `.mitos/runtime.db`; neither AGENTS.md nor browser storage owns these settings.
- `internal/web/settings.go` provides `GET/PUT /api/settings` and
  `GET/PUT /api/projects/{project}/settings`. Writes require the revision read from
  the corresponding GET. `POST /api/settings/decision-provider/test` tests an unsaved
  connection without requiring a project.
- `internal/studio/agent_policy.go` resolves project provider overrides before system
  defaults. System configuration revisions invalidate stale route/answer receipts and
  stale project writes; changing settings never resets request call allowances.
- `internal/studio/agent_routing.go` and `agent_resolution.go` classify requests and
  select permitted answers in separate, journaled calls. `internal/decisionmodel/model.go`
  implements the System One, OpenAI Decisions, Gemini Decisions, and OpenRouter Decisions
  wire adapters, uses a supplied key or named environment variable, bounds HTTP duration
  and payload sizes, and
  validates choices, probability distributions and confidence. It performs no automatic
  HTTP retry or redirect and executes no action itself.
- `internal/studio/decision_credentials.go` stores pasted keys separately and resolves
  references only at the outbound inference boundary. Gemini uses `x-goog-api-key`;
  the other adapters use bearer authentication.
- `internal/agentcli/registry.go` applies native launch defaults before constructing
  arguments; `internal/desk/terminal.go` and `continuation.go` resolve project inheritance
  for new sessions and continuations. Secret environment values are not serialized.

Application workflow hooks configured with `decision_model` in `app.json` remain
separate explicit execution profiles. System defaults govern agent-request review.
