// docs_md.go — the Markdown a model writes a document in (doc_write, D201),
// read into blocks and inline runs that the .docx writer and the HTML
// preview both draw from. CommonMark's everyday subset: ATX headings,
// paragraphs (two trailing spaces or a backslash for a line break), - * +
// and 1. lists nested by indentation, > quotes, ``` fences, --- rules, GFM
// pipe tables, and **bold**, *italic*, `code`, [links](https://…) and
// ![images](chart.png) inline — an image alone in its paragraph is a figure.
// Anything else is text, never an error: a model's document always writes.
package main

import (
	"regexp"
	"strconv"
	"strings"
)

type mdBlock struct {
	Kind   string // h p ul ol code quote hr table img
	Level  int    // a heading's 1–6
	Text   string // h, p, quote: inline Markdown; code: the code
	Lang   string // a fence's info word
	Items  []mdItem
	Start  int // an ordered list's first number
	Header []string
	Align  []string // table column alignment: "", "left", "center", "right"
	Rows   [][]string
	Src    string // img: the session file it names
	Alt    string
}

type mdItem struct {
	Text string
	Sub  *mdBlock // a nested list, or nil
}

var (
	mdHeadingRE = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	mdFenceRE   = regexp.MustCompile("^[ \t]{0,3}(```+|~~~+)[ \t]*([^`\\s]*)")
	mdRuleRE    = regexp.MustCompile(`^[ \t]{0,3}((\*[ \t]*){3,}|(-[ \t]*){3,}|(_[ \t]*){3,})$`)
	mdListRE    = regexp.MustCompile(`^([ \t]*)([-*+]|(\d{1,9})[.)])[ \t]+(.*)$`)
	mdSepRE     = regexp.MustCompile(`^[ \t]*\|?[ \t]*:?-{1,}:?[ \t]*(\|[ \t]*:?-{1,}:?[ \t]*)*\|?[ \t]*$`)
	mdImgOnlyRE = regexp.MustCompile(`^!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)$`)
)

// parseMarkdown reads a document into blocks.
func parseMarkdown(src string) []mdBlock {
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	var out []mdBlock
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		text := joinPara(para)
		para = nil
		if m := mdImgOnlyRE.FindStringSubmatch(strings.TrimSpace(text)); m != nil {
			out = append(out, mdBlock{Kind: "img", Alt: m[1], Src: m[2]})
			return
		}
		out = append(out, mdBlock{Kind: "p", Text: text})
	}
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		trim := strings.TrimSpace(ln)
		switch {
		case trim == "":
			flush()
		case mdFenceRE.MatchString(ln):
			flush()
			m := mdFenceRE.FindStringSubmatch(ln)
			fence := m[1]
			var code []string
			for i++; i < len(lines); i++ {
				if strings.HasPrefix(strings.TrimSpace(lines[i]), fence[:3]) && strings.Trim(strings.TrimSpace(lines[i]), string(fence[0])) == "" {
					break
				}
				code = append(code, lines[i])
			}
			out = append(out, mdBlock{Kind: "code", Lang: m[2], Text: strings.Join(code, "\n")})
		case mdHeadingRE.MatchString(trim):
			flush()
			m := mdHeadingRE.FindStringSubmatch(trim)
			out = append(out, mdBlock{Kind: "h", Level: len(m[1]), Text: m[2]})
		case mdRuleRE.MatchString(ln) && !(len(para) > 0 && strings.Trim(trim, "-") == ""):
			flush()
			out = append(out, mdBlock{Kind: "hr"})
		case strings.HasPrefix(trim, ">"):
			flush()
			var q []string
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), ">"); i++ {
				q = append(q, strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(lines[i]), ">"), " "))
			}
			i--
			out = append(out, mdBlock{Kind: "quote", Text: joinPara(q)})
		case strings.Contains(trim, "|") && i+1 < len(lines) && mdSepRE.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			flush()
			t := mdBlock{Kind: "table", Header: tableCells(ln)}
			for _, c := range tableCells(lines[i+1]) {
				c = strings.TrimSpace(c)
				switch {
				case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
					t.Align = append(t.Align, "center")
				case strings.HasSuffix(c, ":"):
					t.Align = append(t.Align, "right")
				case strings.HasPrefix(c, ":"):
					t.Align = append(t.Align, "left")
				default:
					t.Align = append(t.Align, "")
				}
			}
			for i += 2; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
				t.Rows = append(t.Rows, tableCells(lines[i]))
			}
			i--
			out = append(out, t)
		case mdListRE.MatchString(ln) && (len(para) == 0 || !strings.HasPrefix(ln, " ")):
			flush()
			var b mdBlock
			b, i = parseList(lines, i)
			out = append(out, b)
			i--
		default:
			para = append(para, ln)
		}
	}
	flush()
	return out
}

// parseList reads one list starting at line i (and the lists nested in it,
// by indentation); it returns the list and the first line after it.
func parseList(lines []string, i int) (mdBlock, int) {
	m := mdListRE.FindStringSubmatch(lines[i])
	indent := indentOf(m[1])
	b := mdBlock{Kind: "ul"}
	if m[3] != "" {
		b.Kind = "ol"
		b.Start, _ = strconv.Atoi(m[3])
	}
	for i < len(lines) {
		ln := lines[i]
		if strings.TrimSpace(ln) == "" {
			// a blank line ends the list unless the list goes on after it
			j := i + 1
			for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
				j++
			}
			if j < len(lines) {
				if mm := mdListRE.FindStringSubmatch(lines[j]); mm != nil && indentOf(mm[1]) >= indent {
					i = j
					continue
				}
			}
			return b, i
		}
		mm := mdListRE.FindStringSubmatch(ln)
		switch {
		case mm != nil && indentOf(mm[1]) == indent && (mm[3] != "") == (b.Kind == "ol"):
			b.Items = append(b.Items, mdItem{Text: mm[4]})
			i++
		case mm != nil && indentOf(mm[1]) > indent && len(b.Items) > 0:
			var sub mdBlock
			sub, i = parseList(lines, i)
			last := &b.Items[len(b.Items)-1]
			if last.Sub == nil {
				last.Sub = &sub
			} else {
				last.Sub.Items = append(last.Sub.Items, sub.Items...)
			}
		case mm == nil && indentOf(leadingWS(ln)) > indent && len(b.Items) > 0:
			// a continuation line of the item before
			last := &b.Items[len(b.Items)-1]
			last.Text += " " + strings.TrimSpace(ln)
			i++
		default:
			return b, i
		}
	}
	return b, i
}

func leadingWS(s string) string { return s[:len(s)-len(strings.TrimLeft(s, " \t"))] }

func indentOf(ws string) int {
	n := 0
	for _, r := range ws {
		if r == '\t' {
			n += 4
		} else {
			n++
		}
	}
	return n
}

// joinPara joins a paragraph's lines: a line ending in two spaces or a
// backslash keeps its break ("\n"); the others join with a space.
func joinPara(ls []string) string {
	var b strings.Builder
	for i, l := range ls {
		hard := strings.HasSuffix(l, "  ") || strings.HasSuffix(l, `\`)
		l = strings.TrimSpace(strings.TrimSuffix(l, `\`))
		b.WriteString(l)
		if i < len(ls)-1 {
			if hard {
				b.WriteString("\n")
			} else {
				b.WriteString(" ")
			}
		}
	}
	return b.String()
}

// tableCells splits a pipe-table row (escaped \| stays in its cell).
func tableCells(ln string) []string {
	s := strings.TrimSpace(ln)
	s = strings.TrimPrefix(s, "|")
	if strings.HasSuffix(s, "|") && !strings.HasSuffix(s, `\|`) {
		s = s[:len(s)-1]
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '|':
			cur.WriteByte('|')
			i++
		case s[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(cells, strings.TrimSpace(cur.String()))
}

// mdRun is a stretch of inline text with one formatting.
type mdRun struct {
	Text         string
	Bold, Italic bool
	Code         bool
	Link         string // a link's target
	Image        string // an inline image's source (Text is its alt)
	Break        bool   // a hard line break (Text empty)
}

// parseInline reads inline Markdown into runs.
func parseInline(s string) []mdRun {
	var out []mdRun
	var bold, italic bool
	var cur strings.Builder
	emit := func() {
		if cur.Len() > 0 {
			out = append(out, mdRun{Text: cur.String(), Bold: bold, Italic: italic})
			cur.Reset()
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\\' && i+1 < len(rs) && strings.ContainsRune("\\`*_[]()#+-.!|~>", rs[i+1]):
			cur.WriteRune(rs[i+1])
			i++
		case r == '\n':
			emit()
			out = append(out, mdRun{Break: true})
		case r == '`':
			n := 1
			for i+n < len(rs) && rs[i+n] == '`' {
				n++
			}
			fence := strings.Repeat("`", n)
			rest := string(rs[i+n:])
			if j := strings.Index(rest, fence); j >= 0 {
				emit()
				out = append(out, mdRun{Text: strings.TrimSpace(rest[:j]), Code: true, Bold: bold, Italic: italic})
				i += n + len([]rune(rest[:j])) + n - 1
			} else {
				cur.WriteString(fence)
				i += n - 1
			}
		case (r == '!' && i+1 < len(rs) && rs[i+1] == '[') || r == '[':
			img := r == '!'
			start := i
			if img {
				start++
			}
			if text, target, end, ok := linkAt(rs, start); ok {
				emit()
				if img {
					out = append(out, mdRun{Text: text, Image: target})
				} else {
					for _, sub := range parseInline(text) {
						sub.Link = target
						sub.Bold = sub.Bold || bold
						sub.Italic = sub.Italic || italic
						out = append(out, sub)
					}
				}
				i = end
			} else {
				cur.WriteRune(r)
			}
		case (r == '*' || r == '_') && i+1 < len(rs) && rs[i+1] == r:
			if bold || hasCloser(rs, i+2, string([]rune{r, r})) {
				emit()
				bold = !bold
				i++
			} else {
				cur.WriteString(string([]rune{r, r}))
				i++
			}
		case r == '*' || (r == '_' && (i == 0 || !isWordRune(rs[i-1]))) || (r == '_' && italic):
			if italic || (i+1 < len(rs) && rs[i+1] != ' ' && hasCloser(rs, i+1, string(r))) {
				emit()
				italic = !italic
			} else {
				cur.WriteRune(r)
			}
		default:
			cur.WriteRune(r)
		}
	}
	emit()
	return out
}

func isWordRune(r rune) bool {
	return r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127
}

func hasCloser(rs []rune, from int, delim string) bool {
	return from < len(rs) && strings.Contains(string(rs[from:]), delim)
}

// linkAt reads [text](target) at rs[i] == '['.
func linkAt(rs []rune, i int) (text, target string, end int, ok bool) {
	depth := 0
	j := i
	for ; j < len(rs); j++ {
		if rs[j] == '[' {
			depth++
		} else if rs[j] == ']' {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if j >= len(rs)-1 || rs[j+1] != '(' {
		return "", "", 0, false
	}
	k := j + 2
	for ; k < len(rs) && rs[k] != ')'; k++ {
	}
	if k >= len(rs) {
		return "", "", 0, false
	}
	target = strings.TrimSpace(string(rs[j+2 : k]))
	if sp := strings.IndexAny(target, " \t"); sp > 0 { // drop a "title"
		target = target[:sp]
	}
	return string(rs[i+1 : j]), strings.Trim(target, "<>"), k, true
}

// plainInline is inline Markdown's text without its marks (titles, alt text).
func plainInline(s string) string {
	var b strings.Builder
	for _, r := range parseInline(s) {
		if r.Break {
			b.WriteString(" ")
			continue
		}
		b.WriteString(r.Text)
	}
	return b.String()
}

// safeLink: only links a reader may follow from a document we wrote.
func safeLink(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:")
}
