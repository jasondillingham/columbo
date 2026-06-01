# Columbo design

The adversarial auditor in the Leonard/bosun/Columbo family. This is the
authoritative scope document.

## What it does

You point Columbo at a target tool. Columbo runs structured red-team lanes
against the target's surface, files findings in audit-format under the
target's `audits/` directory, and reports back what's worth fixing.

The job is "poke other tools to break them so the operator can improve
them." Driven mode walks lanes with a human in the loop. Autonomous mode
runs the whole round overnight on k3s.

## The five abstractions

### Target

The thing being audited. Has:

- A **surface**: MCP stdio, MCP unix socket, MCP TCP, HTTP server, CLI
  binary, Go library.
- A **baseline**: git SHA, version, test-suite state at audit time.
- A **threat model**: seeded from recent commits + the target's CLAUDE.md;
  operator can edit.
- An **audit history**: prior rounds, so the next round knows what's
  already been covered and can declare what new lens it's bringing.

Lives in `target.yaml` at the target repo's `audits/` directory (or
elsewhere if the operator prefers). Operator-supplied, not sniffed. Forcing
the operator to write the threat model down is the point.

### Lane

An audit lens. Caps. Concurrency. Schema. Protocol. UX. Cross-platform.
Each lane is a directory of probes plus a brief that names what surface
this lens covers.

The eight lanes Leonard and bosun both used:

| Lane | Lens |
|---|---|
| L1 | build invariants (version, ldflags, baseline tests) |
| L2 | MCP / API input caps |
| L3 | state correctness across edits / writes |
| L4 | concurrency, lockfile, daemon lifecycle |
| L5 | the public-facing surface (HTTP, RPC) |
| L6 | protocol-level fuzz |
| L7 | real dogfood (use the tool the way an operator would) |
| L8 | cross-platform |

The catalog is open. Different targets surface different lanes; the eight
above are a strong default starting set.

### Probe

A single parametric adversarial test. Has:

- Input (templated, fuzzed, LLM-generated, or fixture).
- An expected behavior contract.
- Observed behavior (captured).
- A verdict: PASS, FAIL, or FINDING.
- A reproducer that survives the run (audit-format).

About 60% of probes are generic. Cap-edges, JSON-RPC framing, concurrent
bursts, schema-violation handling. The other 40% are target-specific and
live alongside the target's `target.yaml`.

The generic library ships with Columbo. The target-specific 40% accumulates
per-target over time, ideally with Claude drafting templates from prior
findings and the operator merging.

### Finding

What gets recorded. Has:

- ID, severity (CRITICAL / HIGH / MEDIUM / LOW), lane, status.
- Files (repo-relative paths into the target).
- Reproducer (runnable verbatim).
- Why this severity (the assessment).
- Fix shape (smallest change that closes the bug).
- Discovered (date + lane).

Same audit-format Leonard and bosun's audits/ dirs use. The finding format
is the deliverable; everything else is in service of producing those files.

### Round

A campaign with N lanes, eventual reconciliation, and a numbered
audit-format output. Each round leaves:

- `audits/bughunt-N-brief.md` — scope + harness baseline.
- `audits/bughunt-N-<lane>.md` — per substantive lane.
- `audits/bughunt-N-findings.md` — consolidated rollup.

Numbering is per-target: Leonard's bughunt-12, bosun's bughunt-1,
Columbo's own bughunt-1.

## Two operator modes

### Driven

Human at the keyboard kicks off lanes one at a time, watches findings land,
makes calls on what to dig into next, reconciles at the end. Model in the
loop. The way both Leonard's bughunt-12 and bosun's bughunt-1 ran.

Best when:

- The target is unfamiliar.
- The threat model is still being learned.
- The operator wants to see the shape of findings before committing the
  rest of the round.

### Autonomous

Operator kicks it off, walks away, wakes up to a PR-ready audit branch with
reproducers and per-lane writeups. No model in the loop for the boring
parts.

Best when:

- The threat model is stable.
- The operator is running round N+1 to extend coverage.
- The lanes are well-curated for this target.

Same tool, two cadences. Autonomous mode needs guardrails Driven gets for
free from human attention: when to stop, when to ask for help, when a
severity call needs a human.

## Where Thor + k3s + local models actually pull weight

### k3s jobs as the lane runner

Each lane is a containerized job pinned to a cluster node, its own
sandbox, its own target clone. Eight lanes equals eight pods instead of
three batches of subagents fighting over `/tmp/`. Better parallelism,
better isolation, free retry-on-failure.

Reconciliation runs as a final job pulling findings from a shared volume
(or object storage).

### Local model on Thor (Ollama + ROCm on the RX 6700 XT)

Pulls weight on:

- **Probe generation.** Give an LLM the target's input schema, ask for
  adversarial JSON. 7B-class on the 6700 XT handles this fine.
- **Finding deduplication.** Sentence-transformer embeddings beat the
  regex-and-prayer reconciler we wrote by hand for Leonard and bosun.
- **Threat-model extraction.** Recent-commit summarization with an
  audit-friendly prompt. Standard summarization scope.

### Does not pull weight on

The main "find the bug" reasoning. F020 on Leonard required synthesizing
"the prune sweep enumerates via `Store.ListFiles`," "`ListFiles` got
hardened to cap at 1000," and "what about the other callers of
`ListFiles`," across three files separated in time by a security review.
That cross-file synthesis stays a frontier model job. Local models will
get the cap-edge probes right and miss the cross-referenced root causes.
The architecture splits those jobs deliberately.

## The MCP query surface

`columbo-mcp` is for observing and controlling, not running probes.

Tools (planned):

- `audit_status(target_id)`: "bughunt-3, lane L4 running, 7 findings
  recorded, ETA 14 min."
- `audit_findings(target_id, since=ts)`: deltas.
- `audit_start(target_id, lanes=[...])`: kick a round.
- `audit_promote(target_id, branch="audit/bughunt-N")`: copy findings to
  the target's `audits/` and prepare the commit on the audit branch.

That way Claude (or the operator from inside a regular dev session) can
query the auditor without opening a separate shell. The probes themselves
stay out of the MCP surface because they're chatty and budget-heavy.

## What this is not

- **Not a fuzzer.** Fuzzers cover libraries with structured inputs.
  Columbo covers long-running daemons with behavioral contracts.
- **Not a chaos engineering tool.** Chaos tools test resilience to
  failure. Columbo tests "does the thing do what it claims?"
- **Not a SAST scanner.** Pattern-matching on source doesn't catch the
  bug classes Columbo is built for. F020 was a query-cap interaction
  across three files separated by a security review; no pattern matcher
  would find that.

## Self-dogfood

Columbo audits Columbo. The first internal milestone is "columbo can run
bughunt-1 against columbo." That's the proof-of-life that matters. Leonard
is self-dogfooded. Bosun is shipped by shipping bosun with bosun. If
Columbo can't find bugs in its own probe library, it isn't earning its
keep.

## Repo shape

```
columbo/
├── cmd/
│   ├── columbo/             # main CLI
│   └── columbo-mcp/         # stdio MCP observe-and-control surface
├── internal/
│   ├── findings/            # audit-format read/write, severity tally
│   ├── lanes/               # lane runner
│   ├── orchestrator/        # parallel-agent / k3s job orchestration
│   ├── probes/
│   │   ├── caps/            # cap-edge probes (generic)
│   │   ├── concurrency/     # concurrent burst, race detection
│   │   ├── lifecycle/       # daemon socket lifecycle, signals
│   │   ├── protocol/        # JSON-RPC spec compliance
│   │   └── schema/          # input schema violation probes
│   ├── reconcile/           # ID-collision merger for parallel agents
│   ├── target/              # target.yaml loader + schema
│   └── version/             # single source of truth
├── audits/                  # Columbo's own audits (self-dogfood)
├── docs/
├── examples/
└── Makefile
```

## Milestones (rough, not committed)

1. **v0.1.** Folder + binaries scaffolded, version discipline in place.
   *(Current.)*
2. **v0.2.** Target loader (`target.yaml` parse) + one runnable lane (L1,
   build invariants) end-to-end against a Leonard or bosun checkout.
3. **v0.3.** Three lanes (L1 + L2 + L6, the cheapest three) + the
   findings/ writer producing valid audit-format markdown.
4. **v0.4.** Reconciliation + parallel lane orchestration locally.
5. **v0.5.** `columbo-mcp` stdio surface with `audit_status` and
   `audit_findings`.
6. **v0.6.** k3s job-based lane runner; first autonomous round.
7. **v0.7.** Local-model integration on Thor for probe generation + finding
   dedup.
8. **v1.0.** Self-audit (bughunt-1 against columbo) clean.

Each minor bump leaves an internal `docs/v0.N-plan.md` per bosun's pattern.
