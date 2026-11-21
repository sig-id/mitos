# Default watch templates

Ready-made `watch_create` inputs for adaptive orchestrators (spec 09, gate W4).
Each file is the exact body the granted `watch_create` tool takes. The creator
owns the result: it fills `owner` is already `responsibility`, the runtime
clamps the empty scope to the creator's graph, and the creator renews the lease
(`lease_ms` is at most 24 hours) while it still wants the watch.

| Template | Covers |
| --- | --- |
| `child-terminal-states` | children finishing, failing or being cancelled; terminal must-deliver rules reach it anyway |
| `verification-failures` | `verification_failed` evidence, one wake per failed child |
| `quiescence-steer` | `quiescent` steer edges plus the block edges the rules must deliver, with a 15-minute silence switch |

These are starting points, not capabilities: a definition requests the
`watch.manage` capability and the orchestrator decides, from its assignment,
whether and how to watch. `agents.WatchTemplates()` loads and validates them;
the studio tests create real watches from each one.
