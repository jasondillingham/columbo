package ollama

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// All suite tests use a local httptest fake — none reach a real Ollama host.

func TestGenerate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req generateReq
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "qwen2.5-coder:7b" || req.Stream {
			t.Errorf("bad request: %+v", req)
		}
		io.WriteString(w, `{"response":"[{\"query\":null}]"}`)
	}))
	defer srv.Close()

	got, err := New(srv.URL, time.Second).Generate("qwen2.5-coder:7b", "make probes")
	if err != nil {
		t.Fatal(err)
	}
	if got != `[{"query":null}]` {
		t.Errorf("response = %q", got)
	}
}

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"embeddings":[[0.1,0.2,0.3]]}`)
	}))
	defer srv.Close()

	v, err := New(srv.URL, time.Second).Embed("mxbai-embed-large", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 || v[0] != 0.1 {
		t.Errorf("vector = %v", v)
	}
}

func TestDisabledClient(t *testing.T) {
	c := New("", time.Second)
	if c.Enabled() {
		t.Error("empty host should be disabled")
	}
	if _, err := c.Generate("m", "p"); err == nil {
		t.Error("disabled client should error (caller fails open)")
	}
}

func TestEmptyEmbeddingErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"embeddings":[]}`)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, time.Second).Embed("m", "x"); err == nil {
		t.Error("empty embeddings should error")
	}
}
