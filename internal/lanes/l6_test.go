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

	// Regression for bughunt-3 F001: a non-JSON line the server logged to stdout
	// (stored as a _raw frame) is NOT a response — the silent drop must still
	// surface as a finding, not be masked into a PASS.
	t.Run("non-JSON stdout noise does not mask a silent drop", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(1), "result": map[string]any{}},
			{"_raw": "2026/06/03 server log: ignoring malformed frame"},
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != FINDING {
			t.Errorf("noise must not count as a response; want FINDING, got %s (%s)", r.Verdict, r.Detail)
		}
	})

	// The flip side: a JSON-RPC batch RESPONSE is a JSON array, which lands as a
	// _raw frame (it can't fit Frame's map). It IS a real response and must PASS
	// — the noise fix must not turn it into a false positive.
	t.Run("batch-array response (valid JSON _raw) is a PASS", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(1), "result": map[string]any{}},
			{"_raw": `[{"jsonrpc":"2.0","id":53,"result":{}},{"jsonrpc":"2.0","id":54,"result":{}}]`},
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != PASS {
			t.Errorf("a batch-array response must PASS, got %s (%s)", r.Verdict, r.Detail)
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

	// Regression for bughunt-3 F002: an error object with NO `code` field is a
	// distinct defect from code:0 and must not be mislabeled as the code-zero
	// class.
	t.Run("missing code is its own finding, not code:0", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(55), "error": map[string]any{"message": "no such method"}}, // no code
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != FINDING || r.Finding == nil {
			t.Fatalf("missing code must be a FINDING, got %s / %v", r.Verdict, r.Finding)
		}
		if r.Finding.Class == "jsonrpc-code-zero" {
			t.Errorf("a code-absent error must not be reported as the code:0 class")
		}
		if r.Finding.Class != "jsonrpc-code-missing" {
			t.Errorf("class = %q, want jsonrpc-code-missing", r.Finding.Class)
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

	// Rejection via an isError RESULT (the MCP convention, e.g. server-everything)
	// is a valid rejection, NOT "answered without error". This is the generality
	// gap the third-party test surfaced.
	t.Run("isError result is a PASS (rejected, other MCP convention)", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(55), "result": map[string]any{"isError": true, "content": []any{}}},
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != PASS {
			t.Errorf("isError result should be a clean rejection (PASS), got %s (%s)", r.Verdict, r.Detail)
		}
	})

	// A success result (no error, isError false/absent) for something that must
	// be rejected is the actual violation — now caught instead of passing.
	t.Run("accept (success result) is a MEDIUM finding", func(t *testing.T) {
		s := &mcp.Session{Responses: []mcp.Frame{
			{"id": float64(55), "result": map[string]any{"content": []any{}}},
		}}
		r := classifyL6(surface, p, s)
		if r.Verdict != FINDING || r.Finding == nil || r.Finding.Severity != findings.Medium {
			t.Errorf("accepting an input that should be rejected must be a MEDIUM finding, got %s / %v", r.Verdict, r.Finding)
		}
		if r.Finding.Class != "accepts-invalid" {
			t.Errorf("class = %q, want accepts-invalid", r.Finding.Class)
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
