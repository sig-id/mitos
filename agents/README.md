# Default agent library

These are reusable agent definitions for the [agent-library design](../spec/system/07_AGENT_LIBRARY.md).
They are embedded in the server and used by the automatic Codex workflow. Add or
edit worker roles in **Agent library**; the server preserves revisions and freezes
definitions for admitted attempts. The orchestrator can create task-specific specializations
within a graph. Existing dormant goals still require an explicit start.

| Definition | Intended contribution |
|---|---|
| `mitos.generalist` | General work and a base for new specialists |
| `mitos.researcher` | Source investigation and evidence gathering |
| `mitos.architect` | Design, interfaces and tradeoffs |
| `mitos.engineer` | Implementation and relevant checks |
| `mitos.reviewer` | Independent assessment of a specific candidate |
| `mitos.tester` | Focused test design/execution and reproduction |
| `mitos.writer` | Documentation and evidence-based synthesis |
| `mitos.orchestrator` | Work discovery, agent selection/creation, and replanning |

This is a starting collection, not a required pipeline. A report task may need only
a researcher and a writer. The orchestrator may create a runtime-contract auditor, data
analyst, or another specialist from an appropriate base rather than asking the user
to configure a role. Creating an agent means creating its definition/binding and a
new attempt on an eligible runtime; it does not install a new model or bypass capacity.

## Package format

`defaults/library.json` lists the bundle's exact IDs, revisions and relative manifest
paths. Each directory contains `agent.json` and `instructions.md`. The manifest pins
the instructions' SHA-256. Authoring manifests follow `definition.schema.json`;
registration also validates references and compatibility and assigns the canonical
content identity described in the spec. Changing published content requires a new
revision. Revision 1 here is the initial authored version, not a deployed registry entry.

Runtime requirements use `native_output` (preserve native responses), `tool_use`
(use registered tools), and, for planning, `proposal_tools` (an authenticated proposal
boundary). Preferences are empty so a definition is portable across eligible native
runtimes. These names are target adapter capabilities, not claims about current CLIs.

Capability requests use `workspace.read/write`, `artifact.read/write`, `command.run`,
`network.read`, `graph.read/propose`, and `agent.specialize`. They are admission inputs,
not permissions granted by the file. `command.run` can have effects and requires an
appropriate execution profile; a review prompt is not a command sandbox. Required
context is supplied by admission; optional context is selected from authorized input
sources. Empty knowledge/skill lists deliberately avoid inventing installed resources.

## Chief Wiggum adaptation

Source: sibling repository `chief-wiggum`, revision
`f11e476037819b9cd15ad00cc16bfd0956481aed`. Each manifest records its source paths.
The original MIT notice is retained in [licenses/chief-wiggum-MIT.txt](licenses/chief-wiggum-MIT.txt).
These are condensed, adapted roles; they do not load or execute Chief Wiggum code.

Preserved: useful role intent, requirements awareness, evidence-based review,
context-sensitive investigation, clear documentation and practical validation.
Removed: PASS/FAIL/FIX/SKIP replies, XML prompt sections, pipeline jumps, fixed PRD
paths, automatic session inheritance, universal Git restrictions, numerical confidence
gates, and mandatory role sequences. Worker replies remain native. The orchestrator uses
an explicit proposal interface; the runtime still owns authority and completion.

## Validate authored packages

Run `python3 agents/validate.py` with the Python `jsonschema` package available.
This optional authoring check validates manifests, unique bundle identities,
package paths and exact instruction digests. It does not register definitions,
verify native authentication, or claim runtime compatibility.
