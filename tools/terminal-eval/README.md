# Terminal renderer evaluation

See [the renderer decision and evaluation](../../spec/ADRs/0001_TERMINAL_RENDERER.md)
for setup, captured workloads, results, and their limits. This directory contains
development-only dependencies; no Ghostty code is included in the Studio bundle.

The test compares the vendored xterm.js renderer against pinned ghostty-web 0.4.0.
Recordings, screenshots, and generated reports stay in ignored `results/`.

After `go build -o bin/mitos ./cmd/mitos`, run `npm run test:sessions --prefix tools/terminal-eval` to exercise the workspace selector, project/session context menus, keyboard focus, background-session actions, native resume, context fork, handover, and mobile dialog against isolated local process fixtures. Set `CHROMIUM_PATH` if using a system Chromium. No model calls are made.

`npm run test:diff --prefix tools/terminal-eval` exercises the side-by-side Changes
view against a temporary Git repository, including syntax and intraline colors,
line alignment, added/deleted/renamed/binary files, quoted paths, missing final
newlines, mobile layout, and bounded output truncation.
