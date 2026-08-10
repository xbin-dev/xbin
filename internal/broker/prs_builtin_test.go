package broker

import (
	"encoding/json"
	"net/http/httptest"
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

	m1, err := b.ProposeBuiltinPR("scaffold:apps/calendar", owner)
	if err != nil {
		t.Fatal(err)
	}
	if m1.Number != 1 || m1.Kind != "builtin-update" || m1.Builtin != "scaffold:apps/calendar" || m1.ToHash == "" {
		t.Fatalf("bad proposal meta: %+v", m1)
	}
	// Same embed again → the same open PR, no duplicate.
	if again, err := b.ProposeBuiltinPR("scaffold:apps/calendar", owner); err != nil || again.Number != 1 {
		t.Fatalf("refile must be idempotent: %+v err=%v", again, err)
	}

	// A newer embed supersedes: old proposal auto-withdrawn, new one filed.
	v2 := builtins.NewUpdater(b.Reg.Root, nil, fstest.MapFS{
		"apps/calendar/index.html": {Data: []byte("<html>upstream-v2</html>\n")},
	})
	b.SetUpdater(v2)
	m2, err := b.ProposeBuiltinPR("scaffold:apps/calendar", owner)
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

	// Merged-close refreshes provenance → the update is no longer offered.
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
