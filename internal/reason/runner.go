package reason

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const reproTestFile = "columbo_repro_test.go"

// runReproducer runs r against an ISOLATED copy of the target at dir, never the
// live tree (a reproducer that mutates state must not corrupt the code being
// audited), and as a bounded subprocess (a hanging reproducer must not wedge
// the harness). Returns the `go test` output, whether it passed (exit 0 = bug
// demonstrated), and a non-nil error only on a HARNESS failure (could not
// isolate / set up) — distinct from a test verdict.
func runReproducer(dir string, r Reproducer, timeout time.Duration) (out string, passed bool, err error) {
	if r.PkgDir == "" || r.Run == "" || r.File == "" {
		return "", false, fmt.Errorf("reproducer needs PkgDir, Run, and File")
	}
	if strings.Contains(r.PkgDir, "..") || filepath.IsAbs(r.PkgDir) {
		return "", false, fmt.Errorf("reproducer PkgDir must be a relative path inside the target")
	}

	iso, cleanup, err := isolate(dir)
	if err != nil {
		return "", false, fmt.Errorf("isolate target: %w", err)
	}
	defer cleanup()

	pkgAbs := filepath.Join(iso, r.PkgDir)
	if fi, e := os.Stat(pkgAbs); e != nil || !fi.IsDir() {
		return "", false, fmt.Errorf("reproducer PkgDir %q not found in target", r.PkgDir)
	}
	testPath := filepath.Join(pkgAbs, reproTestFile)
	if err := os.WriteFile(testPath, []byte(r.File), 0o644); err != nil {
		return "", false, fmt.Errorf("write reproducer: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "test", "./"+filepath.ToSlash(r.PkgDir), "-run", r.Run, "-count=1")
	cmd.Dir = iso
	b, runErr := cmd.CombinedOutput()
	out = string(b)
	if ctx.Err() == context.DeadlineExceeded {
		// A hung reproducer is not a confirmation; report it, don't wedge.
		return out + "\n[reproducer timed out]", false, nil
	}
	// exit 0 (runErr == nil) => the test passed => the bug is present.
	return out, runErr == nil, nil
}

// isolate produces a throwaway copy of the target for running a reproducer.
// Prefers a detached git worktree (cheap, shares .git, no mutation to the live
// tree); falls back to cp -r for a non-git target. Runs against HEAD, not the
// dirty working tree.
func isolate(dir string) (path string, cleanup func(), err error) {
	tmp, err := os.MkdirTemp("", "columbo-repro-")
	if err != nil {
		return "", nil, err
	}
	noop := func() { _ = os.RemoveAll(tmp) }

	if isGitRepo(dir) {
		wt := filepath.Join(tmp, "wt")
		if out, e := git(dir, "worktree", "add", "--detach", "--quiet", wt, "HEAD"); e != nil {
			noop()
			return "", nil, fmt.Errorf("git worktree add: %v (%s)", e, out)
		}
		cleanup = func() {
			_, _ = git(dir, "worktree", "remove", "--force", wt)
			_ = os.RemoveAll(tmp)
		}
		return wt, cleanup, nil
	}

	// Non-git: copy the tree.
	dst := filepath.Join(tmp, "copy")
	if out, e := exec.Command("cp", "-R", dir, dst).CombinedOutput(); e != nil {
		noop()
		return "", nil, fmt.Errorf("cp -R: %v (%s)", e, out)
	}
	return dst, noop, nil
}

func isGitRepo(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, err := cmd.CombinedOutput()
	return string(b), err
}
