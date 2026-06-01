package reconcile

import (
	"strings"
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
)

func TestDedupCollapsesClass(t *testing.T) {
	in := []findings.Finding{
		{Severity: findings.Low, Class: "reflect-null-leak", Title: "`find_symbol` leaks on null query", Locus: "find_symbol.query", Files: []string{"a.go"}, Reproducer: "repro-A"},
		{Severity: findings.Low, Class: "reflect-null-leak", Title: "`get_decisions` leaks on null topic", Locus: "get_decisions.topic", Files: []string{"a.go", "b.go"}},
		{Severity: findings.Low, Class: "reflect-null-leak", Title: "x", Locus: "list_files.pattern"},
		{Severity: findings.Low, Class: "int64-overflow-leak", Title: "`get_claim` overflow", Locus: "get_claim.claim_id"},
	}
	out := Dedup(in)

	// Two classes -> two findings.
	if len(out) != 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(out), out)
	}

	null := out[0]
	if !strings.Contains(null.Observed, "3 sites") {
		t.Errorf("merged Observed should report 3 sites, got %q", null.Observed)
	}
	for _, want := range []string{"find_symbol.query", "get_decisions.topic", "list_files.pattern"} {
		if !strings.Contains(null.Observed, want) {
			t.Errorf("merged Observed missing locus %q: %q", want, null.Observed)
		}
	}
	// Representative reproducer survives verbatim.
	if null.Reproducer != "repro-A" {
		t.Errorf("merged finding should keep the representative reproducer, got %q", null.Reproducer)
	}
	// Files are unioned.
	if len(null.Files) != 2 {
		t.Errorf("files should union to {a.go,b.go}, got %v", null.Files)
	}
	if null.Severity != findings.Low || null.Class != "reflect-null-leak" {
		t.Errorf("merged severity/class wrong: %+v", null)
	}
}

func TestDedupPassesThroughSingletonsAndEmptyClass(t *testing.T) {
	in := []findings.Finding{
		{Severity: findings.High, Class: "server-panic", Title: "panic A", Locus: "x"},      // singleton class
		{Severity: findings.Low, Class: "", Title: "version drift", Locus: "v.go"},           // no class
		{Severity: findings.Low, Class: "", Title: "another drift"},                          // no class
	}
	out := Dedup(in)
	if len(out) != 3 {
		t.Fatalf("singletons and empty-class must pass through: got %d, want 3", len(out))
	}
	// A singleton class is unchanged (no "and N more" suffix).
	if strings.Contains(out[0].Title, "more site") {
		t.Errorf("singleton should not be rewritten as merged: %q", out[0].Title)
	}
}

func TestDedupPreservesGroupOrder(t *testing.T) {
	in := []findings.Finding{
		{Severity: findings.Medium, Class: "B", Title: "b1", Locus: "1"},
		{Severity: findings.Medium, Class: "A", Title: "a1", Locus: "2"},
		{Severity: findings.Medium, Class: "B", Title: "b2", Locus: "3"},
	}
	out := Dedup(in)
	if len(out) != 2 || out[0].Class != "B" || out[1].Class != "A" {
		t.Errorf("groups should follow first-appearance order (B then A), got %+v", out)
	}
}
