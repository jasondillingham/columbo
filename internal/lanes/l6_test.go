package lanes

import (
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/probes/mcp"
	"github.com/jasondillingham/columbo/internal/probes/protocol"
	"github.com/jasondillingham/columbo/internal/target"
)

// handshakeOnly is a session where only the initialize (id=1) replied — the
// silent-drop signal for a NotSilent probe.
func handshakeOnly() *mcp.Session {
	return &mcp.Session{Responses: []mcp.Frame{{"id": float64(1), "result": map[string]any{}}}}
}

func TestClassifyL6NotSilent(t *testing.T) {
	surface := target.Surface{Command: []string{"leonard-mcp"}}
	p := protocol.Probe{Label: "deeply nested JSON", Expect: protocol.NotSilent}

	t.Run("silent drop is a MEDIUM finding", func(t *testing.T) {
		r := classifyL6(surface, p, handshakeOnly())
		if r.Verdict != FINDING || r.Finding == nil || r.Finding.Severity != findings.Medium {
			t.Errorf("want MEDIUM finding, got %s / %v", r.Verdict, r.Finding)
		}
	})

	t.Run("any response beyond handshake is a PASS", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(1), "result": map[string]any{}},
			{"id": nil, "error": map[string]any{"code": float64(-32700), "message": "parse error"}},
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != PASS {
			t.Errorf("want PASS, got %s (%s)", r.Verdict, r.Detail)
		}
	})
}

func TestClassifyL6NonzeroCode(t *testing.T) {
	surface := target.Surface{Command: []string{"leonard-mcp"}}
	p := protocol.Probe{Label: "unknown method", Expect: protocol.NonzeroCode, ID: 55}

	t.Run("code 0 is a LOW finding", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(1), "result": map[string]any{}},
			{"id": float64(55), "error": map[string]any{"code": float64(0), "message": "bad"}},
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != FINDING || r.Finding == nil || r.Finding.Severity != findings.Low {
			t.Errorf("want LOW finding, got %s / %v", r.Verdict, r.Finding)
		}
		if r.Finding.Class != "jsonrpc-code-zero" {
			t.Errorf("code:0 finding must carry dedup class, got %q", r.Finding.Class)
		}
	})

	t.Run("nonzero code is a PASS", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(55), "error": map[string]any{"code": float64(-32601), "message": "method not found"}},
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != PASS {
			t.Errorf("want PASS, got %s (%s)", r.Verdict, r.Detail)
		}
	})

	t.Run("no response to error frame is a MEDIUM finding", func(t *testing.T) {
		r := classifyL6(surface, p, handshakeOnly())
		if r.Verdict != FINDING || r.Finding == nil || r.Finding.Severity != findings.Medium {
			t.Errorf("want MEDIUM finding, got %s / %v", r.Verdict, r.Finding)
		}
	})
}

func TestClassifyL6Panic(t *testing.T) {
	surface := target.Surface{Command: []string{"leonard-mcp"}}
	p := protocol.Probe{Label: "deeply nested JSON", Expect: protocol.NotSilent}
	s := &mcp.Session{Stderr: "panic: stack overflow", Responses: []mcp.Frame{{"id": float64(1)}}}
	r := classifyL6(surface, p, s)
	if r.Verdict != FINDING || r.Finding == nil || r.Finding.Severity != findings.High {
		t.Errorf("panic should be a HIGH finding, got %s / %v", r.Verdict, r.Finding)
	}
}
