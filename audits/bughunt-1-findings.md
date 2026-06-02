# Bughunt-1 — Findings rollup (Columbo self-audit)

**Round:** Bughunt #1 for columbo
**Date:** 2026-06-01
**Baseline:** see `bughunt-1-brief.md`

Severity scale (audits/README.md):
- **CRITICAL** — exploitable RCE, arbitrary file write, trust bypass
- **HIGH** — privilege boundary breach, DoS, secret leakage, state corruption
- **MEDIUM** — resource exhaustion within bounds, error-swallowing, weak validation
- **LOW** — quality, races without practical exploit, structural leakage, future-proofing

## Rollup

| ID | Severity | Area | Title | Status |
|---|---|---|---|---|
| F001 | **HIGH** | mcp client | Unbounded `ReadBytes` in `internal/probes/mcp/client.go` — a hostile target can OOM the auditor by flooding stdout with no newline. The exact F018 class Columbo hunts, in Columbo's own client. | fixed |
| F002 | LOW | cli / k3s | `auto --k3s` named lane Jobs `columbo-r0-*` regardless of round (`gatherReports` hardcoded round 0), so a round's Jobs didn't reflect their round number. | fixed |
| F003 | LOW | reconcile | Embedding dedup uses single-linkage, which can chain-merge a similarity gradient (A~B, B~C, A~C below threshold → all collapse) — an over-merge that hides a finding. | confirmed (mitigated) |
| F004 | LOW | lanes (L1) | L1 P3's binary-version check runs `go run`, which does NOT apply ldflags, so it is blind to a version injected only via `-X` (Columbo's own mechanism). | confirmed (deferred) |
| F005 | LOW | cli / autonomous | `auto` runs lanes then checks the clean tree, so a target whose `baseline.build` writes an artifact into the tree self-dirties and BLOCKS every time. | confirmed (documented) |
| F006 | LOW | k3s transport | The sentinel-over-logs findings transport was only cluster-verified at 1-finding volume; L2's ~27-finding block has never crossed `kubectl logs` on the cluster (egress flakiness blocked it). | open (unverified) |

**Round status: 6 findings (1 HIGH, 5 LOW). The HIGH is fixed in-round; F002
fixed; F003–F006 are LOW, recorded with rationale.** No open CRITICAL/HIGH.

---

## F001 — HIGH — Unbounded read in Columbo's own MCP client (the bug Columbo hunts)

**Files:** `internal/probes/mcp/client.go` (reader goroutine).

**Expected.** A bounded read: a frame with no newline must not grow the
reader's buffer without limit. (This is exactly what L6/F018 flags in *other*
servers; bosun's analogous server-side F018 was HIGH.)

**Observed (pre-fix).** The reader looped `r.ReadBytes('\n')` and only checked
`total > maxBytes` AFTER a full line returned. A target that floods stdout
without a newline never returns from `ReadBytes`, so bufio buffers without
bound until the process is killed at the deadline — within that window, RSS
grows unbounded. Since Columbo audits potentially-hostile targets, a malicious
target could OOM the auditor: a DoS of the tool itself.

**Reproducer.** Point the client at a server that writes >maxBytes with no
newline; pre-fix the reader accumulates it all. Post-fix, `readCappedLine`
caps each line and drains the overflow.

**Fix shape (applied this round).** Replaced `ReadBytes('\n')` with
`readCappedLine(r, maxBytes)` (mirrors the server's `readBoundedLine`): reads
at most `maxBytes` before a newline, drops + drains an over-cap line, never
buffers to OOM. Guarded by `TestReadCappedLineBounds`. The bug-hunter shipped
the bug it hunts; now closed.

## F002 — LOW — auto --k3s Job names ignored the round number

**Files:** `cmd/columbo/main.go` (`gatherReports`).

**Observed.** `gatherReports` called `k3srunner.RunLane(0, …)`, so `auto --k3s`
named Jobs `columbo-r0-l2` etc. regardless of the actual round (the `round`
command threaded `rRound` correctly; only the auto path was wrong). Not a hard
collision (each lane id differs, and RunLane deletes-then-creates), but the Job
name misrepresents the round.

**Fix shape (applied this round).** Resolve the round number before
`gatherReports` and thread it through to `RunLane`.

## F003 — LOW — Embedding dedup single-linkage can chain-merge

**Files:** `internal/reconcile/embed.go`.

**Observed.** `DedupEmbed` uses single-linkage clustering: a finding joins a
cluster if it is similar to ANY member. A similarity gradient (A~B≥t, B~C≥t,
A~C<t) chains all three into one finding — an over-merge, which for an auditor
hides a finding.

**Why LOW / mitigated, not fixed.** Embedding dedup is OPT-IN (`--dedup=embed`);
structural dedup is the conservative default precisely because of this. The
conservative threshold (0.88) limits chaining, and on targets with a clean
inter-class gap (leonard: 0.84 vs 0.896) it does not bridge. Documented in
`embed.go`; the chaining is reproduced and pinned by
`TestEmbedSingleLinkageChains`. Tightening to complete-linkage would under-merge
(the safe direction) but make embed worse than structural at its job; left as a
deliberate opt-in tradeoff.

## F004 — LOW — L1 P3 binary-version check is blind to ldflags

**Files:** `internal/lanes/l1.go` (P3), `docs/v0.2-deferred.md`.

**Observed.** P3 runs `version.command` via `go run`, which does not apply the
Makefile's `-ldflags -X`. For a target whose release version is injected via
ldflags (Columbo's own mechanism), P3 sees the source default, not the injected
value — so the binary-vs-build drift detector goes dark exactly where it would
matter at a release. P2 (source-const drift) still works.

**Why deferred.** Masked today (Columbo's source default `0.1.0-pre` equals the
injected value). Fix shape: build the real binary with the target's build flags
into a temp dir (the build-to-tempdir option v0.2 declined). Tracked in
`docs/v0.2-deferred.md`; required before a release-version self-audit.

## F005 — LOW — Lane build artifacts dirty the tree and block `auto`

**Files:** `cmd/columbo/main.go` (`auto`), `docs/v0.6-autonomous-plan.md`.

**Observed.** `auto` runs lanes then checks the clean-tree guardrail. If a
target's `baseline.build` writes a binary into the repo (e.g. single-package
`go build .`), the tree is dirty by the time the guard runs and `auto` BLOCKS
100% of the time — which reads as "auto is broken."

**Why LOW / documented.** The guard firing is correct (the tree IS dirty); the
constraint is on target authoring: lane builds must stay out of the tree
(multi-package `go build` discards, or `-o /tmp`). leonard's build is safe.
Documented in the autonomous plan.

## F006 — LOW — sentinel-over-logs unverified at finding-volume

**Files:** `internal/lanewire/`, `internal/k3srunner/`, `docs/v0.6-plan.md`.

**Observed.** The k3s findings transport (sentinel JSON over `kubectl logs`)
was cluster-verified only with a 1-finding L6 block. L2's ~27-finding block —
the volume the transport risk is actually about — has never crossed the
boundary on the cluster; attempts were blocked by intermittent cluster egress
to github.

**Why LOW / open.** Fail-closed (a truncated block fails `json.Unmarshal` →
the lane surfaces as `failed`, never silently-fewer) and ~15 KB (well under
containerd's ~10 MB log cap), so very likely fine — but "very likely" is not
"verified." This is an open verification gap, recorded honestly rather than
claimed closed. Re-run an L2 cluster round once egress is stable.
