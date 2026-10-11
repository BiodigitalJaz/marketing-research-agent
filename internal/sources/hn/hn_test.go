package hn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/match"
)

func fixture(t *testing.T, itemCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	items := map[int]string{
		101: `{"id":101,"type":"story","by":"alice","time":1790000000,"title":"Ask HN: Is it safe to let an AI agent trade my Trello account?","text":"<p>Thinking about &quot;agentic planning&quot;.</p>","score":42,"descendants":17}`,
		102: `{"id":102,"type":"story","by":"bob","time":1790000100,"title":"Show HN: A Rust parser","url":"https://example.com/rust","score":5}`,
		103: `{"id":103,"type":"comment","by":"c","time":1790000200,"text":"trello comment"}`,
		104: `{"id":104,"type":"story","by":"d","time":1790000300,"title":"Trello launches an API","url":"https://example.com/rh","score":3,"dead":true}`,
		105: `{"id":105,"type":"story","by":"e","time":1790000400,"title":"Trello MCP server released","url":"https://example.com/mcp","score":12}`,
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("non-GET request %s", r.Method)
		}
		switch r.URL.Path {
		case "/v0/newstories.json":
			_, _ = w.Write([]byte(`[105,104,103,102,101]`))
		case "/v0/askstories.json":
			_, _ = w.Write([]byte(`[101]`))
		default:
			var id int
			if _, err := fmt.Sscanf(r.URL.Path, "/v0/item/%d.json", &id); err != nil {
				http.NotFound(w, r)
				return
			}
			itemCalls.Add(1)
			_, _ = w.Write([]byte(items[id]))
		}
	}))
}

func TestFetchMatchesAndAdvancesCursor(t *testing.T) {
	var calls atomic.Int32
	srv := fixture(t, &calls)
	defer srv.Close()
	src, _ := sources.Get(Name, sources.Deps{Fetch: fetch.New(fetch.Options{AllowPrivate: true, PerHostRate: 1000})})
	cfg := []byte(`{"base_url":"` + srv.URL + `/v0"}`)
	q := sources.Query{Config: cfg, Match: match.Compile("trello OR \"agentic planning\"")}

	res, err := src.Fetch(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range res.Items {
		titles = append(titles, it.Title)
	}
	if len(res.Items) != 2 || !strings.Contains(titles[0], "Ask HN") || titles[1] != "Trello MCP server released" {
		t.Fatalf("items = %q (comment, dead, and non-matching must be skipped)", titles)
	}
	ask := res.Items[0]
	if ask.ExternalID != "101" || ask.URL != "https://news.ycombinator.com/item?id=101" || ask.AuthorHandle != "alice" ||
		ask.Body != `Thinking about "agentic planning".` || ask.Metrics["score"] != 42 {
		t.Fatalf("ask item = %+v", ask)
	}
	var st State
	_ = json.Unmarshal(res.State, &st)
	if st.LastMaxID != 105 || calls.Load() != 5 {
		t.Fatalf("state=%+v calls=%d", st, calls.Load())
	}

	// Second run with the new cursor fetches nothing.
	q.State = res.State
	res2, err := src.Fetch(context.Background(), q)
	if err != nil || len(res2.Items) != 0 || calls.Load() != 5 {
		t.Fatalf("second run: items=%d calls=%d err=%v", len(res2.Items), calls.Load(), err)
	}
}

func TestValidate(t *testing.T) {
	src, _ := sources.Get(Name, sources.Deps{})
	if src.ValidateConfig([]byte(`{"lists":["frontpage"]}`)) == nil {
		t.Fatal("unknown list must be invalid")
	}
	if src.ValidateConfig([]byte(`{}`)) != nil {
		t.Fatal("empty config should be valid")
	}
}
