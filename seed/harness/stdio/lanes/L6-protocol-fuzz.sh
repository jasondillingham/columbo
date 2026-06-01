#!/usr/bin/env bash
# L6 — MCP JSON-RPC protocol fuzz.
#
# Pushes leonard-mcp's stdio JSON-RPC layer past spec compliance.  Focus areas:
#   - Error-code catalog (F008 follow-on across many failure classes)
#   - Initialize state-machine invariants (pre-init calls, double init, wrong protocolVersion)
#   - Missing/wrong required JSON-RPC fields (no jsonrpc, no method, wrong jsonrpc value)
#   - Batch requests (JSON-RPC 2.0 §6 — array-shaped frames)
#   - additionalProperties:false enforcement (schemas all declare it)
#   - Notification handling (request-shaped notification, cancelled-with-id, wrong-id cancellation)
#   - Duplicate-id requests in flight
#   - Trailing data / multiple frames in one line
#
# Each probe records its raw JSON-RPC response in the runlog so the next audit
# can recover the exact wire-level shape without re-running.

set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L6-protocol-fuzz

# --- helper: extract_errors  (python script lives in /tmp so heredoc-stdin-
# replacement bug doesn't eat the JSON pipe — see lane preamble below). -----

EXTRACT_PY="/tmp/l6_extract_errors.py"
cat > "$EXTRACT_PY" <<'PY'
"""Read JSON (a single mcp.py response object OR a bare JSON-RPC frame OR a
list) on stdin, print one line per response in `id=N  code=...  msg=...` form."""
import json, sys
raw = sys.stdin.read()
try:
    data = json.loads(raw)
except json.JSONDecodeError as e:
    print(f"  PARSE-ERROR: {e} -- first 200 chars: {raw[:200]!r}")
    sys.exit(0)

if isinstance(data, dict) and "responses" in data:
    responses = data["responses"]
    if data.get("timed_out"):
        print(f"  [timed_out=True after harness timeout]")
    if data.get("stderr"):
        print(f"  [stderr: {data['stderr'][:200]!r}]")
elif isinstance(data, dict):
    responses = [data]
elif isinstance(data, list):
    responses = data
else:
    responses = []

print(f"  ({len(responses)} response(s))")
for r in responses:
    if not isinstance(r, dict):
        print(f"  raw: {r!r}")
        continue
    rid = r.get("id", "<no-id>")
    if "_raw" in r:
        print(f"  raw-line: {r['_raw']!r}")
        continue
    if "error" in r:
        e = r["error"]
        print(f"  id={rid}  code={e.get('code')!r:>10}  msg={e.get('message')!r}")
    elif "result" in r:
        res = r["result"]
        if isinstance(res, dict) and res.get("isError"):
            content = res.get("content", [])
            msg = content[0].get("text", "<no text>") if content else "<no content>"
            print(f"  id={rid}  TOOL-ERROR  text={msg!r}")
        else:
            keys = list(res.keys()) if isinstance(res, dict) else type(res).__name__
            print(f"  id={rid}  result-ok  keys={keys}")
    else:
        print(f"  id={rid}  unknown-shape  keys={list(r.keys())}")
PY

extract_errors() { python3 "$EXTRACT_PY"; }
export EXTRACT_PY
export -f extract_errors

# ---------- L6a — Error-code catalog (F008 follow-on) ----------

rt_section "L6a — Error-code catalog: provoke every failure class, catalog (code,message)"

rt_note "We collect codes from JSON-RPC errors AND from MCP tool errors (result.isError). The JSON-RPC spec reserves: -32700 parse, -32600 invalid request, -32601 method not found, -32602 invalid params, -32603 internal, -32000..-32099 server-defined."

# Pre-init tool call (we know F008 returns code:0 here — confirm baseline)
rt_run "[catalog] pre-init tools/call → expect code:0 per F008" bash -c "
    python3 harness/mcp.py rawnohandshake \
      '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"list_files\",\"arguments\":{}}}' \
      | extract_errors
"

rt_run "[catalog] unknown method → JSON-RPC -32601 method-not-found expected" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"nope/does_not_exist\",\"params\":{}}' \
      | extract_errors
"

rt_run "[catalog] unknown tool name in tools/call → -32602 invalid-params expected" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":{\"name\":\"no_such_tool\",\"arguments\":{}}}' \
      | extract_errors
"

rt_run "[catalog] missing required arg (verify_symbol w/o name) → -32602 expected" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":4,\"method\":\"tools/call\",\"params\":{\"name\":\"verify_symbol\",\"arguments\":{}}}' \
      | extract_errors
"

rt_run "[catalog] arguments as array not object" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"tools/call\",\"params\":{\"name\":\"verify_symbol\",\"arguments\":[]}}' \
      | extract_errors
"

rt_run "[catalog] arguments field missing entirely" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":6,\"method\":\"tools/call\",\"params\":{\"name\":\"verify_symbol\"}}' \
      | extract_errors
"

rt_run "[catalog] params missing entirely on tools/call" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"tools/call\"}' \
      | extract_errors
"

rt_run "[catalog] malformed JSON (parse error) → -32700 expected" bash -c "
    python3 harness/mcp.py raw \
      'not-json-at-all' \
      | extract_errors
"

rt_run "[catalog] wrong jsonrpc version → -32600 invalid-request expected" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"1.0\",\"id\":8,\"method\":\"tools/list\"}' \
      | extract_errors
"

rt_run "[catalog] missing jsonrpc field → -32600 expected" bash -c "
    python3 harness/mcp.py raw \
      '{\"id\":9,\"method\":\"tools/list\"}' \
      | extract_errors
"

rt_run "[catalog] missing method field → -32600 expected" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":10}' \
      | extract_errors
"

rt_run "[catalog] wrong-type id (object) → -32600 expected" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":{\"x\":1},\"method\":\"tools/list\"}' \
      | extract_errors
"

# ---------- L6b — Initialize state machine ----------

rt_section "L6b — Initialize state machine invariants"

rt_run "[init] second initialize after first → does server reset, error, or accept?" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":100,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"redteam-2nd-init\",\"version\":\"0.1\"}}}' \
      | extract_errors
"

rt_run "[init] initialize with wrong protocolVersion '1999-01-01'" bash -c "
    python3 harness/mcp.py rawnohandshake \
      '{\"jsonrpc\":\"2.0\",\"id\":101,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"1999-01-01\",\"capabilities\":{},\"clientInfo\":{\"name\":\"redteam\",\"version\":\"0.1\"}}}' \
      '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}' \
      '{\"jsonrpc\":\"2.0\",\"id\":102,\"method\":\"tools/list\"}' \
      | extract_errors
"

rt_run "[init] initialize with non-string protocolVersion (integer)" bash -c "
    python3 harness/mcp.py rawnohandshake \
      '{\"jsonrpc\":\"2.0\",\"id\":103,\"method\":\"initialize\",\"params\":{\"protocolVersion\":12345,\"capabilities\":{},\"clientInfo\":{\"name\":\"r\",\"version\":\"0.1\"}}}' \
      | extract_errors
"

rt_run "[init] tools/call AFTER initialize but BEFORE notifications/initialized" bash -c "
    python3 harness/mcp.py rawnohandshake \
      '{\"jsonrpc\":\"2.0\",\"id\":110,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"r\",\"version\":\"0.1\"}}}' \
      '{\"jsonrpc\":\"2.0\",\"id\":111,\"method\":\"tools/call\",\"params\":{\"name\":\"list_files\",\"arguments\":{}}}' \
      | extract_errors
"

rt_run "[init] notifications/initialized sent twice (duplicate)" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}' \
      '{\"jsonrpc\":\"2.0\",\"id\":120,\"method\":\"tools/list\"}' \
      | extract_errors
"

# ---------- L6c — Batch requests (JSON-RPC 2.0 §6) ----------

rt_section "L6c — Batch requests: spec §6 allows array-shaped frames"

rt_run "[batch] post-init batch [{tools/list},{tools/call verify_symbol}]" bash -c "
    python3 harness/mcp.py raw \
      '[{\"jsonrpc\":\"2.0\",\"id\":200,\"method\":\"tools/list\"},{\"jsonrpc\":\"2.0\",\"id\":201,\"method\":\"tools/call\",\"params\":{\"name\":\"verify_symbol\",\"arguments\":{\"name\":\"BulkFunc0001\"}}}]' \
      | extract_errors
"

rt_run "[batch] cold (no handshake) batch [{init},{notifications/initialized},{tools/list}]" bash -c "
    python3 harness/mcp.py rawnohandshake \
      '[{\"jsonrpc\":\"2.0\",\"id\":210,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"r\",\"version\":\"0.1\"}}},{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"},{\"jsonrpc\":\"2.0\",\"id\":211,\"method\":\"tools/list\"}]' \
      | extract_errors
"

rt_run "[batch] empty array [] (spec: invalid request → -32600)" bash -c "
    python3 harness/mcp.py raw \
      '[]' \
      | extract_errors
"

# ---------- L6d — additionalProperties:false enforcement ----------

rt_section "L6d — schema additionalProperties:false: extra field on tool input?"

rt_run "[addprop] verify_symbol with extra unknown field 'extra_field'" bash -c "
    python3 harness/mcp.py call verify_symbol '{\"name\":\"BulkFunc0001\",\"extra_field\":\"ignored?\"}' 2>&1
    echo \"exit=\$?\"
"

rt_run "[addprop] find_symbol with extra field" bash -c "
    python3 harness/mcp.py call find_symbol '{\"query\":\"BulkFunc0001\",\"limit\":1,\"NOT_A_REAL_FIELD\":42}' 2>&1
    echo \"exit=\$?\"
"

rt_run "[addprop] list_files with bogus type for known field (limit as string)" bash -c "
    python3 harness/mcp.py call list_files '{\"limit\":\"fifty\"}' 2>&1
    echo \"exit=\$?\"
"

# ---------- L6e — Notification handling ----------

rt_section "L6e — Notification handling: id-bearing notifications, unknown-id cancellation"

rt_run "[notif] notifications/cancelled with NON-EXISTENT requestId — silent? error?" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":999999,\"reason\":\"test\"}}' \
      '{\"jsonrpc\":\"2.0\",\"id\":300,\"method\":\"tools/list\"}' \
      | extract_errors
"

rt_run "[notif] notifications/cancelled with id (should be notification — no id) — server response?" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":310,\"method\":\"notifications/cancelled\",\"params\":{\"requestId\":1,\"reason\":\"test\"}}' \
      '{\"jsonrpc\":\"2.0\",\"id\":311,\"method\":\"tools/list\"}' \
      | extract_errors
"

rt_run "[notif] unknown notification method" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"method\":\"notifications/imaginary\",\"params\":{}}' \
      '{\"jsonrpc\":\"2.0\",\"id\":320,\"method\":\"tools/list\"}' \
      | extract_errors
"

# ---------- L6f — Duplicate-id requests in flight ----------

rt_section "L6f — Duplicate in-flight ids: 2 requests with id=500 — both answered? collision?"

rt_run "[dup-id] two tools/call with same id=500 in one session" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":500,\"method\":\"tools/call\",\"params\":{\"name\":\"verify_symbol\",\"arguments\":{\"name\":\"BulkFunc0001\"}}}' \
      '{\"jsonrpc\":\"2.0\",\"id\":500,\"method\":\"tools/call\",\"params\":{\"name\":\"verify_symbol\",\"arguments\":{\"name\":\"BulkFunc1499\"}}}' \
      > /tmp/l6_dup.json
    python3 -c \"
import json
d = json.load(open('/tmp/l6_dup.json'))
print('total responses:', len(d['responses']))
for r in d['responses']:
    if isinstance(r, dict) and r.get('id') == 500:
        # Find which symbol it answered for
        try:
            content = r['result']['structuredContent']
            print(f'  id=500 response — match path:', content.get('match', {}).get('file', 'n/a'), 'exists=', content.get('exists'))
        except Exception:
            print('  id=500 (unparseable result shape):', json.dumps(r)[:200])
\"
"

# ---------- L6g — Trailing data / multiple frames per line ----------

rt_section "L6g — Two frames concatenated in one stdin line (no newline between)"

# Build a single argument with two valid JSON-RPC frames concatenated. The
# harness still appends \n after the arg, but the server should be choosing
# its parse boundary — either one frame & ignore the rest, or parse both, or
# emit a parse error.
rt_run "[frames] {frame1}{frame2} concatenated, no separator" bash -c "
    python3 harness/mcp.py raw \
      '{\"jsonrpc\":\"2.0\",\"id\":600,\"method\":\"tools/list\"}{\"jsonrpc\":\"2.0\",\"id\":601,\"method\":\"tools/list\"}' \
      | extract_errors
"

# ---------- L6h — Field-type coercion / null vs missing ----------

rt_section "L6h — Field-type coercion: query=null vs missing vs empty"

rt_run "[null] find_symbol with query=null (explicit JSON null)" bash -c "
    python3 harness/mcp.py call find_symbol '{\"query\":null}' 2>&1
    echo \"exit=\$?\"
"

rt_run "[null] verify_symbol with name=null" bash -c "
    python3 harness/mcp.py call verify_symbol '{\"name\":null}' 2>&1
    echo \"exit=\$?\"
"

# ---------- L6i — Unicode + control chars ----------

rt_section "L6i — Unicode control chars in arguments"

rt_run "[unicode] verify_symbol with embedded NUL (U+0000) in name" bash -c "
    python3 -c 'import json,sys; sys.stdout.write(json.dumps({\"name\":\"BulkFunc\\u0000extra\"}))' \
      | python3 harness/mcp.py call verify_symbol -
    echo \"exit=\$?\"
"

rt_run "[unicode] find_symbol with RTLO + BOM in query" bash -c "
    python3 -c 'import json,sys; sys.stdout.write(json.dumps({\"query\":\"BulkFunc\\u202e\\ufeff\"}))' \
      | python3 harness/mcp.py call find_symbol -
    echo \"exit=\$?\"
"

# ---------- L6j — Final error-code summary table ----------

rt_section "L6j — Summary: every error code observed across L6"

rt_run "grep all error codes from this lane's runlog" bash -c "
    grep -oE 'code= *[^ ]+' '$RT_RUNLOG' | sort -u
    echo --
    echo 'Counts:'
    grep -oE 'code= *[^ ]+' '$RT_RUNLOG' | sort | uniq -c | sort -rn
    echo --
    echo 'Tool-error count (result.isError text):'
    grep -c 'TOOL-ERROR' '$RT_RUNLOG'
    echo 'Dropped lines (stderr):'
    grep -c 'dropped non-JSON-RPC line' '$RT_RUNLOG'
"

# ---------- Findings ----------

rt_finding F021 LOW \
    "Unknown method returns code:0 instead of JSON-RPC standard -32601 method-not-found" \
    "Three distinct sites observed with code:0 (extends F008): (a) pre-init tools/call, (b) unknown method (msg 'JSON RPC not handled'), (c) bad protocolVersion type. JSON-RPC 2.0 specifies -32601 for unknown method. Clients dispatching by code can't distinguish these classes."

rt_finding F022 LOW \
    "initialize accepted twice in same session; no reset, no error" \
    "Second initialize with a new client identity returns 200 OK with serverInfo. MCP spec/JSON-RPC handshake should be one-shot. Risk: a session-state-aware adapter that resets on init could be confused. Severity LOW because tools still answer."

rt_finding F023 LOW \
    "tools/call works between initialize and notifications/initialized; ordering not enforced" \
    "MCP lifecycle requires the client to send notifications/initialized after initialize before issuing requests. The server accepts and answers tools/call between those two frames. Pre-init tools/call IS rejected (F008/F021), so the gate fires on 'has initialize completed' rather than 'initialized notification received'."

rt_finding F024 LOW \
    "Server silently accepts unknown client protocolVersion '1999-01-01'; only the integer-type variant errors" \
    "Per MCP spec the server should report its supported protocolVersion in response and the client should disconnect if incompatible. leonard-mcp returns its own protocolVersion ('2025-11-25') and proceeds. A client that mis-spells or sends an old version gets no actionable signal."

rt_finding F025 MEDIUM \
    "JSON-RPC batch requests (spec §6) completely unsupported — silently dropped" \
    "Sending '[{req1},{req2}]' as a single frame produces zero responses; stderr shows 'dropped non-JSON-RPC line'. Empty batch '[]' likewise dropped. JSON-RPC 2.0 §6 mandates support; if intentionally unsupported, the server should respond with -32600 invalid-request rather than silently drop. A client batching legitimate calls hangs waiting for replies."

rt_finding F026 LOW \
    "Duplicate in-flight ids: server emits two responses both with id=500" \
    "Sending two tools/call requests with id=500 produces TWO distinct responses, both with id=500. JSON-RPC 2.0 forbids reusing an id until the prior response is received (§4); the server cannot distinguish. A correctness-sensitive client that routes by id will pair the wrong response with the wrong request. F008-class finding — protocol-correctness leakage."

rt_finding F027 LOW \
    "Schema validation errors return as tool errors (result.isError) instead of JSON-RPC -32602 invalid-params" \
    "Tool input that fails additionalProperties:false or required-field checks returns isError:true with a text body, not a JSON-RPC error.code:-32602. Two parallel error channels for the same failure class (cf. id=3 'unknown tool' returns -32602; id=4 'missing required name' returns isError). Inconsistent."

rt_finding F028 LOW \
    "null vs missing required-field leaks Go reflect internals in user-visible error" \
    "find_symbol query=null returns: 'validating /properties/query: type: <invalid reflect.Value> has type \"null\", want \"string\"'. The 'invalid reflect.Value' phrase is a Go-runtime artifact. Cosmetic — same finding shape as F006/F012 (Go internals leaking out of MCP error messages)."

rt_finding F029 LOW \
    "Embedded NUL (U+0000) in symbol name silently treated as a legitimate (mismatch) query" \
    "verify_symbol with name='BulkFunc\\u0000extra' returns exists:false — server doesn't reject the NUL. Many SQL bindings reject NUL bytes outright; SQLite's behavior is to truncate at NUL in some bindings. Risk: a query containing NUL could match more or less than the model expects without warning. LOW because no exploit demonstrated."

rt_finding F030 LOW \
    "Frames-with-no-newline-between dropped silently; no -32700 parse error" \
    "Two valid JSON frames concatenated in one stdin line ('{a}{b}') get dropped at the line scanner with stderr log only — no -32700 parse error returned. A client whose framing breaks (e.g. forgets newline) gets no response and no client-visible diagnostic, only a per-process stderr log Claude Code may not surface."

rt_summary
