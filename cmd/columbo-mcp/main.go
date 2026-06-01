// Columbo-MCP. Stdio JSON-RPC server for observing Columbo audits from inside a
// Claude Code (or any MCP client) session.
//
// "Just one more thing..."
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jasondillingham/columbo/internal/mcpserver"
	"github.com/jasondillingham/columbo/internal/query"
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
