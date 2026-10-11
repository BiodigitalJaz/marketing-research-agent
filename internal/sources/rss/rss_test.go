package rss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/fetch"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/sources/match"
)

// alertsFeed mirrors a Google Alerts Atom feed.
const alertsFeed = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Google Alert - AI planning bot</title>
  <entry>
    <id>tag:google.com,2013:googlealerts/feed:111</id>
    <title type="html">Is an &lt;b&gt;AI planning bot&lt;/b&gt; safe for a small account?</title>
    <link href="https://www.google.com/url?rct=j&amp;sa=t&amp;url=https://example.com/forum/ai-bot-safety&amp;ct=ga&amp;usg=abc"/>
    <published>2026-10-04T08:00:00Z</published>
    <content type="html">Users ask whether an &lt;b&gt;AI planning bot&lt;/b&gt; can be trusted with a Trello account.</content>
  </entry>
  <entry>
    <id>tag:google.com,2013:googlealerts/feed:222</id>
    <title type="html">Hiring &lt;b&gt;AI planning bot&lt;/b&gt; promises 100x</title>
    <link href="https://www.google.com/url?url=https://spam.example/x"/>
    <published>2026-10-04T07:00:00Z</published>
    <content type="html">Join our telegram signal group.</content>
  </entry>
  <entry>
    <id>tag:google.com,2013:googlealerts/feed:333</id>
    <title type="html">Old news about an AI planning bot</title>
    <link href="https://www.google.com/url?url=https://example.com/old"/>
    <published>2026-09-01T00:00:00Z</published>
    <content type="html">Old.</content>
  </entry>
</feed>`

func TestGoogleAlertsFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(alertsFeed))
	}))
	defer srv.Close()
	src, _ := sources.Get(Name, sources.Deps{Fetch: fetch.New(fetch.Options{AllowPrivate: true, PerHostRate: 100})})
	res, err := src.Fetch(context.Background(), sources.Query{
		// The trailing tab is how a copied URL often arrives; it must still work.
		Config:  []byte(`{"feed_url":"` + srv.URL + `/alerts/feeds/1\t","match_all":true}`),
		Exclude: match.AnyOf(nil, []string{"telegram", "hiring"}),
		Since:   time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("want 1 item (negatives + since applied), got %+v", res.Items)
	}
	it := res.Items[0]
	if it.URL != "https://example.com/forum/ai-bot-safety" || it.Title != "Is an AI planning bot safe for a small account?" ||
		strings.Contains(it.Body, "<b>") || it.Community != "Google Alert - AI planning bot" || it.PublishedAt.IsZero() {
		t.Fatalf("item = %+v", it)
	}
}

func TestMatchingAndFullText(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><article><p>` + strings.Repeat("The risk engine refuses unsafe orders. ", 30) + `</p></article></body></html>`))
	}))
	defer page.Close()
	feed := `<rss version="2.0"><channel><title>Forum</title>
	  <item><guid>q1</guid><title>Trello stop loss question</title><link>` + page.URL + `/q1</link><description>Short.</description></item>
	  <item><guid>q2</guid><title>Unrelated tax question</title><link>` + page.URL + `/q2</link><description>Short.</description></item>
	</channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(feed)) }))
	defer srv.Close()
	src, _ := sources.Get(Name, sources.Deps{Fetch: fetch.New(fetch.Options{AllowPrivate: true, PerHostRate: 100})})
	res, err := src.Fetch(context.Background(), sources.Query{
		Config: []byte(`{"feed_url":"` + srv.URL + `","full_text":true}`),
		Match:  match.Compile("trello"),
	})
	if err != nil || len(res.Items) != 1 || res.Items[0].ExternalID != "q1" || !strings.Contains(res.Items[0].Body, "risk engine refuses") {
		t.Fatalf("res=%+v err=%v", res.Items, err)
	}
}

func TestValidateAndUnwrap(t *testing.T) {
	src, _ := sources.Get(Name, sources.Deps{})
	for _, bad := range []string{`{}`, `{"feed_url":"ftp://x"}`, `{"feed_url":"https://x","bogus":1}`} {
		if src.ValidateConfig([]byte(bad)) == nil {
			t.Errorf("config %s should be invalid", bad)
		}
	}
	if UnwrapGoogleLink("https://www.google.com/url?q=https://a.com/b&sa=U") != "https://a.com/b" ||
		UnwrapGoogleLink("https://a.com/x") != "https://a.com/x" ||
		UnwrapGoogleLink("https://www.google.com/url?url=javascript:alert(1)") != "https://www.google.com/url?url=javascript:alert(1)" {
		t.Fatal("unwrap")
	}
}
