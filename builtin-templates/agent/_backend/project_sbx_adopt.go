// project_sbx_adopt.go — a project's own sandbox made once (API.md
// §Projects and tasks, the sandbox job). A manager that makes a VM before
// its create answers can take longer than a try waits; the sandbox is made
// all the same. So the create's clientId is one per sandbox (sbxClientID),
// the sandbox carries projectMadeLabel (<uid>.<generation>), and before it
// creates, the job looks for one an earlier try made and takes it up. The
// rest of what earlier tries made — before this build each try made its own
// sandbox, with only projectLabel to say whose — are its twins: deleted
// (sweepTwins), again on the job's next turns until none is left.
package main

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
)

// projectMadeLabel marks a sandbox the project's sandbox job made:
// <project uid>.<sandbox generation>.
const projectMadeLabel = "xbin.agent/project-made"

func twinsKey(pid int64) string { return "proj_sbx_twins:" + strconv.FormatInt(pid, 10) }

// madeRank orders candidates: a usable one first, then the newest.
func madeRank(b *sbxSandbox) int {
	switch b.State {
	case "running":
		return 0
	case "stopped", "archived":
		return 1
	case "creating", "starting":
		return 2
	case "error":
		return 9
	}
	return 5
}

// adoptMade is the sandbox an earlier try of the job made at conn (made: its
// label), else nil. More than one: the best is taken, the others are
// queued as twins. Generation 0 also takes the sandboxes earlier builds
// made, which carry only projectLabel: a project with a {new} sandbox to
// make and no generation has never had any other sandbox.
func adoptMade(ctx context.Context, conn *sbxConn, p *Project, made string) (*sbxSandbox, error) {
	boxes, err := conn.List(ctx)
	if err != nil {
		return nil, err
	}
	gen0 := made == p.UID+".0"
	var mine []*sbxSandbox
	for _, b := range boxes {
		m := b.Labels[projectMadeLabel]
		if m == made || gen0 && m == "" && b.Labels[projectLabel] == p.UID {
			mine = append(mine, b)
		}
	}
	if len(mine) == 0 {
		return nil, nil
	}
	sort.SliceStable(mine, func(i, k int) bool {
		if ri, rk := madeRank(mine[i]), madeRank(mine[k]); ri != rk {
			return ri < rk
		}
		return mine[i].Created > mine[k].Created
	})
	best := mine[0]
	if madeRank(best) == 9 { // every one failed at its manager: none taken, all deleted, a fresh create
		best = nil
	}
	var twins []string
	for _, b := range mine {
		if b != best {
			twins = append(twins, sandboxRef(conn.M.Provider, b.ID))
		}
	}
	if len(twins) > 0 {
		addTwins(p.ID, twins)
		logf("project %d: %d sandbox(es) earlier tries made are deleted (one taken up: %v)", p.ID, len(twins), best != nil)
	}
	if best != nil {
		// the same sandbox as Get says it (List may say less)
		return conn.Get(ctx, best.ID)
	}
	if err := deleteTwins(ctx, p); err != nil {
		return nil, err
	}
	return nil, nil
}

func addTwins(pid int64, refs []string) {
	d := projAg().db
	var have []string
	_ = json.Unmarshal([]byte(d.getSetting(twinsKey(pid))), &have)
	seen := map[string]bool{}
	for _, r := range have {
		seen[r] = true
	}
	for _, r := range refs {
		if !seen[r] {
			have = append(have, r)
			seen[r] = true
		}
	}
	b, _ := json.Marshal(have)
	_ = d.putSetting(twinsKey(pid), string(b))
}

// deleteTwins deletes every queued twin of p it can; one gone already
// counts as deleted. The first other error is answered (the rest stay queued).
func deleteTwins(ctx context.Context, p *Project) error {
	d := projAg().db
	var refs []string
	if json.Unmarshal([]byte(d.getSetting(twinsKey(p.ID))), &refs) != nil || len(refs) == 0 {
		return nil
	}
	var left []string
	var first error
	for _, ref := range refs {
		if ref == p.SandboxRef {
			continue // never the one it works in
		}
		conn, id, err := sbxDialRef(ref, sbxUserOf(binderWho(p.Owner)))
		if err == nil {
			err = conn.Delete(ctx, id)
		}
		if err != nil && sbxRefusal(err) != "not-found" {
			left = append(left, ref)
			if first == nil {
				first = err
			}
		}
	}
	if len(left) == 0 {
		_, _ = d.q.Exec(`DELETE FROM settings WHERE k=?`, twinsKey(p.ID))
		return nil
	}
	b, _ := json.Marshal(left)
	_ = d.putSetting(twinsKey(p.ID), string(b))
	return first
}

// sweepTwins deletes p's queued twins: how many are left (a manager that
// won't delete one still being made, say).
func sweepTwins(ctx context.Context, p *Project) int {
	if err := deleteTwins(ctx, p); err != nil {
		logf("project %d: deleting a duplicate sandbox: %v", p.ID, err)
	}
	var refs []string
	_ = json.Unmarshal([]byte(projAg().db.getSetting(twinsKey(p.ID))), &refs)
	return len(refs)
}
