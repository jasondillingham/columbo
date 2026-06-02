# Reason harness plan — Columbo red-teams code, driven by a Claude Code session

Goal: point Columbo at a directory FROM a Claude Code session and have it
red-team the code — find the cross-file root-cause bugs the probe lanes can't.

## Architecture (decided): Columbo is the harness, the session is the reasoner

The frontier model is **the Claude Code session you're already in**, not
something Columbo calls. So there is NO Anthropic API client, no key, no token
budget inside Columbo. This is DESIGN's "Driven mode: model in the loop,"
realized as the control surface DESIGN deferred (`audit_start`/`audit_promote`).

Division of labor:

- **The session reasons** — reads the code (its own Read/Grep tools), finds
  candidate bugs, writes reproducers. Columbo does NOT feed it source; the
  session already has filesystem access.
- **Columbo is the structure** — it runs the deterministic lanes (L1/L2/L6) so
  the session doesn't re-derive the cheap stuff; ingests the session's
  findings; RUNS their reproducers to confirm-or-kill (the verification);
  dedups; and writes the audit-format round. Plus the round bookkeeping and
  the honesty discipline (severity scale, the firewall).

## The interface: active `columbo-mcp` tools (the session calls them)

`columbo-mcp` already exists (read-only `audit_status`/`audit_findings`). Add
the active reason tools; the session drives a round as a tool loop:

- `reason_start(dir, target?)` — begin a round against `dir`. If a target.yaml
  is given (or found), return its threat_model + run the deterministic lanes
  and include their findings; else just scope the dir. Returns: the
  audit-format + severity spec, recent commits, the deterministic findings so
  far, and a round handle. (The session reads the actual source itself.)
- `reason_record(finding)` — ingest a session-reasoned finding: title,
  severity, files, mechanism, and a reproducer (a Go test body, or a shell
  command). Returns a finding id. Held in the in-progress round (in-memory in
  the columbo-mcp process — it's long-lived per session).
- `reason_reproduce(id)` — RUN the recorded reproducer against the target;
  return pass/fail + output; mark the finding confirmed (reproduced) or not.
- `reason_finalize()` — dedup (incl. the deterministic-lane findings), assign
  IDs, write the `bughunt-N-*.md` round. Returns the paths + the rollup.

## The crux: confirmation by execution (the honesty firewall)

The session PROPOSES; Columbo's reproducer execution DISPOSES. A finding is
"confirmed" only if `reason_reproduce` actually demonstrates the bug. One that
has no runnable reproducer, or whose reproducer doesn't demonstrate it, is
`UNTRIAGED` (candidate for human review), never "confirmed." This is what keeps
the reason harness from degrading into "a list of an LLM's confident guesses"
— the failure mode that would betray Columbo's reproducer-producing ethos.

Expect a SPECTRUM: some findings get a running reproducer (confirmed), many get
only a refute-survived argument (UNTRIAGED). The confirmed:UNTRIAGED ratio is
the unknown that decides whether this is an autonomous confirmer or a
human-review assistant. We measure it, not assume it.

Reproducer execution runs model-written code (a Go test) against the operator's
own repo, locally, in the operator's own session — acceptable for a dev
red-team tool (it's running a test you're watching it write), but noted.

## Reproducer isolation (settle before writing the runner)

`reason_reproduce` runs model-written code. It must NOT exec in the long-lived
`columbo-mcp` process, and must NOT run against the live target repo:
- In-process exec means a reproducer that hangs/forks/writes affects the
  harness itself. Run it as a BOUNDED subprocess (timeout), the same discipline
  as the probe lanes / the F018 fix.
- Running in the live repo lets a reproducer that mutates state (writes
  `.leonard/`, touches files) corrupt the code being audited and poison every
  later finding. Run in a THROWAWAY isolate: `git worktree add --detach <tmp>`
  if the target is a git repo (cheap, no mutation to the live tree), else
  `cp -r`. Write the test into the isolate, `go test` there, discard the
  isolate. Reuses the build-to-tempdir instinct from k3srunner.

Reproducer convention: a Go test that **passes (exit 0) iff the bug is
present** (the session asserts the bug's symptom). reason_reproduce runs
`go test -run`; exit 0 -> confirmed, non-zero (incl. compile error) -> NOT
confirmed, with output returned so the session can iterate.

## Stateful tools must answer out-of-order calls cleanly

The caller is a model; it will call out of order. `record` before `start`,
`finalize` on an empty/all-UNTRIAGED round, `reproduce` an unknown id, a second
`start` without `finalize` — each gets a defined, non-panicking answer (clean
error). This is the L6 silent-drop/error-code discipline turned inward.
`finalize` refuses-or-warns on a round with no confirmed findings rather than
emit a hollow "clean" round. One test per out-of-order path.

## Reuse

`findings.Round` / writer / `reconcile` / the lanes already exist. The new code
is: the active MCP tools, the in-memory round state in `columbo-mcp`, and the
reproducer runner (write the test into a temp file in the target, `go test`,
capture).

## First slice: prove `record -> reproduce -> finalize`, NOT the lanes

One axis at a time. The minimal slice that answers "can the session find a real
bug and can Columbo confirm it by EXECUTION?" is the reproducer engine, not the
lane integration. So:

- Build `reason_start(dir)` MINIMAL — open a round + record the target dir.
  NO lane-running yet (that couples to all the lane machinery on the first run;
  fold it in slice 2).
- Build `reason_record`, `reason_reproduce` (the isolated runner — the crux),
  `reason_finalize`.
- Validate by driving them FROM this session against a leonard/bosun region
  **whose bugs have not been read** (avoid the author==auditor / rigged-demo
  trap): record a candidate with a Go-test reproducer, `reason_reproduce` it,
  and let EXECUTION be the judge — a real bug lands CONFIRMED (its reproducer
  exits 0); a wrong candidate's reproducer fails → not confirmed. Not my
  say-so; the test run.

Slice 2 (DONE): `reason_start` takes an optional `target` (a target.yaml path);
when given it loads the target and runs the deterministic lanes (L1/L2/L6) via
`internal/lanes`, attaches them with `Session.SetLaneFindings`, and `Finalize`
folds them into the round alongside the reasoned "Reason (driven review)" lane.
A lane-load failure is non-fatal (returned as `lanes_error`); the reason round
still runs. A round with only lane findings (no candidates) still finalizes; a
truly empty round (no candidates AND no lanes) is refused.

This is Columbo's Driven control surface (the `audit_start`/`audit_promote` half
DESIGN deferred), not "a lane": probe lanes (automated) + reason harness
(session-driven), one auditor.

## Non-goals / honesty

- Not SAST; not a bug-freeness guarantee; bounded to the dir/package scoped.
- Not autonomous reasoning — the session is the reasoner, by design.
- "Confirmed" means "reproducer ran and demonstrated it," nothing more.

## Open decisions for review

1. **In-memory round state in columbo-mcp** vs persisting to `.columbo/` — lean
   in-memory (one session, one round, ephemeral) for the first slice.
2. **Reproducer types** — start with Go-test bodies (run via `go test` in a
   temp file in the target) and shell commands; expand later.
3. **How the session is told to drive the loop** — a short skill/slash-command
   that documents the `reason_start -> record -> reproduce -> finalize` flow,
   or just the tool descriptions. Lean: tool descriptions first, skill later.
