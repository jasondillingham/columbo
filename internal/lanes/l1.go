// Package lanes holds the audit-lens runners. v0.2 ships L1-static only.
package lanes

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/probes/mcp"
	"github.com/jasondillingham/columbo/internal/target"
)

// Verdict is a probe outcome. SKIP means the probe did not apply to this
// target (logged, never silently dropped — see docs/v0.2-plan.md).
type Verdict string

const (
	PASS    Verdict = "PASS"
	FAIL    Verdict = "FAIL"
	FINDING Verdict = "FINDING"
	SKIP    Verdict = "SKIP"
)

// Result is one probe's outcome. When Verdict is FINDING, Finding carries the
// mechanically-captured record for the audit-format writer (the probe fills
// what it can; the writer assigns ID and the operator triages the rest).
type Result struct {
	Probe   string
	Verdict Verdict
	Detail  string
	Finding *findings.Finding
}

// Report converts a lane's results into a findings.LaneReport: FINDING
// verdicts become findings, PASS verdicts become re-verified contracts. SKIP
// and FAIL are NOT findings about the target, but they are recorded (not
// dropped) so the written round cannot present a lane that did not run as a
// clean pass.
func Report(lane, slug string, results []Result) findings.LaneReport {
	lr := findings.LaneReport{Lane: lane, Slug: slug}
	for _, r := range results {
		switch r.Verdict {
		case FINDING:
			f := findings.Finding{Lane: lane, Title: r.Probe + ": " + r.Detail}
			if r.Finding != nil {
				f = *r.Finding
				f.Lane = lane
			}
			lr.Findings = append(lr.Findings, f)
		case PASS:
			lr.Reverified = append(lr.Reverified, r.Probe)
		case SKIP:
			lr.Skipped = append(lr.Skipped, label(r))
		case FAIL:
			lr.Failed = append(lr.Failed, label(r))
		}
	}
	return lr
}

func label(r Result) string {
	if r.Detail == "" {
		return r.Probe
	}
	return r.Probe + ": " + oneLine(r.Detail)
}

// RunL1 runs the L1-static build-invariants lane against t and returns one
// Result per probe. It does not write audit-format markdown; that is v0.3.
//
// Probes:
//
//	P1 build         — baseline.build exits 0
//	P2 source drift  — each version site carries its expected value
//	P3 binary version— built binary's --version contains version.expected (or SKIP)
//	P4 tests green   — baseline.test exits 0
func RunL1(t *target.Target) []Result {
	repo := t.RepoPath()
	var out []Result

	// P1 — build.
	if _, stderr, err := runShell(repo, t.Baseline.Build); err != nil {
		out = append(out, res("P1 build", FAIL,
			fmt.Sprintf("`%s` failed: %v\n%s", t.Baseline.Build, err, tail(stderr, 20))))
		// Build is the gate for P3. Note it but keep going; P2/P4 don't need it.
	} else {
		out = append(out, res("P1 build", PASS, t.Baseline.Build))
	}

	// P2 — source-side version drift.
	out = append(out, probeSourceDrift(t, repo)...)

	// P3 — built-binary version.
	out = append(out, probeBinaryVersion(t, repo))

	// P4 — baseline tests green.
	if _, stderr, err := runShell(repo, t.Baseline.Test); err != nil {
		out = append(out, res("P4 tests", FAIL,
			fmt.Sprintf("`%s` failed: %v\n%s", t.Baseline.Test, err, tail(stderr, 20))))
	} else {
		out = append(out, res("P4 tests", PASS, t.Baseline.Test))
	}

	// P5 — live serverInfo version drift (the half of seed L1 that needs the
	// MCP client; the static P3 checks the binary, this checks the wire).
	out = append(out, probeServerInfo(t))

	return out
}

// probeServerInfo reads serverInfo.version over the wire and compares it to the
// pinned expected version. SKIPs cleanly when there is no mcp-stdio surface or
// no pinned version. This does NOT cover the ldflags-vs-go-run gap that affects
// P3 (still in docs/v0.2-deferred.md); P5 reads what the running server reports.
func probeServerInfo(t *target.Target) Result {
	const probe = "P5 serverInfo version"
	surface, ok := t.MCPStdio()
	if !ok {
		return res(probe, SKIP, "no mcp-stdio surface")
	}
	if t.Version.Expected == "" {
		return res(probe, SKIP, "no pinned version.expected to compare against")
	}
	argv, dir, cleanup, err := buildMCPServer(t, surface)
	if err != nil {
		return res(probe, FAIL, err.Error())
	}
	defer cleanup()
	name, version, sess, err := mcp.New(argv, dir, l2ProbeTimeout).ServerInfo()
	if err != nil {
		return res(probe, FAIL, fmt.Sprintf("%v (stderr: %s)", err, oneLine(sess.Stderr)))
	}
	return classifyServerInfo(name, version, t.Version.Expected)
}

// classifyServerInfo is the pure verdict for the wire-version check: a FINDING
// (LOW, the F001 wire-drift class) when serverInfo.version does not contain the
// expected version, else a re-verified PASS. Testable without a server.
func classifyServerInfo(name, version, expected string) Result {
	const probe = "P5 serverInfo version"
	if !strings.Contains(version, expected) {
		return Result{probe, FINDING,
			fmt.Sprintf("serverInfo.version %q does not contain expected %q (wire drift)", version, expected),
			&findings.Finding{
				Severity:   findings.Low, // F001 wire-drift class (cf. bosun ServerVersion)
				Title:      fmt.Sprintf("MCP serverInfo.version %q does not match expected %q (wire drift)", version, expected),
				Observed:   fmt.Sprintf("serverInfo: name=%q version=%q", name, version),
				Expected:   fmt.Sprintf("version containing %q", expected),
				Reproducer: "send an initialize handshake, read result.serverInfo.version",
			}}
	}
	return res(probe, PASS, fmt.Sprintf("serverInfo.version = %q", version))
}

// res builds a non-finding Result (PASS/FAIL/SKIP have no Finding payload).
func res(probe string, v Verdict, detail string) Result {
	return Result{Probe: probe, Verdict: v, Detail: detail}
}

// probeSourceDrift reads each version site and checks the value assigned to
// its symbol matches the expected value.
func probeSourceDrift(t *target.Target, repo string) []Result {
	var out []Result
	for _, s := range t.Version.Sites {
		want := t.ExpectedFor(s)
		probe := fmt.Sprintf("P2 source %s:%s", s.File, s.Symbol)
		got, err := readSymbolValue(filepath.Join(repo, s.File), s.Symbol)
		if err != nil {
			out = append(out, res(probe, FAIL, err.Error()))
			continue
		}
		if got != want {
			out = append(out, Result{probe, FINDING,
				fmt.Sprintf("%s = %q, expected %q (version drift)", s.Symbol, got, want),
				&findings.Finding{
					Severity:   findings.Low, // F001 class: characterized LOW in Leonard/bosun
					Title:      fmt.Sprintf("`%s` in %s = %q, expected %q (version drift)", s.Symbol, s.File, got, want),
					Files:      []string{s.File},
					Observed:   fmt.Sprintf("%s = %q", s.Symbol, got),
					Expected:   fmt.Sprintf("%s should equal %q", s.Symbol, want),
					Reproducer: fmt.Sprintf("grep -n %q %s", s.Symbol, s.File),
				}})
			continue
		}
		out = append(out, res(probe, PASS, fmt.Sprintf("%s = %q", s.Symbol, got)))
	}
	return out
}

// probeBinaryVersion runs the built binary's version command and checks its
// output contains the expected version. SKIPs when the target has no pinned
// version to assert (e.g. git-describe builds).
func probeBinaryVersion(t *target.Target, repo string) Result {
	const probe = "P3 binary version"
	if len(t.Version.Command) == 0 || t.Version.Expected == "" {
		return res(probe, SKIP, "no pinned version.command/expected (dynamic version)")
	}
	stdout, stderr, err := runArgv(repo, t.Version.Command)
	if err != nil {
		return res(probe, FAIL,
			fmt.Sprintf("`%s` failed: %v\n%s", strings.Join(t.Version.Command, " "), err, tail(stderr, 10)))
	}
	if !strings.Contains(stdout, t.Version.Expected) {
		got := strings.TrimSpace(stdout)
		return Result{probe, FINDING,
			fmt.Sprintf("output %q does not contain expected %q (version drift)", got, t.Version.Expected),
			&findings.Finding{
				Severity:   findings.Low,
				Title:      fmt.Sprintf("built binary version %q does not match expected %q (drift)", got, t.Version.Expected),
				Observed:   got,
				Expected:   fmt.Sprintf("output containing %q", t.Version.Expected),
				Reproducer: strings.Join(t.Version.Command, " "),
			}}
	}
	return res(probe, PASS, fmt.Sprintf("output contains %q", t.Version.Expected))
}

// Tally counts results by verdict.
func Tally(rs []Result) map[Verdict]int {
	m := map[Verdict]int{}
	for _, r := range rs {
		m[r.Verdict]++
	}
	return m
}

// readSymbolValue returns the string literal bound to symbol in a Go source
// file. It parses the file with go/ast and reads the const/var declaration's
// value, so a match inside a COMMENT or an unrelated string literal cannot
// shadow the real declaration (bughunt-3 F002 — a regex first-match took the
// commented value). Handles grouped const/var blocks; errors if symbol is
// absent or not bound to a string literal.
func readSymbolValue(path, symbol string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, data, 0)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != symbol || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return "", fmt.Errorf("symbol %q in %s is not a string literal", symbol, path)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					return "", fmt.Errorf("symbol %q in %s: %w", symbol, path, err)
				}
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("symbol %q not found (or not a string literal) in %s", symbol, path)
}

// runShell runs a command string via `sh -c` with cwd=dir. The command is
// operator-supplied (target.yaml is trusted input).
func runShell(dir, command string) (stdout, stderr string, err error) {
	cmd := exec.Command("sh", "-c", command)
	return runCmd(dir, cmd)
}

// runArgv runs an explicit argv with cwd=dir.
func runArgv(dir string, argv []string) (stdout, stderr string, err error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	return runCmd(dir, cmd)
}

func runCmd(dir string, cmd *exec.Cmd) (string, string, error) {
	cmd.Dir = dir
	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se
	err := cmd.Run()
	return so.String(), se.String(), err
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
