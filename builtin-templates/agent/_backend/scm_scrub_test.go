package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// scrubbed: the files are empty, the row says scrubbed with why, the purpose
// was revoked and the token is no longer live (still masked).
func (fx *credFx) scrubbed(t *testing.T, why string, tok string) {
	t.Helper()
	if got := readFile(t, fx.credFile("github.com.cred")) + readFile(t, fx.credFile("gh/hosts.yml")); got != "" {
		t.Fatalf("%s: the files still hold %q", why, got)
	}
	fx.gone(t, why, tok)
}

// gone: as scrubbed, for a sandbox whose files can't be read any more.
func (fx *credFx) gone(t *testing.T, why string, tok string) {
	t.Helper()
	if row := credRowOf(t, fx); row.State != credScrubbed || row.Why != why {
		t.Fatalf("%s: row %+v", why, row)
	}
	revoked := false
	for _, p := range fx.scm.Revoked() {
		revoked = revoked || p == "proj:k3x9qa:"+fx.ref
	}
	if !revoked {
		t.Fatalf("%s: not revoked (%v)", why, fx.scm.Revoked())
	}
	if scmLiveGet(scmLiveKey(fx.p.ID, fx.ref, "github.com")) != nil {
		t.Fatalf("%s: still live", why)
	}
	if redactText(tok) == tok {
		t.Fatalf("%s: no longer masked", why)
	}
}

// Sharing a sandbox through the agent empties a project's credential in it
// before the share goes out — the bot's too (the gate decides afresh at
// the next write).
func TestScrubOnShare(t *testing.T) {
	fx := credFixture(t, modeGlobal)
	fx.ensure(t)
	tok := fx.scm.Tokens()[0].Value
	mux := fx.h.(*http.ServeMux)
	if w := callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(fx.ref), map[string]any{"visibility": "team"}); w.Code != 200 {
		t.Fatalf("share: %d %s", w.Code, w.Body)
	}
	fx.scrubbed(t, scrubShare, tok)
	// team-visible, the project private: the gate refuses the next write
	if err := ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft); err == nil || !strings.Contains(err.Error(), "may not act in the project") {
		t.Fatalf("the next write: %v", err)
	}
	if row := credRowOf(t, fx); row.State != credBlocked {
		t.Fatalf("row: %+v", row)
	}
}

// A credential that can't be emptied refuses the share — and keeps refusing
// it while the file holds the value (the row stays live; the value is
// revoked anyway); once the write works the share goes through. A …tmp a
// write left behind goes too.
func TestScrubOnShareRefused(t *testing.T) {
	fx := credFixture(t, modeGlobal)
	fx.ensure(t)
	tok := fx.scm.Tokens()[0].Value
	mux := fx.h.(*http.ServeMux)
	stale := fx.credFile("github.com.cred.tmp")
	if err := os.WriteFile(stale, []byte("password="+tok+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	share := func() *httptest.ResponseRecorder {
		return callAs(t, mux, asAlice, "PATCH", "/sandboxes/"+url.PathEscape(fx.ref), map[string]any{"visibility": "team"})
	}
	for try := 1; try <= 2; try++ {
		fx.m.FailNext("write", 500, "unavailable", "the disk is full")
		if w := share(); w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "isn't shared") {
			t.Fatalf("try %d: %d %s", try, w.Code, w.Body)
		}
		if !strings.Contains(readFile(t, fx.credFile("github.com.cred")), tok) {
			t.Fatalf("try %d: the fault didn't hold the file", try)
		}
		if row := credRowOf(t, fx); row.State != credLive || row.Why != scrubShare || row.Refresh != 0 {
			t.Fatalf("try %d: row %+v", try, row)
		}
		if scmCredsDue(fx.ag.db, fx.p, nil) != true {
			t.Fatalf("try %d: a revoked value in the file isn't due", try)
		}
		conn, id, err := sbxDialRef(fx.ref, "alice")
		if err != nil {
			t.Fatal(err)
		}
		if box, err := conn.Get(context.Background(), id); err != nil || sandboxShared(box) {
			t.Fatalf("try %d: shared anyway: %+v %v", try, box, err)
		}
	}
	if len(fx.scm.Revoked()) == 0 {
		t.Fatal("not revoked while the file couldn't be emptied")
	}
	if w := share(); w.Code != 200 {
		t.Fatalf("the share once the write works: %d %s", w.Code, w.Body)
	}
	fx.scrubbed(t, scrubShare, tok)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("the …tmp a write left: %v", err)
	}
}

// A share or an archive through the agent while a credential is being
// minted: neither waits on the provider, and the write that follows re-reads
// the sandbox under its lock — the shared one the gate refuses (the token
// just handed out revoked, the row blocked), the archived one the manager
// refuses — so no token is left in the files and no row says live.
func TestScrubRacesEnsure(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		body               any
		want               string
	}{
		{"share", "PATCH", "", map[string]any{"visibility": "team"}, "may not act in the project"},
		{"archive", "POST", "/archive", nil, "archived"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := credFixture(t, modeGlobal)
			mux := fx.h.(*http.ServeMux)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			fx.scm.TokenHold = func() {
				once.Do(func() { close(entered) })
				<-release
			}
			done := make(chan error, 1)
			go func() { done <- ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft) }()
			select {
			case <-entered:
			case <-time.After(20 * time.Second):
				t.Fatal("the provider was never asked")
			}
			acted := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				acted <- callAs(t, mux, asAlice, tc.method, "/sandboxes/"+url.PathEscape(fx.ref)+tc.path, tc.body)
			}()
			select {
			case w := <-acted:
				if w.Code != 200 {
					close(release)
					t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body)
				}
			case <-time.After(20 * time.Second):
				close(release)
				t.Fatalf("the %s waited on the provider", tc.name)
			}
			close(release)
			var err error
			select {
			case err = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("ensure never finished")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the write after the %s: %v", tc.name, err)
			}
			tok := fx.scm.Tokens()[0].Value
			for _, f := range []string{"github.com.cred", "github.com.cred.tmp", "gh/hosts.yml", "gh/hosts.yml.tmp"} {
				if b, _ := os.ReadFile(fx.credFile(f)); bytes.Contains(b, []byte(tok)) {
					t.Fatalf("%s holds the token after the %s", f, tc.name)
				}
			}
			for _, c := range fx.ag.db.scmCredsOf(fx.p.ID, fx.ref) {
				if c.State == credLive {
					t.Fatalf("a live row after the %s: %+v", tc.name, c)
				}
			}
			if scmLiveGet(scmLiveKey(fx.p.ID, fx.ref, "github.com")) != nil {
				t.Fatalf("still live in memory after the %s", tc.name)
			}
			if tc.name == "share" {
				if row := credRowOf(t, fx); row.State != credBlocked {
					t.Fatalf("row: %+v", row)
				}
				if !slices.Contains(fx.scm.Revoked(), "proj:k3x9qa:"+fx.ref) {
					t.Fatalf("the token handed out during the share isn't revoked: %v", fx.scm.Revoked())
				}
			}
		})
	}
}

// A write that fails partway leaves no value no row points at. A first
// write (or one after a scrub) is scrubbed at once — files emptied, the
// …tmp removed, the purpose revoked, the row back to what it was (one the
// manager refused outright: revoked) — and a share then finds nothing; a …tmp it can't remove keeps the row live, so
// the share is refused until it can. A refresh that fails keeps the older
// row live (its scrub covers the new …tmp; the older value still works).
func TestCredWriteFailsPartway(t *testing.T) {
	files := []string{"github.com.cred", "github.com.cred.tmp", "gh/hosts.yml", "gh/hosts.yml.tmp"}
	holds := func(fx *credFx, tok string) string {
		for _, f := range files {
			if b, _ := os.ReadFile(fx.credFile(f)); bytes.Contains(b, []byte(tok)) {
				return f
			}
		}
		return ""
	}
	purpose := func(fx *credFx) string { return "proj:k3x9qa:" + fx.ref }
	share := func(t *testing.T, fx *credFx) *httptest.ResponseRecorder {
		return callAs(t, fx.h.(*http.ServeMux), asAlice, "PATCH", "/sandboxes/"+url.PathEscape(fx.ref), map[string]any{"visibility": "team"})
	}
	last := func(fx *credFx) string { ts := fx.scm.Tokens(); return ts[len(ts)-1].Value }

	t.Run("rename", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		fx.m.FailNext("run", 503, "unavailable", "down")
		if err := ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft); err == nil {
			t.Fatal("the write went through")
		}
		tok := last(fx)
		if rows := fx.ag.db.scmCredsOf(fx.p.ID, fx.ref); len(rows) != 0 {
			t.Fatalf("rows after a failed first write: %+v", rows)
		}
		if !slices.Contains(fx.scm.Revoked(), purpose(fx)) {
			t.Fatalf("not revoked: %v", fx.scm.Revoked())
		}
		if f := holds(fx, tok); f != "" {
			t.Fatalf("%s holds the token", f)
		}
		if scmLiveGet(scmLiveKey(fx.p.ID, fx.ref, "github.com")) != nil || redactText(tok) == tok {
			t.Fatal("still live, or no longer masked")
		}
		if w := share(t, fx); w.Code != 200 {
			t.Fatalf("share: %d %s", w.Code, w.Body)
		}
	})

	t.Run("refused outright", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		fx.m.FailNext("write", 409, "state", "the sandbox is archived")
		if err := ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft); err == nil {
			t.Fatal("the write went through")
		}
		if rows := fx.ag.db.scmCredsOf(fx.p.ID, fx.ref); len(rows) != 0 {
			t.Fatalf("rows after a refused write: %+v", rows)
		}
		if !slices.Contains(fx.scm.Revoked(), purpose(fx)) {
			t.Fatalf("not revoked: %v", fx.scm.Revoked())
		}
	})

	t.Run("after a scrub", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		fx.ensure(t)
		if err := scmScrub(context.Background(), fx.p, fx.ref, scrubStop); err != nil {
			t.Fatal(err)
		}
		fx.m.FailNext("run", 503, "unavailable", "down")
		if err := ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft); err == nil {
			t.Fatal("the write went through")
		}
		if row := credRowOf(t, fx); row.State != credScrubbed || row.Why != scrubStop {
			t.Fatalf("row after a failed write: %+v", row)
		}
		if f := holds(fx, last(fx)); f != "" {
			t.Fatalf("%s holds the token", f)
		}
	})

	t.Run("second file", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		// the second file's …tmp can't be written, nor removed
		block := fx.credFile("gh/hosts.yml.tmp")
		if err := os.MkdirAll(filepath.Join(block, "x"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft); err == nil {
			t.Fatal("the write went through")
		}
		tok := last(fx)
		if !slices.Contains(fx.scm.Revoked(), purpose(fx)) {
			t.Fatalf("not revoked: %v", fx.scm.Revoked())
		}
		if f := holds(fx, tok); f != "" {
			t.Fatalf("%s holds the token", f)
		}
		if row := credRowOf(t, fx); row.State != credLive || row.Refresh != 0 || !scmCredsDue(fx.ag.db, fx.p, nil) {
			t.Fatalf("a …tmp left: the row %+v (due %v)", row, scmCredsDue(fx.ag.db, fx.p, nil))
		}
		if w := share(t, fx); w.Code != http.StatusBadGateway {
			t.Fatalf("share with a …tmp left: %d %s", w.Code, w.Body)
		}
		if err := os.RemoveAll(block); err != nil {
			t.Fatal(err)
		}
		if w := share(t, fx); w.Code != 200 {
			t.Fatalf("share: %d %s", w.Code, w.Body)
		}
		if row := credRowOf(t, fx); row.State != credScrubbed || row.Why != scrubShare {
			t.Fatalf("row after the share: %+v", row)
		}
	})

	t.Run("refresh", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		fx.scm.NoCache = true
		fx.ensure(t)
		old := credRowOf(t, fx)
		l := scmLiveGet(scmLiveKey(fx.p.ID, fx.ref, "github.com"))
		fx.m.FailNext("run", 503, "unavailable", "down")
		if err := ensureCreds(context.Background(), fx.p, nil, "", time.Until(time.UnixMilli(l.expires))+time.Minute); err == nil {
			t.Fatal("the refresh went through")
		}
		if len(fx.scm.Tokens()) != 2 {
			t.Fatalf("tokens: %d", len(fx.scm.Tokens()))
		}
		if row := credRowOf(t, fx); row != old {
			t.Fatalf("row after a failed refresh: %+v, was %+v", row, old)
		}
		if len(fx.scm.Revoked()) != 0 {
			t.Fatalf("a failed refresh revoked the older value: %v", fx.scm.Revoked())
		}
		if w := share(t, fx); w.Code != 200 {
			t.Fatalf("share: %d %s", w.Code, w.Body)
		}
		for _, tok := range fx.scm.Tokens() {
			if f := holds(fx, tok.Value); f != "" {
				t.Fatalf("%s holds a token after the share", f)
			}
		}
		if !slices.Contains(fx.scm.Revoked(), purpose(fx)) {
			t.Fatalf("not revoked: %v", fx.scm.Revoked())
		}
	})
}

// Stopping, archiving and deleting a sandbox through the agent, and Forget,
// each take the credential out (Forget: before the provider forgets the
// sign-in); Forget is a person's, in their own partition.
func TestScrubOnStopDeleteForget(t *testing.T) {
	t.Run("stop and delete", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		mux := fx.h.(*http.ServeMux)
		esc := url.PathEscape(fx.ref)
		fx.ensure(t)
		tok := fx.scm.Tokens()[0].Value
		if w := callAs(t, mux, asAlice, "POST", "/sandboxes/"+esc+"/stop", nil); w.Code != 200 {
			t.Fatalf("stop: %d %s", w.Code, w.Body)
		}
		fx.scrubbed(t, scrubStop, tok)
		if w := callAs(t, mux, asAlice, "POST", "/sandboxes/"+esc+"/start", nil); w.Code != 200 {
			t.Fatalf("start: %d %s", w.Code, w.Body)
		}
		fx.scm.NoCache = true
		fx.ensure(t)
		tok = fx.scm.Tokens()[1].Value
		if w := callAs(t, mux, asAlice, "DELETE", "/sandboxes/"+esc, nil); w.Code != 200 {
			t.Fatalf("delete: %d %s", w.Code, w.Body)
		}
		fx.gone(t, scrubDelete, tok)
	})
	t.Run("archive", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		fx.ensure(t)
		tok := fx.scm.Tokens()[0].Value
		if w := callAs(t, fx.h.(*http.ServeMux), asAlice, "POST", "/sandboxes/"+url.PathEscape(fx.ref)+"/archive", nil); w.Code != 200 {
			t.Fatalf("archive: %d %s", w.Code, w.Body)
		}
		fx.scrubbed(t, scrubArchive, tok)
	})
	t.Run("a project the store doesn't list there", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		fx.ensure(t)
		tok := fx.scm.Tokens()[0].Value
		projectsInSandbox = func(string) []*Project { return nil } // the row alone says whose: revoked at every bound provider
		if w := callAs(t, fx.h.(*http.ServeMux), asAlice, "POST", "/sandboxes/"+url.PathEscape(fx.ref)+"/stop", nil); w.Code != 200 {
			t.Fatalf("stop: %d %s", w.Code, w.Body)
		}
		fx.scrubbed(t, scrubStop, tok)
	})
	t.Run("forget", func(t *testing.T) {
		fx := credFixture(t, modeUser)
		mux := fx.h.(*http.ServeMux)
		fx.ensure(t)
		tok := fx.scm.Tokens()[0].Value
		if w := callAs(t, mux, asViewAs, "DELETE", "/projects/scm/signin", nil); w.Code != 403 {
			t.Fatalf("view-as forgets: %d", w.Code)
		}
		if w := callAs(t, mux, asAlice, "DELETE", "/projects/scm/signin", nil); w.Code != 204 {
			t.Fatalf("forget: %d %s", w.Code, w.Body)
		}
		fx.scrubbed(t, scrubForget, tok)
		reqs := fx.scm.Requests("")
		var revokeAt, forgetAt int
		for i, r := range reqs {
			switch r.Method + " " + r.Path {
			case "POST /token/revoke":
				revokeAt = i
			case "DELETE /signin":
				forgetAt = i
			}
		}
		if forgetAt == 0 || revokeAt > forgetAt || fx.scm.Person != nil {
			t.Fatalf("the provider forgot first, or not at all: %v", reqs)
		}
	})
	t.Run("forget at global", func(t *testing.T) {
		fx := credFixture(t, modeGlobal)
		if w := callAs(t, fx.h.(*http.ServeMux), asAlice, "DELETE", "/projects/scm/signin", nil); w.Code != 409 || !strings.Contains(w.Body.String(), "your own space") {
			t.Fatalf("at global: %d %s", w.Code, w.Body)
		}
	})
}

// The scrub job records §14.1's word: the one its queuer put in its step,
// else a repo's job is a repo removed, an archived or deleting project's
// archive or delete, a task's fork delete, else the sandbox left.
func TestScrubJobWhy(t *testing.T) {
	active, archived, deleting := &Project{State: projActive}, &Project{State: projArchived}, &Project{State: projDeleting}
	fork := &ProjectTask{ForkMade: true, SandboxRef: "apps/cs|f"}
	for _, c := range []struct {
		p    *Project
		k    *ProjectTask
		j    *ProjectJob
		want string
	}{
		{active, nil, &ProjectJob{Step: scrubForget}, scrubForget},
		{active, nil, &ProjectJob{Step: "running"}, scrubLeft},
		{active, nil, &ProjectJob{Repo: "web"}, scrubRepoRemoved},
		{archived, nil, &ProjectJob{}, scrubArchive},
		{deleting, nil, nil, scrubDelete},
		{active, fork, &ProjectJob{}, scrubDelete},
		{active, nil, &ProjectJob{}, scrubLeft},
	} {
		if got := scmScrubWhy(c.p, c.k, c.j); got != c.want {
			t.Errorf("%+v %+v: %q, want %q", c.p.State, c.j, got, c.want)
		}
	}
}

// A 409 signin keeps the device code for that person and provider only (the
// gate's park shows it to them), parks the task on it, and the creds job
// polls until the sign-in is done — then writes the credential, clears the
// code, puts the task back and wakes its parked run.
func TestPendingSigninKept(t *testing.T) {
	fx := credFixture(t, modeUser)
	fx.scm.Person = nil
	run := fx.task(t, 1, statusSleep)
	if err := fx.ag.db.setStatus(run, statusSleep, 0, "", `{"kind":"project"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE project_tasks SET ws='preparing'`); err != nil {
		t.Fatal(err)
	}
	k := &ProjectTask{ProjectID: fx.p.ID, N: 1, RunID: run}
	job := &ProjectJob{Kind: pjCreds, Created: nowMs()}
	out, err := scmCredsJob(context.Background(), fx.p, k, job)
	if err != nil || out.Done || out.WaitMs != 5000 {
		t.Fatalf("the creds job: %+v %v", out, err)
	}
	s := scmPendingSignin("alice", "apps/scm-github")
	if s == nil || s.UserCode != "ABCD-1234" || s.PollID == "" {
		t.Fatalf("kept: %+v", s)
	}
	if scmPendingSignin("bob", "apps/scm-github") != nil || scmPendingSignin("alice", "apps/other") != nil {
		t.Fatal("another person's or provider's")
	}
	var ws string
	_ = fx.ag.db.q.QueryRow(`SELECT ws FROM project_tasks WHERE n=1`).Scan(&ws)
	if ws != wsSignin {
		t.Fatalf("ws: %s", ws)
	}
	// still pending: the job polls and waits
	if out, err = scmCredsJob(context.Background(), fx.p, k, job); err != nil || out.WaitMs < 2000 || out.Done {
		t.Fatalf("pending: %+v %v", out, err)
	}
	if n := len(fx.scm.Requests("GET /signin/" + s.PollID)); n != 1 {
		t.Fatalf("polled %d times", n)
	}
	fx.scm.CompleteSignin()
	if out, err = scmCredsJob(context.Background(), fx.p, k, job); err != nil || !out.Done {
		t.Fatalf("done: %+v %v", out, err)
	}
	if scmPendingSignin("alice", "apps/scm-github") != nil {
		t.Fatal("the code is still kept")
	}
	_ = fx.ag.db.q.QueryRow(`SELECT ws FROM project_tasks WHERE n=1`).Scan(&ws)
	if ws != wsPreparing {
		t.Fatalf("ws after: %s", ws)
	}
	var wakes int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND kind=?`, run, inboxWake).Scan(&wakes)
	if wakes == 0 {
		t.Fatal("the parked run wasn't woken")
	}
	if row := credRowOf(t, fx); row.State != credLive || row.Login != "octocat" {
		t.Fatalf("row: %+v", row)
	}
	// a sign-in nobody finishes ends the job after 15 minutes
	fx.scm.Person = nil
	scmLiveDrop(scmLiveKey(fx.p.ID, fx.ref, "github.com"))
	old := &ProjectJob{Kind: pjCreds, Created: nowMs() - 16*60*1000}
	if _, err := scmCredsJob(context.Background(), fx.p, k, old); err == nil || !strings.Contains(err.Error(), "15 minutes") {
		t.Fatalf("an old sign-in: %v", err)
	}
}

// GET /projects/scm/signin reads the state and starts nothing; POST starts
// one; the poll says how it went. Only the person, in their own partition.
func TestSigninStateStartsNothing(t *testing.T) {
	fx := credFixture(t, modeUser)
	mux := fx.h.(*http.ServeMux)
	fx.scm.Person = nil
	var st scmSigninState
	w := callAs(t, mux, asAlice, "GET", "/projects/scm/signin", nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.State != "none" {
		t.Fatalf("GET: %d %s", w.Code, w.Body)
	}
	if len(fx.scm.Requests("POST /signin")) != 0 || len(fx.scm.Requests("POST /token")) != 0 || scmPendingSignin("alice", "apps/scm-github") != nil {
		t.Fatal("reading the state started a sign-in")
	}
	w = callAs(t, mux, asAlice, "POST", "/projects/scm/signin", map[string]string{"scm": "apps/scm-github"})
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil || st.State != "pending" || st.Signin == nil {
		t.Fatalf("POST: %d %s", w.Code, w.Body)
	}
	if scmPendingSignin("alice", "apps/scm-github") == nil {
		t.Fatal("the started sign-in isn't kept for the gate")
	}
	poll := "/projects/scm/signin/" + st.Signin.PollID
	if w = callAs(t, mux, asAlice, "GET", poll, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"pending"`) {
		t.Fatalf("poll: %d %s", w.Code, w.Body)
	}
	fx.scm.CompleteSignin()
	if w = callAs(t, mux, asAlice, "GET", poll, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"done"`) {
		t.Fatalf("poll done: %d %s", w.Code, w.Body)
	}
	if scmPendingSignin("alice", "apps/scm-github") != nil {
		t.Fatal("done, still kept")
	}
	if w = callAs(t, mux, asViewAs, "GET", "/projects/scm/signin", nil); w.Code != 403 {
		t.Fatalf("view-as: %d", w.Code)
	}
	if w = callAs(t, mux, asElement, "GET", "/projects/scm/signin", nil); w.Code != 403 {
		t.Fatalf("an element: %d", w.Code)
	}
	gfx := credFixture(t, modeGlobal)
	for _, m := range []string{"GET", "POST"} {
		if w := callAs(t, gfx.h.(*http.ServeMux), asAlice, m, "/projects/scm/signin", nil); w.Code != 409 {
			t.Fatalf("%s at global: %d %s", m, w.Code, w.Body)
		}
	}
}

// A provider's 409 signin reached through a route anyone may call (the
// repos list, in alice's partition) carries the device code to alice only:
// view-as, an element and another person get the refusal without it.
func TestSigninCodeOnlyToPerson(t *testing.T) {
	fx := credFixture(t, modeUser)
	mux := fx.h.(*http.ServeMux)
	fx.scm.Person = nil
	for name, c := range map[string]caller{"view-as": asViewAs, "an element": asElement, "another person": asMgr} {
		w := callAs(t, mux, c, "GET", "/projects/scm/repos", nil)
		if w.Code != 409 || !strings.Contains(w.Body.String(), `"refusal":"signin"`) {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "ABCD-1234") || strings.Contains(w.Body.String(), "userCode") || strings.Contains(w.Body.String(), "pollId") {
			t.Fatalf("%s got the device code: %s", name, w.Body)
		}
	}
	if w := callAs(t, mux, asAlice, "GET", "/projects/scm/repos", nil); w.Code != 409 || !strings.Contains(w.Body.String(), `"userCode":"ABCD-1234"`) {
		t.Fatalf("alice: %d %s", w.Code, w.Body)
	}
}

// The redactors mask GitHub's token shapes — up to a space, quote or @,
// whatever their charset and length — and every value handed out, exactly,
// same-length; so does every message row.
func TestRedactPatternsAndLive(t *testing.T) {
	long := "ghs_" + strings.Repeat("aZ9.-_+/=", 57) // a stateless installation token of ~520 characters
	for _, s := range []string{
		"ghp_" + strings.Repeat("a", 36), "gho_" + strings.Repeat("B", 36), "ghu_" + strings.Repeat("c", 40),
		"ghr_" + strings.Repeat("d", 76), long, "github_pat_11AAAA_" + strings.Repeat("e", 70),
	} {
		in := `push "https://x-access-token:` + s + `@github.com/acme/web" failed; token='` + s + `' ` + s + "\n"
		out := redactText(in)
		if strings.Contains(out, s) || len(out) != len(in) || !strings.Contains(out, "@github.com/acme/web") || !strings.Contains(out, redactMark) {
			t.Fatalf("%s…: %q", s[:8], out)
		}
	}
	if got := redactText("ghp_short and github_pat_x"); got != "ghp_short and github_pat_x" {
		t.Fatalf("short words are text: %q", got)
	}
	odd := "v1.0123456789abcdefABCDEF" // a provider's token of no known shape
	if redactText("x "+odd) != "x "+odd {
		t.Fatal("masked before it was handed out")
	}
	scmMask(newSCMSecret(odd), nowMs()+time.Hour.Milliseconds())
	t.Cleanup(func() {
		scmLiveMu.Lock()
		delete(scmRetired, odd)
		scmRebuildLocked()
		scmLiveMu.Unlock()
	})
	if got := redactText("x " + odd + " y"); strings.Contains(got, odd) || len(got) != len(odd)+4 {
		t.Fatalf("a live value: %q", got)
	}
	if got := newRedactor().text(odd); strings.Contains(got, odd) {
		t.Fatal("a harness's redactor doesn't mask a live value")
	}
	var buf bytes.Buffer
	rr := newRedactReader(strings.NewReader("{\"x\":\""+odd+"\"}\n"), func() *redactor { return nil })
	_, _ = buf.ReadFrom(rr)
	if strings.Contains(buf.String(), odd) {
		t.Fatalf("the harness stdout: %q", buf.String())
	}
	db := newTestDB(t)
	rid, err := db.createRun("t", "{}", 0)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.addMessage(&Message{RunID: rid, Role: "tool", Content: "cat: " + odd})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.rewriteMessage(rid, id, "again "+long); err != nil {
		t.Fatal(err)
	}
	if all := fullText(db, rid); strings.Contains(all, odd) || strings.Contains(all, long) {
		t.Fatalf("a row: %q", all)
	}
	var fts string
	_ = db.q.QueryRow(`SELECT content FROM messages_fts WHERE msg_id=?`, id).Scan(&fts)
	if strings.Contains(fts, long) {
		t.Fatal("the search index")
	}
}

// The seeded token: a project's credential, printed by bash in a plain
// conversation working in its sandbox — the file's content, the
// environment, an error — is in no row of any table, nor in the log.
func TestSeededTokenNeverStored(t *testing.T) {
	var logs bytes.Buffer
	var lmu sync.Mutex
	log.SetOutput(writerFunc(func(p []byte) (int, error) { lmu.Lock(); defer lmu.Unlock(); return logs.Write(p) }))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	fx := credFixture(t, modeGlobal)
	fx.scm.LongTokens = true
	fx.ensure(t)
	tok := fx.scm.Tokens()[0].Value
	cred := fx.credFile("github.com.cred")
	cfg := defaultConfig()
	cfg.Class = "coding"
	cfg.Features = map[string]bool{"streaming": false}
	b := sbxBindingOf(fx.box)
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	f := fakeOf(fx.ag)
	f.on(lastUser("leak it"), callTools(
		tc("c1", "bash", mustJSON(map[string]string{"command": "cat " + cred})),
		tc("c2", "bash", mustJSON(map[string]string{"command": "export T=$(sed -n 2p " + cred + " | cut -d= -f2); env | grep '^T='"})),
		tc("c3", "bash", mustJSON(map[string]string{"command": "git ls-remote \"https://x:$(sed -n 2p " + cred + " | cut -d= -f2)@127.0.0.1:1/x\"; echo \"error: bad $(sed -n 2p " + cred + ")\" >&2; exit 2"})),
	)).once()
	f.on(lastIs("tool", ""), say("done"))
	run := newRun(t, fx.ag, cfg, "leak it")
	waitFor(t, "the run to answer", func() bool { return strings.Contains(transcript(fx.ag.db, run), "A:done") })
	waitQuiet(t, fx.ag)
	all := fullText(fx.ag.db, run)
	if !strings.Contains(all, "password=") || !strings.Contains(all, "T=") || !strings.Contains(all, "error: bad") {
		t.Fatalf("the commands didn't print what they should: %q", clip(all, 2000))
	}
	if got := tablesHolding(t, fx.ag.db, "password="); !strings.Contains(" "+strings.Join(got, " ")+" ", " messages ") {
		t.Fatalf("the scan doesn't see what the transcript holds: %v", got)
	}
	if got := tablesHolding(t, fx.ag.db, tok[:40]); len(got) != 0 {
		t.Errorf("the token is in %v", got)
	}
	lmu.Lock()
	defer lmu.Unlock()
	if strings.Contains(logs.String(), tok[:40]) {
		t.Error("the token is in the log")
	}
}

// tablesHolding is every table of db with a row any of whose columns
// holds s.
func tablesHolding(t *testing.T, db *DB, s string) []string {
	t.Helper()
	rows, err := db.q.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables, out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tbl := range tables {
		cols, err := db.q.Query(`SELECT name FROM pragma_table_info(?)`, tbl)
		if err != nil {
			continue
		}
		var names []string
		for cols.Next() {
			var n string
			_ = cols.Scan(&n)
			names = append(names, `COALESCE(CAST("`+n+`" AS TEXT), '')`)
		}
		cols.Close()
		if len(names) == 0 {
			continue
		}
		var n int
		q := `SELECT count(*) FROM "` + tbl + `" WHERE instr(` + strings.Join(names, "||") + `, ?) > 0`
		if db.q.QueryRow(q, s).Scan(&n) == nil && n > 0 {
			out = append(out, tbl)
		}
	}
	return out
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
