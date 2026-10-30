// files_links.go — the files a report links to (#46, D207). A report often
// comes with files to act on: the PDF it summarizes, the .docx it drafted,
// the CSV behind a chart. render_html and the document previews record, in
// their render step, every link of the report that names a file —
// links: [{href, path}], the href as written and the session file it is —
// and the tile's preview turns exactly those into downloads of
// GET /runs/{id}/dl/<path> (handlers_files.go); every other link stays
// stripped (frameDoc, agent.js).
//
// A link resolves where the report is: a report shown from the sandbox
// links the files beside it there, and those are copied into the session
// files as the report itself is (copyName: a copy keeps its own name, so
// the href and the session file it becomes can differ — hence the map); a
// session report links session files, and — when a sandbox is bound — a
// file it doesn't have is looked for in the sandbox's working directory.
// session:<key> names a session file anywhere; an absolute path or
// sandbox:<path> a sandbox file. Only files that exist in the session
// after copying are listed; what couldn't be copied is said in the tool's
// result and the step's `skipped`.
package main

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// reportLink is one entry of a render step's links.
type reportLink struct {
	Href string `json:"href"` // as the report writes it
	Path string `json:"path"` // the session file it resolves to
}

// linkSkip is a file link that couldn't be offered, and why.
type linkSkip struct {
	Href string `json:"href"`
	Why  string `json:"why"`
}

const (
	maxReportHrefs = 400              // hrefs read from one report
	maxReportLinks = 32               // distinct files one render follows
	linkCopyBudget = 60 * time.Second // copying the sandbox's linked files, in all
)

// htmlHrefs: the href of every <a> and <area> in src, in document order,
// each once — as a browser's DOM gives it (entities decoded, the first of
// duplicate attributes).
func htmlHrefs(src string) []string {
	z := html.NewTokenizer(strings.NewReader(src))
	seen := map[string]bool{}
	var out []string
	for len(out) < maxReportHrefs {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		if tt != html.StartTagToken && tt != html.SelfClosingTagToken {
			continue
		}
		name, attrs := z.TagName()
		if tag := string(name); tag != "a" && tag != "area" {
			continue
		}
		for attrs {
			var k, v []byte
			k, v, attrs = z.TagAttr()
			if string(k) == "href" {
				if h := string(v); !seen[h] {
					seen[h] = true
					out = append(out, h)
				}
				break
			}
		}
	}
	return out
}

var schemeRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)

// reportAt is where a report is: a sandbox file's directory (an absolute
// path) or a session file's ("." at the top).
type reportAt struct {
	sandbox bool
	dir     string
}

// linkRef reads a report's href as the file it names. ok false: not a file
// link (#fragment, http:, mailto:, //host, data:…) or nothing a file can be
// (../ above the session files). bare: a relative path (a session report's
// may then be looked for in the sandbox).
func linkRef(href string, at reportAt, sbx bool) (r fileRef, bare, ok bool) {
	h := strings.TrimSpace(href)
	if i := strings.IndexAny(h, "?#"); i >= 0 { // a file is named by its path
		h = h[:i]
	}
	if h == "" || strings.HasPrefix(h, "//") || strings.ContainsRune(h, '\\') {
		return r, false, false
	}
	unesc := func(s string) (string, bool) {
		p, err := url.PathUnescape(s)
		return p, err == nil && p != "" && !strings.ContainsAny(p, "\x00\n\r")
	}
	switch {
	case strings.HasPrefix(h, "session:"):
		p, okp := unesc(strings.TrimPrefix(h, "session:"))
		if !okp {
			return r, false, false
		}
		k, err := normReplPath(p)
		return fileRef{key: k}, false, err == nil
	case strings.HasPrefix(h, "sandbox:"), strings.HasPrefix(h, "file://"):
		p, okp := unesc(strings.TrimPrefix(strings.TrimPrefix(h, "sandbox:"), "file://"))
		if !okp || !sbx {
			return r, false, false
		}
		if at.sandbox && !looksSandbox(p) { // beside the report, as a relative link
			p = path.Join(at.dir, p)
		}
		return fileRef{sandbox: true, path: p}, false, true
	case schemeRE.MatchString(h):
		return r, false, false
	}
	p, okp := unesc(h)
	if !okp {
		return r, false, false
	}
	switch {
	case strings.HasPrefix(p, "/"): // an absolute path is the sandbox's
		return fileRef{sandbox: true, path: path.Clean(p)}, false, sbx
	case at.sandbox:
		return fileRef{sandbox: true, path: path.Join(at.dir, p)}, false, true
	}
	k, err := normReplPath(path.Join(at.dir, p))
	return fileRef{key: k}, true, err == nil
}

// sessionLinkHref: a document's link that names a file of the session
// (relative, or session:) — the document previews keep these (#46).
func sessionLinkHref(u string) bool {
	_, _, ok := linkRef(u, reportAt{dir: "."}, false)
	return ok
}

// reportLinks follows the file links of src, the report f shows (render_html
// or a document's preview; tool names the copies' source). from: the
// report's sandbox path when it was copied from the sandbox, else "". sbx:
// the sandbox may be read (render_html in a conversation with one bound).
func (ag *Agent) reportLinks(ctx context.Context, run *Run, cfg Config, f *ReplFile, src, from, tool string, sbx bool) (links []reportLink, skipped []linkSkip, copied []string) {
	at := reportAt{dir: path.Dir(f.Path)}
	if from != "" {
		at = reportAt{sandbox: true, dir: path.Dir(from)}
	}
	ctx, cancel := context.WithTimeout(ctx, linkCopyBudget)
	defer cancel()
	type target struct {
		sandbox bool
		p       string
	}
	done := map[target]string{} // a target followed → its session key ("" failed)
	why := map[target]string{}
	for _, href := range htmlHrefs(src) {
		r, bare, ok := linkRef(href, at, sbx)
		if !ok {
			continue
		}
		if !r.sandbox { // a session file; a bare one missing may be the sandbox's
			if _, err := ag.db.replFile(run.ID, r.key); err == nil {
				links = append(links, reportLink{Href: href, Path: r.key})
				continue
			} else if !bare || !sbx {
				skipped = append(skipped, linkSkip{href, "no session file " + r.key})
				continue
			}
			r = fileRef{sandbox: true, path: "./" + r.key}
		}
		t := target{true, r.path}
		key, seen := done[t]
		if !seen {
			if len(done) >= maxReportLinks {
				skipped = append(skipped, linkSkip{href, fmt.Sprintf("over the %d files one render follows", maxReportLinks)})
				continue
			}
			if ctx.Err() != nil {
				skipped = append(skipped, linkSkip{href, "out of time copying the linked files"})
				continue
			}
			var err error
			var fresh bool
			key, fresh, err = ag.copyLinked(ctx, run, cfg, r.path, tool)
			done[t] = key
			if err != nil {
				why[t] = err.Error()
			} else if fresh {
				copied = append(copied, key)
			}
		}
		if key == "" {
			skipped = append(skipped, linkSkip{href, why[t]})
			continue
		}
		links = append(links, reportLink{Href: href, Path: key})
	}
	return links, skipped, copied
}

// copyLinked copies the sandbox file p, which a report links, into the
// session files (under its own name, copyName — a repeated render updates
// the same copy); fresh: its content was written now.
func (ag *Agent) copyLinked(ctx context.Context, run *Run, cfg Config, p, tool string) (key string, fresh bool, err error) {
	ctx = sbxCtx(ctx, run, cfg, tool)
	use, abs, err := ag.sandboxFile(ctx, run, cfg, p)
	if err != nil {
		return "", false, err
	}
	f, _, unchanged, err := ag.fetchSandboxFile(ctx, run, use, abs, ag.copyName(run.ID, use, abs), tool, false)
	if err != nil {
		return "", false, err
	}
	return f.Path, !unchanged, nil
}

// linkWords says, in a tool's result, what became of a report's file links.
func linkWords(links []reportLink, skipped []linkSkip, copied []string) string {
	var b strings.Builder
	if len(links) > 0 {
		seen := map[string]bool{}
		var files []string
		for _, l := range links {
			if !seen[l.Path] {
				seen[l.Path] = true
				files = append(files, l.Path)
			}
		}
		fmt.Fprintf(&b, "\nlinked files the human can download from the preview: %s", strings.Join(files, ", "))
		if len(copied) > 0 {
			fmt.Fprintf(&b, " (copied from the sandbox now: %s)", strings.Join(copied, ", "))
		}
	}
	for i, s := range skipped {
		if i == 8 {
			fmt.Fprintf(&b, "\n… and %d more links not offered", len(skipped)-i)
			break
		}
		fmt.Fprintf(&b, "\nnot offered as a download: %s — %s", s.Href, s.Why)
	}
	return b.String()
}
