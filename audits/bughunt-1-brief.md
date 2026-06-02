# Bughunt-1 — Brief (Columbo audits Columbo)

**Target:** columbo (self-audit)
**Date:** 2026-06-01
**Baseline:** v0.7 HEAD plus this round's in-round fixes (F001, F002).
**Mode:** Driven (Claude in the loop), per DESIGN — the way Leonard's
bughunt-12 and bosun's bughunt-1 ran.

## Scope and the auditor's conflict of interest (stated plainly)

This is a self-audit: the author, the auditor, and the model are the same. That
is the weakest possible auditor — across Columbo's own development, nearly every
*reasoning*-class issue was caught by an outside reviewer, not by self-review,
while the issues the author reliably caught alone were the ones found by
*running code*. So this round leads with execution, not armchair review, and is
honest about what it does NOT establish.

**What bughunt-1 claims:** these findings are triaged; the serious one is fixed
in-round. **What it does NOT claim:** that Columbo is bug-free. A clean
self-audit closes this round's findings; it does not prove the absence of bugs,
especially the cross-file reasoning bugs a single self-reviewing context
under-finds.

## Two sources

1. **Automated lanes** (L1 build, L2 caps, L6 protocol) against
   `examples/columbo.target.yaml` (the columbo-mcp surface): **16 PASS, 0
   FINDING.** Expected — this surface was the v0.5 discrimination test, and the
   one real surface bug (parseRound silent-accept) was already closed in v0.5.
   The lanes finding nothing here is the point: the machine half can't reach
   the codebase.
2. **Driven review / execution** against the adversarial surfaces (the MCP
   client transport headline) plus the issues surfaced during development. This
   is where bughunt-1's findings come from.

## Result

6 findings (see `bughunt-1-findings.md`): 1 HIGH, 5 LOW. The HIGH (F001 —
unbounded read in Columbo's own MCP client, the bug Columbo hunts) is FIXED in
this round; F002 (cosmetic Job naming) is also fixed. F003–F006 are
LOW/observability/open-verification, recorded with rationale rather than forced
to a fake "fixed."

Lanes were clean, so there are no per-lane finding files; the consolidated
`bughunt-1-findings.md` holds the round.
