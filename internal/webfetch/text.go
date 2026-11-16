package webfetch

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// skipped are elements whose content is never text a reader sees.
var skipped = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Template: true,
	atom.Svg: true, atom.Math: true, atom.Iframe: true, atom.Object: true, atom.Canvas: true,
	atom.Select: true, atom.Button: true, atom.Head: true,
}

// blocks are elements that start and end a line.
var blocks = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Br: true, atom.Li: true, atom.Ul: true, atom.Ol: true,
	atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
	atom.Tr: true, atom.Table: true, atom.Section: true, atom.Article: true, atom.Header: true,
	atom.Footer: true, atom.Nav: true, atom.Aside: true, atom.Main: true, atom.Pre: true,
	atom.Blockquote: true, atom.Hr: true, atom.Dl: true, atom.Dt: true, atom.Dd: true,
	atom.Figure: true, atom.Figcaption: true, atom.Form: true, atom.Fieldset: true, atom.Details: true,
	atom.Summary: true, atom.Address: true,
}

// HTMLText is an HTML page as text: its title, and what a reader sees —
// scripts, styles and other non-text content dropped, comments gone, block
// elements on lines of their own, a heading marked with #, a list item with
// -, a link's target after its words when it is a web address (resolved
// against base). Whitespace is collapsed; nothing is interpreted.
func HTMLText(page string, base *url.URL) (title, text string) {
	z := html.NewTokenizer(strings.NewReader(page))
	z.SetMaxBuf(MaxBody + 4096)
	var b strings.Builder
	var line strings.Builder
	skip := 0
	inTitle, inPre := false, false
	var titleB strings.Builder
	var href string
	flush := func() {
		l := strings.TrimSpace(line.String())
		line.Reset()
		if l != "" {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			flush()
			return clean(titleB.String(), true), strings.TrimSpace(collapseBlank(b.String()))
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			a := atom.Lookup(name)
			switch {
			case a == atom.Title:
				inTitle = tt == html.StartTagToken
				continue
			case skipped[a]:
				if tt == html.StartTagToken {
					skip++
				}
				continue
			}
			if skip > 0 {
				continue
			}
			if blocks[a] {
				flush()
			}
			switch a {
			case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
				line.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
			case atom.Li:
				line.WriteString("- ")
			case atom.Pre:
				inPre = true
			case atom.A:
				href = ""
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "href" {
						href = linkOf(string(v), base)
					}
				}
			case atom.Img:
				for hasAttr {
					var k, v []byte
					k, v, hasAttr = z.TagAttr()
					if string(k) == "alt" && strings.TrimSpace(string(v)) != "" {
						line.WriteString(" [image: " + clean(string(v), true) + "] ")
					}
				}
			case atom.Td, atom.Th:
				line.WriteString(" | ")
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			a := atom.Lookup(name)
			switch {
			case a == atom.Title:
				inTitle = false
				continue
			case skipped[a]:
				if skip > 0 {
					skip--
				}
				continue
			}
			if skip > 0 {
				continue
			}
			if a == atom.A && href != "" {
				line.WriteString(" <" + href + ">")
				href = ""
			}
			if a == atom.Pre {
				inPre = false
			}
			if blocks[a] {
				flush()
			}
		case html.TextToken:
			t := string(z.Text())
			switch {
			case inTitle:
				titleB.WriteString(t)
			case skip > 0:
			case inPre:
				for i, l := range strings.Split(plain(t), "\n") {
					if i > 0 {
						flush()
					}
					line.WriteString(l)
				}
			default:
				line.WriteString(clean(t, false))
			}
		}
	}
}

// linkOf is an href as a reader can follow it: an absolute http(s) URL,
// or "" for anything else (a script, a mail address, a fragment).
func linkOf(href string, base *url.URL) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if base != nil {
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	u.Fragment = ""
	s := u.String()
	if len(s) > MaxURL {
		return ""
	}
	return s
}

// clean collapses a run of white space to one space (trimmed when trim)
// and drops control characters.
func clean(s string, trim bool) string {
	s = plain(s)
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == ' ' {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		if space && b.Len() == 0 && !trim {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	if space && !trim {
		b.WriteByte(' ')
	}
	if trim {
		return strings.TrimSpace(b.String())
	}
	return b.String()
}

// collapseBlank keeps at most one empty line between lines.
func collapseBlank(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
