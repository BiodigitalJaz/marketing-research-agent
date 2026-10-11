package fetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.0.0.1": false, "192.168.0.10": false, "172.16.5.4": false,
		"169.254.169.254": false, "100.64.1.1": false, "::1": false, "fe80::1": false, "fd00::1": false, "0.0.0.0": false,
	} {
		if got := PublicIP(net.ParseIP(ip)); got != want {
			t.Errorf("PublicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestSSRFGuardBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<p>secret</p>")) }))
	defer srv.Close()
	f := New(Options{})
	_, err := f.Article(context.Background(), srv.URL+"/admin")
	if err == nil || !strings.Contains(err.Error(), "refusing") && !errors.Is(err, ErrDisallowed) {
		t.Fatalf("expected SSRF refusal, got %v", err)
	}
	// Trusted (admin-configured) URLs may target the LAN/loopback.
	if _, err := f.Trusted(context.Background(), srv.URL, nil); err != nil {
		t.Fatalf("trusted fetch: %v", err)
	}
}

func site(t *testing.T, robots string, robotsStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.WriteHeader(robotsStatus)
			_, _ = w.Write([]byte(robots))
		case "/data.json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			if !strings.Contains(r.Header.Get("User-Agent"), RobotsAgent) {
				t.Errorf("missing user agent")
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<html><head><title>Risk engines</title><script>evil()</script></head><body><nav>menu</nav>
				<article><h1>Risk engines</h1><p>A risk engine refuses orders that break limits. It does not advise.</p>
				<p>Approval mode means a person clicks before any order is sent to the vendor.</p></article></body></html>`))
		}
	}))
}

func TestRobotsAndExtraction(t *testing.T) {
	ctx := context.Background()
	f := New(Options{AllowPrivate: true, PerHostRate: 1000})

	ok := site(t, "User-agent: *\nDisallow: /private\n", 200)
	defer ok.Close()
	p, err := f.Article(ctx, ok.URL+"/post")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Text, "risk engine refuses orders") || strings.Contains(p.Text, "evil") {
		t.Fatalf("extraction: %q", p.Text)
	}
	if _, err := f.Article(ctx, ok.URL+"/private/x"); !errors.Is(err, ErrDisallowed) {
		t.Fatalf("robots disallow not honored: %v", err)
	}
	if _, err := f.Article(ctx, ok.URL+"/data.json"); !errors.Is(err, ErrNotHTML) {
		t.Fatalf("non-HTML should be refused: %v", err)
	}

	agent := site(t, "User-agent: "+RobotsAgent+"\nDisallow: /\n", 200)
	defer agent.Close()
	if _, err := f.Article(ctx, agent.URL+"/post"); !errors.Is(err, ErrDisallowed) {
		t.Fatalf("agent-specific disallow not honored: %v", err)
	}

	missing := site(t, "", 404) // 4xx robots = no restrictions
	defer missing.Close()
	if _, err := f.Article(ctx, missing.URL+"/post"); err != nil {
		t.Fatalf("404 robots should allow: %v", err)
	}

	down := site(t, "", 503) // 5xx robots = assume disallow
	defer down.Close()
	if _, err := f.Article(ctx, down.URL+"/post"); !errors.Is(err, ErrDisallowed) {
		t.Fatalf("5xx robots should disallow: %v", err)
	}

	if _, err := f.Article(ctx, "file:///etc/passwd"); !errors.Is(err, ErrScheme) {
		t.Fatalf("file scheme: %v", err)
	}
}

func TestHTMLToText(t *testing.T) {
	got := HTMLToText(`<p>Hello <b>world</b> &amp; friends</p><script>x()</script><ul><li>one</li><li>two</li></ul>`)
	want := "Hello world & friends\n\none\n\ntwo"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if HTMLToText("  plain   text  ") != "plain text" {
		t.Fatal("plain text")
	}
}

func TestImage(t *testing.T) {
	ctx := context.Background()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private\n"))
		case "/chart.png", "/private/chart.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(png)
		case "/fake.png": // claims to be an image, is HTML
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("<html><body>nope</body></html>"))
		case "/big.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(append(png, make([]byte, 4096)...))
		default:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		}
	}))
	defer srv.Close()
	f := New(Options{AllowPrivate: true, PerHostRate: 1000, MaxBytes: 1024})
	data, mt, err := f.Image(ctx, srv.URL+"/chart.png")
	if err != nil || mt != "image/png" || len(data) != len(png) {
		t.Fatalf("image = %d bytes %q %v", len(data), mt, err)
	}
	for path, want := range map[string]error{"/private/chart.png": ErrDisallowed, "/fake.png": ErrNotImage, "/page": ErrNotImage} {
		if _, _, err := f.Image(ctx, srv.URL+path); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", path, err, want)
		}
	}
	if _, _, err := f.Image(ctx, srv.URL+"/big.png"); err == nil {
		t.Error("oversized image must be refused")
	}
	guarded := New(Options{PerHostRate: 1000})
	if _, _, err := guarded.Image(ctx, srv.URL+"/chart.png"); err == nil {
		t.Error("the SSRF guard must apply to images")
	}
}

func TestURLWhitespaceAndInvalid(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()
	f := New(Options{AllowPrivate: true, PerHostRate: 100})
	// A pasted URL with a trailing tab or newline still works.
	body, err := f.Trusted(context.Background(), srv.URL+"/feed\t\n", nil)
	if err != nil || string(body) != "ok" {
		t.Fatalf("trailing whitespace: %q %v", body, err)
	}
	// A URL that doesn't parse says so, instead of blaming the scheme.
	if _, err := f.Trusted(context.Background(), "https://exa mple.com/%zz", nil); err == nil || !strings.Contains(err.Error(), "invalid URL") {
		t.Errorf("invalid URL error = %v", err)
	}
	if _, err := f.Trusted(context.Background(), "ftp://example.com/feed", nil); !errors.Is(err, ErrScheme) {
		t.Errorf("non-http scheme = %v", err)
	}
}
