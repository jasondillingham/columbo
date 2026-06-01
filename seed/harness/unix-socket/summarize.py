#!/usr/bin/env python3
"""Compact verdict-line printer for a JSON-RPC tools/call response on stdin."""
import json, sys
try:
    r = json.load(sys.stdin)
except Exception as e:
    print(f"parse-error: {e}"); sys.exit(0)
res = r.get("result")
if r.get("error"):
    err = r["error"]
    code = err.get("code")
    msg = err.get("message", "")[:140]
    print(f"RPC-ERROR code={code} msg={msg!r}")
    sys.exit(0)
if not res:
    print("no result"); sys.exit(0)
is_err = res.get("isError")
content = res.get("content") or []
txt = content[0].get("text", "")[:160] if content else ""
sc = res.get("structuredContent")
prefix = "ERR" if is_err else "OK "
print(f"{prefix} content[0]={txt!r}")
if sc and not is_err:
    print(f"    structured: {json.dumps(sc)[:160]}")
