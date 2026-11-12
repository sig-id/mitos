# Terminal renderer evaluation

This directory contains development-only dependencies; no Ghostty code is included
in the browser bundle. Mitos retains its vendored xterm.js renderer. The
[streaming contract](../../spec/system/05_INTERFACES_AND_INTEGRATIONS.md#terminal-streaming)
defines output replay and input behavior.

The test compares the vendored xterm.js renderer against pinned ghostty-web 0.4.0.
Recordings, screenshots, and generated reports stay in ignored `results/`.

From the repository root:

```sh
npm ci --prefix tools/terminal-eval --ignore-scripts
python3 tools/terminal-eval/capture.py tools/terminal-eval/results/recordings
CHROMIUM_PATH=/absolute/path/to/chromium npm test --prefix tools/terminal-eval
go build -o bin/mitos ./cmd/mitos
CHROMIUM_PATH=/absolute/path/to/chromium npm run test:stream --prefix tools/terminal-eval
```

Startup recordings use installed CLIs with isolated configuration, no task prompt,
and a bounded capture period. Results describe the captured workloads, not general
renderer performance or full coding-session coverage.

After `go build -o bin/mitos ./cmd/mitos`, run `npm run test:sessions --prefix tools/terminal-eval` to exercise the workspace selector, project/session context menus, keyboard focus, background-session actions, native resume, context fork, handover, and mobile dialog against isolated local process fixtures. Set `CHROMIUM_PATH` if using a system Chromium. No model calls are made.

`npm run test:diff --prefix tools/terminal-eval` exercises the side-by-side Changes
view against a temporary Git repository, including syntax and intraline colors,
line alignment, added/deleted/renamed/binary files, quoted paths, missing final
newlines, mobile layout, and bounded output truncation.

`npm run test:operating --prefix tools/terminal-eval` covers project creation,
reviewed launch, durable task/attempt bindings, supervision notes, replacement,
stale-report refusal, human report acceptance, service activation, mobile layout,
disconnection, wrong-server routing, and restart continuity. It uses temporary data
and local process fixtures. `MITOS_BIN` selects a separately built server binary;
`MITOS_SCREENSHOT_DIR` optionally saves desktop and mobile screenshots.

`npm run test:pages --prefix tools/terminal-eval` audits all HTML page families at
desktop and mobile widths against an isolated demo. It checks shared navigation,
assets, scroll containment, cross-page script loading, application launch, report
and artifact access, service controls, and decision resolution. It makes no model
calls and does not mutate the running demo. Set `MITOS_SCREENSHOT_DIR` to retain
the page screenshots.

`npm run test:graphs --prefix tools/terminal-eval` exercises free-text goal creation,
planner-generated steps, dependency edges, output formatting, stable zoom and
selection, the sidebar work tree, automatic result acceptance, mobile scrolling,
reconnection, exact-key recovery after a lost creation response, explicit start of
old goals, Devices/Plugins navigation, and agent-library create/edit persistence.
It launches an isolated native-CLI fixture; no model calls or demo changes are made.
`MITOS_TEST_NATIVE_CODEX=1 go test ./internal/studio -run TestAutomaticInstalledCodex`
separately checks the installed authenticated Codex on a temporary one-file project.

`npm run test:picker --prefix tools/terminal-eval` checks folder navigation, hidden
folders, filtering, editable suggested names, invalid paths, stale response
protection, keyboard entry, mobile layout, and opening projects from both the
project and terminal pages. It uses an isolated temporary directory and server.
