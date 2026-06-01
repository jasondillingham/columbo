package caps

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jasondillingham/columbo/internal/probes/mcp"
)

// GenFunc runs a text completion (injected; ollama in prod, a fake in tests).
// caps stays free of any model/HTTP dependency.
type GenFunc func(prompt string) (string, error)

// GenerateLLM asks the model for adversarial argument objects per tool and
// turns each into a Probe. Honesty firewall: the model only PROPOSES inputs —
// the deterministic, tested classifier still decides what's a finding, and the
// concrete Args travel with the probe so the reproducer is real (never
// "re-prompt the model"). Class is left EMPTY so structural dedup passes these
// through unmerged (a shared class would over-merge distinct LLM findings);
// embedding dedup merges only the genuinely-same ones by title.
func GenerateLLM(gen GenFunc, tools []mcp.Tool, perTool int) ([]Probe, error) {
	if perTool <= 0 {
		perTool = 5
	}
	var probes []Probe
	for _, t := range tools {
		out, err := gen(llmPrompt(t, perTool))
		if err != nil {
			return probes, fmt.Errorf("generate for %s: %w", t.Name, err)
		}
		args, err := parseArgObjects(out)
		if err != nil {
			// One tool's bad output shouldn't sink the rest; skip it.
			continue
		}
		for i, a := range args {
			probes = append(probes, Probe{
				Tool:  t.Name,
				Label: fmt.Sprintf("llm-generated #%d", i+1),
				Args:  a,
				Locus: t.Name,
				// Class intentionally empty.
			})
		}
	}
	return probes, nil
}

func llmPrompt(t mcp.Tool, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Generate %d adversarial JSON argument objects to stress-test an MCP tool.\n", n)
	fmt.Fprintf(&b, "Tool: %s\nInput fields:\n", t.Name)
	for name, typ := range t.Properties {
		fmt.Fprintf(&b, "- %s (%s)\n", name, typ)
	}
	if len(t.Required) > 0 {
		fmt.Fprintf(&b, "Required: %s\n", strings.Join(t.Required, ", "))
	}
	b.WriteString(`
Each object should try to break a naive server: oversized strings, wrong types,
boundary integers (INT64 max/min, negative, zero), control characters, missing
required fields, or unexpected extra fields.
Output ONLY a JSON array of objects, no prose, no markdown fences.`)
	return b.String()
}

// parseArgObjects extracts a JSON array of objects from the model's output,
// tolerating markdown fences and surrounding prose.
func parseArgObjects(s string) ([]map[string]any, error) {
	s = strings.TrimSpace(s)
	// Strip ```json ... ``` fences if present.
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		s = strings.TrimPrefix(s, "json")
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	// Take from the first '[' to the last ']'.
	lo, hi := strings.Index(s, "["), strings.LastIndex(s, "]")
	if lo < 0 || hi <= lo {
		return nil, fmt.Errorf("no JSON array in model output")
	}
	var objs []map[string]any
	if err := json.Unmarshal([]byte(s[lo:hi+1]), &objs); err != nil {
		return nil, fmt.Errorf("parse arg objects: %w", err)
	}
	if len(objs) == 0 {
		return nil, fmt.Errorf("empty arg array")
	}
	return objs, nil
}
