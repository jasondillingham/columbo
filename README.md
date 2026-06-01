# Columbo

> *"Just one more thing..."*

Adversarial auditor for code that's already shipped. Walks in rumpled, asks
dumb-seeming questions, finds the inconsistency nobody else noticed, hands
you back a fix shape.

Sibling to [Leonard](https://github.com/jasondillingham/leonard) (ground-truth
keeper) and [bosun](https://github.com/jasondillingham/bosun) (work crew).
Three small focused tools, three different professional archetypes.

## Status

**Alpha, through v0.6.** A working MCP-server audit harness: it runs three
adversarial lanes against a target's MCP surface, collapses duplicate findings,
and writes audit-format markdown. Proven against real targets (leonard, bosun,
itself). It is **not** yet the cross-file bug-reasoning auditor `DESIGN.md` aims
at; see "What it does *not* do."

See [`STATUS.md`](./STATUS.md) for exactly where things are,
[`DESIGN.md`](./DESIGN.md) for the shape, [`docs/`](./docs/) for the per-version
plans, and [`audits/README.md`](./audits/README.md) for the format convention.

## What it does today

You point Columbo at a `target.yaml` (where the repo is, how to build it, where
the version constant lives, what MCP surface it exposes). Then:

- **`columbo target validate <target.yaml>`** — load + validate a target.
- **`columbo audit l1 <target>`** — build invariants: source vs binary vs
  wire (`serverInfo`) version drift, baseline tests.
- **`columbo audit l2 <target>`** — input caps: fire malformed arguments
  (INT64-max, null, wrong type, oversized, unknown fields) at every MCP tool;
  flag findings when the server leaks Go internals or panics.
- **`columbo audit l6 <target>`** — protocol fuzz: broken JSON-RPC frames
  (truncated, BOM, deeply nested, batch) and error-code compliance.
- **`columbo audit round <target> [--k3s]`** — run all three lanes (locally in
  parallel, or as k3s Jobs), reconcile duplicate findings, and write a
  `bughunt-N-*.md` set (rollup + per-lane + brief).
- **`columbo audit auto <target>`** — the autonomous round: run, apply
  guardrails (block on a failed lane or dirty tree; escalate HIGH/UNTRIAGED to
  a human), and commit the round to a `audit/bughunt-N` git branch in the
  target repo. No push; you review and open the PR.
- **`columbo-mcp`** — a read-only MCP server exposing `audit_status` /
  `audit_findings`, so a Claude session can query the last round without a
  shell.

It never invents a severity it can't justify; those land as `UNTRIAGED` for a
human. Against leonard it consistently surfaces real, reproducible findings
(Go-internal error leaks, JSON-RPC `code:0`) that match leonard's own audit
history; against the spec-correct `columbo-mcp` it's clean, so the lanes
discriminate.

## What it does *not* do (yet)

- It audits **MCP servers** (and Go build hygiene), not arbitrary code. The
  hard "synthesize a root cause across files separated by a security review"
  reasoning `DESIGN.md` names as the real goal is **not built** — that stays a
  frontier-model job.
- Only **3 of the 8** planned lanes exist (L1, L2, L6).
- Probes are **fixed/templated**, not LLM-generated. No local-model
  integration yet (planned for v0.7: probe generation + embedding dedup on
  Thor).
- Only pointed at leonard, bosun, and itself so far.

## The family

- **Leonard.** Symbol index, decision log, claim ledger. Keeps Claude Code's
  overconfident theorizing tethered to reality.
- **bosun.** Coordinates parallel Claude Code sessions on isolated git
  worktrees.
- **Columbo.** Asks the dumb-seeming question until the inconsistency falls
  out. The tool that audits Leonard, bosun, and itself.

## Self-dogfood

Columbo audits Columbo. The first internal milestone is "columbo can run
bughunt-1 against columbo." That's the proof-of-life that matters; Leonard
does the same, and bosun ships by shipping bosun with bosun.

## Build

```sh
make build      # builds columbo + columbo-mcp into the repo root
make install    # go install both into $GOPATH/bin
./columbo version
```

## License

Apache 2.0. See [`LICENSE`](./LICENSE).
