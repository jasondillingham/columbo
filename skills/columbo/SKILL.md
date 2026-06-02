---
name: columbo
description: Red-team a code directory by driving Columbo's reason tools (reason_start, reason_record, reason_reproduce, reason_finalize) over the columbo-mcp MCP server. You read the code and propose bugs; Columbo confirms each one by EXECUTING a reproducer in an isolated copy, so only demonstrated bugs are "confirmed." Use when asked to "red-team", "audit", "find bugs in", or "point Columbo at" a directory or repo. Targets are Go codebases (reproducers are Go tests).
---

# Columbo: red-team a directory

You are the reasoner. Columbo is the harness. You read the code and find
candidate bugs; Columbo holds the round and CONFIRMS each finding by running its
reproducer in an isolated copy of the target. The firewall is the whole point:
you propose, execution disposes. A finding is "confirmed" only when its
reproducer actually demonstrates the bug. Anything you record but cannot
reproduce finalizes as UNTRIAGED, never as a confirmed severity.

"Just one more thing..." The value you add is the bug you can prove, not the bug
you can describe.

## Before you start

The `reason_start` / `reason_record` / `reason_reproduce` / `reason_finalize`
tools come from the **columbo-mcp** MCP server. If you don't see them, the
server isn't wired into this session. Tell the user to add it to their MCP
config (`{"command": "columbo-mcp"}`) and reconnect, then stop. Do not try to
fake the loop with shell commands.

The target must be a Go codebase (reproducers are Go tests run with `go test`).
The repo should be a git repo for clean isolation (Columbo runs each reproducer
in a `git worktree`; it falls back to a copy for non-git dirs).

## The loop

1. **`reason_start`** — open the round.
   - Always pass `dir` (the target repo/dir root).
   - Pass `target` (a path to a `target.yaml`) when one exists for this code.
     When you do, Columbo runs the deterministic lanes first (L1 version-drift,
     L2 input caps, L6 protocol handling) and folds their findings into the
     round. The response's `deterministic_lanes` tells you what already ran.
     **Do not re-hunt those classes** — spend your reasoning on what a probe
     can't reach (see "What to hunt"). If no `target.yaml` exists, you are the
     only auditor; cover those classes too.

2. **Hunt** — read the code and find real bugs (see below). This is the work.

3. **`reason_record`** — for each candidate, with a reproducer.
   - `title`, `severity` (CRITICAL|HIGH|MEDIUM|LOW, or omit for UNTRIAGED),
     `files`, `mechanism` (how the bug works), `expected` (correct behavior),
     `fix_shape`.
   - `repro_pkgdir` — the package dir (repo-relative) the test goes in.
   - `repro_run` — the test function name.
   - `repro_file` — a complete `_test.go` file (package decl + imports + func)
     that **PASSES (exit 0) iff the bug is present**. Assert the bug's symptom,
     not its absence. (Worked example below.)

4. **`reason_reproduce`** — run candidate `<id>`'s reproducer in isolation.
   `status: confirmed` means exit 0, the bug is demonstrated. Anything else
   means it is not confirmed. **Reproduce every candidate you record** — an
   unreproduced candidate finalizes as UNTRIAGED, the same as a refuted one. If
   a reproducer refutes your hypothesis, that is the system working; drop the
   claim or fix the reproducer if it was wrong, don't argue with the run.

5. **`reason_finalize`** — write the round as `bughunt-N-*.md` under
   `<dir>/audits`. Confirmed findings keep their severity; unconfirmed ones are
   written UNTRIAGED. Report the written files and the audits path to the user.

## What to hunt

A probe can grep for a pattern. Your edge is reading logic across functions and
files. Aim there:

- **Cross-file invariants.** A value validated in one place and trusted in
  another; a cap enforced on one path but not its sibling; a "can't happen"
  comment that a second caller makes happen.
- **State machines and ordering.** Operations that assume a sequence (open
  before write, lock before read) but accept calls out of order. Idempotency:
  does calling a thing twice corrupt state or double-count?
- **Error paths.** The half of the code tests skip. A swallowed error that
  leaves a half-built object; a cleanup that doesn't run on the failure branch;
  a wrong-type input silently coerced to a default (the silent-accept class)
  instead of rejected.
- **Resource lifecycle.** Leaks (a file/conn/goroutine opened on a path that
  never closes it), use-after-close, double-close.
- **Boundary and parsing logic.** Off-by-one on slice/index math; a length read
  from input and trusted; integer truncation or sign assumptions.

When a `target.yaml` ran the lanes, version-drift, input caps, and protocol
handling are already covered. Reaching for those again wastes the round. Read
for the semantic bugs the probes structurally cannot see.

Read with adversarial, fresh eyes. If you wrote this code, you will read past
the bug; assume every "obviously correct" block is where it hides.

## Writing a reproducer (the crux)

The reproducer is a Go test that passes exactly when the bug is live. Make the
assertion BE the bug.

Suppose `pkg.Sum(a, b int)` returns `a`, ignoring `b`. The reproducer asserts
the buggy behavior, so it passes today and would fail once fixed:

```go
package pkg

import "testing"

func TestSumIgnoresB(t *testing.T) {
	if Sum(2, 3) != 2 { // bug: ignores b, so 2+3 yields 2
		t.Fatal("bug absent: Sum no longer ignores its second argument")
	}
}
```

Record it with `repro_pkgdir: "pkg"`, `repro_run: "TestSumIgnoresB"`,
`repro_file:` the text above. `reason_reproduce` runs
`go test ./pkg -run TestSumIgnoresB` in an isolated worktree; exit 0 confirms.

Guidelines:
- Import and call the real package. Don't reimplement the logic in the test.
- One bug per reproducer. If you're asserting two things, you have two findings.
- If the bug needs setup (a temp dir, a built store), do it in the test; the
  isolate is a full copy of the repo, so relative paths and fixtures work.
- If you can't write a test that demonstrates it, you don't yet understand the
  bug well enough to claim it. Record it as UNTRIAGED (omit severity) or keep
  reading.

## Honesty and non-goals

- "Confirmed" means a reproducer ran and demonstrated the bug. Nothing more.
- This is not SAST and not a proof of bug-freeness. It is bounded to the code
  you read and the reproducers you wrote.
- Go targets only, for now (reproducers are Go tests).
- You are the reasoner by design; Columbo does not reason. Don't expect it to
  find bugs for you. It finds out whether yours are real.
