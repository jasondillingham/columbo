package reason

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
)

// tempBugRepo builds a tiny git repo with a real bug: Sum(a,b) returns a,
// ignoring b. Returns the repo root.
func tempBugRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module testbug\n\ngo 1.21\n")
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "pkg", "bug.go"),
		"package pkg\n\n// Sum should add; BUG: it ignores b.\nfunc Sum(a, b int) int { return a }\n")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		{"add", "-A"}, {"commit", "-qm", "init"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The crux: execution is the judge. A reproducer that PASSES iff the bug is
// present must land Confirmed; one whose assertion the code does NOT satisfy
// must land Refuted. Run against an isolated git worktree, never the live tree.
func TestReproduceConfirmsAndRefutes(t *testing.T) {
	repo := tempBugRepo(t)
	s := NewSession()
	if _, err := s.Start(repo); err != nil {
		t.Fatal(err)
	}

	// Real bug: Sum(2,3) returns 2 (ignores b). Test passes iff that's true.
	realID, _ := s.Record(findings.Finding{Severity: findings.High, Title: "Sum ignores b"}, Reproducer{
		PkgDir: "pkg", Run: "TestReproReal",
		File: "package pkg\nimport \"testing\"\nfunc TestReproReal(t *testing.T){ if Sum(2,3) != 2 { t.Fatal(\"bug absent\") } }\n",
	})
	// Non-bug: assert a behavior the code does NOT have -> test fails -> refuted.
	fakeID, _ := s.Record(findings.Finding{Severity: findings.High, Title: "imagined bug"}, Reproducer{
		PkgDir: "pkg", Run: "TestReproFake",
		File: "package pkg\nimport \"testing\"\nfunc TestReproFake(t *testing.T){ if Sum(2,2) != 99 { t.Fatal(\"no such bug\") } }\n",
	})

	c, err := s.Reproduce(realID)
	if err != nil {
		t.Fatalf("reproduce real: %v\n%s", err, c.RunOutput)
	}
	if c.Status != Confirmed {
		t.Errorf("real bug should be CONFIRMED, got %s\n%s", c.Status, c.RunOutput)
	}

	c2, err := s.Reproduce(fakeID)
	if err != nil {
		t.Fatalf("reproduce fake: %v", err)
	}
	if c2.Status != Refuted {
		t.Errorf("imagined bug should be REFUTED (reproducer didn't demonstrate it), got %s", c2.Status)
	}

	// The live repo must be untouched (reproducer ran in an isolate).
	if _, err := os.Stat(filepath.Join(repo, "pkg", reproTestFile)); !os.IsNotExist(err) {
		t.Errorf("reproducer file leaked into the live repo")
	}

	// Finalize: confirmed keeps severity; refuted downgrades to UNTRIAGED.
	reports, err := s.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("want 1 lane report (reason; no deterministic lanes set), got %d", len(reports))
	}
	lr := reports[0]
	if len(lr.Findings) != 2 {
		t.Fatalf("want 2 findings, got %d", len(lr.Findings))
	}
	var sawConfirmedHigh, sawUntriaged bool
	for _, f := range lr.Findings {
		if f.Status == "confirmed" && f.Severity == findings.High {
			sawConfirmedHigh = true
		}
		if f.Severity == findings.Untriaged {
			sawUntriaged = true
		}
	}
	if !sawConfirmedHigh {
		t.Error("confirmed finding should keep its HIGH severity")
	}
	if !sawUntriaged {
		t.Error("refuted finding must be downgraded to UNTRIAGED, not left as a confirmed HIGH")
	}
}

// The caller is a model; out-of-order calls get clean errors, never panics.
func TestOutOfOrderCalls(t *testing.T) {
	s := NewSession()
	if _, err := s.Record(findings.Finding{Title: "x"}, Reproducer{}); err == nil {
		t.Error("record before start must error")
	}
	if _, err := s.Reproduce(1); err == nil {
		t.Error("reproduce before start must error")
	}
	if _, err := s.Finalize(); err == nil {
		t.Error("finalize before start must error")
	}
	if _, err := s.Start(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reproduce(99); err == nil {
		t.Error("reproduce of an unknown id must error")
	}
	if _, err := s.Finalize(); err == nil {
		t.Error("finalize of an empty round must refuse (no hollow clean round)")
	}
}

// Slice 2: deterministic lanes set at start are folded into the round
// alongside the reasoned lane.
func TestFinalizeFoldsLaneFindings(t *testing.T) {
	s := NewSession()
	if _, err := s.Start(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	s.SetLaneFindings([]findings.LaneReport{
		{Lane: "L2 caps", Slug: "caps", Findings: []findings.Finding{{Severity: findings.Low, Title: "a leak"}}},
		{Lane: "L6 protocol", Slug: "protocol"},
	})
	s.Record(findings.Finding{Severity: findings.High, Title: "reasoned bug"}, Reproducer{})

	reports, err := s.Finalize()
	if err != nil {
		t.Fatal(err)
	}
	// 2 deterministic lanes + 1 reason lane.
	if len(reports) != 3 {
		t.Fatalf("want 3 lane reports (L2, L6, reason), got %d", len(reports))
	}
	if reports[2].Slug != "reason" {
		t.Errorf("reason lane should come last, got %q", reports[2].Slug)
	}
	// A round with only lane findings (no candidates) still finalizes.
	s2 := NewSession()
	s2.Start(t.TempDir())
	s2.SetLaneFindings([]findings.LaneReport{{Lane: "L1", Slug: "build-invariants"}})
	if r, err := s2.Finalize(); err != nil || len(r) != 1 {
		t.Errorf("lanes-only round should finalize to 1 report, got %d (%v)", len(r), err)
	}
}

func TestStartBadDir(t *testing.T) {
	if _, err := NewSession().Start(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("start on a non-directory must error")
	}
}
