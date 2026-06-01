#!/usr/bin/env python3
"""Pretty-print the verify_claim JSON-RPC response on stdin. One line per claim
plus a summary line."""
import json, sys
try:
    r = json.load(sys.stdin)
except Exception as e:
    print("PARSE-ERROR:", e); sys.exit(0)
if r.get("error"):
    print("RPC-ERROR:", r["error"].get("message"), "code:", r["error"].get("code")); sys.exit(0)
res = r.get("result", {})
sc = res.get("structuredContent") or {}
sm = sc.get("summary", {})
print("summary: total=%d verified=%d unverified=%d forbidden=%d opinion=%d" % (
    sm.get("total", 0), sm.get("verified", 0), sm.get("unverified", 0),
    sm.get("forbidden", 0), sm.get("opinion", 0),
))
for c in sc.get("claims", []) or []:
    print("  - %-10s %-40r path=%s" % (
        c.get("verdict"),
        (c.get("text") or "")[:38],
        c.get("rule_path") or c.get("fact_path") or "—",
    ))
