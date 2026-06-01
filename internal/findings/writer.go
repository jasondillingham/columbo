package findings

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LaneReport is one lane's contribution to a round: the findings it produced,
// the contracts it re-verified clean (PASS), and the probes that skipped or
// failed to run. Skipped/Failed are recorded — not dropped — so the written
// round can never present a lane that did not run as a clean pass.
type LaneReport struct {
	Lane       string    // display name, e.g. "L1 build invariants"
	Slug       string    // filename slug, e.g. "build-invariants"
	Findings   []Finding // FINDING-verdict probes
	Reverified []string  // names of probes that passed (no finding)
	Skipped    []string  // probes/lanes that did not apply (e.g. no mcp-stdio surface)
	Failed     []string  // probes that failed to execute (setup/build/transport errors)
}

// ran reports whether the lane produced any substantive result (a finding or a
// re-verified contract). A lane with only skips/failures did not run.
func (lr LaneReport) ran() bool {
	return len(lr.Findings)+len(lr.Reverified) > 0
}

// statusLine summarizes the lane for the brief and the per-lane header. A lane
// that did not run says so explicitly, instead of looking like "0 findings".
func (lr LaneReport) statusLine() string {
	if !lr.ran() {
		if len(lr.Failed) > 0 {
			return "FAILED to run: " + lr.Failed[0]
		}
		if len(lr.Skipped) > 0 {
			return "SKIPPED: " + lr.Skipped[0]
		}
		return "did not run (no probes)"
	}
	s := fmt.Sprintf("%d finding(s)", len(lr.Findings))
	if len(lr.Failed) > 0 {
		s += fmt.Sprintf(", %d probe failure(s)", len(lr.Failed))
	}
	return s
}

// Round is the metadata + lane reports for one audit round.
type Round struct {
	Target        string // target name, e.g. "leonard"
	N             int    // round number
	Date          string // YYYY-MM-DD; passed in so the writer stays deterministic
	BaselineSHA   string
	BaselineBuild string
	BaselineTest  string
	Lanes         []LaneReport
	// RawFindings is the count of findings BEFORE reconciliation/dedup. When it
	// exceeds the written total, the consolidated doc discloses that it was
	// reconciled (so a reader of the file alone knows dedup happened and that
	// --raw recovers every instance). Zero means "not reconciled / unknown".
	RawFindings int
}

// severityScale is copied verbatim from audits/README.md so every emitted doc
// carries the same scale the convention defines.
const severityScale = `Severity scale:
- **CRITICAL** — exploitable RCE, arbitrary file write, trust bypass
- **HIGH** — privilege boundary breach, DoS, secret leakage, state corruption that survives recovery
- **MEDIUM** — resource exhaustion within bounds, error-swallowing that masks problems, weak input validation
- **LOW** — quality, races without practical exploit paths, structural leakage, future-proofing`

// AssignIDs assigns F001.. to every finding across all lanes in order, stamps
// Discovered (date + lane), and defaults empty Status to "confirmed". It
// mutates r in place. Call once before rendering.
func (r *Round) AssignIDs() {
	n := 0
	for li := range r.Lanes {
		for fi := range r.Lanes[li].Findings {
			n++
			f := &r.Lanes[li].Findings[fi]
			f.ID = fmt.Sprintf("F%03d", n)
			f.Discovered = r.Date + " / " + r.Lanes[li].Lane
			if f.Status == "" {
				f.Status = "confirmed"
			}
			if f.Severity == "" {
				f.Severity = Untriaged
			}
		}
	}
}

// allFindings flattens the round's findings in ID order.
func (r *Round) allFindings() []Finding {
	var out []Finding
	for _, lr := range r.Lanes {
		out = append(out, lr.Findings...)
	}
	return out
}

// --- rendering ---

// Consolidated renders bughunt-N-findings.md: the cross-lane rollup table +
// round status.
func (r *Round) Consolidated() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Bughunt-%d — Findings rollup\n\n", r.N)
	fmt.Fprintf(&b, "**Round:** Bughunt #%d for %s\n", r.N, r.Target)
	fmt.Fprintf(&b, "**Date:** %s\n", r.Date)
	fmt.Fprintf(&b, "**Baseline:** see `bughunt-%d-brief.md`\n\n", r.N)
	b.WriteString(severityScale + "\n\n")

	b.WriteString("## Rollup\n\n")
	b.WriteString(renderRollup(r.allFindings()))

	b.WriteString("\n## Round status\n\n")
	tally := map[Severity]int{}
	for _, f := range r.allFindings() {
		tally[f.Severity]++
	}
	total := len(r.allFindings())
	fmt.Fprintf(&b, "Total findings: %d  (CRITICAL %d, HIGH %d, MEDIUM %d, LOW %d, UNTRIAGED %d)\n",
		total, tally[Critical], tally[High], tally[Medium], tally[Low], tally[Untriaged])
	if r.RawFindings > total {
		fmt.Fprintf(&b, "Reconciled from %d raw probe instances (re-run with --raw to see every instance).\n", r.RawFindings)
	}
	b.WriteString("\n")
	// Per-lane status, so the canonical rollup never presents a lane that did
	// not run as a clean pass (audits/README.md: "lane-by-lane sub-test counts").
	for _, lr := range r.Lanes {
		fmt.Fprintf(&b, "- %s — %s\n", lr.Lane, lr.statusLine())
	}
	return b.String()
}

// Lane renders bughunt-N-<slug>.md for one lane.
func (r *Round) Lane(lr LaneReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Lane %s\n\n", lr.Lane)
	fmt.Fprintf(&b, "**Lane:** %s (bughunt-%d)\n", lr.Slug, r.N)
	fmt.Fprintf(&b, "**Date:** %s\n", r.Date)
	fmt.Fprintf(&b, "**Baseline:** %s `%s`\n", r.Target, r.BaselineSHA)
	fmt.Fprintf(&b, "**Target:** %s\n", r.Target)
	fmt.Fprintf(&b, "**Status:** %s\n\n", lr.statusLine())
	b.WriteString(severityScale + "\n\n")

	b.WriteString("## Rollup\n\n")
	b.WriteString(renderRollup(lr.Findings))

	b.WriteString("\n## Contracts re-verified (no findings)\n\n")
	if len(lr.Reverified) == 0 {
		b.WriteString("(none recorded)\n")
	} else {
		for _, c := range lr.Reverified {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}

	if len(lr.Skipped) > 0 {
		b.WriteString("\n## Skipped (did not apply)\n\n")
		for _, s := range lr.Skipped {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	if len(lr.Failed) > 0 {
		b.WriteString("\n## Probe failures (lane execution, not target findings)\n\n")
		for _, f := range lr.Failed {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}

	for _, f := range lr.Findings {
		b.WriteString("\n---\n\n")
		b.WriteString(renderDetail(f))
	}
	return b.String()
}

// Brief renders bughunt-N-brief.md: a scaffold the operator fills in.
func (r *Round) Brief() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Bughunt-%d — Brief\n\n", r.N)
	fmt.Fprintf(&b, "**Target:** %s\n", r.Target)
	fmt.Fprintf(&b, "**Date:** %s\n", r.Date)
	fmt.Fprintf(&b, "**Baseline SHA:** %s\n", r.BaselineSHA)
	fmt.Fprintf(&b, "**Build:** `%s`\n", r.BaselineBuild)
	fmt.Fprintf(&b, "**Test:** `%s`\n\n", r.BaselineTest)
	b.WriteString("## Scope\n\nTODO(operator): what this round covers and why.\n\n")
	b.WriteString("## Lanes run\n\n")
	for _, lr := range r.Lanes {
		fmt.Fprintf(&b, "- %s (`bughunt-%d-%s.md`) — %s\n", lr.Lane, r.N, lr.Slug, lr.statusLine())
	}
	b.WriteString("\n## Prior-round context\n\nTODO(operator).\n")
	return b.String()
}

func renderDetail(f Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s — %s — %s\n\n", f.ID, f.Severity, f.Title)
	if len(f.Files) > 0 {
		b.WriteString("**Files:**\n")
		for _, fl := range f.Files {
			fmt.Fprintf(&b, "- `%s`\n", fl)
		}
		b.WriteString("\n")
	}
	if f.Expected != "" {
		fmt.Fprintf(&b, "**Expected.** %s\n\n", f.Expected)
	}
	if f.Observed != "" {
		fmt.Fprintf(&b, "**Observed.** %s\n\n", f.Observed)
	}
	if f.Reproducer != "" {
		fmt.Fprintf(&b, "**Reproducer.**\n\n```\n%s\n```\n\n", f.Reproducer)
	}
	if f.FixShape != "" {
		fmt.Fprintf(&b, "**Fix shape.** %s\n", f.FixShape)
	} else {
		b.WriteString("**Fix shape.** TODO(operator): smallest change that closes this.\n")
	}
	if f.Severity == Untriaged {
		b.WriteString("\n**TODO(operator): assign a severity.** The probe could not justify one.\n")
	}
	return b.String()
}

// renderRollup writes the | ID | Severity | Lane | Title | Status | table.
func renderRollup(fs []Finding) string {
	var b strings.Builder
	b.WriteString("| ID | Severity | Lane | Title | Status |\n")
	b.WriteString("|---|---|---|---|---|\n")
	for _, f := range fs {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n",
			escapeCell(f.ID), escapeCell(f.Severity.Cell()), escapeCell(f.Lane),
			escapeCell(f.Title), escapeCell(f.Status))
	}
	return b.String()
}

// --- parse-back (the round-trip contract) ---

// RollupRow is a parsed rollup table row. Only the table-borne fields survive
// a round-trip; the detail fields live in the per-lane docs.
type RollupRow struct {
	ID       string
	Severity Severity
	Lane     string
	Title    string
	Status   string
}

// ParseRollup extracts the rollup rows from rendered markdown. It finds the
// first `| ID | Severity | ...` header, skips the separator, and reads rows
// until the table ends.
func ParseRollup(md string) []RollupRow {
	lines := strings.Split(md, "\n")
	var rows []RollupRow
	inTable := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if !inTable {
			if strings.HasPrefix(t, "|") {
				cells := splitCells(t)
				if len(cells) >= 1 && cells[0] == "ID" {
					inTable = true
				}
			}
			continue
		}
		// In table: stop at first non-row line.
		if !strings.HasPrefix(t, "|") {
			break
		}
		if isSeparatorRow(t) {
			continue
		}
		cells := splitCells(t)
		if len(cells) < 5 {
			continue
		}
		rows = append(rows, RollupRow{
			ID:       cells[0],
			Severity: parseSeverityCell(cells[1]),
			Lane:     cells[2],
			Title:    cells[3],
			Status:   cells[4],
		})
	}
	return rows
}

func isSeparatorRow(t string) bool {
	for _, c := range splitCells(t) {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if strings.Trim(c, "-:") != "" {
			return false
		}
	}
	return true
}

func parseSeverityCell(c string) Severity {
	return Severity(strings.Trim(c, "*"))
}

// escapeCell makes s safe inside a markdown table cell and reversible by
// splitCells: backslash and pipe are escaped, newlines flattened to spaces.
func escapeCell(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "|", `\|`)
	return s
}

// splitCells splits a `| a | b |` row into trimmed, unescaped cells in one
// pass: a backslash escapes the next rune (so `\|` is literal, `\\` is a
// literal backslash), and an unescaped `|` is a cell boundary. The empty
// leading/trailing cells from the bordering pipes are dropped.
func splitCells(row string) []string {
	var cells []string
	var cur strings.Builder
	rs := []rune(row)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		if c == '\\' && i+1 < len(rs) {
			cur.WriteRune(rs[i+1])
			i++
			continue
		}
		if c == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	if strings.TrimSpace(cur.String()) != "" {
		cells = append(cells, strings.TrimSpace(cur.String()))
	}
	// Drop the empty leading cell produced by the row's first `|`.
	if len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	return cells
}

// --- file output ---

// WriteRound writes the brief, per-lane, and consolidated docs into dir. It
// refuses to overwrite an existing file for this (round, lane) unless force
// is set, in which case it overwrites wholesale (no merge of operator edits;
// that is v0.4 reconcile work).
func (r *Round) WriteRound(dir string, force bool) ([]string, error) {
	r.AssignIDs()
	files := map[string]string{
		fmt.Sprintf("bughunt-%d-brief.md", r.N):    r.Brief(),
		fmt.Sprintf("bughunt-%d-findings.md", r.N): r.Consolidated(),
	}
	for _, lr := range r.Lanes {
		files[fmt.Sprintf("bughunt-%d-%s.md", r.N, lr.Slug)] = r.Lane(lr)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if !force {
		for name := range files {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				return nil, fmt.Errorf("%s already exists; pass --force to overwrite or choose another round number", p)
			}
		}
	}
	var written []string
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return written, err
		}
		written = append(written, p)
	}
	return written, nil
}
