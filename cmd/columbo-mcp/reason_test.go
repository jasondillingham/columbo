package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jasondillingham/columbo/internal/mcpserver"
	"github.com/jasondillingham/columbo/internal/reason"
)

// End-to-end through the MCP surface: start -> record -> reproduce -> finalize,
// with Columbo's reproducer execution as the judge. The reason tools are
// STATEFUL — they rely on the persistent MCP connection a Claude Code session
// holds (ONE server process across all calls). So this drives a single Serve
// with the whole frame sequence (the one-shot client spawns per call and would
// lose the round — which is correct for probes, wrong for this surface).
func TestReasonHarnessStatefulFlow(t *testing.T) {
	repo := bugRepo(t) // git repo whose pkg.Sum(a,b) ignores b

	srv := mcpserver.New("columbo-mcp", "test")
	registerReasonTools(srv, reason.NewSession())

	frames := []string{
		req(1, "initialize", map[string]any{}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		req(2, "tools/call", call("reason_start", map[string]any{"dir": repo})),
		req(3, "tools/call", call("reason_record", map[string]any{
			"title": "Sum ignores second argument", "severity": "HIGH", "files": []string{"pkg/bug.go"},
			"repro_pkgdir": "pkg", "repro_run": "TestSumBug",
			"repro_file": "package pkg\nimport \"testing\"\nfunc TestSumBug(t *testing.T){ if Sum(2,3) != 2 { t.Fatal(\"bug absent\") } }\n",
		})),
		req(4, "tools/call", call("reason_reproduce", map[string]any{"id": 1})),
		req(5, "tools/call", call("reason_finalize", map[string]any{})),
	}

	var out strings.Builder
	if err := srv.Serve(strings.NewReader(strings.Join(frames, "\n")+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	resp := parseResults(t, out.String())

	if isErr(resp[3]) {
		t.Fatalf("reason_record errored: %s", text(resp[3]))
	}
	if got := result(t, resp[4])["status"]; got != "confirmed" {
		t.Fatalf("reproducer should CONFIRM the real bug, got %v\n%s", got, text(resp[4]))
	}
	if got := result(t, resp[5])["findings"]; got != float64(1) {
		t.Errorf("finalize should carry 1 finding, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "audits", "bughunt-1-findings.md")); err != nil {
		t.Errorf("finalize should have written the round: %v", err)
	}
}

// Slice 2 through the MCP surface: reason_start with a `target` runs the
// deterministic lanes (L1/L2/L6) and folds them into the round. This is the
// path the unit tests can't reach — they inject lane reports by hand; here the
// handler actually loads the target.yaml and runs the lanes. A clean target
// (build/test exit 0, version in sync, no MCP surface) yields zero findings but
// three lane reports, proving the chain (target.Load -> RunL1/L2/L6 -> Report ->
// SetLaneFindings -> Finalize) is wired, not just the helpers.
func TestReasonStartWithTargetFoldsLanes(t *testing.T) {
	dir := cleanTarget(t)

	srv := mcpserver.New("columbo-mcp", "test")
	registerReasonTools(srv, reason.NewSession())

	frames := []string{
		req(1, "initialize", map[string]any{}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		req(2, "tools/call", call("reason_start", map[string]any{
			"dir": dir, "target": filepath.Join(dir, "target.yaml"),
		})),
		req(3, "tools/call", call("reason_finalize", map[string]any{})),
	}

	var out strings.Builder
	if err := srv.Serve(strings.NewReader(strings.Join(frames, "\n")+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	resp := parseResults(t, out.String())

	if isErr(resp[2]) {
		t.Fatalf("reason_start errored: %s", text(resp[2]))
	}
	start := result(t, resp[2])
	if _, ok := start["lanes_error"]; ok {
		t.Fatalf("lanes should have run, got lanes_error: %v", start["lanes_error"])
	}
	lanes, ok := start["deterministic_lanes"].(map[string]any)
	if !ok {
		t.Fatalf("reason_start should report deterministic_lanes, got: %v", start)
	}
	for _, want := range []string{"L1 build invariants", "L2 caps", "L6 protocol"} {
		if _, ok := lanes[want]; !ok {
			t.Errorf("deterministic_lanes missing %q (got %v)", want, lanes)
		}
	}

	// Finalize: no candidates, but three lane reports must still land as a round.
	if isErr(resp[3]) {
		t.Fatalf("reason_finalize errored: %s", text(resp[3]))
	}
	fin := result(t, resp[3])
	if got := fin["lanes"]; got != float64(3) {
		t.Errorf("round should carry 3 lane reports (L1/L2/L6, no reason candidates), got %v", got)
	}
	// The per-lane files prove the lanes were written, not just counted.
	for _, slug := range []string{"build-invariants", "caps", "protocol"} {
		if _, err := os.Stat(filepath.Join(dir, "audits", "bughunt-1-"+slug+".md")); err != nil {
			t.Errorf("finalize should have written the %s lane file: %v", slug, err)
		}
	}
}

// cleanTarget builds a tiny repo + target.yaml that the deterministic lanes pass
// cleanly: build/test are `true`, the version site is in sync, and there is no
// MCP surface (so L2/L6 SKIP and L1's wire probes SKIP). Zero findings, three
// lanes.
func cleanTarget(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module cleanfix\n\ngo 1.21\n")
	write(t, filepath.Join(dir, "version.go"), "package cleanfix\n\nconst Version = \"1.0.0\"\n")
	write(t, filepath.Join(dir, "target.yaml"), strings.Join([]string{
		"name: cleanfix",
		"repo: .",
		"baseline:",
		"  build: \"true\"",
		"  test: \"true\"",
		"version:",
		"  sites:",
		"    - file: version.go",
		"      symbol: Version",
		"      expect: \"1.0.0\"",
		"",
	}, "\n"))
	return dir
}

// --- frame helpers ---

func req(id int, method string, params any) string {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return string(b)
}
func call(name string, args map[string]any) map[string]any {
	return map[string]any{"name": name, "arguments": args}
}
func parseResults(t *testing.T, logs string) map[int]map[string]any {
	t.Helper()
	out := map[int]map[string]any{}
	sc := bufio.NewScanner(strings.NewReader(logs))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if idf, ok := m["id"].(float64); ok {
			out[int(idf)] = m
		}
	}
	return out
}
func isErr(frame map[string]any) bool { _, ok := frame["error"]; return ok }
func text(frame map[string]any) string {
	if e, ok := frame["error"].(map[string]any); ok {
		return e["message"].(string)
	}
	res, _ := frame["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) > 0 {
		if cm, ok := content[0].(map[string]any); ok {
			return cm["text"].(string)
		}
	}
	return ""
}

// result parses the tool result's text payload (the handler's JSON return).
func result(t *testing.T, frame map[string]any) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text(frame)), &m); err != nil {
		t.Fatalf("tool result not JSON: %q", text(frame))
	}
	return m
}

func bugRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "go.mod"), "module testbug\n\ngo 1.21\n")
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	write(t, filepath.Join(dir, "pkg", "bug.go"), "package pkg\n\nfunc Sum(a, b int) int { return a }\n")
	for _, a := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}, {"add", "-A"}, {"commit", "-qm", "x"}} {
		cmd := exec.Command("git", a...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	return dir
}

func write(t *testing.T, p, b string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
}
