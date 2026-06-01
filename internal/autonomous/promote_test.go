package autonomous

import (
	"os"
	"path/filepath"
	"testing"
)

// initRepo makes a temp git repo with one commit on main.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := git(dir, args...); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := git(dir, "add", "-A"); err != nil {
		t.Fatalf("add: %v %s", err, out)
	}
	if out, err := git(dir, "commit", "-qm", "init"); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	return dir
}

// writeRound simulates the round writer: drops a findings file under audits/.
func writeRound(dir string, round int) func() ([]string, error) {
	return func() ([]string, error) {
		ad := filepath.Join(dir, "audits")
		if err := os.MkdirAll(ad, 0o755); err != nil {
			return nil, err
		}
		f := filepath.Join(ad, "bughunt-"+itoa(round)+"-findings.md")
		if err := os.WriteFile(f, []byte("# round\n"), 0o644); err != nil {
			return nil, err
		}
		return []string{f}, nil
	}
}

func itoa(n int) string { return string(rune('0'+n%10)) } // single-digit rounds in tests

func TestPromoteDirtyTreeBlocks(t *testing.T) {
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "wip.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Promote(dir, 1, writeRound(dir, 1), "msg"); err == nil {
		t.Error("dirty tree must block promote")
	}
}

func TestPromoteCreatesBranchAndReturns(t *testing.T) {
	dir := initRepo(t)
	branch, err := Promote(dir, 1, writeRound(dir, 1), "audit msg")
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if branch != "audit/bughunt-1" {
		t.Errorf("branch = %q", branch)
	}
	// Returned to main, tree clean, no audits/ on main.
	if cur, _ := git(dir, "rev-parse", "--abbrev-ref", "HEAD"); cur != "main" {
		t.Errorf("should return to main, on %q", cur)
	}
	if clean, _ := CleanTree(dir); !clean {
		t.Error("tree should be clean after promote")
	}
	if _, err := os.Stat(filepath.Join(dir, "audits")); !os.IsNotExist(err) {
		t.Error("audits/ must NOT be on main (only on the audit branch)")
	}
	// The commit exists on the branch.
	if out, _ := git(dir, "log", "--oneline", branch); out == "" {
		t.Error("audit branch should have a commit")
	}
}

// The bug a single run hides: next-N must account for existing audit branches,
// or the second autonomous run recomputes the same N and collides.
func TestNextRoundIsBranchAware(t *testing.T) {
	dir := initRepo(t)
	// First promote: working tree has no rounds -> N=1.
	n1, err := NextRound(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n1 != 1 {
		t.Fatalf("first next-round = %d, want 1", n1)
	}
	if _, err := Promote(dir, n1, writeRound(dir, n1), "r1"); err != nil {
		t.Fatal(err)
	}
	// Second run: main's tree still has no audits/, but branch audit/bughunt-1
	// exists -> next must be 2, not 1.
	n2, err := NextRound(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 2 {
		t.Errorf("second next-round = %d, want 2 (branch-aware); a worktree-only calc would say 1 and collide", n2)
	}
	if _, err := Promote(dir, n2, writeRound(dir, n2), "r2"); err != nil {
		t.Errorf("second promote should not collide: %v", err)
	}
}
