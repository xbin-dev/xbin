// project_dream_ingest.go — a coding agent's dream round's pages (API.md
// §Project wiki, "Dreaming"; project_dream.go has the round): the files it
// wrote to its task's folder, <project dir>/.xbin/wiki-out/<n>/<slug>.md,
// each with the header of the copy it read, applied with compare-and-swap
// against that version; what lost goes back once with the conflict's words.
package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dreamClockSkew is how much older than the round's start a page file may
// look and still be the round's (the sandbox's clock may run behind).
var dreamClockSkew = 2 * time.Minute

// --- a coding agent's pages --------------------------------------------------------------------

var wikiHeaderRe = regexp.MustCompile(`^<!--\s*wiki:\s*([A-Za-z0-9-]+)\s+version\s+(\d+)\s*-->\s*$`)

// dreamPage is one page file a dream round wrote.
type dreamPage struct {
	file    string
	slug    string
	base    int64
	title   string
	body    string
	sources []string
	err     string // why it can't be read as a page
}

// parseDreamPage reads a page file: its header (none: a new page named by
// the file), an optional # title, the body, a trailing sources comment.
func parseDreamPage(file, text string) dreamPage {
	pg := dreamPage{file: file, slug: strings.TrimSuffix(file, ".md")}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) {
		if m := wikiHeaderRe.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			pg.slug = strings.ToLower(m[1])
			pg.base, _ = strconv.ParseInt(m[2], 10, 64)
			i++
		}
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) && strings.HasPrefix(lines[i], "# ") {
		pg.title = strings.TrimSpace(lines[i][2:])
		i++
	}
	rest := lines[i:]
	for j := len(rest) - 1; j >= 0; j-- {
		l := strings.TrimSpace(rest[j])
		if l == "" {
			continue
		}
		if s, ok := strings.CutPrefix(l, "<!-- sources:"); ok && strings.HasSuffix(s, "-->") {
			for _, x := range strings.Split(strings.TrimSuffix(s, "-->"), ";") {
				if x = strings.TrimSpace(x); x != "" {
					pg.sources = append(pg.sources, x)
				}
			}
			rest = rest[:j]
		}
		break
	}
	pg.body = strings.TrimSpace(strings.Join(rest, "\n"))
	if _, err := wikiSlug(pg.slug); err != nil {
		pg.err = err.Error()
	}
	return pg
}

// jobWikiIngest applies the page files task k's dream round wrote — in its
// own folder, those written since the round began. Never fails the
// workspace: what can't be read is a note. When no follow-up round goes
// out, the end dream's hold on the task's cleanup ends here.
func jobWikiIngest(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (out jobOutcome, err error) {
	d := projAg().db
	if k == nil || p.Dir == "" {
		return doneJob("no task")
	}
	again := false // a follow-up round went out: the cleanup waits for it
	defer func() {
		if !again {
			_ = d.Tx(func(t *DB) error {
				if r := t.dreamOf(k.ID); r.HoldClean {
					r.HoldClean = false
					t.putDream(k, r)
					dreamLetClean(t, k)
				}
				return nil
			})
		}
	}()
	note := func(text string) {
		_ = d.Tx(func(t *DB) error {
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": clip(text, 1000)}, false, "")
			return nil
		})
	}
	ref := taskRef(p, k)
	s, err := openWsbx(ctx, p, ref)
	if err != nil {
		note("the dream round's wiki pages weren't read: " + err.Error())
		return doneJob("the sandbox: " + clip(err.Error(), 200))
	}
	dir := wikiOutFor(p, k)
	since := d.dreamOf(k.ID).StartedMs - dreamClockSkew.Milliseconds()
	list, err := s.conn.ListDir(ctx, s.id, dir, 0)
	if err != nil {
		if sbxRefusal(err) == "not-found" {
			return doneJob("the dream round wrote no page")
		}
		note("the dream round's wiki pages weren't read: " + err.Error())
		return doneJob("listing its pages: " + clip(err.Error(), 200))
	}
	var names []string
	for _, e := range list.Entries {
		if e.Type != "file" || !strings.HasSuffix(e.Name, ".md") {
			continue
		}
		if e.MtimeMs > 0 && e.MtimeMs < since { // left from before this round: not its pages
			_ = s.conn.Remove(ctx, s.id, dir+"/"+e.Name, false)
			continue
		}
		names = append(names, e.Name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return doneJob("the dream round wrote no page")
	}
	if len(names) > dreamFilesMax {
		names = names[:dreamFilesMax]
	}
	actor := wikiActor{By: fmt.Sprintf("task #%d (dream)", k.N), Run: k.RunID, Task: k.N}
	var wrote, lost []string
	var bad []string // "slug: why"
	for _, name := range names {
		path := dir + "/" + name
		data, _, err := s.conn.ReadFile(ctx, s.id, path, 0, 0, dreamFileMaxLen)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: unreadable (%v)", name, err))
			continue
		}
		pg := parseDreamPage(name, string(data))
		_ = s.conn.Remove(ctx, s.id, path, false) // read: the agent writes it again if it has to
		if pg.err != "" {
			bad = append(bad, fmt.Sprintf("%s: %s", name, pg.err))
			continue
		}
		base := pg.base
		pw, err := d.wikiPut(p.ID, actor, wikiChange{Slug: pg.slug, Title: pg.title, Body: pg.body, Sources: pg.sources, Base: &base, Note: "dream round"})
		switch {
		case err == nil:
			wrote = append(wrote, fmt.Sprintf("%s (v%d)", pw.Slug, pw.Version))
		case isWikiConflict(err):
			lost = append(lost, pg.slug)
		default:
			bad = append(bad, fmt.Sprintf("%s: %v", pg.slug, err))
		}
	}
	var round int
	_ = d.Tx(func(t *DB) error {
		r := t.dreamOf(k.ID)
		r.Round++
		round = r.Round
		t.putDream(k, r)
		return nil
	})
	if len(wrote) > 0 {
		note("the dream round wrote the wiki: " + strings.Join(wrote, ", "))
	}
	if len(lost)+len(bad) == 0 {
		return doneJob(fmt.Sprintf("wrote %d page(s)", len(wrote)))
	}
	if round >= dreamRoundsMax || k.RunID == 0 {
		note(fmt.Sprintf("the dream round's pages left out after %d rounds: %s", round, strings.Join(append(lost, bad...), "; ")))
		return doneJob(fmt.Sprintf("wrote %d page(s), dropped %d", len(wrote), len(lost)+len(bad)))
	}
	if len(lost) > 0 { // their copies, fresh: what the agent merges into
		if _, err := wikiSyncTo(ctx, p, ref); err == nil {
			d.setWikiSynced(ref, p.ID, d.wikiSeq(p.ID))
		}
	}
	text := dreamFollowUpFor(p, k, lost, bad)
	again = true
	err = d.Tx(func(t *DB) error {
		_, err := queueTaskInput(t, taskInput{Project: p.ID, N: k.N, Kind: "input", Text: text, Source: srcDream, HoldPark: true,
			Dedupe: fmt.Sprintf("dream:%d:again:%d:%d", k.N, j.ID, round)})
		return err
	})
	if err != nil {
		again = false
		note("the dream round's follow-up wasn't sent: " + err.Error())
	}
	return doneJob(fmt.Sprintf("wrote %d page(s); %d sent back", len(wrote), len(lost)+len(bad)))
}

var dreamFollowUpFor = dreamFollowUp // (a var: tests)

// dreamFollowUp is the one prompt a round's lost and refused pages go back
// in: the conflict in the owner's words.
func dreamFollowUp(p *Project, k *ProjectTask, lost, bad []string) string {
	var b strings.Builder
	b.WriteString("[dream round — from the platform, not a person]\nSome of your wiki pages weren't saved:\n")
	for _, s := range lost {
		fmt.Fprintf(&b, "- %s: %s\n", s, wikiConflict)
	}
	for _, s := range bad {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	fmt.Fprintf(&b, "Read each again in %s/<slug>.md (just updated), merge your change into it, and write the whole page to %s/<slug>.md again with that copy's header. "+
		"Answer with one line.", wikiDir(p), wikiOutFor(p, k))
	return b.String()
}
