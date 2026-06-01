// Package query reads the audit-format rounds the writer produced
// (bughunt-N-findings.md) and exposes them as structured data for the
// columbo-mcp observe surface. The written markdown is the source of truth;
// findings.ParseRollup round-trips it, so there is no separate store.
//
// Scope (v0.5): rollup-level only — ID/Severity/Lane/Title/Status. The
// reproducer, Observed, and fix-shape live in the per-lane bughunt-N-<lane>.md
// files, which this package does not parse. The audit_findings tool discloses
// that.
package query

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jasondillingham/columbo/internal/findings"
)

var roundFileRe = regexp.MustCompile(`^bughunt-(\d+)-findings\.md$`)

// Rounds returns the round numbers with a consolidated findings file in dir,
// ascending.
func Rounds(dir string) ([]int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read audits dir: %w", err)
	}
	var ns []int
	for _, e := range entries {
		if m := roundFileRe.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			ns = append(ns, n)
		}
	}
	sort.Ints(ns)
	return ns, nil
}

// resolveRound maps round 0 to the latest, and validates a specific round.
func resolveRound(dir string, round int) (int, error) {
	ns, err := Rounds(dir)
	if err != nil {
		return 0, err
	}
	if len(ns) == 0 {
		return 0, fmt.Errorf("no audit rounds found in %s", dir)
	}
	if round == 0 {
		return ns[len(ns)-1], nil
	}
	for _, n := range ns {
		if n == round {
			return round, nil
		}
	}
	return 0, fmt.Errorf("round %d not found (have %v)", round, ns)
}

func roundPath(dir string, n int) string {
	return filepath.Join(dir, fmt.Sprintf("bughunt-%d-findings.md", n))
}

// Findings returns the resolved round number and its rollup rows (round 0 =
// latest).
func Findings(dir string, round int) (int, []findings.RollupRow, error) {
	n, err := resolveRound(dir, round)
	if err != nil {
		return 0, nil, err
	}
	data, err := os.ReadFile(roundPath(dir, n))
	if err != nil {
		return 0, nil, fmt.Errorf("read round %d: %w", n, err)
	}
	return n, findings.ParseRollup(string(data)), nil
}

// Summary is the audit_status view of one completed round.
type Summary struct {
	Round      int            `json:"round"`
	Total      int            `json:"total"`
	Severity   map[string]int `json:"severity"`
	Lanes      []string       `json:"lanes"`      // per-lane status lines, e.g. "L2 caps — 2 finding(s)"
	Reconciled bool           `json:"reconciled"` // dedup was applied
}

// Summarize derives the audit_status view for a round (0 = latest). It reflects
// the LAST COMPLETED round; there is no live/running audit until the v0.6
// control surface lands.
func Summarize(dir string, round int) (*Summary, error) {
	n, rows, err := Findings(dir, round)
	if err != nil {
		return nil, err
	}
	sev := map[string]int{}
	for _, r := range rows {
		sev[string(r.Severity)]++
	}
	data, _ := os.ReadFile(roundPath(dir, n))
	return &Summary{
		Round:      n,
		Total:      len(rows),
		Severity:   sev,
		Lanes:      laneStatusLines(string(data)),
		Reconciled: strings.Contains(string(data), "Reconciled from"),
	}, nil
}

// laneStatusLines pulls the "- <lane> — <status>" bullets from the consolidated
// doc's Round-status section.
func laneStatusLines(md string) []string {
	var out []string
	inStatus := false
	for _, ln := range strings.Split(md, "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "## Round status") {
			inStatus = true
			continue
		}
		if inStatus {
			if strings.HasPrefix(t, "## ") {
				break
			}
			if strings.HasPrefix(t, "- ") {
				out = append(out, strings.TrimPrefix(t, "- "))
			}
		}
	}
	return out
}
