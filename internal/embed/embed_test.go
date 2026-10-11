package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServer returns vectors of length dims whose first element is the input
// index within the batch, in reverse order to exercise index handling.
func fakeServer(t *testing.T, dims int, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
			http.Error(w, "bad route", http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var resp embedResponse
		for i := len(req.Input) - 1; i >= 0; i-- {
			v := make([]float32, dims)
			v[0] = float32(len(req.Input[i]))
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{i, v})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestEmbedBatchesAndOrders(t *testing.T) {
	calls := 0
	srv := fakeServer(t, 4, &calls)
	defer srv.Close()
	e := NewOpenAICompat(srv.URL+"/v1/", "k", "m", 4)

	texts := make([]string, MaxBatch+3)
	for i := range texts {
		texts[i] = strings.Repeat("x", i+1)
	}
	vecs, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 batches", calls)
	}
	for i, v := range vecs {
		if int(v[0]) != i+1 {
			t.Fatalf("vector %d out of order: %v", i, v[0])
		}
	}
}

func TestEmbedErrors(t *testing.T) {
	calls := 0
	srv := fakeServer(t, 3, &calls)
	defer srv.Close()

	if _, err := NewOpenAICompat(srv.URL+"/v1", "k", "m", 4).Embed(context.Background(), []string{"a"}); !errors.Is(err, ErrDims) {
		t.Errorf("want ErrDims, got %v", err)
	}
	if _, err := NewOpenAICompat(srv.URL+"/v1", "wrong", "m", 3).Embed(context.Background(), []string{"a"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("want 401 error, got %v", err)
	}
}
