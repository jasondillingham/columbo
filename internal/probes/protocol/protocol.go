// Package protocol generates JSON-RPC protocol-fuzz probes for the L6 lane.
// Like caps, it is target-agnostic: the probes are framing edges and spec
// violations, not target-specific tool calls.
//
// Two gradeable signals cover the seed L6's findings:
//
//   - NotSilent: a malformed or unusual frame must elicit SOME JSON-RPC error,
//     never silence. Covers F019 (parse error swallowed), F021 (BOM dropped),
//     F023 (deep-nest dropped), F025 (batch dropped). Silence is the FINDING.
//   - NonzeroCode: an error response must carry a nonzero JSON-RPC code. Covers
//     F004 (code:0). code==0 is the FINDING; silence is also a FINDING.
//
// Every probe is sent as raw bytes after a real handshake, so the handshake
// reply provides fast first data (the silent-drop probes do not pay the full
// before-first-byte deadline). Some frames deliberately violate the framing
// contract, which is why they go through the client's RawActions escape hatch.
package protocol

import "strings"

// Expect is how a probe's session should be graded.
type Expect int

const (
	// NotSilent: the server must respond to the frame (error or answer), not
	// drop it silently. Silence is a MEDIUM finding.
	NotSilent Expect = iota
	// NonzeroCode: the response for ID must be an error with a nonzero code.
	// code==0 is a LOW finding; no response is a MEDIUM (silent) finding.
	NonzeroCode
)

// Probe is one protocol-fuzz frame plus how to grade the result.
type Probe struct {
	Label  string
	Frame  string // raw bytes sent after the handshake (newline appended by the lane)
	Expect Expect
	ID     int // the JSON-RPC id the response is checked against (NonzeroCode only)
}

const bom = "\ufeff" // UTF-8 BOM as an escape; a literal BOM byte breaks the Go parser

// Probes returns the fixed L6 battery.
func Probes() []Probe {
	deep := strings.Repeat("[", 5000) + strings.Repeat("]", 5000)
	return []Probe{
		{
			Label:  "truncated JSON frame (parse error expected)",
			Frame:  `{"jsonrpc":"2.0","id":50,"method":`,
			Expect: NotSilent,
		},
		{
			Label:  "UTF-8 BOM prefix on a valid request",
			Frame:  bom + `{"jsonrpc":"2.0","id":51,"method":"tools/list"}`,
			Expect: NotSilent,
		},
		{
			Label:  "deeply nested JSON (5000 levels)",
			Frame:  `{"jsonrpc":"2.0","id":52,"method":"tools/list","params":` + deep + `}`,
			Expect: NotSilent,
		},
		{
			Label:  "JSON-RPC batch array",
			Frame:  `[{"jsonrpc":"2.0","id":53,"method":"tools/list"},{"jsonrpc":"2.0","id":54,"method":"tools/list"}]`,
			Expect: NotSilent,
		},
		{
			Label:  "unknown method error code (must be nonzero)",
			Frame:  `{"jsonrpc":"2.0","id":55,"method":"columbo/nosuch"}`,
			Expect: NonzeroCode,
			ID:     55,
		},
		{
			Label:  "unknown tool error code (must be nonzero)",
			Frame:  `{"jsonrpc":"2.0","id":56,"method":"tools/call","params":{"name":"__columbo_nope__","arguments":{}}}`,
			Expect: NonzeroCode,
			ID:     56,
		},
	}
}
