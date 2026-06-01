#!/usr/bin/env python3
"""Leonard MCP stdio client — red-team driver.

Speaks newline-delimited JSON-RPC 2.0 to a `leonard-mcp` process over stdio.
Built for stress-testing: every call is bounded by a timeout, raw frames can
be injected for protocol fuzzing, and all traffic is optionally teed to a log.

Reusable across projects: point LEONARD_MCP at any leonard-mcp binary and run
from inside a directory that has a `.leonard/` store (the server walks up).

Usage:
    mcp.py list                                  # tools/list
    mcp.py call <tool> '<json-args>'             # one tools/call
    mcp.py raw '<frame1>' '<frame2>' ...         # send arbitrary frames after handshake
    mcp.py rawnohandshake '<frame>' ...          # send frames with NO initialize first

Options (env):
    LEONARD_MCP   path to leonard-mcp (default: ~/go/bin/leonard-mcp)
    MCP_TIMEOUT   per-session wall-clock seconds (default: 15)
    MCP_LOG       append-only JSONL transcript path (default: none)

Exit codes: 0 ok, 2 timeout, 3 transport/protocol error, 4 tool returned isError.
"""
import json
import os
import subprocess
import sys
import threading
import time

LEONARD_MCP = os.environ.get("LEONARD_MCP", os.path.expanduser("~/go/bin/leonard-mcp"))
TIMEOUT = float(os.environ.get("MCP_TIMEOUT", "15"))
LOGPATH = os.environ.get("MCP_LOG", "")

PROTOCOL_VERSION = "2025-06-18"


def _log(direction, payload):
    if not LOGPATH:
        return
    rec = {"ts": time.time(), "dir": direction, "frame": payload}
    with open(LOGPATH, "a") as f:
        f.write(json.dumps(rec) + "\n")


def run_session(frames, do_handshake=True):
    """Spawn leonard-mcp, optionally handshake, send `frames` (list of dicts or
    raw strings), collect every stdout line as a parsed-or-raw response within
    TIMEOUT. Returns (responses, stderr, exit_code, timed_out)."""
    proc = subprocess.Popen(
        [LEONARD_MCP],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
    )

    out_lines = []
    reader_done = threading.Event()

    def reader():
        for line in proc.stdout:
            line = line.rstrip("\n")
            if line:
                out_lines.append(line)
                _log("recv", line)
        reader_done.set()

    t = threading.Thread(target=reader, daemon=True)
    t.start()

    def send(obj):
        s = obj if isinstance(obj, str) else json.dumps(obj)
        _log("send", s)
        proc.stdin.write(s + "\n")
        proc.stdin.flush()

    timed_out = False
    try:
        if do_handshake:
            send({
                "jsonrpc": "2.0", "id": 1, "method": "initialize",
                "params": {
                    "protocolVersion": PROTOCOL_VERSION,
                    "capabilities": {},
                    "clientInfo": {"name": "redteam", "version": "0.1"},
                },
            })
            time.sleep(0.25)
            send({"jsonrpc": "2.0", "method": "notifications/initialized"})
            time.sleep(0.1)
        for fr in frames:
            send(fr)
            time.sleep(0.05)
        time.sleep(0.4)  # drain window
        proc.stdin.close()
    except BrokenPipeError:
        pass

    deadline = time.time() + TIMEOUT
    while time.time() < deadline:
        if proc.poll() is not None and reader_done.is_set():
            break
        time.sleep(0.05)
    else:
        timed_out = True

    if proc.poll() is None:
        proc.kill()
    try:
        proc.wait(timeout=2)
    except subprocess.TimeoutExpired:
        pass

    stderr = proc.stderr.read() if proc.stderr else ""
    parsed = []
    for ln in out_lines:
        try:
            parsed.append(json.loads(ln))
        except json.JSONDecodeError:
            parsed.append({"_raw": ln})
    return parsed, stderr, proc.returncode, timed_out


def _find_result(responses, want_id):
    for r in responses:
        if isinstance(r, dict) and r.get("id") == want_id:
            return r
    return None


def cmd_list():
    frames = [{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}]
    responses, stderr, rc, to = run_session(frames)
    if to:
        print("TIMEOUT", file=sys.stderr); print(stderr, file=sys.stderr); return 2
    res = _find_result(responses, 2)
    if not res or "result" not in res:
        print("no tools/list result; stderr:", stderr, file=sys.stderr)
        print(json.dumps(responses, indent=2)); return 3
    names = [t["name"] for t in res["result"].get("tools", [])]
    print("\n".join(names))
    return 0


def cmd_call(tool, args_json):
    # Passing huge strings on argv hits ARG_MAX. Accept '-' to read JSON
    # from stdin instead, and '@FILE' to read from a file. Otherwise treat
    # args_json as the literal JSON text.
    if args_json == "-":
        args_json = sys.stdin.read()
    elif args_json.startswith("@"):
        with open(args_json[1:]) as f:
            args_json = f.read()
    try:
        args = json.loads(args_json) if args_json else {}
    except json.JSONDecodeError as e:
        print(f"bad json args: {e}", file=sys.stderr); return 3
    frames = [{
        "jsonrpc": "2.0", "id": 2, "method": "tools/call",
        "params": {"name": tool, "arguments": args},
    }]
    responses, stderr, rc, to = run_session(frames)
    if to:
        print("TIMEOUT after %.1fs" % TIMEOUT, file=sys.stderr)
        if stderr:
            print("stderr:", stderr, file=sys.stderr)
        return 2
    res = _find_result(responses, 2)
    if res is None:
        print("NO RESPONSE for id=2", file=sys.stderr)
        print("all responses:", json.dumps(responses, indent=2), file=sys.stderr)
        if stderr:
            print("stderr:", stderr, file=sys.stderr)
        return 3
    print(json.dumps(res, indent=2))
    if "error" in res:
        return 4
    if isinstance(res.get("result"), dict) and res["result"].get("isError"):
        return 4
    return 0


def cmd_raw(frames, do_handshake=True):
    responses, stderr, rc, to = run_session(list(frames), do_handshake=do_handshake)
    out = {
        "timed_out": to,
        "exit_code": rc,
        "stderr": stderr,
        "responses": responses,
    }
    print(json.dumps(out, indent=2))
    return 2 if to else 0


def main(argv):
    if len(argv) < 2:
        print(__doc__); return 1
    sub = argv[1]
    if sub == "list":
        return cmd_list()
    if sub == "call":
        if len(argv) < 3:
            print("usage: mcp.py call <tool> '<json-args>'", file=sys.stderr); return 1
        return cmd_call(argv[2], argv[3] if len(argv) > 3 else "{}")
    if sub == "raw":
        return cmd_raw(argv[2:], do_handshake=True)
    if sub == "rawnohandshake":
        return cmd_raw(argv[2:], do_handshake=False)
    print(f"unknown subcommand: {sub}", file=sys.stderr)
    print(__doc__)
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
