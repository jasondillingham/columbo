package lanes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/target"
)

func TestReadSymbolValue(t *testing.T) {
	dir := t.TempDir()
	src := `package main

const Version = "0.54.0"

const (
	Other  = "x"
	Server = "0.2.0-alpha"
)

var notAString = 42
`
	p := filepath.Join(dir, "v.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		symbol  string
		want    string
		wantErr bool
	}{
		"plain const":   {"Version", "0.54.0", false},
		"grouped const": {"Server", "0.2.0-alpha", false},
		"absent symbol": {"Nope", "", true},
		"non-string":    {"notAString", "", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := readSymbolValue(p, c.symbol)
			if c.wantErr {
				if err == nil {
					t.Errorf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Regression for bughunt-3 F002: a comment mentioning the symbol before the
// real declaration must not shadow it (the old regex took the first textual
// match). The AST reader reads the declared value, ignoring comments.
func TestReadSymbolValueIgnoresComment(t *testing.T) {
	dir := t.TempDir()
	src := "package p\n\n" +
		"// historical note: Version = \"0.0.1\" in the old scheme\n" +
		"const Version = \"1.2.3\"\n"
	p := filepath.Join(dir, "v.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readSymbolValue(p, "Version")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("got %q, want %q (a comment must not shadow the const)", got, "1.2.3")
	}
}

// The drift FINDING is the outcome the whole lane exists to produce, so it
// gets an explicit assertion: a site whose source value does not match its
// expected value must yield FINDING (not PASS, the dangerous false negative).
func TestProbeSourceDriftFinding(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "cmd", "v.go"),
		[]byte("package main\nconst Version = \"1.0.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tg := &target.Target{
		Repo: repo,
		Version: target.Version{
			Expected: "2.0.0", // source says 1.0.0 -> drift
			Sites:    []target.VersionSite{{File: "cmd/v.go", Symbol: "Version"}},
		},
	}
	// Target.dir is unexported; RepoPath returns Repo verbatim when absolute,
	// which it is here, so probeSourceDrift reads the file correctly.
	results := probeSourceDrift(tg, tg.RepoPath())
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Verdict != FINDING {
		t.Errorf("drift verdict = %s, want FINDING (detail: %s)", results[0].Verdict, results[0].Detail)
	}

	// Control: matching value must PASS, so the test above isn't trivially green.
	tg.Version.Expected = "1.0.0"
	if got := probeSourceDrift(tg, tg.RepoPath())[0].Verdict; got != PASS {
		t.Errorf("matching verdict = %s, want PASS", got)
	}
}

// P5's wire-drift verdict: a serverInfo.version that doesn't contain the
// expected version is a LOW finding; a match is a PASS. (The SKIP/FAIL paths
// are I/O-gated in probeServerInfo and exercised by the real run.)
func TestClassifyServerInfo(t *testing.T) {
	if r := classifyServerInfo("leonard-mcp", "0.52.0", "0.54.0"); r.Verdict != FINDING ||
		r.Finding == nil || r.Finding.Severity != findings.Low {
		t.Errorf("drift should be a LOW finding, got %s / %v", r.Verdict, r.Finding)
	}
	if r := classifyServerInfo("leonard-mcp", "leonard 0.54.0", "0.54.0"); r.Verdict != PASS {
		t.Errorf("matching version should PASS, got %s", r.Verdict)
	}
}

func TestTally(t *testing.T) {
	rs := []Result{
		res("a", PASS, ""), res("b", PASS, ""), res("c", FINDING, ""), res("d", SKIP, ""),
	}
	m := Tally(rs)
	if m[PASS] != 2 || m[FINDING] != 1 || m[SKIP] != 1 || m[FAIL] != 0 {
		t.Errorf("tally = %v", m)
	}
}
