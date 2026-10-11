// Package hn collects Hacker News stories through the official API
// (hacker-news.firebaseio.com/v0). Keyword selection happens locally.
package hn

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
)

const (
	Name = "hn"
	// APIHost is the official API host (its rate limit is raised in the fetcher).
	APIHost = "hacker-news.firebaseio.com"
)

func init() { sources.Register(Name, func(d sources.Deps) sources.Source { return &connector{d: d} }) }

// Config is sources.config for hn.
type Config struct {
	Lists    []string `json:"lists"`     // newstories (default), askstories, showstories, topstories
	MaxItems int      `json:"max_items"` // new items fetched per run; default 200
	MinScore int      `json:"min_score"`
	BaseURL  string   `json:"base_url,omitempty"` // tests only
}

// State is the cursor: the highest item id already seen.
type State struct {
	LastMaxID int64 `json:"last_max_id"`
}

type connector struct{ d sources.Deps }

func (c *connector) Connector() string { return Name }

var validLists = []string{"newstories", "askstories", "showstories", "topstories", "beststories"}

func parseConfig(raw json.RawMessage) (Config, error) {
	var cfg Config
	if err := sources.DecodeConfig(raw, &cfg); err != nil {
		return cfg, err
	}
	if len(cfg.Lists) == 0 {
		cfg.Lists = []string{"newstories", "askstories"}
	}
	for _, l := range cfg.Lists {
		if !slices.Contains(validLists, l) {
			return cfg, fmt.Errorf("unknown list %q", l)
		}
	}
	if cfg.MaxItems <= 0 || cfg.MaxItems > 1000 {
		cfg.MaxItems = 200
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://" + APIHost + "/v0"
	}
	return cfg, nil
}

func (c *connector) ValidateConfig(raw json.RawMessage) error {
	_, err := parseConfig(raw)
	return err
}

type item struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	By          string `json:"by"`
	Time        int64  `json:"time"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	URL         string `json:"url"`
	Score       int    `json:"score"`
	Descendants int    `json:"descendants"`
	Dead        bool   `json:"dead"`
	Deleted     bool   `json:"deleted"`
}

func (c *connector) Fetch(ctx context.Context, q sources.Query) (sources.Result, error) {
	cfg, err := parseConfig(q.Config)
	if err != nil {
		return sources.Result{}, err
	}
	var st State
	if len(q.State) > 0 {
		_ = json.Unmarshal(q.State, &st)
	}
	seen := map[int64]bool{}
	var ids []int64
	for _, l := range cfg.Lists {
		raw, err := c.d.Fetch.Trusted(ctx, cfg.BaseURL+"/"+l+".json", nil)
		if err != nil {
			return sources.Result{}, fmt.Errorf("list %s: %w", l, err)
		}
		var list []int64
		if err := json.Unmarshal(raw, &list); err != nil {
			return sources.Result{}, fmt.Errorf("decode %s: %w", l, err)
		}
		for _, id := range list {
			if id > st.LastMaxID && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	if len(ids) > cfg.MaxItems {
		ids = ids[len(ids)-cfg.MaxItems:] // newest
	}

	items := make([]*item, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	var mu sync.Mutex
	failures := 0
	for i, id := range ids {
		g.Go(func() error {
			raw, err := c.d.Fetch.Trusted(gctx, cfg.BaseURL+"/item/"+strconv.FormatInt(id, 10)+".json", nil)
			if err != nil {
				mu.Lock()
				failures++
				mu.Unlock()
				return nil // one missing item must not fail the run
			}
			var it item
			if json.Unmarshal(raw, &it) == nil {
				items[i] = &it
			}
			return nil
		})
	}
	_ = g.Wait()
	if len(ids) > 0 && failures == len(ids) {
		return sources.Result{}, fmt.Errorf("all %d item fetches failed", failures)
	}

	newState := st
	var out []sources.Item
	for _, it := range items {
		if it == nil {
			continue
		}
		if it.ID > newState.LastMaxID {
			newState.LastMaxID = it.ID
		}
		if it.Dead || it.Deleted || (it.Type != "story" && it.Type != "job" && it.Type != "poll") || it.Score < cfg.MinScore {
			continue
		}
		discussion := "https://news.ycombinator.com/item?id=" + strconv.FormatInt(it.ID, 10)
		si := sources.Normalize(sources.Item{
			ExternalID:   strconv.FormatInt(it.ID, 10),
			URL:          discussion,
			AuthorHandle: it.By,
			Title:        it.Title,
			Body:         it.Text,
			Community:    "Hacker News",
			PublishedAt:  time.Unix(it.Time, 0).UTC(),
			Metrics:      map[string]any{"score": it.Score, "comments": it.Descendants, "link": it.URL},
		})
		if si.Body == "" {
			si.Body = si.Title // link posts: the title is the content
			if it.URL != "" {
				si.Body += "\n" + it.URL
			}
		}
		if q.Match != nil && !q.Match.Match(si.Title+"\n"+si.Body) {
			continue
		}
		out = append(out, si)
	}
	stateJSON, _ := json.Marshal(newState)
	return sources.Result{Items: out, State: stateJSON}, nil
}
