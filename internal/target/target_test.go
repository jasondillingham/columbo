package target

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTarget(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const validYAML = `
name: demo
repo: ./checkout
baseline:
  build: "go build ./cmd/demo"
  test: "go test ./..."
version:
  expected: "1.2.3"
  command: ["go", "run", "./cmd/demo", "--version"]
  sites:
    - { file: cmd/demo/main.go, symbol: Version }
`

func TestLoadValid(t *testing.T) {
	tg, err := Load(writeTarget(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tg.Name != "demo" {
		t.Errorf("name = %q", tg.Name)
	}
	// repo resolves relative to the yaml's own dir.
	if !filepath.IsAbs(tg.RepoPath()) || filepath.Base(tg.RepoPath()) != "checkout" {
		t.Errorf("RepoPath = %q", tg.RepoPath())
	}
	if got := tg.ExpectedFor(tg.Version.Sites[0]); got != "1.2.3" {
		t.Errorf("ExpectedFor default = %q, want 1.2.3", got)
	}
}

func TestExpectedForSiteOverride(t *testing.T) {
	tg := &Target{Version: Version{Expected: "1.0.0"}}
	site := VersionSite{File: "f.go", Symbol: "S", Expect: "9.9.9"}
	if got := tg.ExpectedFor(site); got != "9.9.9" {
		t.Errorf("site override = %q, want 9.9.9", got)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	cases := map[string]string{
		"missing name": `
repo: ./c
baseline: { build: b, test: t }
version: { expected: "1", command: [x], sites: [{file: f, symbol: S}] }`,
		"missing build": `
name: d
repo: ./c
baseline: { test: t }
version: { expected: "1", command: [x], sites: [{file: f, symbol: S}] }`,
		"no sites": `
name: d
repo: ./c
baseline: { build: b, test: t }
version: { expected: "1", command: [x] }`,
		"site missing symbol": `
name: d
repo: ./c
baseline: { build: b, test: t }
version: { expected: "1", command: [x], sites: [{file: f}] }`,
		"command without expected": `
name: d
repo: ./c
baseline: { build: b, test: t }
version: { command: [x], sites: [{file: f, symbol: S, expect: "1"}] }`,
		"site with no resolvable expected": `
name: d
repo: ./c
baseline: { build: b, test: t }
version: { sites: [{file: f, symbol: S}] }`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTarget(t, body)); err == nil {
				t.Errorf("expected validation error, got nil")
			}
		})
	}
}

// A target with a dynamic version (no expected/command) but a per-site expect
// is valid: this is the bosun shape.
func TestLoadDynamicVersionValid(t *testing.T) {
	body := `
name: bosunlike
repo: ./c
baseline: { build: b, test: t }
version:
  sites:
    - { file: internal/mcp/server.go, symbol: ServerVersion, expect: "0.2.0-alpha" }
`
	if _, err := Load(writeTarget(t, body)); err != nil {
		t.Fatalf("dynamic-version target should be valid: %v", err)
	}
}
