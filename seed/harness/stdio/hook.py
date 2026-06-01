#!/usr/bin/env python3
"""Leonard hook driver — synthesize Claude Code hook payloads and feed them to
`leonard-hook <event>` directly. Used to stress the hook dispatcher without
needing a real Claude Code session.

Subcommands mirror leonard-hook events:
    hook.py pre-edit  <Tool> <file_path> [<content_or_command>]
    hook.py post-edit <Tool> <file_path> [<content_or_command>] [<vet_ok>]
    hook.py session-start
    hook.py stop
    hook.py raw <event> '<json-payload>'    # send arbitrary payload

Tool one of: Write Edit MultiEdit Bash. For Edit: pass new content as old/new
(same string twice → no-op edit; or use 'raw' for full control). For MultiEdit
and adversarial payloads use 'raw'.

Env:
    LEONARD_HOOK  binary path (default ~/go/bin/leonard-hook)
    HOOK_TIMEOUT  seconds (default 10)

Exit code is the hook's exit code. stdout/stderr passthrough. JSON payload is
also printed to stderr prefixed with `>>>` so it's part of the audit trail.
"""
import json
import os
import subprocess
import sys
import uuid

LEONARD_HOOK = os.environ.get("LEONARD_HOOK", os.path.expanduser("~/go/bin/leonard-hook"))
TIMEOUT = float(os.environ.get("HOOK_TIMEOUT", "10"))


def run_hook(event, payload):
    body = json.dumps(payload)
    print(">>>", event, body, file=sys.stderr)
    p = subprocess.run(
        [LEONARD_HOOK, event],
        input=body,
        capture_output=True,
        text=True,
        timeout=TIMEOUT,
    )
    if p.stdout:
        sys.stdout.write(p.stdout)
        if not p.stdout.endswith("\n"):
            sys.stdout.write("\n")
    if p.stderr:
        sys.stderr.write(p.stderr)
        if not p.stderr.endswith("\n"):
            sys.stderr.write("\n")
    return p.returncode


def _tool_input(tool, file_path, blob):
    if tool == "Bash":
        return {"command": blob or "true"}
    if tool == "Write":
        return {"file_path": file_path, "content": blob or ""}
    if tool == "Edit":
        return {"file_path": file_path, "old_string": blob or "", "new_string": blob or ""}
    if tool == "MultiEdit":
        return {"file_path": file_path, "edits": [{"old_string": blob or "", "new_string": blob or ""}]}
    return {"file_path": file_path}


def cmd_pre_edit(argv):
    if len(argv) < 2:
        print("pre-edit needs <Tool> <file_path>", file=sys.stderr); return 1
    tool, path = argv[0], argv[1]
    blob = argv[2] if len(argv) > 2 else ""
    payload = {
        "session_id": f"rt-{uuid.uuid4().hex[:8]}",
        "hook_event_name": "PreToolUse",
        "tool_name": tool,
        "tool_input": _tool_input(tool, path, blob),
        "cwd": os.getcwd(),
    }
    return run_hook("pre-edit", payload)


def cmd_post_edit(argv):
    if len(argv) < 2:
        print("post-edit needs <Tool> <file_path>", file=sys.stderr); return 1
    tool, path = argv[0], argv[1]
    blob = argv[2] if len(argv) > 2 else ""
    payload = {
        "session_id": f"rt-{uuid.uuid4().hex[:8]}",
        "hook_event_name": "PostToolUse",
        "tool_name": tool,
        "tool_input": _tool_input(tool, path, blob),
        "tool_response": {"success": True, "filePath": path},
        "cwd": os.getcwd(),
    }
    return run_hook("post-edit", payload)


def cmd_simple(event):
    payload = {
        "session_id": f"rt-{uuid.uuid4().hex[:8]}",
        "hook_event_name": event.replace("-", ""),
        "cwd": os.getcwd(),
    }
    return run_hook(event, payload)


def cmd_raw(argv):
    if len(argv) < 2:
        print("raw needs <event> '<json>'", file=sys.stderr); return 1
    event = argv[0]
    try:
        payload = json.loads(argv[1])
    except json.JSONDecodeError as e:
        print(f"bad json: {e}", file=sys.stderr); return 1
    return run_hook(event, payload)


def main(argv):
    if len(argv) < 2:
        print(__doc__); return 1
    sub = argv[1]
    if sub == "pre-edit":  return cmd_pre_edit(argv[2:])
    if sub == "post-edit": return cmd_post_edit(argv[2:])
    if sub == "session-start": return cmd_simple("session-start")
    if sub == "stop":          return cmd_simple("stop")
    if sub == "raw":           return cmd_raw(argv[2:])
    print(f"unknown subcommand: {sub}", file=sys.stderr); print(__doc__)
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))
