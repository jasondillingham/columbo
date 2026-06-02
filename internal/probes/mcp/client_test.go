package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain doubles as the fake MCP server. When COLUMBO_FAKE_MCP is set, the
// test binary behaves as a tiny stdio JSON-RPC server and os.Exit(0)s WITHOUT
// ever reaching m.Run(), so the test framework's "PASS\nok ..." never lands on
// stdout (which is the exact stream the client parses). See advisor note #2.
func TestMain(m *testing.M) {
	if os.Getenv("COLUMBO_FAKE_MCP") != "" {
		fakeServer()
		return // unreachable: fakeServer exits the process
	}
	os.Exit(m.Run())
}

// fakeServer speaks just enough MCP to exercise the client:
//   - initialize        -> serverInfo result
//   - tools/list        -> three tools
//   - tools/call echo    -> immediate result
//   - tools/call slow    -> result after 300ms (exercises the adaptive wait)
//   - tools/call silent  -> NO response (silent-drop signal)
//   - anything else      -> NO response
func fakeServer() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var f map[string]any
		if json.Unmarshal(sc.Bytes(), &f) != nil {
			continue
		}
		method, _ := f["method"].(string)
		id := idStr(f["id"])
		switch method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"serverInfo":{"name":"fake","version":"9.9.9"},"protocolVersion":"2025-06-18","capabilities":{}}}`+"\n", id)
		case "notifications/initialized":
			// no response
		case "tools/list":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo"},{"name":"slow"},{"name":"silent"}]}}`+"\n", id)
		case "tools/call":
			name := ""
			if p, ok := f["params"].(map[string]any); ok {
				name, _ = p["name"].(string)
			}
			switch name {
			case "slow":
				time.Sleep(300 * time.Millisecond)
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"slow":true}}`+"\n", id)
			case "silent":
				// no response
			case "flood":
				// Emit lines forever: exercises the >64-buffered-send path and
				// the deadline stop. The client must still return (no hang).
				for i := 0; ; i++ {
					fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"n":%d}}`+"\n", id, i)
				}
			default:
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"echo":true}}`+"\n", id)
			}
		}
	}
	os.Exit(0)
}

func idStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func fakeClient(timeout time.Duration) *Client {
	c := New([]string{os.Args[0]}, "", timeout)
	c.Env = []string{"COLUMBO_FAKE_MCP=1"}
	return c
}

func TestListAndStreamIsClean(t *testing.T) {
	c := fakeClient(5 * time.Second)
	names, sess, err := c.List()
	if err != nil {
		t.Fatalf("List: %v (stderr: %s)", err, sess.Stderr)
	}
	want := map[string]bool{"echo": true, "slow": true, "silent": true}
	if len(names) != 3 {
		t.Fatalf("names = %v, want 3 tools", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected tool %q", n)
		}
	}
	// Advisor #2: the helper-process stdout must carry no test-framework noise.
	for _, ln := range sess.Lines {
		if strings.Contains(ln, "PASS") || strings.HasPrefix(ln, "ok ") || strings.Contains(ln, "FAIL") {
			t.Fatalf("test-framework noise leaked into the response stream: %q", ln)
		}
	}
}

func TestCallEcho(t *testing.T) {
	c := fakeClient(5 * time.Second)
	sess, err := c.Call("echo", map[string]any{"x": 1})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Find(2) == nil {
		t.Errorf("no id=2 response; responses=%v", sess.Responses)
	}
}

// slow responds after 300ms; before the first byte the client waits the full
// deadline, so a slow-but-correct response must still arrive.
func TestAdaptiveSlowResponse(t *testing.T) {
	c := fakeClient(5 * time.Second)
	sess, err := c.Call("slow", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Find(2) == nil {
		t.Errorf("slow response should arrive within deadline; responses=%v", sess.Responses)
	}
}

// silent never answers the tool call, though the handshake initialize does.
// Silent-drop is detected by the missing id=2, not by an empty session.
func TestSilentDrop(t *testing.T) {
	c := fakeClient(5 * time.Second) // idle grace stops us ~500ms after init reply
	sess, err := c.Call("silent", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Find(1) == nil {
		t.Errorf("handshake initialize should have answered; responses=%v", sess.Responses)
	}
	if sess.Find(2) != nil {
		t.Errorf("silent tool must not answer id=2; got %v", sess.Find(2))
	}
}

// Total silence (no handshake, unknown method) exercises the full-deadline
// before-first-byte branch.
func TestNoHandshakeTotalSilence(t *testing.T) {
	c := fakeClient(700 * time.Millisecond)
	sess, err := c.Raw(false, `{"jsonrpc":"2.0","id":9,"method":"unknown/method"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Responses) != 0 {
		t.Errorf("expected no responses on total silence, got %v", sess.Responses)
	}
	if !sess.DeadlineHit {
		t.Errorf("total silence should hit the wall-clock deadline")
	}
}

// F018 fix: a no-newline flood must be BOUNDED — readCappedLine drops the
// over-cap line (drains to the next newline) instead of buffering to OOM, and
// recovers to the next frame. (Before the fix, ReadBytes buffered unbounded.)
func TestReadCappedLineBounds(t *testing.T) {
	in := bufio.NewReader(strings.NewReader(strings.Repeat("A", 5000) + "\n" + "{\"ok\":1}\n"))
	line, tooLong, err := readCappedLine(in, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !tooLong || line != nil {
		t.Errorf("5000-byte line with cap 100 must be tooLong + dropped, got len=%d tooLong=%v", len(line), tooLong)
	}
	line2, tooLong2, _ := readCappedLine(in, 100)
	if tooLong2 || string(line2) != `{"ok":1}` {
		t.Errorf("reader must recover to the next frame, got %q tooLong=%v", line2, tooLong2)
	}
}

// A server that floods stdout without pause must not hang or leak the reader:
// the client stops at the deadline, unblocks the reader, and returns.
func TestFloodDoesNotHang(t *testing.T) {
	c := fakeClient(800 * time.Millisecond)
	done := make(chan *Session, 1)
	go func() {
		sess, err := c.Call("flood", nil)
		if err != nil {
			t.Errorf("Call: %v", err)
		}
		done <- sess
	}()
	select {
	case sess := <-done:
		// The stop reason (deadline, or the 16 MiB cap if the flood is fast) is
		// incidental; the invariant is that the client returns without hanging
		// and collected output.
		if len(sess.Lines) == 0 {
			t.Errorf("flood should have collected some lines")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client hung on a flooding server")
	}
}

// A single frame split across two writes must reassemble server-side (the
// multi-write Action primitive, advisor note #1).
func TestSplitFrameReassembled(t *testing.T) {
	c := fakeClient(5 * time.Second)
	full := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"0"}}}` + "\n"
	half := len(full) / 2
	sess, err := c.RawActions(false,
		Action{Bytes: []byte(full[:half]), After: 80 * time.Millisecond},
		Action{Bytes: []byte(full[half:])},
	)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Find(1) == nil {
		t.Errorf("split initialize should reassemble and answer id=1; responses=%v", sess.Responses)
	}
}
