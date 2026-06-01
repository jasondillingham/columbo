#!/usr/bin/env bash
# Lane L7 — real-dogfood UX probes for bughunt-1 on bosun
# Most of this lane is qualitative observation; this script is a
# repro-skeleton for the durable findings (F030..F038).
#
# Designed to run on a clean machine: it stands up a fresh sandbox,
# walks the operator workflow, and exits non-zero if any of the L7
# findings stop reproducing.
#
# Run from anywhere:  bash /tmp/bosun-redteam/harness/lanes/L7-dogfood.sh
set -euo pipefail

BOSUN="${BOSUN:-/tmp/bosun_test}"
SAND="${SAND:-/tmp/bosun-redteam-L7}"
HARNESS="${HARNESS:-/tmp/bosun-redteam/harness}"
PY_MCP="$HARNESS/mcp_sock.py"

rt_run() { echo "▶ $*"; "$@"; echo "  → exit=$?"; }

cleanup_daemons() {
    # WARNING: greps for any process matching "<BOSUN> mcp". When multiple
    # lanes (L6/L7/L8) run in parallel from the same bosun binary, this will
    # kill sibling lanes' daemons too. Safe in serial runs; in parallel runs,
    # scope by sandbox PWD: `lsof -t .bosun/mcp.sock | xargs kill` is safer.
    pgrep -f "$BOSUN mcp" 2>/dev/null | xargs -n1 kill 2>/dev/null || true
    sleep 1
}

# --- Setup --------------------------------------------------------------
rm -rf "$SAND"
mkdir -p "$SAND"
cd "$SAND"
git init --quiet test-repo
cd test-repo
git config user.email "L7@local"
git config user.name "L7"
mkdir -p cmd/myapp internal/handler internal/store
cat > cmd/myapp/main.go <<'GO'
package main
import "fmt"
func main() { fmt.Println("hello") }
GO
cat > internal/handler/handler.go <<'GO'
package handler
type Handler struct{}
func (h *Handler) Serve() error { return nil }
GO
cat > internal/store/store.go <<'GO'
package store
type Store struct{}
func New() *Store { return &Store{} }
GO
cat > go.mod <<'GO'
module example.com/myapp
go 1.21
GO
git add -A
git commit -m "initial app" --quiet
"$BOSUN" init >/dev/null

# --- F030 repro: re-init poisons state ---------------------------------
echo "=== F030 — second init pollutes init.state, error misdirects ==="
set +e
"$BOSUN" init > /tmp/L7-init2.out 2> /tmp/L7-init2.err
echo "second-init exit=$?"
set -e
grep -q "already used by worktree" /tmp/L7-init2.err && echo "F030: confirmed (operator sees git's 'already used' error, not 'sessions already exist')"
[ -f .bosun/init.state ] && echo "F030: confirmed (init.state was written even though init failed)"
rm -f .bosun/init.state  # heal so subsequent probes can run

# --- start MCP daemon ---------------------------------------------------
cleanup_daemons
(nohup "$BOSUN" mcp > /tmp/L7-mcp.log 2>&1 &)
sleep 2

# --- F031 repro: bosun attach doesn't satisfy bosun_spawn -------------
echo "=== F031 — bosun attach claims to satisfy liveness gate but doesn't ==="
"$BOSUN" config set agent_spawn.enabled true >/dev/null
WT1=$(git worktree list | grep "session-1" | awk '{print $1}')
(cd "$WT1" && "$BOSUN" attach session-1 --pid $$ >/dev/null)
RESP=$(BOSUN_MCP_SOCK="$PWD/.bosun/mcp.sock" python3 "$PY_MCP" call bosun_spawn \
  '{"parent":"session-1","brief":"## httph\n\nadd handler\n","launch":false}')
if echo "$RESP" | grep -q "no live agent detected"; then
    echo "F031: confirmed (bosun attach was accepted, bosun_spawn still refuses)"
fi

# --- F032 repro: merge conflict returns exit 0 --------------------------
echo "=== F032 — bosun merge exit 0 on conflict (HIGH) ==="
cat > "$WT1/internal/handler/handler.go" <<'GO'
package handler
type Handler struct{}
func (h *Handler) Serve() error { return nil }
func (h *Handler) A() {}
GO
(cd "$WT1" && git add -A && git commit -m "s1 A" --quiet)
WT2=$(git worktree list | grep "session-2" | awk '{print $1}')
cat > "$WT2/internal/handler/handler.go" <<'GO'
package handler
type Handler struct{}
func (h *Handler) Serve() error { return nil }
func (h *Handler) B() {}
GO
(cd "$WT2" && git add -A && git commit -m "s2 B" --quiet)
"$BOSUN" done session-1 >/dev/null
"$BOSUN" done session-2 >/dev/null
"$BOSUN" merge session-1 >/dev/null 2>&1 || true
set +e
"$BOSUN" merge session-2 > /tmp/L7-merge.out 2> /tmp/L7-merge.err
MERGE_EXIT=$?
set -e
echo "merge exit code on conflict = $MERGE_EXIT"
if [ "$MERGE_EXIT" -eq 0 ]; then
    echo "F032: CONFIRMED HIGH (conflict yields exit 0)"
fi
grep -q "conflict" /tmp/L7-merge.out && echo "F032: stdout said 'conflict' but exit=$MERGE_EXIT"
git status --porcelain | grep -q "^UU " && echo "F032: working tree still has UU file — script would fail-open"
# heal
git restore --staged internal/handler/handler.go 2>/dev/null || true
git checkout -- internal/handler/handler.go 2>/dev/null || true
rm -f .git/MERGE_MSG .git/AUTO_MERGE

# --- F037 repro: debug bundle reports "dev" -----------------------------
echo "=== F037 — bosun debug reports 'dev' version not binary version ==="
DBGVER=$("$BOSUN" debug 2>/dev/null | awk '/bosun --version/{getline; getline; print; exit}')
BINVER=$("$BOSUN" --version)
echo "binary --version: $BINVER"
echo "debug-bundled --version: $DBGVER"
if [ "$DBGVER" = "dev" ] && [ "$BINVER" != "bosun version dev" ]; then
    echo "F037: confirmed (cmd_debug.go:126 uses raw \`version\` not resolvedVersion())"
fi

# --- F038 repro: session-XXX silently orphaned --------------------------
echo "=== F038 — bosun init session-clean creates orphan (init accepts; list hides) ==="
set +e
"$BOSUN" init session-clean > /tmp/L7-init-clean.out 2>&1
set -e
if grep -q "Created 1 session(s): *$" /tmp/L7-init-clean.out || grep -q "session-clean" /tmp/L7-init-clean.out; then
    echo "F038: init succeeded for session-clean"
fi
if ! "$BOSUN" list | grep -q "^session-clean$"; then
    echo "F038: CONFIRMED — session-clean is invisible to bosun list (silent orphan)"
fi
"$BOSUN" show session-clean 2>&1 | grep -q "not found" && \
    echo "F038: bosun show says not found"

cleanup_daemons
echo "L7 done. See /tmp/bosun-redteam/findings/L7-findings.md"
