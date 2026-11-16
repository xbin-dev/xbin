// preview_test.go — D234: xbind renders a person's own view in its preview
// browser (X-XBin-Preview). Such a call reads as the person and changes
// nothing: no watch made or re-read, no sandbox asked, no page noted open.
// (A terminal: terminal_relay_test.go; a sign-in's poll: scm_scrub_test.go.)
package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// GET /runs/{id}/ci is the CI as stored: a watch never read stays unread,
// with fresh=1 too.
func TestPreviewCIReadsStored(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	fx.scm.SetChecks("acme/web", "feature", ciChecks(ciSHA1))
	fx.watchRow(t, root, "feature", ciSHA1, nil)
	reads := func() int { return len(fx.scm.Requests("GET /checks")) }
	for _, q := range []string{"", "?fresh=1"} {
		var v CIView
		if code := ciGET(t, fx.mux, asPreview, fmt.Sprintf("/runs/%d/ci%s", root, q), &v); code != 200 || len(v.Watches) != 1 || v.Watches[0].Checks != nil || reads() != 0 {
			t.Fatalf("a preview's GET ci%s: %d, %d reads, %+v", q, code, reads(), v)
		}
	}
	var v CIView
	if ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", root), &v); reads() != 1 || v.Watches[0].Checks == nil {
		t.Fatalf("alice's own look: %d reads, %+v", reads(), v)
	}
}

// A task with no watch yet gets none from a preview — and no subscription
// at its code host; the person's own look makes it.
func TestPreviewCIMakesNoWatch(t *testing.T) {
	fx := newCIFix(t)
	fx.scm.SetChecks("acme/web", "xbin/k3x9/1-fix-login", ciChecks(ciSHA1))
	p, k := fx.ciTask(t, ciSHA1, []TaskPR{{Repo: "acme/web", Number: 42, State: "open", HeadSHA: ciSHA1}})
	if code := ciGET(t, fx.mux, asPreview, fmt.Sprintf("/runs/%d/ci", k.RunID), nil); code != 200 {
		t.Fatalf("a preview's GET ci: %d", code)
	}
	if ws := fx.ag.db.ciWatchesWhere(`WHERE root_run=?`, k.RunID); len(ws) != 0 || len(fx.scm.Requests("POST /subscriptions")) != 0 {
		t.Fatalf("a preview made a watch: %s", ciDump(ws))
	}

	// the project's status and overview: its page isn't noted open (no
	// fetches of its repos for a preview)
	forget := func() {
		projectStatusRead.Lock()
		delete(projectStatusRead.m, p.ID)
		projectStatusRead.Unlock()
	}
	forget()
	t.Cleanup(forget)
	for _, page := range []string{"status", "overview"} {
		path := fmt.Sprintf("/projects/%d/%s", p.ID, page)
		if w := callAs(t, fx.mux, asPreview, "GET", path, nil); w.Code != 200 || statusReadAt(p.ID) != 0 {
			t.Fatalf("a preview's GET %s: %d %s (noted open: %v)", page, w.Code, w.Body, statusReadAt(p.ID) != 0)
		}
		if w := callAs(t, fx.mux, asAlice, "GET", path, nil); w.Code != 200 || statusReadAt(p.ID) == 0 {
			t.Fatalf("alice's GET %s: %d %s (noted open: %v)", page, w.Code, w.Body, statusReadAt(p.ID) != 0)
		}
		forget()
	}

	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", k.RunID), nil)
	if ws := fx.ag.db.ciWatchesWhere(`WHERE root_run=?`, k.RunID); len(ws) != 1 {
		t.Fatalf("alice's own look made %d watches", len(ws))
	}
	ciWait(t, "its setup", func() bool { return len(fx.scm.Requests("POST /subscriptions")) == 1 })
}

// GET /harnesses?probe= runs nothing in the sandbox and records nothing —
// nor does it stand as the probe of the next 10 minutes.
func TestPreviewHarnessProbe(t *testing.T) {
	_, mux := accessFixture(t)
	t.Cleanup(forgetHarnessProbes)
	forgetHarnessProbes()
	m := bindSbx(t, "apps/fsb")["apps/fsb"]
	m.Harnesses = []fsbHarness{{ID: "fake", Argv: []string{"sh"}}}
	box := mkSandbox(t, "apps/fsb", "alice", sbxCreate{Name: "api"})
	ref := sandboxRef("apps/fsb", box.ID)
	q := "?probe=" + url.QueryEscape(ref)
	runs := func() int { return m.count("POST", "/sbx/sandboxes/"+box.ID+"/run") }
	l := getHarnesses(t, mux, asPreview, q)
	if l.Probe == nil || l.Probe.Ran || l.Probe.Cached || l.Probe.Ref != ref || !strings.Contains(l.Probe.Error, "preview") || runs() != 0 {
		t.Fatalf("a preview's probe: %+v (runs %d)", l.Probe, runs())
	}
	if s := seenIn(l.entry(t, "fake"), ref); s != nil {
		t.Fatalf("a preview's probe recorded: %s", jsonOf(s))
	}
	if l := getHarnesses(t, mux, asAlice, q); l.Probe == nil || !l.Probe.Ran || runs() != 1 {
		t.Fatalf("alice's own probe: %+v (runs %d)", l.Probe, runs())
	}
}

// GET /projects/{pid}/board marks no row stale: the rows as stored.
func TestPreviewBoardMarksNothing(t *testing.T) {
	fx, p := teamGlobal(t, map[string]any{"members": []map[string]any{{"user": "bob", "role": "participant"}}}, nil)
	base := fmt.Sprintf("/projects/%d", p.ID)
	if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "PUT", base+"/board/1", boardRow(partitionIDBase+1, "bob's task")); w.Code != 200 {
		t.Fatalf("bob's row: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "DELETE", base+"/members/bob", nil); w.Code != 204 {
		t.Fatalf("removing bob: %d %s", w.Code, w.Body)
	}
	stale := func() (n int) {
		_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_board WHERE project_id=? AND stale<>0`, p.ID).Scan(&n)
		return n
	}
	if rows := boardOf(t, callAs(t, fx.mux, asPreview, "GET", base+"/board", nil)); len(rows) != 1 || rows[0].Stale || stale() != 0 {
		t.Fatalf("a preview's board: %+v (%d stale stored)", rows, stale())
	}
	if rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", base+"/board", nil)); len(rows) != 1 || !rows[0].Stale || stale() != 1 {
		t.Fatalf("alice's board: %+v (%d stale stored)", rows, stale())
	}
}

// GET /memberships and a membership's pending part re-read no definition
// (which would apply it: repos, jobs): the memberships as stored.
func TestPreviewMembershipsReadStored(t *testing.T) {
	fx := newTeamFix(t)
	fx.def(7, `{}`, "")
	a := fx.join(t, 7, nil)
	waitJobsDone(t, fx.projFix, a.ID)
	forget := func() {
		teamSynced.Lock()
		teamSynced.m = map[int64]time.Time{}
		teamSynced.Unlock()
	}
	forget()
	gets := func() int {
		fx.g.mu.Lock()
		defer fx.g.mu.Unlock()
		return fx.g.gets
	}
	before := gets()
	if w := callAs(t, fx.mux, asPreview, "GET", "/memberships", nil); w.Code != 200 || strings.Count(w.Body.String(), `"kind":"membership"`) != 1 {
		t.Fatalf("a preview's GET /memberships: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asPreview, "GET", fmt.Sprintf("/memberships/%d/pending", a.ID), nil); w.Code != 200 {
		t.Fatalf("a preview's GET pending: %d %s", w.Code, w.Body)
	}
	if gets() != before {
		t.Fatalf("a preview re-read the definition: %d reads, then %d", before, gets())
	}
	if w := callAs(t, fx.mux, asAlice, "GET", "/memberships", nil); w.Code != 200 || gets() != before+1 {
		t.Fatalf("alice's GET /memberships: %d (%d reads, then %d)", w.Code, before, gets())
	}
	waitJobsDone(t, fx.projFix, a.ID)
}
