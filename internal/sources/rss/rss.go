// Package rss collects RSS/Atom feeds, including Google Alerts feeds
// ("Deliver to: RSS feed"), whose links are unwrapped to the real URL.
package rss

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/mmcdole/gofeed"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
)

const Name = "rss"

func init() { sources.Register(Name, func(d sources.Deps) sources.Source { return &connector{d: d} }) }

// Config is sources.config for rss.
type Config struct {
	FeedURL  string `json:"feed_url"`
	FullText bool   `json:"full_text"` // fetch the linked page when the feed body is short
	MaxItems int    `json:"max_items"` // per run; default 50
	// MatchAll skips keyword matching (for feeds that are already topical,
	// such as a Google Alert or a Stack Exchange tag feed).
	MatchAll bool `json:"match_all"`
}

type connector struct{ d sources.Deps }

func (c *connector) Connector() string { return Name }

func parseConfig(raw json.RawMessage) (Config, error) {
	var cfg Config
	if err := sources.DecodeConfig(raw, &cfg); err != nil {
		return cfg, err
	}
	cfg.FeedURL = strings.TrimSpace(cfg.FeedURL)
	u, err := url.Parse(cfg.FeedURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return cfg, errors.New("feed_url must be an absolute http(s) URL")
	}
	if cfg.MaxItems <= 0 || cfg.MaxItems > 500 {
		cfg.MaxItems = 50
	}
	return cfg, nil
}

func (c *connector) ValidateConfig(raw json.RawMessage) error {
	_, err := parseConfig(raw)
	return err
}

// shortBody is the length under which the linked page is fetched (FullText).
const shortBody = 400

func (c *connector) Fetch(ctx context.Context, q sources.Query) (sources.Result, error) {
	cfg, err := parseConfig(q.Config)
	if err != nil {
		return sources.Result{}, err
	}
	raw, err := c.d.Fetch.Trusted(ctx, cfg.FeedURL, map[string]string{"Accept": "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.5"})
	if err != nil {
		return sources.Result{}, fmt.Errorf("fetch feed: %w", err)
	}
	feed, err := gofeed.NewParser().ParseString(string(raw))
	if err != nil {
		return sources.Result{}, fmt.Errorf("parse feed: %w", err)
	}
	var out []sources.Item
	for _, fi := range feed.Items {
		if len(out) >= cfg.MaxItems {
			break
		}
		it := sources.Item{
			ExternalID: fi.GUID,
			URL:        UnwrapGoogleLink(fi.Link),
			Title:      fi.Title,
			Body:       fi.Content,
			Community:  feed.Title,
		}
		if it.Body == "" {
			it.Body = fi.Description
		}
		if it.ExternalID == "" {
			it.ExternalID = it.URL
		}
		if it.ExternalID == "" {
			continue
		}
		switch {
		case fi.PublishedParsed != nil:
			it.PublishedAt = *fi.PublishedParsed
		case fi.UpdatedParsed != nil:
			it.PublishedAt = *fi.UpdatedParsed
		}
		if !q.Since.IsZero() && !it.PublishedAt.IsZero() && it.PublishedAt.Before(q.Since) {
			continue
		}
		if len(fi.Authors) > 0 && fi.Authors[0] != nil {
			it.AuthorHandle = fi.Authors[0].Name
		}
		it = sources.Normalize(it)
		matcher := q.Match
		if cfg.MatchAll {
			matcher = q.Exclude
		}
		if matcher != nil && !matcher.Match(it.Title+"\n"+it.Body) {
			continue
		}
		if cfg.FullText && len(it.Body) < shortBody && it.URL != "" {
			if page, err := c.d.Fetch.Article(ctx, it.URL); err == nil && len(page.Text) > len(it.Body) {
				it.Body = page.Text
				it = sources.Normalize(it)
			}
		}
		out = append(out, it)
	}
	return sources.Result{Items: out, State: q.State}, nil
}

// UnwrapGoogleLink returns the target of a Google redirect link
// (https://www.google.com/url?...&url=TARGET), as used by Google Alerts
// feeds; other links are returned unchanged.
func UnwrapGoogleLink(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	host := strings.TrimPrefix(u.Hostname(), "www.")
	if (host == "google.com" || strings.HasPrefix(host, "google.")) && u.Path == "/url" {
		for _, k := range []string{"url", "q"} {
			if t := u.Query().Get(k); strings.HasPrefix(t, "http://") || strings.HasPrefix(t, "https://") {
				return t
			}
		}
	}
	return link
}
