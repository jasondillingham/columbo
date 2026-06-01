package lanewire

import (
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
)

func TestRoundTrip(t *testing.T) {
	in := findings.LaneReport{
		Lane: "L6 protocol", Slug: "protocol",
		Findings: []findings.Finding{
			{Severity: findings.Low, Title: "code:0 on unknown method", Class: "jsonrpc-code-zero", Locus: "unknown method"},
		},
		Reverified: []string{"truncated JSON frame"},
		Skipped:    []string{"nothing"},
	}
	block, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate mixed pod logs: progress noise on either side of the block.
	logs := "booting lane...\nsome stderr noise\n" + block + "\nlane done\n"
	out, err := Extract(logs)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if out.Lane != in.Lane || out.Slug != in.Slug {
		t.Errorf("lane/slug lost: %+v", out)
	}
	if len(out.Findings) != 1 || out.Findings[0].Class != "jsonrpc-code-zero" {
		t.Errorf("findings lost: %+v", out.Findings)
	}
	if len(out.Reverified) != 1 {
		t.Errorf("reverified lost: %v", out.Reverified)
	}
}

func TestExtractMissingBlock(t *testing.T) {
	if _, err := Extract("no block here, just logs"); err == nil {
		t.Error("missing block should error")
	}
	if _, err := Extract(Begin + "\n{bad json"); err == nil {
		t.Error("unterminated block should error")
	}
}
