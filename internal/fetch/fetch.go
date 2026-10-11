// Package fetch is the only way to retrieve pages whose URLs come from
// collected (untrusted) content. It honors robots.txt, rate-limits per host,
// identifies itself, caps size and time, and refuses private, loopback,
// link-local, and cluster addresses (SSRF guard) for untrusted URLs.
// It only issues GET requests.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	readability "github.com/go-shiori/go-readability"
	"github.com/temoto/robotstxt"
	"golang.org/x/time/rate"
)

// UserAgent identifies the crawler. RobotsAgent is the token matched in
// robots.txt groups.
const (
	UserAgent   = "marketing-research-agent/0.1 (+https://github.com/BiodigitalJaz/marketing-research-agent; read-only; respects robots.txt)"
	RobotsAgent = "marketing-research-agent"
)

var (
	ErrDisallowed  = errors.New("blocked by robots.txt")
	ErrPrivateAddr = errors.New("refusing to fetch a private or local address")
	ErrNotHTML     = errors.New("not an HTML or text document")
	ErrScheme      = errors.New("only http and https URLs can be fetched")
)

// Options configures a Fetcher.
type Options struct {
	Timeout time.Duration // untrusted page fetches; default 15s
	// TrustedTimeout bounds admin-configured endpoints (feeds, APIs, search
	// backends), which can be slower than a page; default 30s.
	TrustedTimeout time.Duration
	MaxBytes       int64      // default 2 MiB
	PerHostRate    rate.Limit // default 1 req/s
	// HostRates overrides PerHostRate for specific hosts (e.g. APIs that
	// allow higher request rates).
	HostRates map[string]rate.Limit
	// AllowPrivate disables the SSRF guard (tests only).
	AllowPrivate bool
	RobotsTTL    time.Duration // default 1h
}

// Fetcher is safe for concurrent use.
type Fetcher struct {
	opts      Options
	untrusted *http.Client // SSRF-guarded
	trusted   *http.Client // admin-configured URLs (feed URLs, LAN search backends)
	mu        sync.Mutex
	limiters  map[string]*rate.Limiter
	robots    map[string]robotsEntry
}

type robotsEntry struct {
	data    *robotstxt.RobotsData
	allow   bool // set when robots could not be evaluated and we default
	fetched time.Time
}

func New(opts Options) *Fetcher {
	if opts.Timeout == 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.TrustedTimeout == 0 {
		opts.TrustedTimeout = 30 * time.Second
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 2 << 20
	}
	if opts.PerHostRate == 0 {
		opts.PerHostRate = 1
	}
	if opts.RobotsTTL == 0 {
		opts.RobotsTTL = time.Hour
	}
	f := &Fetcher{opts: opts, limiters: map[string]*rate.Limiter{}, robots: map[string]robotsEntry{}}
	f.untrusted = f.client(!opts.AllowPrivate, opts.Timeout)
	f.trusted = f.client(false, opts.TrustedTimeout)
	return f
}

func (f *Fetcher) client(guard bool, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if guard {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !PublicIP(ip) {
				return fmt.Errorf("%w: %s", ErrPrivateAddr, host)
			}
			return nil
		}
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   2,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return ErrScheme
			}
			return nil
		},
	}
}

var blockedNets = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "0.0.0.0/8", "240.0.0.0/4"} {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// PublicIP reports whether ip is a globally routable unicast address.
func PublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	for _, n := range blockedNets {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

func (f *Fetcher) wait(ctx context.Context, host string) error {
	f.mu.Lock()
	lim, ok := f.limiters[host]
	if !ok {
		r, burst := f.opts.PerHostRate, 1
		if hr, ok := f.opts.HostRates[host]; ok {
			r, burst = hr, int(hr)+1
		}
		lim = rate.NewLimiter(r, burst)
		f.limiters[host] = lim
	}
	f.mu.Unlock()
	return lim.Wait(ctx)
}

// Allowed reports whether robots.txt permits fetching u.
func (f *Fetcher) Allowed(ctx context.Context, u *url.URL) (bool, error) {
	key := u.Scheme + "://" + u.Host
	f.mu.Lock()
	e, ok := f.robots[key]
	f.mu.Unlock()
	if !ok || time.Since(e.fetched) > f.opts.RobotsTTL {
		e = f.loadRobots(ctx, u)
		f.mu.Lock()
		f.robots[key] = e
		f.mu.Unlock()
	}
	if e.data == nil {
		return e.allow, nil
	}
	return e.data.TestAgent(u.EscapedPath(), RobotsAgent), nil
}

// loadRobots follows RFC 9309: 4xx means no restrictions; 5xx or an
// unreachable server means assume full disallow (retried after the TTL).
func (f *Fetcher) loadRobots(ctx context.Context, u *url.URL) robotsEntry {
	e := robotsEntry{fetched: time.Now()}
	ru := url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/robots.txt"}
	if err := f.wait(ctx, u.Host); err != nil {
		return e
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ru.String(), nil)
	if err != nil {
		return e
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := f.untrusted.Do(req)
	if err != nil {
		return e // allow=false: unreachable
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	switch {
	case resp.StatusCode >= 500:
		return e
	case resp.StatusCode >= 400:
		e.allow = true
		return e
	}
	data, err := robotstxt.FromStatusAndBytes(resp.StatusCode, body)
	if err != nil {
		e.allow = true // unparseable: treat as no rules
		return e
	}
	e.data = data
	return e
}

// Page is a fetched, extracted document.
type Page struct {
	URL         string // final URL after redirects
	Title       string
	Text        string
	Excerpt     string
	SiteName    string
	PublishedAt *time.Time
}

// Article fetches an untrusted URL and extracts its main text.
func (f *Fetcher) Article(ctx context.Context, rawURL string) (*Page, error) {
	u, err := httpURL(rawURL)
	if err != nil {
		return nil, err
	}
	ok, err := f.Allowed(ctx, u)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrDisallowed
	}
	body, final, ctype, err := f.get(ctx, f.untrusted, u)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(ctype, "text/plain") {
		return &Page{URL: final.String(), Text: strings.TrimSpace(string(body))}, nil
	}
	art, err := readability.FromReader(strings.NewReader(string(body)), final)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", final, err)
	}
	return &Page{URL: final.String(), Title: art.Title, Text: strings.TrimSpace(art.TextContent), Excerpt: art.Excerpt,
		SiteName: art.SiteName, PublishedAt: art.PublishedTime}, nil
}

// Trusted fetches an admin-configured URL (a feed or API endpoint). It is
// rate-limited but skips robots.txt and the private-address guard, so LAN
// services can be used. Never pass URLs taken from collected content.
func (f *Fetcher) Trusted(ctx context.Context, rawURL string, headers map[string]string) ([]byte, error) {
	u, err := httpURL(rawURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if err := f.wait(ctx, u.Host); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := f.trusted.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", u.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	return io.ReadAll(io.LimitReader(resp.Body, f.opts.MaxBytes))
}

// StatusError is a non-200 response.
type StatusError struct {
	Code       int
	RetryAfter string
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Code) }

func (f *Fetcher) get(ctx context.Context, c *http.Client, u *url.URL) ([]byte, *url.URL, string, error) {
	return f.getAs(ctx, c, u, "text/html,application/xhtml+xml,text/plain;q=0.8", func(ctype string) bool {
		return ctype == "" || strings.Contains(ctype, "html") || strings.HasPrefix(ctype, "text/plain")
	}, ErrNotHTML)
}

// Image types models accept as vision input.
var imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

// ErrNotImage is returned when a URL does not serve a supported image.
var ErrNotImage = errors.New("not a PNG, JPEG, GIF, or WebP image")

// Image fetches an untrusted image URL (robots.txt, SSRF guard, rate and
// size limits) for vision input.
func (f *Fetcher) Image(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := httpURL(rawURL)
	if err != nil {
		return nil, "", err
	}
	ok, err := f.Allowed(ctx, u)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", ErrDisallowed
	}
	body, _, _, err := f.getAs(ctx, f.untrusted, u, strings.Join(imageTypes, ","), func(ctype string) bool {
		return strings.HasPrefix(ctype, "image/")
	}, ErrNotImage)
	if err != nil {
		return nil, "", err
	}
	if int64(len(body)) >= f.opts.MaxBytes {
		return nil, "", fmt.Errorf("image larger than %d bytes", f.opts.MaxBytes)
	}
	// Trust the bytes, not the header.
	mt := http.DetectContentType(body)
	if !slices.Contains(imageTypes, mt) {
		return nil, "", ErrNotImage
	}
	return body, mt, nil
}

func (f *Fetcher) getAs(ctx context.Context, c *http.Client, u *url.URL, accept string, okType func(string) bool, typeErr error) ([]byte, *url.URL, string, error) {
	if err := f.wait(ctx, u.Host); err != nil {
		return nil, nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", accept)
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, "", fmt.Errorf("get %s: %w", u.Host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, "", &StatusError{Code: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	ctype := strings.ToLower(resp.Header.Get("Content-Type"))
	if !okType(ctype) {
		return nil, nil, "", typeErr
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.opts.MaxBytes))
	if err != nil {
		return nil, nil, "", err
	}
	return body, resp.Request.URL, ctype, nil
}

// httpURL parses an absolute http(s) URL, ignoring surrounding whitespace
// (a pasted URL often carries a trailing tab or newline). A URL that doesn't
// parse is reported as such, not as a scheme problem.
func httpURL(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", trimmed, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrScheme
	}
	return u, nil
}
