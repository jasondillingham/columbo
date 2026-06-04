package lanes

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/probes/caps"
	"github.com/jasondillingham/columbo/internal/probes/mcp"
	"github.com/jasondillingham/columbo/internal/probes/protocol"
	"github.com/jasondillingham/columbo/internal/target"
)

const l6ProbeTimeout = 5 * time.Second

// RunL6 runs the L6 protocol-fuzz lane against the target's mcp-stdio surface.
// It builds the server once to a temp dir, then sends each fuzz frame as raw
// bytes after a handshake, one fresh process per probe.
//
// SKIPs cleanly when there is no mcp-stdio surface. FINDING on silent-drop of a
// frame that should error (MEDIUM, F019/F021/F023/F025 class), on a zero
// JSON-RPC error code (LOW, F004 class), or on a server panic (HIGH).
func RunL6(t *target.Target) []Result {
	surface, ok := t.MCPStdio()
	if !ok {
		return []Result{res("L6 setup", SKIP, "target exposes no mcp-stdio surface")}
	}
	argv, dir, cleanup, err := buildMCPServer(t, surface)
	if err != nil {
		return []Result{res("L6 setup", FAIL, err.Error())}
	}
	defer cleanup()

	client := mcp.New(argv, dir, l6ProbeTimeout)
	var out []Result
	for _, p := range protocol.Probes() {
		s, err := client.RawActions(true, mcp.Action{Bytes: []byte(p.Frame + "\n")})
		if err != nil {
			out = append(out, res("L6 "+p.Label, FAIL, err.Error()))
			continue
		}
		out = append(out, classifyL6(surface, p, s))
	}
	return out
}

// classifyL6 maps one protocol probe's session to a verdict. Pure (no I/O):
// reads only the session's captured stderr/responses, unit-testable with
// synthetic sessions. Same shape as classifyL2.
func classifyL6(surface target.Surface, p protocol.Probe, s *mcp.Session) Result {
	probe := "L6 " + p.Label
	if caps.Panicked(s.Stderr) {
		return Result{probe, FINDING, "server panicked: " + tail(s.Stderr, 8),
			&findings.Finding{
				Severity:   findings.High,
				Title:      fmt.Sprintf("MCP server panics on %s", p.Label),
				Observed:   tail(s.Stderr, 8),
				Expected:   "a clean JSON-RPC error, not a crash",
				Reproducer: reproFrame(surface, p),
				Class:      "server-panic",
				Locus:      p.Label,
			}}
	}

	switch p.Expect {
	case protocol.NotSilent:
		if respondedBeyondHandshake(s) {
			return res(probe, PASS, "server responded (not silently dropped)")
		}
		return Result{probe, FINDING, "silently dropped (no JSON-RPC error response)",
			&findings.Finding{
				Severity:   findings.Medium, // F019/F021/F023/F025 silent-drop class
				Title:      fmt.Sprintf("frame silently dropped, no error response: %s", p.Label),
				Observed:   "no response beyond the handshake",
				Expected:   "a JSON-RPC error (e.g. -32700 parse error), per JSON-RPC 2.0 §5",
				Reproducer: reproFrame(surface, p),
				Class:      "silent-drop",
				Locus:      p.Label,
			}}

	case protocol.NonzeroCode:
		f := s.Find(p.ID)
		if f == nil {
			return Result{probe, FINDING, "no response to the error-triggering frame",
				&findings.Finding{
					Severity:   findings.Medium,
					Title:      fmt.Sprintf("no error response: %s", p.Label),
					Observed:   fmt.Sprintf("no response for id=%d", p.ID),
					Expected:   "a JSON-RPC error response",
					Reproducer: reproFrame(surface, p),
					Class:      "silent-drop",
					Locus:      p.Label,
				}}
		}
		// A "must be rejected" frame can be rejected two valid ways: a JSON-RPC
		// error, OR a tool result with isError:true (the MCP SDK convention).
		// Both are clean rejections. The actual violation is being ACCEPTED — a
		// success result for something that should have been refused.
		if code, hasError, hasCode := errorCode(f); hasError {
			if !hasCode {
				// An error object with no numeric `code` is malformed, but it is
				// NOT "code 0" — report it as its own defect, not the code:0 class.
				return Result{probe, FINDING, "JSON-RPC error object is missing the required `code` field",
					&findings.Finding{
						Severity:   findings.Low,
						Title:      fmt.Sprintf("JSON-RPC error missing required `code` field on %s", p.Label),
						Observed:   "error object with no `code` field",
						Expected:   "a JSON-RPC error carrying an integer `code` (JSON-RPC 2.0 §5.1)",
						Reproducer: reproFrame(surface, p),
						Class:      "jsonrpc-code-missing",
						Locus:      p.Label,
					}}
			}
			if code == 0 {
				return Result{probe, FINDING, "JSON-RPC error code is 0 (spec reserves nonzero)",
					&findings.Finding{
						Severity:   findings.Low, // F004 class
						Title:      fmt.Sprintf("JSON-RPC error uses code:0 on %s", p.Label),
						Observed:   "error code 0",
						Expected:   "a nonzero JSON-RPC error code (e.g. -32601 / -32602)",
						Reproducer: reproFrame(surface, p),
						Class:      "jsonrpc-code-zero",
						Locus:      p.Label,
					}}
			}
			return res(probe, PASS, fmt.Sprintf("clean error, code %d", code))
		}
		if resultIsError(f) {
			// Rejected via an isError result — a valid MCP convention, not a
			// finding. (Previously misread as "answered without error".)
			return res(probe, PASS, "rejected via isError result")
		}
		// No JSON-RPC error and isError is false/absent: the server ACCEPTED an
		// input it should have rejected (e.g. an unknown tool returning success).
		return Result{probe, FINDING, "accepted (no error, isError not set) — should have been rejected",
			&findings.Finding{
				Severity:   findings.Medium,
				Title:      fmt.Sprintf("server accepted what it should reject: %s", p.Label),
				Observed:   "success result, no JSON-RPC error and isError not set",
				Expected:   "a rejection — JSON-RPC error or an isError result",
				Reproducer: reproFrame(surface, p),
				Class:      "accepts-invalid",
				Locus:      p.Label,
			}}
	}
	return res(probe, PASS, "")
}

// respondedBeyondHandshake reports whether the server emitted any response
// other than the handshake's initialize reply (id=1). A parse error with a
// null id, or an answer to the probe frame, both count.
//
// A _raw frame is a stdout line the client could not parse as a JSON object.
// Counting it blindly masks a silent drop: a server that logs noise to stdout
// while dropping a malformed frame would read as "responded" (bughunt-3 F001).
// But a JSON-RPC BATCH response is a JSON array, which also lands as _raw
// (it can't fit Frame's map shape) and IS a real response. So a _raw frame
// counts only when it is itself valid JSON; non-JSON log noise does not.
func respondedBeyondHandshake(s *mcp.Session) bool {
	for _, r := range s.Responses {
		if n, ok := r["id"].(float64); ok && n == 1 {
			continue // the handshake initialize reply
		}
		if raw, isRaw := r["_raw"].(string); isRaw {
			if json.Valid([]byte(raw)) {
				return true // a structured (e.g. batch-array) response
			}
			continue // non-JSON stdout noise is not a response
		}
		return true // a parsed JSON-RPC object that isn't the handshake reply
	}
	return false
}

// errorCode extracts a JSON-RPC error code from a response frame. hasError is
// true when an `error` object is present; hasCode is true only when that object
// carries a numeric `code`. The two are distinct: an error with NO `code` is a
// different defect (JSON-RPC 2.0 §5.1 requires it) from an error with code:0,
// and must not be reported as "code 0" (bughunt-3 F002).
func errorCode(f mcp.Frame) (code int, hasError, hasCode bool) {
	e, ok := f["error"].(map[string]any)
	if !ok {
		return 0, false, false
	}
	if c, ok := e["code"].(float64); ok {
		return int(c), true, true
	}
	return 0, true, false // error object present but no numeric code
}

// resultIsError reports whether the frame is a tool result flagged isError:true
// (the MCP convention for a tool-level rejection, distinct from a JSON-RPC error).
func resultIsError(f mcp.Frame) bool {
	res, ok := f["result"].(map[string]any)
	if !ok {
		return false
	}
	ie, _ := res["isError"].(bool)
	return ie
}

func reproFrame(surface target.Surface, p protocol.Probe) string {
	cmd := "leonard-mcp"
	if len(surface.Command) > 0 {
		cmd = surface.Command[0]
	}
	return fmt.Sprintf("%s | (handshake, then send raw) %s", cmd, oneLine(p.Frame))
}
