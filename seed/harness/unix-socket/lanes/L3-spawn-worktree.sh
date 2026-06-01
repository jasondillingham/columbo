#!/usr/bin/env bash
# L3-spawn-worktree.sh — bughunt-1 Lane L3: spawn pipeline + worktree lifecycle
#
# Pre-reqs (operator setup, scripted in `setup_sandbox` below):
#   - /tmp/bosun-redteam-L3/test-repo with `bosun init session-1`
#   - .bosun/config.json with agent_spawn.enabled=true and a per-probe
#     max_concurrent_sub_sessions cap
#   - /tmp/bosun-redteam-L3/test-repo-bosun-1 dir exists and houses a
#     long-running fake-claude binary (a Go-compiled `sleep(99999s)`
#     binary literally named `claude`). Required because the
#     bosun_spawn parent-liveness gate hardcodes the IsAgent
#     allowlist (claude / claude-code / code-cli) and looks at the
#     LEGACY worktree path (WorktreePathForLabel(.., "")) regardless
#     of how the actual worktree was created.
#
# Probes (numbering from /tmp/bosun-redteam/findings/bughunt-1-brief.md):
#   P5: schema discovery (the `briefs[]` straw man) — usability
#   P3: single-call N-over-cap (deterministic; per-rollback bug-hunt)
#   P2: concurrent burst (N=4, N=8) — fbb3223 fix regression
#   P4: mixed concurrent + multi-brief (4 calls × 2 briefs, cap=5)
#   P6: spawn-tree.json corruption (bad JSON / array / truncated)
#   P10: simultaneous bosun_remove on same session
#   P11: branch-rename mid-flight
#   P13: bosun_done idempotency

set -u

# ----- where things live -----------------------------------------------------

SANDBOX="/tmp/bosun-redteam-L3"
REPO="$SANDBOX/test-repo"
LEGACY_PARENT_WT="$SANDBOX/test-repo-bosun-1"   # liveness-gate path target
FAKE_CLAUDE="$SANDBOX/fake-bin/claude"
BOSUN="/tmp/bosun_test"
HARNESS_ROOT="/tmp/bosun-redteam"
MCP_SOCK="$REPO/.bosun/mcp.sock"

# rt.sh expects RT_ROOT for runlog/ + findings/
export RT_ROOT="$HARNESS_ROOT"
# Use shared FINDINGS.md so lanes converge; we mirror to L3-findings.md after.
# shellcheck disable=SC1091
source "$HARNESS_ROOT/harness/rt.sh"

# ----- helpers (all `export -f`'d so `rt_run` sub-shells see them) -----------

start_mcp() {
    rm -f "$MCP_SOCK" 2>/dev/null
    cd "$REPO" || return 1
    nohup "$BOSUN" mcp --socket "$MCP_SOCK" </dev/null \
        > "$SANDBOX/mcp.log" 2>&1 &
    echo $! > "$SANDBOX/mcp-pid"
    # Wait for socket up to 5s.
    for _ in $(seq 1 50); do
        [ -S "$MCP_SOCK" ] && return 0
        sleep 0.1
    done
    return 1
}

stop_mcp() {
    local pid
    pid="$(cat "$SANDBOX/mcp-pid" 2>/dev/null)"
    [ -n "$pid" ] && kill "$pid" 2>/dev/null
    sleep 0.3
    rm -f "$MCP_SOCK" "$SANDBOX/mcp-pid"
}

start_fake_claude() {
    pkill -f '/tmp/bosun-redteam-L3/fake-bin/claude' 2>/dev/null
    sleep 0.3
    mkdir -p "$LEGACY_PARENT_WT"
    (
        cd "$LEGACY_PARENT_WT" || exit 1
        nohup "$FAKE_CLAUDE" </dev/null >/dev/null 2>&1 &
        echo $! > "$SANDBOX/fake-pid"
    )
    sleep 0.3
    local pid; pid="$(cat "$SANDBOX/fake-pid" 2>/dev/null)"
    kill -0 "$pid" 2>/dev/null
}

stop_fake_claude() {
    pkill -f '/tmp/bosun-redteam-L3/fake-bin/claude' 2>/dev/null
}

reset_tree() {
    # Wipe any spawned sub-sessions back to a clean session-1-only tree.
    # 1. Remove every git worktree except the main + the timestamped session-1
    cd "$REPO" || return 1
    local extra
    while IFS= read -r extra; do
        [ -z "$extra" ] && continue
        case "$extra" in
            "$REPO"|*test-repo-bosun-20260528-*-1) continue ;;
        esac
        git worktree remove --force "$extra" 2>/dev/null
    done < <(git worktree list --porcelain | awk '/^worktree /{print $2}')

    # 2. Delete every bosun/* branch except session-1
    git branch --list 'bosun/*' | sed 's/^[* ]*//' | while read -r br; do
        [ "$br" = "bosun/session-1" ] && continue
        git branch -D "$br" 2>/dev/null
    done

    # 3. Reset spawn-tree.json to session-1-only
    cat > "$REPO/.bosun/spawn-tree.json" <<EOF
{
  "version": "v1",
  "sessions": {
    "session-1": {
      "depth": 0,
      "spawned_at": "2026-05-28T19:06:09Z"
    }
  }
}
EOF
}

set_cap() {
    local cap="$1" depth="${2:-3}"
    cat > "$REPO/.bosun/config.json" <<EOF
{
  "base_branch": "main",
  "session_prefix": "bosun",
  "agent_spawn": {
    "enabled": true,
    "max_concurrent_sub_sessions": $cap,
    "max_depth": $depth
  }
}
EOF
}

# call_spawn DESC BRIEF — synchronous spawn call, prints response JSON.
call_spawn() {
    local desc="$1" brief="$2"
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=20 \
        python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
        "$(printf '{"parent":"session-1","brief":%s,"launch":false}' \
            "$(printf '%s' "$brief" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')")"
}

# spawn_inline ID BRIEF — write a JSON args file and call. Used for concurrent
# fan-out where each request needs its own args file.
spawn_inline_json() {
    local brief="$1"
    python3 - "$brief" <<'PY'
import json,sys
print(json.dumps({"parent":"session-1","brief":sys.argv[1],"launch":False}))
PY
}

count_tree_kids() {
    python3 - <<PY
import json
with open("$REPO/.bosun/spawn-tree.json") as f:
    t = json.load(f)
sess = t.get("sessions", {})
parent = sess.get("session-1", {})
print(len(parent.get("children", [])))
PY
}

count_fs_subs() {
    ls "$SANDBOX" 2>/dev/null | grep -E '^test-repo-bosun-1\.' | wc -l | tr -d ' '
}

count_git_sub_branches() {
    cd "$REPO" || return 1
    git branch --list 'bosun/session-1.*' | wc -l | tr -d ' '
}

count_git_sub_worktrees() {
    cd "$REPO" || return 1
    git worktree list --porcelain 2>/dev/null \
        | awk '/^branch refs\/heads\/bosun\/session-1\./{n++} END{print n+0}'
}

export SANDBOX REPO LEGACY_PARENT_WT FAKE_CLAUDE BOSUN HARNESS_ROOT MCP_SOCK
export -f start_mcp stop_mcp start_fake_claude stop_fake_claude reset_tree \
          set_cap call_spawn spawn_inline_json count_tree_kids count_fs_subs \
          count_git_sub_branches count_git_sub_worktrees

# ----- lane orchestration ----------------------------------------------------

rt_init L3-spawn-worktree

rt_section "Sandbox sanity"

rt_run "fake-claude is alive in legacy path" bash -c '
    start_fake_claude && {
        pid=$(cat "$SANDBOX/fake-pid")
        kill -0 "$pid" 2>&1 && echo "fake-claude pid=$pid alive in $LEGACY_PARENT_WT"
    }
'

rt_run "MCP socket binds" bash -c 'start_mcp && ls -la "$MCP_SOCK"'

rt_run "tools/list returns 14 bosun_* tools" bash -c '
    BOSUN_MCP_SOCK="$MCP_SOCK" python3 "$HARNESS_ROOT/harness/mcp_sock.py" list \
        | grep -c "^bosun_"
'

# -------------------- P5: schema discovery — usability ----------------------

rt_section "P5: bosun_spawn schema discovery (usability)"

rt_run "spawn with no args" bash -c '
    BOSUN_MCP_SOCK="$MCP_SOCK" python3 "$HARNESS_ROOT/harness/mcp_sock.py" \
        call bosun_spawn "{}"
'

rt_run "spawn with briefs:[...] (the straw-man field name)" bash -c '
    BOSUN_MCP_SOCK="$MCP_SOCK" python3 "$HARNESS_ROOT/harness/mcp_sock.py" \
        call bosun_spawn "{\"parent\":\"session-1\",\"briefs\":[{\"label\":\"x\",\"body\":\"y\"}]}"
'

rt_run "spawn with brief:string (the real schema)" bash -c '
    set_cap 4
    stop_mcp; start_mcp
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=20 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
      "{\"parent\":\"session-1\",\"brief\":\"## probex\\n\\nBody.\\n\",\"launch\":false}"
    # cleanup
    reset_tree
    stop_mcp
'

# -------------------- P3: single-call N-over-cap ---------------------------

rt_section "P3: single-call N-over-cap (5 briefs, cap=2)"

rt_run "set cap=2, spawn 5 briefs in one call, count children" bash -c '
    reset_tree
    set_cap 2
    start_mcp
    brief="## probe1\\n\\nA.\\n\\n## probe2\\n\\nB.\\n\\n## probe3\\n\\nC.\\n\\n## probe4\\n\\nD.\\n\\n## probe5\\n\\nE.\\n"
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=40 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
      "$(spawn_inline_json "$(printf %b "$brief")")"
    echo "--- after spawn ---"
    echo "tree children: $(count_tree_kids)"
    echo "fs subs:       $(count_fs_subs)"
    echo "git branches:  $(count_git_sub_branches)"
    echo "git worktrees: $(count_git_sub_worktrees)"
    echo "tree JSON:"
    cat "$REPO/.bosun/spawn-tree.json"
    echo "--- fs dirs:"
    ls "$SANDBOX" | grep "^test-repo-bosun-1\."
    echo "--- branches:"
    cd "$REPO" && git branch --list "bosun/session-1.*"
    stop_mcp
'

# -------------------- P2: concurrent burst ---------------------------------

rt_section "P2: concurrent burst — N=4 spawn calls, cap=2"

rt_run "N=4 concurrent single-brief spawns, cap=2" bash -c '
    reset_tree
    set_cap 2
    start_mcp
    # Launch 4 background spawn calls
    for i in 1 2 3 4; do
        BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=40 \
          python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
          "$(spawn_inline_json "$(printf "## burst%d\n\nBody.\n" $i)")" \
          > "$SANDBOX/burst-$i.out" 2>&1 &
    done
    wait
    echo "--- per-call outcomes ---"
    for i in 1 2 3 4; do
        echo "--- burst-$i.out ---"
        head -30 "$SANDBOX/burst-$i.out"
    done
    echo "--- aggregate state ---"
    echo "tree children: $(count_tree_kids)"
    echo "fs subs:       $(count_fs_subs)"
    echo "git branches:  $(count_git_sub_branches)"
    echo "git worktrees: $(count_git_sub_worktrees)"
    cat "$REPO/.bosun/spawn-tree.json"
    stop_mcp
'

rt_run "N=8 concurrent single-brief spawns, cap=3" bash -c '
    reset_tree
    set_cap 3
    start_mcp
    for i in $(seq 1 8); do
        BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=60 \
          python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
          "$(spawn_inline_json "$(printf "## brst%d\n\nBody.\n" $i)")" \
          > "$SANDBOX/burst8-$i.out" 2>&1 &
    done
    wait
    echo "--- aggregate state ---"
    echo "tree children: $(count_tree_kids)"
    echo "fs subs:       $(count_fs_subs)"
    echo "git branches:  $(count_git_sub_branches)"
    echo "git worktrees: $(count_git_sub_worktrees)"
    cat "$REPO/.bosun/spawn-tree.json"
    stop_mcp
'

# -------------------- P4: mixed concurrent + multi-brief -------------------

rt_section "P4: 4 concurrent calls × 2 briefs, cap=5"

rt_run "4 calls × 2 briefs each, cap=5" bash -c '
    reset_tree
    set_cap 5
    start_mcp
    for i in 1 2 3 4; do
        BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=60 \
          python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
          "$(spawn_inline_json "$(printf "## mxa%d\n\nA.\n\n## mxb%d\n\nB.\n" $i $i)")" \
          > "$SANDBOX/mixed-$i.out" 2>&1 &
    done
    wait
    echo "--- aggregate state ---"
    echo "tree children: $(count_tree_kids)"
    echo "fs subs:       $(count_fs_subs)"
    echo "git branches:  $(count_git_sub_branches)"
    echo "git worktrees: $(count_git_sub_worktrees)"
    cat "$REPO/.bosun/spawn-tree.json"
    echo "--- per-call:"
    for i in 1 2 3 4; do echo "--- mixed-$i.out ---"; head -25 "$SANDBOX/mixed-$i.out"; done
    stop_mcp
'

# -------------------- P6: spawn-tree.json corruption ----------------------

rt_section "P6: corrupted spawn-tree.json — recover or refuse?"

rt_run "spawn against truncated spawn-tree.json" bash -c '
    reset_tree
    set_cap 3
    # Truncate spawn-tree.json to invalid prefix
    printf "{ \"version\": \"v1\"," > "$REPO/.bosun/spawn-tree.json"
    start_mcp
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=10 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
      "$(spawn_inline_json "## c1\n\nx.\n")"
    echo "--- daemon still alive? ---"
    ls -la "$MCP_SOCK" 2>&1
    BOSUN_MCP_SOCK="$MCP_SOCK" python3 "$HARNESS_ROOT/harness/mcp_sock.py" list \
        | head -3
    stop_mcp
'

rt_run "spawn against spawn-tree.json that is a JSON array" bash -c '
    reset_tree
    set_cap 3
    echo "[]" > "$REPO/.bosun/spawn-tree.json"
    start_mcp
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=10 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
      "$(spawn_inline_json "## c2\n\nx.\n")"
    stop_mcp
'

rt_run "spawn against spawn-tree.json with cycle (a->b->a)" bash -c '
    reset_tree
    set_cap 3
    cat > "$REPO/.bosun/spawn-tree.json" <<EOJ
{
  "version": "v1",
  "sessions": {
    "session-1": {"depth": 0, "parent": "session-1.kid", "spawned_at": "2026-05-28T19:06:09Z"},
    "session-1.kid": {"depth": 1, "parent": "session-1", "spawned_at": "2026-05-28T19:06:09Z"}
  }
}
EOJ
    start_mcp
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=10 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
      "$(spawn_inline_json "## c3\n\nx.\n")"
    stop_mcp
'

# -------------------- P11: branch-rename mid-life ---------------------------

rt_section "P11: branch rename behind bosun's back"

rt_run "rename bosun/session-1 → bosun/session-renamed, then bosun_done" bash -c '
    reset_tree
    set_cap 3
    cd "$REPO"
    # Sanity: spawn one sub so we have a tree to confuse
    start_mcp
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=40 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
      "$(spawn_inline_json "## rn1\n\nx.\n")" > "$SANDBOX/rn-spawn.out" 2>&1
    head -20 "$SANDBOX/rn-spawn.out"
    stop_mcp
    # Now rename
    git branch -m bosun/session-1.rn1 bosun/session-renamed 2>&1
    git branch --list "bosun/*"
    # bosun_done over MCP on session-1.rn1 ; first start MCP back up
    start_mcp
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=10 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_done \
      "{\"session\":\"session-1.rn1\"}"
    stop_mcp
'

# -------------------- P13: bosun_done idempotency --------------------------

rt_section "P13: bosun_done idempotency"

rt_run "double bosun_done on same session" bash -c '
    reset_tree
    set_cap 3
    start_mcp
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=40 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_spawn \
      "$(spawn_inline_json "## dn1\n\nx.\n")" > "$SANDBOX/done-spawn.out" 2>&1
    head -20 "$SANDBOX/done-spawn.out"
    echo "--- 1st bosun_done ---"
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=10 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_done \
      "{\"session\":\"session-1.dn1\"}"
    echo "--- 2nd bosun_done (idempotency) ---"
    BOSUN_MCP_SOCK="$MCP_SOCK" MCP_TIMEOUT=10 \
      python3 "$HARNESS_ROOT/harness/mcp_sock.py" call bosun_done \
      "{\"session\":\"session-1.dn1\"}"
    stop_mcp
'

# -------------------- cleanup -----------------------------------------------

rt_section "Lane teardown"
rt_run "stop fake-claude + daemon" bash -c '
    stop_mcp
    stop_fake_claude
    echo cleanup ok
'

rt_summary
