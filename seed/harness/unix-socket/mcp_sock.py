#!/usr/bin/env python3
"""Bosun MCP unix-socket client — JSON-RPC newline-framed.

Mirrors projectdogwalker/harness/mcp.py but speaks unix-domain socket
instead of stdio (bosun's MCP server binds to <repo>/.bosun/mcp.sock).

Usage:
    mcp_sock.py list                         # tools/list
    mcp_sock.py call <tool> '<json-args>'    # one tools/call
    mcp_sock.py raw '<frame>' [...]          # arbitrary frames after handshake
    mcp_sock.py rawnohandshake '<frame>' ... # frames without initialize

Env:
    BOSUN_MCP_SOCK   override socket path (default: <PWD>/.bosun/mcp.sock)
    MCP_TIMEOUT      per-session wall-clock seconds (default 10)
    MCP_LOG          append-only JSONL transcript path

Pass '-' as args to read JSON from stdin (escapes ARG_MAX for huge payloads).
"""
import json
import os
import socket
import sys
import time

SOCK_PATH = os.environ.get(
    "BOSUN_MCP_SOCK",
    os.path.join(os.getcwd(), ".bosun", "mcp.sock"),
)
TIMEOUT = float(os.environ.get("MCP_TIMEOUT", "10"))
LOGPATH = os.environ.get("MCP_LOG", "")
PROTOCOL_VERSION = "2025-06-18"


def _log(direction, frame):
    if not LOGPATH:
        return
    rec = {"ts": time.time(), "dir": direction, "frame": frame}
    with open(LOGPATH, "a") as f:
        f.write(json.dumps(rec) + "\n")


def run_session(frames, do_handshake=True):
    """Open a single socket connection, drive frames, collect responses."""
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.settimeout(TIMEOUT)
    try:
        s.connect(SOCK_PATH)
    except Exception as e:
        return [], f"connect: {e}", None, False

    def send(obj):
        line = obj if isinstance(obj, str) else json.dumps(obj)
        _log("send", line)
        try:
            s.sendall((line + "\n").encode())
        except BrokenPipeError:
            pass

    if do_handshake:
        send({
            "jsonrpc": "2.0", "id": 1, "method": "initialize",
            "params": {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "redteam", "version": "0.1"},
            },
        })
        time.sleep(0.15)
        send({"jsonrpc": "2.0", "method": "notifications/initialized"})
        time.sleep(0.10)

    for fr in frames:
        send(fr)
        time.sleep(0.05)

    # NOTE: do NOT shutdown(SHUT_WR) — L4 of this campaign found that bosun's
    # go-mcp SDK runtime tears down the connection on client-EOF before flushing
    # responses for Derive-gated tools (attach/usage/done). We just drain until
    # the deadline; the server keeps its half of the connection ready to flush.
    data = b""
    deadline = time.time() + TIMEOUT
    timed_out = False
    # Adaptive per-recv timeout: after we've seen *some* response, drop the
    # per-recv timeout to 0.5s so we exit promptly when the server is idle
    # (it doesn't half-close). Before any data, wait the full deadline.
    while time.time() < deadline:
        remaining = max(0.05, deadline - time.time())
        s.settimeout(min(remaining, 0.5 if data else remaining))
        try:
            chunk = s.recv(65536)
        except socket.timeout:
            if data:
                # Got our response (or some); idle now, stop.
                break
            timed_out = True
            break
        if not chunk:
            break
        data += chunk
        if len(data) > 16 * 1024 * 1024:
            break
    s.close()

    parsed = []
    for ln in data.decode(errors="replace").splitlines():
        if not ln.strip():
            continue
        _log("recv", ln)
        try:
            parsed.append(json.loads(ln))
        except json.JSONDecodeError:
            parsed.append({"_raw": ln})
    return parsed, "", 0, timed_out


def _find(responses, want_id):
    for r in responses:
        if isinstance(r, dict) and r.get("id") == want_id:
            return r
    return None


def cmd_list():
    resp, err, _, to = run_session([{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}])
    if to:
        print("TIMEOUT", file=sys.stderr); return 2
    if err:
        print("transport:", err, file=sys.stderr); return 3
    r = _find(resp, 2)
    if not r or "result" not in r:
        print("no tools/list result", file=sys.stderr)
        print(json.dumps(resp, indent=2))
        return 3
    print("\n".join(t["name"] for t in r["result"].get("tools", [])))
    return 0


def cmd_call(tool, args_json):
    if args_json == "-":
        args_json = sys.stdin.read()
    elif args_json.startswith("@"):
        with open(args_json[1:]) as f:
            args_json = f.read()
    try:
        args = json.loads(args_json) if args_json else {}
    except json.JSONDecodeError as e:
        print(f"bad json args: {e}", file=sys.stderr); return 3
    resp, err, _, to = run_session([
        {"jsonrpc": "2.0", "id": 2, "method": "tools/call",
         "params": {"name": tool, "arguments": args}},
    ])
    if to:
        print("TIMEOUT", file=sys.stderr); return 2
    if err:
        print("transport:", err, file=sys.stderr); return 3
    r = _find(resp, 2)
    if not r:
        print("NO RESPONSE for id=2", file=sys.stderr)
        print(json.dumps(resp, indent=2), file=sys.stderr); return 3
    print(json.dumps(r, indent=2))
    if "error" in r:
        return 4
    if isinstance(r.get("result"), dict) and r["result"].get("isError"):
        return 4
    return 0


def cmd_raw(frames, do_handshake=True):
    resp, err, _, to = run_session(list(frames), do_handshake=do_handshake)
    out = {"timed_out": to, "transport_error": err, "responses": resp}
    print(json.dumps(out, indent=2))
    return 2 if to else (3 if err else 0)


def main(argv):
    if len(argv) < 2:
        print(__doc__); return 1
    sub = argv[1]
    if sub == "list":           return cmd_list()
    if sub == "call":           return cmd_call(argv[2], argv[3] if len(argv) > 3 else "{}")
    if sub == "raw":            return cmd_raw(argv[2:], do_handshake=True)
    if sub == "rawnohandshake": return cmd_raw(argv[2:], do_handshake=False)
    print(f"unknown subcommand: {sub}", file=sys.stderr); print(__doc__); return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
