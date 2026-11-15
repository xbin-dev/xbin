package server

// docsapi.go — the embedded docs on /api/ (D228 part C0): a person's
// workspace agent can't read /docs/ (its credential resolves on /api/
// only), and a search beats fetching pages whole into a model's context.
//
//	GET /docs/search?q=&limit=    ranked sections: {results: [{path, title,
//	                              section, heading, score, snippet}]}
//	GET /docs/page?path=&section= one page, or one section of it, capped
//
// For any signed-in person, their agent and a tile's own principals: the
// docs are what every builder reads. The index is built once, in memory,
// from s.DocsFS: each page cut at its headings. Nothing here reads a path
// from the request — a page is looked up in the index by name.

import (
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Bounds of the docs reads.
const (
	docsMaxQuery   = 200
	docsMaxTerms   = 8
	docsMaxResults = 20
	docsSnippet    = 240
	docsMaxPage    = 64 << 10
	docsMaxFile    = 2 << 20
)

type docSection struct {
	path, title   string // the page, its first heading
	slug, heading string // "" for the page's lead
	body          string
	lower, hlower string
}

type docsIndex struct {
	once     sync.Once
	sections []docSection
	pages    map[string][]int // path → its sections, in order
}

func (s *Server) registerDocsAPI() {
	s.RegisterAPI("GET /docs/search", s.apiDocsSearch)
	s.RegisterAPI("GET /docs/page", s.apiDocsPage)
}

func slugOf(heading string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(heading) {
		switch {
		case unicode.IsLetter(c) || unicode.IsDigit(c):
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *Server) docs() *docsIndex {
	ix := &s.docsIx
	ix.once.Do(func() {
		ix.pages = map[string][]int{}
		if s.DocsFS == nil {
			return
		}
		_ = fs.WalkDir(s.DocsFS, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".md") {
				return nil
			}
			if fi, err := d.Info(); err != nil || fi.Size() > docsMaxFile {
				return nil
			}
			raw, err := fs.ReadFile(s.DocsFS, p)
			if err != nil {
				return nil
			}
			title, cur := "", docSection{path: p}
			var lines []string
			flush := func() {
				cur.body = strings.TrimSpace(strings.Join(lines, "\n"))
				if cur.body != "" || cur.heading != "" {
					cur.title, cur.lower, cur.hlower = title, strings.ToLower(cur.body), strings.ToLower(cur.heading)
					ix.pages[p] = append(ix.pages[p], len(ix.sections))
					ix.sections = append(ix.sections, cur)
				}
				lines = nil
			}
			fence := false
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "```") {
					fence = !fence
				}
				if h := strings.TrimLeft(line, "#"); !fence && len(h) < len(line) && strings.HasPrefix(h, " ") {
					h = strings.TrimSpace(h)
					if title == "" {
						title = h
					}
					flush()
					cur = docSection{path: p, heading: h, slug: slugOf(h)}
					continue
				}
				lines = append(lines, line)
			}
			flush()
			for _, i := range ix.pages[p] { // the title, known only once the page is read
				ix.sections[i].title = title
			}
			return nil
		})
	})
	return ix
}

func docsReader(w http.ResponseWriter, r *http.Request) bool {
	if p := auth.PrincipalOf(r); p.UserID == "" && p.Component == "" && !p.Owner {
		WriteError(w, http.StatusUnauthorized, "sign in to read the docs here")
		return false
	}
	return true
}

func snippetOf(sec docSection, term string) string {
	body := sec.body
	at := strings.Index(sec.lower, term)
	if at < 0 {
		at = 0
	}
	start := max(0, at-docsSnippet/3)
	for start > 0 && !utf8Start(body[start]) {
		start--
	}
	end := min(len(body), start+docsSnippet)
	for end < len(body) && !utf8Start(body[end]) {
		end--
	}
	out := strings.Join(strings.Fields(body[start:end]), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(body) {
		out += "…"
	}
	return out
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// apiDocsSearch — GET /docs/search?q=&limit=: the sections that match
// best. q is at most 200 bytes and 8 words (more are dropped); limit at
// most 20 (default 8). A word in a page's title scores 10, in a section's
// heading 5, each time in its body 1 (at most 5); every word must match
// somewhere in the section or its headings.
func (s *Server) apiDocsSearch(w http.ResponseWriter, r *http.Request) {
	if !docsReader(w, r) {
		return
	}
	q := r.URL.Query().Get("q")
	if len(q) > docsMaxQuery {
		WriteError(w, http.StatusBadRequest, "a query is at most "+strconv.Itoa(docsMaxQuery)+" bytes")
		return
	}
	terms := strings.FieldsFunc(strings.ToLower(q), func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '-' && c != '_' && c != '/' && c != '.'
	})
	if len(terms) > docsMaxTerms {
		terms = terms[:docsMaxTerms]
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 8
	}
	limit = min(limit, docsMaxResults)
	type hit struct {
		i, score int
	}
	var hits []hit
	ix := s.docs()
	if len(terms) > 0 {
		for i, sec := range ix.sections {
			score, tl := 0, strings.ToLower(sec.title)
			for _, t := range terms {
				n := 0
				if strings.Contains(tl, t) {
					n += 10
				}
				if strings.Contains(sec.hlower, t) {
					n += 5
				}
				n += min(strings.Count(sec.lower, t), 5)
				if n == 0 {
					score = 0
					break
				}
				score += n
			}
			if score > 0 {
				hits = append(hits, hit{i, score})
			}
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].score > hits[b].score })
	out := []map[string]any{}
	for _, h := range hits[:min(len(hits), limit)] {
		sec := ix.sections[h.i]
		out = append(out, map[string]any{"path": sec.path, "title": sec.title, "section": sec.slug, "heading": sec.heading, "score": h.score, "snippet": snippetOf(sec, terms[0])})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"q": q, "results": out, "total": len(hits)})
}

// apiDocsPage — GET /docs/page?path=&section=: one page as markdown, or
// one section (by the slug search answers) with what is under its heading
// up to the next one. At most 64 KiB: {path, title, section?, markdown,
// truncated?, sections: [{section, heading}]}. 404 for a path or section
// that isn't in the index.
func (s *Server) apiDocsPage(w http.ResponseWriter, r *http.Request) {
	if !docsReader(w, r) {
		return
	}
	ix := s.docs()
	path, section := r.URL.Query().Get("path"), r.URL.Query().Get("section")
	path = strings.TrimPrefix(strings.TrimPrefix(path, "/"), "docs/")
	idx, ok := ix.pages[path]
	if !ok || len(path) > 300 {
		WriteError(w, http.StatusNotFound, "no such docs page (GET /api/xbin/docs/search finds them)")
		return
	}
	var b strings.Builder
	toc := []map[string]string{}
	found := section == ""
	for _, i := range idx {
		sec := ix.sections[i]
		if sec.heading != "" {
			toc = append(toc, map[string]string{"section": sec.slug, "heading": sec.heading})
		}
		if section != "" && sec.slug != section {
			continue
		}
		found = true
		if sec.heading != "" {
			b.WriteString("## " + sec.heading + "\n\n")
		}
		b.WriteString(sec.body + "\n\n")
	}
	if !found {
		WriteError(w, http.StatusNotFound, "no such section on this page")
		return
	}
	md, truncated := b.String(), false
	if len(md) > docsMaxPage {
		cut := docsMaxPage
		for cut > 0 && !utf8Start(md[cut]) {
			cut--
		}
		md, truncated = md[:cut], true
	}
	out := map[string]any{"path": path, "title": ix.sections[idx[0]].title, "markdown": md, "sections": toc}
	if section != "" {
		out["section"] = section
	}
	if truncated {
		out["truncated"] = true // read it section by section
	}
	WriteJSON(w, http.StatusOK, out)
}
