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
		"its backend":  {Component: "apps/calendar", Via: "instance"},
		"a frame":      {Component: "apps/calendar", Via: "frame", UserID: "alice"},
		"another tile": {Component: "apps/other", Via: "terminal", UserID: "alice"},
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
}
