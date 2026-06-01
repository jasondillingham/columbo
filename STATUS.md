# Columbo — current state

Last touched: 2026-06-01. Picks up from a session that scaffolded the repo
from zero. Same pattern as bosun's `STATUS.md`: where things are, what's
next, anything at risk.

## What's in this repo right now

- Folder structure scaffolded. All `internal/` subdirectories carry
  `.gitkeep` files so they survive the eventual `git init`.
- Two binaries (`cmd/columbo`, `cmd/columbo-mcp`) compile clean as stubs.
  `columbo` prints help and supports `columbo version`. `columbo-mcp`
  prints "not yet implemented" to stderr and exits 1.
- **Single-source version constant** at `internal/version/version.go`.
  Both binaries print the same string. Makefile injects via `-X` ldflags.
  F001 class closed before the first finding could land.
- Apache 2.0 LICENSE (copied from Leonard), Makefile, .gitignore, README,
  CLAUDE.md, DESIGN.md, audits/README.md all in place.
- **v0.2 landed (2026-06-01): target loader + L1-static, end-to-end.**
  - `internal/target/` parses `target.yaml` (schema models all four Target
    parts; only the L1 subset is consumed, rest is parse-only by design).
    Unit-tested (validate happy/error paths, repo-path resolution).
  - `examples/leonard.target.yaml` + `examples/bosun.target.yaml`, both
    hand-written against real source HEAD. Bosun is the shape-check: its
    version is dynamic (ldflags git-describe) with a separate `ServerVersion`
    const, which forced `version.expected/command` to be optional and added
    per-site `expect`. Leonard's is a pinned const duplicated in two files.
  - `internal/lanes/l1.go` runs L1-static: P1 build, P2 source-const drift,
    P3 built-binary version (via `go run`, SKIP when version is dynamic),
    P4 baseline tests. Emits plain PASS/FAIL/FINDING/SKIP + tally, no
    markdown (that's v0.3). Unit-tested (symbol extractor, tally).
  - CLI: `columbo target validate <yaml>` and `columbo audit l1 <yaml>`
    (exits 2 when the lane has any FINDING/FAIL, for CI gating).
  - Plan pinned in `docs/v0.2-plan.md` before coding.
- `projectdogwalker/` (the red-team sandbox that became Columbo) sits in-tree
  as local-only seed material: harness, fixtures, findings, runlog. It is
  gitignored (`/projectdogwalker/`), so it won't be committed. Two things
  guard against it leaking into the build or getting lost:
  - It carries its own `projectdogwalker/go.mod`, making it a separate nested
    module. That boundary keeps `go vet ./...` from descending into its
    intentionally-malformed fixtures (`fixtures/edge/empty.go`,
    `fixtures/symlinks/escapes_root.go`), which otherwise breaks `make check`.
  - The reusable harness scripts are mirrored into `seed/harness/` (tracked),
    so ignoring the sandbox loses no seed material. See the table below.

## v0.3 in progress — Gate A landed (2026-06-01)

`docs/v0.3-plan.md` splits v0.3 into Gate A (the findings writer, the
milestone's named deliverable, reachable with zero MCP work) and Gate B (the
MCP stdio client + L2 + L6). **Gate A is done:**

- `internal/findings/` — `Finding` + `Severity` types and the audit-format
  writer. Emits brief / per-lane / consolidated `bughunt-N-*.md`, matching
  Leonard/bosun format (rollup table, **bold** HIGH/CRITICAL, severity scale,
  contracts-re-verified, per-finding detail). Round-trip tested: the rollup
  table re-parses to the same rows, including a pipe- and backtick-laden
  Title (cell escaping is reversible). The per-lane detail block (Files /
  Expected / Observed / Reproducer) is asserted too. Write-once per
  `(round, lane)`; `--force` overwrites (no merge — that's v0.4 reconcile).
- L1 now emits enriched `Finding` records (the F001 drift class declares
  `LOW`; everything else would default to `UNTRIAGED`, never a guess).
- `columbo audit l1 <target> --write [--round N] [--out dir] [--force]`
  runs the lane and writes the audit set (default `<target repo>/audits`).
- Proven end-to-end: clean Leonard writes a 3-file scaffold (0 findings);
  a drift target pushes F001-F003 through to valid, re-parseable markdown.

**Gate B in progress — MCP stdio client landed (step 1 of 5):**

- `internal/probes/mcp/client.go` — one-shot-session-per-probe stdio
  JSON-RPC client. Each call spawns a fresh server, optionally handshakes,
  writes an ordered list of `Action`s to stdin, drains stdout with the ported
  adaptive deadline (full wait before first byte; short idle grace only after
  data AND after all writes are sent), caps at 16 MiB, never closes the write
  side early. API: `List`, `Call`, `Raw` (newline-framed), `RawActions` (exact
  bytes / multi-write, for split-frame and no-newline fuzz).
- Tested against a pure-Go fake server (the `os.Args[0]` helper-process
  pattern, stdout kept byte-clean so no test-runner noise corrupts the JSON
  stream): handshake, echo, the adaptive slow-response path, silent-drop
  (missing id), total-silence deadline, and split-frame reassembly.
- Verified end-to-end against the real `leonard-mcp` (built fresh to a temp
  dir): `tools/list` returns all 12 Leonard tools. (Throwaway test, removed
  to keep the suite portable.)

**Gate B — L2 caps landed (step 2 of 5):**

- `internal/probes/caps/` — schema-driven, target-agnostic probe generation:
  per integer field INT64-max + string-for-int; per string field null +
  int-for-string + oversized (1 MiB); per array field `[null]`; per tool an
  unknown-field probe when `additionalProperties:false`. Plus `LeaksInternals`
  (the FINDING signal) and `Panicked`.
- The leak detector is deliberately tight: it flags only unambiguous Go
  internals (`json: cannot unmarshal`, `overflows`, `reflect.Value`,
  `Go struct field`), NOT bare `has type ... want ...`. Leonard's JSON-Schema
  validator legitimately says the latter; flagging it would be a false
  positive. Locked by a test. (This honesty pass was the real work here.)
- `internal/lanes/l2.go` — builds the mcp-stdio server ONCE to a temp dir
  (new `surface.build` field, e.g. `./cmd/leonard-mcp`), lists tools, fires
  the battery one fresh process per probe, generous 8s per-probe timeout.
  FINDING on leak (LOW, F002/F003 class) or server panic (HIGH, DoS); clean
  rejections are re-verified PASS. SKIPs if the target has no mcp-stdio.
- `columbo audit l2 <target> [--write ...]`, sharing the writer plumbing.
- Verified end-to-end against real `leonard-mcp`: **68 PASS, 27 FINDING, 0
  FAIL**. The 27 collapse to two genuine leak classes — `string=null` ->
  `<invalid reflect.Value>` (F003) and `int=INT64-max` -> `json: cannot
  unmarshal ... overflows into Go struct field` (F002), the exact portable
  class Leonard/bosun already documented. `--write` emits a valid 27-row
  rollup. (The heavy duplication across tools is real; dedup is v0.4
  reconcile, not v0.3.)
- The verdict mapping is a pure `classifyL2(surface, probe, session) Result`
  with its own test (leak->LOW, panic->HIGH, clean->PASS, panic-beats-leak) —
  the panic->HIGH branch never fires against Leonard, so the test is its only
  coverage. This `classify`-and-test shape is the pattern L6 will reuse.
- L2 shipped at ~half its planned scope: leak/panic only, NOT the silent-
  accept (unenforced-cap) half, which needs per-field intent. Recorded in
  `docs/v0.3-plan.md`. Verified no hidden finding hides in PASS:
  find_symbol/verify_symbol enforce their documented 4096-byte cap.

**Gate B — L6 protocol fuzz landed (step 3 of 5):**

- `internal/probes/protocol/` — the fixed fuzz battery (target-agnostic):
  truncated JSON, UTF-8 BOM prefix, deeply-nested JSON, JSON-RPC batch array
  (the F019/F021/F023/F025 silent-drop class), plus unknown-method and
  unknown-tool (the F004 error-code class). Frames sent raw after a real
  handshake via the client's `RawActions`.
- `internal/lanes/l6.go` — `RunL6` + a pure, tested `classifyL6` (same shape
  as classifyL2): silent-drop -> MEDIUM, JSON-RPC `code:0` -> LOW, server
  panic -> HIGH; a proper error response (any nonzero code) -> re-verified
  PASS. SKIPs with no mcp-stdio surface.
- `columbo audit l6 <target> [--write ...]`.
- Verified end-to-end against real `leonard-mcp`: **5 PASS, 1 FINDING**. The
  finding is real and specific: unknown *method* returns `code:0` (F004
  class) while unknown *tool* correctly uses `-32602`. The PASSes are honest,
  not a weak grader — confirmed by raw inspection that Leonard returns a
  proper `-32700` for truncated JSON and `-32600` for a batch, i.e. it
  handles the exact frames bosun silently dropped. Leonard's transport is
  more robust than bosun's was.
- `internal/probes/protocol` has a data-sanity test; the BOM frame is written
  with the `\ufeff` escape (a literal BOM byte breaks the Go parser).

- Fixture correction: `bosun mcp` is unix-socket-only (verified), so the
  bosun fixture's MCP surface is now `kind: mcp-unix` (not `mcp-stdio`). L2/L6
  SKIP cleanly against bosun (exit 0, no hang). Consequence: L6's silent-drop
  FINDING path has no stdio target that exhibits it (Leonard handles those
  frames), so that branch is synthetic-test-only in v0.3; re-run L6 against
  bosun once unix-socket support lands in v0.4. Both caveats are in
  `docs/v0.3-plan.md`.

**Gate B — L1 P5 live serverInfo landed (step 4 of 5):**

- `mcp.Client.ServerInfo()` returns the handshake's reported name/version.
- `internal/lanes/l1.go` gains P5: build the mcp-stdio server, read
  `serverInfo.version` over the wire, compare to `version.expected`. Pure
  `classifyServerInfo` (tested: drift -> LOW, match -> PASS); SKIP with no
  mcp-stdio surface or no pinned version. This is the deferred half of seed L1
  (the wire check; P3 stays the binary check). Does NOT close the ldflags-P3
  gap.
- All three branches verified end-to-end: Leonard PASS (`serverInfo.version`
  = 0.54.0, matches binary — no wire drift), bosun SKIP (mcp-unix), and a
  drift target FINDING (0.54.0 != 9.9.9). Leonard L1 is now 6 PASS.

**Gate B — `audit round` landed (step 5 of 5): v0.3 COMPLETE.**

- `columbo audit round <target> [--write --round N --out --force]` runs
  L1+L2+L6, prints each lane, assembles all three `LaneReport`s into one
  `findings.Round`, and writes the full set. `writeRound` is now variadic so
  the single-lane commands and the round share one path.
- Verified end-to-end against real `leonard-mcp`: **79 PASS, 28 FINDING, 0
  FAIL, 0 SKIP**. Wrote 5 files (brief + 3 per-lane + consolidated). The
  consolidated rollup spans lanes with F001-F028 numbered across them (27
  caps + 1 protocol; L1 clean), brief lists all three lanes with counts, the
  0-finding L1 file renders a valid empty rollup plus its 6 re-verified
  contracts (incl. P5), round status reads `28 LOW`.

- Integrity fix (caught at the finale): `lanes.Report` used to DROP SKIP/FAIL,
  so a lane that never ran rendered as `0 finding(s)` in the brief —
  indistinguishable from a clean pass. A `round` against bosun (mcp-unix)
  would have claimed L2/L6 ran clean. Now `LaneReport` carries `Skipped` /
  `Failed`, and ALL THREE rendered surfaces — brief, per-lane files, and the
  consolidated `bughunt-N-findings.md` Round status (per-lane lines, per
  `audits/README.md`) — render `SKIPPED: <reason>` / `FAILED to run: <reason>`.
  Verified end-to-end: a bosun-shaped round reports L2/L6 SKIPPED while L1
  (which did run) shows `0 finding(s)`. `Report` and all three renders are
  tested (the mappings that weren't). This is the silent-coverage-gap class
  Columbo exists to catch, closed in Columbo's own output.

v0.3 is done: the findings writer (Gate A) plus three lanes on a stdio MCP
client (Gate B), producing valid audit-format markdown end-to-end. Deferrals
recorded in `docs/v0.3-plan.md` (L2 silent-accept, L6 spec-compliance depth,
unix-socket surfaces) and `docs/v0.2-deferred.md` (ldflags-P3, F018 in the
client). v0.4 per DESIGN: reconciliation + parallel orchestration (and the
finding dedup these 28-with-heavy-duplication results clearly motivate).

## v0.4 COMPLETE — parallel orchestration + reconciliation (2026-06-01)

`docs/v0.4-plan.md`. Scope call: the seed reconcile.py solves ID *collisions*
(self-numbering agents), which Columbo doesn't have locally (central
`AssignIDs`) — deferred to v0.6. The locally-valuable reconcile is structural
finding dedup (the v0.3 round's 28 findings are really ~3 classes). DESIGN
files *embedding* dedup at v0.7 and calls the hand-written one the baseline
embeddings "beat", so structural dedup now is the documented baseline.

- `internal/orchestrator/` — generic `RunParallel[T]` goroutine fan-out,
  results in input order regardless of finish order. `audit round` now runs
  L1+L2+L6 concurrently. Tested (barrier proves true concurrency; order
  preserved; `-race` clean).
- `internal/reconcile/` — `Dedup` collapses findings sharing a
  `(Severity, Class)` into one that lists every `Locus`; keeps the
  representative's reproducer verbatim, unions Files, caps the locus list with
  "+K more". Empty-Class and singletons pass through. Tested.
- `findings.Finding` gains `Class` (dedup key) + `Locus` (site); caps/protocol/
  L1 probes set them (classify tests assert propagation).
- `columbo audit round` orchestrates -> dedups per lane -> writes. `--raw`
  opt-out emits every instance. Prints "reconciled: N raw -> M", and the
  consolidated doc itself discloses "Reconciled from N raw probe instances
  (re-run with --raw...)" so the written artifact never hides its own dedup.
- **Determinism bug the diff-gate caught:** `caps.Generate` iterated a map
  (random order), so F-numbering reshuffled every run even sequentially. Now
  sorts property names; an audit tool's output must be stable/diffable. Tested.
- Verified end-to-end against real Leonard: two parallel `--raw` runs are
  byte-identical (deterministic), set-identical to the sequential baseline
  (parallelism safe — no DB-lock errors against the shared `.leonard/` store,
  confirming the write-probes stay read-only), and the default run collapses
  **28 raw findings -> 3** (20 null-string reflect leaks, 7 INT64 overflow
  leaks, 1 code:0), coherent across per-lane + consolidated docs.

## v0.5 COMPLETE — columbo-mcp observe surface (2026-06-01)

`docs/v0.5-plan.md`. Milestone = the two READ tools; `audit_start`/
`audit_promote` (control) deferred to v0.6.

- `internal/query/` — reads the written `bughunt-N-findings.md` rounds (the
  markdown is the source of truth; no `.columbo/` store). `Rounds`,
  `Findings(dir, round)` (0=latest, via `findings.ParseRollup`), `Summarize`
  (severity tally + per-lane status + reconciled flag). Tested against
  writer-produced fixtures.
- `internal/mcpserver/` — small hand-rolled stdio JSON-RPC server (no SDK
  dep). Deliberately correct on the classes Columbo hunts: nonzero error codes
  (-32700/-32601/-32602), a parse-error RESPONSE not a silent drop (F019), and
  a bounded reader so a no-newline flood can't OOM it (F018). Tool registry.
  Tested incl. every error class + the bounded reader.
- `cmd/columbo-mcp` — replaces the stub: serves `audit_status` (last completed
  round summary; NOT live — no running audit until the control surface) and
  `audit_findings` (rollup-level; reproducers live in per-lane files, disclosed
  in the tool description). `--audits <dir>` flag.
- **Dogfood loop (kept, portable):** `cmd/columbo-mcp/main_test.go` drives
  columbo-mcp with Columbo's OWN client (`internal/probes/mcp`) — builds the
  server from source, points it at a fixture round, verifies tools/list +
  audit_findings (F001) + audit_status (total/reconciled).
- **Discrimination test (the honest claim):** `examples/columbo.target.yaml`.
  The SAME L6 lane PASSes columbo-mcp (6/6, built to spec) AND FINDs
  leonard-mcp's `code:0` (1 finding). That distinguishes a correct server from
  a buggy one — it is NOT "columbo-mcp passes its own L6 so it's correct"
  (server + probes share an author, so they share blind spots; L6-green here is
  a consistency check). The independent proof that L6 catches real bugs is the
  leonard-mcp `code:0` catch. L2 vs columbo-mcp is also clean (cap probes ->
  clean errors, no leaks).
- Self-caught bug fixed before shipping: `parseRound` first swallowed a
  wrong-TYPE round (`{round:"3"}`) and silently returned the latest round — the
  silent-accept class Columbo hunts, in Columbo's own server (the empty-audits
  dir had masked it from L2). Now rejected with a CLEAN error (not the raw
  json.Unmarshal text, which would leak Go internals — the F002/F003 class).
  Dogfood test asserts all three: errors, not silent; no F001 returned; no Go
  internals. Closed before it could surface at the v1.0 self-audit.

## v0.6 IN PROGRESS — k3s job-based lane runner (live on homelab) (2026-06-01)

`docs/v0.6-plan.md`. Operator chose full live k3s. Built and **proven on the
real cluster** (macmini + thor):

- Scope correction (honest): the seed's "ID-collision merger" is OBVIATED, not
  built. It existed because manual agent sessions self-numbered F0NN. Engineered
  lane pods emit ID-LESS findings; the orchestrator assigns F001.. centrally
  (existing `AssignIDs`) after collecting all lanes. No collision, no merger.
- `internal/lanewire/` — pods emit their LaneReport as a sentinel-delimited
  JSON block on stdout; the orchestrator extracts it from `kubectl logs`. This
  carries findings over logs, sidestepping the cluster's missing shared PV.
- `columbo audit lane <l1|l2|l6> <target>` — single-lane pod entrypoint
  (progress to stderr, sentinel block to stdout). `--lanes` selects lanes.
- `internal/k3srunner/` — renders a Job per lane (imagePullPolicy:Never,
  backoffLimit:0, restartPolicy:Never), applies via kubectl, waits for complete
  OR failed (never hangs on a crash), collects logs, extracts the report; a
  failed/timed-out Job becomes a `Failed` LaneReport (honest, not vanished).
- `columbo audit round --k3s --lanes l2,l6 ...` runs each lane as a Job.
- Image: `deploy/` — static amd64 binaries (built on Mac) -> `columbo:slim`
  (alpine + binaries + examples) built on the homelab docker host -> `ctr`
  import to both nodes (no registry). `deploy/build-and-import.sh`.
- **Verified end-to-end on the cluster:** (1) `columbo version` Job (import +
  schedule); (2) hand-run L6 Job vs in-image columbo-mcp -> known-good 6 PASS,
  0 findings over logs; (3) `audit round --k3s --lanes l2,l6` -> two concurrent
  Jobs -> collected -> wrote a clean round (L2 5 reverified, L6 6 reverified, 0
  findings). The local-testable parts (lanewire round-trip, RenderJob) are in
  `make check`.

**Slice 2 DONE — external target via in-pod clone:** `clone`/`setup` added to
the target schema; `columbo audit lane --workdir` clones the target, checks out
`baseline.sha`, runs setup (build + `leonard init`), then probes. Image
`columbo:build` = `golang:1.25-alpine` + git (debian golang's git links GnuTLS
which fails SNI to github from the cluster; Alpine's git clones cleanly — a real
bug found and fixed by running it). `examples/leonard-cluster.target.yaml` pins
a github SHA so cluster and local clone the same code. **Verified on cluster:**
the L6 pod cloned leonard from github, built it, `leonard init`-ed, probed
leonard-mcp -> 5 PASS, 1 FINDING (the code:0 F004 class). Diff-gate: the cluster
finding is set-identical (Title+Class) to a local clone-path run at the same
SHA.

**Honest scope on the sentinel-over-logs VOLUME claim (corrected):** only a
1-finding L6 block has crossed the boundary on the cluster. L2's ~27-finding
block (the volume case the transport risk is actually about) is NOT yet
cluster-verified. It is fail-closed (a truncated block fails `json.Unmarshal`
in `Extract` -> the lane surfaces as `failed`, never as silently-fewer
findings) and ~15 KB (well under containerd's ~10 MB log cap), so it will very
likely pass — but "very likely" is not "verified." Attempts to run the L2
cluster round were blocked by intermittent cluster egress to github (git clone
failing with TLS "unrecognized name" / GnuTLS handshake — environmental, NOT a
columbo bug; the same image cloned fine minutes earlier). Operational caveat
for the runner: in-pod clone depends on cluster egress to the target's git
host; a production runner wants clone retries or a pre-fetched target cache.

**v0.6 k3s job runner: DONE** (slice 1 in-image + slice 2 clone-based, both
proven on the real cluster). Local-testable parts in `make check` (13 pkgs).
Deploy tooling in `deploy/` (Dockerfiles, build-and-import.sh).

## v0.6 second half DONE — the first autonomous round (2026-06-01)

`docs/v0.6-autonomous-plan.md`. `columbo audit auto <target>`: kick off ->
round -> guardrails -> PR-ready local audit branch -> wake-up summary. The
guardrails are the feature (they replace the human attention a Driven round
gets for free).

- `internal/autonomous/` — `Check(reports)` is pure: **BLOCK** (don't commit a
  misleading round) on any failed lane or empty round; **ESCALATE** (flag, but
  still produce the branch) on UNTRIAGED or HIGH/CRITICAL findings.
  `CommitMessage`/`Summary` render escalations ("REVIEW NEEDED", "these need
  you") — tested with synthetic HIGH+UNTRIAGED, since leonard's all-LOW round
  can't exercise that path.
- `Promote` (git, no push): require clean tree (else block), capture current
  branch, `checkout -b audit/bughunt-N`, write the round on the branch
  (NEVER pre-write on main — clean-tree guarantee), commit, return to the
  original branch. `NextRound` is **branch-aware** — max(working-tree rounds,
  existing `audit/bughunt-*` branches)+1 — or the second run would recompute
  the same N and collide (tested with a real second run).
- `audit auto [--k3s] [--round] [--lanes]`; auto always reconciles + discloses.
- Verified end-to-end through the command against a controlled git target, all
  three paths: BLOCKED on a dirty tree (no commit; the dirtiness was a real
  catch — a lane's build artifact polluting the tree), clean proceed ->
  `audit/bughunt-1` committed + returned to main, and a branch-aware second
  run -> `audit/bughunt-2` (no collision). Promote's git ops are also unit-
  tested against a temp repo.

**v0.6 COMPLETE** — k3s job runner (first half) + autonomous round (second
half), both proven. `make check` green (16 packages, 14 with tests).

Two known-incomplete edges (record, not blocking): (1) `auto`'s dirty-tree
guard runs lanes THEN checks the tree, so a target whose `baseline.build`
emits an in-tree artifact self-dirties and blocks 100% of the time — lane
builds must stay out of the target tree (multi-package `go build` discards, or
`-o /tmp`); leonard's build is multi-package so it's safe, but a future
target.yaml author will hit this. Documented in the autonomous plan. (2)
`auto --k3s` is wired but unrun; `gatherReports` hardcodes the Job round number
to 0 (`columbo-r0-*`), so it doesn't thread the real round into Job names yet —
fix before any real cluster-auto run.

## v0.7 COMPLETE — local-model integration on Thor (2026-06-01)

`docs/v0.7-plan.md`. Ollama on thor (RX 6700 XT) was already up;
`qwen2.5-coder:7b` (gen) + `nomic-embed-text` (embed, 768-dim) confirmed live.
The discipline shift: LLM output is nondeterministic, so verification moved
from the byte-diff gate to STRUCTURAL properties, and **the model is opt-in —
defaults stay deterministic** (fixed probes + structural dedup), so `make
check` and committed rounds are unchanged without the flags.

- `internal/ollama/` — runtime HTTP client (`Generate`, `Embed`); generic
  `localhost` default (no homelab IP in source); timeout + fail-OPEN.
  Unit-tested against httptest; no `make check` test touches thor.
- **Embedding dedup** (`reconcile.DedupEmbed`, `--dedup=embed`): single-linkage
  cosine clustering within severity, conservative threshold 0.88 (loss is
  asymmetric — over-merge HIDES findings, so lean high). Gate test proves the
  differentiator leonard can't show: it SEPARATES distinct findings that share
  a Class (where structural over-merges) and merges near-identical. Live on
  leonard L2: 27 -> 2 clusters, matching structural (within-class cosine
  ~0.896, cross-class ~0.82-0.84, threshold sits in the gap). Falls back to
  structural on any embed error.
- **LLM probe generation** (`caps.GenerateLLM`, `audit l2 --llm-probes N`):
  prompts a coder model with each tool's schema for adversarial JSON args.
  Honesty firewall — the model PROPOSES, the deterministic `classifyL2`
  DISPOSES; concrete args travel as the reproducer; Class left empty so
  structural won't over-merge distinct LLM findings. Live on leonard: valid
  schema-aware probes ran + classified (some PASS, some FINDING). Honest
  result: they hit the SAME leak classes the fixed battery already covers — no
  novel class surfaced on leonard. Additive; fixed battery always runs; fails
  open.

**v0.7 done.** `make check` green (incl. `internal/ollama`); defaults unchanged
without the flags.

**Next (toward v1.0 = self-audit clean):** embedding dedup / LLM probes inside
k3s pods; threat-model extraction (the third local-model use); clone-retry for
cluster egress; the `auto --k3s` round-number fix; a real `auto` run against
the live leonard checkout (writes a branch in the operator's repo — needs a
go-ahead).

## Verified clean as of last touch

- `make build` produces both binaries (~5.4 MB each) into the repo root.
- `make check` passes (vet + `go test ./...`; `internal/target`,
  `internal/lanes`, and `internal/findings` have real tests).
- Version flows from `internal/version` through ldflags into both binaries
  and prints matching values.
- `columbo audit l1 examples/leonard.target.yaml` runs end-to-end against the
  real Leonard checkout: 5 PASS, exit 0.
- `columbo audit l1 examples/bosun.target.yaml` runs end-to-end against the
  real bosun checkout: P1/P2 PASS, P3 correctly SKIP (dynamic version), P4
  FAIL. The P4 FAIL is a real result, not a Columbo bug: bosun's
  `TestScenario_DoctorFixReapsCorruption` fails to bind a Unix socket because
  the sandbox temp path exceeds the ~104-char `sun_path` limit. L1 faithfully
  reported a genuinely red baseline. (Likely environmental, not a bosun bug.)

## Not yet done (deliberate)

- ~~No `git init` run yet.~~ Repo initialized 2026-06-01 (commit on `main`,
  initial commit through v0.6). No remote yet — local only. (Earlier
  per-version "nothing committed" notes below are historical.)
- `internal/{orchestrator,probes,reconcile}` are still empty stubs awaiting
  their packages (`internal/findings` landed in v0.3 Gate A).
- `audits/` has the README but no rounds. Columbo's own first round needs
  more than L1 (it'd find almost nothing yet).

## Natural next step (v0.3 from DESIGN.md milestones)

v0.3 is *three lanes (L1 + L2 + L6, the cheapest three) + the findings/
writer producing valid audit-format markdown.* Two sub-tracks, roughly
independent:

1. **The audit-format findings writer** (`internal/findings/`). Right now L1
   prints plain PASS/FAIL/FINDING. v0.3 turns FINDINGs into the same
   `audits/bughunt-N-*.md` shape Leonard and bosun use. This is the
   deliverable Columbo exists to produce; do it before adding more lanes.
2. **L2 (input caps) and L6 (protocol fuzz).** Both need the MCP stdio
   client that v0.2 deliberately skipped. Port the handshake from
   `seed/harness/unix-socket/mcp_sock.py` (the reference impl with the
   adaptive-recv-timeout patch) into a shared `internal/probes` client.
   That client is also what unlocks the deferred **live serverInfo drift
   probe** for L1 (binary version vs `serverInfo`), the half of the seed L1
   v0.2 left out on purpose.

See `docs/v0.2-plan.md` for what shipped and the explicit deferral list.
Write `docs/v0.3-plan.md` before coding, same as v0.2.

## Where the rest of the session's work lives

| Artifact | Location | State |
|---|---|---|
| Leonard bughunt-12 | `leonard/audits/bughunt-12-*.md` on branch `audit/bughunt-12` (commit `b10644a`) | committed, local-only |
| Bosun bughunt-1 | `bosun/audits/bughunt-1-*.md` on branch `audit/bughunt-1` (commit `81433e6`) | committed, local-only |
| Leonard F020 blog draft | `personal-site/drafts/leonard-f020.md` | written, parked behind `live_date: 2099-12-31` |
| Reusable harness (stdio) | `seed/harness/stdio/` | committed seed. `mcp.py`, `hook.py`, `gen_fixtures.py`, `reconcile.py`, plus `lanes/`. Mirrored out of `projectdogwalker/harness/` on 2026-06-01 (the sandbox itself is now gitignored, so this is the tracked copy). |
| Reusable harness (unix socket) | `seed/harness/unix-socket/` | committed seed. `mcp_sock.py` (the L4-discovered adaptive-recv-timeout patch), `reconcile.py`, and all 8 lane scripts `L1`-`L8`. Mirrored out of the ephemeral `/tmp/bosun-redteam/harness/` on 2026-06-01. Distinct from the stdio harness above (this one has `mcp_sock.py`; that one doesn't). |

Both harnesses are seed material for Columbo's `internal/probes/` and
`internal/reconcile/` packages.

## Open decisions parked

- Push `audit/bughunt-12` and `audit/bughunt-1` branches? Both local-only.
  No PRs opened.
- Backfill the rounds 7-11 entries in `leonard/audits/README.md`? Flagged
  during bughunt-12 promotion; not done.
- Em-dash pass on `personal-site/drafts/bosun-bughunt-1-five-highs.md`
  (had 34 em-dashes per voice rules). Operator said he was building a
  system for that and to leave it be.

## Reading order for the next session

1. This file.
2. [`DESIGN.md`](./DESIGN.md) for the five abstractions and the milestone list.
3. [`CLAUDE.md`](./CLAUDE.md) for the discipline rules (scope, version, voice).
4. [`audits/README.md`](./audits/README.md) for the audit-format convention
   Columbo's first round will follow.
