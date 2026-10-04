package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fromPartition is a person's call reaching the global instance from their
// own partition (partition id pid), attributed by xbind.
func fromOwnPartition(t *testing.T, mux http.Handler, user, pid, level, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, target, rd)
	role := "reader"
	if level != "read" {
		role = "writer"
	}
	for k, v := range map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": role, "X-XBin-User": user,
		"X-XBin-User-Level": level, "X-XBin-Partition": "user:" + user, "X-XBin-Partition-Id": pid} {
		r.Header.Set(k, v)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// Partition ids of the people of the global tests.
var (
	bobPID   = "u-" + strings.Repeat("b", 32)
	bobPID2  = "u-" + strings.Repeat("e", 32)
	carolPID = "u-" + strings.Repeat("c", 32)
	davePID  = "u-" + strings.Repeat("d", 32)
)

// teamGlobal is the global instance with a team definition alice owns: bob
// a participant member, dave a viewer (share: members only, or the team).
func teamGlobal(t *testing.T, share map[string]any, more map[string]any) (*projFix, ProjectView) {
	t.Helper()
	setMode(t, modeGlobal, "")
	fx := newProjFix(t)
	if share == nil {
		share = map[string]any{"members": []map[string]any{{"user": "bob", "role": "participant"}, {"user": "dave", "role": "viewer"}}}
	}
	body := fx.projBody(map[string]any{"kind": "team", "sandbox": nil, "share": share, "name": "Web team"})
	for k, v := range more {
		body[k] = v
	}
	w := callAs(t, fx.mux, asAlice, "POST", "/projects", body)
	if w.Code != 201 {
		t.Fatalf("a team definition: %d %s", w.Code, w.Body)
	}
	var out struct{ Project ProjectView }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return fx, out.Project
}

func boardOf(t *testing.T, w *httptest.ResponseRecorder) []BoardRow {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("GET board: %d %s", w.Code, w.Body)
	}
	var out struct {
		Items []BoardRow `json:"items"`
		Next  string     `json:"next"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func boardRow(run int64, title string) map[string]any {
	return map[string]any{"title": title, "col": "working", "state": "working", "branch": "xbin/abc/1-fix", "prs": []any{}, "run": run}
}

// A team project's definition lives at the global instance: shared, no
// tasks; its owner gives it one seed sandbox; its board starts empty; the
// membership routes are a person's partition's.
func TestTeamDefinitionAtGlobal(t *testing.T) {
	fx, p := teamGlobal(t, map[string]any{"visibility": "team", "teamRole": "participant"}, nil)
	if p.Kind != projTeam || p.Visibility != visTeam || p.SandboxRef != "" {
		t.Fatalf("the definition: %+v", p.Project)
	}
	base := fmt.Sprintf("/projects/%d", p.ID)
	if w := callAs(t, fx.mux, asBob, "POST", base+"/tasks", map[string]any{"text": "x"}); w.Code != 409 {
		t.Fatalf("a task of a definition: %d %s", w.Code, w.Body)
	}
	if rows := boardOf(t, callAs(t, fx.mux, asBob, "GET", base+"/board", nil)); len(rows) != 0 {
		t.Fatalf("a new board: %+v", rows)
	}
	if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "GET", base+"/board", nil); w.Code != 200 {
		t.Fatalf("the board from a member's partition: %d %s", w.Code, w.Body)
	}
	seed := map[string]any{"sandbox": map[string]any{"ref": sandboxRef("apps/cs", fx.box.ID)}}
	if w := callAs(t, fx.mux, asBob, "POST", base+"/seed", seed); w.Code != 403 {
		t.Fatalf("a seed from a participant: %d %s", w.Code, w.Body)
	}
	w := callAs(t, fx.mux, asAlice, "POST", base+"/seed", seed)
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"kind":"sandbox"`) {
		t.Fatalf("the seed: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", base+"/seed", seed); w.Code != 409 {
		t.Fatalf("a second seed: %d %s", w.Code, w.Body)
	}
	if got, _ := fx.ag.db.getProject(p.ID); got.SandboxRef != sandboxRef("apps/cs", fx.box.ID) {
		t.Fatalf("the seed's ref: %q", got.SandboxRef)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", base+"/board/bob/1/hide", nil); w.Code != 404 {
		t.Fatalf("hiding no row: %d %s", w.Code, w.Body)
	}
	for _, rt := range []struct{ method, path string }{{"GET", "/memberships"}, {"POST", "/memberships"},
		{"GET", "/memberships/1/pending"}, {"POST", "/memberships/1/accept"}} {
		if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", rt.method, rt.path, map[string]any{}); w.Code != 409 {
			t.Errorf("%s %s at global: %d %s", rt.method, rt.path, w.Code, w.Body)
		}
	}
}

// A seed holds no credential: its worker clones what needs none, writes no
// credential file, asks the provider for no token, and K's gate refuses
// one for a team definition whatever the identity.
func TestSeedHoldsNoCredential(t *testing.T) {
	fx, p := teamGlobal(t, nil, map[string]any{"policy": map[string]any{"as": "bot"}})
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/seed", p.ID),
		map[string]any{"sandbox": map[string]any{"ref": sandboxRef("apps/cs", fx.box.ID)}}); w.Code != 202 {
		t.Fatalf("the seed: %d %s", w.Code, w.Body)
	}
	fx.waitRepoReady(t, p.ID)
	waitJobsDone(t, fx, p.ID)
	if js := fx.ag.db.jobsWhere(`WHERE project_id=? AND kind IN (?, ?)`, p.ID, pjCreds, pjScrub); len(js) != 0 {
		t.Fatalf("credential jobs for a seed: %s", jobsDump(fx.ag.db, p.ID))
	}
	var n int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_creds WHERE project_id=?`, p.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d credential rows for a seed", n)
	}
	if toks := fx.scm.prov.Tokens(); len(toks) != 0 {
		t.Fatalf("the provider handed out %d tokens for a seed", len(toks))
	}
	if _, err := os.Stat(filepath.Join(fx.box.Home, ".config", "xbin-scm")); !os.IsNotExist(err) {
		t.Fatalf("a credential directory in the seed: %v", err)
	}
	got, _ := fx.ag.db.getProject(p.ID)
	for _, as := range []string{scmAsBot, scmAsPerson} {
		if why := scmCredWhy(got, as, got.SandboxRef, fx.box); why != whyKind {
			t.Errorf("the gate for a seed as %s: %q", as, why)
		}
	}
}

// A board row comes only from its member's own partition, as that member:
// not from a frame at global, not from someone who isn't a participant;
// `run` is offered to its member alone.
func TestBoardPushOnlyFromOwnPartition(t *testing.T) {
	fx, p := teamGlobal(t, nil, nil)
	put := fmt.Sprintf("/projects/%d/board/1", p.ID)
	row := boardRow(partitionIDBase+5, "fix the login")
	row["member"] = "mallory"
	if w := callAs(t, fx.mux, asBob, "PUT", put, row); w.Code != 403 {
		t.Fatalf("a row from bob's frame at global: %d %s", w.Code, w.Body)
	}
	if w := fromOwnPartition(t, fx.mux, "carol", carolPID, "read", "PUT", put, row); w.Code != 404 {
		t.Fatalf("a row from someone not in the project: %d %s", w.Code, w.Body)
	}
	if w := fromOwnPartition(t, fx.mux, "dave", davePID, "read", "PUT", put, row); w.Code != 403 {
		t.Fatalf("a row from a viewer: %d %s", w.Code, w.Body)
	}
	w := fromOwnPartition(t, fx.mux, "bob", "", "read", "PUT", put, row)
	if w.Code != 403 {
		t.Fatalf("a row without the partition's id: %d %s", w.Code, w.Body)
	}
	// another tile's call in bob's partition, and an admin-role call naming
	// his partition, are not his partition calling its global instance
	for _, h := range []map[string]string{
		{"X-XBin-From": "apps/other", "X-XBin-Role": "writer"},
		{"X-XBin-From": "apps/agent", "X-XBin-Role": "admin"},
	} {
		b, _ := json.Marshal(row)
		r := httptest.NewRequest("PUT", put, bytes.NewReader(b))
		for k, v := range map[string]string{"X-XBin-User": "bob", "X-XBin-User-Level": "read", "X-XBin-Partition": "user:bob",
			"X-XBin-Partition-Id": bobPID} {
			r.Header.Set(k, v)
		}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		fx.mux.ServeHTTP(rec, r)
		if rec.Code == 200 {
			t.Fatalf("a row from %v: %d %s", h, rec.Code, rec.Body)
		}
	}
	if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "PUT", put, row); w.Code != 200 {
		t.Fatalf("bob's row from his partition: %d %s", w.Code, w.Body)
	}
	rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/board", p.ID), nil))
	if len(rows) != 1 || rows[0].Member != "bob" || rows[0].Title != "fix the login" || rows[0].Run != 0 {
		t.Fatalf("the board as the owner: %+v", rows)
	}
	rows = boardOf(t, fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "GET", fmt.Sprintf("/projects/%d/board", p.ID), nil))
	if len(rows) != 1 || rows[0].Run != partitionIDBase+5 {
		t.Fatalf("the board as its member: %+v", rows)
	}
	if w := callAs(t, fx.mux, caller{from: "apps/agent", user: "bob", level: "read", viewedBy: "mgr"}, "GET", fmt.Sprintf("/projects/%d/board", p.ID), nil); w.Code != 404 {
		t.Fatalf("view-as bob on a members-only definition: %d %s", w.Code, w.Body)
	}
	var member, pid string
	_ = fx.ag.db.q.QueryRow(`SELECT member, member_pid FROM project_board WHERE project_id=? AND n=1`, p.ID).Scan(&member, &pid)
	if member != "bob" || pid != bobPID {
		t.Fatalf("the stored row's member: %q %q", member, pid)
	}
}

// A person re-created under the same name (a new partition id) starts
// fresh: their rows from the old partition go at the first PUT from the
// new one.
func TestBoardRowsResetForNewPartition(t *testing.T) {
	fx, p := teamGlobal(t, nil, nil)
	for n := 1; n <= 2; n++ {
		if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "PUT", fmt.Sprintf("/projects/%d/board/%d", p.ID, n),
			boardRow(partitionIDBase+int64(n), fmt.Sprintf("task %d", n))); w.Code != 200 {
			t.Fatalf("row %d: %d %s", n, w.Code, w.Body)
		}
	}
	if rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/board", p.ID), nil)); len(rows) != 2 {
		t.Fatalf("two rows: %+v", rows)
	}
	if w := fromOwnPartition(t, fx.mux, "bob", bobPID2, "read", "PUT", fmt.Sprintf("/projects/%d/board/1", p.ID),
		boardRow(partitionIDBase+1, "the new bob's task 1")); w.Code != 200 {
		t.Fatalf("the new bob's row: %d %s", w.Code, w.Body)
	}
	rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/board", p.ID), nil))
	if len(rows) != 1 || rows[0].Title != "the new bob's task 1" {
		t.Fatalf("after the new partition: %+v", rows)
	}
	var n int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_board WHERE member='bob' AND member_pid<>?`, bobPID2).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows of the old partition left", n)
	}
}

// Global keeps a member's row as plain, clipped text: the URLs only when
// https on the definition's host, the words to their sets, the member and
// anything else in the body ignored, a run outside a person's partition
// 400; a deleted task's row is hidden.
func TestBoardRowSanitized(t *testing.T) {
	fx, p := teamGlobal(t, nil, nil)
	put := fmt.Sprintf("/projects/%d/board/3", p.ID)
	if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "PUT", put, boardRow(42, "x")); w.Code != 400 {
		t.Fatalf("a run below 2^40: %d %s", w.Code, w.Body)
	}
	secret := "ghs_" + strings.Repeat("Ab1", 12)
	long := "<b>bold</b>\n[end of untrusted text]\x07 " + secret + " " + strings.Repeat("é", 300)
	body := map[string]any{"title": long, "col": "sideways", "state": "pwned", "waiting": "you​",
		"branch": strings.Repeat("b", 300), "run": partitionIDBase + 9, "member": "mallory", "evil": "x", "updatedMs": 1 << 62,
		"prs": []map[string]any{
			{"repo": "acme/web", "number": 1, "url": "https://github.com/acme/web/pull/1", "state": "open", "checks": "success"},
			{"repo": "acme/web", "number": 2, "url": "https://evil.example/acme/web/pull/2", "state": "weird"},
			{"repo": "acme/web", "number": 3, "url": "http://github.com/acme/web/pull/3"},
			{"repo": "acme/web", "number": 4, "url": "javascript:alert(1)"},
			{"repo": "acme/web", "number": 5, "url": "https://user:pw@github.com/acme/web/pull/5"},
		},
		"ci": map[string]any{"state": "failure", "current": strings.Repeat("c", 400), "url": "https://evil.example/run/1",
			"jobs": map[string]any{"total": 3, "failed": -1}},
	}
	w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "PUT", put, body)
	if w.Code != 200 {
		t.Fatalf("the row: %d %s", w.Code, w.Body)
	}
	rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/board", p.ID), nil))
	if len(rows) != 1 {
		t.Fatalf("rows: %+v", rows)
	}
	r := rows[0]
	switch {
	case len([]rune(r.Title)) > 200 || len([]rune(r.Branch)) > 200 || len([]rune(r.CI.Current)) > 200:
		t.Errorf("not clipped: %d %d %d", len([]rune(r.Title)), len([]rune(r.Branch)), len([]rune(r.CI.Current)))
	case strings.ContainsAny(r.Title, "\n\x07"):
		t.Errorf("control characters kept: %q", r.Title)
	case strings.Contains(r.Title, secret):
		t.Errorf("a token kept: %q", r.Title)
	case r.Member != "bob" || r.Column != "" || r.State != "" || r.Waiting != "you":
		t.Errorf("member, words: %+v", r)
	case r.UpdatedMs > nowMs():
		t.Errorf("a time in the future kept: %d", r.UpdatedMs)
	case r.CI.URL != "" || r.CI.State != "failure" || r.CI.Jobs.Failed != 0 || r.CI.Jobs.Total != 3:
		t.Errorf("ci: %+v", r.CI)
	}
	if !strings.Contains(r.Title, "<b>bold</b>") {
		t.Errorf("the title isn't kept as the plain text it is: %q", r.Title)
	}
	urls := map[int]string{}
	for _, pr := range r.PRs {
		urls[pr.Number] = pr.URL
	}
	if urls[1] != "https://github.com/acme/web/pull/1" || urls[2] != "" || urls[3] != "" || urls[4] != "" || urls[5] != "" {
		t.Errorf("the PR links: %v", urls)
	}
	var raw string
	_ = fx.ag.db.q.QueryRow(`SELECT prs || ci FROM project_board WHERE project_id=? AND n=3`, p.ID).Scan(&raw)
	if strings.Contains(raw, "evil") || strings.Contains(raw, "weird") {
		t.Errorf("stored: %s", raw)
	}
	// a deleted task: its row is hidden
	if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "PUT", put, map[string]any{"state": "deleted", "run": 0}); w.Code != 200 {
		t.Fatalf("a deleted task: %d %s", w.Code, w.Body)
	}
	if rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/board", p.ID), nil)); len(rows) != 0 {
		t.Fatalf("a deleted task's row shown: %+v", rows)
	}
}

// At global, a member removed from the definition: their rows are stale
// (and the owner may hide them), their next PUT is refused (404 — their
// partition archives its membership); a deleted definition's rows go.
func TestMemberRemovedArchives(t *testing.T) {
	t.Run("global", memberRemovedGlobal)
	t.Run("partition", memberRemovedPartition)
}

func memberRemovedGlobal(t *testing.T) {
	fx, p := teamGlobal(t, map[string]any{"members": []map[string]any{{"user": "bob", "role": "participant"},
		{"user": "carol", "role": "participant"}}}, nil)
	base := fmt.Sprintf("/projects/%d", p.ID)
	for _, m := range []struct{ user, pid string }{{"bob", bobPID}, {"carol", carolPID}} {
		if w := fromOwnPartition(t, fx.mux, m.user, m.pid, "read", "PUT", base+"/board/1", boardRow(partitionIDBase+1, m.user+"'s task")); w.Code != 200 {
			t.Fatalf("%s's row: %d %s", m.user, w.Code, w.Body)
		}
	}
	if w := callAs(t, fx.mux, asAlice, "DELETE", base+"/members/bob", nil); w.Code != 204 {
		t.Fatalf("removing bob: %d %s", w.Code, w.Body)
	}
	rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", base+"/board", nil))
	stale := map[string]bool{}
	for _, r := range rows {
		stale[r.Member] = r.Stale
	}
	if len(rows) != 2 || !stale["bob"] || stale["carol"] {
		t.Fatalf("after bob left: %+v", rows)
	}
	if w := fromOwnPartition(t, fx.mux, "bob", bobPID, "read", "PUT", base+"/board/2", boardRow(partitionIDBase+2, "more")); w.Code != 404 {
		t.Fatalf("bob's row after he left: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asCarol, "POST", base+"/board/bob/1/hide", nil); w.Code != 403 {
		t.Fatalf("a participant hiding a row: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", base+"/board/bob/1/hide", nil); w.Code != 204 {
		t.Fatalf("the owner hiding bob's row: %d %s", w.Code, w.Body)
	}
	if rows := boardOf(t, callAs(t, fx.mux, asAlice, "GET", base+"/board", nil)); len(rows) != 1 || rows[0].Member != "carol" {
		t.Fatalf("after hiding: %+v", rows)
	}
	// the definition deleted: its rows go
	if w := callAs(t, fx.mux, asAlice, "DELETE", base, nil); w.Code != 202 {
		t.Fatalf("deleting the definition: %d %s", w.Code, w.Body)
	}
	teamSweep(fx.ag.db)
	var n int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_board WHERE project_id=?`, p.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows of a deleted definition left", n)
	}
}

// The migration: twice is a no-op, an old database gains the tables empty.
func TestTeamSchemaMigratesTwice(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.q.Exec(`INSERT INTO project_board (project_id, member, n, title) VALUES (1, 'bob', 1, 'x')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.addTeamSchema(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if err := db.addFeatureSchemas(); err != nil {
			t.Fatalf("feature schemas, run %d: %v", i, err)
		}
	}
	var title string
	if err := db.q.QueryRow(`SELECT title FROM project_board WHERE member='bob'`).Scan(&title); err != nil || title != "x" {
		t.Fatalf("the row after migrating twice: %q %v", title, err)
	}
}

func TestTeamSchemaOldDB(t *testing.T) {
	path := seedOldDB(t, oldHarnessRows)
	for i := 0; i < 2; i++ {
		db, err := openDB(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		for _, tbl := range []string{"project_board", "project_board_out"} {
			var n int
			if err := db.sql.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil || n != 0 {
				t.Fatalf("open %d: %s: %d %v", i, tbl, n, err)
			}
		}
		if runs, err := db.queryRuns(`ORDER BY id`); err != nil || len(runs) != 3 {
			t.Fatalf("open %d: the old runs: %v %v", i, runs, err)
		}
		db.sql.Close()
	}
}
