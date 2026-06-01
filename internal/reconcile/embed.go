package reconcile

import (
	"fmt"
	"math"

	"github.com/jasondillingham/columbo/internal/findings"
)

// Embedder returns a vector for text. The reconcile package stays free of any
// HTTP/model dependency; the caller injects an embedder (ollama in prod, a
// synthetic map in tests).
type Embedder func(text string) ([]float32, error)

// DefaultEmbedThreshold is deliberately conservative. The loss is asymmetric:
// under-merging costs a duplicate row a human skims; over-merging HIDES a
// finding. So only near-identical titles merge.
const DefaultEmbedThreshold = 0.88

// DedupEmbed collapses findings by semantic similarity of their titles, within
// the same severity. Unlike structural Dedup (exact Severity+Class key), it
// merges same-root findings whose Class keys don't align AND, crucially, does
// NOT merge distinct findings that happen to share a Class. Returns an error if
// any embedding fails — the caller must fall back to structural Dedup (fail
// open; the model is an augmentation, never a gate).
func DedupEmbed(fs []findings.Finding, emb Embedder, threshold float64) ([]findings.Finding, error) {
	vecs := make([][]float32, len(fs))
	for i, f := range fs {
		v, err := emb(f.Title)
		if err != nil {
			return nil, fmt.Errorf("embed %q: %w", f.Title, err)
		}
		vecs[i] = v
	}

	// Greedy single-linkage clustering, within-severity, in first-appearance
	// order (deterministic given the vectors). Single-linkage (join if similar
	// to ANY cluster member, not just the first) is what lets near-identical
	// titles chain into one cluster; comparing only to a representative
	// under-merges. Safe here because the conservative threshold keeps a clear
	// gap between distinct classes (no bridging).
	type cluster struct {
		members []findings.Finding
		vecs    [][]float32
		sev     findings.Severity
	}
	var clusters []*cluster
	for i, f := range fs {
		placed := false
		for _, c := range clusters {
			if c.sev != f.Severity {
				continue
			}
			for _, mv := range c.vecs {
				if cosine(vecs[i], mv) >= threshold {
					c.members = append(c.members, f)
					c.vecs = append(c.vecs, vecs[i])
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if !placed {
			clusters = append(clusters, &cluster{members: []findings.Finding{f}, vecs: [][]float32{vecs[i]}, sev: f.Severity})
		}
	}

	out := make([]findings.Finding, 0, len(clusters))
	for _, c := range clusters {
		if len(c.members) == 1 {
			out = append(out, c.members[0])
			continue
		}
		out = append(out, merge(c.members))
	}
	return out, nil
}

// cosine similarity; 0 for a zero-magnitude or mismatched-length vector.
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
