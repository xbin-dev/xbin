package docscheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The tile-deployments vocabulary guard. Tile deployments, live reload and
// checkpoints reuse nothing another xbin feature already named: a word such
// as "snapshot", "instance" or "pause the tile" already means something to a
// builder or a coding agent, so the feature never says it. The rules are the
// glossary's banned words, checked where the feature talks to people: the
// strings of its web modules and bx commands, its builder page, and every
// docs section whose heading names live reload or tile deployments.

// wordingSources are the code files whose user-visible strings are checked:
// the terminal window's live reload modules and the bx commands. A glob that
// matches nothing yet (a later file) is fine; all of them matching nothing
// is not.
var wordingSources = []string{
	"web/deploy-state.js",
	"web/deploy-panel.js",
	"web/frame-deploy.js",
	"web/bx-deploy.js",
	"workspace-template/tiles/admin/tabs/deployments.js",
	"cmd/bx/livereload.go",
	"cmd/bx/deploy.go",
	"cmd/bx/deployclient.go",
	"cmd/bx/deployment.go",
}

// wordingPage is the builder page, checked whole.
const wordingPage = "docs/tile-deployments.md"

// wordingDocs are the markdown files whose sections headed by
// wordingHeading are checked.
var wordingDocs = []string{"docs/*.md", "docs/overview/*.md", "workspace-template/AGENTS.md"}

var wordingHeading = regexp.MustCompile(`(?i)live reload|tile deployment`)

// wordingAllowed are phrases that keep a word's existing meaning; they are
// blanked before the banned words are looked for (lowercase).
var wordingAllowed = []string{
	"interface instance", "template instance", "instance token", "instance principal",
	"net slot", "interface slot", "slot:",
	"--version",
}

// wordingBanned are the glossary's words this feature must not use, each
// with what to say instead.
var wordingBanned = []struct {
	re  *regexp.Regexp
	say string
}{
	{regexp.MustCompile(`\bidentit(y|ies)\b`), "a tile deployment shares its tile's path and principal: it is not an identity"},
	{regexp.MustCompile(`\bsub-?deployments?\b`), `"tile deployment"`},
	{regexp.MustCompile(`\bhot[- ]?reload\w*`), `"live reload"`},
	{regexp.MustCompile(`\benvironments?\b`), `"deployment" (environment is the setup layer and env vars)`},
	{regexp.MustCompile(`\bstag(e|es|ing)\b`), `"deployment"`},
	{regexp.MustCompile(`\bsnapshots?\b`), `"checkpoint"`},
	{regexp.MustCompile(`\brevisions?\b`), `"checkpoint"`},
	{regexp.MustCompile(`\bversions?\b`), `"checkpoint"`},
	{regexp.MustCompile(`\bpreviews?\b`), `"deployment URL" (preview is bx preview and ?preview=1)`},
	{regexp.MustCompile(`\bchannels?\b`), `"deployment" (channels are chat channels)`},
	{regexp.MustCompile(`\blanes?\b`), `"deployment" (lanes are the agent template's)`},
	{regexp.MustCompile(`\bfreez\w*|\bfrozen\b`), `"pinned"`},
	{regexp.MustCompile(`\bfork\w*`), `"deployment" (a fork is a new tile at a new path)`},
	{regexp.MustCompile(`\bprod\b|\bproduction\b`), `"the primary"`},
	{regexp.MustCompile(`\bautomations?\b`), `"deliveries"`},
	{regexp.MustCompile(`\bunpaus\w*`), `"resume live reload"`},
	{regexp.MustCompile(`\bslots?\b`), `"deployment" (a slot is an interface slot)`},
	{regexp.MustCompile(`\binstances?\b`), `"deployment" (an instance is an interface or template instance)`},
	{regexp.MustCompile(`\bpaus\w*\s+(the\s+|a\s+|this\s+|that\s+|your\s+|its\s+|each\s+|every\s+)?tiles?([^'’\w]|$)`), `"pause live reload" (pausing a tile is lifecycle disable)`},
	{regexp.MustCompile(`\btiles?\s+(is\s+|are\s+|was\s+|were\s+)?paused\b`), `"live reload is paused" (a paused tile is a disabled one)`},
	{regexp.MustCompile(`\blive\s+deployments?\b`), `"the primary" or "the live reload target"`},
	{regexp.MustCompile(`\bswap\w*\s+(the\s+)?(deployments?|primar(y|ies))\b`), `"reassign the primary"`},
	{regexp.MustCompile(`\bsource\s+deployments?\b`), `"from"`},
	{regexp.MustCompile(`\bpublish\w*\s+(a\s+|the\s+)?(deployments?|checkpoints?)\b`), `"deploy"`},
	{regexp.MustCompile(`\b(copy|clone)\s+of\s+(the\s+|a\s+|this\s+)?(tiles?|deployments?)\b`), "a tile deployment runs the same tile, not a copy of it"},
	{regexp.MustCompile(`⏸`), "⏸ is Disable: pausing live reload is 📌"},
	{regexp.MustCompile(`[⟲⟳]`), "⟲ resets the terminal layer and ⟳ refetches: Reload now is ⇡"},
}

// covers P2 NP-15-9 — the feature's words: the terminal window's strings,
// bx's, the builder page and the docs sections about live reload and tile
// deployments use none of the glossary's banned words; the builder page
// exists and is linked from docs/index.md and docs/elements.md; and the
// default claim stays: a tile that never opts in has no deploy step.
func TestDeploymentWording(t *testing.T) {
	var problems []string
	check := func(where, text string) {
		low := strings.ToLower(text)
		for _, a := range wordingAllowed {
			low = strings.ReplaceAll(low, a, strings.Repeat(" ", len(a)))
		}
		for _, b := range wordingBanned {
			if m := b.re.FindString(low); m != "" {
				problems = append(problems, fmt.Sprintf("%s: %q in %q — say %s", where, m, clip(text), b.say))
			}
		}
	}

	scanned := 0
	for _, pat := range wordingSources {
		files, err := filepath.Glob(filepath.Join(repo, pat))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			scanned++
			rel := filepath.ToSlash(strings.TrimPrefix(f, repo+string(filepath.Separator)))
			var lits []strLit
			if strings.HasSuffix(f, ".go") {
				lits = goStrings(t, f)
			} else {
				lits = jsStrings(t, f)
			}
			for _, l := range lits {
				if strings.ContainsAny(l.text, " \t\n") { // prose, not a key or an id
					check(fmt.Sprintf("%s:%d", rel, l.line), l.text)
				}
			}
		}
	}
	if scanned < 3 {
		t.Fatalf("scanned only %d of the feature's code files — were they renamed? update wordingSources", scanned)
	}

	page, err := os.ReadFile(filepath.Join(repo, wordingPage))
	if err != nil {
		t.Fatalf("the builder page: %v", err)
	}
	for i, line := range strings.Split(string(page), "\n") {
		check(fmt.Sprintf("%s:%d", wordingPage, i+1), line)
	}

	sections := 0
	for _, pat := range wordingDocs {
		files, err := filepath.Glob(filepath.Join(repo, pat))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			rel := filepath.ToSlash(strings.TrimPrefix(f, repo+string(filepath.Separator)))
			if rel == wordingPage {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range headedSections(string(b), wordingHeading) {
				sections++
				for i, line := range s.lines {
					check(fmt.Sprintf("%s:%d", rel, s.start+i), line)
				}
			}
		}
	}
	if sections < 3 {
		t.Errorf("found only %d docs sections headed by live reload or tile deployments — did the headings change?", sections)
	}

	// The builder page is where every other page sends a reader.
	for _, f := range []string{"docs/index.md", "docs/elements.md"} {
		b, err := os.ReadFile(filepath.Join(repo, f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "/docs/tile-deployments.md") {
			problems = append(problems, f+": doesn't link /docs/tile-deployments.md")
		}
	}
	// Glossary rule 4: the default claim stays true, and stays said.
	for _, f := range []string{"workspace-template/AGENTS.md", wordingPage} {
		b, err := os.ReadFile(filepath.Join(repo, f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(strings.ToLower(string(b)), "no deploy step") {
			problems = append(problems, f+`: lost "no deploy step" — a tile that never opts in still has none`)
		}
	}

	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		return s[:117] + "…"
	}
	return s
}

// strLit is one string literal: its value and the line it starts on.
type strLit struct {
	text string
	line int
}

// goStrings returns f's string literals, leaving out import paths and
// struct tags.
func goStrings(t *testing.T, f string) []strLit {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	skip := map[*ast.BasicLit]bool{}
	for _, im := range file.Imports {
		skip[im.Path] = true
	}
	var out []strLit
	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			if n.Tag != nil {
				skip[n.Tag] = true
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING && !skip[n] {
				if v, err := strconv.Unquote(n.Value); err == nil {
					out = append(out, strLit{v, fset.Position(n.Pos()).Line})
				}
			}
		}
		return true
	})
	return out
}

// jsStrings returns the string literals of an ES module: quoted strings, and
// template literals with each ${…} read as one placeholder word (strings
// inside the expression are returned on their own). Comments and regular
// expression literals are skipped.
func jsStrings(t *testing.T, f string) []strLit {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	l := &jsLexer{src: []rune(string(b)), line: 1}
	l.code(false)
	if l.err != "" {
		t.Fatalf("%s:%d: %s", f, l.line, l.err)
	}
	return l.out
}

type jsLexer struct {
	src  []rune
	i    int
	line int
	out  []strLit
	err  string
	prev rune   // the last significant code character
	word string // the last identifier or keyword
}

func (l *jsLexer) peek(k int) rune {
	if l.i+k < len(l.src) {
		return l.src[l.i+k]
	}
	return 0
}

func (l *jsLexer) next() rune {
	c := l.src[l.i]
	l.i++
	if c == '\n' {
		l.line++
	}
	return c
}

// regexAfter is where a '/' starts a regular expression rather than a
// division: after an operator or an opening bracket, or a keyword.
const regexAfter = "(,=:[!&|?{};+-*%<>~^"

var regexKeywords = map[string]bool{"return": true, "typeof": true, "case": true, "do": true, "else": true,
	"in": true, "of": true, "new": true, "delete": true, "void": true, "throw": true, "yield": true, "await": true}

// code lexes code until the end, or, inside a template's ${…}, until its
// closing brace.
func (l *jsLexer) code(inTemplate bool) {
	depth := 0
	for l.i < len(l.src) && l.err == "" {
		c := l.peek(0)
		switch {
		case c == '/' && l.peek(1) == '/':
			for l.i < len(l.src) && l.peek(0) != '\n' {
				l.next()
			}
		case c == '/' && l.peek(1) == '*':
			l.next()
			l.next()
			for l.i < len(l.src) && !(l.peek(0) == '*' && l.peek(1) == '/') {
				l.next()
			}
			if l.i < len(l.src) {
				l.next()
				l.next()
			}
		case c == '\'' || c == '"':
			l.quoted(l.next())
			l.prev, l.word = 'a', ""
		case c == '`':
			l.next()
			l.template()
			l.prev, l.word = 'a', ""
		case c == '/' && (l.prev == 0 || strings.ContainsRune(regexAfter, l.prev) || regexKeywords[l.word]):
			l.regex()
			l.prev, l.word = 'a', ""
		case c == '{':
			depth++
			l.next()
			l.prev, l.word = c, ""
		case c == '}':
			if inTemplate && depth == 0 {
				l.next()
				return
			}
			depth--
			l.next()
			l.prev, l.word = c, ""
		case c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
			start := l.i
			for l.i < len(l.src) {
				d := l.peek(0)
				if !(d == '_' || d == '$' || d >= 'a' && d <= 'z' || d >= 'A' && d <= 'Z' || d >= '0' && d <= '9') {
					break
				}
				l.next()
			}
			l.word, l.prev = string(l.src[start:l.i]), 'a'
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			l.next()
		default:
			l.next()
			l.prev, l.word = c, ""
		}
	}
	if inTemplate && l.err == "" {
		l.err = "unterminated ${ in a template literal"
	}
}

func (l *jsLexer) quoted(q rune) {
	line := l.line
	var sb strings.Builder
	for l.i < len(l.src) {
		c := l.next()
		switch {
		case c == '\\' && l.i < len(l.src):
			sb.WriteRune(l.next())
		case c == q:
			l.out = append(l.out, strLit{sb.String(), line})
			return
		case c == '\n':
			l.err = "unterminated string"
			return
		default:
			sb.WriteRune(c)
		}
	}
	l.err = "unterminated string"
}

func (l *jsLexer) template() {
	line := l.line
	var sb strings.Builder
	for l.i < len(l.src) && l.err == "" {
		c := l.next()
		switch {
		case c == '\\' && l.i < len(l.src):
			sb.WriteRune(l.next())
		case c == '`':
			l.out = append(l.out, strLit{sb.String(), line})
			return
		case c == '$' && l.peek(0) == '{':
			l.next()
			l.prev, l.word = '{', ""
			l.code(true)
			sb.WriteString(" X ")
		default:
			sb.WriteRune(c)
		}
	}
	if l.err == "" {
		l.err = "unterminated template literal"
	}
}

func (l *jsLexer) regex() {
	l.next() // the opening '/'
	class := false
	for l.i < len(l.src) {
		c := l.next()
		switch {
		case c == '\\' && l.i < len(l.src):
			l.next()
		case c == '[':
			class = true
		case c == ']':
			class = false
		case c == '/' && !class:
			for l.i < len(l.src) && (l.peek(0) >= 'a' && l.peek(0) <= 'z') {
				l.next()
			}
			return
		case c == '\n':
			l.err = "unterminated regular expression"
			return
		}
	}
}

// section is a markdown section: its lines, the first being its heading,
// which is line start of the file.
type section struct {
	start int
	lines []string
}

// headedSections returns the sections of md whose heading matches re, each
// up to the next heading of its level or above. Lines inside code fences
// are never headings.
func headedSections(md string, re *regexp.Regexp) []section {
	lines := strings.Split(md, "\n")
	type head struct{ at, level int }
	var heads []head
	fence := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if n := len(line) - len(strings.TrimLeft(line, "#")); n > 0 && n <= 6 && strings.HasPrefix(line[n:], " ") {
			heads = append(heads, head{i, n})
		}
	}
	var out []section
	for k, h := range heads {
		if !re.MatchString(lines[h.at]) {
			continue
		}
		end := len(lines)
		for _, g := range heads[k+1:] {
			if g.level <= h.level {
				end = g.at
				break
			}
		}
		out = append(out, section{h.at + 1, lines[h.at:end]})
	}
	return out
}
