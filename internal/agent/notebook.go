package agent

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Source is something the model may cite as [N].
type Source struct {
	N         int        `json:"n"`
	Kind      string     `json:"kind"` // corpus | web | page | internal | brand
	RawItemID *uuid.UUID `json:"raw_item_id,omitempty"`
	URL       string     `json:"url"`
	Title     string     `json:"title"`
}

// Dataset is tabular data a tool produced; charts may only use datasets.
type Dataset struct {
	ID      string           `json:"id"`
	Origin  string           `json:"origin"` // the tool and arguments that produced it
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
}

// Chart is a chart spec stored in reports.charts.
type Chart struct {
	ID      string           `json:"id"`
	Type    string           `json:"type"` // line | bar | pie | table
	Title   string           `json:"title"`
	X       string           `json:"x"`
	Y       []string         `json:"y"`
	Data    []map[string]any `json:"data"`
	Dataset string           `json:"dataset"`
}

// ChartTypes are the supported chart types.
var ChartTypes = []string{"line", "bar", "pie", "table"}

// Notebook records what a run has seen: numbered sources for citations, and
// datasets and charts built from tool results. It is safe for concurrent use.
type Notebook struct {
	mu       sync.Mutex
	sources  []Source
	byKey    map[string]int // dedupe key -> index
	datasets map[string]Dataset
	charts   []Chart
	evidence map[int][]string // source number -> texts the tools showed for it
}

func NewNotebook() *Notebook {
	return &Notebook{byKey: map[string]int{}, datasets: map[string]Dataset{}, evidence: map[int][]string{}}
}

// maxEvidenceChars bounds the text kept per source for claim checking.
const maxEvidenceChars = 12000

// Record keeps text a tool showed the model for source n (a snippet, an
// item, a page), so claims citing n can later be checked against it.
// Repeated text is kept once; each source keeps at most maxEvidenceChars.
func (nb *Notebook) Record(n int, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	nb.mu.Lock()
	defer nb.mu.Unlock()
	if n < 1 || n > len(nb.sources) {
		return
	}
	used := 0
	for _, t := range nb.evidence[n] {
		if t == text || strings.Contains(t, text) {
			return
		}
		used += len(t)
	}
	if left := maxEvidenceChars - used; left <= 0 {
		return
	} else if len(text) > left {
		for left > 0 && !utf8.RuneStart(text[left]) {
			left--
		}
		text = text[:left]
	}
	nb.evidence[n] = append(nb.evidence[n], text)
}

// Evidence returns the text recorded for source n, or "" if none.
func (nb *Notebook) Evidence(n int) string {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return strings.Join(nb.evidence[n], "\n\n")
}

func urlKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	u.Fragment = ""
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	return host + strings.TrimSuffix(u.EscapedPath(), "/") + "?" + u.RawQuery
}

// Cite registers a source and returns its number. The same item or URL
// keeps its number; a page stored later for a cited URL fills in its id.
func (nb *Notebook) Cite(s Source) int {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	keys := []string{"url:" + urlKey(s.URL)}
	if s.Kind == "internal" {
		// Doc sections share a page URL and differ by anchor: keep it.
		keys = []string{"internal:" + s.URL}
	}
	if s.RawItemID != nil {
		keys = append(keys, "item:"+s.RawItemID.String())
	}
	for _, k := range keys {
		if i, ok := nb.byKey[k]; ok {
			cur := &nb.sources[i]
			if cur.RawItemID == nil && s.RawItemID != nil {
				cur.RawItemID, cur.Kind = s.RawItemID, s.Kind
				nb.byKey["item:"+s.RawItemID.String()] = i
			}
			if cur.Title == "" {
				cur.Title = s.Title
			}
			return cur.N
		}
	}
	s.N = len(nb.sources) + 1
	nb.sources = append(nb.sources, s)
	for _, k := range keys {
		nb.byKey[k] = len(nb.sources) - 1
	}
	return s.N
}

// Source returns source n.
func (nb *Notebook) Source(n int) (Source, bool) {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	if n < 1 || n > len(nb.sources) {
		return Source{}, false
	}
	return nb.sources[n-1], true
}

// Sources returns every source in number order.
func (nb *Notebook) Sources() []Source {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return slices.Clone(nb.sources)
}

// AddDataset stores tool data and returns its id (d1, d2, ...).
func (nb *Notebook) AddDataset(origin string, columns []string, rows []map[string]any) string {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	id := fmt.Sprintf("d%d", len(nb.datasets)+1)
	nb.datasets[id] = Dataset{ID: id, Origin: origin, Columns: columns, Rows: rows}
	return id
}

// Dataset returns a dataset by id.
func (nb *Notebook) Dataset(id string) (Dataset, bool) {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	d, ok := nb.datasets[id]
	return d, ok
}

// MakeChart builds a chart from a stored dataset: the model chooses what to
// show, the data always comes from the tool result.
func (nb *Notebook) MakeChart(dataset, typ, title, x string, y []string, limit int) (Chart, error) {
	d, ok := nb.Dataset(dataset)
	if !ok {
		return Chart{}, fmt.Errorf("unknown dataset %q: call a data tool first and use the dataset id it returns", dataset)
	}
	if !slices.Contains(ChartTypes, typ) {
		return Chart{}, fmt.Errorf("type must be one of %s", strings.Join(ChartTypes, ", "))
	}
	if typ != "table" && !slices.Contains(d.Columns, x) {
		return Chart{}, fmt.Errorf("x must be one of the dataset's columns: %s", strings.Join(d.Columns, ", "))
	}
	if len(y) == 0 && typ != "table" {
		return Chart{}, fmt.Errorf("y needs at least one column")
	}
	for _, c := range y {
		if !slices.Contains(d.Columns, c) {
			return Chart{}, fmt.Errorf("y column %q is not in the dataset: %s", c, strings.Join(d.Columns, ", "))
		}
	}
	rows := d.Rows
	if limit > 0 && limit < len(rows) {
		rows = rows[:limit]
	}
	nb.mu.Lock()
	defer nb.mu.Unlock()
	c := Chart{ID: fmt.Sprintf("c%d", len(nb.charts)+1), Type: typ, Title: strings.TrimSpace(title), X: x, Y: y, Data: rows, Dataset: d.ID}
	nb.charts = append(nb.charts, c)
	return c, nil
}

// Charts returns the charts made so far.
func (nb *Notebook) Charts() []Chart {
	nb.mu.Lock()
	defer nb.mu.Unlock()
	return slices.Clone(nb.charts)
}
