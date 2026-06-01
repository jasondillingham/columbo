# CLAUDE.md — operating instructions for Columbo

If you're a Claude Code session working on the Columbo codebase, read this
before you start.

## What you're building

Columbo is the adversarial auditor in the Leonard/bosun/Columbo family.
**Read [`DESIGN.md`](./DESIGN.md) first** for the five abstractions (Target,
Lane, Probe, Finding, Round) and the two operator modes (Driven vs
Autonomous). That document is authoritative for scope.

## Version discipline (closing a bug we've already shipped twice)

The version string lives in **one place**: [`internal/version/version.go`](./internal/version/version.go).
The Makefile injects build-time overrides via `-X` ldflags. Both `cmd/columbo`
and `cmd/columbo-mcp` import from `internal/version` and print whatever's
there.

Do not duplicate the version constant into command packages. Leonard's
bughunt-12 F001 and bosun's bughunt-1 F001 were both this exact class of
bug, and the second time was less funny than the first. We are not going to
ship the same finding from this repo.

## Scope discipline

Same rule as bosun's CLAUDE.md: do not extend into v0.2+ work just because
it would only take a few hours. If you're tempted, write the idea down in
`docs/v0.2-deferred.md` and keep moving.

Concrete examples of drift this rule is designed to catch:

- Adding a TUI before the CLI works end-to-end.
- Adding "intelligent probe selection" before the probe library has more
  than a dozen probes.
- Auto-generating `target.yaml` from the target's source before any human
  has hand-written one.
- Adding more lanes than Leonard + bosun's eight before any lane has shipped.

## The audits/ directory

Columbo audits itself. The convention is established in
[`audits/README.md`](./audits/README.md) from day zero so the first round
has a place to land. Every round leaves a brief, per-lane findings files,
and a consolidated rollup. Same shape as `leonard/audits/` and
`bosun/audits/`.

## Voice notes for contributed prose

Plain words. Worked examples over abstract labels. Concrete numbers carry
arguments better than adjectives. Self-undercutting hedges are fine; tidy
résumé-narration ("I built X, then Y, then Z, and it worked perfectly") is
not. If a country idiom genuinely fits, use it; if it's decorative, drop it.

No em-dashes. Use periods, commas, or parentheses.
