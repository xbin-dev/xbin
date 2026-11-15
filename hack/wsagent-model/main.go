// wsagent-model generates the workspace agent's short map of xbin — the
// mental model its prompt carries (docs/workspace-agent.md §What the agent
// knows about xbin) — from the docs themselves, so the map can't drift from
// them: for each concept below it takes one passage of a doc page (a
// section's opening paragraph, or the paragraph that holds a marker),
// strips the markup, cuts it at a sentence within its budget, and names the
// page docs_read opens for the rest.
//
//	go run ./hack/wsagent-model            # write the file (hack/wsagent-model.sh)
//	go run ./hack/wsagent-model -check     # fail when the file isn't what the docs give
//
// The file is checked in (builtin-templates/agent/_backend/ws_model.md) and
// embedded by the template's backend; TestModelIsCurrent fails when a doc
// passage changed and the file was not regenerated, when a passage is gone,
// and when the map is over its budget.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// out is the generated file, from the repository's root; budget its size.
const (
	out    = "builtin-templates/agent/_backend/ws_model.md"
	budget = 6 << 10
)

// passage is one concept: the doc page (under docs/), where in it —
// Heading's first prose paragraph (Skip: that many paragraphs later; "" is
// the page's opening), or the paragraph or list item containing Marker —
// and how many characters it may take.
type passage struct {
	Label   string
	File    string
	Heading string
	Skip    int
	Marker  string
	Max     int
}

var passages = []passage{
	{Label: "Components and tiles", File: "overview/03-components.md", Heading: "Vocabulary: component, element, tile", Max: 420},
	{Label: "A tile is a directory", File: "overview/01-model.md", Heading: "Directory = component = identity", Max: 260},
	{Label: "Screens", File: "overview/04-frontend.md", Marker: "**Screens & cards**", Max: 110},
	{Label: "People's access levels (read < write < terminal)", File: "overview/07-users-orgs.md", Heading: "Access levels: read < write < terminal", Max: 380},
	{Label: "Orgs", File: "overview/07-users-orgs.md", Heading: "Organizations (D19)", Max: 300},
	{Label: "Partitions", File: "partitions.md", Marker: "xbind then runs **one backend instance per person**", Max: 170},
	{Label: "Grants", File: "overview/06-authorization.md", Max: 420},
	{Label: "Interfaces and bindings", File: "overview/11-interfaces.md", Max: 460},
	{Label: "Sandboxes", File: "overview/08-sandbox.md", Max: 360},
	{Label: "Terminals", File: "overview/09-terminals.md", Max: 360},
	{Label: "Tile agents", File: "overview/09-terminals.md", Heading: "Agent sessions (D74)", Max: 420},
	{Label: "The workspace agent", File: "workspace-agent.md", Max: 520},
	{Label: "What the workspace agent can't do", File: "workspace-agent.md", Heading: "What it never does", Max: 620},
}

var (
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdRef     = regexp.MustCompile(`\s*\((?:[A-Z]{1,3}-?\d+[a-z]?(?:,\s*)?)+\)`)
	mdRefTail = regexp.MustCompile(`,\s*[A-Z]{1,3}-?\d+[a-z]?\)`)
	mdAside   = regexp.MustCompile(`\s*\((?:this chapter|\d\d-[a-z-]+\.md)\)`)
	mdEmph    = regexp.MustCompile(`\*([^*\s][^*]*)\*`)
	mdSpaces  = regexp.MustCompile(`\s+`)
	sentence  = regexp.MustCompile(`[.!?:;]["”)]?(\s|$)`)
	listStart = regexp.MustCompile(`^\s*(?:[-*]|\d+\.)\s+`)
)

// blocks splits a page into its blocks under each heading: paragraphs and
// list items, with code fences, tables, quotes and "Related:" lines left out.
func blocks(page string) map[string][]string {
	by := map[string][]string{}
	head, fence := "", false
	var cur []string
	flush := func() {
		if s := strings.TrimSpace(strings.Join(cur, " ")); s != "" {
			by[head] = append(by[head], s)
		}
		cur = nil
	}
	for _, line := range strings.Split(page, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "```"):
			flush()
			fence = !fence
		case fence:
		case strings.HasPrefix(t, "#"):
			flush()
			if strings.HasPrefix(t, "# ") {
				head = "" // the page's opening
			} else {
				head = strings.TrimSpace(strings.TrimLeft(t, "#"))
			}
		case t == "" || strings.HasPrefix(t, "|") || strings.HasPrefix(t, ">") || strings.HasPrefix(t, "**Related:**"):
			flush()
		case listStart.MatchString(line):
			flush()
			cur = append(cur, t)
		default:
			cur = append(cur, t)
		}
	}
	flush()
	return by
}

// plain is a block without its markup, on one line: links as their text,
// emphasis marks gone (code spans keep their text as it is), and the
// asides that only make sense on the page — decision ids, "(this
// chapter)", a sibling page named in brackets — left out.
func plain(s string) string {
	s = listStart.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	parts := strings.Split(s, "`")
	for i := 0; i < len(parts); i += 2 { // outside code spans
		parts[i] = mdEmph.ReplaceAllString(strings.ReplaceAll(parts[i], "**", ""), "$1")
	}
	s = strings.Join(parts, "")
	s = mdAside.ReplaceAllString(s, "")
	s = mdRefTail.ReplaceAllString(s, ")")
	s = mdRef.ReplaceAllString(s, "")
	return strings.TrimSpace(mdSpaces.ReplaceAllString(s, " "))
}

// cut is s at the last sentence end within max characters, ending in a
// full stop.
func cut(s string, max int) string {
	if len(s) > max {
		end := 0
		for _, m := range sentence.FindAllStringIndex(s[:max], -1) {
			end = m[0] + 1
		}
		if end == 0 {
			return strings.TrimSpace(s[:max]) + "…"
		}
		s = s[:end]
	}
	if t := strings.TrimRight(s, ":;"); t != s {
		return t + "."
	}
	return s
}

// find is p's passage in the docs under dir.
func find(dir string, p passage) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p.File)))
	if err != nil {
		return "", err
	}
	by := blocks(string(raw))
	if p.Marker != "" {
		for _, bs := range by {
			for _, b := range bs {
				if strings.Contains(b, p.Marker) {
					return cut(plain(b), p.Max), nil
				}
			}
		}
		return "", fmt.Errorf("%s: no paragraph holds %q any more", p.File, p.Marker)
	}
	bs := by[p.Heading]
	var prose []string
	for _, b := range bs {
		if !listStart.MatchString(b) {
			prose = append(prose, b)
		}
	}
	if p.Skip >= len(prose) {
		return "", fmt.Errorf("%s: no paragraph %d under %q any more", p.File, p.Skip+1, p.Heading)
	}
	return cut(plain(prose[p.Skip]), p.Max), nil
}

// generate is the map the docs under dir give.
func generate(dir string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("<!-- Generated from docs/ by hack/wsagent-model.sh (the passages: hack/wsagent-model/main.go). Don't edit: change the docs and run the script. -->\n")
	for _, p := range passages {
		text, err := find(dir, p)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "- %s: %s (more: docs_read %s)\n", p.Label, text, p.File)
	}
	if b.Len() > budget {
		return nil, fmt.Errorf("the map is %d bytes, over its budget of %d: shorten a passage's Max", b.Len(), budget)
	}
	return b.Bytes(), nil
}

func main() {
	root := flag.String("root", ".", "the repository's root")
	check := flag.Bool("check", false, "compare with the checked-in file instead of writing it")
	flag.Parse()
	got, err := generate(filepath.Join(*root, "docs"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "wsagent-model:", err)
		os.Exit(1)
	}
	path := filepath.Join(*root, filepath.FromSlash(out))
	if *check {
		have, _ := os.ReadFile(path)
		if !bytes.Equal(have, got) {
			fmt.Fprintf(os.Stderr, "wsagent-model: %s isn't what the docs give: run hack/wsagent-model.sh\n", out)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(path, got, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "wsagent-model:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%d of %d bytes)\n", out, len(got), budget)
}
