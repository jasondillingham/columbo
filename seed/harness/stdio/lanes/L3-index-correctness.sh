#!/usr/bin/env bash
# L3 — Index correctness across many edits.
# Targets: add/edit/delete/rename, hash-skip, symbol-replaced-in-place,
# symbol collisions, recent_changes semantics, post-edit hook re-index.
#
# Each probe snapshots SQLite state, mutates the filesystem (via shell —
# the lane wants to control exactly when the post-edit hook fires), then
# checks DB invariants. Findings are logged with rt_finding.
set -u
cd "$(dirname "$0")/../.."
source harness/rt.sh
rt_init L3-index-correctness

DB=.leonard/leonard.db
SCRATCH=fixtures/l3_scratch
rm -rf "$SCRATCH"
mkdir -p "$SCRATCH"

# ---- helpers ----------------------------------------------------------

db_count_files()      { sqlite3 "$DB" "select count(*) from files;"; }
db_count_symbols()    { sqlite3 "$DB" "select count(*) from symbols;"; }
db_file_hash()        { sqlite3 "$DB" "select hash from files where path = '$1';"; }
db_file_indexed_at()  { sqlite3 "$DB" "select indexed_at from files where path = '$1';"; }
db_symbol_exists()    { sqlite3 "$DB" "select count(*) from symbols where name = '$1';"; }
db_symbols_for_file() { sqlite3 "$DB" "select name from symbols where file_path = '$1' order by name;"; }
db_files_with_path()  { sqlite3 "$DB" "select count(*) from files where path = '$1';"; }

# expect_eq "<label>" "<actual>" "<expected>"
expect_eq() {
    local label="$1" actual="$2" expected="$3"
    if [ "$actual" = "$expected" ]; then
        echo "  ✓ $label: $actual"
    else
        echo "  ✗ $label: got '$actual', expected '$expected'"
        return 1
    fi
}

# reindex: run `leonard index` to pick up changes that didn't go through the hook
reindex() { ~/go/bin/leonard index 2>&1 | tail -2; }

# touch a file via shell (no hook) — used when we want to test what the
# WALKER picks up vs. what the hook picks up.
shell_write() { mkdir -p "$(dirname "$1")"; printf '%s' "$2" > "$1"; }

# `rt_run` invokes via `bash -c`, which spawns a subshell that doesn't
# inherit function definitions. Export them so the subshells see them.
# (L5 lane had the same class of bug — exit=127 silently for everything.)
export DB SCRATCH
export -f db_count_files db_count_symbols db_file_hash db_file_indexed_at
export -f db_symbol_exists db_symbols_for_file db_files_with_path
export -f expect_eq reindex shell_write

# ---- L3a — Add detection via shell + full re-index ---------------------

rt_section "L3a — Add (shell write, then leonard index)"
rt_run "snapshot baseline" bash -c "
echo files=\$(db_count_files) symbols=\$(db_count_symbols)
"
F1="$SCRATCH/add_via_shell.go"
shell_write "$F1" "package l3scratch

// AddViaShell_L3a is L3a probe symbol — added via shell, not via hook.
func AddViaShell_L3a() int { return 1 }
"
rt_run "after shell write — DB shouldn't see it yet" bash -c "
files=\$(db_count_files); syms=\$(db_count_symbols)
sym_exists=\$(db_symbol_exists AddViaShell_L3a)
echo files=\$files symbols=\$syms sym_exists=\$sym_exists
if [ \"\$sym_exists\" = '0' ]; then echo '  ✓ AddViaShell_L3a correctly NOT in DB before reindex'; else echo '  ✗ symbol appeared without reindex — hook fired unexpectedly OR background indexer'; fi
"
rt_run "leonard index — should add the new file" bash -c "reindex"
rt_run "after reindex" bash -c "
sym_exists=\$(db_symbol_exists AddViaShell_L3a)
file_present=\$(db_files_with_path '$F1')
expect_eq 'symbol exists' \"\$sym_exists\" '1'
expect_eq 'file row present' \"\$file_present\" '1'
"

# ---- L3b — Add detection via Write tool (post-edit hook re-index) ------

rt_section "L3b — Add via Write (relies on post-edit hook in this session)"
# We use Write here to test if the post-edit hook fires and indexes.
# This is the agent's natural mode. Use `cat >` is shell; instead emit a
# Python-orchestrated sentinel to confirm hook activity via /tmp.
F2="$SCRATCH/add_via_write.go"
rt_run "write F2 via shell (control — hook should NOT fire on bash redirect for Write-matcher)" bash -c "
cat > $F2 <<'EOF'
package l3scratch
func AddViaShell_L3b_Control() int { return 2 }
EOF
echo wrote $F2
sym=\$(db_symbol_exists AddViaShell_L3b_Control)
echo immediate sym_exists=\$sym
"

# ---- L3c — Edit — hash changes -> reindex picks up ---------------------

rt_section "L3c — Edit existing file — hash changes -> indexer updates symbol"
TARGET=fixtures/bulk_go/pkg0500.go
rt_run "before edit: pkg0500 row" bash -c "
hash=\$(db_file_hash $TARGET); ts=\$(db_file_indexed_at $TARGET)
echo hash=\$hash indexed_at=\$ts
"
shell_write "$TARGET" "package bulk

// BulkFunc0500_EDITED — signature changed in L3c.
func BulkFunc0500_EDITED(x, y int) int {
    return x + y + 500
}
"
sleep 1  # ensure indexed_at could change
rt_run "after edit (before reindex) — DB should still show old hash" bash -c "
hash=\$(db_file_hash $TARGET); ts=\$(db_file_indexed_at $TARGET)
old_sym=\$(db_symbol_exists BulkFunc0500)
new_sym=\$(db_symbol_exists BulkFunc0500_EDITED)
echo hash=\$hash indexed_at=\$ts old_sym=\$old_sym new_sym=\$new_sym
expect_eq 'old symbol still present (no reindex yet)' \"\$old_sym\" '1' || true
expect_eq 'new symbol NOT yet present' \"\$new_sym\" '0' || true
"
rt_run "reindex" bash -c "reindex"
rt_run "after reindex — old symbol gone, new symbol present" bash -c "
old_sym=\$(db_symbol_exists BulkFunc0500)
new_sym=\$(db_symbol_exists BulkFunc0500_EDITED)
hash=\$(db_file_hash $TARGET); ts=\$(db_file_indexed_at $TARGET)
echo hash=\$hash indexed_at=\$ts old_sym=\$old_sym new_sym=\$new_sym
expect_eq 'old symbol pruned' \"\$old_sym\" '0'
expect_eq 'new symbol present' \"\$new_sym\" '1'
"

# ---- L3d — Edit — identical content -> hash skip -----------------------

rt_section "L3d — Edit with identical bytes — hash-skip (indexed_at should NOT change)"
TARGET2=fixtures/bulk_go/pkg0700.go
rt_run "snapshot" bash -c "echo hash=\$(db_file_hash $TARGET2) indexed_at=\$(db_file_indexed_at $TARGET2)"
BEFORE_TS=$(db_file_indexed_at "$TARGET2")
# Rewrite with identical bytes
CONTENT="$(cat "$TARGET2")"
sleep 1
shell_write "$TARGET2" "$CONTENT"
rt_run "reindex with identical content" bash -c "reindex"
rt_run "after reindex — indexed_at should be UNCHANGED ($BEFORE_TS)" bash -c "
after=\$(db_file_indexed_at $TARGET2)
expect_eq 'indexed_at unchanged' \"\$after\" '$BEFORE_TS'
"

# ---- L3e — Delete — prune via reindex ----------------------------------

rt_section "L3e — Delete a file; reindex should prune the rows (FK CASCADE)"
F3="$SCRATCH/will_be_deleted.go"
shell_write "$F3" "package l3scratch
func WillBeDeleted_L3e() int { return 3 }
"
rt_run "index after add" bash -c "reindex; echo sym=\$(db_symbol_exists WillBeDeleted_L3e) file=\$(db_files_with_path $F3)"
rm "$F3"
rt_run "deleted from FS — DB still has it before reindex" bash -c "
sym=\$(db_symbol_exists WillBeDeleted_L3e); fp=\$(db_files_with_path $F3)
echo sym=\$sym file=\$fp
expect_eq 'symbol still in DB' \"\$sym\" '1' || true
"
rt_run "reindex to prune" bash -c "reindex"
rt_run "after reindex — row + symbol gone" bash -c "
sym=\$(db_symbol_exists WillBeDeleted_L3e); fp=\$(db_files_with_path $F3)
echo sym=\$sym file=\$fp
expect_eq 'symbol pruned' \"\$sym\" '0'
expect_eq 'file row pruned' \"\$fp\" '0'
"

# ---- L3f — Rename a file (same content, new path) ---------------------

rt_section "L3f — Rename — mv A->B; DB should show only B after reindex"
F4="$SCRATCH/rename_src.go"
F5="$SCRATCH/rename_dst.go"
shell_write "$F4" "package l3scratch
func RenameSubject_L3f() int { return 4 }
"
rt_run "index after add of $F4" bash -c "reindex"
mv "$F4" "$F5"
rt_run "after mv (no reindex)" bash -c "
src=\$(db_files_with_path $F4); dst=\$(db_files_with_path $F5)
echo src=\$src dst=\$dst
"
rt_run "reindex" bash -c "reindex"
rt_run "after reindex — only dst should be present" bash -c "
src=\$(db_files_with_path $F4); dst=\$(db_files_with_path $F5)
sym=\$(db_symbol_exists RenameSubject_L3f)
echo src=\$src dst=\$dst symbol=\$sym
expect_eq 'old path pruned' \"\$src\" '0'
expect_eq 'new path present' \"\$dst\" '1'
expect_eq 'symbol still indexed under new path' \"\$sym\" '1'
"

# ---- L3g — Symbol renamed in place (Foo -> Bar in same file) -----------

rt_section "L3g — Symbol replaced in place; old name should disappear, new appears"
F6="$SCRATCH/symbol_renamed.go"
shell_write "$F6" "package l3scratch
func L3g_BeforeName() int { return 5 }
"
rt_run "index initial — sym L3g_BeforeName" bash -c "reindex; echo before=\$(db_symbol_exists L3g_BeforeName) after=\$(db_symbol_exists L3g_AfterName)"
shell_write "$F6" "package l3scratch
func L3g_AfterName() int { return 5 }
"
rt_run "reindex after rename — old gone, new present" bash -c "
reindex
before=\$(db_symbol_exists L3g_BeforeName); after=\$(db_symbol_exists L3g_AfterName)
echo before=\$before after=\$after
expect_eq 'old name gone' \"\$before\" '0'
expect_eq 'new name present' \"\$after\" '1'
"

# ---- L3h — Symbol collision across files (two files, same symbol name)

rt_section "L3h — Symbol name collision across files"
F7="$SCRATCH/collide1.go"
F8="$SCRATCH/collide2.go"
shell_write "$F7" "package l3a
func L3h_Collide() int { return 7 }
"
shell_write "$F8" "package l3b
func L3h_Collide() int { return 8 }
"
rt_run "reindex" bash -c "reindex"
rt_run "DB should show 2 distinct symbols with name L3h_Collide" bash -c "
n=\$(db_symbol_exists L3h_Collide)
expect_eq 'two rows' \"\$n\" '2'
sqlite3 $DB \"select name, qualified_name, file_path from symbols where name='L3h_Collide';\"
"
rt_run "MCP verify_symbol L3h_Collide" python3 harness/mcp.py call verify_symbol '{"name":"L3h_Collide"}'

# ---- L3i — recent_changes semantics ------------------------------------

rt_section "L3i — recent_changes MCP tool"
NOW=$(date +%s)
rt_run "recent_changes since=0 (everything; cap behavior?)" python3 harness/mcp.py call recent_changes '{"since":0}'
rt_run "recent_changes since=NOW+1 (no future files)" python3 harness/mcp.py call recent_changes "{\"since\":$((NOW+1))}"
rt_run "recent_changes since=NOW-60 (recent only)" python3 harness/mcp.py call recent_changes "{\"since\":$((NOW-60))}"
rt_run "recent_changes since=-1 (negative — what?)" python3 harness/mcp.py call recent_changes '{"since":-1}'
rt_run "recent_changes limit=1000 (cap is 500 per server.go)" python3 harness/mcp.py call recent_changes '{"since":0,"limit":1000}'

# ---- cleanup ------------------------------------------------------------

rm -rf "$SCRATCH"
rt_run "cleanup reindex (prune the scratch rows)" bash -c "reindex; echo files=\$(db_count_files) syms=\$(db_count_symbols)"

# Also re-apply original pkg0500 content so other lanes see the baseline.
cat > fixtures/bulk_go/pkg0500.go <<'EOF'
package bulk

// BulkFunc0500 is fixture #500 for cap-edge testing.
func BulkFunc0500(x int) int {
    return x + 500
}
EOF
reindex >/dev/null

rt_summary
