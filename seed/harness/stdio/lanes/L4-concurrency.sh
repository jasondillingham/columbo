#!/usr/bin/env bash
# L4 — SQLite concurrency + crash-consistency.
#
# Targets:
#   - Parallel `leonard index` invocations (N>2; bughunt-3 informational said N=2 was clean)
#   - MCP query during a running index (WAL should allow concurrent reads)
#   - kill -9 mid-index — DB integrity and recovery on next run
#   - Rapid hook firing — WAL bounded?
#   - Concurrent in-session MCP tool calls (single server, multiple parallel requests)
#   - Two MCP server processes simultaneously
#
# Each destructive section restores from .leonard/leonard.db.l4-backup so the
# DB is left in a clean state for subsequent lanes.
set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L4-concurrency

DB=.leonard/leonard.db
BACKUP=.leonard/leonard.db.l4-backup
LEONARD=~/go/bin/leonard
LEONARD_MCP=~/go/bin/leonard-mcp

# --- helpers (exported for `bash -c` subshells, per L3 lesson) ----------

integrity_check()  { sqlite3 "$DB" "pragma integrity_check;"; }
file_count()       { sqlite3 "$DB" "select count(*) from files;"; }
symbol_count()     { sqlite3 "$DB" "select count(*) from symbols;"; }
wal_size()         { [ -f "${DB}-wal" ] && stat -f%z "${DB}-wal" || echo 0; }
restore_db()       { cp "$BACKUP" "$DB"; rm -f "${DB}-wal" "${DB}-shm"; }

export DB BACKUP LEONARD LEONARD_MCP
export -f integrity_check file_count symbol_count wal_size restore_db

# --- L4a — Parallel leonard index ---------------------------------------

rt_section "L4a — Parallel leonard index (N=2, 4, 8)"

for N in 2 4 8; do
    rt_run "snapshot before N=$N" bash -c "echo files=\$(file_count) symbols=\$(symbol_count) integrity=\$(integrity_check) wal=\$(wal_size)B"

    rt_run "spawn N=$N concurrent index, wait for all, check exit codes" bash -c "
        rm -f /tmp/l4_idx_*.log /tmp/l4_idx_*.rc
        pids=()
        for i in \$(seq 1 $N); do
            ( \$LEONARD index >/tmp/l4_idx_\$i.log 2>&1; echo \$? > /tmp/l4_idx_\$i.rc ) &
            pids+=(\$!)
        done
        echo \"spawned \${#pids[@]} workers: \${pids[*]}\"
        for p in \"\${pids[@]}\"; do wait \$p 2>/dev/null; done
        echo 'all workers complete'
        for i in \$(seq 1 $N); do
            rc=\$(cat /tmp/l4_idx_\$i.rc 2>/dev/null || echo MISSING)
            head_line=\$(head -1 /tmp/l4_idx_\$i.log 2>/dev/null)
            err_line=\$(grep -i 'locked\|busy\|error' /tmp/l4_idx_\$i.log | head -1)
            echo \"  worker[\$i] rc=\$rc  output='\$head_line'  errors='\$err_line'\"
        done
    "
    rt_run "post N=$N integrity" bash -c "echo files=\$(file_count) symbols=\$(symbol_count) integrity=\$(integrity_check)"
done

# --- L4b — MCP query DURING running index -------------------------------

rt_section "L4b — MCP query while index is running (WAL should allow concurrent reads)"

rt_run "start index in background, immediately fire MCP find_symbol — measure latency" bash -c "
    rm -f /tmp/l4_bg_idx.log /tmp/l4_mcp_query.log
    \$LEONARD index >/tmp/l4_bg_idx.log 2>&1 &
    IDX_PID=\$!
    # Tiny delay so the indexer has started its first write
    sleep 0.05
    t_start=\$(date +%s%N)
    python3 harness/mcp.py call find_symbol '{\"query\":\"BulkFunc0500\",\"limit\":10}' >/tmp/l4_mcp_query.log 2>&1
    mcp_rc=\$?
    t_end=\$(date +%s%N)
    elapsed_ms=\$(( (t_end - t_start) / 1000000 ))
    wait \$IDX_PID 2>/dev/null; idx_rc=\$?
    echo \"mcp_rc=\$mcp_rc idx_rc=\$idx_rc elapsed_ms=\$elapsed_ms\"
    head -3 /tmp/l4_mcp_query.log
    echo '---'
    head -2 /tmp/l4_bg_idx.log
"

# --- L4c — kill -9 mid-index ---------------------------------------------

rt_section "L4c — kill -9 mid-index — DB integrity after, recoverable on re-run"

rt_run "kill -9 the indexer immediately after spawn" bash -c "
    \$LEONARD index >/tmp/l4_killed.log 2>&1 &
    KILL_PID=\$!
    # Try to kill while it's likely mid-write (the index of 1500 files takes ~0.5s)
    sleep 0.02
    kill -9 \$KILL_PID 2>/dev/null && echo 'sent SIGKILL to '\$KILL_PID
    wait \$KILL_PID 2>/dev/null; killed_rc=\$?
    echo killed_rc=\$killed_rc \"(137 = OK = SIGKILLed)\"
    echo 'leftover files in .leonard/:'
    ls .leonard/leonard.db* 2>&1
"

rt_run "DB integrity right after kill" bash -c "
    chk=\$(integrity_check 2>&1)
    files=\$(file_count 2>&1)
    syms=\$(symbol_count 2>&1)
    wal=\$(wal_size)
    echo integrity=\$chk files=\$files symbols=\$syms wal=\${wal}B
"

rt_run "re-run leonard index after kill — should complete cleanly" bash -c "
    \$LEONARD index 2>&1 | head -3
    echo --post--
    echo files=\$(file_count) syms=\$(symbol_count) integrity=\$(integrity_check)
"

# --- L4d — WAL growth under rapid hook-equivalent writes ----------------

rt_section "L4d — WAL growth under rapid post-edit-like reindex bursts"

rt_run "snapshot WAL before burst" bash -c "echo wal=\$(wal_size)B"

rt_run "20 rapid back-to-back leonard index calls (sequential — checkpoints between?)" bash -c "
    for i in \$(seq 1 20); do
        \$LEONARD index >/dev/null 2>&1
    done
    echo wal_after_burst=\$(wal_size)B
    echo files=\$(file_count) symbols=\$(symbol_count) integrity=\$(integrity_check)
"

# --- L4e — Concurrent in-session MCP tool calls -------------------------

rt_section "L4e — Concurrent in-session MCP tool calls (multiple parallel tools/call)"

rt_run "send 5 parallel tools/call in one session — id 10..14" python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc0001"}}}' \
  '{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc0100"}}}' \
  '{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc0500"}}}' \
  '{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc1000"}}}' \
  '{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc1499"}}}'

rt_run "send 5 parallel HEAVY find_symbol calls in one session (query=BulkFunc, limit=500)" python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"find_symbol","arguments":{"query":"BulkFunc","limit":500}}}' \
  '{"jsonrpc":"2.0","id":21,"method":"tools/call","params":{"name":"find_symbol","arguments":{"query":"BulkFunc","limit":500}}}' \
  '{"jsonrpc":"2.0","id":22,"method":"tools/call","params":{"name":"find_symbol","arguments":{"query":"BulkFunc","limit":500}}}' \
  '{"jsonrpc":"2.0","id":23,"method":"tools/call","params":{"name":"find_symbol","arguments":{"query":"BulkFunc","limit":500}}}' \
  '{"jsonrpc":"2.0","id":24,"method":"tools/call","params":{"name":"find_symbol","arguments":{"query":"BulkFunc","limit":500}}}'

rt_run "intermix: tools/list and tools/call in flight" python3 harness/mcp.py raw \
  '{"jsonrpc":"2.0","id":30,"method":"tools/call","params":{"name":"verify_symbol","arguments":{"name":"BulkFunc0500"}}}' \
  '{"jsonrpc":"2.0","id":31,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":32,"method":"tools/call","params":{"name":"list_files","arguments":{"limit":50}}}' \
  '{"jsonrpc":"2.0","id":33,"method":"tools/list"}'

# --- L4f — Two MCP server processes simultaneously ----------------------

rt_section "L4f — Two MCP server processes against the same DB"

rt_run "spawn two MCP servers concurrently, query each — same DB, WAL mode" bash -c "
    python3 harness/mcp.py call find_symbol '{\"query\":\"BulkFunc0001\",\"limit\":5}' >/tmp/l4_mcp_a.log 2>&1 &
    A=\$!
    python3 harness/mcp.py call find_symbol '{\"query\":\"BulkFunc1499\",\"limit\":5}' >/tmp/l4_mcp_b.log 2>&1 &
    B=\$!
    wait \$A; rcA=\$?
    wait \$B; rcB=\$?
    echo \"A rc=\$rcA  B rc=\$rcB\"
    grep -E 'matches|error|locked' /tmp/l4_mcp_a.log | head -3
    echo --
    grep -E 'matches|error|locked' /tmp/l4_mcp_b.log | head -3
"

# --- L4g — Hook firing during index --------------------------------------

rt_section "L4g — pre-edit hook fired while leonard index is running"

rt_run "background index + foreground pre-edit hook against same DB" bash -c "
    \$LEONARD index >/tmp/l4_bg_idx2.log 2>&1 &
    IDX=\$!
    sleep 0.02
    payload='{\"session_id\":\"l4-hook\",\"tool_name\":\"Write\",\"tool_input\":{\"file_path\":\"/tmp/x.txt\",\"content\":\"y\"}}'
    echo \"\$payload\" | ~/go/bin/leonard-hook pre-edit
    hook_rc=\$?
    wait \$IDX 2>/dev/null; idx_rc=\$?
    echo \"hook_rc=\$hook_rc idx_rc=\$idx_rc\"
"

# --- L4h — Final integrity + cleanup -------------------------------------

rt_section "L4h — Final integrity + restore"

rt_run "final integrity_check before restore" bash -c "
    echo integrity=\$(integrity_check) files=\$(file_count) syms=\$(symbol_count) wal=\$(wal_size)B
"

rt_run "restore DB from backup so subsequent lanes start clean" bash -c "
    restore_db
    echo restored
    echo integrity=\$(integrity_check) files=\$(file_count) syms=\$(symbol_count)
"

rt_summary
