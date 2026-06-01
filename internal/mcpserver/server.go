// Package mcpserver is a small, deliberately-correct stdio JSON-RPC server for
// columbo-mcp's observe surface. Hand-rolled (no SDK dep) because the surface
// is tiny and because Columbo's purpose is finding MCP-server bugs — so this
// server gets the classes Columbo hunts RIGHT on purpose: nonzero error codes,
// a -32700 response on a parse error (never a silent drop, the F019 class), and
// a bounded reader so a no-newline flood can't grow memory without limit (the
// F018 class).
package mcpserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const protocolVersion = "2025-06-18"

// maxLine bounds a single frame. A frame larger than this is rejected with a
// JSON-RPC error rather than buffered unbounded (F018 guard).
const maxLine = 1 << 20 // 1 MiB

// Tool is a registered tool the server advertises and dispatches.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// Handler receives the call's raw arguments and returns a value (JSON-
	// encoded into the tool result's text) or an error (returned as an
	// isError tool result, not a protocol error).
	Handler func(args json.RawMessage) (any, error)
}

// Server hosts a set of tools over stdio JSON-RPC.
type Server struct {
	name, version string
	tools         map[string]Tool
	order         []string
}

func New(name, version string) *Server {
	return &Server{name: name, version: version, tools: map[string]Tool{}}
}

// Register adds a tool (idempotent on name; last registration wins).
func (s *Server) Register(t Tool) {
	if _, ok := s.tools[t.Name]; !ok {
		s.order = append(s.order, t.Name)
	}
	s.tools[t.Name] = t
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent => notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// Serve runs the read loop until EOF on r, writing responses to w.
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)
	enc := json.NewEncoder(w)
	for {
		line, tooLong, err := readBoundedLine(br, maxLine)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if tooLong {
			s.writeError(enc, nil, -32700, fmt.Sprintf("frame exceeds %d-byte limit", maxLine))
			continue
		}
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req request
		if json.Unmarshal(line, &req) != nil {
			// Parse error MUST be answered, never silently dropped.
			s.writeError(enc, nil, -32700, "parse error: malformed JSON-RPC")
			continue
		}
		s.dispatch(enc, &req)
	}
}

func (s *Server) dispatch(enc *json.Encoder, req *request) {
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		s.writeResult(enc, req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
			"capabilities":    map[string]any{"tools": map[string]any{}},
		})
	case "notifications/initialized":
		// notification: no response
	case "tools/list":
		s.writeResult(enc, req.ID, map[string]any{"tools": s.toolList()})
	case "tools/call":
		s.handleCall(enc, req)
	default:
		if !isNotification {
			s.writeError(enc, req.ID, -32601, fmt.Sprintf("method not found: %q", req.Method))
		}
	}
}

func (s *Server) handleCall(enc *json.Encoder, req *request) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.writeError(enc, req.ID, -32602, "invalid params for tools/call")
		return
	}
	tool, ok := s.tools[p.Name]
	if !ok {
		s.writeError(enc, req.ID, -32602, fmt.Sprintf("unknown tool %q", p.Name))
		return
	}
	out, err := tool.Handler(p.Arguments)
	if err != nil {
		// Tool execution error: an isError result, not a protocol error.
		s.writeResult(enc, req.ID, map[string]any{
			"content": []any{textContent(err.Error())},
			"isError": true,
		})
		return
	}
	body, mErr := json.MarshalIndent(out, "", "  ")
	if mErr != nil {
		s.writeResult(enc, req.ID, map[string]any{
			"content": []any{textContent("internal: could not encode result: " + mErr.Error())},
			"isError": true,
		})
		return
	}
	s.writeResult(enc, req.ID, map[string]any{
		"content": []any{textContent(string(body))},
	})
}

func (s *Server) toolList() []any {
	var out []any
	for _, name := range s.order {
		t := s.tools[name]
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return out
}

func textContent(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func (s *Server) writeResult(enc *json.Encoder, id json.RawMessage, result any) {
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawID(id),
		"result":  result,
	})
}

func (s *Server) writeError(enc *json.Encoder, id json.RawMessage, code int, msg string) {
	_ = enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      rawID(id),
		"error":   map[string]any{"code": code, "message": msg},
	})
}

// rawID echoes the request id verbatim, or null when absent.
func rawID(id json.RawMessage) any {
	if len(id) == 0 {
		return nil
	}
	return id
}

// readBoundedLine reads one newline-terminated frame, capped at max bytes. A
// frame longer than max returns tooLong=true after draining to the next
// newline, so the loop can answer with an error and keep serving rather than
// buffering without bound.
func readBoundedLine(r *bufio.Reader, max int) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		b, e := r.ReadByte()
		if e != nil {
			if len(buf) > 0 && e == io.EOF {
				return buf, false, nil
			}
			return nil, false, e
		}
		if b == '\n' {
			return buf, false, nil
		}
		if len(buf) >= max {
			for {
				bb, de := r.ReadByte()
				if de != nil || bb == '\n' {
					break
				}
			}
			return nil, true, nil
		}
		buf = append(buf, b)
	}
}
