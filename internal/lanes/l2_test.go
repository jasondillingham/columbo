package lanes

import (
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/probes/caps"
	"github.com/jasondillingham/columbo/internal/probes/mcp"
	"github.com/jasondillingham/columbo/internal/target"
)

// Report must represent SKIP and FAIL, not drop them: a lane that did not run
// cannot be allowed to look like a clean pass in the written round.
func TestReportPreservesSkipAndFail(t *testing.T) {
	lr := Report("L2 caps", "caps", []Result{
		res("L2 setup", SKIP, "no mcp-stdio surface"),
	})
	if len(lr.Findings) != 0 || len(lr.Reverified) != 0 {
		t.Error("a SKIP-only lane must produce no findings or re-verified contracts")
	}
	if len(lr.Skipped) != 1 {
		t.Errorf("SKIP should be recorded, got Skipped=%v", lr.Skipped)
	}

	lr2 := Report("L2 caps", "caps", []Result{
		res("L2 tools/list", FAIL, "server returned no tools"),
	})
	if len(lr2.Failed) != 1 {
		t.Errorf("FAIL should be recorded, got Failed=%v", lr2.Failed)
	}
}

// errorSession builds a synthetic session whose id=2 response carries the given
// JSON-RPC error message.
func errorSession(msg, stderr string) *mcp.Session {
	return &mcp.Session{
		Stderr: stderr,
		Responses: []mcp.Frame{
			{"id": float64(2), "error": map[string]any{"message": msg}},
		},
	}
}

// classifyL2 is the verdict mapping the manual Leonard run exercises but CI
// otherwise wouldn't. The panic->HIGH branch in particular never fires against
// Leonard, so it is only covered here.
func TestClassifyL2(t *testing.T) {
	surface := target.Surface{Command: []string{"leonard-mcp"}}
	probe := caps.Probe{Tool: "find_symbol", Label: "string field `query` = null", Args: map[string]any{"query": nil}, Class: "reflect-null-leak", Locus: "find_symbol.query"}

	t.Run("leak is a LOW finding carrying the probe's dedup class/locus", func(t *testing.T) {
		s := errorSession(`<invalid reflect.Value> has type "null", want "string"`, "")
		r := classifyL2(surface, probe, s)
		if r.Verdict != FINDING {
			t.Fatalf("verdict = %s, want FINDING", r.Verdict)
		}
		if r.Finding == nil || r.Finding.Severity != findings.Low {
			t.Errorf("severity = %v, want LOW", r.Finding)
		}
		if r.Finding.Class != "reflect-null-leak" || r.Finding.Locus != "find_symbol.query" {
			t.Errorf("finding must carry the probe's Class/Locus for dedup, got class=%q locus=%q", r.Finding.Class, r.Finding.Locus)
		}
	})

	t.Run("panic is a HIGH finding", func(t *testing.T) {
		s := errorSession("", "goroutine 1 [running]:\npanic: nil map write")
		r := classifyL2(surface, probe, s)
		if r.Verdict != FINDING {
			t.Fatalf("verdict = %s, want FINDING", r.Verdict)
		}
		if r.Finding == nil || r.Finding.Severity != findings.High {
			t.Errorf("severity = %v, want HIGH", r.Finding)
		}
	})

	t.Run("clean rejection is a PASS", func(t *testing.T) {
		s := errorSession("query must not be empty", "")
		r := classifyL2(surface, probe, s)
		if r.Verdict != PASS {
			t.Errorf("verdict = %s, want PASS", r.Verdict)
		}
		if r.Finding != nil {
			t.Errorf("PASS should carry no finding")
		}
	})

	// Panic takes precedence over a leaky message: a crash is the worse outcome.
	t.Run("panic beats leak", func(t *testing.T) {
		s := errorSession("json: cannot unmarshal", "panic: boom")
		r := classifyL2(surface, probe, s)
		if r.Finding == nil || r.Finding.Severity != findings.High {
			t.Errorf("panic+leak should classify HIGH, got %v", r.Finding)
		}
	})
}
