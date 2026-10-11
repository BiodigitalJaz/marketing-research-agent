// Package search provides web search backends behind one interface, used
// by the `search` collection connector and (in M6) the research agent's
// web_search tool. Every backend issues plain GET requests to an
// admin-configured endpoint through the fetcher's trusted client.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
)

// Backend names.
const (
	SearXNG   = "searxng"
	GooglePSE = "google_pse"
	Brave     = "brave"
)

// Query is a backend-neutral search request.
type Query struct {
	Q          string
	TimeRange  string // "" | day | week | month | year
	Count      int    // desired results (backends may cap)
	Lang       string // e.g. "en"
	Country    string // e.g. "US"
	Safesearch string // off | moderate | strict
}

// Result is one search hit. Snippet is untrusted text.
type Result struct {
	URL         string     `json:"url"`
	Title       string     `json:"title"`
	Snippet     string     `json:"snippet"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	Engine      string     `json:"engine,omitempty"`
}

// Backend runs searches.
type Backend interface {
	Name() string
	// Paid reports whether queries cost money and count against the budget.
	Paid() bool
	Search(ctx context.Context, q Query) ([]Result, error)
}

// Getter is the subset of fetch.Fetcher backends need.
type Getter interface {
	Trusted(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error)
}

var _ Getter = (*fetch.Fetcher)(nil)

// TimeRangeSince picks the narrowest range covering since.
func TimeRangeSince(since time.Time, now time.Time) string {
	if since.IsZero() {
		return ""
	}
	switch d := now.Sub(since); {
	case d <= 24*time.Hour:
		return "day"
	case d <= 7*24*time.Hour:
		return "week"
	case d <= 31*24*time.Hour:
		return "month"
	default:
		return "year"
	}
}

// ---- SearXNG ----

type searxng struct {
	base       string
	categories string
	get        Getter
}

// NewSearXNG queries a self-hosted SearXNG JSON API at base (e.g.
// http://searxng:8080). categories defaults to general,science,it,news.
func NewSearXNG(base, categories string, get Getter) Backend {
	if categories == "" {
		categories = "general,science,it,news"
	}
	return &searxng{base: strings.TrimRight(base, "/"), categories: categories, get: get}
}

func (b *searxng) Name() string { return SearXNG }
func (b *searxng) Paid() bool   { return false }

func (b *searxng) Search(ctx context.Context, q Query) ([]Result, error) {
	// No time_range: SearXNG drops engines without time-range support, and
	// the permissive engines (Stack Exchange, arXiv, Crossref...) have none,
	// so any range returns nothing. Callers filter by PublishedAt instead.
	v := url.Values{"q": {q.Q}, "format": {"json"}, "categories": {b.categories}}
	if q.Lang != "" {
		v.Set("language", q.Lang)
	}
	switch q.Safesearch {
	case "moderate":
		v.Set("safesearch", "1")
	case "strict":
		v.Set("safesearch", "2")
	case "off":
		v.Set("safesearch", "0")
	}
	// X-Real-IP: SearXNG's bot detection logs an error per request without
	// it, even with the limiter off; we are an internal client.
	body, err := b.get.Trusted(ctx, b.base+"/search?"+v.Encode(), map[string]string{"Accept": "application/json", "X-Real-IP": "127.0.0.1"})
	if err != nil {
		return nil, fmt.Errorf("searxng: %w", err)
	}
	var resp struct {
		Results []struct {
			URL           string `json:"url"`
			Title         string `json:"title"`
			Content       string `json:"content"`
			Engine        string `json:"engine"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("searxng: decode: %w", err)
	}
	out := make([]Result, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, Result{URL: r.URL, Title: r.Title, Snippet: r.Content, Engine: r.Engine, PublishedAt: parseTime(r.PublishedDate)})
		if q.Count > 0 && len(out) >= q.Count {
			break
		}
	}
	return out, nil
}

// ---- Google Programmable Search (Custom Search JSON API) ----

type googlePSE struct {
	key, cx string
	get     Getter
	base    string
}

// NewGooglePSE uses the Custom Search JSON API (free daily quota, then paid).
func NewGooglePSE(apiKey, cx string, get Getter) Backend {
	return &googlePSE{key: apiKey, cx: cx, get: get, base: "https://www.googleapis.com/customsearch/v1"}
}

func (b *googlePSE) Name() string { return GooglePSE }
func (b *googlePSE) Paid() bool   { return true }

func (b *googlePSE) Search(ctx context.Context, q Query) ([]Result, error) {
	if b.key == "" || b.cx == "" {
		return nil, fmt.Errorf("google_pse: api key and cx are required")
	}
	num := q.Count
	if num <= 0 || num > 10 {
		num = 10
	}
	v := url.Values{"key": {b.key}, "cx": {b.cx}, "q": {q.Q}, "num": {strconv.Itoa(num)}}
	if dr := map[string]string{"day": "d1", "week": "w1", "month": "m1", "year": "y1"}[q.TimeRange]; dr != "" {
		v.Set("dateRestrict", dr)
	}
	if q.Lang != "" {
		v.Set("lr", "lang_"+q.Lang)
	}
	if q.Country != "" {
		v.Set("gl", strings.ToLower(q.Country))
	}
	if q.Safesearch == "strict" || q.Safesearch == "moderate" {
		v.Set("safe", "active")
	}
	body, err := b.get.Trusted(ctx, b.base+"?"+v.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("google_pse: %w", redact(err, b.key))
	}
	var resp struct {
		Items []struct {
			Link    string `json:"link"`
			Title   string `json:"title"`
			Snippet string `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("google_pse: decode: %w", err)
	}
	out := make([]Result, 0, len(resp.Items))
	for _, it := range resp.Items {
		out = append(out, Result{URL: it.Link, Title: it.Title, Snippet: it.Snippet, Engine: GooglePSE})
	}
	return out, nil
}

// ---- Brave Search API ----

type brave struct {
	key  string
	get  Getter
	base string
}

// NewBrave uses the Brave Search web endpoint (paid).
func NewBrave(apiKey string, get Getter) Backend {
	return &brave{key: apiKey, get: get, base: "https://api.search.brave.com/res/v1/web/search"}
}

func (b *brave) Name() string { return Brave }
func (b *brave) Paid() bool   { return true }

func (b *brave) Search(ctx context.Context, q Query) ([]Result, error) {
	if b.key == "" {
		return nil, fmt.Errorf("brave: api key is required")
	}
	count := q.Count
	if count <= 0 || count > 20 {
		count = 20
	}
	v := url.Values{"q": {q.Q}, "count": {strconv.Itoa(count)}}
	if f := map[string]string{"day": "pd", "week": "pw", "month": "pm", "year": "py"}[q.TimeRange]; f != "" {
		v.Set("freshness", f)
	}
	if q.Country != "" {
		v.Set("country", q.Country)
	}
	if q.Lang != "" {
		v.Set("search_lang", q.Lang)
	}
	if q.Safesearch != "" {
		v.Set("safesearch", q.Safesearch)
	}
	body, err := b.get.Trusted(ctx, b.base+"?"+v.Encode(), map[string]string{"Accept": "application/json", "X-Subscription-Token": b.key})
	if err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	var resp struct {
		Web struct {
			Results []struct {
				URL         string `json:"url"`
				Title       string `json:"title"`
				Description string `json:"description"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("brave: decode: %w", err)
	}
	out := make([]Result, 0, len(resp.Web.Results))
	for _, r := range resp.Web.Results {
		out = append(out, Result{URL: r.URL, Title: r.Title, Snippet: r.Description, Engine: Brave, PublishedAt: parseTime(r.PageAge)})
	}
	return out, nil
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// redact removes a secret from an error message (URLs may carry API keys).
func redact(err error, secret string) error {
	if secret == "" || err == nil {
		return err
	}
	return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), secret, "[redacted]"))
}

// Settings is workspace_settings.search. The Brave key keeps its original
// field names (api_key_secret_id / api_key_env) for compatibility.
type Settings struct {
	Backend              string     `json:"backend,omitempty"` // searxng | google_pse | brave | "" (none)
	SearXNGURL           string     `json:"searxng_url,omitempty"`
	SearXNGCategories    string     `json:"searxng_categories,omitempty"`
	GooglePSECX          string     `json:"google_pse_cx,omitempty"`
	GooglePSEKeySecretID *uuid.UUID `json:"google_pse_key_secret_id,omitempty"`
	GooglePSEKeyEnv      string     `json:"google_pse_key_env,omitempty"`
	APIKeySecretID       *uuid.UUID `json:"api_key_secret_id,omitempty"` // Brave
	APIKeyEnv            string     `json:"api_key_env,omitempty"`       // Brave
	Country              string     `json:"country,omitempty"`
	SearchLang           string     `json:"search_lang,omitempty"`
	Safesearch           string     `json:"safesearch,omitempty"`
	MonthlyBudget        int        `json:"monthly_budget,omitempty"` // paid backends; 0 = no limit
}
