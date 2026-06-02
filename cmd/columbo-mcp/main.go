// Columbo-MCP. Stdio JSON-RPC server for observing Columbo audits from inside a
// Claude Code (or any MCP client) session.
//
// "Just one more thing..."
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/columbo/internal/findings"
	"github.com/jasondillingham/columbo/internal/mcpserver"
	"github.com/jasondillingham/columbo/internal/query"
	"github.com/jasondillingham/columbo/internal/reason"
	"github.com/jasondillingham/columbo/internal/version"
)

func main() {
	var auditsDir string

	root := &cobra.Command{
		Use:   "columbo-mcp",
		Short: "Stdio MCP server for Columbo's observe surface",
		Long: `columbo-mcp serves the read-only observe tools audit_status and
audit_findings over JSON-RPC stdio, reading the written bughunt-N-*.md rounds
in the audits directory. Wire it into Claude Code via .mcp.json.

Control tools (audit_start, audit_promote) are a later milestone.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			srv := mcpserver.New("columbo-mcp", version.Version)
			registerTools(srv, auditsDir)
			registerReasonTools(srv, reason.NewSession())
			return srv.Serve(os.Stdin, os.Stdout)
		},
	}
	root.Flags().StringVar(&auditsDir, "audits", "audits", "directory holding bughunt-N-*.md rounds")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the build version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("columbo-mcp version %s\n", version.Version)
		},
	})

	root.SilenceErrors = true
	root.SilenceUsage = true
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// roundArgs is the shared input schema/parse for both tools: an optional round
// number, 0 (or absent) meaning the latest completed round.
type roundArgs struct {
	Round int `json:"round"`
}

// parseRound reads the optional round arg. A wrong-TYPE round (e.g. "3" instead
// of 3) is rejected with a CLEAN error — not silently treated as round 0
// (latest), which would be the silent-accept class Columbo hunts, and not the
// raw json.Unmarshal error, which would leak Go internals (the F002/F003
// class). Columbo's own server must not exhibit either.
func parseRound(raw json.RawMessage) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var a roundArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return 0, fmt.Errorf("invalid arguments: round must be an integer")
	}
	return a.Round, nil
}

var roundSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"round": map[string]any{
			"type":        "integer",
			"description": "round number N (bughunt-N); 0 or omitted = latest completed round",
		},
	},
}

func registerTools(srv *mcpserver.Server, auditsDir string) {
	srv.Register(mcpserver.Tool{
		Name:        "audit_status",
		Description: "Summary of the last completed audit round: round number, total findings, severity tally, and per-lane status (including SKIPPED/FAILED lanes). Not live progress — there is no running audit until the control surface lands.",
		InputSchema: roundSchema,
		Handler: func(args json.RawMessage) (any, error) {
			round, err := parseRound(args)
			if err != nil {
				return nil, err
			}
			return query.Summarize(auditsDir, round)
		},
	})
	srv.Register(mcpserver.Tool{
		Name:        "audit_findings",
		Description: "Rollup of findings for a round (id, severity, lane, title, status). Rollup-level only; full reproducers and fix-shapes live in the per-lane bughunt-N-<lane>.md files.",
		InputSchema: roundSchema,
		Handler: func(args json.RawMessage) (any, error) {
			round, err := parseRound(args)
			if err != nil {
				return nil, err
			}
			n, rows, err := query.Findings(auditsDir, round)
			if err != nil {
				return nil, err
			}
			return map[string]any{"round": n, "findings": rows}, nil
		},
	})
}

// registerReasonTools adds Columbo's Driven control surface: a Claude Code
// session reads the code and reasons; these tools let it drive a red-team round
// (start -> record -> reproduce -> finalize). Columbo holds the round and
// CONFIRMS findings by executing their reproducers in isolation — the session
// proposes, execution disposes.
func registerReasonTools(srv *mcpserver.Server, sess *reason.Session) {
	srv.Register(mcpserver.Tool{
		Name: "reason_start",
		Description: "Begin a red-team round against a directory. You (the session) read the code and find bugs; record each with reason_record, confirm it with reason_reproduce, then reason_finalize. Returns the round spec.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"dir": map[string]any{"type": "string", "description": "target repo/dir root to red-team"}},
			"required":   []any{"dir"},
		},
		Handler: func(args json.RawMessage) (any, error) {
			var a struct {
				Dir string `json:"dir"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, fmt.Errorf("invalid args: dir is required")
			}
			note, err := sess.Start(a.Dir)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"started": a.Dir,
				"note":    note,
				"how": "Read the code with your own tools. For each real bug, call reason_record with a Go-test reproducer that PASSES iff the bug is present (assert the bug's symptom). Then reason_reproduce(id): exit 0 => CONFIRMED, else not. Findings whose reproducer doesn't demonstrate the bug land UNTRIAGED, never confirmed. reason_finalize writes the round.",
				"severity_scale": "CRITICAL|HIGH|MEDIUM|LOW (UNTRIAGED if you can't justify one — don't guess).",
			}, nil
		},
	})

	srv.Register(mcpserver.Tool{
		Name: "reason_record",
		Description: "Record a candidate finding with a reproducer. The reproducer is a complete Go _test.go file that PASSES (exit 0) iff the bug is present.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":        map[string]any{"type": "string"},
				"severity":     map[string]any{"type": "string", "description": "CRITICAL|HIGH|MEDIUM|LOW (or omit for UNTRIAGED)"},
				"files":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "repo-relative source files implicated"},
				"mechanism":    map[string]any{"type": "string", "description": "how the bug works (becomes Observed)"},
				"expected":     map[string]any{"type": "string", "description": "the correct behavior"},
				"fix_shape":    map[string]any{"type": "string"},
				"repro_pkgdir": map[string]any{"type": "string", "description": "package dir (repo-relative) the reproducer test goes in"},
				"repro_run":    map[string]any{"type": "string", "description": "the test function name to run"},
				"repro_file":   map[string]any{"type": "string", "description": "full _test.go contents (package decl + imports + func)"},
			},
			"required": []any{"title", "repro_pkgdir", "repro_run", "repro_file"},
		},
		Handler: func(args json.RawMessage) (any, error) {
			var a struct {
				Title, Severity, Mechanism, Expected, FixShape string
				Files                                          []string
				ReproPkgdir                                    string `json:"repro_pkgdir"`
				ReproRun                                       string `json:"repro_run"`
				ReproFile                                      string `json:"repro_file"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, fmt.Errorf("invalid args: %w", err)
			}
			sev := findings.Severity(a.Severity)
			if !sev.Valid() {
				sev = findings.Untriaged
			}
			id, err := sess.Record(findings.Finding{
				Severity: sev, Title: a.Title, Files: a.Files,
				Observed: a.Mechanism, Expected: a.Expected, FixShape: a.FixShape,
				Reproducer: fmt.Sprintf("go test ./%s -run %s", a.ReproPkgdir, a.ReproRun),
			}, reason.Reproducer{PkgDir: a.ReproPkgdir, Run: a.ReproRun, File: a.ReproFile})
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id, "next": "call reason_reproduce with this id to confirm by execution"}, nil
		},
	})

	srv.Register(mcpserver.Tool{
		Name: "reason_reproduce",
		Description: "Run candidate <id>'s reproducer in an isolated copy of the target. Exit 0 => CONFIRMED (bug demonstrated); otherwise the candidate stays unconfirmed (it will finalize as UNTRIAGED).",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"id": map[string]any{"type": "integer"}},
			"required":   []any{"id"},
		},
		Handler: func(args json.RawMessage) (any, error) {
			var a struct {
				ID int `json:"id"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, fmt.Errorf("invalid args: id is required")
			}
			c, err := sess.Reproduce(a.ID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": a.ID, "status": c.Status, "output": c.RunOutput}, nil
		},
	})

	srv.Register(mcpserver.Tool{
		Name:        "reason_finalize",
		Description: "Write the round as bughunt-N-*.md under <target>/audits. Confirmed findings keep their severity; unconfirmed ones are written UNTRIAGED. Refuses an empty round.",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(args json.RawMessage) (any, error) {
			lr, err := sess.Finalize("Reason (driven review)", "reason")
			if err != nil {
				return nil, err
			}
			dir := sess.Dir()
			auditsDir := filepath.Join(dir, "audits")
			wt, _ := query.Rounds(auditsDir)
			n := 1
			for _, r := range wt {
				if r >= n {
					n = r + 1
				}
			}
			round := &findings.Round{
				Target: filepath.Base(dir), N: n, Date: time.Now().Format("2006-01-02"),
				Lanes: []findings.LaneReport{lr},
			}
			written, err := round.WriteRound(auditsDir, false)
			if err != nil {
				return nil, err
			}
			names := make([]string, len(written))
			for i, p := range written {
				names[i] = filepath.Base(p)
			}
			return map[string]any{"round": n, "dir": auditsDir, "files": names, "findings": len(lr.Findings)}, nil
		},
	})
}
