#!/usr/bin/env python3
"""Generate adversarial fixtures for Leonard red-teaming.

Deterministic — re-running produces byte-identical output, so reproducers stay
stable. Sizes are picked to bracket known Leonard caps:

- 1500 .go files in fixtures/bulk_go/   → over the list_files cap (1000)
- 600 symbols in fixtures/symbols/many_syms.go → over MaxSymbolResults (500)
- 1 file with a >1MB symbol body in fixtures/edge/huge_func.go
- unicode-named files & symbols in fixtures/edge/unicode_*
- BOM-prefixed Python file in fixtures/edge/bom_utf8.py
- nested package depth in fixtures/deep/<a/b/c/.../zz>/pkg.go (10 deep)
- empty files, zero-byte test
- a file that LOOKS like Go but is actually Rust (extension mismatch)

Usage:  python3 harness/gen_fixtures.py
"""
import os
import pathlib

ROOT = pathlib.Path(__file__).resolve().parent.parent / "fixtures"


def ensure_clean(p: pathlib.Path):
    p.mkdir(parents=True, exist_ok=True)


def write(path: pathlib.Path, content: str):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)


def write_bytes(path: pathlib.Path, content: bytes):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(content)


def gen_bulk_go(n=1500):
    """Many small Go files — one func each. Pushes list_files past its cap and
    gives find_symbol enough rows that limit semantics are observable."""
    base = ROOT / "bulk_go"
    ensure_clean(base)
    for i in range(n):
        write(base / f"pkg{i:04d}.go", f"""package bulk

// BulkFunc{i:04d} is fixture #{i} for cap-edge testing.
func BulkFunc{i:04d}(x int) int {{
    return x + {i}
}}
""")


def gen_many_symbols(count=600):
    """One Go file with `count` exported funcs — exercises MaxSymbolResults
    (500) and SQL LIMIT correctness."""
    p = ROOT / "symbols" / "many_syms.go"
    parts = ["package symbols", ""]
    for i in range(count):
        parts.append(f"func ManySym{i:04d}() int {{ return {i} }}")
    write(p, "\n".join(parts) + "\n")


def gen_huge_function():
    """One func with a >1MiB body. Tests snippet caps in hook payloads
    (1MiB) and parser memory under a single oversized symbol."""
    p = ROOT / "edge" / "huge_func.go"
    body_line = '    _ = "padding to make this function very large indeed"\n'
    body = body_line * (1024 * 24)  # roughly 1.2 MiB of body text
    content = "package edge\n\nfunc HugeFunc() {\n" + body + "}\n"
    write(p, content)


def gen_unicode():
    """Unicode-named files and unicode-named symbols. NFC vs NFD canonicalization
    is a known macOS foot-gun. Also test homoglyph (Cyrillic 'a' = U+0430)."""
    e = ROOT / "edge"
    # NFC form ("café" with é as single codepoint U+00E9)
    write(e / "café.go", "package edge\n\nfunc CaféNFC() int { return 1 }\n")
    # NFD form ("café" with é as e + combining acute U+0301) — written as a
    # separate file. On HFS+/APFS macOS this MAY resolve to the same inode.
    write(e / "café.go", "package edge\n\nfunc CaféNFD() int { return 2 }\n")
    # Cyrillic-a homoglyph in a symbol name
    write(e / "homoglyph.go", "package edge\n\nfunc Pаssword() int { return 3 }\n  // U+0430 not 'a'\n")
    # Right-to-left override in a symbol name (visual spoofing)
    write(e / "rtlo.go", "package edge\n\nfunc Reverse‮me() int { return 4 }\n")


def gen_bom_utf8():
    """UTF-8 BOM at the start of a Python file. tree-sitter typically tolerates
    it but our parsers may or may not."""
    p = ROOT / "edge" / "bom_utf8.py"
    write_bytes(p, b"\xef\xbb\xbfdef bom_function():\n    return 'utf8 bom'\n")


def gen_nested():
    """10-deep package nesting."""
    path = ROOT / "deep"
    for letter in "abcdefghij":
        path = path / letter
    write(path / "leaf.go", "package leaf\n\nfunc DeeplyNested() int { return 42 }\n")


def gen_empty_and_pathological():
    e = ROOT / "edge"
    write(e / "empty.go", "")
    write(e / "only_comment.go", "// no declarations\npackage edge\n")
    # A file with a syntactically broken Go declaration. Should not crash the parser.
    write(e / "broken.go", "package edge\n\nfunc Broken( {\n  // unterminated\n")
    # A file with extension .go but the contents are Rust — language detection
    # should reject or at least not lie about it.
    write(e / "looks_like_go_is_rust.go",
          "// Misleading extension — actually Rust.\nfn rust_in_a_go_file() -> i32 { 7 }\n")


def gen_symlink_traps():
    """Symlink that points outside the project root. On a Darwin host, /etc/passwd
    is a stable target. Leonard should refuse to follow these for both indexing
    and audit log writes."""
    e = ROOT / "symlinks"
    e.mkdir(parents=True, exist_ok=True)
    out = e / "escapes_root.go"
    if out.is_symlink() or out.exists():
        out.unlink()
    os.symlink("/etc/passwd", out)


def main():
    ROOT.mkdir(parents=True, exist_ok=True)
    gen_bulk_go(1500)
    gen_many_symbols(600)
    gen_huge_function()
    gen_unicode()
    gen_bom_utf8()
    gen_nested()
    gen_empty_and_pathological()
    gen_symlink_traps()
    print(f"fixtures generated under {ROOT}")
    # Quick tally for sanity
    counts = {}
    for p in ROOT.rglob("*"):
        if p.is_file():
            counts.setdefault(p.suffix or "(none)", 0)
            counts[p.suffix or "(none)"] += 1
    for k, v in sorted(counts.items()):
        print(f"  {k}: {v}")


if __name__ == "__main__":
    main()
