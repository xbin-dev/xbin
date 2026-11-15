// coord_know_tools.go — the knowledge store's tools (API.md §Project wiki):
// wiki_search, wiki_read, wiki_list, wiki_history (reading) and
// wiki_write, wiki_edit, wiki_archive (writing; coord_know.go has the
// compare-and-swap). Who has them, and on which store, is decided on every
// call from the run as it is stored, never from arguments — by the
// Knowledge hook of the coordinator profile whose origin the run has
// (coord_profile.go). For Projects (project_wiki_role.go):
//
//   - a project's coordinator (depth 0) and its research subagents
//     (Config.CodeRead, projects_coord_research.go): read and write;
//   - a built-in task's own run (depth 0): read, and write only while its
//     turn is the dream round's (wiki_dreams.active, project_dream.go);
//   - nothing else — a coding agent reads the wiki from files
//     (project_wiki_sync.go) and writes it through its dream round's files.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func init() {
	turnEndHooks = append(turnEndHooks, wikiTurnEnd)
}

var wikiToolNames = map[string]bool{"wiki_search": true, "wiki_read": true, "wiki_list": true, "wiki_history": true,
	"wiki_write": true, "wiki_edit": true, "wiki_archive": true}

var wikiWrites = map[string]bool{"wiki_write": true, "wiki_edit": true, "wiki_archive": true}

const wikiReadPage = 12000 // wiki_read's page of a long body

// wikiToolSpecs are the reading tools, and the writing ones with write.
// The first sentence of each is pinned (tooldesc_test.go).
func wikiToolSpecs(write bool) []toolSpec {
	slug := strProp("the page's slug (lowercase words joined by dashes, like build-and-test; [[slug]] links name pages)")
	note := strProp("required: why you change it, in one line (shown in the page's history)")
	specs := []toolSpec{
		{Type: "function", Function: funcDef{Name: "wiki_search",
			Description: "Search this project's wiki — the notes its agents and people keep: how to build and test, traps, decisions — by words, for the best-matching pages with a snippet each. " +
				"wiki_read reads one in full.",
			Parameters: obj([]string{"query"}, map[string]any{
				"query": strProp("search words (plain words, stemmed; every word must match)"),
				"limit": intProp("hits (default 8, at most 20)"),
			})}},
		{Type: "function", Function: funcDef{Name: "wiki_read",
			Description: "Read a page of this project's wiki in full by its slug — notes that may be wrong, so verify what you rely on. " +
				"It records the version you saw: your next change to the page builds on it.",
			Parameters: obj([]string{"slug"}, map[string]any{
				"slug":   slug,
				"offset": intProp("character offset to start from, for a long page (default 0)"),
			})}},
		{Type: "function", Function: funcDef{Name: "wiki_list",
			Description: "List this project's wiki pages — slug, title, version, size, who changed it last — the index (slug `index`, its map) first.",
			Parameters:  obj(nil, map[string]any{})}},
		{Type: "function", Function: funcDef{Name: "wiki_history",
			Description: "A wiki page's versions, newest first: who changed it, when and why. " +
				"With version, that version's full text.",
			Parameters: obj([]string{"slug"}, map[string]any{
				"slug":    slug,
				"version": intProp("a version to read in full"),
			})}},
	}
	if !write {
		return specs
	}
	return append(specs,
		toolSpec{Type: "function", Function: funcDef{Name: "wiki_write",
			Description: "Create a page of this project's wiki, or replace a whole page — Markdown, [[slug]] links to other pages, at most 16 KB. " +
				"To replace an existing page, wiki_read it first: a write is refused when another agent changed the page since you read it (then read it again and merge). " +
				"Record verified facts with their paths, quoting identifiers and commands verbatim; never task status or anything stale next week. " +
				wikiProvenance + " The title is shown above the body: don't repeat it as a heading. The index page (slug `index`) is the wiki's short map. " +
				"(note required: one line, why)",
			Parameters: obj([]string{"slug", "body", "note"}, map[string]any{
				"slug":    slug,
				"title":   strProp("the page's title (default: kept, or the slug in words)"),
				"body":    strProp("the whole page, Markdown"),
				"note":    note,
				"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "where its facts come from: file paths, commands, commits, URLs (at most 20)"},
			})}},
		toolSpec{Type: "function", Function: funcDef{Name: "wiki_edit",
			Description: "Replace an exact piece of a wiki page's text — old must appear in the page exactly once. " +
				"The same rule as wiki_write: read the page first, and read it again if another agent changed it since. (note required: one line, why)",
			Parameters: obj([]string{"slug", "old", "new", "note"}, map[string]any{
				"slug": slug,
				"old":  strProp("the exact text to replace, as the page has it"),
				"new":  strProp("its replacement (empty deletes it)"),
				"note": note,
			})}},
		toolSpec{Type: "function", Function: funcDef{Name: "wiki_archive",
			Description: "Archive a wiki page that is wrong, merged into another or no longer useful — it leaves search and the list; its history stays. " +
				"Read it first, as for a change. (note required: one line, why)",
			Parameters: obj([]string{"slug", "note"}, map[string]any{"slug": slug, "note": note})}},
	)
}

// wikiRole is what run may do with a knowledge store: which one (its
// store id: a project's id for a project's wiki, DB.knowID's for any other
// owner), who it writes as, and whether it may write now.
type wikiRole struct {
	store int64
	actor wikiActor
	write bool
	why   string // a write refused: why
}

// wikiRoleOf resolves run's role in a knowledge store, from the database:
// the Knowledge hook of the coordinator profile whose origin run has
// (coord_profile.go) — for Projects, projWikiRole. errNoWiki: it has none.
func (ag *Agent) wikiRoleOf(run *Run, cfg Config) (*wikiRole, error) {
	if run == nil || hostedID(run.ID) || !ag.db.features {
		return nil, errNoWiki
	}
	if p := coordProfiles.byOrigin[run.Origin]; p != nil && p.Knowledge != nil {
		if r, err := p.Knowledge(ag, run, cfg); r != nil || err != nil {
			return r, err
		}
	}
	return nil, errNoWiki
}

var errNoWiki = errors.New("the wiki tools are a project's coordinator's, its research subagents' and its built-in tasks'")

// wikiToolsFor is what runToolSpecs adds for run: the reading tools for a
// wiki role, the writing ones while it may write.
func wikiToolsFor(cfg Config, run *Run) []toolSpec {
	ag := projAg()
	if ag == nil || run == nil || cfg.CodeRead != nil { // a research subagent's list is its own (codeReadToolSpecs)
		return nil
	}
	if run.Depth != 0 || run.ParentID != 0 || coordProfiles.byOrigin[run.Origin] == nil {
		return nil
	}
	r, err := ag.wikiRoleOf(run, cfg)
	if err != nil {
		return nil
	}
	var out []toolSpec
	for _, s := range wikiToolSpecs(r.write) {
		if !cfg.denied(s.Function.Name) {
			out = append(out, s)
		}
	}
	return out
}

// runWikiTool runs one of the wiki's tools, the role resolved again.
func (ag *Agent) runWikiTool(_ context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	r, err := ag.wikiRoleOf(run, cfg)
	if err != nil {
		return "", err
	}
	if wikiWrites[name] && !r.write {
		return "", errors.New(orStr(r.why, "you can't change "+ag.db.knowName(r.store)))
	}
	d, pid := ag.db, r.store
	slugArg := func() (string, error) { return wikiSlug(str(args["slug"])) }
	switch name {
	case "wiki_search":
		q := strings.TrimSpace(str(args["query"]))
		if q == "" {
			return "", errors.New("query: the words to find")
		}
		limit := toInt(args["limit"])
		if limit <= 0 {
			limit = 8
		}
		hits, mode, err := d.wikiSearch(pid, q, min(limit, 20))
		if err != nil {
			return "", fmt.Errorf("searching the wiki: %v", err)
		}
		if mode == "stopwords" {
			return "no page matches — try more specific words (wiki_list lists them all)", nil
		}
		if len(hits) == 0 {
			return fmt.Sprintf("no page matches %q (wiki_list lists them all)", clip(q, 80)), nil
		}
		var b strings.Builder
		if mode == "any" {
			fmt.Fprintf(&b, "no page has every word; %d page(s) with some of them:\n", len(hits))
		} else {
			fmt.Fprintf(&b, "%d page(s) match:\n", len(hits))
		}
		for _, h := range hits {
			fmt.Fprintf(&b, "- %s — %s (v%d): %s\n", h.Slug, h.Title, h.Version, clip(h.Snippet, 300))
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "wiki_list":
		pages := d.wikiPages(pid, false)
		if len(pages) == 0 {
			if r.write {
				return "the wiki is empty — wiki_write starts it (the index page, slug `index`, is its map)", nil
			}
			return "the wiki is empty", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d page(s):\n", len(pages))
		for _, p := range pages {
			at := ""
			if p.At != "" {
				at = ", read at " + p.At
			}
			fmt.Fprintf(&b, "- %s — %s (v%d, %s, by %s, %s%s)\n", p.Slug, p.Title, p.Version, humanBytes(p.Size), p.UpdatedBy, wikiWhen(p.UpdatedMs), at)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "wiki_read":
		slug, err := slugArg()
		if err != nil {
			return "", err
		}
		p, err := d.wikiPage(pid, slug)
		if err != nil {
			return "", err
		}
		d.wikiRead(run.ID, p)
		off := max(toInt(args["offset"]), 0)
		head := fmt.Sprintf("# %s\n(page %s, version %d, by %s, %s", p.Title, p.Slug, p.Version, p.UpdatedBy, wikiWhen(p.UpdatedMs))
		if p.At != "" {
			head += "; read at " + p.At
		}
		if p.Archived {
			head += "; archived — a write brings it back"
		}
		head += ")"
		if len(p.Sources) > 0 {
			head += "\nsources: " + strings.Join(p.Sources, "; ")
		}
		if off >= len(p.Body) && off > 0 {
			return fmt.Sprintf("offset %d is past the end: the page has %d characters", off, len(p.Body)), nil
		}
		end := min(off+wikiReadPage, len(p.Body))
		out := head + "\n\n" + strings.ToValidUTF8(p.Body[off:end], "")
		if end < len(p.Body) {
			out += fmt.Sprintf("\n\n(more: offset %d)", end)
		}
		return out, nil
	case "wiki_history":
		slug, err := slugArg()
		if err != nil {
			return "", err
		}
		p, err := d.wikiPage(pid, slug)
		if err != nil {
			return "", err
		}
		if v := int64(toInt(args["version"])); v > 0 {
			rev, err := d.wikiRevision(p.ID, v)
			if err != nil {
				return "", err
			}
			arch := ""
			if rev.Archived {
				arch = "; archived"
			}
			return fmt.Sprintf("# %s\n(page %s, version %d of %d, by %s, %s%s)\n\n%s", rev.Title, slug, rev.Version, p.Version, rev.By,
				wikiWhen(rev.AtMs), arch, rev.Body), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s — %d version(s), newest first:\n", slug, p.Version)
		for _, rev := range d.wikiHistory(p.ID, 30) {
			fmt.Fprintf(&b, "- v%d %s by %s (%s)", rev.Version, wikiWhen(rev.AtMs), rev.By, humanBytes(rev.Size))
			if rev.Archived {
				b.WriteString(" archived")
			}
			if rev.Note != "" {
				b.WriteString(": " + rev.Note)
			}
			b.WriteString("\n")
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "wiki_write", "wiki_edit", "wiki_archive":
		c := wikiChange{Slug: str(args["slug"]), Note: str(args["note"])}
		if strings.TrimSpace(c.Note) == "" { // the history's reason (a person's PUT may leave it out)
			return "", errors.New("note: say in a line why you change this page")
		}
		switch name {
		case "wiki_write":
			c.Title, c.Body = str(args["title"]), str(args["body"])
			if raw, ok := args["sources"].([]any); ok {
				c.Sources = []string{}
				for _, s := range raw {
					c.Sources = append(c.Sources, fmt.Sprint(s))
				}
			}
		case "wiki_edit":
			c.Edit, c.Old, c.New = true, str(args["old"]), str(args["new"])
		case "wiki_archive":
			c.Archive = true
		}
		p, err := d.wikiPut(pid, r.actor, c)
		if err != nil {
			return "", err
		}
		switch {
		case p.unchanged:
			return fmt.Sprintf("no change: %s already reads so (version %d)", p.Slug, p.Version), nil
		case p.Archived:
			return fmt.Sprintf("archived %s — now version %d", p.Slug, p.Version), nil
		case name == "wiki_edit":
			return fmt.Sprintf("edited %s — now version %d (%s)", p.Slug, p.Version, humanBytes(p.Size)), nil
		case p.Version == 1:
			return fmt.Sprintf("wrote %s — a new page, version 1 (%s)", p.Slug, humanBytes(p.Size)), nil
		}
		return fmt.Sprintf("wrote %s — now version %d (%s)", p.Slug, p.Version, humanBytes(p.Size)), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// wikiWhen is ms as an absolute time and how long ago: "2026-11-09 10:41
// UTC (3h ago)" — an age alone means nothing in a page read later.
func wikiWhen(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04 UTC") + " (" + wikiAge(ms) + ")"
}

// wikiAge says how long ago ms was, roughly.
func wikiAge(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// wikiTurnEnd (turnEndHooks): a coordinator's turn ended — its prompt takes
// the index as it is at its next turn.
func wikiTurnEnd(t *DB, run *Run, why, outcome, result string) {
	if !t.features || coordProfileOf(t, run) == nil {
		return
	}
	_, _ = t.q.Exec(`DELETE FROM wiki_snaps WHERE run_id=?`, run.ID)
}
