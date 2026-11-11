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
