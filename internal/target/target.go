// Package target loads and validates target.yaml, the operator-supplied
// description of the thing Columbo audits.
//
// The schema models all four parts of DESIGN.md's Target abstraction
// (surface, baseline, threat model, history), but v0.2 only *consumes* the
// subset L1-static needs: Name, Repo, Baseline.{Build,Test}, and Version.
// The rest is parsed and shape-validated, then ignored. See
// docs/v0.2-plan.md for the "fields yes, consumers no" line.
package target

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Target is a loaded target.yaml.
type Target struct {
	Name        string    `yaml:"name"`
	Repo        string    `yaml:"repo"`
	Module      string    `yaml:"module"` // parse-only in v0.2
	Baseline    Baseline  `yaml:"baseline"`
	Version     Version   `yaml:"version"`
	Surfaces    []Surface `yaml:"surfaces"`
	ThreatModel string    `yaml:"threat_model"` // parse-only in v0.2
	History     []Round   `yaml:"history"`      // parse-only in v0.2
	// Clone, when set, tells a cluster lane pod how to fetch + prepare the
	// target from scratch (it has no local checkout). Ignored by local runs,
	// which use Repo. See internal/k3srunner + the lane entrypoint's --workdir.
	Clone *Clone `yaml:"clone"`

	// dir is the directory of the loaded yaml file, used to resolve a
	// relative Repo. Not a yaml field.
	dir string
}

// Baseline is the target's state at audit time.
type Baseline struct {
	SHA   string `yaml:"sha"` // parse-only in v0.2
	Build string `yaml:"build"`
	Test  string `yaml:"test"`
}

// Version describes where version strings live and what they should be.
type Version struct {
	// Expected is the canonical pinned version, if any. Empty when the build
	// version is dynamic (git-describe), in which case the binary check SKIPs.
	Expected string `yaml:"expected"`
	// Command is an invocation whose stdout must contain Expected, run with
	// cwd = the target repo. Prefer `go run ./cmd/foo --version` so it builds
	// from HEAD and leaves no artifact. Empty disables the check.
	Command []string `yaml:"command"`
	// Sites are source files that hardcode a version literal. L1 checks each
	// agrees with its expected value.
	Sites []VersionSite `yaml:"sites"`
}

// VersionSite is one source location that hardcodes a version.
type VersionSite struct {
	File   string `yaml:"file"`
	Symbol string `yaml:"symbol"`
	// Expect overrides Version.Expected for this site (e.g. bosun's
	// ServerVersion is a different value from the build version). Empty means
	// "use Version.Expected".
	Expect string `yaml:"expect"`
}

// Surface is one interface the target exposes.
type Surface struct {
	Kind    string   `yaml:"kind"`
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
	// Build, when set, is the Go package the MCP/CLI lanes compile once to a
	// temp dir (e.g. "./cmd/leonard-mcp"), then run per probe — fresh from
	// HEAD, no stale binary, no per-probe `go run` compile cost. When empty,
	// lanes fall back to Command (with the staleness caveat).
	Build string `yaml:"build"`
}

// MCPStdio returns the first mcp-stdio surface, or false if the target exposes
// none (the MCP lanes SKIP in that case).
func (t *Target) MCPStdio() (Surface, bool) {
	for _, s := range t.Surfaces {
		if s.Kind == "mcp-stdio" {
			return s, true
		}
	}
	return Surface{}, false
}

// Clone tells a cluster pod how to obtain + prepare the target. setup runs
// after clone+checkout (e.g. build the binaries, create the tool's store) so
// the surface is ready to probe.
type Clone struct {
	URL   string   `yaml:"url"`
	Setup []string `yaml:"setup"`
}

// Round is one prior audit round. Parse-only in v0.2.
type Round struct {
	Round    string `yaml:"round"`
	Lens     string `yaml:"lens"`
	Findings int    `yaml:"findings"`
}

// Load reads and validates a target.yaml at path.
func Load(path string) (*Target, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read target: %w", err)
	}

	var t Target
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse target %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve target path: %w", err)
	}
	t.dir = filepath.Dir(abs)

	if err := t.validate(); err != nil {
		return nil, fmt.Errorf("invalid target %s: %w", path, err)
	}
	return &t, nil
}

// validate checks the fields v0.2 relies on. Parse-only fields are not
// required; unknown fields are tolerated.
func (t *Target) validate() error {
	if t.Name == "" {
		return fmt.Errorf("name is required")
	}
	if t.Repo == "" {
		return fmt.Errorf("repo is required")
	}
	if t.Baseline.Build == "" {
		return fmt.Errorf("baseline.build is required")
	}
	if t.Baseline.Test == "" {
		return fmt.Errorf("baseline.test is required")
	}
	if len(t.Version.Sites) == 0 {
		return fmt.Errorf("version.sites must list at least one source site")
	}
	for i, s := range t.Version.Sites {
		if s.File == "" || s.Symbol == "" {
			return fmt.Errorf("version.sites[%d]: file and symbol are required", i)
		}
		if t.ExpectedFor(s) == "" {
			return fmt.Errorf("version.sites[%d] (%s): no expected value (set site `expect` or top-level version.expected)", i, s.File)
		}
	}
	// A binary version check needs both halves or neither.
	if (len(t.Version.Command) == 0) != (t.Version.Expected == "") {
		return fmt.Errorf("version.command and version.expected must be set together (got command=%v, expected=%q)", t.Version.Command, t.Version.Expected)
	}
	return nil
}

// RepoPath returns the absolute path to the target repo, resolving a relative
// Repo against the target.yaml's own directory.
func (t *Target) RepoPath() string {
	if filepath.IsAbs(t.Repo) {
		return t.Repo
	}
	return filepath.Join(t.dir, t.Repo)
}

// ExpectedFor returns the version a site should carry: its own Expect, or the
// target-wide Version.Expected as the default.
func (t *Target) ExpectedFor(s VersionSite) string {
	if s.Expect != "" {
		return s.Expect
	}
	return t.Version.Expected
}
