package research

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/BiodigitalJaz/marketing-research-agent/internal/agent"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/embed"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/knowledge"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/llm"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/pipeline"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/prompts"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/search"
	"github.com/BiodigitalJaz/marketing-research-agent/internal/store"
)

// Built-in tool names (task_routes.tools allowlists these).
const (
	ToolSearchCorpus     = "search_corpus"
	ToolGetItem          = "get_item"
	ToolSearchBrand      = "search_brand"
	ToolOpportunityStats = "opportunity_stats"
	ToolMakeChart        = "make_chart"
	ToolWebSearch        = "web_search"
	ToolFetchURL         = "fetch_url"
	ToolViewImage        = "view_image"
	ToolSearchKnowledge  = "search_knowledge"
	ToolReadDoc          = "read_doc"
	ToolReadMemoryBank   = "read_memory_bank"
)

// BuiltinTools lists every built-in tool, for Settings.
var BuiltinTools = []string{ToolSearchCorpus, ToolGetItem, ToolSearchBrand, ToolOpportunityStats, ToolMakeChart, ToolWebSearch, ToolFetchURL, ToolViewImage,
	ToolSearchKnowledge, ToolReadDoc, ToolReadMemoryBank}

const (
	fetchTextChars = 5000
	itemTextChars  = 6000
)

// toolkit holds one run's tool state: the notebook of citable sources and
// the hosts of pages fetched so far (view_image may only use those hosts).
type toolkit struct {
	s      *Service
	ws     uuid.UUID
	nb     *agent.Notebook
	web    bool
	mu     sync.Mutex
	hosts  map[string]bool
	failed map[string]string // URL -> error, so a failing page is not refetched
	chunks map[int]uuid.UUID // source number -> knowledge chunk
}

func (s *Service) builtinTools(ws uuid.UUID, nb *agent.Notebook, useWeb bool) *agent.Registry {
	k := &toolkit{s: s, ws: ws, nb: nb, web: useWeb, hosts: map[string]bool{}, failed: map[string]string{}, chunks: map[int]uuid.UUID{}}
	r := agent.NewRegistry()
	add := func(name, desc, schema string, web, vision bool, h func(context.Context, json.RawMessage) (agent.Output, error)) {
		r.Add(agent.Tool{Def: llm.ToolDef{Name: name, Description: desc, Schema: json.RawMessage(schema)}, Handler: h, Web: web, Vision: vision, Source: "builtin"})
	}
	add(ToolSearchCorpus, "Search content the team has already collected (forums, Q&A sites, feeds, fetched pages). Returns numbered sources to cite.",
		`{"type":"object","properties":{"query":{"type":"string","description":"Keywords or a short question"},"lane":{"type":"string","enum":["inbound","media","thought_leadership"],"description":"Only items that became opportunities in this lane"},"community":{"type":"string","description":"Filter by community or source name"},"since_days":{"type":"integer","minimum":1,"maximum":365},"limit":{"type":"integer","minimum":1,"maximum":15}},"required":["query"],"additionalProperties":false}`,
		false, false, k.searchCorpus)
	add(ToolGetItem, "Read the full text of a collected item or fetched page by its source number.",
		`{"type":"object","properties":{"source":{"type":"integer","description":"The source number, e.g. 3 for [3]"}},"required":["source"],"additionalProperties":false}`,
		false, false, k.getItem)
	add(ToolSearchBrand, "Get the brand's own profile: offering, positioning, tone rules, ideal customers, portfolio pieces, and competitors.",
		`{"type":"object","properties":{},"additionalProperties":false}`, false, false, k.searchBrand)
	add(ToolOpportunityStats, "Count opportunities (or collected items) grouped by one dimension. Returns a dataset id usable with make_chart.",
		`{"type":"object","properties":{"group_by":{"type":"string","enum":["lane","opp_type","status","community","source","day","week"]},"metric":{"type":"string","enum":["opportunities","items"],"description":"items supports community, source, day, week"},"since_days":{"type":"integer","minimum":1,"maximum":365},"lane":{"type":"string","enum":["inbound","media","thought_leadership"]}},"required":["group_by"],"additionalProperties":false}`,
		false, false, k.opportunityStats)
	add(ToolMakeChart, "Create a chart from a dataset a data tool returned. The data comes from the dataset; you choose the columns. Returns a chart id to place in the answer.",
		`{"type":"object","properties":{"dataset":{"type":"string","description":"Dataset id, e.g. d1"},"type":{"type":"string","enum":["line","bar","pie","table"]},"title":{"type":"string"},"x":{"type":"string","description":"Column for the x axis or labels"},"y":{"type":"array","items":{"type":"string"},"description":"Value columns"},"limit":{"type":"integer","minimum":1,"maximum":60}},"required":["dataset","type","title","x","y"],"additionalProperties":false}`,
		false, false, k.makeChart)
	add(ToolWebSearch, "Search the public web with the workspace's search backend. Returns numbered sources (titles and snippets); use fetch_url to read a page.",
		`{"type":"object","properties":{"query":{"type":"string"},"time_range":{"type":"string","enum":["day","week","month","year"]},"count":{"type":"integer","minimum":1,"maximum":10}},"required":["query"],"additionalProperties":false}`,
		true, false, k.webSearch)
	add(ToolFetchURL, "Fetch and read a web page (respects robots.txt). Give a URL or the number of a web source. The page is stored so it can be cited.",
		`{"type":"object","properties":{"url":{"type":"string"},"source":{"type":"integer","description":"A web source number instead of a URL"}},"additionalProperties":false}`,
		true, false, k.fetchURL)
	add(ToolSearchKnowledge, "Search the team's internal knowledge base: the project's own docs (architecture, security, decisions, operations, business). Returns numbered doc sections to cite.",
		`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":10}},"required":["query"],"additionalProperties":false}`,
		false, false, k.searchKnowledge)
	add(ToolReadDoc, "Read an internal doc section in full by its source number, or a doc by file name (optionally one section of it). Without a section, lists the doc's sections.",
		`{"type":"object","properties":{"source":{"type":"integer","description":"A doc section's source number"},"path":{"type":"string","description":"File name, e.g. SECURITY.md"},"section":{"type":"string","description":"Words from a section heading"}},"additionalProperties":false}`,
		false, false, k.readDoc)
	add(ToolReadMemoryBank, "Read a memory bank file: concise summaries of the project generated from its docs (projectbrief, productContext, systemPatterns, techContext, activeContext, progress). Use for orientation; cite doc sections, not these files.",
		`{"type":"object","properties":{"file":{"type":"string","enum":["projectbrief","productContext","systemPatterns","techContext","activeContext","progress"]}},"required":["file"],"additionalProperties":false}`,
		false, false, k.readMemoryBank)
	add(ToolViewImage, "Look at an image (a chart or screenshot) from a page you have already fetched.",
		`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}`,
		true, true, k.viewImage)
	return r
}

func decode[T any](args json.RawMessage) (T, error) {
	var v T
	if err := json.Unmarshal(args, &v); err != nil {
		return v, fmt.Errorf("invalid arguments: %w", err)
	}
	return v, nil
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func day(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

type corpusHit struct {
	id        uuid.UUID
	url       string
	title     string
	snippet   string
	community string
	date      string
}

// searchCorpus fuses keyword and semantic results (reciprocal rank fusion).
func (k *toolkit) searchCorpus(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		Query     string  `json:"query"`
		Lane      *string `json:"lane"`
		Community *string `json:"community"`
		SinceDays int     `json:"since_days"`
		Limit     int     `json:"limit"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return agent.Output{}, errors.New("query is required")
	}
	if in.Limit <= 0 || in.Limit > 15 {
		in.Limit = 8
	}
	var since *time.Time
	if in.SinceDays > 0 {
		t := time.Now().AddDate(0, 0, -min(in.SinceDays, 365))
		since = &t
	}
	var lane *store.Lane
	if in.Lane != nil && *in.Lane != "" {
		l := store.Lane(*in.Lane)
		lane = &l
	}
	community := in.Community
	if community != nil && strings.TrimSpace(*community) == "" {
		community = nil
	}
	q := store.New(k.s.Pool)
	scores := map[uuid.UUID]float64{}
	hits := map[uuid.UUID]corpusHit{}
	const rrfK = 60.0
	text, err := q.SearchCorpusText(ctx, store.SearchCorpusTextParams{WorkspaceID: k.ws, Query: in.Query, Since: since, Community: community, Lane: lane, Lim: 30})
	if err != nil {
		return agent.Output{}, err
	}
	for i, r := range text {
		scores[r.ID] += 1 / (rrfK + float64(i+1))
		hits[r.ID] = corpusHit{r.ID, r.Url, deref(r.Title), r.Snippet, communityOf(r.Community, r.SourceName), day(firstTime(r.PublishedAt, &r.FetchedAt))}
	}
	if e, err := k.embedder(ctx); err == nil {
		if vecs, err := e.Embed(ctx, []string{in.Query}); err == nil && len(vecs) == 1 {
			rows, err := q.SearchCorpusVector(ctx, store.SearchCorpusVectorParams{WorkspaceID: k.ws, Vec: pgvector.NewVector(vecs[0]), Model: e.Model(), Dims: int32(e.Dims()), // #nosec G115 -- small
				Since: since, Community: community, Lane: lane, Lim: 30})
			if err == nil {
				for i, r := range rows {
					scores[r.ID] += 1 / (rrfK + float64(i+1))
					if _, ok := hits[r.ID]; !ok {
						hits[r.ID] = corpusHit{r.ID, r.Url, deref(r.Title), r.Snippet, communityOf(r.Community, r.SourceName), day(firstTime(r.PublishedAt, &r.FetchedAt))}
					}
				}
			}
		}
	}
	ids := make([]uuid.UUID, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int {
		if scores[a] != scores[b] {
			if scores[a] > scores[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a.String(), b.String())
	})
	if len(ids) > in.Limit {
		ids = ids[:in.Limit]
	}
	if len(ids) == 0 {
		return agent.Output{Text: "No collected items match. Try other keywords" + map[bool]string{true: " or web_search.", false: "."}[k.webAllowed()], Summary: "0 results"}, nil
	}
	var b strings.Builder
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		h := hits[id]
		item := h.id
		n := k.nb.Cite(agent.Source{Kind: "corpus", RawItemID: &item, URL: h.url, Title: h.title})
		fmt.Fprintf(&b, "[%d] %s — %s, %s\n%s\n%s\n\n", n, orURL(h.title, h.url), h.community, h.date, h.url, clip(oneLine(h.snippet), 280))
		k.nb.Record(n, h.title+"\n"+clip(oneLine(h.snippet), 280))
		rows = append(rows, map[string]any{"source": n, "title": orURL(h.title, h.url), "community": h.community, "date": h.date})
	}
	ds := k.nb.AddDataset("search_corpus("+in.Query+")", []string{"source", "title", "community", "date"}, rows)
	out := prompts.ExternalContent("corpus search", strings.TrimSpace(b.String()))
	return agent.Output{Text: out + fmt.Sprintf("\n\nThese results are also dataset %s (a table: source, title, community, date).", ds), Summary: fmt.Sprintf("%d results", len(ids))}, nil
}

func (k *toolkit) webAllowed() bool { return k.web }

func (k *toolkit) embedder(ctx context.Context) (embed.Embedder, error) {
	if k.s.Embedder == nil {
		return nil, embed.ErrNotConfigured
	}
	return k.s.Embedder(ctx, k.ws)
}

func (k *toolkit) getItem(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		Source int `json:"source"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	src, ok := k.nb.Source(in.Source)
	if !ok {
		return agent.Output{}, fmt.Errorf("there is no source [%d]", in.Source)
	}
	if src.RawItemID == nil {
		return agent.Output{}, fmt.Errorf("[%d] is a web search result; use fetch_url with source %d to read it", in.Source, in.Source)
	}
	it, err := store.New(k.s.Pool).GetResearchItem(ctx, store.GetResearchItemParams{WorkspaceID: k.ws, ID: *src.RawItemID})
	if err != nil {
		return agent.Output{}, fmt.Errorf("source [%d] is no longer stored", in.Source)
	}
	header := fmt.Sprintf("[%d] %s — %s, %s\n%s\n\n", in.Source, orURL(deref(it.Title), it.Url), communityOf(it.Community, it.SourceName), day(firstTime(it.PublishedAt, &it.FetchedAt)), it.Url)
	body := clip(it.Body, itemTextChars)
	k.nb.Record(in.Source, deref(it.Title)+"\n"+body)
	return agent.Output{Text: header + prompts.ExternalContent(it.SourceName, body), Summary: "read [" + strconv.Itoa(in.Source) + "]"}, nil
}

func (k *toolkit) searchBrand(ctx context.Context, _ json.RawMessage) (agent.Output, error) {
	q := store.New(k.s.Pool)
	bc, err := pipeline.LoadBrand(ctx, q, k.ws)
	if err != nil {
		return agent.Output{}, err
	}
	// The profile is a numbered source like any other, so claims drawn from
	// it are cited [n] (linking to the Brand page) instead of with a made-up
	// label.
	n := k.nb.Cite(agent.Source{Kind: "brand", URL: "/brand", Title: bc.Name + " brand profile"})
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] %s brand profile (the brand's own positioning; cite claims drawn from it as [%d])\n\nBrand: %s\n%s\n", n, bc.Name, n, bc.Name, bc.Summary)
	if bc.Tone.Voice != "" {
		fmt.Fprintf(&b, "Voice: %s\n", bc.Tone.Voice)
	}
	items, err := q.RecentPortfolioItems(ctx, store.RecentPortfolioItemsParams{WorkspaceID: k.ws, Limit: 20})
	if err != nil {
		return agent.Output{}, err
	}
	if len(items) > 0 {
		b.WriteString("\nPortfolio pieces (the brand's own content):\n")
		for _, it := range items {
			fmt.Fprintf(&b, "- %s: %s (%s) — %s\n", it.Kind, it.Title, orStr(it.Url, "not published"), clip(oneLine(it.Summary), 200))
		}
	}
	k.nb.Record(n, b.String())
	return agent.Output{Text: strings.TrimSpace(b.String()), Summary: fmt.Sprintf("[%d] brand profile", n)}, nil
}

func (k *toolkit) opportunityStats(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		GroupBy   string  `json:"group_by"`
		Metric    string  `json:"metric"`
		SinceDays int     `json:"since_days"`
		Lane      *string `json:"lane"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	if in.SinceDays <= 0 || in.SinceDays > 365 {
		in.SinceDays = 30
	}
	since := time.Now().AddDate(0, 0, -in.SinceDays)
	q := store.New(k.s.Pool)
	var cols []string
	var rows []map[string]any
	switch in.Metric {
	case "items":
		if !slices.Contains([]string{"community", "source", "day", "week"}, in.GroupBy) {
			return agent.Output{}, errors.New("items can be grouped by community, source, day, or week")
		}
		res, err := q.ItemStats(ctx, store.ItemStatsParams{WorkspaceID: k.ws, GroupBy: in.GroupBy, Since: since})
		if err != nil {
			return agent.Output{}, err
		}
		cols = []string{in.GroupBy, "items"}
		for _, r := range res {
			rows = append(rows, map[string]any{in.GroupBy: r.Key, "items": r.Count})
		}
	case "", "opportunities":
		if !slices.Contains([]string{"lane", "opp_type", "status", "community", "source", "day", "week"}, in.GroupBy) {
			return agent.Output{}, errors.New("group_by must be lane, opp_type, status, community, source, day, or week")
		}
		var lane *store.Lane
		if in.Lane != nil && *in.Lane != "" {
			l := store.Lane(*in.Lane)
			lane = &l
		}
		res, err := q.OpportunityStats(ctx, store.OpportunityStatsParams{WorkspaceID: k.ws, GroupBy: in.GroupBy, Since: since, Lane: lane})
		if err != nil {
			return agent.Output{}, err
		}
		cols = []string{in.GroupBy, "opportunities", "avg_score"}
		for _, r := range res {
			rows = append(rows, map[string]any{in.GroupBy: r.Key, "opportunities": r.Count, "avg_score": r.AvgScore})
		}
	default:
		return agent.Output{}, errors.New("metric must be opportunities or items")
	}
	if len(rows) == 0 {
		return agent.Output{Text: fmt.Sprintf("No data in the last %d days.", in.SinceDays), Summary: "no data"}, nil
	}
	origin := fmt.Sprintf("opportunity_stats(group_by=%s, metric=%s, since_days=%d)", in.GroupBy, orDefault(in.Metric, "opportunities"), in.SinceDays)
	ds := k.nb.AddDataset(origin, cols, rows)
	var b strings.Builder
	fmt.Fprintf(&b, "Dataset %s, last %d days (columns: %s):\n", ds, in.SinceDays, strings.Join(cols, ", "))
	for _, r := range rows {
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = fmt.Sprint(r[c])
		}
		b.WriteString(strings.Join(vals, " | ") + "\n")
	}
	return agent.Output{Text: strings.TrimSpace(b.String()), Summary: fmt.Sprintf("%s: %d rows", ds, len(rows))}, nil
}

func (k *toolkit) makeChart(_ context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		Dataset string   `json:"dataset"`
		Type    string   `json:"type"`
		Title   string   `json:"title"`
		X       string   `json:"x"`
		Y       []string `json:"y"`
		Limit   int      `json:"limit"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	c, err := k.nb.MakeChart(in.Dataset, in.Type, clip(in.Title, 120), in.X, in.Y, in.Limit)
	if err != nil {
		return agent.Output{}, err
	}
	return agent.Output{Text: fmt.Sprintf("Chart %s created (%s, %d rows). To show it, put {{chart:%s}} on its own line.", c.ID, c.Type, len(c.Data), c.ID), Summary: c.ID + ": " + c.Title}, nil
}

func (k *toolkit) webSearch(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		Query     string `json:"query"`
		TimeRange string `json:"time_range"`
		Count     int    `json:"count"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return agent.Output{}, errors.New("query is required")
	}
	if in.Count <= 0 || in.Count > 10 {
		in.Count = 6
	}
	if k.s.Search == nil {
		return agent.Output{}, errors.New("web search is not configured")
	}
	results, err := k.s.Search(ctx, k.ws, search.Query{Q: in.Query, TimeRange: in.TimeRange, Count: in.Count})
	if err != nil {
		return agent.Output{}, err
	}
	if len(results) > in.Count {
		results = results[:in.Count]
	}
	if len(results) == 0 {
		return agent.Output{Text: "No web results.", Summary: "0 results"}, nil
	}
	var b strings.Builder
	for _, r := range results {
		n := k.nb.Cite(agent.Source{Kind: "web", URL: r.URL, Title: r.Title})
		fmt.Fprintf(&b, "[%d] %s — %s%s\n%s\n\n", n, orURL(r.Title, r.URL), r.URL, map[bool]string{true: ", " + day(r.PublishedAt), false: ""}[r.PublishedAt != nil], clip(oneLine(r.Snippet), 280))
		k.nb.Record(n, r.Title+"\n"+clip(oneLine(r.Snippet), 280))
	}
	return agent.Output{Text: prompts.ExternalContent("web search", strings.TrimSpace(b.String())), Summary: fmt.Sprintf("%d results", len(results))}, nil
}

func (k *toolkit) fetchURL(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		URL    string `json:"url"`
		Source int    `json:"source"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	target := strings.TrimSpace(in.URL)
	if in.Source > 0 {
		src, ok := k.nb.Source(in.Source)
		if !ok {
			return agent.Output{}, fmt.Errorf("there is no source [%d]", in.Source)
		}
		if src.RawItemID != nil {
			// Already stored (a collected item or a fetched page): read it locally.
			return k.getItem(ctx, json.RawMessage(fmt.Sprintf(`{"source":%d}`, in.Source)))
		}
		target = src.URL
	}
	if target == "" {
		return agent.Output{}, errors.New("give a url or a source number")
	}
	if k.s.Fetch == nil {
		return agent.Output{}, errors.New("fetching is not configured")
	}
	k.mu.Lock()
	prev, seen := k.failed[target]
	k.mu.Unlock()
	if seen {
		return agent.Output{}, fmt.Errorf("this page already failed (%s); do not retry it, use another source", prev)
	}
	// Untrusted URL: only the polite fetcher (robots.txt, SSRF guard, limits).
	page, err := k.s.Fetch.Article(ctx, target)
	if err != nil {
		k.mu.Lock()
		k.failed[target] = err.Error()
		k.mu.Unlock()
		return agent.Output{}, err
	}
	text := strings.TrimSpace(page.Text)
	if text == "" {
		return agent.Output{}, errors.New("the page has no readable text")
	}
	id, err := k.store(ctx, page.URL, page.Title, text, page.SiteName, page.PublishedAt)
	if err != nil {
		return agent.Output{}, err
	}
	n := k.nb.Cite(agent.Source{Kind: "page", RawItemID: &id, URL: target, Title: page.Title})
	if page.URL != target {
		k.nb.Cite(agent.Source{Kind: "page", RawItemID: &id, URL: page.URL, Title: page.Title})
	}
	if u, err := url.Parse(page.URL); err == nil {
		k.mu.Lock()
		k.hosts[strings.ToLower(u.Hostname())] = true
		k.mu.Unlock()
	}
	header := fmt.Sprintf("[%d] %s — %s\n\n", n, orURL(page.Title, page.URL), page.URL)
	shown := clip(text, fetchTextChars)
	k.nb.Record(n, page.Title+"\n"+shown)
	return agent.Output{Text: header + prompts.ExternalContent(page.URL, shown), Summary: "read [" + strconv.Itoa(n) + "]"}, nil
}

// store saves a fetched page as a raw item so it can be cited.
func (k *toolkit) store(ctx context.Context, pageURL, title, text, site string, published *time.Time) (uuid.UUID, error) {
	q := store.New(k.s.Pool)
	src, err := q.ResearchSourceID(ctx, k.ws)
	if err != nil {
		return uuid.Nil, fmt.Errorf("research source: %w", err)
	}
	sum := sha256.Sum256([]byte(strings.ToLower(oneLine(title + "\n" + text))))
	var expires *time.Time
	if k.s.RetentionDays > 0 {
		t := time.Now().AddDate(0, 0, k.s.RetentionDays)
		expires = &t
	}
	var t, community *string
	if title != "" {
		t = &title
	}
	if site == "" {
		if u, err := url.Parse(pageURL); err == nil {
			site = strings.TrimPrefix(u.Hostname(), "www.")
		}
	}
	if site != "" {
		community = &site
	}
	return q.UpsertResearchItem(ctx, store.UpsertResearchItemParams{WorkspaceID: k.ws, SourceID: src, ExternalID: pageURL, Url: pageURL, Title: t,
		Body: clip(text, 100_000), Community: community, PublishedAt: published, ContentHash: sum[:], ExpiresAt: expires})
}

func (k *toolkit) viewImage(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		URL string `json:"url"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || u.Host == "" {
		return agent.Output{}, errors.New("give the image's full URL")
	}
	k.mu.Lock()
	allowed := k.hosts[strings.ToLower(u.Hostname())]
	k.mu.Unlock()
	if !allowed {
		return agent.Output{}, errors.New("only images on pages you have fetched with fetch_url can be viewed")
	}
	if k.s.Fetch == nil {
		return agent.Output{}, errors.New("fetching is not configured")
	}
	data, mt, err := k.s.Fetch.Image(ctx, u.String())
	if err != nil {
		return agent.Output{}, err
	}
	return agent.Output{Text: "The image from " + u.String() + " is attached after these results.", Images: []llm.Image{{MediaType: mt, Data: data}}, Summary: "viewed image"}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orStr(s *string, def string) string {
	if s == nil || *s == "" {
		return def
	}
	return *s
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func orURL(title, u string) string {
	if strings.TrimSpace(title) == "" {
		return u
	}
	return oneLine(title)
}

func communityOf(c *string, source string) string {
	if c != nil && *c != "" {
		return *c
	}
	return source
}

func firstTime(a, b *time.Time) *time.Time {
	if a != nil {
		return a
	}
	return b
}

const internalNote = "Internal document: it describes design and intent, which can differ from the code. Attribute claims to it."

// citeChunk numbers a knowledge section as a citable source (a GitHub link
// pinned to the commit the docs were read at).
func (k *toolkit) citeChunk(id uuid.UUID, heading, anchor, path, commit, repo string) int {
	n := k.nb.Cite(agent.Source{Kind: "internal", URL: knowledge.BlobURL(repo, commit, path, anchor), Title: heading})
	k.mu.Lock()
	k.chunks[n] = id
	k.mu.Unlock()
	return n
}

type knowledgeHit struct {
	id                                           uuid.UUID
	heading, anchor, snippet, path, commit, repo string
}

func (k *toolkit) searchKnowledge(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return agent.Output{}, errors.New("query is required")
	}
	if in.Limit <= 0 || in.Limit > 10 {
		in.Limit = 6
	}
	q := store.New(k.s.Pool)
	scores := map[uuid.UUID]float64{}
	hits := map[uuid.UUID]knowledgeHit{}
	const rrfK = 60.0
	text, err := q.SearchKnowledgeText(ctx, store.SearchKnowledgeTextParams{WorkspaceID: k.ws, Query: in.Query, Lim: 20})
	if err != nil {
		return agent.Output{}, err
	}
	for i, r := range text {
		scores[r.ID] += 1 / (rrfK + float64(i+1))
		hits[r.ID] = knowledgeHit{r.ID, r.Heading, r.Anchor, r.Snippet, r.Path, r.CommitSha, r.Repo}
	}
	if e, err := k.embedder(ctx); err == nil {
		if vecs, err := e.Embed(ctx, []string{in.Query}); err == nil && len(vecs) == 1 {
			rows, err := q.SearchKnowledgeVector(ctx, store.SearchKnowledgeVectorParams{WorkspaceID: k.ws, Vec: pgvector.NewVector(vecs[0]), Model: e.Model(), Dims: int32(e.Dims()), Lim: 20}) // #nosec G115 -- small
			if err == nil {
				for i, r := range rows {
					scores[r.ID] += 1 / (rrfK + float64(i+1))
					if _, ok := hits[r.ID]; !ok {
						hits[r.ID] = knowledgeHit{r.ID, r.Heading, r.Anchor, r.Snippet, r.Path, r.CommitSha, r.Repo}
					}
				}
			}
		}
	}
	ids := make([]uuid.UUID, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int {
		if scores[a] != scores[b] {
			if scores[a] > scores[b] {
				return -1
			}
			return 1
		}
		return strings.Compare(a.String(), b.String())
	})
	if len(ids) > in.Limit {
		ids = ids[:in.Limit]
	}
	if len(ids) == 0 {
		return agent.Output{Text: "No internal doc sections match. Try other words, or read_memory_bank for an overview.", Summary: "0 results"}, nil
	}
	var b strings.Builder
	for _, id := range ids {
		h := hits[id]
		n := k.citeChunk(h.id, h.heading, h.anchor, h.path, h.commit, h.repo)
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", n, h.heading, clip(oneLine(h.snippet), 400))
		k.nb.Record(n, h.heading+"\n"+clip(oneLine(h.snippet), 400))
	}
	return agent.Output{Text: internalNote + "\n\n" + prompts.ExternalContent("internal docs", strings.TrimSpace(b.String())) + "\n\nUse read_doc with a source number for the full section.",
		Summary: fmt.Sprintf("%d sections", len(ids))}, nil
}

func (k *toolkit) readDoc(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		Source  int    `json:"source"`
		Path    string `json:"path"`
		Section string `json:"section"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	q := store.New(k.s.Pool)
	if in.Source > 0 {
		k.mu.Lock()
		id, ok := k.chunks[in.Source]
		k.mu.Unlock()
		if !ok {
			return agent.Output{}, fmt.Errorf("[%d] is not an internal doc section; use search_knowledge first", in.Source)
		}
		c, err := q.GetKnowledgeChunk(ctx, store.GetKnowledgeChunkParams{WorkspaceID: k.ws, ID: id})
		if err != nil {
			return agent.Output{}, fmt.Errorf("section [%d] is no longer in the knowledge base", in.Source)
		}
		k.nb.Record(in.Source, c.Heading+"\n"+clip(c.Content, itemTextChars))
		return agent.Output{Text: fmt.Sprintf("[%d] %s\n%s\n\n", in.Source, c.Heading, internalNote) + prompts.ExternalContent(c.Path, clip(c.Content, itemTextChars)),
			Summary: "read [" + strconv.Itoa(in.Source) + "]"}, nil
	}
	p := strings.TrimSpace(in.Path)
	if p == "" {
		return agent.Output{}, errors.New("give a source number or a doc path")
	}
	rows, err := q.ChunksByDocPath(ctx, store.ChunksByDocPathParams{WorkspaceID: k.ws, Path: p})
	if err != nil {
		return agent.Output{}, err
	}
	if len(rows) == 0 {
		return agent.Output{}, fmt.Errorf("no internal doc named %s", p)
	}
	want := strings.ToLower(strings.TrimSpace(in.Section))
	if want == "" {
		var b strings.Builder
		fmt.Fprintf(&b, "Sections of %s (read one with read_doc and section):\n", rows[0].Path)
		for _, r := range rows {
			fmt.Fprintf(&b, "- %s\n", r.Heading)
		}
		return agent.Output{Text: strings.TrimSpace(b.String()), Summary: fmt.Sprintf("%d sections", len(rows))}, nil
	}
	var b strings.Builder
	used := 0
	for _, r := range rows {
		if !strings.Contains(strings.ToLower(r.Heading), want) || used+len(r.Content) > itemTextChars {
			continue
		}
		n := k.citeChunk(r.ID, r.Heading, r.Anchor, r.Path, r.CommitSha, r.Repo)
		fmt.Fprintf(&b, "[%d] %s\n%s\n\n", n, r.Heading, prompts.ExternalContent(r.Path, r.Content))
		k.nb.Record(n, r.Heading+"\n"+r.Content)
		used += len(r.Content)
	}
	if b.Len() == 0 {
		return agent.Output{}, fmt.Errorf("no section of %s matches %q; call read_doc without section to list them", rows[0].Path, in.Section)
	}
	return agent.Output{Text: internalNote + "\n\n" + strings.TrimSpace(b.String()), Summary: "read " + rows[0].Path}, nil
}

func (k *toolkit) readMemoryBank(ctx context.Context, args json.RawMessage) (agent.Output, error) {
	in, err := decode[struct {
		File string `json:"file"`
	}](args)
	if err != nil {
		return agent.Output{}, err
	}
	f, err := store.New(k.s.Pool).GetMemoryBankFile(ctx, store.GetMemoryBankFileParams{WorkspaceID: k.ws, Name: strings.TrimSuffix(in.File, ".md")})
	if errors.Is(err, pgx.ErrNoRows) {
		// Not an error: the memory bank is optional and builds incrementally.
		return agent.Output{Text: fmt.Sprintf("The memory bank has no %s file yet. Use search_knowledge and read_doc for the docs themselves, or answer from other sources.", in.File), Summary: "not available yet"}, nil
	}
	if err != nil {
		return agent.Output{}, err
	}
	commit := ""
	if f.CommitSha != nil && len(*f.CommitSha) >= 7 {
		commit = " at commit " + (*f.CommitSha)[:7]
	}
	header := fmt.Sprintf("%s.md, generated from the docs%s on %s. A summary for orientation: cite doc sections (search_knowledge, read_doc), not this file.\n\n",
		f.Name, commit, f.UpdatedAt.UTC().Format("2006-01-02"))
	return agent.Output{Text: header + prompts.ExternalContent("memory bank", f.Content), Summary: f.Name + ".md"}, nil
}

// unusedKnowledgeTools lists the internal-knowledge tools a workspace has
// nothing for: the doc tools without synced docs, and read_memory_bank
// without memory bank files. The knowledge base is optional, so these are
// left out rather than offered and failing.
func (s *Service) unusedKnowledgeTools(ctx context.Context, ws uuid.UUID) ([]string, error) {
	q := store.New(s.Pool)
	counts, err := q.KnowledgeCounts(ctx, store.KnowledgeCountsParams{WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	files, err := q.ListMemoryBank(ctx, ws)
	if err != nil {
		return nil, err
	}
	var out []string
	if counts.Docs == 0 {
		out = append(out, ToolSearchKnowledge, ToolReadDoc)
	}
	if len(files) == 0 {
		out = append(out, ToolReadMemoryBank)
	}
	return out, nil
}
