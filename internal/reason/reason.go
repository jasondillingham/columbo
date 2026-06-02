// Package reason is Columbo's Driven control surface: the harness a Claude Code
// session drives to red-team code. The session reasons (reads the code, finds
// candidate bugs, writes reproducers); Columbo holds the round, RUNS the
// reproducers in isolation to confirm-or-kill, and writes the audit-format
// round. Columbo does not reason — the session is the model in the loop.
//
// The firewall: the session PROPOSES, execution DISPOSES. A finding is
// "confirmed" only if its reproducer actually demonstrates the bug when run; a
// reproducer is a Go test that PASSES (exit 0) iff the bug is present.
package reason

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jasondillingham/columbo/internal/findings"
)

// Reproducer is a session-written Go test that passes iff the bug is present.
type Reproducer struct {
	PkgDir string // package dir relative to the target root, e.g. "internal/store"
	Run    string // test name to run (regex for `go test -run`)
	File   string // full _test.go file content (package decl + imports + func)
}

// Status of a recorded candidate.
type Status string

const (
	Recorded  Status = "recorded"  // not yet reproduced
	Confirmed Status = "confirmed" // reproducer ran and demonstrated the bug
	Refuted   Status = "refuted"   // reproducer ran but did NOT demonstrate it
)

// Candidate is a session-reasoned finding plus its reproducer and verdict.
type Candidate struct {
	Finding    findings.Finding
	Reproducer Reproducer
	Status     Status
	RunOutput  string // captured `go test` output from the last reproduce
}

// Session holds one in-progress reason round (in-memory; ephemeral per
// columbo-mcp process). Not safe for concurrent use; the MCP server drives it
// one call at a time.
type Session struct {
	dir         string // target root
	open        bool
	candidates  []*Candidate
	laneReports []findings.LaneReport // deterministic lanes run at start (slice 2)
	reproduce   int                   // wall-clock seconds budget per reproducer (0 -> default)
}

func NewSession() *Session { return &Session{} }

// Start opens a fresh round against dir. A second Start discards any unfinalized
// round (returns a note) rather than erroring — the caller is a model.
func (s *Session) Start(dir string) (note string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("reason: %q is not a directory", dir)
	}
	if s.open && len(s.candidates) > 0 {
		note = fmt.Sprintf("discarded an in-progress round with %d unfinalized candidate(s)", len(s.candidates))
	}
	s.dir, s.open, s.candidates, s.laneReports = abs, true, nil, nil
	return note, nil
}

// SetLaneFindings attaches the deterministic-lane reports (L1/L2/L6) the
// caller ran at start, so the session gets them for free and finalize folds
// them into the round alongside the reasoned findings.
func (s *Session) SetLaneFindings(reports []findings.LaneReport) {
	s.laneReports = reports
}

// Record adds a session-reasoned candidate. Errors if no round is open.
func (s *Session) Record(f findings.Finding, r Reproducer) (int, error) {
	if !s.open {
		return 0, fmt.Errorf("reason: no round open — call reason_start first")
	}
	s.candidates = append(s.candidates, &Candidate{Finding: f, Reproducer: r, Status: Recorded})
	return len(s.candidates), nil // 1-based id
}

// Reproduce runs candidate id's reproducer in isolation and records the verdict.
// Errors (clean) on no round / bad id.
func (s *Session) Reproduce(id int) (*Candidate, error) {
	if !s.open {
		return nil, fmt.Errorf("reason: no round open — call reason_start first")
	}
	if id < 1 || id > len(s.candidates) {
		return nil, fmt.Errorf("reason: no candidate with id %d (have %d)", id, len(s.candidates))
	}
	c := s.candidates[id-1]
	out, passed, err := runReproducer(s.dir, c.Reproducer, s.reproduceTimeout())
	if err != nil {
		// Harness failure (could not isolate/build) — not a verdict on the bug.
		c.RunOutput = err.Error()
		return c, err
	}
	c.RunOutput = out
	if passed {
		c.Status = Confirmed
	} else {
		c.Status = Refuted
	}
	return c, nil
}

func (s *Session) reproduceTimeout() time.Duration {
	if s.reproduce > 0 {
		return time.Duration(s.reproduce) * time.Second
	}
	return 90 * time.Second
}

// Finalize returns the round's lane reports for the writer: the deterministic
// lanes run at start (if any) plus a "reason" lane built from the reasoned
// candidates. Confirmed candidates keep their declared severity; unconfirmed
// ones are downgraded to UNTRIAGED (the firewall: only execution-confirmed
// findings are "confirmed"). Refuses a truly empty round (no candidates AND no
// lanes) rather than emit a hollow "clean" one.
func (s *Session) Finalize() ([]findings.LaneReport, error) {
	if !s.open {
		return nil, fmt.Errorf("reason: no round open")
	}
	if len(s.candidates) == 0 && len(s.laneReports) == 0 {
		return nil, fmt.Errorf("reason: round has no candidates and ran no lanes — nothing to finalize")
	}
	out := append([]findings.LaneReport{}, s.laneReports...)
	if len(s.candidates) > 0 {
		lr := findings.LaneReport{Lane: "Reason (driven review)", Slug: "reason"}
		for _, c := range s.candidates {
			f := c.Finding
			switch c.Status {
			case Confirmed:
				f.Status = "confirmed"
			default:
				// Not execution-confirmed: never claim a severity the run didn't back.
				f.Severity = findings.Untriaged
				f.Status = "unconfirmed (reproducer did not demonstrate it)"
			}
			lr.Findings = append(lr.Findings, f)
		}
		out = append(out, lr)
	}
	s.open = false
	return out, nil
}

// Open reports whether a round is in progress (for tool status).
func (s *Session) Open() bool { return s.open }

// Dir returns the target root of the current/last round.
func (s *Session) Dir() string { return s.dir }

// Candidates returns the recorded candidates (read-only view for tooling).
func (s *Session) Candidates() []*Candidate { return s.candidates }
