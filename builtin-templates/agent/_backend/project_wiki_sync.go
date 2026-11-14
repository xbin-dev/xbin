// project_wiki_sync.go — the wiki as files, for coding agents (API.md
// §Project wiki, "Coding agents"): a coding agent has no wiki tools, so
// before a prompt reaches it the workspace gate (projectGate) asks whether
// its sandbox's copy is current — wiki_sync keeps, per sandbox, the
// project's newest revision id when it was last written — and if not parks
// the turn for a wikisync job, which writes every live page to
// <project dir>/.xbin/wiki/<slug>.md (read-only), each file starting
// `<!-- wiki: <slug> version <N> -->`. A big task's own sandbox (its fork,
// the same project dir) is synced the same way. The job never fails a
// workspace: a sync that didn't work is a note, and the agent goes on with
// the copy it has (the next change to the wiki tries again).
package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"
)

const pjWikiSync = "wikisync"

func init() {
	projectJobKinds[pjWikiSync] = jobWikiSync
}

// wikiSyncDue (projectGate, from the database): task k's coding agent's
// sandbox holds an older copy of the wiki than the project has.
func wikiSyncDue(d *DB, p *Project, k *ProjectTask, run *Run) bool {
	if run == nil || run.Engine != engineHarness || k.WS != wsReady || p.Dir == "" || !d.features {
		return false
	}
	seq := d.wikiSeq(p.ID)
	if seq == 0 {
		return false // nothing to write yet
	}
	return d.wikiSynced(taskRef(p, k), p.ID) < seq
}

// wikiSynced is the seq sandbox ref's copy of project pid's wiki is at.
func (d *DB) wikiSynced(ref string, pid int64) int64 {
	var seq int64
	_ = d.q.QueryRow(`SELECT seq FROM wiki_sync WHERE ref=? AND project_id=?`, ref, pid).Scan(&seq)
	return seq
}

func (d *DB) setWikiSynced(ref string, pid, seq int64) {
	_, _ = d.q.Exec(`INSERT INTO wiki_sync (ref, project_id, seq, at_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT(ref, project_id) DO UPDATE SET seq=MAX(seq, excluded.seq), at_ms=excluded.at_ms`, ref, pid, seq, nowMs())
}

// wikiDir and wikiOutDir are where a project's sandbox holds the wiki's
// copy, and where a dream round's pages go.
func wikiDir(p *Project) string    { return p.Dir + "/.xbin/wiki" }
func wikiOutDir(p *Project) string { return p.Dir + "/.xbin/wiki-out" }

// wikiFileHeader is a page file's first line.
func wikiFileHeader(slug string, v int64) string {
	return fmt.Sprintf("<!-- wiki: %s version %d -->", slug, v)
}

// jobWikiSync writes the wiki into task k's sandbox, and records the seq
// it wrote — even when it didn't work: the turn goes on.
func jobWikiSync(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	d := projAg().db
	if k == nil || k.WS != wsReady || p.Dir == "" {
		return doneJob("no workspace")
	}
	ref := taskRef(p, k)
	seq := d.wikiSeq(p.ID)
	n, err := wikiSyncTo(ctx, p, ref)
	d.setWikiSynced(ref, p.ID, seq)
	if err != nil {
		logf("project %d task %d: writing the wiki into its sandbox: %v", p.ID, k.N, err)
		_ = d.Tx(func(t *DB) error {
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": clip("the wiki's copy in the task's sandbox wasn't updated: "+err.Error(), 400)}, false, "")
			return nil
		})
		return doneJob("the wiki wasn't written: " + clip(err.Error(), 200))
	}
	return doneJob(fmt.Sprintf("wrote %d wiki page(s)", n))
}

// wikiSyncScript swaps the new copy in: N the new tree, W the copy, O
// the old one; then the copy is read-only and the dream's folder exists.
const wikiSyncScript = `rm -rf -- "$O"
if [ -e "$W" ]; then chmod -R u+w "$W"; mv -- "$W" "$O"; fi
mv -- "$NW" "$W"
chmod -R a-w "$W"
chmod u+w "$W"
rm -rf -- "$O"
mkdir -p "$OUT"
`

// wikiSyncPrep empties the place the new copy is unpacked into.
const wikiSyncPrep = `if [ -e "$NW" ]; then chmod -R u+w "$NW"; rm -rf -- "$NW"; fi
mkdir -p "$NW"
`

// wikiSyncTo writes every live page of p into sandbox ref's copy, as the
// project's owner (the worker's identity there). Answers how many.
func wikiSyncTo(ctx context.Context, p *Project, ref string) (int, error) {
	d := projAg().db
	s, err := openWsbx(ctx, p, ref)
	if err != nil {
		return 0, err
	}
	env := map[string]string{"W": wikiDir(p), "NW": wikiDir(p) + ".new", "O": wikiDir(p) + ".old", "OUT": wikiOutDir(p)}
	if _, err := s.must(ctx, "preparing the wiki's copy", wikiSyncPrep, env, "", time.Minute); err != nil {
		return 0, err
	}
	pages := d.wikiPages(p.ID, false)
	files := map[string][]byte{}
	for _, pg := range pages {
		full, err := d.wikiPage(p.ID, pg.Slug)
		if err != nil {
			continue
		}
		files[pg.Slug+".md"] = []byte(wikiFileBody(full))
	}
	files["README.md"] = []byte(wikiReadme)
	hello, _ := managerHello(ctx, s.conn.M)
	if hello != nil && hello.has("tar") {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
				return 0, err
			}
			if _, err := tw.Write(body); err != nil {
				return 0, err
			}
		}
		if err := tw.Close(); err != nil {
			return 0, err
		}
		if err := s.conn.TarPut(ctx, s.id, env["NW"], &buf, true); err != nil {
			return 0, err
		}
	} else {
		for name, body := range files {
			if _, err := s.conn.WriteFile(ctx, s.id, env["NW"]+"/"+name, bytes.NewReader(body), sbxWrite{Mode: "0644", Mkdirs: true}); err != nil {
				return 0, err
			}
		}
	}
	if _, err := s.must(ctx, "putting the wiki's copy in place", wikiSyncScript, env, "", time.Minute); err != nil {
		return 0, err
	}
	return len(pages), nil
}

// wikiFileBody is a page as its file: the header, its title, its body.
func wikiFileBody(p *WikiPage) string {
	var b strings.Builder
	b.WriteString(wikiFileHeader(p.Slug, p.Version) + "\n")
	fmt.Fprintf(&b, "# %s\n\n", p.Title)
	b.WriteString(strings.TrimSpace(p.Body) + "\n")
	if len(p.Sources) > 0 {
		b.WriteString("\n<!-- sources: " + strings.ReplaceAll(strings.Join(p.Sources, "; "), "-->", "- ->") + " -->\n")
	}
	return b.String()
}

const wikiReadme = `<!-- not a page: how this folder works -->
# The project wiki (a read-only copy)

Notes the project's agents and people keep: how to build, test and run, where
things live, traps and decisions. They may be wrong or out of date — verify
what you rely on. index.md is the map; grep the folder for the rest.

The platform rewrites this folder when the wiki changes; edits here are lost.
In a dream round you write pages to the folder the dream round's prompt
names (../wiki-out/<task number>/<slug>.md) instead.
`
