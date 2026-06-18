# Mitos

LLM task runner for automated code analysis and generation.

## Docs

- [Product](spec/02_PRODUCT.md): mitos as an AI-native agent operating system.
- [Gentix Facilitator](spec/08_FACILITATOR.md): the hosted capability
  service built on mitos — "Pickaxe for agent harnesses": sellers' processes
  executed by full agent harnesses in sandboxes, sold as metered completed
  jobs with proof pages. Business model, MVP scope, and gap analysis
  (decided 2026-06-11; pairs with `gentix/spec/product/00_STRATEGY.md`).

## Build

```bash
# Build the Zig CLI and sidecar binaries.
env ZIG_GLOBAL_CACHE_DIR=/tmp/zig-cache zig build
```

The build writes binaries under `zig-out/bin/`, including:

- `zig-out/bin/mitos` — standalone CLI
- `zig-out/bin/mitos-term` — BEAM-owned engine sidecar

## Test

```bash
# Zig unit/integration tests.
env ZIG_GLOBAL_CACHE_DIR=/tmp/zig-cache zig build test

# BEAM protocol/control-plane tests.
cd beam
mix test
```

## Usage

### Standalone CLI

```bash
# Show help
./zig-out/bin/mitos --help

# List available providers
./zig-out/bin/mitos providers

# Run with the fake provider (for testing)
./zig-out/bin/mitos run --provider fake --task "Summarize this repo" --run-id test-run

# Run with real providers (headless only)
./zig-out/bin/mitos run --provider codex --task "Fix failing tests"
./zig-out/bin/mitos run --provider claude --task "Summarize this repo"
./zig-out/bin/mitos run --provider gemini --task "Review this code"
```

### BEAM Control Plane

Build the sidecar first, then start the BEAM HTTP/WebSocket service:

```bash
env ZIG_GLOBAL_CACHE_DIR=/tmp/zig-cache zig build

cd beam
MITOS_TERM_BIN=../zig-out/bin/mitos-term MITOS_HTTP_PORT=7682 mix run --no-halt
```

In another terminal:

```bash
curl http://127.0.0.1:7682/healthz

curl -X POST http://127.0.0.1:7682/agents \
  -H 'content-type: application/json' \
  -d '{"provider":"fake","task":"hello","transport_order":["stdio-json","pty"]}'
```

The browser terminal UI is served at `http://127.0.0.1:7682/`.

## Artifacts

Standalone runs write reports under:

```
.runs/<run-id>/report.md
```

BEAM sessions write control-plane traces under `.runs/<session-id>/` unless
`MITOS_TRACE_DIR` or the application trace base directory is configured.

## License

MIT
