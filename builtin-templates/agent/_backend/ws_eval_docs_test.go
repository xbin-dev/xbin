package main

import (
	"strings"
	"testing"
)

// The feedback run (2026-11-17), from the model's own account and the
// transcripts: docs_read's "section" was described as "a section heading"
// while xbind wants the section's slug — the heading in words was "no such
// section on this page", three calls to get one section; and docs_search,
// which wants every word in one section, answered a question asked in a
// sentence with "nothing in xbin's docs matches … — try other words" while
// two of its words found the page. A heading reads the section; a miss
// names the sections there are; a sentence gets the closest sections and
// is told how the search works.
func TestWsEvalDocsToolsTakeWhatAModelSends(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	fx.x.mu.Lock()
	fx.x.docs = map[string]string{
		"overview/11-interfaces.md": "# Interfaces\nHow a tile reads another tile.\n## Binding mechanics\nA slot is bound to a provider.\n## What the component sees\nThe page reads its bound slot through xbin.iface.\n",
		"overview/07-users-orgs.md": "# Users\nPeople and orgs.\n## Roles\nA role is granted.\n",
	}
	fx.x.mu.Unlock()
	// a heading in words, and the slug, both read the section
	for _, sec := range []string{"What the component sees", "what-the-component-sees", "  What The Component Sees "} {
		got := fx.tool(t, s, "docs_read", map[string]any{"path": "overview/11-interfaces.md", "section": sec})
		if !strings.Contains(got, "xbin.iface") || strings.Contains(got, "A slot is bound") {
			t.Errorf("docs_read section %q: %s", sec, got)
		}
	}
	// a section that isn't there: the answer names the ones that are
	if got := fx.tool(t, s, "docs_read", map[string]any{"path": "overview/11-interfaces.md", "section": "Pseudo slots"}); !strings.HasPrefix(got, "error: ") ||
		!strings.Contains(got, `has no section "Pseudo slots"`) || !strings.Contains(got, "binding-mechanics · what-the-component-sees") {
		t.Errorf("a section that isn't on the page: %s", got)
	}
	// the page read whole lists its sections by the names that read them
	if got := fx.tool(t, s, "docs_read", map[string]any{"path": "overview/11-interfaces.md"}); !strings.Contains(got, "(sections: binding-mechanics · what-the-component-sees)") {
		t.Errorf("the page's list of sections: %s", got)
	}
	// a sentence: no section has every word; the closest are given, and the answer says how the search works
	got := fx.tool(t, s, "docs_search", map[string]any{"query": "how does a frontend page read its bound slot through xbin.iface javascript"})
	if !strings.Contains(got, "No section of xbin's docs has all of those words") || !strings.Contains(got, "overview/11-interfaces.md") || strings.Contains(got, "07-users-orgs") {
		t.Errorf("a question in a sentence: %s", got)
	}
	// words nothing has: still a plain "nothing"
	if got := fx.tool(t, s, "docs_search", map[string]any{"query": "zeppelin moorings quarterly"}); !strings.Contains(got, "nothing in xbin's docs matches") {
		t.Errorf("words no page has: %s", got)
	}
	// and a query that matches is answered as before
	if got := fx.tool(t, s, "docs_search", map[string]any{"query": "bound slot"}); !strings.HasPrefix(got, "1 section(s) of xbin's docs:") {
		t.Errorf("a query that matches: %s", got)
	}
}
