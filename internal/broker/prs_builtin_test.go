package broker

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/builtins"
)

// Builtin updates filed as PRs (update mode "pr", D49): proposals carry
// kind/builtin/toHash, refiling the same embed is idempotent, a newer embed
// supersedes (auto-withdraws) the stale open proposal, and merged-close
// refreshes update tracking so the unit stops being offered.
func TestBuiltinUpdatePR(t *testing.T) {
	b := testBroker(t) // workspace has apps/calendar with <html></html>

	v1 := builtins.NewUpdater(b.Reg.Root, nil, fstest.MapFS{
		"apps/calendar/index.html": {Data: []byte("<html>upstream-v1</html>\n")},
	})
	b.SetUpdater(v1)
	owner := auth.Principal{Owner: true}

	m1, _, err := b.ProposeBuiltinPR("scaffold:apps/calendar", owner)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Number != 1 || m1.Kind != "builtin-update" || m1.Builtin != "scaffold:apps/calendar" || m1.ToHash == "" {
		t.Fatalf("bad proposal meta: %+v", m1)
	}
	// Same embed again → the same open PR, no duplicate.
	if again, _, err := b.ProposeBuiltinPR("scaffold:apps/calendar", owner); err != nil || again.Number != 1 {
		t.Fatalf("refile must be idempotent: %+v err=%v", again, err)
	}

	// A newer embed supersedes: old proposal auto-withdrawn, new one filed.
	v2 := builtins.NewUpdater(b.Reg.Root, nil, fstest.MapFS{
		"apps/calendar/index.html": {Data: []byte("<html>upstream-v2</html>\n")},
	})
	b.SetUpdater(v2)
	m2, _, err := b.ProposeBuiltinPR("scaffold:apps/calendar", owner)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Number != 2 {
		t.Fatalf("superseding proposal should be #2: %+v", m2)
	}
	old, err := b.prLoad("apps/calendar", 1)
	if err != nil || old.State != "withdrawn" {
		t.Fatalf("stale proposal not withdrawn: %+v err=%v", old, err)
	}

	// Merged-close refreshes provenance → the update is no longer offered
	// (the tile holds it: its plane applied the series).
	if err := os.WriteFile(filepath.Join(b.Reg.Root, "apps", "calendar", "index.html"), []byte("<html>upstream-v2</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"target": "apps/calendar", "n": 2, "state": "merged", "comment": "applied"})
	r := httptest.NewRequest("POST", "/code/pr/state", strings.NewReader(string(body)))
	r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
	w := httptest.NewRecorder()
	b.apiPRState(w, r)
	if w.Code != 200 {
		t.Fatalf("merged close: %d %s", w.Code, w.Body.String())
	}
	ups, err := v2.Updates()
	if err != nil {
		t.Fatal(err)
	}
	for _, uu := range ups {
		if uu.ID == "scaffold:apps/calendar" {
			t.Fatalf("update still offered after merged-close: %+v", uu)
		}
	}
}

// covers the review of 2026-11-20, finding 5 — a builtin-update proposal
// is a security update the tile's own code must not bury: its backend, a
// frame of it and another tile's code can't close it merged (a person, or a
// person's terminal session in the tile, can), and closing it merged
// records only what the tile holds — the rest stays offered, named in the
// PR's pending.
func TestBuiltinUpdateMergedCloseIsAPersonsAndRecordsWhatIsThere(t *testing.T) {
	b := testBroker(t) // workspace has apps/calendar with <html></html>
	u := builtins.NewUpdater(b.Reg.Root, nil, fstest.MapFS{
		"apps/calendar/index.html": {Data: []byte("<html>upstream</html>\n")},
	})
	b.SetUpdater(u)
	m, _, err := b.ProposeBuiltinPR("scaffold:apps/calendar", auth.Principal{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	closeAs := func(p auth.Principal) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"target": "apps/calendar", "n": m.Number, "state": "merged"})
		r := httptest.NewRequest("POST", "/code/pr/state", strings.NewReader(string(body)))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiPRState(w, r)
		return w
	}
	for name, p := range map[string]auth.Principal{
		"its backend":             {Component: "apps/calendar", Via: "instance"},
		"a partition of it":       {Component: "apps/calendar", Via: "instance", Partition: "u-alice"},
		"a frame":                 {Component: "apps/calendar", Via: "frame", UserID: "alice"},
		"a cron job":              {Component: "apps/calendar", Via: "cron"},
		"another tile":            {Component: "apps/other", Via: "terminal", UserID: "alice"},
		"a deployment's terminal": {Component: "apps/calendar", Via: "terminal", UserID: "alice", Deployment: "staging"},
		"a person's agent":        {UserID: "alice", Via: "delegate", Agent: "xbin/wsagent"},
		"view-as":                 {Owner: true, UserID: "alice", Impersonator: "admin"},
	} {
		if w := closeAs(p); w.Code != 403 {
			t.Fatalf("%s closed it merged: %d %s", name, w.Code, w.Body.String())
		}
	}
	offeredNow := func() bool {
		ups, err := u.Updates()
		if err != nil {
			t.Fatal(err)
		}
		for _, uu := range ups {
			if uu.ID == "scaffold:apps/calendar" {
				return true
			}
		}
		return false
	}
	// A person's terminal session in the tile closes it merged — without
	// having applied anything: the update stays offered, and the PR says
	// which file still differs.
	w := closeAs(auth.Principal{Component: "apps/calendar", Via: "terminal", UserID: "alice"})
	if w.Code != 200 {
		t.Fatalf("the terminal session's merged close: %d %s", w.Code, w.Body.String())
	}
	var got prMeta
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Pending) != 1 || got.Pending[0] != "apps/calendar/index.html" {
		t.Fatalf("pending: %v", got.Pending)
	}
	if !offeredNow() {
		t.Fatal("closing merged without applying buried the update")
	}
	// a workspace with no accounts (the root token): the terminal it opens
	// carries no user, and closes the tile's proposal merged as it did
	// (re-review of 2026-11-20, M2)
	m2, _, err := b.ProposeBuiltinPR("scaffold:apps/calendar", auth.Principal{Owner: true})
	if err != nil {
		t.Fatal(err)
	}
	m = m2
	if w := closeAs(auth.Principal{Component: "apps/calendar", Via: "terminal"}); w.Code != 200 {
		t.Fatalf("the root token's terminal: %d %s", w.Code, w.Body.String())
	}
}

// covers the re-review of 2026-11-20 (missing tests) — the HTTP handlers
// with a link in the tile: GET /builtins/updates says it (a conflict with
// its note); POST /builtins/update's merge leaves it and says so, pr files
// a series without what it names, replace puts upstream's file in its
// place and says that, pin and unpin answer — and the secret it names is
// never read or written.
func TestBuiltinUpdateHandlersWithALink(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	v1 := fstest.MapFS{"apps/calendar/index.html": {Data: []byte("<p>v1</p>\n")}, "apps/calendar/app.js": {Data: []byte("one\n")}}
	v2 := fstest.MapFS{"apps/calendar/index.html": {Data: []byte("<p>v2</p>\n")}, "apps/calendar/app.js": {Data: []byte("two\n")}}
	if _, err := builtins.NewUpdater(root, nil, v1).ApplyReplace("scaffold:apps/calendar"); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "homes", "admin", "creds")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("TOKEN-s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "apps", "calendar", "app.js")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	b.SetUpdater(builtins.NewUpdater(root, nil, v2))
	owner := auth.Principal{Owner: true}
	call := func(method, path, body string) (int, string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
		w := httptest.NewRecorder()
		if method == "GET" {
			b.apiBuiltinsUpdates(w, r)
		} else {
			b.apiBuiltinsUpdate(w, r)
		}
		return w.Code, w.Body.String()
	}
	if code, body := call("GET", "/builtins/updates", ""); code != 200 || !strings.Contains(body, `"note":"apps/calendar/app.js is a symbolic link`) {
		t.Fatalf("the listing: %d %s", code, body)
	}
	if code, body := call("POST", "/builtins/update", `{"id":"scaffold:apps/calendar","mode":"merge"}`); code != 200 || !strings.Contains(body, "apps/calendar/app.js is a symbolic link") {
		t.Fatalf("merge: %d %s", code, body)
	}
	if l, err := os.Readlink(link); err != nil || l != secret {
		t.Fatal("merge touched the link")
	}
	code, body := call("POST", "/builtins/update", `{"id":"scaffold:apps/calendar","mode":"pr"}`)
	if code != 200 || strings.Contains(body, "s3cret") {
		t.Fatalf("pr: %d %s", code, body)
	}
	if series, _ := os.ReadFile(filepath.Join(b.prDir("apps/calendar"), "1", "series.mbox")); strings.Contains(string(series), "s3cret") || strings.Contains(string(series), "diff --git a/app.js") {
		t.Fatalf("the series holds the link's target or the link:\n%s", series)
	}
	for _, mode := range []string{"pin", "unpin"} {
		if code, body := call("POST", "/builtins/update", `{"id":"scaffold:apps/calendar","mode":"`+mode+`"}`); code != 200 {
			t.Fatalf("%s: %d %s", mode, code, body)
		}
	}
	if code, body := call("POST", "/builtins/update", `{"id":"scaffold:apps/calendar","mode":"replace"}`); code != 200 || !strings.Contains(body, "replace put upstream's file in its place") {
		t.Fatalf("replace: %d %s", code, body)
	}
	if got, _ := os.ReadFile(secret); string(got) != "TOKEN-s3cret" {
		t.Fatal("the secret changed")
	}
	if got, _ := os.ReadFile(link); string(got) != "two\n" {
		t.Fatalf("replace: app.js is %q", got)
	}
}

// covers the re-review of 2026-11-20, L5 — a proposal with nothing to
// propose (every file that differs is a link the update leaves alone)
// doesn't claim the update applied: no PR, the link's file stays offered,
// and the notes say so.
func TestBuiltinUpdatePRWithNothingToProposeSaysWhatStays(t *testing.T) {
	b := testBroker(t)
	root := b.Reg.Root
	v1 := fstest.MapFS{"apps/calendar/index.html": {Data: []byte("<p>v1</p>\n")}, "apps/calendar/app.js": {Data: []byte("one\n")}}
	v2 := fstest.MapFS{"apps/calendar/index.html": {Data: []byte("<p>v1</p>\n")}, "apps/calendar/app.js": {Data: []byte("two\n")}}
	if _, err := builtins.NewUpdater(root, nil, v1).ApplyReplace("scaffold:apps/calendar"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "apps", "calendar", "app.js")
	if err := os.WriteFile(filepath.Join(root, "apps", "calendar", "shared.js"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("shared.js", link); err != nil {
		t.Fatal(err)
	}
	u := builtins.NewUpdater(root, nil, v2)
	b.SetUpdater(u)
	m, notes, err := b.ProposeBuiltinPR("scaffold:apps/calendar", auth.Principal{Owner: true})
	if err != nil || m != nil {
		t.Fatalf("a proposal of nothing: %+v %v", m, err)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "nothing could be proposed for apps/calendar/app.js") {
		t.Fatalf("the notes: %v", notes)
	}
	ups, _ := u.Updates()
	if len(ups) != 1 || ups[0].ID != "scaffold:apps/calendar" {
		t.Fatalf("the update isn't offered any more: %+v", ups)
	}
}
