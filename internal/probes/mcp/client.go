// Package mcp is a stdio JSON-RPC client for driving an MCP server under
// audit. It is deliberately a one-shot-session-per-probe design: each call
// spawns a fresh server process, optionally handshakes, writes a sequence of
// actions to the server's stdin, drains stdout adaptively, then kills the
// process. A stdio server *is* the connection, so there is nothing to
// reconnect to; a fresh process per probe is also what the protocol-fuzz lane
// wants, since some probes deliberately wedge the connection.
//
// Ported from seed/harness/stdio/mcp.py (handshake) and
// seed/harness/unix-socket/mcp_sock.py (the adaptive-recv-timeout lesson):
// wait the full deadline before the first byte (the server may be slow, or for
// framing fuzz may stay silent on purpose), then drop to a short idle grace
// once data arrives, because the server stays up and will not half-close.
// Never close the write side early (the unix-socket L4 run found the SDK tears
// the connection down on client-EOF before flushing some responses).
//
// The client is launch-agnostic: it takes an argv and a working dir, and does
// not care who built the server. LANE NOTE for L2/L6: build the target's MCP
// binary ONCE to a temp dir at lane start and point Argv at it, rather than
// `go run ./cmd/...` per probe (which pays the compile delay on every probe).
// That build-to-tempdir step is the same machinery deferred for the
// ldflags-P3 gap in docs/v0.2-deferred.md; they share a solution.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	protocolVersion  = "2025-06-18"
	maxBytes         = 16 << 20 // cap accumulated stdout, mirroring transport.go guards
	defaultIdleGrace = 500 * time.Millisecond
)

// Action is one write to the server's stdin within a session: raw bytes, plus
// an optional pause after writing them (to let the server process before the
// next write, and to make split-frame probes expressible).
type Action struct {
	Bytes []byte
	After time.Duration
}

// Frame is a parsed JSON-RPC response line. Lines that do not parse as JSON
// are stored as {"_raw": "<line>"} so framing probes can still inspect them.
type Frame map[string]any

// Session is the outcome of driving one server process.
type Session struct {
	Responses   []Frame  // parsed stdout lines, in order
	Lines       []string // raw stdout lines, verbatim
	Stderr      string
	DeadlineHit bool // the overall wall-clock deadline elapsed
}

// Find returns the response whose JSON-RPC id equals want, or nil. A nil
// result for an id a probe sent is the silent-drop signal.
func (s *Session) Find(want int) Frame {
	for _, r := range s.Responses {
		if n, ok := r["id"].(float64); ok && int(n) == want {
			return r
		}
	}
	return nil
}

// ResponseText flattens the response for id into one string for leak scanning:
// the JSON-RPC error message, or the tool result's text content.
func (s *Session) ResponseText(id int) string {
	r := s.Find(id)
	if r == nil {
		return ""
	}
	if e, ok := r["error"].(map[string]any); ok {
		if m, ok := e["message"].(string); ok {
			return m
		}
	}
	if res, ok := r["result"].(map[string]any); ok {
		var b strings.Builder
		if content, ok := res["content"].([]any); ok {
			for _, c := range content {
				if cm, ok := c.(map[string]any); ok {
					if txt, ok := cm["text"].(string); ok {
						b.WriteString(txt)
					}
				}
			}
		}
		return b.String()
	}
	return ""
}

// IsError reports whether the response for id is a JSON-RPC error or a tool
// result flagged isError.
func (s *Session) IsError(id int) bool {
	r := s.Find(id)
	if r == nil {
		return false
	}
	if _, ok := r["error"]; ok {
		return true
	}
	if res, ok := r["result"].(map[string]any); ok {
		if ie, ok := res["isError"].(bool); ok {
			return ie
		}
	}
	return false
}

// Client launches and drives one MCP stdio server.
type Client struct {
	Argv      []string      // server command, e.g. ["/tmp/leonard-mcp"]
	Dir       string        // cwd; for leonard-mcp this MUST contain a .leonard/ store
	Env       []string      // extra env appended to os.Environ() (used by tests)
	Timeout   time.Duration // overall wall-clock per session
	IdleGrace time.Duration // quiet window after first byte before stopping
}

// New builds a Client with the default idle grace.
func New(argv []string, dir string, timeout time.Duration) *Client {
	return &Client{Argv: argv, Dir: dir, Timeout: timeout, IdleGrace: defaultIdleGrace}
}

// List performs the handshake and returns the server's tool names.
func (c *Client) List() ([]string, *Session, error) {
	acts := append(handshake(), frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	}, 0))
	sess, err := c.run(acts)
	if err != nil {
		return nil, sess, err
	}
	r := sess.Find(2)
	if r == nil {
		return nil, sess, fmt.Errorf("no tools/list response (stderr: %s)", strings.TrimSpace(sess.Stderr))
	}
	var names []string
	if result, ok := r["result"].(map[string]any); ok {
		if tools, ok := result["tools"].([]any); ok {
			for _, t := range tools {
				if tm, ok := t.(map[string]any); ok {
					if n, ok := tm["name"].(string); ok {
						names = append(names, n)
					}
				}
			}
		}
	}
	return names, sess, nil
}

// Tool is a tool descriptor from tools/list, with its input schema flattened
// to what the caps lane needs: property types, required names, and whether
// additional properties are allowed.
type Tool struct {
	Name                 string
	Properties           map[string]string // property name -> JSON Schema type
	Required             []string
	AdditionalProperties bool // false when the schema sets it explicitly false
}

// ListTools performs the handshake and returns full tool descriptors.
func (c *Client) ListTools() ([]Tool, *Session, error) {
	acts := append(handshake(), frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	}, 0))
	sess, err := c.run(acts)
	if err != nil {
		return nil, sess, err
	}
	r := sess.Find(2)
	if r == nil {
		return nil, sess, fmt.Errorf("no tools/list response (stderr: %s)", strings.TrimSpace(sess.Stderr))
	}
	result, _ := r["result"].(map[string]any)
	rawTools, _ := result["tools"].([]any)
	var tools []Tool
	for _, rt := range rawTools {
		tm, ok := rt.(map[string]any)
		if !ok {
			continue
		}
		t := Tool{AdditionalProperties: true}
		t.Name, _ = tm["name"].(string)
		schema, _ := tm["inputSchema"].(map[string]any)
		if ap, ok := schema["additionalProperties"].(bool); ok {
			t.AdditionalProperties = ap
		}
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					t.Required = append(t.Required, s)
				}
			}
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			t.Properties = map[string]string{}
			for name, pv := range props {
				if pm, ok := pv.(map[string]any); ok {
					t.Properties[name], _ = pm["type"].(string)
				}
			}
		}
		tools = append(tools, t)
	}
	return tools, sess, nil
}

// ServerInfo performs the handshake and returns the server's reported
// name and version from the initialize result's serverInfo.
func (c *Client) ServerInfo() (name, version string, sess *Session, err error) {
	sess, err = c.run(handshake())
	if err != nil {
		return "", "", sess, err
	}
	r := sess.Find(1)
	if r == nil {
		return "", "", sess, fmt.Errorf("no initialize response (stderr: %s)", strings.TrimSpace(sess.Stderr))
	}
	result, _ := r["result"].(map[string]any)
	si, _ := result["serverInfo"].(map[string]any)
	name, _ = si["name"].(string)
	version, _ = si["version"].(string)
	return name, version, sess, nil
}

// Call performs the handshake and invokes one tool (id=2).
func (c *Client) Call(tool string, args any) (*Session, error) {
	if args == nil {
		args = map[string]any{}
	}
	acts := append(handshake(), frame(map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	}, 0))
	return c.run(acts)
}

// Raw sends each frame string newline-terminated (optionally after a
// handshake). For frames that must violate the framing contract (no trailing
// newline, split across writes, BOM prefix), use RawActions.
func (c *Client) Raw(doHandshake bool, frames ...string) (*Session, error) {
	var acts []Action
	if doHandshake {
		acts = handshake()
	}
	for _, f := range frames {
		acts = append(acts, Action{Bytes: []byte(f + "\n"), After: 50 * time.Millisecond})
	}
	return c.run(acts)
}

// RawActions sends an explicit ordered sequence of byte writes (optionally
// after a handshake). This is the escape hatch for framing fuzz: split a frame
// across two writes, send bytes with no newline, prepend a BOM, etc.
func (c *Client) RawActions(doHandshake bool, actions ...Action) (*Session, error) {
	var acts []Action
	if doHandshake {
		acts = handshake()
	}
	acts = append(acts, actions...)
	return c.run(acts)
}

func handshake() []Action {
	return []Action{
		frame(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "initialize",
			"params": map[string]any{
				"protocolVersion": protocolVersion,
				"capabilities":    map[string]any{},
				"clientInfo":      map[string]any{"name": "columbo", "version": "0.1"},
			},
		}, 150*time.Millisecond),
		frame(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, 100*time.Millisecond),
	}
}

func frame(obj any, after time.Duration) Action {
	b, _ := json.Marshal(obj)
	return Action{Bytes: append(b, '\n'), After: after}
}

// run spawns the server, performs the write actions, and drains stdout with
// the adaptive deadline. It always reaps the process before returning.
func (c *Client) run(actions []Action) (*Session, error) {
	if len(c.Argv) == 0 {
		return nil, fmt.Errorf("mcp: empty argv")
	}
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Reader goroutine: newline-framed, capped, ignores blank lines.
	// readerDone lets us join the goroutine before cmd.Wait() (os/exec requires
	// all reads to finish first).
	//
	// KNOWN LIMITATION (F018 class, the very bug Columbo hunts): ReadBytes
	// buffers an unterminated line in its own internal buffer without bound, so
	// the maxBytes check below only fires *after* a full line returns. A target
	// that floods bytes with no newline can grow that buffer until the client
	// OOMs. Acceptable for v0.3 because targets are trusted (leonard/bosun/
	// columbo); when Columbo audits its own MCP client this is a known finding,
	// not a surprise. Fix shape: read fixed chunks with a manual delimiter scan
	// and an enforced cap. Tracked for the self-audit.
	lines := make(chan string, 64)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(lines)
		r := bufio.NewReader(stdout)
		total := 0
		for {
			b, err := r.ReadBytes('\n')
			if len(b) > 0 {
				total += len(b)
				if s := strings.TrimRight(string(b), "\n"); strings.TrimSpace(s) != "" {
					lines <- s
				}
				if total > maxBytes {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Writer goroutine: perform actions in order, then signal completion. Do
	// NOT close stdin (L4 lesson); it closes when we kill the process after
	// draining.
	writesDone := make(chan struct{})
	go func() {
		for _, a := range actions {
			_, _ = stdin.Write(a.Bytes)
			if a.After > 0 {
				time.Sleep(a.After)
			}
		}
		close(writesDone)
	}()

	idleGrace := c.IdleGrace
	if idleGrace == 0 {
		idleGrace = defaultIdleGrace
	}
	var sess Session
	got := false   // received at least one line
	done := false  // all writes sent
	deadline := time.After(c.Timeout)
	// The idle grace only applies once we have data AND have finished sending
	// requests: a quiet gap while requests are still going out is expected, not
	// idle. Each received line or the writes-done event resets the window (the
	// timer is recreated fresh every loop iteration).
drain:
	for {
		var idle <-chan time.Time
		if got && done {
			idle = time.After(idleGrace)
		}
		select {
		case line, ok := <-lines:
			if !ok {
				break drain // server closed stdout
			}
			sess.Lines = append(sess.Lines, line)
			got = true
		case <-writesDone:
			done = true
			writesDone = nil // stop selecting on the closed channel
		case <-idle:
			break drain // quiet after data and after writes: server idle, stop
		case <-deadline:
			sess.DeadlineHit = true
			break drain
		}
	}

	_ = stdin.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	// Unblock any pending `lines <- s` send (the reader may have queued >64
	// lines while we stopped draining), then join the reader before Wait so all
	// reads complete first (os/exec contract) and no goroutine leaks.
	go func() {
		for range lines {
		}
	}()
	<-readerDone
	_ = cmd.Wait()
	sess.Stderr = stderr.String()

	for _, ln := range sess.Lines {
		var f Frame
		if json.Unmarshal([]byte(ln), &f) == nil {
			sess.Responses = append(sess.Responses, f)
		} else {
			sess.Responses = append(sess.Responses, Frame{"_raw": ln})
		}
	}
	return &sess, nil
}
