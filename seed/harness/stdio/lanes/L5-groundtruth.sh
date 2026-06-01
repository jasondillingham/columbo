#!/usr/bin/env bash
# L5 — v0.53 ground-truth adapter sweep
# Targets: claim-detector FP/FN, fuzzy threshold #85, facts impact/diff (HEAD-fresh),
# stories loose-heading parser #83, verify_claim cap edges (bughunt-11 F6 cap = 256 KiB),
# leonard check with line numbers #eb5d11d, leonard list-stale-claims.
#
# Prereq: .leonard/config.toml has both [[adapters]] entries; ground-truth fixtures
# populated; harness adapter knows about ground-truth-adapter MCP tools.
set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L5-groundtruth

PROSE=fixtures/prose/mixed_claims.md

# Pretty-printer lives in harness/verdict.py — sourced via a tiny wrapper so the
# subshells `rt_run` spawns don't lose the function (the original `declare -f`
# pattern silently broke under `bash -c` and we got 21 exit=127 entries before
# noticing — see runlog/run-2026-05-27-L5-groundtruth.md).
verdict_check() {
  # usage: verdict_check '<json-payload>'
  echo "$1" | python3 harness/mcp.py call verify_claim - 2>/dev/null | python3 harness/verdict.py
}
export -f verdict_check

rt_section "L5a — claim detector: exact forbidden matches"
for TEXT in \
  "We have done two security reviews." \
  "Older docs claim 52 releases" \
  "still an 8-tool MCP server" \
  "the agile transformation succeeded" \
  "our Scrum master left"
do
  PAYLOAD=$(python3 -c "import json,sys; print(json.dumps({'text': sys.argv[1]}))" "$TEXT")
  rt_run "forbidden: '$TEXT'" bash -c "verdict_check '$PAYLOAD'"
done

rt_section "L5b — fuzzy threshold #85 (default = 1)"
# Rules: "two security reviews", "52 releases"
# At threshold=1, near-misses with edit distance 1 should still match.
for TEXT in \
  "we did two security review" \
  "we did three security reviews" \
  "Tow security reviews completed" \
  "52 releases" \
  "53 releases" \
  "57 releases"
do
  PAYLOAD=$(python3 -c "import json,sys; print(json.dumps({'text': sys.argv[1]}))" "$TEXT")
  rt_run "fuzzy: '$TEXT'" bash -c "verdict_check '$PAYLOAD'"
done

rt_section "L5c — false-positive candidates (DOGFOOD #2 — should NOT trigger)"
for TEXT in \
  "Call 1-555-123-1389 for billing." \
  "Phone home: 800-867-5309." \
  "PR #19755 stats: +3,402/-1,289 LOC." \
  "I started here in 2010 and stayed until 2015." \
  "2026 is the year. 13 years of dyslexia and I still write code."
do
  PAYLOAD=$(python3 -c "import json,sys; print(json.dumps({'text': sys.argv[1]}))" "$TEXT")
  rt_run "FP candidate: '$TEXT'" bash -c "verdict_check '$PAYLOAD'"
done

rt_section "L5d — word-boundary edge cases (#85)"
for TEXT in \
  "Goofy programming is fun" \
  "agileness as a virtue" \
  "Scrum mastery course" \
  "tons of releases this quarter" \
  "two_security_reviews_var"
do
  PAYLOAD=$(python3 -c "import json,sys; print(json.dumps({'text': sys.argv[1]}))" "$TEXT")
  rt_run "word-boundary: '$TEXT'" bash -c "verdict_check '$PAYLOAD'"
done

rt_section "L5e — verify_claim cap edges (bughunt-11 F6 cap = 262144 bytes)"
rt_run "text=empty"                              python3 harness/mcp.py call verify_claim '{"text":""}'
rt_run "text=at cap (262144 bytes)" bash -c "python3 -c 'import json; print(json.dumps({\"text\":\"x\"*262144}))' | python3 harness/mcp.py call verify_claim -"
rt_run "text=at cap+1 (262145 bytes)" bash -c "python3 -c 'import json; print(json.dumps({\"text\":\"x\"*262145}))' | python3 harness/mcp.py call verify_claim -"
rt_run "text=1 MiB"                              bash -c "python3 -c 'import json; print(json.dumps({\"text\":\"x\"*1048576}))' | python3 harness/mcp.py call verify_claim -"

rt_section "L5f — stories loose-heading (#83) regression"
rt_run "get_story name=launch (strict STORY: prefix)"   python3 harness/mcp.py call get_story '{"name":"launch"}'
rt_run "get_story name=dogfood"                          python3 harness/mcp.py call get_story '{"name":"dogfood"}'
rt_run "get_story name=nonexistent"                      python3 harness/mcp.py call get_story '{"name":"nonexistent"}'
rt_run "get_story name=empty"                            python3 harness/mcp.py call get_story '{"name":""}'

rt_section "L5g — facts_impact (HEAD-fresh feature)"
# Place a prose file claiming "12 engineers" (matches facts.yaml team.engineers=12)
mkdir -p fixtures/prose
cat > fixtures/prose/uses_facts.md <<'PROSE'
The team has 12 engineers.
Bosun has 9 tools.
Leonard has 14 MCP tools today.
We shipped 46 releases.
Employment started in 2009.
PROSE
rt_run "facts impact team.engineers"     ~/go/bin/leonard facts impact team.engineers
rt_run "facts impact bosun.tool_count"   ~/go/bin/leonard facts impact bosun.tool_count
rt_run "facts impact leonard.security_reviews (key NOT referenced in prose)" ~/go/bin/leonard facts impact leonard.security_reviews
rt_run "facts impact deployments (array key)" ~/go/bin/leonard facts impact deployments
rt_run "facts impact deployments[0]"          ~/go/bin/leonard facts impact 'deployments[0]'
rt_run "facts impact nonexistent.key"         ~/go/bin/leonard facts impact nonexistent.key
rt_run "facts impact (empty key — argparse?)" ~/go/bin/leonard facts impact ""

rt_section "L5h — facts diff (HEAD-fresh) — capture baseline, mutate, diff"
rt_run "facts diff (no prior snapshot — first run)"   ~/go/bin/leonard facts diff
# Mutate: bump engineers 12 -> 14
cp .leonard/ground-truth/facts.yaml /tmp/facts.before
sed -i.bak 's/engineers: 12/engineers: 14/' .leonard/ground-truth/facts.yaml
rt_run "facts diff (after engineers 12->14)"          ~/go/bin/leonard facts diff
# revert
mv /tmp/facts.before .leonard/ground-truth/facts.yaml

rt_section "L5i — leonard check with line numbers (#eb5d11d)"
rt_run "leonard check fixtures/prose/mixed_claims.md"   ~/go/bin/leonard check fixtures/prose/mixed_claims.md
rt_run "leonard check fixtures/prose/uses_facts.md"     ~/go/bin/leonard check fixtures/prose/uses_facts.md

rt_section "L5j — list-stale-claims (#eb5d11d)"
rt_run "leonard list-stale-claims"                      ~/go/bin/leonard list-stale-claims

rt_summary
