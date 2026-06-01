package caps

import (
	"strings"
	"testing"

	"github.com/jasondillingham/columbo/internal/probes/mcp"
)

func TestGenerateLLMParsesAndFirewall(t *testing.T) {
	tools := []mcp.Tool{{Name: "find_symbol", Properties: map[string]string{"query": "string", "limit": "integer"}, Required: []string{"query"}}}
	// Fake model: returns a markdown-fenced JSON array (the common real shape).
	gen := func(prompt string) (string, error) {
		if !strings.Contains(prompt, "find_symbol") {
			t.Errorf("prompt should describe the tool")
		}
		return "here you go:\n```json\n[{\"query\": null}, {\"limit\": 9223372036854775807}]\n```\n", nil
	}
	probes, err := GenerateLLM(gen, tools, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 2 {
		t.Fatalf("want 2 probes, got %d", len(probes))
	}
	for _, p := range probes {
		// Honesty firewall: concrete args travel with the probe (real reproducer),
		// and Class is empty so structural dedup won't over-merge distinct findings.
		if p.Args == nil {
			t.Error("probe must carry concrete generated args")
		}
		if p.Class != "" {
			t.Errorf("LLM probe Class must be empty (avoid over-merge), got %q", p.Class)
		}
		if p.Tool != "find_symbol" {
			t.Errorf("tool = %q", p.Tool)
		}
	}
}

func TestGenerateLLMBadOutputSkipsTool(t *testing.T) {
	tools := []mcp.Tool{{Name: "x", Properties: map[string]string{"a": "string"}}}
	gen := func(string) (string, error) { return "I cannot do that.", nil } // no JSON
	probes, err := GenerateLLM(gen, tools, 2)
	if err != nil {
		t.Fatalf("a tool with unparseable output should be skipped, not error: %v", err)
	}
	if len(probes) != 0 {
		t.Errorf("want 0 probes from unparseable output, got %d", len(probes))
	}
}

func TestParseArgObjectsFences(t *testing.T) {
	cases := []string{
		`[{"a":1}]`,
		"```json\n[{\"a\":1}]\n```",
		"sure!\n[{\"a\":1}]\nhope that helps",
	}
	for _, c := range cases {
		got, err := parseArgObjects(c)
		if err != nil || len(got) != 1 {
			t.Errorf("parse %q -> %v, %v", c, got, err)
		}
	}
	if _, err := parseArgObjects("no array here"); err == nil {
		t.Error("missing array should error")
	}
}
