# Pipeline: redteam

Goal: read-only security assessment — recon, vuln analysis, exploit validation, report — then
remediate confirmed vulnerabilities.

```yaml
budget: { usd: 6, wall_ms: 2400000 }
constraints: [ "no harmful payloads; PoC only", "read-only except remediate" ]
allowed_capabilities: [recon, vuln-analysis, exploit-validation, redteam-report,
  redteam-remediate]
success_criteria: [ "a report exists; confirmed vulns remediated or documented as accepted risk" ]
checkpoints: [after:redteam-report]
```

## Stages

1. **recon** — map endpoints, inputs, trust boundaries. (read-only)
2. **vuln-analysis** — hypothesize + confirm vulnerabilities. (read-only)
3. **exploit-validation** — safely validate exploitability with a non-harmful PoC. (read-only)
4. **redteam-report** — compile findings + remediation guidance. **Checkpoint**. On FIX,
   `insert` **redteam-remediate**.
5. **redteam-remediate** — fix confirmed vulns. On FIX, `retry` **vuln-analysis** to re-confirm;
   on PASS, proceed.

## Notes

- The original `on_result: { FIX: { remediate, on_result: { FIX: jump:vuln-analysis } } }`
  becomes: orchestrator `insert`s remediate on FIX, then `retry`s vuln-analysis to re-verify.
