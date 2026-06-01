#!/usr/bin/env bash
# L6 — MCP protocol fuzz (bosun-specific surface).
#
# Targets:
#  - Framing edges: unbounded ReadBytes, split frames, two frames in one send,
#    very large frames, BOM, no-newline blocking
#  - Schema validation: NUL bytes, wrong type, empty string, deeply nested,
#    duplicate keys, additionalProperties on edge values, float id
#  - Tool-name surface: wrong case, prefix (mcp__bosun__attach), empty,
#    substring, namespaced-form
#  - Concurrent in-session: 10 concurrent calls, mid-session reconnect
#  - F007 follow-up: pid=2/99M/INT32_MAX side-effects on disk after attach
#
# Each probe uses harness/mcp_sock.py for newline-framed correctness, or a
# custom python one-liner for the framing-edge probes that DELIBERATELY
# violate the framing contract.
set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L6-protocol

SANDBOX=/tmp/bosun-redteam-L6
REPO=$SANDBOX/test-repo
BOSUN=/tmp/bosun_test
export SANDBOX REPO BOSUN
export BOSUN_MCP_SOCK="$REPO/.bosun/mcp.sock"

# The brief sets up the sandbox before running the lane; we assume a daemon
# is already running. If it isn't, boot one.
if ! [ -S "$BOSUN_MCP_SOCK" ]; then
    cd "$REPO"
    $BOSUN mcp > /tmp/l6_mcp.log 2>&1 &
    MCP_PID=$!
    sleep 1
    cd - >/dev/null
    trap "kill $MCP_PID 2>/dev/null; wait $MCP_PID 2>/dev/null; true" EXIT
else
    # Pick the running daemon's PID (for RSS observations).
    MCP_PID=$(lsof -t "$BOSUN_MCP_SOCK" 2>/dev/null | head -1)
fi
export MCP_PID
rt_note "Daemon PID = $MCP_PID, socket = $BOSUN_MCP_SOCK"

call_tool()  { python3 /tmp/bosun-redteam/harness/mcp_sock.py call "$1" "$2"; }
raw_frames() { python3 /tmp/bosun-redteam/harness/mcp_sock.py raw "$@"; }
summarize()  { python3 /tmp/bosun-redteam/harness/summarize.py 2>/dev/null || cat; }
export -f call_tool raw_frames summarize

#########################################################
rt_section "L6a — Framing edges (bufio.Reader/ReadBytes)"
#########################################################

rt_run "P1: unbounded buffer growth — 64 MiB no-newline garbage / single conn" \
    bash -c 'python3 /tmp/bosun-redteam-L6/probe_unbounded_buffer.py $MCP_PID 67108864 2>&1'

# Restart daemon between large-RSS probes so each probe has a clean baseline.
restart_daemon() {
    kill $MCP_PID 2>/dev/null || true
    wait $MCP_PID 2>/dev/null || true
    sleep 0.5
    cd "$REPO"
    $BOSUN mcp > /tmp/l6_mcp.log 2>&1 &
    MCP_PID=$!
    export MCP_PID
    sleep 0.8
    cd - >/dev/null
}
export -f restart_daemon

rt_run "P1b: restart daemon (so P2 sees clean baseline RSS)" \
    bash -c 'restart_daemon; echo "new PID=$MCP_PID"'

rt_run "P2: 8 concurrent attackers × 16 MiB each (amplifies P1 to 128 MiB)" \
    bash -c 'python3 /tmp/bosun-redteam-L6/probe_unbounded_concurrent.py $MCP_PID 8 16777216 2>&1'

rt_run "P2b: restart daemon" \
    bash -c 'restart_daemon; echo "new PID=$MCP_PID"'

rt_run "P3: split-frame across 2 sends — does framer reassemble?" \
    bash -c '
python3 <<EOF
import socket, json, os, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5); s.connect(os.environ["BOSUN_MCP_SOCK"])
init = (json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"L6","version":"0"}}})+"\n").encode()
s.sendall(init); time.sleep(0.1)
s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
time.sleep(0.1)
# Drain handshake.
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
except BlockingIOError: pass
s.setblocking(True)
# Now send tools/list in TWO sends, neither containing the full frame.
frame = json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list"})+"\n"
half = len(frame)//2
s.sendall(frame[:half].encode())
time.sleep(0.2)
s.sendall(frame[half:].encode())
time.sleep(0.3)
data = b""
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
        data += c
except BlockingIOError: pass
s.close()
n_lines = len([l for l in data.decode().splitlines() if l.strip()])
print(f"got {n_lines} response lines, {len(data)} bytes")
import sys
sys.exit(0 if n_lines >= 1 else 1)
EOF
'

rt_run "P4: two frames in ONE send (back-to-back, both newline-terminated)" \
    bash -c '
python3 <<EOF
import socket, json, os, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5); s.connect(os.environ["BOSUN_MCP_SOCK"])
init = (json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"L6","version":"0"}}})+"\n").encode()
s.sendall(init); time.sleep(0.1)
s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
time.sleep(0.1)
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
except BlockingIOError: pass
s.setblocking(True)
# Pack two tools/list frames in one send.
f1 = json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list"})+"\n"
f2 = json.dumps({"jsonrpc":"2.0","id":3,"method":"tools/list"})+"\n"
s.sendall((f1+f2).encode())
time.sleep(0.4)
data = b""
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
        data += c
except BlockingIOError: pass
s.close()
import sys, json as j
ids = []
for ln in data.decode().splitlines():
    try:
        m = j.loads(ln)
        if isinstance(m,dict) and "id" in m: ids.append(m["id"])
    except: pass
print(f"response ids = {ids}")
sys.exit(0 if 2 in ids and 3 in ids else 1)
EOF
'

rt_run "P5: 1 MiB single frame (large but newline-terminated)" \
    bash -c '
python3 <<EOF
import socket, json, os, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(10); s.connect(os.environ["BOSUN_MCP_SOCK"])
init = (json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"L6","version":"0"}}})+"\n").encode()
s.sendall(init); time.sleep(0.1)
s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
time.sleep(0.1)
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
except BlockingIOError: pass
s.setblocking(True)
# 1 MiB frame: tools/list with massive _noise field.
noise = "Z" * (1024*1024)
f = json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list","_noise":noise})+"\n"
s.sendall(f.encode())
time.sleep(1.0)
data = b""
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
        data += c
except BlockingIOError: pass
s.close()
import json as j, sys
ok=False
for ln in data.decode().splitlines():
    try:
        m=j.loads(ln)
        if isinstance(m,dict) and m.get("id")==2: ok=True
    except: pass
print(f"got {len(data)} bytes back, id=2 response = {ok}")
sys.exit(0 if ok else 1)
EOF
'

rt_run "P6: UTF-8 BOM prefix on frame (\\xef\\xbb\\xbf{...}\\n)" \
    bash -c '
python3 <<EOF
import socket, json, os, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5); s.connect(os.environ["BOSUN_MCP_SOCK"])
init = (json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"L6","version":"0"}}})+"\n").encode()
s.sendall(init); time.sleep(0.1)
s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
time.sleep(0.1)
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
except BlockingIOError: pass
s.setblocking(True)
# Send tools/list with BOM prefix.
bom = b"\xef\xbb\xbf"
f = json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list"})+"\n"
s.sendall(bom + f.encode())
time.sleep(0.4)
data = b""
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
        data += c
except BlockingIOError: pass
s.close()
print(f"BOM probe — got {len(data)} bytes back")
print(data.decode(errors="replace")[:500])
EOF
'

rt_run "P7: JSON-RPC id as float (id=2.5)" \
    bash -c '
python3 /tmp/bosun-redteam/harness/mcp_sock.py raw \
    "{\"jsonrpc\":\"2.0\",\"id\":2.5,\"method\":\"tools/list\"}" 2>&1
'

rt_run "P7b: JSON-RPC id as null (legal per spec for notification, but tools/list expects a response)" \
    bash -c '
python3 /tmp/bosun-redteam/harness/mcp_sock.py raw \
    "{\"jsonrpc\":\"2.0\",\"id\":null,\"method\":\"tools/list\"}" 2>&1
'

rt_run "P8: malformed JSON frame (bare {)" \
    bash -c '
python3 /tmp/bosun-redteam/harness/mcp_sock.py raw "{" 2>&1
'

rt_run "P8b: trailing-comma JSON (RFC-noncompliant — Go json strict?)" \
    bash -c '
python3 /tmp/bosun-redteam/harness/mcp_sock.py raw \
    "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",}" 2>&1
'

#########################################################
rt_section "L6b — Schema validation edges"
#########################################################

rt_run "S1: bosun_attach with embedded NUL in session label" \
    bash -c 'python3 /tmp/bosun-redteam/harness/mcp_sock.py call bosun_attach "{\"pid\":2,\"session\":\"sess\\u0000ion-1\"}" 2>&1 | summarize'

rt_run "S2: bosun_attach with wrong type — pid='two' (string for int)" \
    bash -c 'python3 /tmp/bosun-redteam/harness/mcp_sock.py call bosun_attach "{\"pid\":\"two\",\"session\":\"session-1\"}" 2>&1'

rt_run "S2b: bosun_attach with wrong type — session=1 (int for string)" \
    bash -c 'python3 /tmp/bosun-redteam/harness/mcp_sock.py call bosun_attach "{\"pid\":2,\"session\":1}" 2>&1'

rt_run "S3: bosun_attach with empty session string" \
    bash -c 'python3 /tmp/bosun-redteam/harness/mcp_sock.py call bosun_attach "{\"pid\":2,\"session\":\"\"}" 2>&1'

rt_run "S4: deeply nested JSON in field (200 levels of nesting)" \
    bash -c '
python3 -c "
import json
o = 1
for _ in range(200):
    o = {\"x\": o}
print(json.dumps({\"session\": \"session-1\", \"pid\": 2, \"_noise\": o}))" | \
    python3 /tmp/bosun-redteam/harness/mcp_sock.py call bosun_attach - 2>&1 | head -10
'

rt_run "S4b: very deeply nested JSON (50,000 levels)" \
    bash -c '
python3 -c "
import json
n = 50000
s = (\"{\\\"x\\\":\" * n) + \"1\" + (\"}\" * n)
print(\"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"id\\\":2,\\\"method\\\":\\\"tools/list\\\",\\\"_noise\\\":\" + s + \"}\")
" | python3 /tmp/bosun-redteam/harness/mcp_sock.py raw - 2>&1 | head -10
'

rt_run "S5: duplicate JSON keys (pid:1 then pid:2 — last-wins or first-wins?)" \
    bash -c '
python3 /tmp/bosun-redteam/harness/mcp_sock.py raw \
    "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"bosun_attach\",\"arguments\":{\"session\":\"session-1\",\"pid\":1,\"pid\":2}}}" 2>&1
echo "---"
echo "Now what does the disk say?"
cat /tmp/bosun-redteam-L6/test-repo/.bosun/state/session-1.attached-pid 2>&1 || echo "no attached-pid"
'

rt_run "S6: bosun_check additionalProperties on top-level (already covered by L2 — but test deep nesting)" \
    call_tool bosun_check '{"paths":["x"],"_extra":{"a":{"b":1}}}'

#########################################################
rt_section "L6c — Tool-name surface (the namespaced-form is the main probe)"
#########################################################

rt_run "T1: tool name with wrong case (bosun_Attach) — should fail" \
    call_tool bosun_Attach '{"pid":2,"session":"session-1"}'

rt_run "T2: Claude Code namespaced form (mcp__bosun__attach) — should fail at bare bosun" \
    call_tool mcp__bosun__attach '{"pid":2,"session":"session-1"}'

rt_run "T3: empty tool name" \
    call_tool '' '{"pid":2,"session":"session-1"}'

rt_run "T4: substring of a real tool name (bosun_a) — error shape" \
    call_tool bosun_a '{}'

#########################################################
rt_section "L6d — Concurrent in-session"
#########################################################

rt_run "C1: 10 concurrent tools/call in one socket session — all answered? ids preserved?" \
    bash -c '
python3 <<EOF
import socket, json, os, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(10); s.connect(os.environ["BOSUN_MCP_SOCK"])
init = (json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"L6","version":"0"}}})+"\n").encode()
s.sendall(init); time.sleep(0.1)
s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
time.sleep(0.1)
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
except BlockingIOError: pass
s.setblocking(True)
# Send 10 tools/list with ids 10..19 back-to-back as one send.
batch = "".join(json.dumps({"jsonrpc":"2.0","id":10+i,"method":"tools/list"})+"\n" for i in range(10))
s.sendall(batch.encode())
time.sleep(1.5)
data = b""
s.setblocking(False)
try:
    while True:
        c = s.recv(65536)
        if not c: break
        data += c
except BlockingIOError: pass
s.close()
ids = []
for ln in data.decode().splitlines():
    try:
        m = json.loads(ln)
        if "id" in m: ids.append(m["id"])
    except: pass
print(f"ids received (order): {ids}")
import sys
sys.exit(0 if sorted(ids) == list(range(10,20)) else 1)
EOF
'

rt_run "C2: mid-session reconnect — close, reopen, send tools/list (per-conn state?)" \
    bash -c '
python3 <<EOF
import socket, json, os, time
sock_path = os.environ["BOSUN_MCP_SOCK"]
def go(label):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(5); s.connect(sock_path)
    s.sendall((json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":label,"version":"0"}}})+"\n").encode())
    time.sleep(0.1)
    s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
    s.sendall((json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/list"})+"\n").encode())
    time.sleep(0.3)
    data = b""
    s.setblocking(False)
    try:
        while True:
            c=s.recv(65536)
            if not c: break
            data += c
    except BlockingIOError: pass
    s.close()
    return data
for i in range(3):
    d = go(f"recon-{i}")
    n = len([l for l in d.decode().splitlines() if l.strip()])
    print(f"  reconnect {i}: {n} response lines")
EOF
'

#########################################################
rt_section "L6e — F007 follow-up: on-disk side effects + cleanup confusion"
#########################################################

rt_run "F7a: attach pid=99999999 then inspect .bosun/state/session-1.attached-pid" \
    bash -c '
# Helper that does NOT half-close (L4 lesson).
python3 <<EOF
import socket, json, os, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5); s.connect(os.environ["BOSUN_MCP_SOCK"])
s.sendall((json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"L6","version":"0"}}})+"\n").encode())
time.sleep(0.1)
s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
s.sendall((json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bosun_attach","arguments":{"pid":99999999,"session":"session-1"}}})+"\n").encode())
time.sleep(0.4)
data = b""
s.setblocking(False)
try:
    while True:
        c=s.recv(65536)
        if not c: break
        data += c
except BlockingIOError: pass
s.close()
print("response:")
for ln in data.decode().splitlines():
    if ln.strip(): print(" ", ln[:200])
EOF
echo "---"
echo "Disk:"
cat /tmp/bosun-redteam-L6/test-repo/.bosun/state/session-1.attached-pid 2>&1 || echo "no .attached-pid"
'

rt_run "F7b: bosun status — does it show a phantom worker for pid 99999999?" \
    bash -c "cd $REPO && $BOSUN status 2>&1 | head -30"

rt_run "F7c: bosun cleanup --dry-run — does it refuse / warn?" \
    bash -c "cd $REPO && $BOSUN cleanup --dry-run 2>&1 | head -30"

rt_run "F7d: After attach with valid-looking PID=\$\$ (this shell), kill shell, does daemon notice?" \
    bash -c '
# We attach $$ then exit; the test harness child shell PID becomes stale.
PID=$$
python3 <<EOF
import socket, json, os, time
s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(5); s.connect(os.environ["BOSUN_MCP_SOCK"])
s.sendall((json.dumps({"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"L6","version":"0"}}})+"\n").encode())
time.sleep(0.1)
s.sendall((json.dumps({"jsonrpc":"2.0","method":"notifications/initialized"})+"\n").encode())
s.sendall((json.dumps({"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"bosun_attach","arguments":{"pid":'$PID',"session":"session-2"}}})+"\n").encode())
time.sleep(0.4)
data = b""
s.setblocking(False)
try:
    while True:
        c=s.recv(65536)
        if not c: break
        data += c
except BlockingIOError: pass
s.close()
print("attach response captured")
EOF
echo "---"
echo "Disk on session-2:"
cat '$REPO'/.bosun/state/session-2.attached-pid 2>&1 || echo "no .attached-pid"
'

rt_summary
