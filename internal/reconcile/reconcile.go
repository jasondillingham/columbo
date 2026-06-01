// Package reconcile collapses duplicate findings. v0.4 is the structural
// baseline: findings that share a (Severity, Class) key fold into one finding
// that lists every affected Locus. DESIGN's embedding-based dedup (v0.7) later
// replaces this; the ID-collision merger for self-numbering agents is a
// separate, v0.6 concern (Columbo numbers centrally, so it has no local job).
package reconcile

import (
	"fmt"
	"strings"

	"github.com/jasondillingham/columbo/internal/findings"
)

// maxLociListed caps how many loci are spelled out in a merged finding's
// Observed line; the rest are summarized as "+K more" (never silently
// dropped).
const maxLociListed = 12

// Dedup collapses findings sharing a (Severity, Class) into one. Findings with
// an empty Class, and any class with a single member, pass through unchanged.
// Group order follows first appearance, so output order is deterministic and a
// reconciled round stays stable run to run.
func Dedup(fs []findings.Finding) []findings.Finding {
	type group struct {
		members []findings.Finding
		order   int
	}
	groups := map[string]*group{}
	var order int
	keyOf := func(f findings.Finding) string {
		return string(f.Severity) + "|" + f.Class
	}

	for _, f := range fs {
		if f.Class == "" {
			// Never-merge: give each its own unique key so it passes through.
			k := fmt.Sprintf("\x00solo-%d", order)
			groups[k] = &group{members: []findings.Finding{f}, order: order}
			order++
			continue
		}
		k := keyOf(f)
		g := groups[k]
		if g == nil {
			g = &group{order: order}
			groups[k] = g
			order++
		}
		g.members = append(g.members, f)
	}

	// Emit in first-appearance order.
	ordered := make([]*group, len(groups))
	for _, g := range groups {
		ordered[g.order] = g
	}

	out := make([]findings.Finding, 0, len(ordered))
	for _, g := range ordered {
		if len(g.members) == 1 {
			out = append(out, g.members[0])
			continue
		}
		out = append(out, merge(g.members))
	}
	return out
}

// merge folds a same-class group into one finding: the first member is the
// representative (its title and reproducer survive), and the Observed line
// enumerates every locus so nothing is lost.
func merge(members []findings.Finding) findings.Finding {
	rep := members[0]

	var loci []string
	fileSet := map[string]bool{}
	var files []string
	for _, m := range members {
		if m.Locus != "" {
			loci = append(loci, m.Locus)
		}
		for _, fl := range m.Files {
			if !fileSet[fl] {
				fileSet[fl] = true
				files = append(files, fl)
			}
		}
	}

	f := rep
	f.Files = files
	f.Title = fmt.Sprintf("%s — and %d more site(s) of the same class", rep.Title, len(members)-1)
	f.Observed = fmt.Sprintf("%d sites of this class: %s", len(members), summarizeLoci(loci))
	// rep.Reproducer is kept verbatim (one runnable reproducer per finding).
	return f
}

func summarizeLoci(loci []string) string {
	if len(loci) <= maxLociListed {
		return strings.Join(loci, ", ")
	}
	return strings.Join(loci[:maxLociListed], ", ") + fmt.Sprintf(", +%d more", len(loci)-maxLociListed)
}
