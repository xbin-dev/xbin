package main

import (
	"regexp"
	"strings"
)

// The static half of `bx lint --native` reads a tile's native modules
// without running them: which modules they import, and whether their
// templates (or the entry's string literals) carry raw colours where the
// vocabulary takes tokens. A full JS parser would be overkill; jsScan blanks
// comments and template text out of the source so the import patterns only
// see code, and keeps each template literal's static text (with its tag) for
// the colour check. It knows strings, template literals with nested ${…},
// comments and — heuristically, by the preceding token — regex literals.

// jsTemplate is one static chunk of a template literal: html`<a x="1">${v}</a>`
// yields "<a x=\"1\">" and "</a>", both tagged "html".
type jsTemplate struct {
	Tag  string
	Text string
	Line int
}

// jsString is a quoted string literal in code (not inside a template's text).
type jsString struct {
	Val  string
	Line int
}

type jsImport struct {
	Spec string
	Line int
}

type jsScanned struct {
	Code      string // the source with comments and template text blanked (newlines kept)
	Templates []jsTemplate
	Strings   []jsString
}

// jsScan walks src once. Unterminated constructs end at EOF (the browser
// run reports real syntax errors).
func jsScan(src string) jsScanned {
	var out jsScanned
	code := []byte(src)
	blank := func(from, to int) {
		for i := from; i < to && i < len(code); i++ {
			if code[i] != '\n' {
				code[i] = ' '
			}
		}
	}
	line := 1
	lineAt := func(from, to int) { line += strings.Count(src[from:to], "\n") }

	// open template literals whose ${…} we are inside: the brace depth of
	// that expression (its text is consumed by readTemplateText at once)
	type tpl struct {
		tag   string
		depth int
	}
	var stack []tpl
	lastSig := byte(0) // last significant code char, for regex detection
	lastWord := ""     // last identifier, for `return /re/` and tags
	i := 0
	n := len(src)

	// readTemplateText scans template text from i (just past ` or }) up to
	// the closing ` or ${, recording the chunk.
	readTemplateText := func(tag string) (end int, closed bool) {
		start, startLine := i, line
		j := i
		for j < n {
			c := src[j]
			if c == '\\' {
				j += 2
				continue
			}
			if c == '`' {
				break
			}
			if c == '$' && j+1 < n && src[j+1] == '{' {
				break
			}
			j++
		}
		if j > n {
			j = n
		}
		out.Templates = append(out.Templates, jsTemplate{Tag: tag, Text: src[start:j], Line: startLine})
		blank(start, j)
		lineAt(start, j)
		if j >= n {
			return n, true
		}
		if src[j] == '`' {
			return j + 1, true
		}
		return j + 2, false // past ${
	}

	for i < n {
		c := src[i]
		// inside a ${…} of a template: track braces to find its end
		if len(stack) > 0 && stack[len(stack)-1].depth >= 0 {
			top := &stack[len(stack)-1]
			if c == '{' {
				top.depth++
			} else if c == '}' {
				if top.depth == 0 {
					i++
					end, closed := readTemplateText(top.tag)
					i = end
					if closed {
						stack = stack[:len(stack)-1]
					} else {
						top.depth = 0
					}
					lastSig, lastWord = ')', ""
					continue
				}
				top.depth--
			}
		}
		switch {
		case c == '\n':
			line++
			i++
			continue
		case c == ' ' || c == '\t' || c == '\r':
			i++
			continue
		case c == '/' && i+1 < n && src[i+1] == '/':
			j := strings.IndexByte(src[i:], '\n')
			if j < 0 {
				j = n - i
			}
			blank(i, i+j)
			i += j
			continue
		case c == '/' && i+1 < n && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			end := n
			if j >= 0 {
				end = i + 2 + j + 2
			}
			blank(i, end)
			lineAt(i, end)
			i = end
			continue
		case c == '\'' || c == '"':
			start, startLine := i, line
			j := i + 1
			for j < n && src[j] != c && src[j] != '\n' {
				if src[j] == '\\' && j+1 < n && src[j+1] != '\n' {
					j++
				}
				j++
			}
			out.Strings = append(out.Strings, jsString{Val: src[start+1 : j], Line: startLine})
			i = j
			if j < n && src[j] == c {
				i++
			}
			lastSig, lastWord = c, ""
			continue
		case c == '`':
			tag := lastWord
			if lastSig != 0 && !isIdentByte(lastSig) {
				tag = ""
			}
			i++
			end, closed := readTemplateText(tag)
			i = end
			if !closed {
				stack = append(stack, tpl{tag: tag, depth: 0})
			}
			lastSig, lastWord = ')', ""
			continue
		case c == '/' && regexAllowed(lastSig, lastWord):
			start := i
			j := i + 1
			inClass := false
			for j < n && src[j] != '\n' {
				if src[j] == '\\' && j+1 < n && src[j+1] != '\n' {
					j += 2
					continue
				}
				if src[j] == '[' {
					inClass = true
				} else if src[j] == ']' {
					inClass = false
				} else if src[j] == '/' && !inClass {
					break
				}
				j++
			}
			if j < n && src[j] == '/' {
				j++
				for j < n && isIdentByte(src[j]) { // flags
					j++
				}
			}
			blank(start, j)
			i = j
			lastSig, lastWord = ')', ""
			continue
		case isIdentByte(c):
			j := i
			for j < n && isIdentByte(src[j]) {
				j++
			}
			lastWord = src[i:j]
			lastSig = src[j-1]
			i = j
			continue
		}
		lastSig, lastWord = c, ""
		i++
	}
	out.Code = string(code)
	return out
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// regexAllowed: a '/' starts a regex literal after an operator/punctuator or
// a keyword like return, not after a value (identifier, number, ')' or ']').
func regexAllowed(lastSig byte, lastWord string) bool {
	if lastWord != "" {
		switch lastWord {
		case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "throw", "case", "do", "else", "yield", "await":
			return true
		}
		return false
	}
	switch lastSig {
	case 0, '(', ',', '=', ':', '[', '!', '&', '|', '?', '{', '}', ';', '+', '-', '*', '%', '<', '>', '~', '^':
		return true
	}
	return false
}

var (
	reStaticImport  = regexp.MustCompile(`(?:^|[^.\w$])import\b\s*(?:[^'"();]*?\bfrom\s*)?['"]([^'"\n]+)['"]`)
	reExportFrom    = regexp.MustCompile(`(?:^|[^.\w$])export\b\s*(?:\*(?:\s*as\s+[\w$]+)?|\{[^}]*\})\s*from\s*['"]([^'"\n]+)['"]`)
	reDynamicImport = regexp.MustCompile(`(?:^|[^.\w$])import\s*\(\s*['"]([^'"\n]+)['"]\s*[,)]`)
)

// jsImports lists the module specifiers a scanned module imports: static
// imports, re-exports and dynamic import() with a literal specifier, in
// source order, each once.
func jsImports(s jsScanned) []jsImport {
	type hit struct {
		at   int
		spec string
	}
	var hits []hit
	for _, re := range []*regexp.Regexp{reStaticImport, reExportFrom, reDynamicImport} {
		for _, m := range re.FindAllStringSubmatchIndex(s.Code, -1) {
			hits = append(hits, hit{m[2], s.Code[m[2]:m[3]]})
		}
	}
	// source order (three regexes interleave)
	for a := 1; a < len(hits); a++ {
		for b := a; b > 0 && hits[b].at < hits[b-1].at; b-- {
			hits[b], hits[b-1] = hits[b-1], hits[b]
		}
	}
	seen := map[string]bool{}
	var out []jsImport
	for _, h := range hits {
		if seen[h.spec] {
			continue
		}
		seen[h.spec] = true
		out = append(out, jsImport{Spec: h.spec, Line: 1 + strings.Count(s.Code[:h.at], "\n")})
	}
	return out
}

// reColour matches a whole value that is a CSS colour literal: #rgb, #rgba,
// #rrggbb, #rrggbbaa, or a colour function.
var reColour = regexp.MustCompile(`(?i)^\s*(?:#(?:[0-9a-f]{3}|[0-9a-f]{4}|[0-9a-f]{6}|[0-9a-f]{8})|(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color-mix)\(.*\))\s*$`)

// reColourFn finds a colour function anywhere in a value (style strings).
var reColourFn = regexp.MustCompile(`(?i)\b(?:rgba?|hsla?|hwb|oklab|oklch|color-mix)\(`)

// reAttr matches name="v", name='v' and name=v in template text.
var reAttr = regexp.MustCompile(`([A-Za-z_:][-\w:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'<>/=` + "`" + `]+))`)

type colourHit struct {
	Line  int
	Attr  string // "" for a string literal
	Value string
}

// rawColours reports raw colours in a scanned module: attributes of html“
// templates whose value is a colour (or holds a colour function), and — when
// strings is true (the native entry itself) — string literals that are
// nothing but a colour. The vocabulary has no colour props: colour comes
// from tokens (tone=…), which the app maps to the platform's palette.
func rawColours(s jsScanned, strings_ bool) []colourHit {
	var out []colourHit
	for _, t := range s.Templates {
		if t.Tag != "html" {
			continue
		}
		for _, m := range reAttr.FindAllStringSubmatchIndex(t.Text, -1) {
			name := t.Text[m[2]:m[3]]
			val := ""
			for g := 4; g <= 8; g += 2 {
				if m[g] >= 0 {
					val = t.Text[m[g]:m[g+1]]
					break
				}
			}
			if reColour.MatchString(val) || reColourFn.MatchString(val) {
				out = append(out, colourHit{Line: t.Line + strings.Count(t.Text[:m[0]], "\n"), Attr: name, Value: val})
			}
		}
	}
	if strings_ {
		for _, str := range s.Strings {
			if reColour.MatchString(str.Val) {
				out = append(out, colourHit{Line: str.Line, Value: str.Val})
			}
		}
	}
	return out
}
