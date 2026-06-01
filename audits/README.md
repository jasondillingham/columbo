# Audits

Adversarial-audit history for Columbo. The convention mirrors
[Leonard's `audits/`](https://github.com/jasondillingham/leonard/tree/main/audits)
and bosun's `audits/`.

Columbo is meant to be self-dogfooded. Columbo audits Columbo. Every
CRITICAL and HIGH finding referenced here is expected to be closed before
the next minor version tag.

## Index

(Empty until the first round runs.)

## How rounds are structured

1. **Brief.** `bughunt-N-brief.md`. Scope, harness baseline, lane
   partition, prior-round context.
2. **Per-lane findings.** `bughunt-N-<lane>.md` for each substantive lane.
   Severity-tagged findings with reproducer, observed vs expected, fix
   shape.
3. **Consolidated findings.** `bughunt-N-findings.md`. Single doc with the
   rollup table plus per-finding detail sections, in ID order.
4. **Round status.** Embedded section in the consolidated doc. Total
   findings, severity mix, highest-ROI fix order, lane-by-lane sub-test
   counts.

## Severity scale

- **CRITICAL.** Exploitable RCE, arbitrary file write, trust bypass.
- **HIGH.** Privilege boundary breach, DoS, secret leakage, state
  corruption that survives recovery commands.
- **MEDIUM.** Resource exhaustion within bounds, error-swallowing that
  masks problems, weak input validation, silent-accept of inputs that
  should be refused.
- **LOW.** Quality, races without practical exploit paths, structural
  leakage, future-proofing.

## Promotion criteria

A finding is "promoted" once it has:

1. A reproducer that runs verbatim against the sandbox the round used.
2. Source-file references via repo-relative paths.
3. A fix shape sized to the scope-discipline rule from CLAUDE.md (smallest
   change that closes the bug).
