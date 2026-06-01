#!/usr/bin/env bash
# L1 — build invariants & version drift for bosun.
# Quick lane — F001 already confirmed in setup; this script captures the
# reproducer in audit format + probes adjacent build-invariant candidates.
set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L1-build-invariants

BOSUN=/tmp/bosun_test
REPO="${REPO:-/path/to/bosun}"   # seed/reference harness — set REPO to your checkout

rt_section "L1a — version drift across surfaces"

rt_run "binary --version (built from HEAD aabaf3d via go build)" $BOSUN --version
rt_run "MCP wire serverInfo (via socket probe — uses test-repo's running mcp)" bash -c '
  cd /tmp/bosun-redteam/test-repo
  $BOSUN mcp > /tmp/l1_mcp.log 2>&1 &
  P=$!; sleep 0.6
  python3 /tmp/bosun-redteam/harness/mcp_sock.py raw \
    "{\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"l1\",\"version\":\"0\"}}}" \
    2>/dev/null | python3 -c "
import json,sys
d=json.load(sys.stdin)
for r in d[\"responses\"]:
  if r.get(\"id\")==99: print(\"serverInfo:\", r[\"result\"][\"serverInfo\"])"
  kill $P 2>/dev/null; wait $P 2>/dev/null
'
export BOSUN REPO
export -f true   # placeholder so the export-functions habit is engaged

rt_section "L1b — source-side version constant audit"

rt_run "grep for hardcoded ServerVersion / API version strings" bash -c '
  cd $REPO
  echo "=== ServerVersion source ==="
  grep -n "ServerVersion\|ServerName" internal/mcp/server.go | head -5
  echo ""
  echo "=== other potentially-stale version refs ==="
  grep -rn "\"0\.[0-9]\+\.[0-9]\+\(-[a-z]*\)\?\"" internal/ cmd/ 2>/dev/null | grep -v _test | head -10
'

rt_run "ldflags wiring in .goreleaser.yaml + Makefile (correct dynamic source)" bash -c '
  cd $REPO
  grep -nE "ldflags|version" .goreleaser.yaml Makefile 2>/dev/null | head -15
'

rt_section "L1c — build noise on go install"

rt_run "go build with full warnings (any leak that should be suppressed/upstreamed?)" bash -c '
  cd $REPO
  go build -v -o /tmp/bosun_l1 ./cmd/bosun 2>&1 | tail -20
  rm -f /tmp/bosun_l1
'

rt_section "L1d — help text health (operator surface)"

rt_run "bosun --help" $BOSUN --help
rt_run "bosun mcp --help" $BOSUN mcp --help

rt_section "L1e — confirm tests green at HEAD"

rt_run "go test ./internal/mcp/... -count=1 -short" bash -c "cd $REPO && go test ./internal/mcp/... -count=1 -short 2>&1 | tail -5"
rt_run "go test ./internal/lockfile/... -count=1 -short (Bundle B regression)" bash -c "cd $REPO && go test ./internal/lockfile/... -count=1 -short 2>&1 | tail -5"
rt_run "go test ./internal/usage/... -count=1 -short (Bundle D regression)" bash -c "cd $REPO && go test ./internal/usage/... -count=1 -short 2>&1 | tail -5"

rt_summary
