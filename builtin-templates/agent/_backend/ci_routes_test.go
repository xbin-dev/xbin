package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// slowChecks puts the fake provider behind a server whose GET /scm/checks
// takes d (reads overlap).
func (fx *ciFix) slowChecks(t *testing.T, d time.Duration) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/scm/checks") {
			time.Sleep(d)
		}
		fx.scm.serve(w, r)
	}))
	t.Cleanup(srv.Close)
	raw, _ := json.Marshal([]map[string]string{{"provider": "apps/scm-github", "url": srv.URL + "/", "service": "scm"}})
	t.Setenv("XBIN_IFACE_SCM", string(raw))
	forgetSCMHellos()
}

func (fx *ciFix) ago(t *testing.T, id int64, d time.Duration) {
	t.Helper()
	if _, err := fx.ag.db.q.Exec(`UPDATE ci_watch SET fetched_ms=?, updated_ms=? WHERE id=?`, time.Now().Add(-d).UnixMilli(), time.Now().Add(-d).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
}

// fresh=1 reads a watch whose snapshot is older than 30 s (webhooks
// healthy; 10 s not) while it has anything pending — one upstream read
// however many ask at once; a passed one, a young one, or a request
// without fresh reads nothing (but a watch never read is read once).
func TestCIFreshCoalesced(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	pending := ciChecks(ciSHA1)
	pending.Checks[2].Conclusion, pending.Checks[2].Status = "", "in_progress" // nothing failed yet
	fx.scm.SetChecks("acme/web", "feature", pending)
	w := fx.watchRow(t, root, "feature", ciSHA1, nil)
	count := func() int { return len(fx.scm.Requests("GET /checks")) }
	var v CIView
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", root), &v) // never read: read once, without fresh
	if count() != 1 || v.Summary.State != ciPending || v.Watches[0].Checks == nil {
		t.Fatalf("the first look: %d reads, %+v", count(), v.Summary)
	}
	fx.slowChecks(t, 300*time.Millisecond)
	fx.ago(t, w.ID, 40*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(c caller) {
			defer wg.Done()
			if code := ciGET(t, fx.mux, c, fmt.Sprintf("/runs/%d/ci?fresh=1", root), nil); code != 200 {
				t.Errorf("GET fresh: %d", code)
			}
		}([]caller{asAlice, asSystem}[i%2])
	}
	wg.Wait()
	if count() != 2 {
		t.Fatalf("six viewers at once read %d times", count()-1)
	}
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci?fresh=1", root), nil) // young: nothing
	fx.ago(t, w.ID, 20*time.Second)                                          // healthy webhooks: 30 s
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci?fresh=1", root), nil)
	fx.ago(t, w.ID, 40*time.Second)
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", root), nil) // no fresh: nothing
	if count() != 2 {
		t.Fatalf("read when nothing asked for it: %d", count())
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE ci_watch SET state='success' WHERE id=?`, w.ID); err != nil {
		t.Fatal(err)
	}
	fx.ago(t, w.ID, time.Hour)
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci?fresh=1", root), nil) // done: nothing to learn
	if count() != 2 {
		t.Fatalf("a finished watch read again: %d", count())
	}
}

// A job's log: ANSI stripped, invisible characters gone, tokens masked;
// the tail held to 256 KiB; the provider's offsets passed through.
func TestCILogRedactedAndStripped(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	w := fx.watchRow(t, root, "feature", ciSHA1, ciChecks(ciSHA1))
	tok := "ghs_" + strings.Repeat("Z", 36)
	fx.scm.SetJobLog("88001", "\x1b[32mok\x1b[0m step 1\r\n\x1b]0;title\x07pass​word="+tok+"\nprogress 10%\rprogress 100%\n", false)
	var lg map[string]any
	if code := ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci/jobs/88001/log?watch=%d&tail=999999", root, w.ID), &lg); code != 200 {
		t.Fatalf("the log: %d", code)
	}
	text := lg["text"].(string)
	if strings.Contains(text, "\x1b") || strings.Contains(text, "\r") || strings.Contains(text, "​") || strings.Contains(text, tok) {
		t.Fatalf("the log as served: %q", text)
	}
	if !strings.HasPrefix(text, "ok step 1\npassword=[redacted]") || !strings.Contains(text, "progress 10%\nprogress 100%") {
		t.Fatalf("the log's words: %q", text)
	}
	req := fx.scm.Requests("GET /checks/jobs/88001/log")
	if len(req) != 1 || !strings.Contains(req[0].Query, "tailBytes=262144") || !strings.Contains(req[0].Query, "as=bot") {
		t.Fatalf("asked the provider: %+v", req)
	}
	if lg["complete"] != true || lg["url"] == "" {
		t.Fatalf("the log's fields: %v", lg)
	}
	if code := ciGET(t, fx.mux, asBob, fmt.Sprintf("/runs/%d/ci/jobs/88001/log?watch=%d", root, w.ID), nil); code != 404 {
		t.Fatalf("someone else's conversation's log: %d", code)
	}
}

// A running job's log on a host that serves logs once a job ends: 409
// in-progress with the live log's link.
func TestCILogInProgress(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	w := fx.watchRow(t, root, "feature", ciSHA1, ciChecks(ciSHA1))
	fx.scm.SetJobLog("88001", "", true)
	r := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/ci/jobs/88001/log?watch=%d", root, w.ID), nil)
	var body map[string]string
	_ = json.Unmarshal(r.Body.Bytes(), &body)
	if r.Code != 409 || body["refusal"] != scmRefInProgress || !strings.HasPrefix(body["url"], "https://github.com/acme/web/actions/runs/") {
		t.Fatalf("a running job's log: %d %s", r.Code, r.Body)
	}
}

// Annotations come redacted and clipped, their level one of three.
func TestCIAnnotationsRedacted(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	w := fx.watchRow(t, root, "feature", ciSHA1, ciChecks(ciSHA1))
	tok := "ghp_" + strings.Repeat("Q", 36)
	fx.scm.SetAnnotations("88100", []scmAnnotation{{Path: "auth/login.go", StartLine: 12, EndLine: 14, Level: "failure", Title: "uncovered",
		Message: "line not covered; \x1b[31m" + tok + "\x1b[0m " + strings.Repeat("m", 6000)}, {Path: "x", Level: "<b>", Message: "m"}})
	var out struct {
		Items []scmAnnotation `json:"items"`
	}
	if code := ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci/checks/88100/annotations?watch=%d", root, w.ID), &out); code != 200 || len(out.Items) != 2 {
		t.Fatalf("annotations: %d %+v", code, out)
	}
	a := out.Items[0]
	if strings.Contains(a.Message, tok) || strings.Contains(a.Message, "\x1b") || len(a.Message) > ciTextMax+4 || a.Path != "auth/login.go" || a.StartLine != 12 {
		t.Fatalf("an annotation as served: %+v", a)
	}
	if out.Items[1].Level != "notice" {
		t.Fatalf("an unknown level kept: %q", out.Items[1].Level)
	}
}

// A rerun is a person's own click, in their own partition, as themselves:
// view-as, a component, and anyone at an unpartitioned agent get 403
// identity; a provider without checks.rerun 501; the conversation's
// journal says who re-ran what.
func TestCIRerunPersonOnly(t *testing.T) {
	setMode(t, modeUser, "alice")
	fx := newCIFix(t)
	fx.scm.SetSignedIn("alice", 1)
	root := fx.conv(t, "alice", true)
	failed := ciChecks(ciSHA1)
	failed.WorkflowRuns[0].Status, failed.WorkflowRuns[0].Conclusion = "completed", "failure"
	w := fx.watchRow(t, root, "feature", ciSHA1, failed)
	body := map[string]any{"watch": w.ID, "runId": "7001", "failedOnly": true}
	var v CIView
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", root), &v)
	if !v.CanRerun || !v.CanWatch {
		t.Fatalf("alice in her own partition: canRerun %v canWatch %v", v.CanRerun, v.CanWatch)
	}
	for name, c := range map[string]caller{"view-as": asViewAs, "system": asSystem, "element": asElement} {
		r := callAs(t, fx.mux, c, "POST", fmt.Sprintf("/runs/%d/ci/rerun", root), body)
		if r.Code != 403 && r.Code != 404 {
			t.Errorf("%s re-runs: %d %s", name, r.Code, r.Body)
		}
	}
	r := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/rerun", root), body)
	if r.Code != 202 || !strings.Contains(r.Body.String(), `"attempt":2`) {
		t.Fatalf("alice re-runs: %d %s", r.Code, r.Body)
	}
	req := fx.scm.Requests("POST /checks/rerun")
	if len(req) != 1 || !strings.Contains(req[0].Body, `"as":"person"`) || !strings.Contains(req[0].Body, `"runId":"7001"`) {
		t.Fatalf("the provider was asked: %+v", req)
	}
	steps, _ := fx.ag.db.steps(root)
	noted := false
	for _, s := range steps {
		noted = noted || (s.Kind == "note" && strings.Contains(s.Detail, "alice re-ran the failed jobs of ci"))
	}
	if !noted {
		t.Fatal("no note in the journal")
	}
	fx.scm.mu.Lock()
	fx.scm.Caps = []string{scmCapCredentials, scmCapChecks, scmCapPulls}
	fx.scm.mu.Unlock()
	forgetSCMHellos()
	if r := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/rerun", root), body); r.Code != 501 {
		t.Fatalf("without checks.rerun: %d %s", r.Code, r.Body)
	}

	// an unpartitioned agent: no person identity, never the bot
	setMode(t, modeLegacy, "")
	fx2 := newCIFix(t)
	root2 := fx2.conv(t, "alice", true)
	w2 := fx2.watchRow(t, root2, "feature", ciSHA1, failed)
	r = callAs(t, fx2.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/rerun", root2), map[string]any{"watch": w2.ID, "runId": "7001", "failedOnly": true})
	if r.Code != 403 || !strings.Contains(r.Body.String(), `"refusal":"identity"`) {
		t.Fatalf("at an unpartitioned agent: %d %s", r.Code, r.Body)
	}
	if len(fx2.scm.Requests("POST /checks/rerun")) != 0 {
		t.Fatal("the bot was asked to re-run")
	}
	ciGET(t, fx2.mux, asAlice, fmt.Sprintf("/runs/%d/ci", root2), &v)
	if v.CanRerun {
		t.Fatal("canRerun at an unpartitioned agent")
	}
}

// An id a caller names must be in the watch's stored snapshot: a job's log,
// a check's annotations, a run's rerun of another id is 404 and asks the
// provider nothing; so is a watch of another conversation.
func TestCIIdsFromSnapshot(t *testing.T) {
	setMode(t, modeUser, "alice")
	fx := newCIFix(t)
	fx.scm.SetSignedIn("alice", 1)
	root := fx.conv(t, "alice", true)
	other := fx.conv(t, "alice", true)
	w := fx.watchRow(t, root, "feature", ciSHA1, ciChecks(ciSHA1))
	fx.scm.SetJobLog("12345", "secret log", false)
	for _, c := range []struct{ method, path string }{
		{"GET", fmt.Sprintf("/runs/%d/ci/jobs/12345/log?watch=%d", root, w.ID)},
		{"GET", fmt.Sprintf("/runs/%d/ci/jobs/88100/log?watch=%d", root, w.ID)}, // a check, not a job
		{"GET", fmt.Sprintf("/runs/%d/ci/jobs/88001/log?watch=%d", other, w.ID)},
		{"GET", fmt.Sprintf("/runs/%d/ci/checks/nope/annotations?watch=%d", root, w.ID)},
		{"GET", fmt.Sprintf("/runs/%d/ci/checks/88100/annotations?watch=%d", other, w.ID)},
	} {
		if r := callAs(t, fx.mux, asAlice, c.method, c.path, nil); r.Code != 404 {
			t.Errorf("%s %s: %d", c.method, c.path, r.Code)
		}
	}
	for _, body := range []map[string]any{{"watch": w.ID, "runId": "1"}, {"watch": w.ID, "runId": "88001"}} {
		if r := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/rerun", root), body); r.Code != 404 {
			t.Errorf("rerun %v: %d %s", body, r.Code, r.Body)
		}
	}
	if r := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/rerun", other), map[string]any{"watch": w.ID, "runId": "7001"}); r.Code != 404 {
		t.Errorf("another conversation's watch: %d", r.Code)
	}
	for _, route := range []string{"GET /checks/jobs/12345/log", "GET /checks/jobs/88100/log", "GET /checks/runs/nope/annotations", "POST /checks/rerun"} {
		if n := len(fx.scm.Requests(route)); n != 0 {
			t.Errorf("the provider was asked %s %d times", route, n)
		}
	}
}

// At most ten live watches a conversation: the oldest pushed one ends to
// make room, else 409 limit. A pull request's number is its head branch;
// a task's own watch stays; the run view carries the summary and canWatch.
func TestCIWatchLimits(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		return callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/ci/watch", root), body)
	}
	if r := post(map[string]any{"repo": "acme/web"}); r.Code != 400 {
		t.Fatalf("neither ref nor pr: %d", r.Code)
	}
	if r := post(map[string]any{"repo": "acme/web", "ref": "bad ref"}); r.Code != 400 {
		t.Fatalf("a bad ref: %d", r.Code)
	}
	if r := callAs(t, fx.mux, asBob, "POST", fmt.Sprintf("/runs/%d/ci/watch", root), map[string]any{"repo": "acme/web", "ref": "x"}); r.Code != 404 {
		t.Fatalf("bob: %d", r.Code)
	}
	fx.scm.AddPull("acme/web", &scmPull{Number: 42, State: "open", Head: scmRef{Ref: "xbin/fix", SHA: ciSHA2}})
	r := post(map[string]any{"repo": "acme/web", "pr": 42})
	var wv CIWatchView
	_ = json.Unmarshal(r.Body.Bytes(), &wv)
	if r.Code != 201 || wv.Ref != "xbin/fix" || wv.PR != 42 || wv.SHA != ciSHA2 || wv.URLs.PR != "https://github.com/acme/web/pull/42" {
		t.Fatalf("by PR number: %d %s", r.Code, r.Body)
	}
	if r := post(map[string]any{"repo": "acme/web", "pr": 42}); r.Code != 200 {
		t.Fatalf("the same again: %d", r.Code)
	}
	pushed := &ciWatch{RootRun: root, RunID: root, Source: ciPushed, SCM: "apps/scm-github", Repo: "acme/web", Ref: "pushed-1"}
	if err := fx.ag.db.Tx(func(t *DB) error { _, _, err := ciUpsert(t, pushed); return err }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ { // ten live now
		if r := post(map[string]any{"repo": "acme/web", "ref": fmt.Sprintf("b%d", i)}); r.Code != 201 {
			t.Fatalf("watch %d: %d %s", i, r.Code, r.Body)
		}
	}
	if r := post(map[string]any{"repo": "acme/web", "ref": "b8"}); r.Code != 201 {
		t.Fatalf("the eleventh, a pushed one to end: %d %s", r.Code, r.Body)
	}
	if x := fx.ag.db.ciWatchByID(pushed.ID); x.EndedMs == 0 {
		t.Fatal("the oldest pushed watch didn't make room")
	}
	if r := post(map[string]any{"repo": "acme/web", "ref": "b9"}); r.Code != 409 || !strings.Contains(r.Body.String(), `"refusal":"limit"`) {
		t.Fatalf("past ten, none pushed: %d %s", r.Code, r.Body)
	}
	if n := len(fx.live(root)); n != ciMaxLive {
		t.Fatalf("%d live", n)
	}
	// unwatch; a viewer may not; a task's own watch stays
	id := fx.live(root)[0].ID
	if r := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/ci/watch/%d", root, id), nil); r.Code != 204 {
		t.Fatalf("unwatch: %d", r.Code)
	}
	if len(fx.live(root)) != ciMaxLive-1 {
		t.Fatal("still watched")
	}
	_, k := fx.ciTask(t, ciSHA1, nil)
	if err := fx.ag.db.Tx(func(t *DB) error { p, _ := t.getProject(k.ProjectID); ciTaskRefs(t, p, k); return nil }); err != nil {
		t.Fatal(err)
	}
	tw := fx.live(k.RunID)
	if len(tw) != 1 {
		t.Fatalf("the task's watch: %s", ciDump(tw))
	}
	if r := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/ci/watch/%d", k.RunID, tw[0].ID), nil); r.Code != 409 {
		t.Fatalf("unwatching a task's own: %d", r.Code)
	}
	// the run view
	var view map[string]any
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/view", root), &view)
	ci, _ := view["ci"].(map[string]any)
	if ci == nil || ci["canWatch"] != true || ci["summary"] == nil {
		t.Fatalf("the run view's ci: %v", view["ci"])
	}
	plain := fx.conv(t, "alice", false)
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/view", plain), &view)
	if ci, _ := view["ci"].(map[string]any); ci == nil || ci["canWatch"] != false || ci["summary"] != nil {
		t.Fatalf("a conversation with no sandbox: %v", view["ci"])
	}
}
