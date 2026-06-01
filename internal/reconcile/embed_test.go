package reconcile

import (
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
)

// fakeEmbed maps a title to a vector by a "semantic group" prefix tag the test
// controls (e.g. "g1:..."), so clustering is deterministic without a model.
// Same group -> identical vector (cosine 1); different group -> orthogonal.
func fakeEmbed(groups map[string]int, dims int) Embedder {
	return func(title string) ([]float32, error) {
		g := groups[title]
		v := make([]float32, dims)
		v[g] = 1
		return v, nil
	}
}

// THE GATE (and the differentiator leonard can't show): several DISTINCT
// findings that share a (Severity, Class). Structural over-merges them into one
// (the hidden-finding sin); embedding dedup keeps them separate by title.
func TestEmbedSeparatesWhatStructuralOverMerges(t *testing.T) {
	// Three distinct bugs, all LOW, all the SAME class.
	fs := []findings.Finding{
		{Severity: findings.Low, Class: "llm-generated", Title: "path traversal in file_path", Locus: "a"},
		{Severity: findings.Low, Class: "llm-generated", Title: "integer overflow in limit", Locus: "b"},
		{Severity: findings.Low, Class: "llm-generated", Title: "unicode mishandling in query", Locus: "c"},
	}

	// Structural: same (Severity, Class) -> ONE finding, the other two become a
	// false "+N more sites". That is the over-merge bug.
	if got := Dedup(fs); len(got) != 1 {
		t.Fatalf("structural should over-merge same-class into 1, got %d", len(got))
	}

	// Embedding: distinct titles (distinct groups) -> kept separate.
	emb := fakeEmbed(map[string]int{
		"path traversal in file_path":  0,
		"integer overflow in limit":    1,
		"unicode mishandling in query": 2,
	}, 4)
	got, err := DedupEmbed(fs, emb, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("embedding dedup must keep 3 distinct findings separate, got %d", len(got))
	}
}

// Documents single-linkage's known failure mode: a similarity GRADIENT chains.
// A~B and B~C both clear the threshold while A~C does not, yet all three
// collapse into one cluster (C becomes a false "+N more"). This is over-merge —
// the audit anti-goal — via chaining. It is WHY structural stays the default
// and embedding dedup is opt-in; the operator who asks for --dedup=embed is
// choosing aggressive semantic merging and will review the result.
func TestEmbedSingleLinkageChains(t *testing.T) {
	// 2D unit vectors at 0deg, 25deg, 50deg: adjacent cosine ~0.906, ends ~0.643.
	vmap := map[string][]float32{
		"A": {1, 0},
		"B": {0.9063, 0.4226},
		"C": {0.6428, 0.7660},
	}
	emb := func(title string) ([]float32, error) { return vmap[title], nil }
	fs := []findings.Finding{
		{Severity: findings.Low, Title: "A", Locus: "a"},
		{Severity: findings.Low, Title: "B", Locus: "b"},
		{Severity: findings.Low, Title: "C", Locus: "c"},
	}
	got, err := DedupEmbed(fs, emb, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	// A~C is 0.643 < 0.9, but B bridges them -> single-linkage merges all 3.
	if len(got) != 1 {
		t.Errorf("single-linkage is expected to chain a gradient into 1 cluster, got %d (if this changed, the linkage strategy changed — re-evaluate the over-merge tradeoff)", len(got))
	}
}

// The other direction: same-root findings whose titles are near-identical must
// merge into one, with the loci preserved (no finding lost).
func TestEmbedMergesNearIdentical(t *testing.T) {
	fs := []findings.Finding{
		{Severity: findings.Low, Class: "reflect-null-leak", Title: "find_symbol leaks on null", Locus: "find_symbol.query", Reproducer: "repro-1"},
		{Severity: findings.Low, Class: "reflect-null-leak", Title: "find_symbol leaks on null", Locus: "get_decisions.topic"},
		{Severity: findings.Low, Class: "reflect-null-leak", Title: "find_symbol leaks on null", Locus: "list_files.pattern"},
	}
	emb := fakeEmbed(map[string]int{"find_symbol leaks on null": 0}, 4) // all same group
	got, err := DedupEmbed(fs, emb, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("near-identical titles should merge to 1, got %d", len(got))
	}
	if got[0].Reproducer != "repro-1" {
		t.Errorf("merged finding should keep a representative reproducer, got %q", got[0].Reproducer)
	}
}

// Severity is a hard boundary: a HIGH and a LOW must not merge even with
// identical titles.
func TestEmbedRespectsSeverity(t *testing.T) {
	fs := []findings.Finding{
		{Severity: findings.High, Title: "same title", Locus: "a"},
		{Severity: findings.Low, Title: "same title", Locus: "b"},
	}
	emb := fakeEmbed(map[string]int{"same title": 0}, 4)
	got, err := DedupEmbed(fs, emb, 0.9)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("different severities must not merge, got %d", len(got))
	}
}

// A failing embedder surfaces an error so the caller can fall back to
// structural (fail open).
func TestEmbedFailReturnsError(t *testing.T) {
	fs := []findings.Finding{{Severity: findings.Low, Title: "x"}}
	bad := func(string) ([]float32, error) { return nil, errBoom }
	if _, err := DedupEmbed(fs, bad, 0.9); err == nil {
		t.Error("embedder failure must propagate so the caller falls back")
	}
}

type boomErr struct{}

func (boomErr) Error() string { return "boom" }

var errBoom = boomErr{}
