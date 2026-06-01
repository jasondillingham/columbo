// Package lanewire is the wire format between a lane pod and the orchestrator.
// A lane pod prints its LaneReport as a sentinel-delimited JSON block on
// stdout; the orchestrator reads `kubectl logs` and extracts the block. This
// sidesteps the cluster's missing shared storage — findings travel over logs.
package lanewire

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jasondillingham/columbo/internal/findings"
)

const (
	Begin = "---COLUMBO-FINDINGS-BEGIN---"
	End   = "---COLUMBO-FINDINGS-END---"
)

// Marshal renders a lane report as the sentinel block (begin / JSON / end).
// The pod writes this to stdout; all other output must go to stderr so the
// block is the only thing between the sentinels.
func Marshal(r findings.LaneReport) (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\n%s\n%s", Begin, b, End), nil
}

// Extract pulls the LaneReport out of captured pod logs (stdout+stderr mixed).
// It takes the content between the first Begin and the next End.
func Extract(logs string) (findings.LaneReport, error) {
	var lr findings.LaneReport
	i := strings.Index(logs, Begin)
	if i < 0 {
		return lr, fmt.Errorf("no findings block (%s) in logs", Begin)
	}
	rest := logs[i+len(Begin):]
	j := strings.Index(rest, End)
	if j < 0 {
		return lr, fmt.Errorf("findings block not terminated (%s missing)", End)
	}
	body := strings.TrimSpace(rest[:j])
	if err := json.Unmarshal([]byte(body), &lr); err != nil {
		return lr, fmt.Errorf("parse findings block: %w", err)
	}
	return lr, nil
}
