# ADR 0001: Retain xterm.js for terminal rendering

Status: accepted, 2026-09-20.

## Decision and consequences

Retain vendored xterm.js. The Ghostty trial did not satisfy the compatibility
gate; no alternate renderer selector is required. Reconsider after a candidate
passes clean recreation, Unicode, input, accessibility, and live-session checks.
The [terminal streaming contract](../system/TERMINAL_STREAMING.md) owns normative
transport behavior. Results below are historical evidence, not current benchmarks.

## Renderer evaluation, 2026-09-20

The trial uses the published `ghostty-web` **0.4.0** package and the vendored
xterm.js build under headless Chromium. Each independent case starts in a fresh
page; one separate lifecycle case deliberately disposes and recreates a terminal
using the same Ghostty WASM instance. Theme, dimensions, and input come from
Mitos's integration.

| Check | xterm.js | ghostty-web 0.4.0 |
|---|---|---|
| Recreate terminal with clean screen | Pass | Fail: `old session contents` followed by recreation and `new` reads back `new session contents` |
| Byte-split UTF-8 and escape sequences | Pass | Fail: combining accent missing from buffer text readback |
| Alternate-screen restoration | Pass | Pass |
| Cursor movement and erasure | Pass | Pass |
| ANSI red with Mitos theme | Pass | Pass |
| Physical Ctrl+C/D and application cursor keys | Pass | Pass |
| Existing bracketed-paste contract | Pass | Different: preserves LF where xterm emits CR |
| Disable keyboard input | Pass | Pass |
| Cursor-position query response | Pass | Pass |
| Resize and selection | Pass | Pass |
| Existing `modes.applicationCursorKeysMode` API | Pass | Missing; Ghostty exposes a different mode API |

Paste normalization and mode access are integration differences, not proof of
terminal-standard violations. Missing combining marks were observed through the
buffer API; that alone does not prove a glyph-rendering failure. The reproduced
stale-screen behavior is the main reason to defer a production switch.

Fresh recordings were captured from Codex, Claude Code, OpenCode, and Pi startup
screens in isolated configuration directories, without API credentials or task
prompts. Each was replayed through both engines with 127-byte chunk boundaries.
All four produced different buffer text snapshots; visual differences must be
inspected separately rather than equating every buffer difference with a broken
screen. These are startup/authentication screens, not full coding-task coverage.
Recordings and screenshots remain local under the evaluation tool's ignored
`results/` directory.

A synthetic 188,890-byte, 5,000-line colored Unicode burst took a median **42.9 ms**
with xterm.js and **45.9 ms** with Ghostty over five fresh-page samples in this run.
The measurement includes write completion and a following animation frame. This
small, noisy browser trial does not establish general performance rankings or
native Ghostty performance. It provides no measured reason to override the
compatibility findings.

The next renderer trial should resolve clean recreation and text readback first,
then cover full live agent sessions, IME, clipboard, accessibility, and multiple
browsers before changing the production dependency.

## Reproduce

The evaluation is a development tool; serving Mitos still needs no frontend build
or Node runtime. Python captures CLI startup; npm installs pinned evaluation-only
dependencies. Chromium must be installed (or installed through Playwright).

```sh
npm ci --prefix tools/terminal-eval --ignore-scripts
python3 tools/terminal-eval/capture.py tools/terminal-eval/results/recordings
CHROMIUM_PATH=/absolute/path/to/chromium npm test --prefix tools/terminal-eval
go build -o bin/mitos ./cmd/mitos
CHROMIUM_PATH=/absolute/path/to/chromium npm run test:stream --prefix tools/terminal-eval
```

`capture.py` launches only installed CLIs, supplies isolated configuration roots,
forwards terminal-identification replies, and terminates each capture after six
seconds. It passes no prompt. Inspect locally before sharing any recordings.
`evaluate.mjs` writes `results/report.json` and screenshots. Candidate failures are
reported as a retain-xterm decision; baseline failures make the command fail.

Validation also covers binary replay across frame boundaries, resume without
repeated bytes, live output and final status, acknowledgment flow control,
cross-origin rejection, shutdown with an open stream, direct keyboard controls,
reconnection, idle heartbeats, and eight simultaneous browser viewers.

Sources: [ghostty-web](https://github.com/coder/ghostty-web),
[Ghostty library status](https://github.com/ghostty-org/ghostty#cross-platform-libghostty-for-embeddable-terminals),
[Coder WebSocket API](https://pkg.go.dev/github.com/coder/websocket).
