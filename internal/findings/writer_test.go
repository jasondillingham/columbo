package findings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleRound() *Round {
	return &Round{
		Target: "leonard", N: 1, Date: "2026-06-01",
		BaselineSHA: "cd6d8ef", BaselineBuild: "go build ./cmd/...", BaselineTest: "go test ./...",
		Lanes: []LaneReport{
			{
				Lane: "L1 build invariants", Slug: "build-invariants",
				Reverified: []string{"P1 build", "P4 tests"},
				Findings: []Finding{
					{
						// Pipe- and backtick-heavy, like bosun's F002/F018, to force
						// table-cell escaping through the round-trip.
						Severity: Low,
						Title:    "`leonard --version` reports 0.52.0 | source HEAD is 0.54.0 (drift)",
						Files:    []string{"cmd/leonard/root.go"},
						Observed: "binary prints 0.52.0",
						Expected: "0.54.0",
					},
				},
			},
			{
				Lane: "L2 caps", Slug: "caps",
				Findings: []Finding{
					{Severity: Medium, Title: "INT64-max leaks `json: cannot unmarshal` internals"},
					{Title: "ambiguous null handling"}, // no severity -> UNTRIAGED
				},
			},
		},
	}
}

func TestRollupRoundTrip(t *testing.T) {
	r := sampleRound()
	r.AssignIDs()
	want := r.allFindings()

	md := r.Consolidated()
	rows := ParseRollup(md)

	if len(rows) != len(want) {
		t.Fatalf("parsed %d rows, want %d\n---\n%s", len(rows), len(want), md)
	}
	for i, row := range rows {
		w := want[i]
		if row.ID != w.ID {
			t.Errorf("row %d ID = %q, want %q", i, row.ID, w.ID)
		}
		if row.Severity != w.Severity {
			t.Errorf("row %d Severity = %q, want %q", i, row.Severity, w.Severity)
		}
		if row.Lane != w.Lane {
			t.Errorf("row %d Lane = %q, want %q", i, row.Lane, w.Lane)
		}
		if row.Title != w.Title {
			t.Errorf("row %d Title round-trip failed:\n got %q\nwant %q", i, row.Title, w.Title)
		}
		if row.Status != w.Status {
			t.Errorf("row %d Status = %q, want %q", i, row.Status, w.Status)
		}
	}
}

func TestAssignIDsAndDefaults(t *testing.T) {
	r := sampleRound()
	r.AssignIDs()
	all := r.allFindings()
	if all[0].ID != "F001" || all[1].ID != "F002" || all[2].ID != "F003" {
		t.Errorf("IDs = %q,%q,%q", all[0].ID, all[1].ID, all[2].ID)
	}
	if all[2].Severity != Untriaged {
		t.Errorf("missing severity should default to UNTRIAGED, got %q", all[2].Severity)
	}
	for _, f := range all {
		if f.Status != "confirmed" {
			t.Errorf("%s status = %q, want confirmed", f.ID, f.Status)
		}
		if f.Discovered == "" {
			t.Errorf("%s Discovered not stamped", f.ID)
		}
	}
}

func TestSeverityCell(t *testing.T) {
	if High.Cell() != "**HIGH**" || Critical.Cell() != "**CRITICAL**" {
		t.Errorf("HIGH/CRITICAL should be bold")
	}
	if Medium.Cell() != "MEDIUM" || Low.Cell() != "LOW" || Untriaged.Cell() != "UNTRIAGED" {
		t.Errorf("non-high severities should be plain")
	}
}

// The per-lane detail block (Files / Expected / Observed / Reproducer) is the
// part an operator acts on. Nothing else reads a per-lane file's body, so
// assert it here: a refactor that drops Files or mangles the reproducer fence
// must fail a test, not slip through green.
func TestLaneDetailRender(t *testing.T) {
	r := sampleRound()
	r.AssignIDs()
	var l1 LaneReport
	for _, lr := range r.Lanes {
		if lr.Slug == "build-invariants" {
			l1 = lr
		}
	}
	// Give the finding a reproducer so we can assert it survives rendering.
	l1.Findings[0].Reproducer = "grep -n Version cmd/leonard/root.go"
	md := r.Lane(l1)

	for _, want := range []string{
		"## F001 — LOW —",                     // detail heading with ID + severity
		"**Files:**",                          // files block present
		"`cmd/leonard/root.go`",               // the file itself
		"grep -n Version cmd/leonard/root.go", // the reproducer body
		"## Contracts re-verified",            // PASS probes section
		"- P1 build",                          // a re-verified contract
	} {
		if !strings.Contains(md, want) {
			t.Errorf("per-lane render missing %q\n---\n%s", want, md)
		}
	}

	// The per-lane rollup table re-parses too (same renderRollup path).
	if rows := ParseRollup(md); len(rows) != 1 || rows[0].ID != "F001" {
		t.Errorf("per-lane rollup parse = %+v, want one row F001", rows)
	}
}

// A lane that did not run must say so in both the brief and its per-lane file,
// never read as a clean "0 finding(s)" pass.
func TestSkippedLaneRendersHonestly(t *testing.T) {
	r := &Round{
		Target: "bosun", N: 1, Date: "2026-06-01",
		Lanes: []LaneReport{
			{Lane: "L2 caps", Slug: "caps", Skipped: []string{"L2 setup: no mcp-stdio surface"}},
		},
	}
	r.AssignIDs()

	brief := r.Brief()
	if !strings.Contains(brief, "SKIPPED") {
		t.Errorf("brief must mark the lane SKIPPED, got:\n%s", brief)
	}
	if strings.Contains(brief, "0 finding(s)") {
		t.Errorf("a skipped lane must NOT render as '0 finding(s)':\n%s", brief)
	}

	lane := r.Lane(r.Lanes[0])
	if !strings.Contains(lane, "**Status:** SKIPPED") {
		t.Errorf("per-lane file must carry a SKIPPED status, got:\n%s", lane)
	}

	// The consolidated rollup (the most-read file) must show per-lane status,
	// not just a findings total that reads as "audited clean".
	cons := r.Consolidated()
	if !strings.Contains(cons, "SKIPPED") {
		t.Errorf("consolidated Round status must mark the lane SKIPPED, got:\n%s", cons)
	}
}

func TestFailedLaneRendersHonestly(t *testing.T) {
	r := &Round{
		Target: "x", N: 1, Date: "2026-06-01",
		Lanes: []LaneReport{
			{Lane: "L2 caps", Slug: "caps", Failed: []string{"L2 tools/list: server returned no tools"}},
		},
	}
	r.AssignIDs()
	if !strings.Contains(r.Brief(), "FAILED to run") {
		t.Errorf("brief must mark a non-running lane FAILED:\n%s", r.Brief())
	}
}

// When findings were reconciled, the consolidated doc must disclose it, so a
// reader of the file alone knows dedup happened (and that --raw recovers all).
func TestConsolidatedDisclosesReconciliation(t *testing.T) {
	r := sampleRound()
	r.RawFindings = 28 // 3 written, 28 raw
	r.AssignIDs()
	cons := r.Consolidated()
	if !strings.Contains(cons, "Reconciled from 28 raw probe instances") {
		t.Errorf("consolidated must disclose reconciliation, got:\n%s", cons)
	}

	// When nothing was reconciled (RawFindings unset or == total), no line.
	r2 := sampleRound()
	r2.AssignIDs()
	if strings.Contains(r2.Consolidated(), "Reconciled from") {
		t.Errorf("must not claim reconciliation when none happened")
	}
}

func TestWriteRoundWriteOnce(t *testing.T) {
	dir := t.TempDir()
	r := sampleRound()
	written, err := r.WriteRound(dir, false)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if len(written) != 4 { // brief + findings + 2 lanes
		t.Errorf("wrote %d files, want 4", len(written))
	}
	// Second write without force must refuse.
	if _, err := sampleRound().WriteRound(dir, false); err == nil {
		t.Errorf("second write without --force should refuse")
	}
	// With force it overwrites.
	if _, err := sampleRound().WriteRound(dir, true); err != nil {
		t.Errorf("force write: %v", err)
	}
	// Sanity: the consolidated file exists and re-parses.
	body, err := os.ReadFile(filepath.Join(dir, "bughunt-1-findings.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ParseRollup(string(body))) != 3 {
		t.Errorf("written consolidated file should have 3 rollup rows")
	}
}
