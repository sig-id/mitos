# S4 control-plane scale evidence

Observed on 2026-11-17 with Go 1.26.11, Linux/amd64, an Intel Core Ultra 7 265K,
GOMAXPROCS 20, and SQLite WAL / synchronous FULL. The fixed dataset contains 20
tenants, 2,000 dormant Tasks, and 8 additional Tasks claimed by 8 registered worker
identities. Runtime execution capacity is 8. The dormant population holds no
worker slot and is reported separately from admitted execution.

| Measure | Observed | Declared budget |
|---|---:|---:|
| Replay | 1.614 s | 15 s |
| Live Go heap after replay | 84.36 MiB | 512 MiB |
| Go heap increase from replay | 81.98 MiB | Included in live heap |
| Quiescent process RSS | 128.2 MiB | 768 MiB |
| Mean durable claim | 18.261 ms | Informational |
| Claim p95 (nearest rank, 8 samples) | 24.363 ms | 250 ms |
| Run read | 50 items, 242,122 bytes | 50 requested, 16 MiB |
| Per-run event read | 3 items, 24,270 bytes | 200 requested, 16 MiB |

Reproduce from the repository root:

```sh
go test ./internal/studio -run '^$' \
  -bench '^BenchmarkDormantResponsibilities$' -benchtime=1x -count=1 -timeout=5m
```

The [raw output](s4-dormant-scale.txt) records the observed run. Setup creates and
fsyncs the complete dataset, closes the store, then drops unreachable setup state
before replay. Memory is sampled after GC and scavenging: both Go heap and Linux
process RSS are reported. Replay and individual claims include their actual store
work; the benchmark timer excludes dataset construction. Total command execution
was 52.445 seconds, including setup.

This measures the transactional control plane with active durable grants. Model
execution, network RTT and production-model throughput are separate measurements.
Read tests also exercise the actual encoded byte limit, including JSON framing.
The measured population stays within the declared memory/replay budgets, so this
profile retains replayed in-memory state. Larger populations require additional
measurement before capacity claims are made.
