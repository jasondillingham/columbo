#!/usr/bin/env bash
# L4 — lockfile, ledger, daemon lifecycle.
# Targets:
#   - F007 follow-up: bosun_attach pid=2/99999999/2147483647 hangs (HARNESS BUG —
#     mcp_sock.py's SHUT_WR causes the go-mcp-SDK server to tear down before
#     the slow Derive-gated tools (attach/usage/done) finish writing the response).
#     The lane uses /tmp/l4_no_shut_call.py which does NOT half-close.
#   - Bundle B (30s bounded lock acquisition + holder-PID diagnostic).
#   - Bundle D (PIPE_BUF=480 atomic-append ledger).
#   - Daemon lifecycle (socket-file removal, two-daemon race, SIGTERM cleanup).
#
# Most probes need MCP daemon up. Lane scripts source rt.sh so output is captured.

set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L4-lock-ledger

REPO=/tmp/bosun-redteam-L4/test-repo
BOSUN=/tmp/bosun_test
NO_SHUT=/tmp/l4_no_shut_call.py
HOLDER=/tmp/l4_lock_holder
export REPO BOSUN NO_SHUT HOLDER
export BOSUN_MCP_SOCK="$REPO/.bosun/mcp.sock"

# Boot the MCP daemon for the duration of this lane
cd "$REPO"
$BOSUN mcp > /tmp/l4_mcp.log 2>&1 &
MCP_PID=$!
sleep 0.8
cd - >/dev/null
trap "kill $MCP_PID 2>/dev/null; wait $MCP_PID 2>/dev/null; true" EXIT

# Helper: call a tool WITHOUT half-closing the write side. Required because
# mcp_sock.py's SHUT_WR races the server: fast tools (tools/list) survive,
# Derive-gated tools (attach/usage/done) lose their response. Side-effect
# still happens server-side. See F007 resolution.
call_tool_no_shut() {
  # call_tool_no_shut <tool> '<json-args>' [-]
  if [ "${3:-}" = "-" ] || [ "${2:-}" = "-" ]; then
    python3 "$NO_SHUT" "$1" - < /dev/stdin
  else
    python3 "$NO_SHUT" "$1" "$2"
  fi
}
call_tool() {
  if [ "${3:-}" = "-" ]; then
    python3 /tmp/bosun-redteam/harness/mcp_sock.py call "$1" - < /dev/stdin
  else
    python3 /tmp/bosun-redteam/harness/mcp_sock.py call "$1" "$2"
  fi
}
summarize() { python3 /tmp/bosun-redteam/harness/summarize.py; }
export -f call_tool_no_shut call_tool summarize

# ---------------------------------------------------------------------------
rt_section "L4a — F007 follow-up (mcp_sock.py SHUT_WR vs Derive-gated tools)"

rt_run "attach pid=2 via SHUT_WR client (reproduces F007 'no response')" \
    python3 /tmp/bosun-redteam/harness/mcp_sock.py call bosun_attach '{"pid":2,"session":"session-1"}'
rt_run "attach pid=2 via no-SHUT_WR client (response arrives, side-effect ok)" \
    python3 "$NO_SHUT" bosun_attach '{"pid":2,"session":"session-1"}'
rt_run "attach pid=99999999 via no-SHUT_WR (response arrives)" \
    python3 "$NO_SHUT" bosun_attach '{"pid":99999999,"session":"session-1"}'
rt_run "attach pid=2147483647 (INT32_MAX) via no-SHUT_WR" \
    python3 "$NO_SHUT" bosun_attach '{"pid":2147483647,"session":"session-1"}'

# F009: the side-effect-without-response shape is a real LOW. A buggy client
# that half-closes gets the .attached-pid file mutated but never receives the
# response. This is also the underlying reason mcp_sock.py masked F007.
rt_finding F009 LOW "bosun side-effects commit on connections that half-close their write side before a response is sent — client never learns of success/failure; harness-level race not a server flaw" \
    "Reproducer: SHUT_WR after sending a slow tools/call (attach/usage/done all go through session.Derive). Server processes the call, writes state, but the response is never flushed. F007 OPEN is withdrawn: the original probe used mcp_sock.py which does this. Recommendation: bosun's MCP server could (a) hold the connection open until the in-flight tools/call response is written, OR (b) document that clients must not half-close pre-response; the latter is the spec-conforming default for go-mcp-SDK so a doc note is sufficient."

# ---------------------------------------------------------------------------
rt_section "L4b — UTF-8 truncation in encodeLineUnderCap (probe #6)"

rt_run "usage with 200×'日' label (raw=600B, must shrink under 480)" bash -c '
python3 -c "
import json
print(json.dumps({
  \"session\":\"session-1\",
  \"model\":\"claude-sonnet-4-5\",
  \"tokens_in\":1,\"tokens_out\":1,
  \"cost_usd\":0.001,
  \"turn_label\":\"日\"*200,
}))" | call_tool_no_shut bosun_usage - | summarize
'
rt_run "usage with 159×'日' (forces cut to land mid-rune at byte 412)" bash -c '
python3 -c "
import json
print(json.dumps({
  \"session\":\"session-1\",
  \"model\":\"claude-sonnet-4-5\",
  \"tokens_in\":1,\"tokens_out\":1,
  \"cost_usd\":0.001,
  \"turn_label\":\"日\"*159,
}))" | call_tool_no_shut bosun_usage - | summarize
'
rt_note "Out-of-band Go reproducer (/tmp/l4_utf8_adversarial.go) swept N=140..170 and 200+ for both fields; all lines came back under 480 bytes. UTF-8 mid-rune cuts are CORRECTLY handled — go json.Marshal replaces partial bytes with U+FFFD (6 ASCII bytes each), which makes the line LARGER, triggering another shrink iteration. The loop converges. **encodeLineUnderCap is robust** — no torn-write risk from this class."

# ---------------------------------------------------------------------------
rt_section "L4c — Bundle B 30s bounded lock acquisition"

# Probe 1+2: hold state lock externally; verify ~30s timeout + holder diagnostic.
rt_run "Bundle B: attach while state lock externally-held (45s window, expect 30s timeout + PID diagnostic)" bash -c '
set -u
HOLDER="'"$HOLDER"'"
NO_SHUT="'"$NO_SHUT"'"
REPO="'"$REPO"'"
"$HOLDER" "$REPO/.bosun/state/.lock" 35 > /tmp/l4_holder.log 2>&1 &
H=$!
sleep 0.5
START=$(date +%s)
MCP_TIMEOUT=45 python3 "$NO_SHUT" bosun_attach "{\"pid\":7,\"session\":\"session-1\"}"
END=$(date +%s)
echo "elapsed=$((END-START))s"
wait $H 2>/dev/null
'

# Probe 4: SIGKILL the holder; the waiter should pick up the lock almost
# immediately (kernel-tracked flock releases on process death).
rt_run "Bundle B: SIGKILL holder mid-wait; waiter must notice within poll interval" bash -c '
set -u
HOLDER="'"$HOLDER"'"
NO_SHUT="'"$NO_SHUT"'"
REPO="'"$REPO"'"
"$HOLDER" "$REPO/.bosun/state/.lock" 60 > /tmp/l4_holder.log 2>&1 &
H=$!
sleep 0.5
( MCP_TIMEOUT=20 python3 "$NO_SHUT" bosun_attach "{\"pid\":11,\"session\":\"session-1\"}" > /tmp/l4_attach_result.json 2>&1; echo $(date +%s) > /tmp/l4_attach_done.ts ) &
W=$!
START=$(date +%s)
sleep 2
kill -9 $H
wait $W
DONE=$(cat /tmp/l4_attach_done.ts)
echo "elapsed=$((DONE-START))s"
cat /tmp/l4_attach_result.json
'

# ---------------------------------------------------------------------------
rt_section "L4d — Bundle D PIPE_BUF atomic-append ledger (N=8 concurrent writers)"

rt_run "Bundle D: 8 writers × 50 entries each, all under cap, no torn writes" bash -c '
set -u
LEDGER="'"$REPO"'/.bosun/state/session-1.usage"
NO_SHUT="'"$NO_SHUT"'"
rm -f "$LEDGER"
cat > /tmp/l4_writer.sh << WEOF
#!/bin/bash
WID=\$1; PER=\$2
NO_SHUT='"$NO_SHUT"'
for ((i=0; i<PER; i++)); do
  python3 -c "
import json
print(json.dumps({
  \"session\":\"session-1\",
  \"model\":\"claude-sonnet-4-5\",
  \"tokens_in\":100,\"tokens_out\":100,
  \"cost_usd\":0.001,
  \"turn_label\":\"writer\${WID}-iter\${i}-\" + \"L\"*100
}))" | python3 "\$NO_SHUT" bosun_usage - > /dev/null 2>&1
done
WEOF
chmod +x /tmp/l4_writer.sh
for w in 1 2 3 4 5 6 7 8; do /tmp/l4_writer.sh $w 50 & done
wait
LINES=$(wc -l < "$LEDGER" | tr -d " ")
echo "lines=$LINES (expected 400)"
python3 -c "
import json, sys
ok=bad=0; maxlen=0
with open(\"$LEDGER\") as f:
  for ln in f:
    s = ln.rstrip(chr(10))
    if not s: continue
    maxlen = max(maxlen, len(s)+1)
    try: json.loads(s); ok += 1
    except Exception: bad += 1
print(f\"parsed_ok={ok} parsed_bad={bad} max_line_bytes={maxlen}\")
"
'

# ---------------------------------------------------------------------------
rt_section "L4e — Daemon lifecycle"

rt_run "probe 9: remove socket file; daemon stays alive but unreachable" bash -c '
set -u
REPO="'"$REPO"'"
ls -la "$REPO/.bosun/mcp.sock"
rm "$REPO/.bosun/mcp.sock"
sleep 0.3
echo "--- after rm: ---"
ls -la "$REPO/.bosun/mcp.sock" 2>&1 || true
echo "--- can clients still connect? (expect transport error) ---"
python3 /tmp/bosun-redteam/harness/mcp_sock.py list 2>&1 || true
echo "--- daemon process: ---"
ps -p '"$MCP_PID"' 2>&1 || true
'

# Recover for next probes
kill $MCP_PID 2>/dev/null
wait $MCP_PID 2>/dev/null
sleep 0.5
( cd "$REPO" && $BOSUN mcp > /tmp/l4_mcp.log 2>&1 & echo $! > /tmp/l4_mcp.pid )
sleep 0.8
MCP_PID=$(cat /tmp/l4_mcp.pid)
trap "kill $MCP_PID 2>/dev/null; wait $MCP_PID 2>/dev/null; true" EXIT

rt_run "probe 10: two daemons race on same socket — second silently steals, first becomes zombie" bash -c '
set -u
REPO="'"$REPO"'"
BOSUN="'"$BOSUN"'"
echo "--- daemon 1 ($(cat /tmp/l4_mcp.pid)) listening:"
ls -la "$REPO/.bosun/mcp.sock"
echo "--- starting daemon 2..."
( cd "$REPO" && "$BOSUN" mcp > /tmp/l4_mcp2.log 2>&1 & echo $! > /tmp/l4_mcp2.pid )
sleep 1.5
echo "daemon 2 PID: $(cat /tmp/l4_mcp2.pid)"
echo "daemon 2 log:"; cat /tmp/l4_mcp2.log
echo "--- both alive?"
ps -p $(cat /tmp/l4_mcp.pid) | tail -1
ps -p $(cat /tmp/l4_mcp2.pid) | tail -1
echo "--- which holds the listener inode?"
lsof "$REPO/.bosun/mcp.sock" 2>/dev/null | tail -3
echo "--- kill daemon 1; daemon 2 should now be unreachable"
kill $(cat /tmp/l4_mcp.pid)
sleep 0.5
ls -la "$REPO/.bosun/mcp.sock" 2>&1
python3 /tmp/bosun-redteam/harness/mcp_sock.py list 2>&1 || true
echo "--- daemon 2 still alive but stranded:"
ps -p $(cat /tmp/l4_mcp2.pid) | tail -1
kill $(cat /tmp/l4_mcp2.pid) 2>/dev/null
'

# Restart for final cleanup
sleep 0.5
( cd "$REPO" && $BOSUN mcp > /tmp/l4_mcp.log 2>&1 & echo $! > /tmp/l4_mcp.pid )
sleep 0.8
MCP_PID=$(cat /tmp/l4_mcp.pid)
trap "kill $MCP_PID 2>/dev/null; wait $MCP_PID 2>/dev/null; true" EXIT

rt_run "probe 12: client connects then closes pre-initialize; daemon handles EOF cleanly" bash -c '
python3 -c "
import socket, os
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.connect(os.environ[\"BOSUN_MCP_SOCK\"])
print(\"connected\")
s.close()
print(\"closed without sending\")
"
sleep 0.3
echo "--- daemon alive?"
ps -p '"$MCP_PID"' | tail -1
echo "--- tools/list still works?"
python3 /tmp/bosun-redteam/harness/mcp_sock.py list 2>&1 | head -3
'

rt_run "probe 13: socket permissions (expect srw-------)" bash -c '
set -u
REPO="'"$REPO"'"
ls -la "$REPO/.bosun/mcp.sock"
PERMS=$(stat -f "%Lp" "$REPO/.bosun/mcp.sock")
[ "$PERMS" = "600" ] && echo "PASS: 0600 (owner-only)" || { echo "FAIL: perms=$PERMS"; exit 1; }
'

# ---------------------------------------------------------------------------
rt_section "Findings registered for this lane"

rt_finding F010 MEDIUM "removing mcp.sock leaves daemon running but unreachable; no automatic recovery" \
    "Operator-visible: ls + rm of .bosun/mcp.sock orphans the daemon. Daemon doesn't notice the inode is gone; clients see ENOENT. Recovery requires SIGTERM + restart. Risk: a maintenance script that 'rm -rf .bosun/' to reset state silently strands the daemon; subsequent bosun_check calls all fail until the process is reaped. Fix shape: have Serve() periodically Stat the socket path and exit if missing — OR rely on operators using bosun stop / SIGTERM."

rt_finding F011 MEDIUM "no inter-process guard on bosun mcp: second daemon silently steals the socket, first becomes a zombie" \
    "Running 'bosun mcp' twice in the same repo: server.go Listen() does removeIfSocket(socketPath) before binding — UNCONDITIONAL unlink even when another daemon holds the inode. The .bosun/mcp.lock guard exists in mcp_autostart.go for the 'bosun init --launch' path but is NOT taken by 'bosun mcp' itself. Effect: daemon-2 unlinks daemon-1's socket file, binds a new one, both processes appear to be 'listening' but only daemon-2 receives connections. When daemon-1 is killed (e.g. by SIGTERM during cleanup), its 'defer os.Remove(socketPath)' clobbers daemon-2's socket file → daemon-2 is now stranded too. Fix: take .bosun/mcp.lock around the Listen+Serve block of cmd_mcp.go, OR check the pidfile for a live owner before removeIfSocket."

rt_finding F012 LOW "Bundle B 30s timeout includes a 1s overshoot from poll-tick granularity (observation, not a bug)" \
    "Observed elapsed=31s for a 30s timeout. The poll loop sleeps 50ms between flock attempts; the deadline check fires AFTER sleep, so the worst-case is timeout + pollInterval. Already-correct semantics — the error message says 'held by PID N for 31s; timed out after 30s' so the operator sees both numbers. Document, don't change."

rt_summary
