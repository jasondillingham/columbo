package autonomous

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// git runs a git command in repoDir.
func git(repoDir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// CleanTree reports whether the target repo has no uncommitted changes.
// Committing into an audit branch from a dirty tree would sweep the operator's
// WIP into the audit, so a dirty tree is a blocker.
func CleanTree(repoDir string) (bool, error) {
	out, err := git(repoDir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %w (%s)", err, out)
	}
	return out == "", nil
}

var branchRoundRe = regexp.MustCompile(`audit/bughunt-(\d+)$`)

// NextRound returns the next free round number, considering BOTH the round
// files in the working tree (worktreeRounds, e.g. from query.Rounds) AND any
// existing audit/bughunt-* branches. The branch check is essential: promote
// commits a round only on its branch, so the working tree never shows it; a
// worktree-only computation would return the same N on the next run and collide
// when creating the branch.
func NextRound(repoDir string, worktreeRounds []int) (int, error) {
	max := 0
	for _, n := range worktreeRounds {
		if n > max {
			max = n
		}
	}
	out, err := git(repoDir, "for-each-ref", "--format=%(refname:short)", "refs/heads/audit/")
	if err != nil {
		return 0, fmt.Errorf("git for-each-ref: %w (%s)", err, out)
	}
	for _, ref := range strings.Split(out, "\n") {
		if m := branchRoundRe.FindStringSubmatch(strings.TrimSpace(ref)); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > max {
				max = n
			}
		}
	}
	return max + 1, nil
}

// Promote creates audit/bughunt-N in repoDir, invokes write (which must write
// the round's files under repoDir, e.g. into audits/), commits exactly those
// files, and returns the tree to the original branch. write runs AFTER the
// branch checkout so the files never land on the original branch (the
// clean-tree guarantee). Returns the audit branch name.
func Promote(repoDir string, round int, write func() ([]string, error), commitMsg string) (string, error) {
	clean, err := CleanTree(repoDir)
	if err != nil {
		return "", err
	}
	if !clean {
		return "", fmt.Errorf("target working tree is dirty; commit or stash before an autonomous round")
	}

	orig, err := git(repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git current branch: %w (%s)", err, orig)
	}

	branch := fmt.Sprintf("audit/bughunt-%d", round)
	if out, err := git(repoDir, "checkout", "-b", branch); err != nil {
		return "", fmt.Errorf("git checkout -b %s: %w (%s)", branch, err, out)
	}

	// From here, return to the original branch on any failure.
	restore := func(cause error) (string, error) {
		_, _ = git(repoDir, "checkout", "--force", orig)
		_, _ = git(repoDir, "branch", "-D", branch)
		return "", cause
	}

	files, err := write()
	if err != nil {
		return restore(fmt.Errorf("write round: %w", err))
	}
	addArgs := append([]string{"add", "--"}, files...)
	if out, err := git(repoDir, addArgs...); err != nil {
		return restore(fmt.Errorf("git add: %w (%s)", err, out))
	}
	if out, err := git(repoDir, "commit", "-m", commitMsg); err != nil {
		return restore(fmt.Errorf("git commit: %w (%s)", err, out))
	}
	if out, err := git(repoDir, "checkout", orig); err != nil {
		return "", fmt.Errorf("committed %s but failed to return to %s: %w (%s)", branch, orig, err, out)
	}
	return branch, nil
}
