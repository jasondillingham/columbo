#!/usr/bin/env bash
# L8 — Path-trust guard, macOS / Darwin / APFS-specific edges.
# Picks up where bughunt-4 path-trust-deep left off. Probes the policy
# boundary where pre-edit silently allows out-of-root paths
# (return-allow-on-not-my-problem) vs the post-edit ResolveSafe rejection
# path, plus APFS case-insensitivity, NFC/NFD unicode equivalence,
# /tmp -> /private/tmp realpath drift, embedded NUL/newline bytes (b4 F6
# closure), v0.52 trust-token relocation closure (bughunt-11 F1/F2), and
# MCP-side path arguments (list_files glob with traversal).
#
# Methodology:
#  - Hook payloads via harness/hook.py
#  - MCP queries via harness/mcp.py
#  - DB inspection via sqlite3
#  - Scratch fixtures isolated under fixtures/L8_scratch (don't collide
#    with L6/L7).

set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L8-path-trust

DB=.leonard/leonard.db
SCRATCH=fixtures/L8_scratch
PROJ="$(pwd)"
rm -rf "$SCRATCH"
mkdir -p "$SCRATCH"

# helpers -----------------------------------------------------------------
db_count_files()      { sqlite3 "$DB" "select count(*) from files;"; }
db_count_symbols()    { sqlite3 "$DB" "select count(*) from symbols;"; }
db_files_like()       { sqlite3 "$DB" "select path from files where path like '$1' order by path;"; }
db_symbols_for_name() { sqlite3 "$DB" "select file_path, line from symbols where name = '$1';"; }
db_count_files_like() { sqlite3 "$DB" "select count(*) from files where path like '$1';"; }

# rt_run uses bash -c subshells; export anything the test invocations
# call via rt_run.
export DB SCRATCH PROJ
export -f db_count_files db_count_symbols db_files_like db_symbols_for_name db_count_files_like

# baseline ---------------------------------------------------------------
rt_section "Baseline / orientation"

rt_run "leonard --version"           ~/go/bin/leonard --version
rt_run "leonard-hook --version"      ~/go/bin/leonard-hook --version
rt_run "leonard-mcp --version"       ~/go/bin/leonard-mcp --version
rt_run "DB file count baseline"      bash -c 'db_count_files'
rt_run "DB symbol count baseline"    bash -c 'db_count_symbols'
rt_run "readlink /tmp on Darwin"     readlink /tmp
rt_run "uname"                       uname -srm
rt_run "project realpath"            python3 -c "import os; print(os.path.realpath('.'))"
rt_run "list orphan rows count"      bash -c 'sqlite3 "$DB" "select count(*) from files where path like '\''fixtures/l3_scratch/%'\'';"'

# 1. pre-edit policy: non-.go out-of-root path -----------------------------
rt_section "1. pre-edit policy for out-of-root paths"
rt_note "pre_edit.go:216 gates on .go suffix; non-.go returns allowResponse(). bughunt-4 F1 policy: silent-allow on path-escape so legitimate sibling-project edits aren't blocked."

rt_run "pre-edit Write /etc/passwd (non-.go, non-existent gate)" \
    python3 harness/hook.py pre-edit Write /etc/passwd
rt_run "pre-edit Write /etc/passwd.go (.go suffix, ResolveSafe rejects)" \
    python3 harness/hook.py pre-edit Write /etc/passwd.go
rt_run "pre-edit Write /tmp/foo.go" \
    python3 harness/hook.py pre-edit Write /tmp/foo.go
rt_run "pre-edit Write /private/tmp/foo.go" \
    python3 harness/hook.py pre-edit Write /private/tmp/foo.go

# 2. post-edit ResolveSafe rejection on out-of-root absolute path ----------
rt_section "2. post-edit ResolveSafe rejection"

rt_run "post-edit Write /etc/passwd (expect: rejected msg, NOT indexed)" \
    python3 harness/hook.py post-edit Write /etc/passwd
rt_run "post-edit Write /tmp/foo.go" \
    python3 harness/hook.py post-edit Write /tmp/foo.go
rt_run "post-edit Write /private/tmp/foo.go" \
    python3 harness/hook.py post-edit Write /private/tmp/foo.go

# 3. /tmp vs /private/tmp realpath equivalence ------------------------------
rt_section "3. /tmp -> /private/tmp realpath drift"
rt_note "On Darwin /tmp is a symlink to /private/tmp. Both should be treated as outside the project root. ResolveSafe needs to agree across both lexical and resolved forms. b4 noted /var->/private/var clean; verify /tmp variant."

rt_run "post-edit /tmp/escape.go (lexical /tmp form)" \
    python3 harness/hook.py post-edit Write /tmp/escape.go
rt_run "post-edit /private/tmp/escape.go (resolved /private form)" \
    python3 harness/hook.py post-edit Write /private/tmp/escape.go

# 4. Symlink inside root pointing outside (existing fixture) --------------
rt_section "4. Symlink-inside-pointing-outside (b4 closure verify)"
rt_note "fixtures/symlinks/escapes_root.go -> /etc/passwd. ResolveSafe should reject via secondary EvalSymlinks check OR the symlink-with-dangling-target guard."

rt_run "ls -la fixtures/symlinks" ls -la fixtures/symlinks
rt_run "pre-edit on symlinked-out-of-root .go (expect silent-allow per F1 policy)" \
    python3 harness/hook.py pre-edit Write fixtures/symlinks/escapes_root.go
rt_run "post-edit on symlinked-out-of-root .go (expect path-escape rejection)" \
    python3 harness/hook.py post-edit Write fixtures/symlinks/escapes_root.go

# 5. .. traversal that escapes after normalization -----------------------
rt_section "5. .. traversal escape from project root"

rt_run "pre-edit ../foo.go (one-level escape)" \
    python3 harness/hook.py pre-edit Write ../foo.go
rt_run "post-edit ../foo.go" \
    python3 harness/hook.py post-edit Write ../foo.go
rt_run "post-edit ../../../etc/passwd.go (deep escape)" \
    python3 harness/hook.py post-edit Write ../../../etc/passwd.go
rt_run "post-edit ${PROJ}/../foo.go (absolute with embedded ..)" \
    python3 harness/hook.py post-edit Write "${PROJ}/../foo.go"

# 6. Cleaned no-op (./././ + //) normalization ---------------------------
rt_section "6. positive control: normalization no-ops"

mkdir -p "$SCRATCH/normalize"
cat > "$SCRATCH/normalize/foo.go" <<'EOF'
package normalize
func L8NormFoo() {}
EOF
rt_run "post-edit fixtures/L8_scratch/normalize/foo.go (clean form)" \
    python3 harness/hook.py post-edit Write fixtures/L8_scratch/normalize/foo.go
rt_run "post-edit ./././fixtures/L8_scratch/normalize/foo.go (dot-segments)" \
    python3 harness/hook.py post-edit Write ./././fixtures/L8_scratch/normalize/foo.go
rt_run "post-edit fixtures//L8_scratch//normalize//foo.go (double-slash)" \
    python3 harness/hook.py post-edit Write fixtures//L8_scratch//normalize//foo.go
rt_run "DB rows for normalize/foo.go (expect: 1)" \
    bash -c "sqlite3 \"\$DB\" \"select count(*) from files where path like 'fixtures/L8_scratch/normalize/%';\""
rt_run "DB rows verbatim path matches" \
    bash -c "sqlite3 \"\$DB\" \"select path from files where path like 'fixtures/L8_scratch/normalize/%';\""

# 7. APFS case-insensitivity ---------------------------------------------
rt_section "7. APFS case-insensitivity (b4 F3 OOS extension)"
rt_note "APFS is case-insensitive-but-preserving by default. ResolveSafe doesn't lower-case; storeKey doesn't either. If hook fires once on 'Foo.go' then once on 'FOO.go' for the *same* on-disk file, do we get one row or two?"

mkdir -p "$SCRATCH/case"
cat > "$SCRATCH/case/Camel.go" <<'EOF'
package casecheck
func L8CaseCamel() {}
EOF
rt_run "shell created file (preserves case): ls" ls -la "$SCRATCH/case/"
rt_run "post-edit fixtures/L8_scratch/case/Camel.go" \
    python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/Camel.go
rt_run "post-edit fixtures/L8_scratch/case/camel.go (lowercase variant)" \
    python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/camel.go
rt_run "post-edit fixtures/L8_scratch/case/CAMEL.GO (uppercase variant)" \
    python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/CAMEL.GO
rt_run "DB rows like fixtures/L8_scratch/case/% (expect: 1 if normalized, multiple if not)" \
    bash -c "sqlite3 \"\$DB\" \"select path from files where path like 'fixtures/L8_scratch/case/%';\""
rt_run "DB symbol L8CaseCamel locations" \
    bash -c "db_symbols_for_name L8CaseCamel"

# 8. Project-prefix case mismatch ----------------------------------------
rt_section "8. Project-dir prefix case mismatch"
rt_note "Project lives under .../projectdogwalker (lowercase 'fixtures'). file_path with uppercase FIXTURES on case-insensitive APFS resolves to the same dir. Does the guard treat them as inside-root, and does the store store both forms separately?"

cat > "$SCRATCH/case/PrefixCase.go" <<'EOF'
package casecheck
func L8PrefixCase() {}
EOF
rt_run "post-edit FIXTURES/L8_scratch/case/PrefixCase.go (uppercase fixtures)" \
    python3 harness/hook.py post-edit Write FIXTURES/L8_scratch/case/PrefixCase.go
rt_run "post-edit fixtures/L8_scratch/case/PrefixCase.go (correct case)" \
    python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/PrefixCase.go
rt_run "DB rows touching PrefixCase" \
    bash -c "sqlite3 \"\$DB\" \"select path from files where path like '%PrefixCase%';\""

# 9. Unicode NFC vs NFD ---------------------------------------------------
rt_section "9. Unicode NFC vs NFD (b4 F3 closure verify)"
rt_note "indexer.go:691-702 storeKey normalizes to NFC. Bughunt-4 F3 was closed. Verify same path passed in NFC and NFD bytes produces one row not two."

# Generate file using NFC encoding via Python
python3 - <<'PY'
import os, unicodedata
os.makedirs('fixtures/L8_scratch/unicode', exist_ok=True)
# Café in NFC: c-a-f-é(U+00E9). U+00E9 is single code point.
nfc_name = 'café.go'  # python source literal — NFC by default for é
assert unicodedata.is_normalized('NFC', nfc_name)
path = f'fixtures/L8_scratch/unicode/{nfc_name}'
with open(path, 'w') as f:
    f.write('package unicode\nfunc L8UnicodeCafe() {}\n')
print(f'wrote {path!r} (NFC bytes: {nfc_name.encode("utf-8").hex()})')
PY
rt_run "ls unicode dir" ls -la "$SCRATCH/unicode/"

# post-edit with NFC form
NFC_PATH="$(python3 -c 'import unicodedata; print(unicodedata.normalize("NFC","fixtures/L8_scratch/unicode/café.go"))')"
NFD_PATH="$(python3 -c 'import unicodedata; print(unicodedata.normalize("NFD","fixtures/L8_scratch/unicode/café.go"))')"
echo "NFC path bytes: $(echo -n "$NFC_PATH" | xxd | head -2)"
echo "NFD path bytes: $(echo -n "$NFD_PATH" | xxd | head -2)"
export NFC_PATH NFD_PATH

rt_run "post-edit with NFC bytes" \
    bash -c 'python3 harness/hook.py post-edit Write "$NFC_PATH"'
rt_run "post-edit with NFD bytes" \
    bash -c 'python3 harness/hook.py post-edit Write "$NFD_PATH"'
rt_run "DB rows like fixtures/L8_scratch/unicode/% (expect: 1)" \
    bash -c "sqlite3 \"\$DB\" \"select path from files where path like 'fixtures/L8_scratch/unicode/%';\""
rt_run "DB raw bytes of café rows" \
    bash -c "sqlite3 \"\$DB\" \"select hex(path), path from files where path like 'fixtures/L8_scratch/unicode/%';\""
rt_run "DB count L8UnicodeCafe symbol rows" \
    bash -c "sqlite3 \"\$DB\" \"select count(*) from symbols where name = 'L8UnicodeCafe';\""

# 10. Embedded NUL / newline (b4 F6 closure verify) ---------------------
rt_section "10. NUL / newline embedded in file_path (b4 F6 closure verify)"
rt_note "b4 F6 noted ResolveSafe accepted NUL and newline; suggested ContainsAny rejection. Verify post-v0.52 behavior."

rt_run "post-edit with NUL byte in file_path" \
    python3 harness/hook.py raw post-edit \
    '{"session_id":"L8-nul","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"fixtures/L8_scratch/nul ../../../etc/passwd","content":""},"tool_response":{"success":true},"cwd":"'"$PROJ"'"}'
rt_run "post-edit with newline in file_path" \
    python3 harness/hook.py raw post-edit \
    '{"session_id":"L8-nl","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"fixtures/L8_scratch/nl\n../../../etc/passwd","content":""},"tool_response":{"success":true},"cwd":"'"$PROJ"'"}'
rt_run "post-edit with carriage return in file_path" \
    python3 harness/hook.py raw post-edit \
    '{"session_id":"L8-cr","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"fixtures/L8_scratch/cr\r../../../etc/passwd","content":""},"tool_response":{"success":true},"cwd":"'"$PROJ"'"}'
rt_run "post-edit with vertical tab" \
    python3 harness/hook.py raw post-edit \
    '{"session_id":"L8-vt","hook_event_name":"PostToolUse","tool_name":"Write","tool_input":{"file_path":"fixtures/L8_scratch/vt../../../etc/passwd","content":""},"tool_response":{"success":true},"cwd":"'"$PROJ"'"}'
rt_run "DB after NUL/NL probes — any new escapy rows?" \
    bash -c "sqlite3 \"\$DB\" \"select path from files where path like '%passwd%' or path like '%fixtures/L8_scratch/nul%' or path like '%fixtures/L8_scratch/nl%';\""

# 11. Empty / pathological file_path ---------------------------------------
rt_section "11. Empty + pathological file_path"

rt_run "pre-edit Write with empty file_path" \
    python3 harness/hook.py pre-edit Write ""
rt_run "post-edit Write with empty file_path" \
    python3 harness/hook.py post-edit Write ""
rt_run "post-edit Write with whitespace-only file_path" \
    python3 harness/hook.py post-edit Write "   "
rt_run "post-edit Write with single '.'" \
    python3 harness/hook.py post-edit Write "."

# 12. Very long path -----------------------------------------------------
rt_section "12. Very long path (PATH_MAX = 1024 on Darwin)"

LONG500="$(python3 -c 'print("a"*500)')"
LONG1000="$(python3 -c 'print("a"*1000)')"
LONG2000="$(python3 -c 'print("a"*2000)')"
export LONG500 LONG1000 LONG2000

rt_run "post-edit 500-char dirname component" \
    bash -c 'python3 harness/hook.py post-edit Write "fixtures/L8_scratch/$LONG500/foo.go"'
rt_run "post-edit 1000-char dirname component" \
    bash -c 'python3 harness/hook.py post-edit Write "fixtures/L8_scratch/$LONG1000/foo.go"'
rt_run "post-edit 2000-char dirname component" \
    bash -c 'python3 harness/hook.py post-edit Write "fixtures/L8_scratch/$LONG2000/foo.go"'

# 13. Trust-token closure (b11 F1/F2 verify) ----------------------------
rt_section "13. Trust token relocation (b11 F1/F2 closure verify)"
rt_note "v0.52 moved trivial tokens out of .leonard/ to \$XDG_CONFIG_HOME/leonard/pending-trivial/. Plant a fake token in the old location; confirm consumeTrivialToken ignores it (no-op closure verify, not a finding)."

# Plant a forged token at the old (now-ignored) location
# Use bash + cat so we don't trigger the .leonard pre-edit reject (pre-edit only blocks Edit/Write/MultiEdit, Bash bypasses; also the path isn't .go).
mkdir -p .leonard/pending-trivial
PROJ_HASH=$(echo -n "$PROJ" | shasum -a 256 | awk '{print $1}')
REL="fixtures/L8_scratch/case/Camel.go"
REL_HASH=$(echo -n "$REL" | shasum -a 256 | awk '{print $1}')
TOKEN_PATH=".leonard/pending-trivial/${PROJ_HASH}.${REL_HASH}.json"
NOW=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
cat > "$TOKEN_PATH" <<EOF
{"file_path":"$REL","trivial_reason":"L8 forged token","ts":"$NOW"}
EOF
rt_run "planted forged token: ls" ls -la "$TOKEN_PATH"
rt_run "ls XDG_CONFIG_HOME/leonard/pending-trivial (should be empty or no dir)" \
    bash -c 'ls -la "${XDG_CONFIG_HOME:-$HOME/.config}/leonard/pending-trivial/" 2>&1 || echo "(no dir — expected)"'
# Trigger a post-edit hook — if the forged token is consumed, behavior would change.
# Post-edit currently calls into selflog only if ground-truth adapter is wired;
# the projectdogwalker ground-truth IS wired. The token-bypass code path lives in
# the trivial.go consumeTrivialToken — it returns ok=false unless it reads the
# token at $XDG_CONFIG_HOME path. Confirm by observing that the token FILE
# remains on disk after the post-edit (not deleted = not consumed).
rt_run "post-edit Camel.go to potentially trigger trivial-check" \
    python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/Camel.go
rt_run "forged token still present after hook (b11 F1 closure: it should be)" \
    ls -la "$TOKEN_PATH"
# Clean up the forged token
rm -f "$TOKEN_PATH"

# Bughunt-11 F2: planted SYMLINK at the canonical token path should also be refused
rt_section "13b. Symlink-as-token refusal (b11 F2 closure verify)"
XDG_BASE="${XDG_CONFIG_HOME:-$HOME/.config}"
mkdir -p "$XDG_BASE/leonard/pending-trivial"
TOKEN_PATH_CORRECT="$XDG_BASE/leonard/pending-trivial/${PROJ_HASH}.${REL_HASH}.json"
# Create attack file outside the project pointing the token at it
ATTACK=$(mktemp)
cat > "$ATTACK" <<EOF
{"file_path":"$REL","trivial_reason":"L8 symlink attack","ts":"$NOW"}
EOF
ln -sf "$ATTACK" "$TOKEN_PATH_CORRECT"
rt_run "symlink-token planted: ls" ls -la "$TOKEN_PATH_CORRECT"
rt_run "post-edit triggers trivial-check — symlink should be refused" \
    python3 harness/hook.py post-edit Write fixtures/L8_scratch/case/Camel.go
rt_run "symlink-token still present (refused, not consumed)" \
    bash -c 'ls -la "$TOKEN_PATH_CORRECT" 2>&1 || echo "(token consumed — UNEXPECTED)"'
rm -f "$TOKEN_PATH_CORRECT" "$ATTACK"

# 14. MCP path arguments — list_files with traversal pattern ----------------
rt_section "14. MCP-side: list_files pattern with traversal"
rt_note "list_files goes against the store's SQLite GLOB on the path column. Path strings are NFC-normalized in storage. Traversal patterns shouldn't escape but the relevant probe is: do they error, or just return [] silently?"

rt_run "list_files pattern=../../../etc/*" \
    python3 harness/mcp.py call list_files '{"pattern":"../../../etc/*"}'
rt_run "list_files pattern=/etc/*" \
    python3 harness/mcp.py call list_files '{"pattern":"/etc/*"}'
rt_run "list_files pattern=%2e%2e/* (URL-encoded; no decode expected)" \
    python3 harness/mcp.py call list_files '{"pattern":"%2e%2e/*"}'
rt_run "list_files pattern=fixtures/../../../etc/*" \
    python3 harness/mcp.py call list_files '{"pattern":"fixtures/../../../etc/*"}'
rt_run "verify_symbol name=../passwd (path-y name)" \
    python3 harness/mcp.py call verify_symbol '{"name":"../passwd"}'

# 15. Cleanup -------------------------------------------------------------
rt_section "15. Cleanup"

rt_run "DB files count after lane" bash -c 'db_count_files'
rt_run "DB symbols count after lane" bash -c 'db_count_symbols'
rt_run "list scratch dir" ls -la "$SCRATCH" 2>&1
rt_run "L8_scratch DB rows" \
    bash -c "sqlite3 \"\$DB\" \"select path from files where path like 'fixtures/L8_scratch/%';\""

rt_summary
