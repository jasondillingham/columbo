#!/usr/bin/env bash
# L5-http.sh — Lane L5 (bosun serve HTTP hardening) probes for bughunt-1.
#
# Scope: verify v0.12 Bundle C contracts (X-Frame-Options, X-Content-Type-Options,
# Referrer-Policy, Content-Security-Policy, MaxBytesReader) and push past them
# for issues Bundle C didn't address: Host-header validation (DNS rebinding),
# slowloris-style DoS via SSE conn cap exhaustion, missing IdleTimeout, and the
# observation that the body cap is unreachable in v0.12 (all handlers are GET).
#
# Usage:
#   bash /tmp/bosun-redteam/harness/lanes/L5-http.sh
#
# Pre-req: a sandbox repo at /tmp/bosun-redteam-L5/test-repo with `bosun init`
# already run, and a `bosun_test serve` binary at /tmp/bosun_test.

set -u
export RT_ROOT="${RT_ROOT:-/tmp/bosun-redteam}"
# shellcheck disable=SC1091
source /tmp/bosun-redteam/harness/rt.sh

BOSUN_BIN="${BOSUN_BIN:-/tmp/bosun_test}"
SANDBOX="/tmp/bosun-redteam-L5/test-repo"
PORT="${PORT:-18080}"
BASE="http://127.0.0.1:$PORT"

rt_init L5-http

# ------------------------------------------------------------
# Sandbox / serve bootstrap
# ------------------------------------------------------------

l5_ensure_serve() {
    if curl -sS --max-time 1 -o /dev/null "$BASE/" 2>/dev/null; then
        return 0
    fi
    if [ ! -d "$SANDBOX/.git" ]; then
        mkdir -p /tmp/bosun-redteam-L5
        rm -rf "$SANDBOX"
        mkdir -p "$SANDBOX"
        ( cd "$SANDBOX" \
            && git init --quiet \
            && git config user.email "L5@local" \
            && git config user.name "L5" \
            && echo "# L5" > README.md \
            && git add README.md \
            && git commit -m "init" --quiet \
            && "$BOSUN_BIN" init >/dev/null 2>&1 )
    fi
    ( cd "$SANDBOX" && "$BOSUN_BIN" serve --bind 127.0.0.1 --port "$PORT" > /tmp/l5_serve.log 2>&1 & )
    sleep 1.0
}

export -f l5_ensure_serve

# ------------------------------------------------------------
# Helpers
# ------------------------------------------------------------

# Confirm all 4 Bundle C headers present in response headers
l5_check_headers() {
    local label="$1"; shift
    local hdrs
    hdrs="$(curl -sS -D - -o /dev/null --max-time 5 "$@" 2>&1)" || { echo "$label: curl failed"; return 1; }
    local missing=()
    grep -qiE '^X-Frame-Options: DENY' <<<"$hdrs" || missing+=("X-Frame-Options")
    grep -qiE '^X-Content-Type-Options: nosniff' <<<"$hdrs" || missing+=("X-Content-Type-Options")
    grep -qiE '^Referrer-Policy: no-referrer' <<<"$hdrs" || missing+=("Referrer-Policy")
    grep -qiE '^Content-Security-Policy: ' <<<"$hdrs" || missing+=("Content-Security-Policy")
    if [ ${#missing[@]} -eq 0 ]; then
        echo "$label: all 4 Bundle C headers present"
        return 0
    else
        echo "$label: MISSING ${missing[*]}"
        return 1
    fi
}
export -f l5_check_headers

# Curl a URL with an evil Host header and print status + first line of body
l5_evil_host() {
    local host="$1" path="$2"
    curl -sS --max-time 5 -o /tmp/l5_body.tmp -w "HTTP %{http_code} (%{size_download} bytes)\n" \
         -H "Host: $host" "$BASE$path"
    head -c 240 /tmp/l5_body.tmp; echo
}
export -f l5_evil_host

# ------------------------------------------------------------
# Probes
# ------------------------------------------------------------

l5_ensure_serve

rt_section "Bundle C header presence across response shapes"
rt_run "GET /"                          l5_check_headers "GET /"                                     "$BASE/"
rt_run "GET /nonexistent (404)"         l5_check_headers "GET /nonexistent"                          "$BASE/nonexistent"
rt_run "HEAD / (405)"                   l5_check_headers "HEAD /"                                    -I "$BASE/"
rt_run "POST / (405)"                   l5_check_headers "POST /"                                    -X POST "$BASE/"
rt_run "OPTIONS / (405)"                l5_check_headers "OPTIONS /"                                 -X OPTIONS "$BASE/"
rt_run "TRACE / (405)"                  l5_check_headers "TRACE /"                                   -X TRACE "$BASE/"
rt_run "GET /api/status (200)"          l5_check_headers "GET /api/status"                           "$BASE/api/status"
rt_run "GET /api/show/zzz (404)"        l5_check_headers "GET /api/show/zzz"                         "$BASE/api/show/zzz"
rt_run "GET /api/show/bad%20label (400)" l5_check_headers "GET /api/show/bad label"                  "$BASE/api/show/bad%20label"
rt_run "GET /api/show/ (404 sub-path empty)" l5_check_headers "GET /api/show/"                       "$BASE/api/show/"
rt_run "GET /api/events (200 SSE)"      l5_check_headers "GET /api/events"                           --max-time 1 "$BASE/api/events"

rt_section "Host-header validation — DNS rebinding vector"
rt_run "Host: evil.com -> /api/status"          l5_evil_host "evil.com"          "/api/status"
rt_run "Host: attacker.com -> /api/show/session-1" l5_evil_host "attacker.com"   "/api/show/session-1"
rt_run "Host: evil.com -> /"                    l5_evil_host "evil.com"          "/"
rt_run "Host: 127.0.0.1.nip.io -> /api/status"  l5_evil_host "127.0.0.1.nip.io"  "/api/status"

rt_section "Origin / Referer enforcement (none expected, log for the record)"
rt_run "Origin: https://evil.com -> /api/status" curl -sS -D - -o /dev/null --max-time 5 \
    -H "Origin: https://evil.com" "$BASE/api/status"
rt_run "Referer: https://evil.com/ -> /api/show/session-1" curl -sS -D - -o /dev/null --max-time 5 \
    -H "Referer: https://evil.com/" "$BASE/api/show/session-1"

rt_section "Body cap (MaxBytesReader) reachability"
# All v0.12 handlers reject non-GET methods before reading the body, so the
# 1 MiB cap is dead code in this release. Document with one probe that
# confirms a POST returns 405 *without* the body being read.
rt_run "POST 1MiB+100 to / (handler 405s before reading body)" bash -c '
    python3 -c "import sys; sys.stdout.write(\"x\"*(1024*1024+100))" > /tmp/l5_big_body
    curl -sS -D - -o /dev/null --max-time 5 -X POST --data-binary @/tmp/l5_big_body http://127.0.0.1:18080/api/status | head -3
'

rt_section "Slowloris / SSE conn-cap DoS"
rt_run "Open 64 SSE conns + try 65th legitimate request" bash -c '
python3 <<EOF
import socket, time
conns = []
for i in range(64):
    s = socket.socket(); s.settimeout(2.0)
    s.connect(("127.0.0.1", 18080))
    s.sendall(b"GET /api/events HTTP/1.1\r\nHost: localhost\r\nAccept: text/event-stream\r\n\r\n")
    conns.append(s)
print(f"opened {len(conns)} SSE conns")
s = socket.socket(); s.settimeout(3.0)
try:
    t0=time.time(); s.connect(("127.0.0.1", 18080))
    s.sendall(b"GET /api/status HTTP/1.1\r\nHost: localhost\r\n\r\n")
    d = s.recv(2048); print(f"65th got response in {time.time()-t0:.2f}s")
except socket.timeout:
    print("65th TIMED OUT -- DoS confirmed (no read/idle/write timeouts on SSE)")
finally:
    s.close()
for c in conns: c.close()
EOF'

rt_section "Read/Write/Idle timeout behavior"
rt_run "ReadHeaderTimeout fires at 10s on incomplete request" bash -c '
python3 <<EOF
import socket, time
s = socket.socket(); s.settimeout(15.0); s.connect(("127.0.0.1", 18080))
s.sendall(b"GET / HTTP/1.1\r\nHost: localhost\r\n")  # no trailing CRLF -- incomplete
t0=time.time()
try:
    d = s.recv(2048); print(f"got {len(d)}B after {time.time()-t0:.1f}s: {d[:80]!r}")
except Exception as e:
    print(f"after {time.time()-t0:.1f}s: {e}")
EOF'
rt_run "Keep-alive holds conn idle across 4s (no IdleTimeout set)" bash -c '
python3 <<EOF
import socket, time
s = socket.socket(); s.settimeout(8.0); s.connect(("127.0.0.1", 18080))
s.sendall(b"GET /api/status HTTP/1.1\r\nHost: localhost\r\nConnection: keep-alive\r\n\r\n")
d=b""
while b"\r\n\r\n" not in d: d += s.recv(4096)
print("first response ok"); time.sleep(4)
s.sendall(b"GET /api/status HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
d = s.recv(4096); print(f"after 4s idle, got {len(d)} bytes back -- conn was kept alive")
EOF'

rt_section "Header injection (CRLF-in-value smoke test)"
rt_run "Inject Set-Cookie via second header line"  bash -c '
python3 <<EOF
import socket
s = socket.socket(); s.connect(("127.0.0.1", 18080))
s.sendall(b"GET / HTTP/1.1\r\nHost: localhost\r\nX-Inject: foo\r\nSet-Cookie: hijack=1\r\n\r\n")
d = s.recv(2048); print(d[:300].decode(errors="replace"))
EOF'

rt_summary

echo ""
echo "Lane L5 complete. See:"
echo "  runlog: $RT_RUNLOG"
echo "  findings: /tmp/bosun-redteam/findings/L5-findings.md"
