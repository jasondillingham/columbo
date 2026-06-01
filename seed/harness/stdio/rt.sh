#!/usr/bin/env bash
# rt.sh — Red-team logging helpers. Source this from a test script:
#
#   source "$(dirname "$0")/rt.sh"
#   rt_init my-test-lane
#   rt_section "list_files cap edges"
#   rt_run "limit=0 baseline"      python3 harness/mcp.py call list_files '{"limit":0}'
#   rt_run "limit=1001 over cap"   python3 harness/mcp.py call list_files '{"limit":1001}'
#   rt_finding F042 MEDIUM "list_files honors negative limit" "details..."
#   rt_summary
#
# Outputs:
#   runlog/run-YYYY-MM-DD-<lane>.md  — full chronological transcript
#   findings/FINDINGS.md             — rollup table + per-finding rows (appended)
#
# Designed to be reused across projects: nothing here is projectdogwalker-specific.
# Set RT_ROOT before sourcing if not running from a project root that holds
# `runlog/` and `findings/` sibling dirs.

set -u

RT_ROOT="${RT_ROOT:-$(pwd)}"
RT_LANE=""
RT_RUNLOG=""
RT_FINDINGS_INDEX="$RT_ROOT/findings/FINDINGS.md"
RT_PASS=0
RT_FAIL=0
RT_FINDINGS=0
RT_STARTED_AT=""

rt__ts()    { date -u +"%Y-%m-%dT%H:%M:%SZ"; }
rt__short() { date +"%H:%M:%S"; }

rt_init() {
    RT_LANE="${1:-unnamed}"
    mkdir -p "$RT_ROOT/runlog" "$RT_ROOT/findings"
    RT_RUNLOG="$RT_ROOT/runlog/run-$(date +%Y-%m-%d)-${RT_LANE}.md"
    RT_STARTED_AT="$(rt__ts)"

    local leonard_ver leonard_mcp_ver leonard_hook_ver leonard_sha
    leonard_ver="$(~/go/bin/leonard --version 2>&1 || echo 'missing')"
    leonard_mcp_ver="$(~/go/bin/leonard-mcp --version 2>&1 || echo 'missing')"
    leonard_hook_ver="$(~/go/bin/leonard-hook --version 2>&1 || echo 'missing')"
    leonard_sha="$(cd ~/Documents/Homelab/leonard && git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"

    {
        echo ""
        echo "# Run — ${RT_LANE} — ${RT_STARTED_AT}"
        echo ""
        echo "**Project root:** \`$RT_ROOT\`  "
        echo "**Leonard SHA:** \`$leonard_sha\`  "
        echo "**Binaries:** $leonard_ver / mcp=$leonard_mcp_ver / hook=$leonard_hook_ver  "
        echo "**Shell:** \`$(uname -srm)\`"
        echo ""
    } >> "$RT_RUNLOG"

    echo "[$(rt__short)] rt_init lane=$RT_LANE log=$RT_RUNLOG" >&2
}

rt_section() {
    local title="$*"
    {
        echo ""
        echo "## $title"
        echo ""
    } >> "$RT_RUNLOG"
    echo "[$(rt__short)] -- $title --" >&2
}

rt_note() {
    {
        echo ""
        echo "> $*"
        echo ""
    } >> "$RT_RUNLOG"
}

# rt_run "<desc>" <cmd> [args...] — runs the command, captures stdout+stderr+exit,
# appends a fenced block to the runlog with all of it, prints a one-line terminal
# summary. Exit code is preserved as $?. Treats exit 0 as PASS, nonzero as FAIL —
# the test author can override classification with rt_finding for the nuanced cases.
rt_run() {
    local desc="$1"; shift
    local ts; ts="$(rt__ts)"
    local out err rc
    out="$(mktemp)"; err="$(mktemp)"
    "$@" >"$out" 2>"$err"
    rc=$?

    local out_bytes err_bytes status
    out_bytes=$(wc -c <"$out" | tr -d ' ')
    err_bytes=$(wc -c <"$err" | tr -d ' ')
    if [ $rc -eq 0 ]; then status="PASS"; RT_PASS=$((RT_PASS+1)); else status="FAIL"; RT_FAIL=$((RT_FAIL+1)); fi

    {
        echo "### \`$desc\`  ($status, exit=$rc)"
        echo ""
        echo "_$ts — stdout ${out_bytes}B, stderr ${err_bytes}B_"
        echo ""
        echo "\`\`\`"
        echo "\$ $*"
        echo "\`\`\`"
        echo ""
        echo "<details><summary>stdout</summary>"
        echo ""
        echo "\`\`\`"
        head -c 8192 "$out"
        [ "$out_bytes" -gt 8192 ] && echo "" && echo "...[truncated $((out_bytes-8192)) bytes]"
        echo "\`\`\`"
        echo ""
        echo "</details>"
        if [ "$err_bytes" -gt 0 ]; then
            echo ""
            echo "<details><summary>stderr</summary>"
            echo ""
            echo "\`\`\`"
            head -c 4096 "$err"
            [ "$err_bytes" -gt 4096 ] && echo "" && echo "...[truncated $((err_bytes-4096)) bytes]"
            echo "\`\`\`"
            echo ""
            echo "</details>"
        fi
        echo ""
    } >> "$RT_RUNLOG"

    echo "[$(rt__short)] $status exit=$rc — $desc" >&2
    rm -f "$out" "$err"
    return $rc
}

# rt_finding ID SEVERITY "title" ["details"]  — log a finding both to the runlog
# and to the findings/FINDINGS.md rollup table. Idempotent for the rollup row.
rt_finding() {
    local id="$1" sev="$2" title="$3" details="${4:-}"
    RT_FINDINGS=$((RT_FINDINGS+1))
    {
        echo ""
        echo "> 🚩 **FINDING $id — $sev — $title**"
        if [ -n "$details" ]; then
            echo ">"
            echo "> $details"
        fi
        echo ""
    } >> "$RT_RUNLOG"

    # Ensure the index file exists with a header.
    if [ ! -f "$RT_FINDINGS_INDEX" ]; then
        {
            echo "# Bughunt-12 — Findings rollup"
            echo ""
            echo "Severity scale: CRITICAL / HIGH / MEDIUM / LOW (matches Leonard \`audits/\` convention)."
            echo ""
            echo "| ID | Severity | Lane | Title | Status |"
            echo "|---|---|---|---|---|"
        } > "$RT_FINDINGS_INDEX"
    fi

    # Append rollup row if missing.
    if ! grep -q "^| $id |" "$RT_FINDINGS_INDEX" 2>/dev/null; then
        printf '| %s | %s | %s | %s | open |\n' "$id" "$sev" "$RT_LANE" "$title" >> "$RT_FINDINGS_INDEX"
    fi
    echo "[$(rt__short)] 🚩 FINDING $id $sev — $title" >&2
}

rt_summary() {
    local ended; ended="$(rt__ts)"
    {
        echo ""
        echo "## Summary"
        echo ""
        echo "- started: $RT_STARTED_AT"
        echo "- ended:   $ended"
        echo "- run results: **$RT_PASS pass, $RT_FAIL fail**, $RT_FINDINGS finding(s) recorded"
        echo ""
    } >> "$RT_RUNLOG"
    echo "[$(rt__short)] DONE — pass=$RT_PASS fail=$RT_FAIL findings=$RT_FINDINGS log=$RT_RUNLOG" >&2
}
