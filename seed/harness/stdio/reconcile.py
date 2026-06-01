#!/usr/bin/env python3
"""Reconcile parallel-agent ID collisions in findings/FINDINGS.md.

Three lanes (L6, L7, L8) ran in parallel and each independently picked
starting ID F021. L6 wrote F021-F030, L7 wrote F021-F034, L8 wrote F031-F033.
The master rollup got the last-writer-wins versions of L6 (F021-F030) and L8
(F031-F033). L7's 14 findings are orphaned in `findings/L7-findings.md` with
colliding IDs.

This script:
  1. Renumbers L7's findings file in place: F021..F034 → F034..F047
  2. Rebuilds findings/FINDINGS.md from scratch:
     - Frontmatter preserved verbatim
     - Unified rollup table with F001..F047 in order
     - F001-F020 detail sections preserved verbatim
     - L4 GREEN-LANE entry preserved
     - Open observations preserved
     - L6 detail sections appended (extracted from L6 file)
     - L7 detail sections appended (extracted from renumbered L7 file)
     - L8 detail sections appended (extracted from L8 file)
     - Round status updated to count L6/L7/L8

Re-runnable: re-running this script after a successful run is a no-op
(L7 renumbering only fires on the F021-F034 range, which is empty after
the first run; rebuild always regenerates the master from current per-lane).
"""
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
FIND = ROOT / "findings"
MASTER = FIND / "FINDINGS.md"
L6 = FIND / "L6-findings.md"
L7 = FIND / "L7-findings.md"
L8 = FIND / "L8-findings.md"

# --- 1. Renumber L7 in place: F021..F034 → F034..F047 (shift +13) -------

def renumber_l7():
    """Apply +13 shift to L7's own F021..F034 range only.

    Cross-references to F001-F020 (existing findings) are unaffected because
    the regex only matches IDs >= F021. F035+ ranges shouldn't exist in L7
    yet, so they're also untouched.
    """
    if not L7.exists():
        return False
    src = L7.read_text()

    # Quick idempotence check: if there's no F021 in the file, we already ran.
    if "F021" not in src:
        return False

    def shift(match):
        old = match.group(0)
        n = int(old[1:])
        if 21 <= n <= 34:
            return f"F{n+13:03d}"
        return old

    new = re.sub(r"\bF\d{3}\b", shift, src)
    L7.write_text(new)
    return True


# --- 2. Extract rollup rows from a per-lane file ------------------------

def extract_rollup_rows(path: Path, expected_id_range):
    """Pull rollup-table rows for the lane's own IDs from a per-lane file.

    Returns a list of (sort_key, row_text) tuples. expected_id_range is a
    (lo, hi) tuple of integer FNNN values to include.
    """
    if not path.exists():
        return []
    rows = []
    lo, hi = expected_id_range
    for line in path.read_text().splitlines():
        # rollup rows look like: | F031 | MEDIUM | ... | ... |
        # or with bold: | **F033** | **HIGH** | ...
        m = re.match(r"\|\s*\*{0,2}(F(\d{3}))\*{0,2}\s*\|", line)
        if not m:
            continue
        nid = int(m.group(2))
        if lo <= nid <= hi:
            rows.append((nid, line))
    rows.sort()
    return rows


# --- 3. Extract per-finding detail sections from a per-lane file --------

DETAIL_HEADER = re.compile(r"^##\s+\*{0,2}(F\d{3})\b", re.MULTILINE)

def extract_details(path: Path, expected_id_range):
    """Pull `## FNNN — ...` detail sections from a per-lane file.

    A section runs from its `## FNNN` header until the next `## ` header
    or end-of-file. Returns list of (sort_key, section_text).
    """
    if not path.exists():
        return []
    text = path.read_text()
    # Find all `## FNNN` start positions
    starts = []
    for m in re.finditer(r"^##\s+\*{0,2}(F(\d{3}))\b.*$", text, re.MULTILINE):
        nid = int(m.group(2))
        starts.append((nid, m.start(), m.group(0)))
    # Append a sentinel end at EOF or next non-FNNN ## header so we can slice
    lo, hi = expected_id_range
    sections = []
    for i, (nid, start, _) in enumerate(starts):
        end = starts[i+1][1] if i+1 < len(starts) else _next_section_or_eof(text, start)
        body = text[start:end].rstrip() + "\n"
        if lo <= nid <= hi:
            sections.append((nid, body))
    sections.sort()
    return sections


def _next_section_or_eof(text, start_offset):
    """Find the next `## ` header that's NOT an FNNN heading after start_offset.
    Used to bound the last FNNN section when the per-lane file has trailing
    non-finding content."""
    pos = start_offset + 1
    while True:
        m = re.search(r"^##\s+(?!\*?F\d{3}\b)\S", text[pos:], re.MULTILINE)
        if not m:
            return len(text)
        return pos + m.start()


# --- 4. Build the unified master FINDINGS.md ----------------------------

FRONTMATTER = """# Bughunt-12 — Findings rollup

**Round:** Bughunt #12 (+ tracking notes for any new security review)
**Started:** 2026-05-27
**Baseline:** Leonard d716b02 + uncommitted `stop.go` simplification (see RED-TEAM-PLAN.md)

> **Reading note.** In `runlog/*.md`, `PASS` means *"the session didn't time out and the call returned exit 0"* — it does NOT mean "the server gave the correct answer." Many findings here (F004, F005, F009) were `PASS exit=0` calls whose response payload exposes the bug. Always cross-check the per-call response in the runlog for any test where correctness matters more than liveness.

Severity scale (matches `~/Documents/Homelab/leonard/audits/`):
- **CRITICAL** — exploitable RCE / arbitrary file write / trust bypass
- **HIGH** — path traversal escaping project root, DoS crashing hook process, secret leakage, trust bypass under known attack classes
- **MEDIUM** — resource exhaustion within bounds, error-swallowing masking problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage

## Rollup

| ID | Severity | Lane | Title | Status |
|---|---|---|---|---|
"""

# Pre-existing F001-F020 rollup rows, in order. These are stable.
EXISTING_ROLLUP_ROWS = [
    "| F001 | LOW | L1 build invariants | Version-constant drift across the three binaries (mcp 0.53.0, leonard + hook 0.52.0) — three separate sources of truth | confirmed |",
    "| F002 | LOW | L1 docs | Homelab CLAUDE.md \"setup pattern\" template puts `mcpServers` in `settings.local.json`, but the current Claude Code schema rejects it there — `.mcp.json` is the correct location | confirmed |",
    "| F003 | MEDIUM | L3 index | `leonard index` reports a false `FilesIndexed` count when project has >1000 files — the count is computed via a SQL-capped `ListFiles` query (sec-4 F3 cap), so on any non-toy project the operator sees `indexed 1000 file(s)` regardless of true count | confirmed (1515 fixtures → reported 1000) |",
    "| F004 | MEDIUM | L5 parser robustness | Python parser rejects UTF-8-BOM-prefixed files that CPython's `python3 file.py` executes cleanly — symbols silently absent from index, `verify_symbol` returns false for symbols that genuinely exist | confirmed |",
    "| F005 | MEDIUM | L2 caps | `find_symbol limit=0` returns exactly 50 matches; schema says `\"0 = unlimited\"`. Bug is *specific to the limit==0 path* (every value 1..500 returns exactly that many) — root cause is in `store.FindSymbolsByQuery` zero-default, not the MCP `filterAndConvert` clamp | confirmed + root cause narrowed |",
    "| F006 | LOW | L2 caps | INT64-max `limit` triggers a leaky JSON-unmarshal error with float64 precision loss + a stray `}` in the user-visible message | confirmed |",
    "| F007 | LOW | L2 caps | `find_symbol` with a 64 KiB query leaks a raw SQLite `\"LIKE or GLOB pattern too complex\"` instead of a clean MCP-layer \"query too long\" cap | confirmed |",
    "| F008 | LOW | L6 protocol | MCP error responses use `code: 0` — JSON-RPC spec reserves 0 (standard server-defined range is -32000..-32099) — clients switching on code break | confirmed |",
    "| F009 | LOW | L2 caps | `find_symbol query=\"\"` returns up to 500 matches (empty substring matches everything — correct LIKE semantics, not a defect). Schema declares `required:[\"query\"]` but no `minLength:1`. Reframed as a design choice; recommend `minLength:1` to surface caller mistakes | confirmed |",
    "| F010 | MEDIUM | L5 onboarding | `adapters = []` (the default `leonard init` writes) silently *swaps* the tool surface based on whether `.leonard/ground-truth/` exists. Operator scaffolding ground-truth without changing config loses `find_symbol`/`verify_symbol`/etc. — 11 code-adapter tools replaced by 3 ground-truth tools, no warning | confirmed |",
    "| F011 | MEDIUM | L5 onboarding | Ground-truth setup is broken end-to-end: `leonard init --adapter=code,ground-truth` scaffolds files but **does not update config.toml**; the natural inferred TOML shape `adapters = [\"code\",\"ground-truth\"]` (matching the CLI flag syntax) crashes leonard-mcp at startup with a leaky Go-type error. Correct shape `[[adapters]] / type = \"...\"` is undocumented in CLI help and CLAUDE.md | confirmed |",
    "| F012 | LOW | L5 onboarding | leonard-mcp startup error on bad config (`toml: cannot decode TOML string into struct field config.Config.Adapters of type []config.AdapterConfig`) leaks Go struct names. Operator can't fix what they can't translate | confirmed (companion to F011) |",
    "| F013 | HIGH | L5 detector | **One malformed entry anywhere in `.leonard/ground-truth/*` causes the entire ground-truth adapter to fail init silently** — all 3 MCP tools (`verify_claim`, `list_facts`, `get_story`) drop from `tools/list` with the error only on stderr. A single bad story heading on line 23 took down the whole adapter. Operator sees \"unknown tool\" calls without context | confirmed |",
    "| F014 | MEDIUM | L5 fuzzy | Fuzz threshold 1 (#85 default) over-matches on **numeric near-misses** — `\"53 releases\"` matches the forbidden rule `\"52 releases\"` (Levenshtein 1, semantically a different fact). `\"57 releases\"` also matches. Any 1-digit difference in a rule's number gets flagged | confirmed |",
    "| F015 | MEDIUM | L5 detector | **4-digit years and \"N years\" tenure phrases still produce unverified-claim FPs after #98 fix.** \"I started here in 2010 and stayed until 2015\" → both years flagged. \"2026 is the year. 13 years of dyslexia\" → both flagged. Echoes DOGFOOD #2 directly; #98 mitigation incomplete | confirmed |",
    "| F016 | MEDIUM | L5 fuzzy/word-boundary | **Word-boundary anchoring (#85) doesn't apply to the fuzzy-window's grown edge** — rule `\"Scrum master\"` matches inside `\"Scrum mastery\"` because the fuzzy window grew to length 13 (distance 1 insertion) and the boundary check only protects the rule's exact-length edge. Also double-counts: same span emits both the exact-match span and the fuzzy-extended span | confirmed |",
    "| F017 | LOW | L5 facts impact | `leonard facts impact \"\"` (empty key) silently falls through to all-keys mode. An operator with a variable-substitution bug gets a huge report instead of a clear error | confirmed |",
    "| F018 | MEDIUM | L5 detector scope | **`leonard check` and `list-stale-claims` scan `runlog/`, `findings/`, `RED-TEAM-PLAN.md` — every ISO date in the operator's OWN audit trail becomes a \"finding.\"** No mechanism to exclude operator-internal harness/log dirs. The #74272b0 ground-truth-file exemption helps, but not for arbitrary working dirs. Single L5 run produced **192 findings** mostly from re-detecting fixture text echoed in this lane's own runlog | confirmed |",
    "| F019 | MEDIUM | L5 caps | **`verify_claim` at exactly 262144-byte cap returns NO response in 30s** while cap+1 (262145) is rejected cleanly with the F6 \"text too large\" message. Looks like a `>` vs `>=` off-by-one — at-cap text falls through to the expensive fuzzy scan against every rule, hanging the server | confirmed |",
    "| **F020** | **HIGH** | L3 index | **`pruneStaleFiles` uses the SQL-capped `Store.ListFiles(\"\", \"\")` — same root cause as F003.** On any project with >1000 files, files at sort position > 1000 are NEVER checked for staleness. Deleted/moved file rows accumulate forever, and `verify_symbol` returns `exists=true` for symbols whose source file has been deleted — exactly the \"fabricated APIs/symbols\" failure Leonard exists to prevent | **confirmed with discriminating test** |",
]


def main():
    # Step 1: renumber L7 in place
    renumbered = renumber_l7()
    print(f"L7 renumbering: {'applied F021-F034 → F034-F047' if renumbered else 'already done (idempotent skip)'}")

    # Step 2: extract rollup rows and detail sections from each per-lane file
    l6_rollup  = extract_rollup_rows(L6, (21, 30))
    l7_rollup  = extract_rollup_rows(L7, (34, 47))   # post-renumber
    l8_rollup  = extract_rollup_rows(L8, (31, 33))

    l6_details = extract_details(L6, (21, 30))
    l7_details = extract_details(L7, (34, 47))
    l8_details = extract_details(L8, (31, 33))

    print(f"L6: {len(l6_rollup)} rollup rows, {len(l6_details)} detail sections")
    print(f"L7: {len(l7_rollup)} rollup rows, {len(l7_details)} detail sections (after renumber)")
    print(f"L8: {len(l8_rollup)} rollup rows, {len(l8_details)} detail sections")

    # Step 3: read current master to extract the preserved sections
    #   - F001-F020 details + Open observations + L4 entry — all live there now
    current = MASTER.read_text()

    # Slice from "## F001" up to (but not including) "## Round status" — captures
    # F001-F020 details, Open observations, and the L4 GREEN-LANE entry.
    preserved_start = current.find("## F001 —")
    preserved_end   = current.find("## Round status")
    if preserved_start == -1 or preserved_end == -1:
        raise SystemExit("Could not locate F001 detail start or Round status — master file in unexpected shape")
    preserved = current[preserved_start:preserved_end].rstrip() + "\n\n---\n\n"

    # Step 4: assemble the new master
    parts = [FRONTMATTER]

    # Unified rollup: pre-existing F001-F020 + L6 + L8 + L7 (in numeric order)
    all_new_rows = l6_rollup + l8_rollup + l7_rollup
    all_new_rows.sort(key=lambda x: x[0])

    parts.extend(row + "\n" for row in EXISTING_ROLLUP_ROWS)
    parts.extend(row + "\n" for _, row in all_new_rows)
    parts.append("\n(Lane runs append rows as findings surface — see `runlog/` for full traces.)\n\n---\n\n")

    # Preserved F001-F020 details + Open observations + L4 entry
    parts.append(preserved)

    # Append all renumbered new findings details, in numeric order
    all_new_details = l6_details + l7_details + l8_details
    all_new_details.sort(key=lambda x: x[0])
    for nid, body in all_new_details:
        parts.append(body)
        if not body.endswith("\n---\n\n"):
            parts.append("\n---\n\n")

    # Step 5: updated Round status
    total = 20 + len(l6_rollup) + len(l7_rollup) + len(l8_rollup)
    # Severity counts — derive from rollup rows
    def sev(row):
        m = re.match(r"\|\s*\*{0,2}F\d{3}\*{0,2}\s*\|\s*\*{0,2}([A-Z/]+)", row)
        return m.group(1) if m else "?"
    # The F020 row has bold IDs (| **F020** | **HIGH** |...) so the regex
    # must accept optional `**` around the ID.
    id_re = re.compile(r"\|\s*\*{0,2}F(\d{3})\*{0,2}")
    all_rollup = (
        [(int(id_re.match(r).group(1)), sev(r), "L1-L5") for r in EXISTING_ROLLUP_ROWS]
        + [(nid, sev(r), "L6") for nid, r in l6_rollup]
        + [(nid, sev(r), "L7") for nid, r in l7_rollup]
        + [(nid, sev(r), "L8") for nid, r in l8_rollup]
    )
    crit = sum(1 for _, s, _ in all_rollup if s == "CRITICAL")
    high = sum(1 for _, s, _ in all_rollup if s == "HIGH")
    med  = sum(1 for _, s, _ in all_rollup if s == "MEDIUM")
    low  = sum(1 for _, s, _ in all_rollup if s == "LOW")
    other = total - crit - high - med - low  # withdrawn entries etc.

    parts.append(f"""## Round status

**Bughunt-12 is substantively COMPLETE across all 8 designed lanes** (L1–L8 of RED-TEAM-PLAN.md).

| Lane | Sub-tests | New findings | Highest severity |
|---|---:|---:|---|
| L1 (orientation) | — | 2 | LOW |
| L2 (cap edges) | 47 | 7 | MEDIUM |
| L3 (index correctness) | 31 + discriminating test | 1 | **HIGH** (F020) |
| L4 (concurrency + crash) | 22 | **0 — GREEN LANE** | — |
| L5 (v0.53 ground-truth) | ~40 + setup | 10 | **HIGH** (F013) |
| L6 (MCP protocol fuzz) | 33 | {len(l6_rollup)} | LOW (cluster — all F008 family) |
| L7 (real dogfood) | 12 sections | {len(l7_rollup)} | **HIGH** (F046 — PROMOTED) |
| L8 (path-trust macOS) | 73 | {len(l8_rollup)} | MEDIUM |

**Findings total: {total}.** Severity mix: {crit} CRITICAL, **{high} HIGH**, {med} MEDIUM, {low} LOW{('  (+ ' + str(other) + ' withdrawn)') if other else ''}.

**Highest-ROI fix order** (best leverage per developer-hour):

1. **F020 + F003** — *single fix at the same site* (`Store.ListFiles("", "")` → `Store.IterFiles`/`Store.AllFilePaths` at `indexer.go:394` and `wire_real.go:65`). Closes the HIGH that re-introduces Leonard's #1 failure mode (fabricated-symbol references via stale prune rows) plus the misleading-count MEDIUM together.
2. **L7 F046 HIGH (Bash matcher bypass)** — extend the ground-truth content detector into the Bash matcher's command-inspect path. `cat > f`, `sed -i`, `echo > f` currently bypass *every* forbidden-claim and content-filter rule. Distinct shape from F020.
3. **F013** — partial-load with loud warnings on malformed ground-truth files. One bad story heading shouldn't take down all 3 MCP tools.
4. **F018** — `.leonardignore` / `scan_exclude` for `runlog/`, `findings/`, audit dirs. Without this, every project Leonard touches drowns in self-detection.
5. **L6 error-helper** — one helper that maps every protocol failure to its JSON-RPC spec code closes **F008 + F021 + F025 + F027 + F030** in one change.
6. **F005** — `find_symbol limit=0 → MaxSymbolResults` (or update tool description to match the 50 default).
7. **F014/F015/F016** — detector quality round (fuzz numeric over-match, year/tenure FPs, fuzzy-window word-boundary).
8. **L8 F031/F032** — lowercase `storeKey` on Darwin/Windows. Same change closes both APFS case-only finds.
9. **F019** — `>` → `>=` in `verify_claim` cap check.

**Validated negative-space:** L4 confirmed the SQLite + WAL foundation handles concurrency (N=8 parallel indexes, kill -9 mid-write, in-session concurrent tool calls, two MCP servers same DB) without bugs. Future audits can skip the storage-layer scrutiny.

**Per-lane source files** (audit-format, ready to promote to `~/Documents/Homelab/leonard/audits/bughunt-12-<lane>.md`):
`findings/L6-findings.md`, `findings/L7-findings.md`, `findings/L8-findings.md`. Per-finding details for F021–F047 below are extracted from these files.
""")

    new_master = "".join(parts)
    MASTER.write_text(new_master)
    print(f"\nMaster FINDINGS.md rebuilt: {len(new_master)} bytes, {total} findings.")


if __name__ == "__main__":
    main()
