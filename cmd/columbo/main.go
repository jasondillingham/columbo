// Columbo. Adversarial auditor for code that's already shipped.
//
// "Just one more thing..."
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/columbo/internal/autonomous"
	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/k3srunner"
	"github.com/jasondillingham/columbo/internal/lanes"
	"github.com/jasondillingham/columbo/internal/lanewire"
	"github.com/jasondillingham/columbo/internal/probes/caps"
	"github.com/jasondillingham/columbo/internal/probes/mcp"
	"github.com/jasondillingham/columbo/internal/ollama"
	"github.com/jasondillingham/columbo/internal/orchestrator"
	"github.com/jasondillingham/columbo/internal/query"
	"github.com/jasondillingham/columbo/internal/reconcile"
	"github.com/jasondillingham/columbo/internal/target"
	"github.com/jasondillingham/columbo/internal/version"
)

func main() {
	root := &cobra.Command{
		Use:   "columbo",
		Short: "Adversarial auditor for code that's already shipped",
		Long: `Columbo runs structured red-team audits against a target tool.

Walks in rumpled, asks dumb-seeming questions, finds the inconsistency
nobody else noticed, hands you back a fix shape.

Pre-alpha. See DESIGN.md for the shape; subcommands land as the
internal packages get filled in.`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("columbo version %s\n", version.Version)
		},
	})

	targetCmd := &cobra.Command{
		Use:   "target",
		Short: "Work with target.yaml files",
	}
	targetCmd.AddCommand(&cobra.Command{
		Use:   "validate <target.yaml>",
		Short: "Load a target.yaml and report whether it is valid",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := target.Load(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("OK  %s  (repo: %s)\n", t.Name, t.RepoPath())
			return nil
		},
	})
	root.AddCommand(targetCmd)

	auditCmd := &cobra.Command{
		Use:   "audit",
		Short: "Run an audit lane against a target",
	}
	var (
		l1Write bool
		l1Round int
		l1Out   string
		l1Force bool
	)
	l1Cmd := &cobra.Command{
		Use:   "l1 <target.yaml>",
		Short: "Run the L1-static build-invariants lane",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := target.Load(args[0])
			if err != nil {
				return err
			}
			results := lanes.RunL1(t)
			printResults(results)
			tally := lanes.Tally(results)
			fmt.Printf("\nL1 %s: %d PASS  %d FINDING  %d FAIL  %d SKIP\n",
				t.Name, tally[lanes.PASS], tally[lanes.FINDING], tally[lanes.FAIL], tally[lanes.SKIP])

			if l1Write {
				report := lanes.Report("L1 build invariants", "build-invariants", results)
				if err := writeRound(t, l1Round, l1Out, l1Force, 0, report); err != nil {
					return err
				}
			}

			// Findings and failures are reportable, not a crash. Exit
			// non-zero so scripts/CI can gate on a dirty lane.
			if tally[lanes.FINDING] > 0 || tally[lanes.FAIL] > 0 {
				os.Exit(2)
			}
			return nil
		},
	}
	l1Cmd.Flags().BoolVar(&l1Write, "write", false, "write findings as audit-format markdown")
	l1Cmd.Flags().IntVar(&l1Round, "round", 1, "round number N for bughunt-N-*.md")
	l1Cmd.Flags().StringVar(&l1Out, "out", "", "output dir (default: <target repo>/audits)")
	l1Cmd.Flags().BoolVar(&l1Force, "force", false, "overwrite existing bughunt-N files")
	auditCmd.AddCommand(l1Cmd)

	var (
		l2Write    bool
		l2Round    int
		l2Out      string
		l2Force    bool
		l2LLM      int
		l2Ollama   string
		l2GenModel string
	)
	l2Cmd := &cobra.Command{
		Use:   "l2 <target.yaml>",
		Short: "Run the L2 input-caps lane against the mcp-stdio surface",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := target.Load(args[0])
			if err != nil {
				return err
			}
			// Optional LLM-generated probes augment the fixed battery. Fail
			// open: if the model is disabled/errors, the fixed probes still run.
			var extra func([]mcp.Tool) []caps.Probe
			if l2LLM > 0 {
				gc := ollama.New(l2Ollama, 120*time.Second)
				if gc.Enabled() {
					extra = func(tools []mcp.Tool) []caps.Probe {
						p, err := caps.GenerateLLM(func(prompt string) (string, error) {
							return gc.Generate(l2GenModel, prompt)
						}, tools, l2LLM)
						if err != nil {
							fmt.Fprintf(os.Stderr, "llm-probes: %v (fixed battery still ran)\n", err)
						}
						return p
					}
				} else {
					fmt.Fprintln(os.Stderr, "llm-probes: no --ollama host; skipping (fixed battery only)")
				}
			}
			results := lanes.RunL2Augmented(t, extra)
			printResults(results)
			tally := lanes.Tally(results)
			fmt.Printf("\nL2 %s: %d PASS  %d FINDING  %d FAIL  %d SKIP\n",
				t.Name, tally[lanes.PASS], tally[lanes.FINDING], tally[lanes.FAIL], tally[lanes.SKIP])
			if l2Write {
				report := lanes.Report("L2 caps", "caps", results)
				if err := writeRound(t, l2Round, l2Out, l2Force, 0, report); err != nil {
					return err
				}
			}
			if tally[lanes.FINDING] > 0 || tally[lanes.FAIL] > 0 {
				os.Exit(2)
			}
			return nil
		},
	}
	l2Cmd.Flags().BoolVar(&l2Write, "write", false, "write findings as audit-format markdown")
	l2Cmd.Flags().IntVar(&l2Round, "round", 1, "round number N for bughunt-N-*.md")
	l2Cmd.Flags().StringVar(&l2Out, "out", "", "output dir (default: <target repo>/audits)")
	l2Cmd.Flags().BoolVar(&l2Force, "force", false, "overwrite existing bughunt-N files")
	l2Cmd.Flags().IntVar(&l2LLM, "llm-probes", 0, "N LLM-generated adversarial probes per tool (0 = off; needs --ollama)")
	l2Cmd.Flags().StringVar(&l2Ollama, "ollama", os.Getenv("COLUMBO_OLLAMA"), "Ollama host URL for --llm-probes (default $COLUMBO_OLLAMA)")
	l2Cmd.Flags().StringVar(&l2GenModel, "gen-model", "qwen2.5-coder:7b", "generation model for --llm-probes")
	auditCmd.AddCommand(l2Cmd)

	var (
		l6Write bool
		l6Round int
		l6Out   string
		l6Force bool
	)
	l6Cmd := &cobra.Command{
		Use:   "l6 <target.yaml>",
		Short: "Run the L6 protocol-fuzz lane against the mcp-stdio surface",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := target.Load(args[0])
			if err != nil {
				return err
			}
			results := lanes.RunL6(t)
			printResults(results)
			tally := lanes.Tally(results)
			fmt.Printf("\nL6 %s: %d PASS  %d FINDING  %d FAIL  %d SKIP\n",
				t.Name, tally[lanes.PASS], tally[lanes.FINDING], tally[lanes.FAIL], tally[lanes.SKIP])
			if l6Write {
				report := lanes.Report("L6 protocol", "protocol", results)
				if err := writeRound(t, l6Round, l6Out, l6Force, 0, report); err != nil {
					return err
				}
			}
			if tally[lanes.FINDING] > 0 || tally[lanes.FAIL] > 0 {
				os.Exit(2)
			}
			return nil
		},
	}
	l6Cmd.Flags().BoolVar(&l6Write, "write", false, "write findings as audit-format markdown")
	l6Cmd.Flags().IntVar(&l6Round, "round", 1, "round number N for bughunt-N-*.md")
	l6Cmd.Flags().StringVar(&l6Out, "out", "", "output dir (default: <target repo>/audits)")
	l6Cmd.Flags().BoolVar(&l6Force, "force", false, "overwrite existing bughunt-N files")
	auditCmd.AddCommand(l6Cmd)

	// lane: the single-lane pod entrypoint. Runs one lane and prints its report
	// as a sentinel JSON block on stdout (progress goes to stderr), so a k3s
	// orchestrator can collect findings over `kubectl logs`. IDs are NOT
	// assigned here — the orchestrator numbers centrally after collecting all
	// lanes (so parallel pods never collide).
	var laneWorkdir string
	laneCmd := &cobra.Command{
		Use:   "lane <l1|l2|l6> <target.yaml>",
		Short: "Run one lane and emit its findings as a sentinel JSON block (pod entrypoint)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := target.Load(args[1])
			if err != nil {
				return err
			}
			// In a cluster pod (--workdir set, target has clone), fetch + prepare
			// the target from scratch, then point the lane at the clone.
			if laneWorkdir != "" && t.Clone != nil {
				dir, err := cloneAndSetup(t, laneWorkdir)
				if err != nil {
					return fmt.Errorf("clone/setup: %w", err)
				}
				t.Repo = dir
			}
			d := laneSpec(args[0], t)
			if d.run == nil {
				return fmt.Errorf("unknown lane %q (want l1|l2|l6)", args[0])
			}
			results := d.run()
			// Progress to stderr, so stdout carries only the sentinel block.
			tally := lanes.Tally(results)
			fmt.Fprintf(os.Stderr, "%s: %d PASS  %d FINDING  %d FAIL  %d SKIP\n",
				d.name, tally[lanes.PASS], tally[lanes.FINDING], tally[lanes.FAIL], tally[lanes.SKIP])
			block, err := lanewire.Marshal(lanes.Report(d.name, d.slug, results))
			if err != nil {
				return err
			}
			fmt.Println(block)
			return nil
		},
	}
	laneCmd.Flags().StringVar(&laneWorkdir, "workdir", "", "clone+prepare the target here (cluster pod mode); empty uses repo: as-is")
	auditCmd.AddCommand(laneCmd)

	var (
		rWrite bool
		rRound int
		rOut   string
		rForce bool
		rRaw   bool
		rK3s        bool
		rImage      string
		rPath       string
		rLanes      string
		rDedup      string
		rOllama     string
		rEmbedModel string
	)
	roundCmd := &cobra.Command{
		Use:   "round <target.yaml>",
		Short: "Run L1 + L2 + L6 and assemble one bughunt-N round",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := target.Load(args[0])
			if err != nil {
				return err
			}
			laneIDs := strings.Split(rLanes, ",")
			defs := make([]laneDef, len(laneIDs))
			for i, id := range laneIDs {
				id = strings.TrimSpace(id)
				laneIDs[i] = id
				defs[i] = laneSpec(id, t)
				if defs[i].run == nil {
					return fmt.Errorf("unknown lane %q in --lanes (want l1|l2|l6)", id)
				}
			}

			var reports []findings.LaneReport
			if rK3s {
				// Each lane runs as a k3s Job; reports come back over pod logs.
				// In-pod the pod runs `columbo lane <id> <rPath>`; rPath is the
				// target as seen inside the image (default: the baked example).
				thunks := make([]func() findings.LaneReport, len(laneIDs))
				for i, id := range laneIDs {
					id, d := id, defs[i]
					thunks[i] = func() findings.LaneReport {
						return k3srunner.RunLane(rRound, id, d.name, d.slug, rImage, rPath, 5*time.Minute)
					}
				}
				reports = orchestrator.RunParallel(thunks)
				for _, rep := range reports {
					fmt.Printf("\n=== %s (k3s) ===\n  %d findings, %d reverified, %d skipped, %d failed\n",
						rep.Lane, len(rep.Findings), len(rep.Reverified), len(rep.Skipped), len(rep.Failed))
				}
			} else {
				// Local goroutine fan-out.
				thunks := make([]func() []lanes.Result, len(defs))
				for i, d := range defs {
					thunks[i] = d.run
				}
				laneResults := orchestrator.RunParallel(thunks)
				var all []lanes.Result
				for i, d := range defs {
					fmt.Printf("\n=== %s ===\n", d.name)
					printResults(laneResults[i])
					all = append(all, laneResults[i]...)
					reports = append(reports, lanes.Report(d.name, d.slug, laneResults[i]))
				}
				tally := lanes.Tally(all)
				fmt.Printf("\nRound %s: %d PASS  %d FINDING  %d FAIL  %d SKIP\n",
					t.Name, tally[lanes.PASS], tally[lanes.FINDING], tally[lanes.FAIL], tally[lanes.SKIP])
			}

			dClient := ollama.New(rOllama, 90*time.Second)
			rawFindings, dedupedFindings := 0, 0
			for i := range reports {
				rawFindings += len(reports[i].Findings)
				if !rRaw {
					reports[i].Findings = dedupFindings(rDedup, dClient, rEmbedModel, reports[i].Findings)
				}
				dedupedFindings += len(reports[i].Findings)
			}
			if !rRaw && rawFindings != dedupedFindings {
				fmt.Printf("reconciled: %d raw findings -> %d after dedup\n", rawFindings, dedupedFindings)
			}

			if rWrite {
				if err := writeRound(t, rRound, rOut, rForce, rawFindings, reports...); err != nil {
					return err
				}
			}
			// Exit 2 if any finding or any lane failed to run (CI gate).
			anyFinding, anyFail := false, false
			for _, rep := range reports {
				if len(rep.Findings) > 0 {
					anyFinding = true
				}
				if len(rep.Failed) > 0 {
					anyFail = true
				}
			}
			if anyFinding || anyFail {
				os.Exit(2)
			}
			return nil
		},
	}
	roundCmd.Flags().BoolVar(&rWrite, "write", false, "write findings as audit-format markdown")
	roundCmd.Flags().IntVar(&rRound, "round", 1, "round number N for bughunt-N-*.md")
	roundCmd.Flags().StringVar(&rOut, "out", "", "output dir (default: <target repo>/audits)")
	roundCmd.Flags().BoolVar(&rForce, "force", false, "overwrite existing bughunt-N files")
	roundCmd.Flags().BoolVar(&rRaw, "raw", false, "skip dedup; emit every finding instance")
	roundCmd.Flags().BoolVar(&rK3s, "k3s", false, "run each lane as a k3s Job instead of local goroutines")
	roundCmd.Flags().StringVar(&rImage, "image", "columbo:slim", "image for k3s lane Jobs (--k3s)")
	roundCmd.Flags().StringVar(&rPath, "target-path", "/examples/columbo-cluster.target.yaml", "target.yaml path INSIDE the image (--k3s)")
	roundCmd.Flags().StringVar(&rLanes, "lanes", "l1,l2,l6", "comma-separated lanes to run")
	roundCmd.Flags().StringVar(&rDedup, "dedup", "structural", "dedup mode: structural | embed")
	roundCmd.Flags().StringVar(&rOllama, "ollama", os.Getenv("COLUMBO_OLLAMA"), "Ollama host URL for --dedup=embed (default $COLUMBO_OLLAMA)")
	roundCmd.Flags().StringVar(&rEmbedModel, "embed-model", "nomic-embed-text", "embedding model for --dedup=embed")
	auditCmd.AddCommand(roundCmd)

	// auto: the autonomous round. Kick off, walk away, get a PR-ready local
	// audit branch. Guardrails (internal/autonomous) replace the human
	// attention a Driven round gets for free: a failed lane or dirty tree
	// BLOCKS; UNTRIAGED/HIGH/CRITICAL findings ESCALATE (flagged, not blocked).
	var (
		aK3s   bool
		aImage string
		aPath  string
		aRound int
		aLanes string
	)
	autoCmd := &cobra.Command{
		Use:   "auto <target.yaml>",
		Short: "Run an autonomous round: lanes + guardrails + a PR-ready audit branch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := target.Load(args[0])
			if err != nil {
				return err
			}
			repo := t.RepoPath()
			auditsDir := filepath.Join(repo, "audits")

			// Resolve the round number FIRST so k3s Job names carry it
			// (gatherReports threads it into RunLane); computing it after the
			// run is what left auto --k3s Jobs named columbo-r0-*.
			round := aRound
			if round == 0 {
				wt, _ := query.Rounds(auditsDir) // missing dir -> nil, fine
				if round, err = autonomous.NextRound(repo, wt); err != nil {
					return err
				}
			}

			laneIDs := strings.Split(aLanes, ",")
			reports := gatherReports(t, round, laneIDs, aK3s, aImage, aPath)

			// Reconcile (autonomous always produces a clean deliverable).
			rawTotal := 0
			for i := range reports {
				rawTotal += len(reports[i].Findings)
				reports[i].Findings = reconcile.Dedup(reports[i].Findings)
			}

			// Guardrails: reports first, then the dirty-tree git check.
			v := autonomous.Check(reports)
			if clean, err := autonomous.CleanTree(repo); err != nil {
				return err
			} else if !clean {
				v.Blockers = append(v.Blockers, "target working tree is dirty; commit or stash before an autonomous round")
			}
			if !v.Proceed() {
				fmt.Print(autonomous.Summary(round, t.Name, "", reports, v))
				os.Exit(2)
			}

			msg := autonomous.CommitMessage(round, t.Name, reports, v)
			branch, err := autonomous.Promote(repo, round, func() ([]string, error) {
				r := &findings.Round{
					Target: t.Name, N: round, Date: time.Now().Format("2006-01-02"),
					BaselineSHA: t.Baseline.SHA, BaselineBuild: t.Baseline.Build, BaselineTest: t.Baseline.Test,
					Lanes: reports, RawFindings: rawTotal,
				}
				return r.WriteRound(auditsDir, false)
			}, msg)
			if err != nil {
				return err
			}
			fmt.Print(autonomous.Summary(round, t.Name, branch, reports, v))
			return nil
		},
	}
	autoCmd.Flags().BoolVar(&aK3s, "k3s", false, "run lanes as k3s Jobs")
	autoCmd.Flags().StringVar(&aImage, "image", "columbo:build", "image for k3s lane Jobs (--k3s)")
	autoCmd.Flags().StringVar(&aPath, "target-path", "/examples/columbo-cluster.target.yaml", "target.yaml path inside the image (--k3s)")
	autoCmd.Flags().IntVar(&aRound, "round", 0, "round number (0 = next free, branch-aware)")
	autoCmd.Flags().StringVar(&aLanes, "lanes", "l1,l2,l6", "comma-separated lanes to run")
	auditCmd.AddCommand(autoCmd)

	root.AddCommand(auditCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// cloneAndSetup fetches the target into workdir/target, checks out the
// baseline SHA, and runs the clone setup commands (build, store-init, etc.) so
// the surface is ready to probe. All subprocess output goes to stderr to keep
// the lane's stdout clean for the findings block. Returns the clone dir.
func cloneAndSetup(t *target.Target, workdir string) (string, error) {
	if err := os.MkdirAll(workdir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(workdir, "target")
	run := func(name string, arg ...string) error {
		c := exec.Command(name, arg...)
		c.Dir = workdir
		c.Stdout, c.Stderr = os.Stderr, os.Stderr
		fmt.Fprintf(os.Stderr, "+ %s %s\n", name, strings.Join(arg, " "))
		return c.Run()
	}
	if err := run("git", "clone", "--quiet", t.Clone.URL, dest); err != nil {
		return "", fmt.Errorf("git clone %s: %w", t.Clone.URL, err)
	}
	if sha := t.Baseline.SHA; sha != "" && sha != "dev" {
		c := exec.Command("git", "checkout", "--quiet", sha)
		c.Dir, c.Stdout, c.Stderr = dest, os.Stderr, os.Stderr
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("git checkout %s: %w", sha, err)
		}
	}
	for _, cmd := range t.Clone.Setup {
		c := exec.Command("sh", "-c", cmd)
		c.Dir, c.Stdout, c.Stderr = dest, os.Stderr, os.Stderr
		fmt.Fprintf(os.Stderr, "+ (in %s) %s\n", dest, cmd)
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("setup %q: %w", cmd, err)
		}
	}
	return dest, nil
}

// dedupFindings collapses a lane's findings by the chosen mode. "embed" uses
// the local model; if the model is disabled or errors, it FAILS OPEN to
// structural dedup (the model is an augmentation, never a gate on the audit).
func dedupFindings(mode string, client *ollama.Client, embedModel string, fs []findings.Finding) []findings.Finding {
	if mode == "embed" && client.Enabled() {
		out, err := reconcile.DedupEmbed(fs, func(t string) ([]float32, error) {
			return client.Embed(embedModel, t)
		}, reconcile.DefaultEmbedThreshold)
		if err == nil {
			return out
		}
		fmt.Fprintf(os.Stderr, "embedding dedup unavailable (%v); falling back to structural\n", err)
	}
	return reconcile.Dedup(fs)
}

// gatherReports runs the given lanes (local goroutines, or k3s Jobs) and
// returns one raw LaneReport per lane, in lane order. No printing, no dedup,
// no write — the caller decides. Unknown lane ids yield a Failed report so the
// guardrails treat them honestly.
func gatherReports(t *target.Target, round int, laneIDs []string, k3s bool, image, path string) []findings.LaneReport {
	if k3s {
		thunks := make([]func() findings.LaneReport, len(laneIDs))
		for i, id := range laneIDs {
			id, d := strings.TrimSpace(id), laneSpec(strings.TrimSpace(id), t)
			thunks[i] = func() findings.LaneReport {
				return k3srunner.RunLane(round, id, d.name, d.slug, image, path, 5*time.Minute)
			}
		}
		return orchestrator.RunParallel(thunks)
	}
	thunks := make([]func() findings.LaneReport, len(laneIDs))
	for i, id := range laneIDs {
		id, d := strings.TrimSpace(id), laneSpec(strings.TrimSpace(id), t)
		thunks[i] = func() findings.LaneReport {
			if d.run == nil {
				return findings.LaneReport{Lane: id, Failed: []string{"unknown lane " + id}}
			}
			return lanes.Report(d.name, d.slug, d.run())
		}
	}
	return orchestrator.RunParallel(thunks)
}

// laneDef is one lane's display name, file slug, and run thunk.
type laneDef struct {
	name, slug string
	run        func() []lanes.Result
}

// laneSpec maps a lane id (l1/l2/l6) to its def for a target. An unknown id
// yields a def whose run is nil (callers check).
func laneSpec(id string, t *target.Target) laneDef {
	switch id {
	case "l1":
		return laneDef{"L1 build invariants", "build-invariants", func() []lanes.Result { return lanes.RunL1(t) }}
	case "l2":
		return laneDef{"L2 caps", "caps", func() []lanes.Result { return lanes.RunL2(t) }}
	case "l6":
		return laneDef{"L6 protocol", "protocol", func() []lanes.Result { return lanes.RunL6(t) }}
	}
	return laneDef{}
}

// indent re-indents a multi-line detail block under its verdict line.
func indent(s string) string {
	return strings.ReplaceAll(s, "\n", "\n         ")
}

// printResults prints the per-probe verdict lines for a lane run.
func printResults(results []lanes.Result) {
	for _, r := range results {
		fmt.Printf("%-8s %s\n", r.Verdict, r.Probe)
		if r.Detail != "" {
			fmt.Printf("         %s\n", indent(r.Detail))
		}
	}
}

// writeRound assembles a round from one or more lane reports and writes the
// audit-format set, defaulting the output dir to the target repo's audits/.
func writeRound(t *target.Target, round int, out string, force bool, rawCount int, reports ...findings.LaneReport) error {
	r := &findings.Round{
		Target:        t.Name,
		N:             round,
		Date:          time.Now().Format("2006-01-02"),
		BaselineSHA:   t.Baseline.SHA,
		BaselineBuild: t.Baseline.Build,
		BaselineTest:  t.Baseline.Test,
		Lanes:         reports,
		RawFindings:   rawCount,
	}
	dir := out
	if dir == "" {
		dir = filepath.Join(t.RepoPath(), "audits")
	}
	written, err := r.WriteRound(dir, force)
	if err != nil {
		return err
	}
	fmt.Printf("\nwrote %d audit file(s) to %s:\n", len(written), dir)
	for _, p := range written {
		fmt.Printf("  %s\n", filepath.Base(p))
	}
	return nil
}
