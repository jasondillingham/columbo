package caps

import (
	"testing"

	"github.com/jasondillingham/columbo/internal/probes/mcp"
)

func TestLeaksInternals(t *testing.T) {
	leaky := []string{
		`json: cannot unmarshal number 9223372036854776000 into Go struct field AttachArgs.pid of type int`,
		`<invalid reflect.Value> has type "null", want "string"`,
		`strconv: value overflows int64`,
	}
	for _, m := range leaky {
		if !LeaksInternals(m) {
			t.Errorf("should flag as leak: %q", m)
		}
	}
	clean := []string{
		"query must not be empty",
		"claim 42 not found",
		// Leonard's JSON-Schema validator message: operator-facing, NOT a leak.
		`validating /properties/limit: type: not-a-number has type "string", want "integer"`,
		"",
	}
	for _, m := range clean {
		if LeaksInternals(m) {
			t.Errorf("should NOT flag clean message: %q", m)
		}
	}
}

func TestPanicked(t *testing.T) {
	if !Panicked("goroutine 1 [running]:\npanic: nil map") {
		t.Error("should detect panic")
	}
	if Panicked("leonard-mcp: clean shutdown") {
		t.Error("should not flag clean stderr")
	}
}

func TestGenerate(t *testing.T) {
	tools := []mcp.Tool{{
		Name:                 "find_symbol",
		Properties:           map[string]string{"query": "string", "limit": "integer", "tags": "array"},
		Required:             []string{"query"},
		AdditionalProperties: false,
	}}
	probes := Generate(tools)

	// 3 string + 2 integer + 1 array + 1 additionalProperties probe = 7.
	// (Don't %+v the probes: one carries a 1 MiB oversized-string value.)
	if len(probes) != 7 {
		var labels []string
		for _, p := range probes {
			labels = append(labels, p.Label)
		}
		t.Fatalf("got %d probes, want 7: %v", len(probes), labels)
	}

	// The INT64-max probe must carry the overflow-triggering value.
	var sawMaxInt bool
	for _, p := range probes {
		if v, ok := p.Args["limit"]; ok {
			if n, ok := v.(int64); ok && n == maxInt64 {
				sawMaxInt = true
			}
		}
	}
	if !sawMaxInt {
		t.Errorf("expected an INT64-max probe on the integer field")
	}

	// additionalProperties:false must produce an unknown-field probe.
	var sawUnknown bool
	for _, p := range probes {
		if _, ok := p.Args["columbo_unknown_field"]; ok {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Errorf("expected an unknown-field probe for additionalProperties:false")
	}
}

// Generate must be deterministic: map iteration is randomized per process, so
// without sorting the probe order (and thus the audit's F-numbering) would
// reshuffle every run. A real audit tool needs stable, diffable output.
func TestGenerateDeterministic(t *testing.T) {
	tools := []mcp.Tool{{
		Name:       "x",
		Properties: map[string]string{"zeta": "string", "alpha": "integer", "mid": "string", "beta": "array"},
	}}
	a := Generate(tools)
	b := Generate(tools)
	if len(a) != len(b) {
		t.Fatalf("len mismatch %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Label != b[i].Label || a[i].Locus != b[i].Locus {
			t.Fatalf("nondeterministic order at %d: %q vs %q", i, a[i].Label, b[i].Label)
		}
	}
	// And the order is the sorted-by-field order (alpha < beta < mid < zeta).
	if len(a) > 0 && a[0].Locus != "x.alpha" {
		t.Errorf("first probe should target the alphabetically-first field, got %q", a[0].Locus)
	}
}

func TestGenerateNoUnknownProbeWhenAdditionalAllowed(t *testing.T) {
	tools := []mcp.Tool{{
		Name:                 "open",
		Properties:           map[string]string{"path": "string"},
		AdditionalProperties: true,
	}}
	for _, p := range Generate(tools) {
		if _, ok := p.Args["columbo_unknown_field"]; ok {
			t.Errorf("should not probe unknown fields when additionalProperties is allowed")
		}
	}
}
