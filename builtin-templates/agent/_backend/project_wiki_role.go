// project_wiki_role.go — a project's wiki as a knowledge store
// (coord_know.go, coord_know_owner.go; API.md §Project wiki): its owner
// kind — the store id is the project's id, a write tells the project's
// stream — and who may read and write it (the project profile's Knowledge,
// coord_know_tools.go): the coordinator, its research subagents, a
// built-in task.
package main

import (
	"fmt"
	"strconv"
	"strings"
)

func init() {
	registerKnowKind(&knowKind{Kind: knowProject, Name: "this project's wiki",
		Changed: func(t *DB, o knowOwner) {
			if pid, err := strconv.ParseInt(o.Key, 10, 64); err == nil {
				emitProject(t, pid, "wiki", 0)
			}
		}})
}

// wikiDropProject deletes project pid's wiki with it, and what Projects
// keeps beside it: its sandboxes' copies' marks and its tasks' dream rounds.
func wikiDropProject(t *DB, pid int64) {
	knowDrop(t, pid)
	for _, q := range []string{`DELETE FROM wiki_sync WHERE project_id=?`, `DELETE FROM wiki_dreams WHERE project_id=?`} {
		_, _ = t.q.Exec(q, pid)
	}
}

// researchRootOf is the project a research subagent reads and its root —
// which must still be that project's live coordinator.
func (ag *Agent) researchRootOf(run *Run, cfg Config) (*Project, *Run, error) {
	d := ag.db
	if run == nil || hostedID(run.ID) || !d.features || cfg.CodeRead == nil {
		return nil, nil, errNoWiki
	}
	root, err := d.getRun(rootOf(run))
	if err != nil || run.ParentID == 0 {
		return nil, nil, errNoWiki
	}
	rcfg, err := d.runConfig(root.ID)
	if err != nil {
		return nil, nil, errNoWiki
	}
	p, err := ag.coordinatorOf(d, root, rcfg)
	if err != nil {
		return nil, nil, err
	}
	if p.ID != cfg.CodeRead.Project {
		return nil, nil, errNoWiki
	}
	return p, root, nil
}

// projWikiRole (the project profile's Knowledge) resolves run's role in
// its project's wiki (the coordinator through coordinatorOf, a research
// subagent through its root coordinator, a task through its row).
func (ag *Agent) projWikiRole(run *Run, cfg Config) (*wikiRole, error) {
	d := ag.db
	if cfg.CodeRead != nil { // a research subagent: its root must still be the project's coordinator
		p, root, err := ag.researchRootOf(run, cfg)
		if err != nil {
			return nil, err
		}
		return &wikiRole{store: p.ID, write: true, actor: wikiActor{By: researchBy(run, root.Owner), Run: run.ID, At: headsAt(cfg.CodeRead.Heads)}}, nil
	}
	if run.Depth != 0 || run.ParentID != 0 || run.Origin != originProject {
		return nil, errNoWiki
	}
	ref := d.projectRefOf(run)
	switch {
	case ref.isCoordinator():
		p, err := ag.coordinatorOf(d, run, cfg)
		if err != nil {
			return nil, err
		}
		return &wikiRole{store: p.ID, write: true, actor: wikiActor{By: "coordinator (" + run.Owner + ")", Run: run.ID}}, nil
	case ref.isTask():
		if run.Engine == engineHarness {
			return nil, errNoWiki // its files (project_wiki_sync.go)
		}
		p, k := d.projectOfRun(run)
		if p == nil {
			return nil, errNoWiki
		}
		r := &wikiRole{store: p.ID, actor: wikiActor{By: fmt.Sprintf("task #%d", k.N), Run: run.ID, Task: k.N, At: d.taskAt(k)}}
		if d.dreamOf(k.ID).Active { // its dream round's turn (project_dream.go)
			r.write, r.actor.By = true, fmt.Sprintf("task #%d (dream)", k.N)
		} else {
			r.why = "a task writes the wiki only in its dream round, at the end of its work — note what you learn, and keep working"
		}
		return r, nil
	}
	return nil, errNoWiki
}

// researchBy names a research subagent as an author: its number, what it
// was asked (the call's label, its run's title), its coordinator's person.
func researchBy(run *Run, owner string) string {
	label := strings.TrimSpace(strings.TrimPrefix(run.Title, "research:"))
	label = strings.ReplaceAll(oneLineTitle(label), `"`, "'")
	if label == "" {
		return fmt.Sprintf("research #%d (%s's coordinator)", run.ID, owner)
	}
	return fmt.Sprintf("research #%d \"%s\" (%s's coordinator)", run.ID, clip(label, 40), owner)
}

// headsAt is a research binding's heads ("acme/web 1234567") as a
// revision's at: "web@1234567, api@89abcde".
func headsAt(heads []string) string {
	var out []string
	for _, h := range heads {
		repo, sha, ok := strings.Cut(strings.TrimSpace(h), " ")
		if !ok || sha == "" || sha == "-" {
			continue
		}
		out = append(out, repo[strings.LastIndexByte(repo, '/')+1:]+"@"+sha7(sha))
	}
	return strings.Join(out, ", ")
}

// taskAt is task k's checkouts' heads as last pushed (the refs check keeps
// them): what its dream round's facts were read at, when it is known.
func (d *DB) taskAt(k *ProjectTask) string {
	var out []string
	for _, c := range d.checkouts(k.ID) {
		if c.RemoteSHA != "" && c.State != "removed" {
			out = append(out, c.Repo+"@"+sha7(c.RemoteSHA))
		}
	}
	return strings.Join(out, ", ")
}
