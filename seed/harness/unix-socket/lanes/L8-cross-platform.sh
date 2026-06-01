#!/usr/bin/env bash
# L8 — cross-platform / Windows continuity audit probes.
#
# Most of L8 is source-side audit of *_windows.go / *_unix.go pairs
# (operator is on Darwin/arm64, no Windows VNC from here). This script
# captures the two probes we COULD reproduce on macOS as a mirror of
# Windows quirks:
#
#   probe-1: Print-fallback launcher emits POSIX-only syntax on every
#            platform. On Windows the resulting copy-pasteable command
#            is unrunnable in cmd.exe (single quotes literal, env-var
#            syntax mismatch). F050.
#
#   probe-2: APFS case-insensitivity (macOS) mirrors NTFS case-
#            insensitivity (Windows). `bosun attach` rejects an
#            implicit-PID attach when the operator's cwd was reached
#            via a different-case path than the canonical worktree —
#            same os.Getwd() vs git-canonicalized-path divergence
#            Windows operators would hit. F052.
#
# Run from anywhere; sandbox lives in /tmp/bosun-redteam-L8/.
# Requires: /tmp/bosun_test built for the host platform.

set -euo pipefail

BOSUN="${BOSUN:-/tmp/bosun_test}"
SANDBOX="/tmp/bosun-redteam-L8"
mkdir -p "$SANDBOX"

step() { printf '\n=== %s ===\n' "$*"; }

# -------- probe-1: print-fallback POSIX-only output ---------------------------

step "probe-1: print-fallback emits POSIX shell syntax (F050)"
rm -rf "$SANDBOX/p1-repo"
mkdir -p "$SANDBOX/p1-repo"
cd "$SANDBOX/p1-repo"
git init --quiet
git config user.email "L8@local"
git config user.name "L8"
echo "# L8 probe-1" > README.md
git add README.md
git commit -m "init" --quiet
"$BOSUN" init 1 --no-load-check >/dev/null 2>&1
"$BOSUN" config set launcher print >/dev/null
out=$("$BOSUN" launch session-1 2>&1 || true)
echo "$out"
if grep -q "cd '/" <<<"$out"; then
    echo "FAIL-OBSERVED: printFallback uses POSIX single-quote shell syntax"
    echo "  On Windows cmd.exe this is unrunnable; finding F050 confirmed."
else
    echo "?? no POSIX-shape output detected; re-inspect"
fi

# -------- probe-2: APFS case-insensitivity bypass on `bosun attach` -----------

step "probe-2: APFS case-insensitive cwd defeats callerInsideWorktree (F052)"
cd "$SANDBOX/p1-repo"
worktree=$(awk '/session-1/ {print $3; exit}' < <("$BOSUN" status 2>/dev/null) || true)
if [[ -z "$worktree" ]]; then
    # Fall back to filesystem discovery.
    worktree=$(ls -d "$SANDBOX"/p1-repo-bosun-*-1 2>/dev/null | head -1)
fi
echo "canonical worktree: $worktree"
# Reach the same directory via uppercased path (APFS treats as identical inode).
case_path=$(echo "$worktree" | sed 's|p1-repo|P1-REPO|')
echo "case-shifted   path: $case_path"
if [[ ! -d "$case_path" ]]; then
    echo "skip: APFS reports this as a different path (case-sensitive filesystem); finding doesn't apply"
    exit 0
fi
cd "$case_path"
attach_out=$("$BOSUN" attach session-1 2>&1 || true)
echo "$attach_out"
if grep -qi 'not inside the session-1 worktree' <<<"$attach_out"; then
    echo "FAIL-OBSERVED: bosun attach refuses an implicit PID when cwd was reached"
    echo "  via a different-case path that names the same physical directory."
    echo "  Same divergence applies to Windows NTFS (default case-insensitive)."
    echo "  Finding F052 confirmed."
else
    echo "?? attach succeeded; bosun handled the case-shift on its own"
fi

# -------- probe-3: noop on probe ideas that require Windows runtime ----------

step "probe-3..N: SOURCE-AUDIT-ONLY findings (no runtime probe possible from Darwin)"
cat <<EOF
The following findings are source-audit only — see L8-findings.md:
  F051: Print-fallback env-var prefix uses POSIX 'KEY=val cmd' syntax that
        cmd.exe doesn't recognize (same root as F050, different surface).
  F053: Windows lockfile diagnostic surface degrades because LockFileEx with
        LOCKFILE_EXCLUSIVE_LOCK denies ALL access (including read) to other
        processes — readLockHolder from a waiting contender returns (0,0)
        and the LockTimeoutError loses HolderPID/HeldFor.
  F054: Windows AF_UNIX socket created by 'bosun mcp' is not ACL-restricted.
        os.Chmod 0o600 is a documented no-op on Windows AF_UNIX; comment in
        server.go acknowledges it but no Windows ACL alternative is wired.
  F055: NTFS multi-user safety: 0o600 file modes on history archives,
        usage ledger, claim files, MCP socket are silently ignored.
        b33f037 commit msg flags this for tests but the runtime impact
        (other local users can read the archives) isn't filed as a follow-up.
  F056: proc.Cwd on Windows missing 'pid <= 0' early return that cwd_unix.go
        has — cross-platform contract mismatch; benign today because
        tool_attach.go gates upstream, but a future caller of proc.Cwd that
        doesn't pre-validate would silently accept negative PIDs on Windows
        and reject them on Linux.
  F057: proc.IsAlive on Windows returns true for the System process (PID 4)
        which is permanently alive and unkillable — same shape as the PID 1
        gate in tool_attach.go but Windows-only and not gated. Higher
        reserved PIDs (System threads, csrss) are also permanent.
  F058: connTransport.Read does ReadBytes('\\n') and drops one trailing
        byte — does not strip a leading '\\r' from CRLF-terminated lines.
        Cross-platform stdio bridges, WSL, or future Windows stdio
        deployments would see the strict JSON decoder choke on a trailing
        \\r.
  F059: cwdInsideWorktree case-sensitivity gap (mirror of F052) inside
        the MCP path. Mirror finding to F052 but on a different surface.
  F060: Atomic-write pattern (os.Rename over existing dest) is fragile on
        Windows: rename fails with sharing violation if any process has
        the destination open. Affects spawn-tree.json, serve.pid, claim
        files, init.state, and several audit-log rotations.
EOF
