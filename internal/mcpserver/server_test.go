package mcpserver

import (
	"bufio"
	"encoding/json"
	"strings"
	"testing"
)

func testServer() *Server {
	s := New("columbo-mcp", "test")
	s.Register(Tool{
		Name:        "echo",
		Description: "echoes its args",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(args json.RawMessage) (any, error) {
			return map[string]any{"got": json.RawMessage(args)}, nil
		},
	})
	return s
}

// drive feeds newline-framed requests and returns the parsed response frames.
func drive(t *testing.T, frames ...string) []map[string]any {
	t.Helper()
	in := strings.Join(frames, "\n") + "\n"
	var out strings.Builder
	if err := testServer().Serve(strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var resps []map[string]any
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("response not JSON: %q", sc.Text())
		}
		resps = append(resps, m)
	}
	return resps
}

func errCode(m map[string]any) (float64, bool) {
	e, ok := m["error"].(map[string]any)
	if !ok {
		return 0, false
	}
	c, _ := e["code"].(float64)
	return c, true
}

func TestInitializeAndToolsList(t *testing.T) {
	r := drive(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	)
	// notifications/initialized produces no response: 2 frames, not 3.
	if len(r) != 2 {
		t.Fatalf("got %d responses, want 2 (notification must be silent): %v", len(r), r)
	}
	si := r[0]["result"].(map[string]any)["serverInfo"].(map[string]any)
	if si["name"] != "columbo-mcp" {
		t.Errorf("serverInfo.name = %v", si["name"])
	}
	tools := r[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Errorf("tools/list = %v", tools)
	}
}

func TestToolCall(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"echo","arguments":{"x":1}}}`)
	res := r[0]["result"].(map[string]any)
	content := res["content"].([]any)
	if len(content) == 0 || content[0].(map[string]any)["type"] != "text" {
		t.Errorf("tool result content = %v", content)
	}
}

// Every error class Columbo hunts must be answered with a nonzero code, never
// dropped silently.
func TestErrorClasses(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		code  float64
	}{
		{"parse error", `{"jsonrpc":"2.0","id":1,"method":`, -32700},
		{"unknown method", `{"jsonrpc":"2.0","id":2,"method":"no/such"}`, -32601},
		{"unknown tool", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`, -32602},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := drive(t, c.frame)
			if len(r) != 1 {
				t.Fatalf("want exactly one (error) response, got %d", len(r))
			}
			code, ok := errCode(r[0])
			if !ok {
				t.Fatalf("want a JSON-RPC error, got %v", r[0])
			}
			if code != c.code {
				t.Errorf("code = %v, want %v", code, c.code)
			}
			if code == 0 {
				t.Errorf("error code must be nonzero")
			}
		})
	}
}

// The bounded reader must reject an over-limit frame (drain + tooLong), not
// buffer it without bound — the F018 guard.
func TestReadBoundedLine(t *testing.T) {
	// A line longer than max, followed by a normal line.
	in := bufio.NewReader(strings.NewReader(strings.Repeat("A", 50) + "\n" + "ok\n"))
	line, tooLong, err := readBoundedLine(in, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !tooLong {
		t.Errorf("a 50-byte line with max=10 must be flagged tooLong")
	}
	if line != nil {
		t.Errorf("over-limit line should not be returned, got %q", line)
	}
	// The reader recovered to the next frame.
	line2, tooLong2, err := readBoundedLine(in, 10)
	if err != nil || tooLong2 || string(line2) != "ok" {
		t.Errorf("reader should recover to next frame, got %q tooLong=%v err=%v", line2, tooLong2, err)
	}
}
