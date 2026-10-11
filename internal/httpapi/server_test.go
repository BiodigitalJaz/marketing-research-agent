package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func TestRoutes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name       string
		db         Pinger
		path       string
		wantStatus int
		wantField  string // "status" value or error code
	}{
		{"health", fakeDB{}, "/health", 200, "ok"},
		{"api health", fakeDB{}, "/api/v1/health", 200, "ok"},
		{"ready", fakeDB{}, "/ready", 200, "ready"},
		{"api ready", fakeDB{}, "/api/v1/ready", 200, "ready"},
		{"not ready", fakeDB{err: errors.New("down")}, "/ready", 503, "db_unavailable"},
		{"health ignores db", fakeDB{err: errors.New("down")}, "/health", 200, "ok"},
		{"not found", fakeDB{}, "/nope", 404, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewRouter(Deps{Log: log, DB: tt.db})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
				t.Errorf("content-type = %q", ct)
			}
			var body struct {
				Status string `json:"status"`
				Error  struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			got := body.Status
			if got == "" {
				got = body.Error.Code
			}
			if got != tt.wantField {
				t.Errorf("body field = %q, want %q", got, tt.wantField)
			}
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := NewRouter(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: fakeDB{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestRequestID(t *testing.T) {
	h := NewRouter(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: fakeDB{}})
	tests := []struct {
		name, in string
		keep     bool
	}{
		{"generated when absent", "", false},
		{"kept when valid", "abc-123", true},
		{"replaced when invalid", "bad id\n<script>", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/health", nil)
			if tt.in != "" {
				req.Header.Set(RequestIDHeader, tt.in)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get(RequestIDHeader)
			if got == "" {
				t.Fatal("no request id header")
			}
			if (got == tt.in) != tt.keep {
				t.Errorf("request id = %q (input %q, keep=%v)", got, tt.in, tt.keep)
			}
		})
	}
}
