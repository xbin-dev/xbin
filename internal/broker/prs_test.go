package broker

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

const testSeries = `From 1234567890abcdef1234567890abcdef12345678 Mon Sep 17 00:00:00 2001
From: Agent A <agent@apps-calendar>
Subject: [PATCH] fix overflow

---
 index.html | 1 +
 1 file changed, 1 insertion(+)

diff --git a/index.html b/index.html
index 0000000..1111111 100644
--- a/index.html
+++ b/index.html
@@ -1 +1,2 @@
 <html>
+<!-- fixed -->
`

func prOpen(t *testing.T, b *Broker, p auth.Principal, target, title string) (*httptest.ResponseRecorder, *prMeta) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"target": target, "title": title, "message": "why + testing notes", "series": testSeries})
	r := httptest.NewRequest("POST", "/code/prs", strings.NewReader(string(body)))
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	b.apiPROpen(w, r)
	var m prMeta
	_ = json.Unmarshal(w.Body.Bytes(), &m)
	return w, &m
}

// Opening a PR needs read visibility on the target (read = suggest, D48):
// an instance principal without a code grant is refused; with code:<target>
// it may file; the target itself, terminals, and admins always may.
func TestPROpenGate(t *testing.T) {
	b := testBroker(t) // apps/calendar, apps/email

	// Unattributed INSTANCE principal (a backend): no grant → 403.
	inst := auth.Principal{Component: "apps/email", Via: "instance"}
	if w, _ := prOpen(t, b, inst, "apps/calendar", "x"); w.Code != 403 {
		t.Fatalf("ungranted instance: want 403, got %d %s", w.Code, w.Body.String())
	}
	// code:apps/calendar grant → allowed.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "code:apps/calendar", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	if w, m := prOpen(t, b, inst, "apps/calendar", "granted instance files"); w.Code != 200 {
		t.Fatalf("granted instance: want 200, got %d %s", w.Code, w.Body.String())
	} else if m.Number != 1 || m.State != "open" || m.From.Component != "apps/email" {
		t.Fatalf("bad meta: %+v", m)
	}
	// Owner-driven terminal principal (no user id): allowed, numbered 2.
	term := auth.Principal{Component: "apps/email", Via: "terminal"}
	if w, m := prOpen(t, b, term, "apps/calendar", "terminal files"); w.Code != 200 || m.Number != 2 {
		t.Fatalf("terminal: want 200/#2, got %d #%d", w.Code, m.Number)
	}
	// Not-a-component target → 404.
	if w, _ := prOpen(t, b, auth.Principal{Owner: true}, "apps/nope", "x"); w.Code != 404 {
		t.Fatalf("bad target: want 404, got %d", w.Code)
	}
	// A series with no diff is refused.
	body, _ := json.Marshal(map[string]any{"target": "apps/calendar", "title": "x", "series": "hello"})
	r := httptest.NewRequest("POST", "/code/prs", strings.NewReader(string(body)))
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	b.apiPROpen(w, r)
	if w.Code != 400 {
		t.Fatalf("non-patch series: want 400, got %d", w.Code)
	}
}

// List/get/series ride the same read gate; the series round-trips verbatim.
func TestPRListAndSeries(t *testing.T) {
	b := testBroker(t)
	if w, _ := prOpen(t, b, auth.Principal{Owner: true}, "apps/calendar", "first"); w.Code != 200 {
		t.Fatalf("open: %d", w.Code)
	}

	get := func(p auth.Principal, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		switch {
		case strings.HasPrefix(path, "/code/prs/summary"):
			b.apiPRSummary(w, r)
		case strings.HasPrefix(path, "/code/prs"):
			b.apiPRList(w, r)
		case strings.HasPrefix(path, "/code/pr/series"):
			b.apiPRSeries(w, r)
		default:
			b.apiPRGet(w, r)
		}
		return w
	}

	// Target list: self sees it; an ungranted instance doesn't.
	self := auth.Principal{Component: "apps/calendar", Via: "instance"}
	if w := get(self, "/code/prs?target=apps/calendar"); w.Code != 200 || !strings.Contains(w.Body.String(), `"first"`) {
		t.Fatalf("self list: %d %s", w.Code, w.Body.String())
	}
	other := auth.Principal{Component: "apps/email", Via: "instance"}
	if w := get(other, "/code/prs?target=apps/calendar"); w.Code != 403 {
		t.Fatalf("ungranted list: want 403, got %d", w.Code)
	}
	// Series is byte-exact.
	if w := get(self, "/code/pr/series?target=apps/calendar&n=1"); w.Body.String() != testSeries {
		t.Fatalf("series mismatch:\n%s", w.Body.String())
	}
	// Summary counts the open PR for a principal who can see the tile only.
	if w := get(self, "/code/prs/summary"); !strings.Contains(w.Body.String(), `"apps/calendar":1`) {
		t.Fatalf("summary: %s", w.Body.String())
	}
	if w := get(other, "/code/prs/summary"); strings.Contains(w.Body.String(), "calendar") {
		t.Fatalf("summary leaked to ungranted principal: %s", w.Body.String())
	}
}

// State transitions: merged/rejected are the target side's call, withdrawn
// the author's; a closed PR stays closed (no reopen, no double-close).
func TestPRStateMachine(t *testing.T) {
	b := testBroker(t)
	author := auth.Principal{Component: "apps/email", Via: "terminal"} // owner-driven → read ok
	if w, _ := prOpen(t, b, author, "apps/calendar", "state test"); w.Code != 200 {
		t.Fatalf("open: %d", w.Code)
	}

	setState := func(p auth.Principal, state string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"target": "apps/calendar", "n": 1, "state": state, "comment": "because"})
		r := httptest.NewRequest("POST", "/code/pr/state", strings.NewReader(string(body)))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiPRState(w, r)
		return w
	}

	// The author (not target-side) cannot merge its own PR…
	authorInst := auth.Principal{Component: "apps/email", Via: "instance"}
	if w := setState(authorInst, "merged"); w.Code != 403 {
		t.Fatalf("author merge: want 403, got %d %s", w.Code, w.Body.String())
	}
	// …but can withdraw it.
	if w := setState(authorInst, "withdrawn"); w.Code != 200 {
		t.Fatalf("author withdraw: want 200, got %d %s", w.Code, w.Body.String())
	}
	// Closed is closed.
	if w := setState(auth.Principal{Owner: true}, "merged"); w.Code != 409 {
		t.Fatalf("double close: want 409, got %d", w.Code)
	}

	// Fresh PR: the target's own principal merges it.
	if w, _ := prOpen(t, b, author, "apps/calendar", "state test 2"); w.Code != 200 {
		t.Fatalf("open2: %d", w.Code)
	}
	target := auth.Principal{Component: "apps/calendar", Via: "instance"}
	body, _ := json.Marshal(map[string]any{"target": "apps/calendar", "n": 2, "state": "merged", "comment": "applied as abc123"})
	r := httptest.NewRequest("POST", "/code/pr/state", strings.NewReader(string(body)))
	r = r.WithContext(auth.WithPrincipal(r.Context(), target))
	w := httptest.NewRecorder()
	b.apiPRState(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"merged"`) {
		t.Fatalf("target merge: %d %s", w.Code, w.Body.String())
	}
	// A withdrawn-by-stranger attempt on someone else's PR is refused.
	if w, _ := prOpen(t, b, auth.Principal{Owner: true}, "apps/calendar", "owner filed"); w.Code != 200 {
		t.Fatal("open3")
	}
	body, _ = json.Marshal(map[string]any{"target": "apps/calendar", "n": 3, "state": "withdrawn"})
	r = httptest.NewRequest("POST", "/code/pr/state", strings.NewReader(string(body)))
	r = r.WithContext(auth.WithPrincipal(r.Context(), authorInst))
	w = httptest.NewRecorder()
	b.apiPRState(w, r)
	if w.Code != 403 {
		t.Fatalf("stranger withdraw: want 403, got %d", w.Code)
	}
}

// Comments append to the review thread and are read-gated.
func TestPRComment(t *testing.T) {
	b := testBroker(t)
	if w, _ := prOpen(t, b, auth.Principal{Owner: true}, "apps/calendar", "comment test"); w.Code != 200 {
		t.Fatal("open")
	}
	comment := func(p auth.Principal, text string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"target": "apps/calendar", "n": 1, "body": text})
		r := httptest.NewRequest("POST", "/code/pr/comment", strings.NewReader(string(body)))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiPRComment(w, r)
		return w
	}
	if w := comment(auth.Principal{Component: "apps/calendar", Via: "instance"}, "needs a test"); w.Code != 200 {
		t.Fatalf("target comment: %d %s", w.Code, w.Body.String())
	}
	if w := comment(auth.Principal{Component: "apps/email", Via: "instance"}, "sneaky"); w.Code != 403 {
		t.Fatalf("ungranted comment: want 403, got %d", w.Code)
	}
	m, err := b.prLoad("apps/calendar", 1)
	if err != nil || len(m.Events) != 1 || m.Events[0].Body != "needs a test" || m.Events[0].Who != "apps/calendar" {
		t.Fatalf("thread: %+v err=%v", m, err)
	}
}
