# Bughunt-2 — Findings rollup

**Round:** Bughunt #2 for columbo
**Date:** 2026-06-02
**Baseline:** see `bughunt-2-brief.md`

Severity scale:
- **CRITICAL** — exploitable RCE, arbitrary file write, trust bypass
- **HIGH** — privilege boundary breach, DoS, secret leakage, state corruption that survives recovery
- **MEDIUM** — resource exhaustion within bounds, error-swallowing that masks problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage, future-proofing

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
| F001 | MEDIUM |  | Execution firewall confirms a "hollow" reproducer: `go test -run <name>` exits 0 when the name matches no test, so a reproducer whose repro_run mismatches its func is marked CONFIRMED without demonstrating anything | confirmed |
| F002 | LOW |  | L1 source-drift `readSymbolValue` returns the first regex match, so a `Symbol = "..."` inside a comment shadows the real const — producing false version-drift findings (or masking real ones) | confirmed |

## Round status

Total findings: 2  (CRITICAL 0, HIGH 0, MEDIUM 1, LOW 1, UNTRIAGED 0)

- Reason (driven review) — 2 finding(s)
