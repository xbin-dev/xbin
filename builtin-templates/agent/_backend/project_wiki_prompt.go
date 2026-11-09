// project_wiki_prompt.go — the wiki in prompts (API.md §Project wiki): the
// index page is the small layer every prompt carries — framed as notes
// that may be wrong — and the rest is reached by search and by reading. A
// task's brief shows the index as it was when the task started (its
// snapshot, wiki_snaps), so the brief stays the same bytes and the prompt
// cache warm; a coordinator's shows it as at the start of its turn.
package main

import (
	"fmt"
	"strings"
)

// wikiProvenance is how every writer of the wiki is told to say how a fact
// is known (the lab: agents couldn't tell verified from reported).
const wikiProvenance = "State how each fact is known: (read in path:line), (ran `cmd`), (reported by ‹who›, unverified). " +
	"For a table or a list read from one place, one line saying how it is known is enough. " +
	"Never write facts about your own environment or session (\"not verified here\"): those go in the note."

// wikiBrief is the # Project wiki part of task k's brief ("" for a
// subagent of it: it has no wiki tools).
func wikiBrief(d *DB, p *Project, k *ProjectTask, sub bool) string {
	if sub || k == nil || k.RunID == 0 || !d.features {
		return ""
	}
	harness := false
	if run, err := d.getRun(k.RunID); err == nil {
		harness = run.Engine == engineHarness
	}
	v, body, ok := d.wikiIndexFor(p.ID, k.RunID)
	var b strings.Builder
	b.WriteString("\n# Project wiki\n")
	if harness {
		fmt.Fprintf(&b, "Project knowledge: %s/.xbin/wiki/index.md is the map, one Markdown file per page — grep the folder. "+
			"It is a read-only copy the platform keeps current.\n", p.Dir)
	} else {
		b.WriteString("The project's shared notes: wiki_search and wiki_read reach its pages.\n")
	}
	if ok {
		b.WriteString(wikiPromptIndex(v, body))
	} else {
		b.WriteString("It has no index page yet.\n")
	}
	return b.String()
}

// researchWikiPrompt is a research subagent's # Project wiki: the index,
// framed as everywhere, as it was when it first saw one (wiki_snaps).
func researchWikiPrompt(d *DB, p *Project, run *Run) string {
	if !d.features {
		return ""
	}
	v, body, ok := d.wikiIndexFor(p.ID, run.ID)
	if !ok {
		return "\n\n# Project wiki\nIt has no index page yet."
	}
	return "\n\n# Project wiki\n" + strings.TrimRight(wikiPromptIndex(v, body), "\n")
}

// coordWikiPrompt is the coordinator's part about research and the wiki.
func coordWikiPrompt(d *DB, p *Project, run *Run) string {
	var b strings.Builder
	b.WriteString(`
- Use research for any question about the code — what it does, where a thing is, how to build or test it, what a task changed: a research subagent reads the repos (or a task's worktree) and answers. Give it the question and the facts it needs; don't guess about code nobody read.
- The project wiki keeps what the project's agents verified: search and read it before you research, and keep it true — update the page a fact belongs on (wiki_read it first), archive what is wrong, keep the index page (slug ` + "`index`" + `) a short map of the pages. Tasks see the index in their brief and add what they learned when their work goes to review.
  ` + wikiProvenance)
	v, body, ok := d.wikiIndexFor(p.ID, run.ID)
	if ok {
		b.WriteString("\n" + strings.TrimRight(wikiPromptIndex(v, body), "\n"))
	} else {
		b.WriteString("\nThe wiki has no index page yet: write one (slug `index`) once there are pages to map.")
	}
	return b.String()
}
