// Package embed provides text embedding providers. All adapters speak the
// OpenAI-compatible /v1/embeddings API (OpenAI, Ollama, LM Studio, vLLM,
// llama.cpp, TEI), so one implementation covers hosted and local models.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MaxBatch is the largest number of texts sent in one request.
const MaxBatch = 64

// Embedder turns texts into vectors.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
	Dims() int
}

// OpenAICompat calls POST {BaseURL}/embeddings.
type OpenAICompat struct {
	BaseURL string // e.g. http://localhost:11434/v1
	APIKey  string // optional; sent as a bearer token when set
	model   string
	dims    int
	client  *http.Client
}

// NewOpenAICompat returns an embedder for the given endpoint and model; dims
// 0 skips the dimension check (used to detect a model's dimensions). dims
// is the expected vector length; responses of any other length are errors.
func NewOpenAICompat(baseURL, apiKey, model string, dims int) *OpenAICompat {
	return &OpenAICompat{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		model:   model,
		dims:    dims,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *OpenAICompat) Model() string { return e.model }
func (e *OpenAICompat) Dims() int     { return e.dims }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// ErrNotConfigured means no embedder is configured for the workspace.
// Jobs that need one are cancelled rather than retried.
var ErrNotConfigured = errors.New("embeddings provider not configured")

// ErrDims is returned when the endpoint's vectors do not match Dims().
var ErrDims = errors.New("embedding dimension mismatch")

// Embed embeds texts in batches of MaxBatch, preserving order.
func (e *OpenAICompat) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += MaxBatch {
		end := min(start+MaxBatch, len(texts))
		vecs, err := e.batch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (e *OpenAICompat) batch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embed request: status %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if len(er.Data) != len(texts) {
		return nil, fmt.Errorf("embed response: got %d vectors for %d inputs", len(er.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range er.Data {
		if d.Index < 0 || d.Index >= len(texts) || vecs[d.Index] != nil {
			return nil, fmt.Errorf("embed response: bad index %d", d.Index)
		}
		if e.dims > 0 && len(d.Embedding) != e.dims {
			return nil, fmt.Errorf("%w: model %s returned %d, want %d", ErrDims, e.model, len(d.Embedding), e.dims)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

// ProviderConfig is the subset of provider settings an embedder needs.
type ProviderConfig struct {
	BaseURL string
	APIKey  string
	Kind    string
}

// New builds an embedder for an OpenAI-compatible provider. Anthropic has no
// embeddings API, so it is rejected.
func New(p ProviderConfig, model string, dims int) (Embedder, error) {
	if p.Kind != "openai_compat" {
		return nil, fmt.Errorf("embeddings provider kind %q has no embeddings API; use an openai_compat provider", p.Kind)
	}
	return NewOpenAICompat(p.BaseURL, p.APIKey, model, dims), nil
}
