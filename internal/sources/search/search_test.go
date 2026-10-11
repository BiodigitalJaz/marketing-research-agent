package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	searchpkg "github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/match"
)

type fakeBackend struct {
	paid    bool
	queries []searchpkg.Query
}

func (f *fakeBackend) Name() string { return "fake" }
func (f *fakeBackend) Paid() bool   { return f.paid }
func (f *fakeBackend) Search(_ context.Context, q searchpkg.Query) ([]searchpkg.Result, error) {
	f.queries = append(f.queries, q)
	recent := time.Now().Add(-2 * time.Hour)
	old := time.Now().Add(-90 * 24 * time.Hour)
	return []searchpkg.Result{
		{URL: "https://stackoverflow.com/q/1", Title: "Trello API order question", Snippet: "How to place a limit order", PublishedAt: &recent, Engine: "stackoverflow"},
		{URL: "https://spam.example/telegram", Title: "Join telegram signals", Snippet: "100x", Engine: "x"},
		{URL: "https://old.example/a", Title: "Old", Snippet: "old", PublishedAt: &old},
		{URL: "https://stackoverflow.com/q/1", Title: "duplicate", Snippet: "dup"},
	}, nil
}

func TestSearchConnector(t *testing.T) {
	be := &fakeBackend{}
	src, _ := sources.Get(Name, sources.Deps{})
	res, err := src.Fetch(context.Background(), sources.Query{
		Config:  []byte(`{"max_queries":2,"site_include":["stackoverflow.com"]}`),
		Terms:   []string{`"Trello API"`, "AI planning bot", "third term"},
		Match:   match.AnyOf([]string{"Trello API", "AI planning bot"}, []string{"telegram"}),
		Exclude: match.AnyOf(nil, []string{"telegram"}),
		Since:   time.Now().Add(-6 * 24 * time.Hour),
		Search:  be,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(be.queries) != 2 || be.queries[0].Q != `"Trello API" (site:stackoverflow.com)` || be.queries[0].TimeRange != "week" {
		t.Fatalf("queries = %+v", be.queries)
	}
	if len(res.Items) != 1 || res.Items[0].Community != "stackoverflow.com" || res.Items[0].ExternalID == res.Items[0].URL || res.Items[0].Metrics["engine"] != "stackoverflow" {
		t.Fatalf("items = %+v (negatives, old, and duplicate URLs must be dropped)", res.Items)
	}
}

func TestBudgetStopsPaidQueries(t *testing.T) {
	be := &fakeBackend{paid: true}
	src, _ := sources.Get(Name, sources.Deps{})
	calls := 0
	_, err := src.Fetch(context.Background(), sources.Query{
		Terms:  []string{"a", "b", "c"},
		Search: be,
		Budget: func(context.Context) error {
			calls++
			if calls > 1 {
				return errors.New("monthly search budget reached")
			}
			return nil
		},
	})
	if err == nil || len(be.queries) != 1 {
		t.Fatalf("budget not enforced: err=%v queries=%d", err, len(be.queries))
	}
}

func TestNoBackend(t *testing.T) {
	src, _ := sources.Get(Name, sources.Deps{})
	if _, err := src.Fetch(context.Background(), sources.Query{Terms: []string{"x"}}); err == nil {
		t.Fatal("expected error without a backend")
	}
}

func TestSearchRequiresMatchUnlessMatchAll(t *testing.T) {
	src, _ := sources.Get(Name, sources.Deps{})
	q := sources.Query{Terms: []string{"x"}, Search: &fakeBackend{}, Match: match.Compile(`"no such phrase"`), Exclude: match.AnyOf(nil, nil)}
	res, _ := src.Fetch(context.Background(), q)
	if len(res.Items) != 0 {
		t.Fatalf("non-matching results kept: %d", len(res.Items))
	}
	q.Config = []byte(`{"match_all":true}`)
	res, _ = src.Fetch(context.Background(), q)
	if len(res.Items) != 3 { // all distinct URLs (no since filter)
		t.Fatalf("match_all should keep backend results, got %d", len(res.Items))
	}
}

func TestRotateTerms(t *testing.T) {
	terms := []string{"a", "b", "c", "d", "e", "f", "g"}
	var state json.RawMessage
	var seen []string
	for run := 0; run < 3; run++ {
		var got []string
		got, state = rotate(terms, 3, state)
		seen = append(seen, strings.Join(got, ""))
	}
	if strings.Join(seen, " ") != "abc def gab" {
		t.Errorf("runs = %q, want every term searched in turn", seen)
	}
	if got, st := rotate([]string{"x", "y"}, 5, json.RawMessage(`{"next":1}`)); strings.Join(got, "") != "xy" || string(st) != `{"next":1}` {
		t.Errorf("few terms: %v %s", got, st)
	}
	if got, _ := rotate(terms, 2, json.RawMessage(`{"next":99}`)); strings.Join(got, "") != "bc" {
		t.Errorf("stale cursor wraps: %v", got)
	}
}
