// acl.go — what a caller may do with a run (D83). Access is always decided on
// the ROOT of a run's tree, so a subagent is exactly as visible as the
// conversation it works for.
package main

import (
	"sync"
	"time"
)

// level is a caller's access to one run.
type level int

const (
	lvNone        level = iota
	lvViewer            // may read it
	lvParticipant       // may talk to it and steer it
	lvOwner             // may rename, share and delete it
	lvSystem            // the owner token or the tile itself
)

func (l level) String() string {
	switch l {
	case lvViewer:
		return "viewer"
	case lvParticipant:
		return "participant"
	case lvOwner:
		return "owner"
	case lvSystem:
		return "system"
	}
	return "none"
}

func roleLevel(role string) level {
	switch role {
	case roleParticipant:
		return lvParticipant
	case roleViewer:
		return lvViewer
	}
	return lvNone
}

// rootACL is who may see one conversation.
type rootACL struct {
	root                 int64
	owner                string
	visibility, teamRole string
	members              map[string]string // user → viewer | participant
	project              int64             // a project's run (origin project): its project's id (projectRunCap)
	loaded               time.Time
}

func (a *rootACL) level(w who) level {
	switch w.kind {
	case whoSystem:
		return lvSystem
	case whoElement:
		if a.owner != "" && a.owner == "el:"+w.el {
			return lvOwner
		}
	case whoUser:
		if w.viewedBy != "" {
			// An admin viewing as this user (D64) sees only what the whole
			// team may see — never the user's private conversations.
			if a.visibility == visTeam {
				return lvViewer
			}
			return lvNone
		}
		l := roleLevel(a.members[w.user])
		if a.visibility == visTeam {
			if t := roleLevel(a.teamRole); t > l {
				l = t
			}
		}
		if a.owner != "" && a.owner == w.user {
			l = lvOwner
		} else if a.owner == "" && w.manager() {
			l = lvOwner // unowned (legacy) runs are the tile managers'
		}
		if a.project != 0 && l > lvViewer {
			l = projectRunCap(a.project, w, l) // a project's people act on its runs (project_store.go)
		}
		return l
	}
	return lvNone
}

// mine: the caller owns or joined it — what "my conversations" lists.
func (a *rootACL) mine(w who) bool {
	if w.kind != whoUser {
		return false
	}
	_, member := a.members[w.user]
	return a.owner == w.user || member
}

const aclTTL = 60 * time.Second

// aclCache keeps recently used ACLs. Every change made in this process
// flushes the root it touched; the TTL bounds what another process (a
// blue/green overlap) changed.
type aclCache struct {
	mu sync.Mutex
	m  map[int64]*rootACL
}

func (c *aclCache) get(root int64) *rootACL {
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.m[root]; a != nil && time.Since(a.loaded) < aclTTL {
		return a
	}
	return nil
}

func (c *aclCache) put(a *rootACL) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 4096 {
		c.m = map[int64]*rootACL{}
	}
	c.m[a.root] = a
}

// flush forgets one root (0: all).
func (c *aclCache) flush(root int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if root == 0 {
		c.m = nil
	} else {
		delete(c.m, root)
	}
}

func (d *DB) loadACL(root int64) (*rootACL, error) {
	a := &rootACL{root: root, members: map[string]string{}, loaded: time.Now()}
	var origin string
	var originID int64
	if err := d.q.QueryRow(`SELECT owner, visibility, team_role, origin, origin_id FROM runs WHERE id=?`, root).
		Scan(&a.owner, &a.visibility, &a.teamRole, &origin, &originID); err != nil {
		return nil, err
	}
	if origin == originProject {
		a.project = originID
	}
	rows, err := d.q.Query(`SELECT user, role FROM run_members WHERE run_id=?`, root)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u, role string
		if rows.Scan(&u, &role) == nil {
			a.members[u] = role
		}
	}
	return a, rows.Err()
}

// aclOf returns a root's ACL, cached.
func (ag *Agent) aclOf(root int64) (*rootACL, error) {
	if a := ag.acl.get(root); a != nil {
		return a, nil
	}
	a, err := ag.db.loadACL(root)
	if err != nil {
		return nil, err
	}
	ag.acl.put(a)
	return a, nil
}

// runAccess resolves a caller's access to a run through its root.
func (ag *Agent) runAccess(w who, runID int64) (*Run, level, error) {
	run, err := ag.db.getRun(runID)
	if err != nil {
		return nil, lvNone, err
	}
	a, err := ag.aclOf(rootOf(run))
	if err != nil {
		return nil, lvNone, err
	}
	return run, a.level(w), nil
}

// aclWhere is the SQL form of level(w) >= viewer over runs aliased r (roots
// only — callers join a subagent to its root first).
func aclWhere(w who) (string, []any) {
	switch w.kind {
	case whoSystem:
		return "1=1", nil
	case whoElement:
		return "r.owner=?", []any{"el:" + w.el}
	case whoUser:
		if w.viewedBy != "" {
			return "r.visibility='team'", nil
		}
		q := "(r.owner=? OR r.visibility='team' OR EXISTS (SELECT 1 FROM run_members m WHERE m.run_id=r.id AND m.user=?))"
		args := []any{w.user, w.user}
		if w.manager() {
			q = "(r.owner='' OR " + q[1:]
		}
		return q, args
	}
	return "0=1", nil
}
