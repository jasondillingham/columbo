package lanes

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/probes/caps"
	"github.com/jasondillingham/columbo/internal/probes/mcp"
	"github.com/jasondillingham/columbo/internal/target"
)

// l2ProbeTimeout is generous on purpose: the MCP client's idle grace would
// otherwise read a slow tool as a silent drop. Leonard's tools answer in
// sub-millisecond, but a slower target needs headroom (see docs/v0.3-plan.md).
const l2ProbeTimeout = 8 * time.Second

// RunL2 runs the L2 caps lane against the target's mcp-stdio surface. It builds
// the server once to a temp dir, lists its tools, then fires the generic
// cap/schema probe battery, one fresh process per probe.
//
// SKIPs cleanly (one SKIP result) when the target exposes no mcp-stdio surface.
// FINDING when a probe leaks Go internals (LOW, F002/F003 class) or panics the
// server (HIGH, DoS).
func RunL2(t *target.Target) []Result {
	return RunL2Augmented(t, nil)
}

// RunL2Augmented runs L2 and, if extra is non-nil, appends LLM-generated probes
// to the fixed battery (the fixed probes always run). extra receives the
// target's tools and returns additional probes; an empty return is fine (fail
// open — the model is an augmentation).
func RunL2Augmented(t *target.Target, extra func([]mcp.Tool) []caps.Probe) []Result {
	surface, ok := t.MCPStdio()
	if !ok {
		return []Result{res("L2 setup", SKIP, "target exposes no mcp-stdio surface")}
	}

	argv, dir, cleanup, err := buildMCPServer(t, surface)
	if err != nil {
		return []Result{res("L2 setup", FAIL, err.Error())}
	}
	defer cleanup()

	client := mcp.New(argv, dir, l2ProbeTimeout)
	tools, sess, err := client.ListTools()
	if err != nil {
		return []Result{res("L2 tools/list", FAIL, fmt.Sprintf("%v (stderr: %s)", err, sess.Stderr))}
	}
	if len(tools) == 0 {
		return []Result{res("L2 tools/list", FAIL, "server returned no tools")}
	}

	var out []Result
	out = append(out, res("L2 tools/list", PASS, fmt.Sprintf("%d tools", len(tools))))

	probes := caps.Generate(tools)
	if extra != nil {
		probes = append(probes, extra(tools)...)
	}
	for _, p := range probes {
		s, err := client.Call(p.Tool, p.Args)
		if err != nil {
			out = append(out, res(fmt.Sprintf("L2 %s: %s", p.Tool, p.Label), FAIL, err.Error()))
			continue
		}
		out = append(out, classifyL2(surface, p, s))
	}
	return out
}

// classifyL2 maps one probe's session to a verdict. Pure (no I/O): it reads
// only the session's captured stderr/responses, so it is unit-testable with
// synthetic sessions. A server panic is a DoS (HIGH); a Go-internals leak is
// the F002/F003 class (LOW); anything else is a re-verified contract (PASS).
func classifyL2(surface target.Surface, p caps.Probe, s *mcp.Session) Result {
	probe := fmt.Sprintf("L2 %s: %s", p.Tool, p.Label)
	if caps.Panicked(s.Stderr) {
		return Result{probe, FINDING,
			"server panicked: " + tail(s.Stderr, 8),
			&findings.Finding{
				Severity:   findings.High, // a crash is a DoS, not a clean rejection
				Title:      fmt.Sprintf("`%s` panics on %s", p.Tool, p.Label),
				Observed:   tail(s.Stderr, 8),
				Expected:   "a clean JSON-RPC error, not a crash",
				Reproducer: reproLine(surface, p),
				Class:      "server-panic",
				Locus:      p.Locus,
			}}
	}
	msg := s.ResponseText(2)
	if caps.LeaksInternals(msg) {
		return Result{probe, FINDING,
			"leaks Go internals: " + oneLine(msg),
			&findings.Finding{
				Severity:   findings.Low, // F002/F003 class: characterized LOW
				Title:      fmt.Sprintf("`%s` leaks Go internals on %s", p.Tool, p.Label),
				Observed:   oneLine(msg),
				Expected:   "an operator-facing error with no Go-internal type/unmarshal detail",
				Reproducer: reproLine(surface, p),
				Class:      p.Class,
				Locus:      p.Locus,
			}}
	}
	// Clean rejection or clean handling: a re-verified contract.
	return res(probe, PASS, verdictDetail(s, msg))
}

// buildMCPServer compiles the surface's Build package once to a temp dir and
// returns the argv to run it, the working dir (the target repo, which must
// hold any store the server needs), and a cleanup func. Falls back to the
// surface Command when Build is empty.
func buildMCPServer(t *target.Target, s target.Surface) (argv []string, dir string, cleanup func(), err error) {
	dir = t.RepoPath()
	cleanup = func() {}
	if s.Build == "" {
		if len(s.Command) == 0 {
			return nil, "", cleanup, fmt.Errorf("surface %q has neither build nor command", s.Name)
		}
		return s.Command, dir, cleanup, nil
	}
	tmp, err := os.MkdirTemp("", "columbo-mcp-")
	if err != nil {
		return nil, "", cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(tmp) }
	bin := filepath.Join(tmp, "server")
	build := exec.Command("go", "build", "-o", bin, s.Build)
	build.Dir = dir
	if out, berr := build.CombinedOutput(); berr != nil {
		cleanup()
		return nil, "", func() {}, fmt.Errorf("build %s: %v\n%s", s.Build, berr, tail(string(out), 20))
	}
	return []string{bin}, dir, cleanup, nil
}

func reproLine(s target.Surface, p caps.Probe) string {
	cmd := "leonard-mcp"
	if len(s.Command) > 0 {
		cmd = s.Command[0]
	}
	return fmt.Sprintf("%s | call %s %s", cmd, p.Tool, jsonArgs(p.Args))
}

func verdictDetail(s *mcp.Session, msg string) string {
	if s.IsError(2) {
		return "clean error: " + oneLine(msg)
	}
	if msg == "" {
		return "no response (silent)"
	}
	return "handled: " + oneLine(msg)
}

// oneLine flattens whitespace and truncates for a single result/finding line.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// jsonArgs renders probe args compactly, truncating oversized values so a
// 1 MiB string probe does not bloat the reproducer line.
func jsonArgs(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{?}"
	}
	if len(b) > 120 {
		return string(b[:120]) + "…}"
	}
	return string(b)
}
