# Lane Reason (driven review)

**Lane:** reason (bughunt-2)
**Date:** 2026-06-02
**Baseline:** columbo ``
**Target:** columbo
**Status:** 2 finding(s)

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

## Contracts re-verified (no findings)

(none recorded)

---

## F001 — MEDIUM — Execution firewall confirms a "hollow" reproducer: `go test -run <name>` exits 0 when the name matches no test, so a reproducer whose repro_run mismatches its func is marked CONFIRMED without demonstrating anything

**Files:**
- `internal/reason/runner.go`
- `internal/reason/reason.go`

**Expected.** A reproducer is "confirmed" only if a test actually ran and passed. runReproducer should require positive evidence the named test executed (e.g. run with -v and require a `--- PASS: <Run>` line for the named test, or treat `no tests to run` / zero RUN lines as a non-confirmation), not merely exit 0.

**Observed.** runReproducer runs `go test ./PkgDir -run Run -count=1` and treats exit 0 (runErr==nil) as `passed` -> Confirmed. But `go test -run X` exits 0 whenever the package compiles, EVEN IF X matches no test function (it prints `[no tests to run]` and exits 0). So if the session's repro_run does not exactly match the func name in repro_file (a typo, a renamed func, a t.Skip(), or a build-tag-excluded test), zero assertions run yet the candidate finalizes as `confirmed`. The same hole accepts a `-run` regex that selects a DIFFERENT, already-passing test. The firewall's central claim ("confirmed means a reproducer ran and demonstrated the bug") is defeated: exit 0 is trusted as "the bug assertion ran and passed" when it actually only means "the toolchain did not error."

**Reproducer.**

```
go test ./internal/reason -run TestFirewallConfirmsHollowRun
```

**Fix shape.** In runReproducer, run `go test -v -run Run` and parse the output: confirm only if it shows the named test ran and passed (`=== RUN   <Run>` followed by `--- PASS: <Run>`), and refute (passed=false) if the output contains `no tests to run` or shows no RUN line for Run. Equivalently, fail-closed on any run where the bug-assertion test did not demonstrably execute. This closes the typo/skip/wrong-regex/build-tag variants, not just the literal no-match case.

---

## F002 — LOW — L1 source-drift `readSymbolValue` returns the first regex match, so a `Symbol = "..."` inside a comment shadows the real const — producing false version-drift findings (or masking real ones)

**Files:**
- `internal/lanes/l1.go`

**Expected.** The probe should read the value bound to the actual Go symbol (the const/var declaration), ignoring matches inside comments or unrelated text. A comment mentioning the symbol must not change the reported value.

**Observed.** readSymbolValue extracts a version literal with the regex `\bSymbol\s*=\s*"([^"]*)"` and returns the FIRST submatch in the file. A comment line that mentions the symbol (e.g. `// historical: Version = \"0.0.1\"`) appearing before the real `const Version = \"1.2.3\"` is matched first, so the probe reads the stale commented value. L1's P2 source-drift probe then either reports a FALSE version-drift finding (got the comment's value, not the const) or MASKS a real drift (if the commented value happens to equal the expected one). Either way the auditor emits a wrong verdict about the target.

**Reproducer.**

```
go test ./internal/lanes -run TestReadSymbolValueMatchesComment
```

**Fix shape.** Parse the file with go/parser + go/ast and read the declared const/var's value (a BasicLit) for the named symbol, instead of a line-agnostic regex over the raw bytes. That also drops matches in comments and string literals for free.
