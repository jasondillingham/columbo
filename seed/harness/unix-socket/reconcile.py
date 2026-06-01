#!/usr/bin/env python3
"""Reconcile parallel-agent ID collisions in bughunt-1 findings.

State on disk:
- L3 ran first, claimed F009-F017 (skipping F011 as positive result)
- L4 + L5 ran in parallel; each picked F009-F012/F013 (collision with L3)
- L6 ran after, took F018-F025
- L7 ran after, took F030-F039
- L8 ran last, took F050-F060 (no collision)
- master FINDINGS.md last-write-wins shows L1-L2 + somebody's F009-F012 + L6 + L7

Reconciliation plan:
- L3: keep F009-F017 (the rollup already shows L3's range via L3's first-write)
- L4: renumber F009→F040, F010→F041, F011→F042, F012→F043 (4 IDs)
- L5: renumber F009→F044, F010→F045, F011→F046, F012→F047, F013→F048 (5 IDs)
- L6: keep F018-F025
- L7: keep F030-F039
- L8: add F050-F060 to rollup

Outputs:
- L4-findings.md, L5-findings.md renumbered in place
- master findings/FINDINGS.md rebuilt with unified rollup + all rollup rows
  from each lane file (preserves L3/L6/L7/L8 unchanged; pulls renumbered
  L4/L5; rolls in F002-F008 detail sections from the current master)

Idempotent: re-running after a successful pass is a no-op for L4/L5
renumber (the F009-F013 range is empty in those files after renumber).
"""
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIND = ROOT / "findings"
MASTER = FIND / "FINDINGS.md"

L_FILES = {
    "L3": (FIND / "L3-findings.md", None),                       # no renumber
    "L4": (FIND / "L4-findings.md", {9:40, 10:41, 11:42, 12:43}), # collide → shift
    "L5": (FIND / "L5-findings.md", {9:44, 10:45, 11:46, 12:47, 13:48}),
    "L6": (FIND / "L6-findings.md", None),
    "L7": (FIND / "L7-findings.md", None),
    "L8": (FIND / "L8-findings.md", None),
}


def renumber(text, mapping):
    """Replace each F0NN where NN is a key in mapping with F0<new>."""
    def shift(match):
        n = int(match.group(1))
        if n in mapping:
            return f"F{mapping[n]:03d}"
        return match.group(0)
    return re.sub(r"\bF(\d{3})\b", shift, text)


def main():
    # 1) renumber L4 and L5 in place (idempotent: only fires if old IDs present)
    for name, (path, mapping) in L_FILES.items():
        if mapping is None or not path.exists():
            continue
        src = path.read_text()
        # Idempotence check: if none of the OLD IDs are present, skip
        old_pattern = "|".join(f"F{n:03d}" for n in mapping.keys())
        if not re.search(rf"\b({old_pattern})\b", src):
            print(f"{name}: already renumbered (idempotent skip)")
            continue
        new = renumber(src, mapping)
        path.write_text(new)
        shifts = ", ".join(f"F{o:03d}→F{n:03d}" for o, n in mapping.items())
        print(f"{name}: renumbered ({shifts})")

    # 2) extract rollup rows from each lane file
    def extract_rollup_rows(path):
        if not path.exists():
            return []
        rows = []
        for line in path.read_text().splitlines():
            m = re.match(r"^\|\s*\*{0,2}F(\d{3})", line)
            if m:
                rows.append((int(m.group(1)), line))
        return rows

    lane_rows = {}
    for name, (path, _) in L_FILES.items():
        lane_rows[name] = extract_rollup_rows(path)
        print(f"{name}: {len(lane_rows[name])} rollup rows")

    # 3) preserve F001-F008 rollup rows from current master (they live nowhere else)
    master_src = MASTER.read_text()
    base_rows = []
    for line in master_src.splitlines():
        m = re.match(r"^\|\s*\*{0,2}F(\d{3})", line)
        if m and int(m.group(1)) <= 8:
            base_rows.append((int(m.group(1)), line))
    print(f"L1-L2 base: {len(base_rows)} rows preserved from master")

    # 4) build the unified rollup
    all_rows = base_rows + [r for rows in lane_rows.values() for r in rows]
    # Dedup by ID (highest-priority source wins; later lanes overwrite earlier — but
    # by design L4/L5 are renumbered out of any conflict, so dedup is mostly safe).
    by_id = {}
    for nid, line in all_rows:
        by_id[nid] = line
    sorted_rows = sorted(by_id.items())

    # 5) severity tally for round status
    def sev(line):
        m = re.match(r"^\|\s*\*{0,2}F\d{3}\*{0,2}\s*\|\s*\*{0,2}([A-Z/]+)", line)
        return m.group(1) if m else "?"
    sev_count = {}
    for _, line in sorted_rows:
        s = sev(line)
        sev_count[s] = sev_count.get(s, 0) + 1
    print(f"severity tally: {sev_count}")

    # 6) rebuild master FINDINGS.md
    header = """# Bughunt-1 — Findings rollup

**Round:** Bughunt #1 for bosun (establishing audits/ convention)
**Started:** 2026-05-28
**Baseline:** see `bughunt-1-brief.md`

> **Reading note.** In `runlog/*.md`, `PASS` means *"the session didn't time out and the call returned exit 0"* — it does NOT mean "the server gave the correct answer." Cross-check the per-call response in the runlog for any test where correctness matters more than liveness.

Severity scale:
- **CRITICAL** — exploitable RCE / arbitrary file write / trust bypass
- **HIGH** — privilege boundary breach, DoS crashing the daemon, secret leakage, trust bypass
- **MEDIUM** — resource exhaustion within bounds, error-swallowing that masks problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
"""

    rollup_body = "\n".join(line for _, line in sorted_rows) + "\n"

    # Round status with the renumbering note
    total = len(sorted_rows)
    crit = sev_count.get("CRITICAL", 0)
    high = sev_count.get("HIGH", 0)
    med = sev_count.get("MEDIUM", 0)
    low = sev_count.get("LOW", 0)
    other = total - crit - high - med - low

    footer = f"""
(Lane runs append rows as findings surface — see `runlog/` for full traces.)

---

## Round status

**Bughunt-1 substantively COMPLETE across all 8 designed lanes (L1–L8).**

| Lane | Sub-tests | New findings | Highest severity |
|---|---:|---:|---|
| L1 (build invariants) | 9 | 1 | LOW |
| L2 (MCP cap edges) | ~30 | 7 (F002–F008) | MEDIUM (F007) |
| L3 (spawn / worktree) | 31+ | 8 (F009-F010, F012-F017) | **HIGH** (F009 — spawn broken on default install) |
| L4 (lock / ledger / daemon) | 13 | 4 (F040-F043) | MEDIUM (F041 — `rm` sock orphans daemon) |
| L5 (`bosun serve` HTTP) | ~15 | 5 (F044-F048) | **HIGH** (F044 — DNS rebinding leaks secrets to malicious tab) |
| L6 (MCP protocol fuzz) | 30+ | 8 (F018-F025) | **HIGH** (F018 — unbounded `bufio.ReadBytes` pins arbitrary RSS) |
| L7 (real dogfood) | 12 | 10 (F030-F039) | **HIGH** (F032 — `bosun merge` exit 0 on conflict; F038 — underscore-name silent orphan) |
| L8 (cross-platform / Windows) | 2 runtime + 9 source-audit | 11 (F050-F060) | MEDIUM |

**Findings total: {total}.** Severity mix: **{crit} CRITICAL, {high} HIGH, {med} MEDIUM, {low} LOW**{f' (+ {other} other)' if other else ''}.

**Highest-ROI fix order:**

1. **L3 F009 (HIGH)** — `bosun_spawn` liveness gate computes wrong worktree path (`roundTimestamp=""`) so every scheme-C session (the v0.11+ default) **cannot spawn**. Complete feature outage on default install. `bosun status`'s `git worktree list`-based resolver is the correct fix model.
2. **L5 F044 (HIGH)** — DNS rebinding via no Host-header validation; confirmed cross-origin leak of `BOSUN_BRIEF.md` (including planted `AWS_SECRET=...`). Bundle C's 4 headers don't defend; needs a Host gate.
3. **L7 F032 (HIGH)** — `bosun merge` exits 0 on conflict (CI scripts get green status on a wedged repo). Two-line fix.
4. **L6 F018 (HIGH)** — Unbounded `bufio.Reader.ReadBytes('\\n')` at `transport.go:51` — one connection can pin arbitrary RSS in the daemon. Two-line fix (bounded reader + read deadline).
5. **L7 F038 (HIGH)** — `bosun init session-<word>` creates a fully-functional worktree that `list/status/show/remove/doctor` treat as nonexistent. `session.Derive:232` excludes via `strconv.Atoi`. Silent orphan.
6. **F001 + L7 F037** — stale version constants (`internal/mcp/server.go:42` "0.2.0-alpha"; `cmd/bosun/cmd_debug.go:126` `"dev"`). Same fix class. ~5-line ldflags extension.
7. **F007 + L8 F057** — Bundle E PID-validation gap (non-PID-1 invalid PIDs accepted; Windows PID 4 same shape). Extend hard-refuse list.
8. **L6 cluster (F019, F021, F025)** — JSON-RPC spec compliance: `-32700` on parse error, BOM frames, batch frames. One helper closes all three plus L2's F004/F005/F006.

**Per-lane source files** (audit-format, ready to promote to `bosun/audits/bughunt-1-*.md`):
`findings/L3-findings.md`, `L4-findings.md`, `L5-findings.md`, `L6-findings.md`, `L7-findings.md`, `L8-findings.md`.

**Reusable harness for future bughunts** lives at `/tmp/bosun-redteam/harness/` (mcp_sock.py with the L4-discovered adaptive-timeout patch; rt.sh logging helpers; verdict.py, summarize.py, reconcile.py).
"""

    new_master = header + rollup_body + footer
    MASTER.write_text(new_master)
    print(f"\nmaster FINDINGS.md rebuilt: {len(new_master)} bytes, {total} findings")


if __name__ == "__main__":
    main()
