package query

import (
	"path/filepath"
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
)

// writeRound writes a round to dir using the real writer, so the test exercises
// the same markdown the query layer reads in production.
func writeRound(t *testing.T, dir string, n int, reconciledFrom int, lanes []findings.LaneReport) {
	t.Helper()
	r := &findings.Round{Target: "demo", N: n, Date: "2026-06-01", RawFindings: reconciledFrom, Lanes: lanes}
	if _, err := r.WriteRound(dir, false); err != nil {
		t.Fatal(err)
	}
}

func TestRoundsAndLatest(t *testing.T) {
	dir := t.TempDir()
	writeRound(t, dir, 1, 0, []findings.LaneReport{{Lane: "L2 caps", Slug: "caps"}})
	writeRound(t, dir, 3, 0, []findings.LaneReport{{Lane: "L2 caps", Slug: "caps"}})

	ns, err := Rounds(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ns) != 2 || ns[0] != 1 || ns[1] != 3 {
		t.Fatalf("rounds = %v, want [1 3]", ns)
	}

	// round 0 resolves to the latest (3).
	n, _, err := Findings(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("latest round = %d, want 3", n)
	}
}

func TestFindingsAndSummary(t *testing.T) {
	dir := t.TempDir()
	lanes := []findings.LaneReport{
		{
			Lane: "L2 caps", Slug: "caps",
			Findings: []findings.Finding{
				{Severity: findings.Low, Title: "leak A", Class: "x"},
				{Severity: findings.Medium, Title: "leak B"},
			},
		},
		{Lane: "L6 protocol", Slug: "protocol", Skipped: []string{"no mcp-stdio"}},
	}
	writeRound(t, dir, 1, 28, lanes) // RawFindings>total => reconciled disclosed

	n, rows, err := Findings(dir, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(rows) != 2 {
		t.Fatalf("got round %d with %d rows, want 1/2", n, len(rows))
	}

	sum, err := Summarize(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Round != 1 || sum.Total != 2 {
		t.Errorf("summary round/total = %d/%d, want 1/2", sum.Round, sum.Total)
	}
	if sum.Severity["LOW"] != 1 || sum.Severity["MEDIUM"] != 1 {
		t.Errorf("severity tally = %v", sum.Severity)
	}
	if !sum.Reconciled {
		t.Errorf("summary should report reconciled (RawFindings 28 > 2)")
	}
	// Lane status lines include the SKIPPED lane (coverage honesty carries through).
	var sawSkip bool
	for _, l := range sum.Lanes {
		if filepath.Base(l) != l { // sanity: it's a plain line
		}
		if containsSkip(l) {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Errorf("summary lanes should disclose the SKIPPED lane, got %v", sum.Lanes)
	}
}

func TestFindingsMissingRound(t *testing.T) {
	dir := t.TempDir()
	writeRound(t, dir, 1, 0, []findings.LaneReport{{Lane: "L2 caps", Slug: "caps"}})
	if _, _, err := Findings(dir, 9); err == nil {
		t.Error("missing round should error")
	}
}

func containsSkip(s string) bool {
	for i := 0; i+7 <= len(s); i++ {
		if s[i:i+7] == "SKIPPED" {
			return true
		}
	}
	return false
}
