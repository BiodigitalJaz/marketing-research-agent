package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
)

func TestSearXNG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/search" || q.Get("format") != "json" || q.Get("q") != `"AI planning" bot` ||
			q.Has("time_range") || q.Get("categories") != "general,science,it,news" {
			t.Errorf("request = %s %s", r.Method, r.URL)
		}
		_, _ = w.Write([]byte(`{"results":[
			{"url":"https://stackoverflow.com/q/1","title":"Bot question","content":"How safe is an <b>AI planning bot</b>?","engine":"stackoverflow","publishedDate":"2026-10-03T10:00:00"},
			{"url":"https://arxiv.org/abs/2","title":"Paper","content":"abstract","engine":"arxiv","publishedDate":null}]}`))
	}))
	defer srv.Close()
	b := NewSearXNG(srv.URL+"/", "", fetch.New(fetch.Options{AllowPrivate: true}))
	res, err := b.Search(context.Background(), Query{Q: `"AI planning" bot`, TimeRange: "day"})
	if err != nil || len(res) != 2 || res[0].Engine != "stackoverflow" || res[0].PublishedAt == nil || res[1].PublishedAt != nil || b.Paid() {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestGooglePSE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("key") != "k" || q.Get("cx") != "cx1" || q.Get("dateRestrict") != "w1" || q.Get("num") != "10" {
			t.Errorf("query = %v", q)
		}
		_, _ = w.Write([]byte(`{"items":[{"link":"https://example.com/a","title":"A","snippet":"s"}]}`))
	}))
	defer srv.Close()
	b := NewGooglePSE("k", "cx1", fetch.New(fetch.Options{AllowPrivate: true})).(*googlePSE)
	b.base = srv.URL
	res, err := b.Search(context.Background(), Query{Q: "x", TimeRange: "week", Count: 50})
	if err != nil || len(res) != 1 || res[0].URL != "https://example.com/a" || !b.Paid() {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestBraveSendsKeyAsHeaderOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "secret-token" || strings.Contains(r.URL.RawQuery, "secret-token") || r.URL.Query().Get("freshness") != "pm" {
			t.Errorf("bad request %s headers=%v", r.URL, r.Header)
		}
		_, _ = w.Write([]byte(`{"web":{"results":[{"url":"https://e.com","title":"T","description":"D","page_age":"2026-09-30T00:00:00"}]}}`))
	}))
	defer srv.Close()
	b := NewBrave("secret-token", fetch.New(fetch.Options{AllowPrivate: true})).(*brave)
	b.base = srv.URL
	res, err := b.Search(context.Background(), Query{Q: "x", TimeRange: "month"})
	if err != nil || len(res) != 1 || res[0].PublishedAt == nil {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestTimeRangeSince(t *testing.T) {
	now := time.Now()
	for d, want := range map[time.Duration]string{time.Hour: "day", 72 * time.Hour: "week", 20 * 24 * time.Hour: "month", 100 * 24 * time.Hour: "year"} {
		if got := TimeRangeSince(now.Add(-d), now); got != want {
			t.Errorf("%v -> %s, want %s", d, got, want)
		}
	}
	if TimeRangeSince(time.Time{}, now) != "" {
		t.Error("zero since")
	}
}

func TestRedact(t *testing.T) {
	err := redact(errString("GET https://x/?key=abc123 failed"), "abc123")
	if strings.Contains(err.Error(), "abc123") {
		t.Fatal("key not redacted")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
