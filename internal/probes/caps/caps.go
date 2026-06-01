// Package caps generates generic input-cap and schema-violation probes for the
// L2 lane. It is target-agnostic: probes are derived from each tool's input
// schema (types, required fields, additionalProperties), so the same battery
// runs against any MCP server.
//
// The probe battery targets the portable bug classes the seed runs found:
//   - INT64-max into an integer field (the F002 "json: cannot unmarshal number
//     ... overflows" leak).
//   - null / wrong-type into a typed field (the F003 reflect-internals leak).
//   - an unknown field when additionalProperties is false.
//   - an oversized string (cap probing).
//
// The verdict is intentionally narrow for v0.3 (see LeaksInternals): a probe is
// a FINDING only when the server leaks Go internals or panics. Judging
// "silently accepted an input it should have rejected" needs per-field intent
// the schema does not carry, so it is deferred (noted in the L2 lane).
package caps

import (
	"sort"
	"strings"

	"github.com/jasondillingham/columbo/internal/probes/mcp"
)

// maxInt64 as a JSON number: the value that triggers the float64-precision /
// int64-overflow unmarshal leak.
const maxInt64 = 9223372036854775807

// oversizedLen is the size of the oversized-string probe value.
const oversizedLen = 1 << 20 // 1 MiB

// Probe is one generated cap/schema probe against a single tool. Class and
// Locus drive dedup: Class keys the bug shape (so the 13 null-string leaks
// fold into one), Locus names the specific site.
type Probe struct {
	Tool  string
	Label string         // human description for the result line + finding title
	Args  map[string]any // the (deliberately malformed) tool arguments
	Class string         // dedup key for any finding this probe produces
	Locus string         // tool.field this probe targets
}

// Generate builds the probe battery for a set of tools.
func Generate(tools []mcp.Tool) []Probe {
	var probes []Probe
	for _, t := range tools {
		// Iterate properties in sorted order: map iteration is randomized per
		// process, and an audit tool's output (and its F-numbering) must be
		// deterministic run to run, not reshuffled every invocation.
		names := make([]string, 0, len(t.Properties))
		for name := range t.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			typ := t.Properties[name]
			loc := t.Name + "." + name
			switch typ {
			case "integer", "number":
				probes = append(probes,
					Probe{t.Name, "int field `" + name + "` = INT64-max", map[string]any{name: int64(maxInt64)}, "int64-overflow-leak", loc},
					Probe{t.Name, "int field `" + name + "` = string", map[string]any{name: "not-a-number"}, "type-mismatch-leak", loc},
				)
			case "string":
				probes = append(probes,
					Probe{t.Name, "string field `" + name + "` = null", map[string]any{name: nil}, "reflect-null-leak", loc},
					Probe{t.Name, "string field `" + name + "` = integer", map[string]any{name: 123}, "type-mismatch-leak", loc},
					Probe{t.Name, "string field `" + name + "` oversized (1 MiB)", map[string]any{name: strings.Repeat("x", oversizedLen)}, "oversize-leak", loc},
				)
			case "array":
				probes = append(probes,
					Probe{t.Name, "array field `" + name + "` = [null]", map[string]any{name: []any{nil}}, "array-null-leak", loc},
				)
			}
		}
		if !t.AdditionalProperties {
			probes = append(probes,
				Probe{t.Name, "unknown extra field (additionalProperties:false)", map[string]any{"columbo_unknown_field": 42}, "additional-properties-leak", t.Name},
			)
		}
	}
	return probes
}

// leakMarkers are substrings that unambiguously indicate Go internals leaking
// into a user-visible error: the encoding/json unmarshal path and reflect
// internals.
//
// Deliberately NOT included: bare "has type ... want ..." or "of type int".
// Leonard's JSON-Schema validator legitimately says
// `validating /properties/limit: type: X has type "string", want "integer"`,
// which is a reasonable operator-facing message, not a leak. Matching it would
// be a false positive (and an embarrassing one, given what Columbo is for). The
// genuine reflect leak (`<invalid reflect.Value> has type ...`) is still caught
// by the reflect.Value marker.
var leakMarkers = []string{
	"json: cannot unmarshal",
	"cannot unmarshal number",
	"overflows",
	"reflect.Value", // catches "<invalid reflect.Value>"
	"reflect:",
	"Go struct field", // encoding/json exposing the target struct
}

// LeaksInternals reports whether msg exposes Go-internal error shapes that
// should never reach an operator.
func LeaksInternals(msg string) bool {
	for _, m := range leakMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}

// Panicked reports whether the server's stderr shows a Go panic (a DoS-class
// crash, not a clean rejection).
func Panicked(stderr string) bool {
	return strings.Contains(stderr, "panic:") || strings.Contains(stderr, "runtime error:")
}
