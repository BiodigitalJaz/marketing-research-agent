// Package search is the collection connector that runs monitor queries
// through the workspace's search backend (SearXNG by default).
package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	searchpkg "github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
)

const Name = "search"

func init() { sources.Register(Name, func(d sources.Deps) sources.Source { return &connector{d: d} }) }

// Config is sources.config for search.
type Config struct {
	MaxQueries int  `json:"max_queries"` // per run; default 5
	MaxResults int  `json:"max_results"` // per query; default 10
	FullText   bool `json:"full_text"`   // fetch result pages through the polite fetcher
	MaxFetch   int  `json:"max_fetch"`   // pages fetched per run; default 10
	// SiteInclude restricts results to these hosts (appended as site: filters).
	SiteInclude []string `json:"site_include"`
	// MatchAll keeps every result the backend returns. By default results
	// must also match the monitor query (or tracked keywords) in their title
	// or snippet: search engines rank loosely, monitoring needs precision.
	MatchAll bool `json:"match_all"`
}

type connector struct{ d sources.Deps }

func (c *connector) Connector() string { return Name }

func parseConfig(raw json.RawMessage) (Config, error) {
	var cfg Config
	if err := sources.DecodeConfig(raw, &cfg); err != nil {
		return cfg, err
	}
	if cfg.MaxQueries <= 0 || cfg.MaxQueries > 25 {
		cfg.MaxQueries = 5
	}
	if cfg.MaxResults <= 0 || cfg.MaxResults > 50 {
		cfg.MaxResults = 10
	}
	if cfg.MaxFetch < 0 || cfg.MaxFetch > 50 {
		cfg.MaxFetch = 10
	}
	if cfg.MaxFetch == 0 && cfg.FullText {
		cfg.MaxFetch = 10
	}
	for _, s := range cfg.SiteInclude {
		if strings.ContainsAny(s, " /:") || s == "" {
			return cfg, fmt.Errorf("site_include entries must be bare hostnames, got %q", s)
		}
	}
	return cfg, nil
}

func (c *connector) ValidateConfig(raw json.RawMessage) error {
	_, err := parseConfig(raw)
	return err
}

func (c *connector) Fetch(ctx context.Context, q sources.Query) (sources.Result, error) {
	cfg, err := parseConfig(q.Config)
	if err != nil {
		return sources.Result{}, err
	}
	if q.Search == nil {
		return sources.Result{}, fmt.Errorf("no search backend configured for this workspace")
	}
	terms, state := rotate(q.Terms, cfg.MaxQueries, q.State)
	timeRange := searchpkg.TimeRangeSince(q.Since, c.d.Now())
	seen := map[string]bool{}
	var out []sources.Item
	fetched := 0
	for _, term := range terms {
		query := term
		if len(cfg.SiteInclude) > 0 {
			sites := make([]string, len(cfg.SiteInclude))
			for i, s := range cfg.SiteInclude {
				sites[i] = "site:" + s
			}
			query += " (" + strings.Join(sites, " OR ") + ")"
		}
		if q.Search.Paid() && q.Budget != nil {
			if err := q.Budget(ctx); err != nil {
				return sources.Result{Items: out, State: q.State}, err
			}
		}
		results, err := q.Search.Search(ctx, searchpkg.Query{Q: query, TimeRange: timeRange, Count: cfg.MaxResults})
		if err != nil {
			return sources.Result{Items: out, State: q.State}, fmt.Errorf("search %q: %w", term, err)
		}
		for _, r := range results {
			if r.URL == "" || seen[r.URL] {
				continue
			}
			seen[r.URL] = true
			it := sources.Normalize(sources.Item{
				ExternalID: urlID(r.URL), URL: r.URL, Title: r.Title, Body: r.Snippet,
				Community: host(r.URL), Metrics: map[string]any{"engine": r.Engine, "query": term},
			})
			if r.PublishedAt != nil {
				it.PublishedAt = *r.PublishedAt
				if !q.Since.IsZero() && it.PublishedAt.Before(q.Since) {
					continue
				}
			}
			matcher := q.Match
			if cfg.MatchAll || matcher == nil {
				matcher = q.Exclude
			}
			if matcher != nil && !matcher.Match(it.Title+"\n"+it.Body) {
				continue
			}
			if cfg.FullText && fetched < cfg.MaxFetch {
				fetched++
				if page, err := c.d.Fetch.Article(ctx, r.URL); err == nil && len(page.Text) > len(it.Body) {
					it.Body = page.Text
					if page.PublishedAt != nil && it.PublishedAt.IsZero() {
						it.PublishedAt = *page.PublishedAt
					}
					it = sources.Normalize(it)
				}
			}
			out = append(out, it)
		}
	}
	return sources.Result{Items: out, State: state}, nil
}

// State is the search cursor: where the next run's queries start in the
// term list, so every tracked keyword gets searched in turn.
type State struct {
	Next int `json:"next"`
}

// rotate picks up to n terms starting at the cursor (wrapping around) and
// returns the advanced cursor. With n or fewer terms, all run every time.
func rotate(terms []string, n int, raw json.RawMessage) ([]string, json.RawMessage) {
	if len(terms) <= n {
		return terms, raw
	}
	var st State
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &st)
	}
	start := st.Next % len(terms)
	if start < 0 {
		start = 0
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, terms[(start+i)%len(terms)])
	}
	next, _ := json.Marshal(State{Next: (start + n) % len(terms)})
	return out, next
}

func urlID(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:16])
}

func host(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(p.Hostname(), "www.")
}
