// Package match implements the keyword matcher used for connectors without
// server-side search (HN, RSS, Google Alerts, journalist digests).
//
// Query syntax (case-insensitive, word-boundary):
//
//	project management    both words must appear
//	"client portal"       exact phrase
//	trello OR asana       either alternative ("|" also works)
//	-jobs                 excludes items containing the term
//
// AND binds tighter than OR: `a b OR c` means (a AND b) OR c. Exclusions
// apply to the whole query.
package match

import (
	"regexp"
	"strings"
	"unicode"
)

// Matcher is a compiled query. The zero value matches everything.
type Matcher struct {
	groups  [][]*regexp.Regexp // OR of ANDs
	exclude []*regexp.Regexp
}

// Compile parses q. An empty or exclusion-only query matches everything
// not excluded.
func Compile(q string) *Matcher {
	m := &Matcher{}
	var group []*regexp.Regexp
	flush := func() {
		if len(group) > 0 {
			m.groups = append(m.groups, group)
			group = nil
		}
	}
	for _, tok := range tokenize(q) {
		switch {
		case tok.text == "OR" && !tok.quoted, tok.text == "|" && !tok.quoted:
			flush()
		case tok.negate:
			if re := termRegexp(tok.text); re != nil {
				m.exclude = append(m.exclude, re)
			}
		default:
			if re := termRegexp(tok.text); re != nil {
				group = append(group, re)
			}
		}
	}
	flush()
	return m
}

// AnyOf builds a matcher that matches any of terms (each may be a phrase),
// minus negatives. Used when a monitor has no query: the workspace's
// tracked keywords become the query.
func AnyOf(terms, negatives []string) *Matcher {
	m := &Matcher{}
	for _, t := range terms {
		if re := termRegexp(t); re != nil {
			m.groups = append(m.groups, []*regexp.Regexp{re})
		}
	}
	for _, n := range negatives {
		if re := termRegexp(n); re != nil {
			m.exclude = append(m.exclude, re)
		}
	}
	return m
}

// WithExclusions returns a copy that also excludes negatives.
func (m *Matcher) WithExclusions(negatives []string) *Matcher {
	out := &Matcher{groups: m.groups, exclude: append([]*regexp.Regexp{}, m.exclude...)}
	for _, n := range negatives {
		if re := termRegexp(n); re != nil {
			out.exclude = append(out.exclude, re)
		}
	}
	return out
}

// Match reports whether text satisfies the query.
func (m *Matcher) Match(text string) bool {
	for _, re := range m.exclude {
		if re.MatchString(text) {
			return false
		}
	}
	if len(m.groups) == 0 {
		return true
	}
	for _, g := range m.groups {
		ok := true
		for _, re := range g {
			if !re.MatchString(text) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// Terms returns the positive alternatives as plain search strings (each AND
// group joined by spaces, phrases quoted). Search connectors use this to
// turn a monitor query into backend queries.
func Terms(q string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, tok := range tokenize(q) {
		switch {
		case (tok.text == "OR" || tok.text == "|") && !tok.quoted:
			flush()
		case tok.negate:
		case tok.quoted:
			cur = append(cur, `"`+tok.text+`"`)
		default:
			cur = append(cur, tok.text)
		}
	}
	flush()
	return out
}

type token struct {
	text   string
	quoted bool
	negate bool
}

func tokenize(q string) []token {
	var toks []token
	r := []rune(q)
	for i := 0; i < len(r); {
		switch {
		case unicode.IsSpace(r[i]):
			i++
		default:
			neg := false
			if r[i] == '-' && i+1 < len(r) && !unicode.IsSpace(r[i+1]) {
				neg = true
				i++
			}
			if i < len(r) && r[i] == '"' {
				j := i + 1
				for j < len(r) && r[j] != '"' {
					j++
				}
				toks = append(toks, token{text: strings.TrimSpace(string(r[i+1 : min(j, len(r))])), quoted: true, negate: neg})
				i = j + 1
				continue
			}
			j := i
			for j < len(r) && !unicode.IsSpace(r[j]) {
				j++
			}
			toks = append(toks, token{text: string(r[i:j]), negate: neg})
			i = j
		}
	}
	return toks
}

// termRegexp matches a word or phrase case-insensitively on word
// boundaries; whitespace inside a phrase matches any whitespace run.
func termRegexp(term string) *regexp.Regexp {
	term = strings.TrimSpace(term)
	if term == "" {
		return nil
	}
	words := strings.Fields(term)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	body := strings.Join(words, `\s+`)
	// \b only works next to word characters; use lookaround-free guards.
	left, right := `(?:^|[^\pL\pN_])`, `(?:$|[^\pL\pN_])`
	return regexp.MustCompile(`(?i)` + left + body + right)
}
