#!/usr/bin/env bash
# L2 — MCP tool cap edges (Bundle A/D regression + extensions).
# Targets:
#   - bosun_spawn brief cap (Bundle A M1, maxBriefBytes = 262144)
#   - bosun_usage line cap (Bundle D M7, maxLineBytes = 480 via encodeLineUnderCap)
#   - bosun_attach PID extremes (Bundle E L2 follow-up)
#   - MCP schema-validation edge cases
#   - Protocol-side over-cap frames
#
# Each probe sends a JSON-RPC frame and prints summary (verdict / isError / size).
set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L2-cap-edges

REPO=/tmp/bosun-redteam/test-repo
BOSUN=/tmp/bosun_test
export REPO BOSUN
export BOSUN_MCP_SOCK="$REPO/.bosun/mcp.sock"

# Boot the MCP daemon for the duration of this lane
cd "$REPO"
$BOSUN mcp > /tmp/l2_mcp.log 2>&1 &
MCP_PID=$!
sleep 0.8
cd - >/dev/null
trap "kill $MCP_PID 2>/dev/null; wait $MCP_PID 2>/dev/null; true" EXIT

call_tool() {
  # call_tool <tool> '<args-json>' [stdin]
  if [ "${3:-}" = "-" ]; then
    python3 /tmp/bosun-redteam/harness/mcp_sock.py call "$1" - < /dev/stdin
  else
    python3 /tmp/bosun-redteam/harness/mcp_sock.py call "$1" "$2"
  fi
}
summarize() {
  python3 /tmp/bosun-redteam/harness/summarize.py
}
export -f call_tool summarize

rt_section "L2a — bosun_spawn brief size cap (Bundle A M1, cap=262144)"

# Build a minimal valid spawn arg shape — bosun_spawn requires briefs:[{brief,...}]
# and a parent label. Send one brief at a time at each cap-edge size.
mk_spawn_payload() {
  # mk_spawn_payload <brief-size-bytes>
  python3 -c "
import json, sys
n = int(sys.argv[1])
brief = 'x' * n
print(json.dumps({
  'parent': 'session-1',
  'briefs': [{'brief': brief, 'label': 'cap-test'}]
}))
" "$1"
}
export -f mk_spawn_payload

rt_run "spawn brief=0 (empty — schema accept/reject?)"               bash -c 'mk_spawn_payload 0 | call_tool bosun_spawn - | summarize'
rt_run "spawn brief=1 (smallest non-empty)"                           bash -c 'mk_spawn_payload 1 | call_tool bosun_spawn - | summarize'
rt_run "spawn brief=262143 (cap-1, should ACCEPT)"                    bash -c 'mk_spawn_payload 262143 | call_tool bosun_spawn - | summarize'
rt_run "spawn brief=262144 (EXACTLY at cap — Leonard F019 shape)"     bash -c 'mk_spawn_payload 262144 | call_tool bosun_spawn - | summarize'
rt_run "spawn brief=262145 (cap+1, should REFUSE)"                    bash -c 'mk_spawn_payload 262145 | call_tool bosun_spawn - | summarize'
rt_run "spawn brief=1048576 (1 MiB, should REFUSE)"                   bash -c 'mk_spawn_payload 1048576 | call_tool bosun_spawn - | summarize'

rt_section "L2b — bosun_usage line cap (Bundle D M7, maxLineBytes=480 via encodeLineUnderCap)"

mk_usage_payload() {
  # mk_usage_payload <label-len> <model-len> [extra]
  python3 -c "
import json, sys
ll = int(sys.argv[1]); ml = int(sys.argv[2])
extra = sys.argv[3] if len(sys.argv) > 3 else ''
print(json.dumps({
  'session': 'session-1',
  'turn_label': ('L' * ll) + extra,
  'model': 'M' * ml,
  'input_tokens': 1000,
  'output_tokens': 1000,
  'cost_usd': 0.01,
}))
" "$1" "$2" "${3:-}"
}
export -f mk_usage_payload

rt_run "usage normal (50/30)"                              bash -c 'mk_usage_payload 50 30 | call_tool bosun_usage - | summarize'
rt_run "usage label=480 (at cap-ish — shrinker activates)" bash -c 'mk_usage_payload 480 30 | call_tool bosun_usage - | summarize'
rt_run "usage label=1000 + model=1000 (force shrink-both)" bash -c 'mk_usage_payload 1000 1000 | call_tool bosun_usage - | summarize'
rt_run "usage label with quote/newline-heavy chars (JSON-escape inflation near cap)" bash -c '
python3 -c "import json; print(json.dumps({
  \"session\":\"session-1\",
  \"turn_label\": (chr(34) + chr(10)) * 200,
  \"model\": \"claude-3-5-sonnet\",
  \"input_tokens\": 100,
  \"output_tokens\": 100,
  \"cost_usd\": 0.001,
}))" | call_tool bosun_usage - | summarize
'

rt_section "L2c — bosun_attach PID extremes (Bundle E L2 follow-up)"

rt_run "attach PID=0 (Bundle E hard-refuse class)"                       call_tool bosun_attach '{"pid":0,"session":"session-1"}' | summarize
rt_run "attach PID=-1 (negative)"                                         call_tool bosun_attach '{"pid":-1,"session":"session-1"}' | summarize
rt_run "attach PID=1 (init / launchd — Bundle E refuse target)"          call_tool bosun_attach '{"pid":1,"session":"session-1"}' | summarize
rt_run "attach PID=2 (kthread on Linux — Bundle E didnt mention)"        call_tool bosun_attach '{"pid":2,"session":"session-1"}' | summarize
rt_run "attach PID=99999999 (way above max — IsAlive false?)"            call_tool bosun_attach '{"pid":99999999,"session":"session-1"}' | summarize
rt_run "attach PID=\$\$ (current shell pid — alive, but bash, not Claude)" bash -c "call_tool bosun_attach \"{\\\"pid\\\":$$,\\\"session\\\":\\\"session-1\\\"}\" | summarize"
rt_run "attach PID=2147483647 (INT32_MAX)"                                call_tool bosun_attach '{"pid":2147483647,"session":"session-1"}' | summarize
rt_run "attach PID=9223372036854775807 (INT64_MAX — JSON-unmarshal class — Leonard F006 shape)" call_tool bosun_attach '{"pid":9223372036854775807,"session":"session-1"}' | summarize

rt_section "L2d — schema-validation edges"

rt_run "bosun_check with NO args (paths required)"                       call_tool bosun_check '{}' | summarize
rt_run "bosun_check paths=null"                                          call_tool bosun_check '{"paths":null}' | summarize
rt_run "bosun_check paths=[] (empty array)"                              call_tool bosun_check '{"paths":[]}' | summarize
rt_run "bosun_check paths=[\"\"] (empty string in array)"                call_tool bosun_check '{"paths":[""]}' | summarize
rt_run "bosun_check paths=[null]"                                        call_tool bosun_check '{"paths":[null]}' | summarize
rt_run "bosun_claim with extra/unknown field (additionalProperties?)"    call_tool bosun_claim '{"session":"session-1","unknown_extra_field":42}' | summarize

rt_section "L2e — protocol-side over-cap"

rt_run "raw frame ~256 KiB noise field" bash -c "
python3 -c 'import json; print(json.dumps({
  \"jsonrpc\":\"2.0\",\"id\":50,\"method\":\"tools/list\",
  \"_noise\":\"Z\"*262144,
}))' | python3 /tmp/bosun-redteam/harness/mcp_sock.py raw - 2>&1 | head -30
"
rt_run "tools/call BEFORE initialize (Leonard L6 F021/F023 regression check)" python3 /tmp/bosun-redteam/harness/mcp_sock.py rawnohandshake \
  '{"jsonrpc":"2.0","id":60,"method":"tools/call","params":{"name":"bosun_check","arguments":{}}}'
rt_run "second initialize within same session (Leonard F022 regression check)" python3 /tmp/bosun-redteam/harness/mcp_sock.py raw \
  '{"jsonrpc":"2.0","id":70,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"x","version":"0"}}}'
rt_run "duplicate ids (Leonard F026 regression check)" python3 /tmp/bosun-redteam/harness/mcp_sock.py raw \
  '{"jsonrpc":"2.0","id":80,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":80,"method":"tools/list"}'

rt_summary
