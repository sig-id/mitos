# Cooperation in Mitos

`AGENTS.md` supplies project instructions for all agents. Add a link to an optional
root `COOPERATION.md` for team conventions. Project setup can edit that document;
new coordinated work freezes its contents and hash. Existing work keeps its snapshot.
The filename is a proposed convention, not an automatic discovery promise for other
agent tools. Instructions do not grant permissions.

A project cooperation document can describe how to divide assignments, announce
shared-file changes, exchange artifacts, request help, resolve conflicts, and hand
off unfinished work. Prefer concrete responsibilities and completion evidence over
fixed chains of roles.

## Local protocol

`POST /api/projects/{project}/cooperation` records a `mitos.cooperation/v1` message.
`GET` on the same endpoint returns the ordered messages and transport capabilities.
The local authenticated server boundary applies. Supply a live `run_id` or
`session_id`, the originating `task_id`, and its current durable task `revision`.

```json
{
  "version": "mitos.cooperation/v1",
  "id": "investigate-failure-1",
  "task_id": "task_ID",
  "run_id": "run_ID",
  "revision": 1,
  "kind": "help",
  "body": "The build fails in the parser. Request an independent diagnosis.",
  "blocking": false,
  "artifacts": ["output.txt"]
}
```

Kinds are `assignment`, `help`, `question`, `approval`, `observation`, `handoff`,
`result`, and `answer`. An `answer` references `in_reply_to`. Requests may specify
an RFC3339 `expires_at`. Identical submissions return their original receipt;
conflicting IDs, stale revisions, expired requests and competing answers fail.
The initial transport binds replies to the same task. Cross-task resolver discovery
is not advertised by this version.

These messages record proposals and evidence. They do not directly launch work,
grant permissions, steer a native conversation, or accept a result. Native questions
and approvals use the agent bridge's request/response protocol. The coordinator
consumes cooperation evidence when revising work. Each adapter must declare which
native request types and live input operations it supports.
