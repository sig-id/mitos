# Mitos

LLM task runner for automated code analysis and generation.

## Usage

```bash
# Show help
zig-out/bin/mitos --help

# List available providers
zig-out/bin/mitos providers

# Run with the fake provider (for testing)
zig-out/bin/mitos run --provider fake --task "Summarize this repo" --run-id test-run

# Run with real providers (headless only)
zig-out/bin/mitos run --provider codex --task "Fix failing tests"
zig-out/bin/mitos run --provider claude --task "Summarize this repo"
zig-out/bin/mitos run --provider gemini --task "Review this code"
```

## Artifacts

A successful run writes a report to:

```
.runs/<run-id>/report.md
```

## License

MIT
