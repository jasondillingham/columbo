// Package ollama is a small runtime client for an Ollama HTTP host: text
// generation (adversarial probe inputs) and embeddings (finding dedup). It is
// an AUGMENTATION — every caller must fail OPEN: if the host is empty, down, or
// slow past the timeout, fall back to the deterministic path (fixed probes /
// structural dedup) and never block or fail the audit.
//
// The default host is a generic localhost (no site-specific IP belongs in
// source); point it at a real host with --ollama / COLUMBO_OLLAMA.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultHost is intentionally generic. Real hosts come from config/flag.
const DefaultHost = "http://localhost:11434"

// Client talks to one Ollama host. A zero/empty host means disabled.
type Client struct {
	host string
	http *http.Client
}

// New returns a client for host (empty -> disabled, Enabled() reports false).
func New(host string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{host: host, http: &http.Client{Timeout: timeout}}
}

// Enabled reports whether a host is configured.
func (c *Client) Enabled() bool { return c.host != "" }

func (c *Client) post(path string, req, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("ollama: no host configured")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.host+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type generateReq struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type generateResp struct {
	Response string `json:"response"`
}

// Generate runs a non-streaming completion and returns the response text.
func (c *Client) Generate(model, prompt string) (string, error) {
	var out generateResp
	if err := c.post("/api/generate", generateReq{Model: model, Prompt: prompt, Stream: false}, &out); err != nil {
		return "", err
	}
	return out.Response, nil
}

type embedReq struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResp struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed returns the embedding vector for text.
func (c *Client) Embed(model, text string) ([]float32, error) {
	var out embedResp
	if err := c.post("/api/embed", embedReq{Model: model, Input: text}, &out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) == 0 || len(out.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("ollama: empty embedding for %q", text)
	}
	return out.Embeddings[0], nil
}
