#!/usr/bin/env bash
# L7 — real-dogfood UX
#
# Goal: use Leonard for actual work and capture every UX friction.
# Extends ~/Documents/Homelab/leonard/DOGFOOD.md (the operator's own log).
# Many L7 probes are qualitative ("read this output, judge UX") and produce
# their finding from operator judgment rather than a numeric assert. This
# script captures the automate-able portion of the lane.
#
# Findings written to findings/L7-findings.md (NOT findings/FINDINGS.md — the
# rt.sh default — see RT_FINDINGS_INDEX override below).
set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L7-dogfood
RT_FINDINGS_INDEX="$RT_ROOT/findings/L7-findings.md"   # send rt_finding rows to L7-findings.md

# ---------- DOGFOOD #1 — stale-claim drift at file-READ time ----------
# DOGFOOD says: "knowledge isn't surfaced when the file is read — only when
# it's edited." HEAD-fresh `leonard check` is the candidate fix. Test whether
# check actually flags a contradiction with facts.yaml that is NOT explicitly
# listed in do-not-claim.md.
rt_section "L7-A — DOGFOOD #1 (stale-claim drift): does \`check\` catch facts.yaml contradictions?"

mkdir -p fixtures/L7_scratch
cat > fixtures/L7_scratch/contradicts_facts.md <<'PROSE'
# Contradicts facts.yaml but NOT in do-not-claim.md
The team has 99 engineers.
Bosun has 4 tools.
Leonard ships 100 MCP tools.
PROSE
rt_run "check contradicts_facts.md (expect: detects 99 vs 12, 4 vs 9, 100 vs 14)" \
    ~/go/bin/leonard check fixtures/L7_scratch/contradicts_facts.md

# ---------- DOGFOOD #5 — fact-diff propagation ----------
rt_section "L7-B — DOGFOOD #5 (facts diff): does it work without git?"
rt_run "facts diff (project is not a git repo)" ~/go/bin/leonard facts diff

# ---------- DOGFOOD #3 — cross-artifact consistency / list-stale-claims scope ----------
rt_section "L7-C — list-stale-claims glob handling"
rt_run "scope=fixtures/L7_scratch/**/*.md (recursive)" ~/go/bin/leonard list-stale-claims --scope="fixtures/L7_scratch/**/*.md"
rt_run "scope=fixtures/L7_scratch/*.md (flat)" ~/go/bin/leonard list-stale-claims --scope="fixtures/L7_scratch/*.md"
rt_run "scope=fixtures/**/*.md (recursive from parent)" bash -c "~/go/bin/leonard list-stale-claims --scope='fixtures/**/*.md' >/dev/null 2>&1; echo exit=\$?"

# ---------- DOGFOOD #4 — SessionStart visibility ----------
rt_section "L7-D — SessionStart message (DOGFOOD #4 confirmation)"
rt_run "session-start hook output" python3 harness/hook.py session-start

# ---------- DOGFOOD #6 — post-edit hook noise ----------
rt_section "L7-E — post-edit hook output volume (DOGFOOD #6)"
for i in 1 2 3; do
  cat > "fixtures/L7_scratch/edit_seq_$i.md" << EOF
# File $i
This is edit number $i.
EOF
  rt_run "post-edit Write fixtures/L7_scratch/edit_seq_$i.md" \
      python3 harness/hook.py post-edit Write "fixtures/L7_scratch/edit_seq_$i.md" "x"
done

# ---------- Pre-edit hook: forbidden claim detection (with trust granted) ----------
rt_section "L7-F — pre-edit forbidden-claim hook (ground-truth trusted)"
# Trust was already granted earlier in the L7 session via `leonard config trust ground-truth --yes`.
# If you re-run this lane after a fresh init, re-grant first.
rt_run "pre-edit Write with '52 releases' content (expect: deny)" \
    python3 harness/hook.py pre-edit Write fixtures/L7_scratch/probe.md "We have done 52 releases"
rt_run "pre-edit Write to .leonard/ subdir (expect: deny)" \
    python3 harness/hook.py pre-edit Write .leonard/ground-truth/probe-write.md "harmless content"
rt_run "pre-edit Edit replacing '52 releases' with '46 releases' (expect: allow)" \
    python3 harness/hook.py raw pre-edit '{"session_id":"test-edit-allow","hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"fixtures/L7_scratch/sample.md","old_string":"We had 52 releases","new_string":"We had 46 releases"},"cwd":"'"$(pwd)"'"}'

# ---------- Post-edit on forbidden file written through Bash (bypasses pre-edit) ----------
rt_section "L7-G — post-edit on forbidden content written via Bash"
cat > fixtures/L7_scratch/post_forbidden.md <<'EOF'
# Stale claims (via Bash, bypassing pre-edit)
This file contains 52 releases.
Our Scrum master is great.
EOF
rt_run "post-edit Write (expect: silently re-indexed, NO flag of '52 releases')" \
    python3 harness/hook.py post-edit Write fixtures/L7_scratch/post_forbidden.md "x"

# ---------- Decision workflow: record → list → supersede → list ----------
rt_section "L7-H — decision roundtrip + supersede"
rt_run "record_decision (initial)" python3 harness/mcp.py call record_decision \
    '{"topic":"L7-probe-decision","choice":"first","reasoning":"initial L7 probe decision"}'
# Note: supersede needs the id returned above. Use leonard CLI to find latest.
rt_run "decisions list (CLI shows ISO timestamps)" ~/go/bin/leonard decisions list
# Supersede the topic — use a known-stale id (the latest one). Script reads it back.
LATEST_ID=$(~/go/bin/leonard decisions list 2>/dev/null | grep -E '^\s+#[0-9]+' | head -1 | sed -E 's/^[ \t]*#([0-9]+).*/\1/')
rt_note "Latest decision_id from CLI list = $LATEST_ID"
rt_run "supersede_decision (latest -> new)" python3 harness/mcp.py call supersede_decision \
    '{"decision_id":'"$LATEST_ID"',"new_choice":"second","new_reasoning":"superseded for L7 dogfood test"}'
rt_run "get_decisions (MCP): does it mark which one is superseded?" \
    python3 harness/mcp.py call get_decisions '{}'
rt_run "decisions list (CLI): does it mark which one is superseded?" \
    ~/go/bin/leonard decisions list

# ---------- Claims workflow: record + list ----------
rt_section "L7-I — record_claim + get_unverified_claims"
rt_run "record_claim verified=false" python3 harness/mcp.py call record_claim \
    '{"claim":"L7 lane probes both MCP and hook surfaces","evidence":"see lane script","verified":false}'
rt_run "get_unverified_claims (MCP): is evidence returned?" \
    python3 harness/mcp.py call get_unverified_claims '{}'

# ---------- verify_symbol: typo without suggestions ----------
rt_section "L7-J — verify_symbol UX on close-miss typos"
rt_run "verify BulkFunc1 (exists? expect: false)" ~/go/bin/leonard verify BulkFunc1
rt_run "find_symbol substring=BulkFunc (expect: 50 hits incl BulkFunc0001)" \
    python3 harness/mcp.py call find_symbol '{"query":"BulkFunc","limit":3}'

# ---------- doctor count vs reality ----------
rt_section "L7-K — doctor command file count (compounds F003)"
rt_run "doctor (expect: reports files: 1000 total despite F003 — i.e. capped)" \
    ~/go/bin/leonard doctor

# ---------- help text completeness ----------
rt_section "L7-L — help text on subcommands"
rt_run "leonard --help" ~/go/bin/leonard --help
rt_run "leonard check --help" ~/go/bin/leonard check --help
rt_run "leonard facts --help" ~/go/bin/leonard facts --help
rt_run "leonard list-stale-claims --help" ~/go/bin/leonard list-stale-claims --help

rt_summary
