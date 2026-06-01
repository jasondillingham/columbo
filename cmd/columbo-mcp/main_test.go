package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/probes/mcp"
)

// The full loop: Columbo's own MCP client (internal/probes/mcp) drives
// Columbo's MCP server (columbo-mcp) end-to-end. Builds the server from source
// (portable, no external dependency), points it at a fixture audits dir written
// by the real findings writer, and queries it over stdio.
func TestColumboMCPOverOwnClient(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "columbo-mcp")
	build := exec.Command("go", "build", "-o", bin, "github.com/jasondillingham/columbo/cmd/columbo-mcp")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build columbo-mcp: %v\n%s", err, out)
	}

	auditsDir := t.TempDir()
	r := &findings.Round{
		Target: "demo", N: 1, Date: "2026-06-01", RawFindings: 5,
		Lanes: []findings.LaneReport{{
			Lane: "L2 caps", Slug: "caps",
			Findings: []findings.Finding{{Severity: findings.Low, Title: "leak on null query"}},
		}},
	}
	if _, err := r.WriteRound(auditsDir, false); err != nil {
		t.Fatal(err)
	}

	client := mcp.New([]string{bin, "--audits", auditsDir}, "", 10*time.Second)

	names, sess, err := client.ListTools()
	if err != nil {
		t.Fatalf("ListTools: %v (stderr: %s)", err, sess.Stderr)
	}
	got := map[string]bool{}
	for _, t := range names {
		got[t.Name] = true
	}
	if !got["audit_status"] || !got["audit_findings"] {
		t.Fatalf("server should advertise audit_status + audit_findings, got %v", names)
	}

	// audit_findings should return the round's rollup, including F001.
	fs, err := client.Call("audit_findings", map[string]any{"round": 0})
	if err != nil {
		t.Fatal(err)
	}
	txt := fs.ResponseText(2)
	if !strings.Contains(txt, "F001") || !strings.Contains(txt, "leak on null query") {
		t.Errorf("audit_findings missing the finding: %s", txt)
	}

	// audit_status should report the reconciled summary.
	st, err := client.Call("audit_status", nil)
	if err != nil {
		t.Fatal(err)
	}
	stxt := st.ResponseText(2)
	if !strings.Contains(stxt, `"total": 1`) || !strings.Contains(stxt, `"reconciled": true`) {
		t.Errorf("audit_status summary unexpected: %s", stxt)
	}

	// Wrong-TYPE round (string, not integer) against a POPULATED dir must error
	// cleanly — NOT silently return the latest round (silent-accept), and NOT
	// leak the Go json.Unmarshal error (the F002/F003 class). This is the exact
	// bug class Columbo hunts; its own server must not have it.
	bad, err := client.Call("audit_findings", map[string]any{"round": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !bad.IsError(2) {
		t.Errorf("string round must be an error, not a silent success: %s", bad.ResponseText(2))
	}
	btxt := bad.ResponseText(2)
	if strings.Contains(btxt, "F001") {
		t.Errorf("string round must NOT silently return the round's findings: %s", btxt)
	}
	if strings.Contains(btxt, "json:") || strings.Contains(btxt, "Go struct field") {
		t.Errorf("error must not leak Go internals: %s", btxt)
	}
}
