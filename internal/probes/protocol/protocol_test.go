package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProbes(t *testing.T) {
	ps := Probes()
	if len(ps) != 6 {
		t.Fatalf("got %d probes, want 6", len(ps))
	}
	var sawBOM, sawNonzero bool
	for _, p := range ps {
		if strings.HasPrefix(p.Frame, "\ufeff") {
			sawBOM = true
		}
		if p.Expect == NonzeroCode && p.ID == 0 {
			t.Errorf("NonzeroCode probe %q must set an ID to check", p.Label)
		}
		// Frames that aren't deliberately malformed should still be JSON.
		if p.Expect == NonzeroCode {
			if !json.Valid([]byte(p.Frame)) {
				t.Errorf("NonzeroCode probe %q frame should be valid JSON: %s", p.Label, p.Frame)
			}
			sawNonzero = true
		}
	}
	if !sawBOM {
		t.Error("expected a BOM-prefixed probe")
	}
	if !sawNonzero {
		t.Error("expected at least one NonzeroCode probe")
	}
}
