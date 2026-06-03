# Bughunt-3 — Findings rollup

**Round:** Bughunt #3 for columbo
**Date:** 2026-06-03
**Baseline:** see `bughunt-3-brief.md`

Severity scale:
- **CRITICAL** — exploitable RCE, arbitrary file write, trust bypass
- **HIGH** — privilege boundary breach, DoS, secret leakage, state corruption that survives recovery
- **MEDIUM** — resource exhaustion within bounds, error-swallowing that masks problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage, future-proofing

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
| F001 | LOW |  | L6 silent-drop grading: any non-JSON stdout line counts as "responded", so a server that logs to stdout while dropping a malformed frame is graded PASS instead of FINDING (masks the silent-drop class) | confirmed |
| F002 | LOW |  | L6 `errorCode` mislabels a code-absent JSON-RPC error as "code:0": an error object with no `code` field is reported as the jsonrpc-code-zero class ("error code 0"), a misdiagnosis | confirmed |

## Round status

Total findings: 2  (CRITICAL 0, HIGH 0, MEDIUM 0, LOW 2, UNTRIAGED 0)

- Reason (driven review) — 2 finding(s)
