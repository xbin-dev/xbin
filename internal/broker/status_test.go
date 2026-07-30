package broker

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/magik6k/xbin/internal/auth"
)

// A component reports its OWN status; the owner may report for any; a cross-
// component element is refused. The GET snapshot is read-filtered. "ok" with an
// empty message clears; transient reports don't persist.
func TestTileStatus(t *testing.T) {
	b := testBroker(t) // apps/calendar, apps/email
	b.statuses = map[string]statusRec{}

	set := func(p auth.Principal, query, body string) int {
		r := httptest.NewRequest("POST", "/tile-status"+query, strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiStatusSet(w, r)
		return w.Code
	}
	list := func(p auth.Principal) map[string]statusRec {
		r := httptest.NewRequest("GET", "/tile-status", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiStatusList(w, r)
		var out struct {
			Statuses map[string]statusRec `json:"statuses"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		return out.Statuses
	}

	// element reports its own status → stored
	if got := set(auth.Principal{Component: "apps/calendar"}, "", `{"level":"error","message":"boom"}`); got != 200 {
		t.Fatalf("self report: want 200, got %d", got)
	}
	if s := list(auth.Principal{Owner: true})["apps/calendar"]; s.Level != "error" || s.Message != "boom" {
		t.Fatalf("stored status wrong: %+v", s)
	}

	// element may NOT report for another component
	if got := set(auth.Principal{Component: "apps/email"}, "?component=apps/calendar", `{"level":"warn"}`); got != 403 {
		t.Fatalf("cross-component report: want 403, got %d", got)
	}

	// owner may report for any component
	if got := set(auth.Principal{Owner: true}, "?component=apps/email", `{"level":"warn","message":"slow"}`); got != 200 {
		t.Fatalf("owner report: want 200, got %d", got)
	}

	// owner sees both (the GET snapshot is read-filtered by CanReadTile; this
	// test broker has no Users, so single-user mode shows all — the RBAC path is
	// covered by the users/auth tests).
	if got := list(auth.Principal{Owner: true}); len(got) != 2 {
		t.Fatalf("owner should see 2 statuses, got %d", len(got))
	}

	// invalid level → 400
	if got := set(auth.Principal{Component: "apps/calendar"}, "", `{"level":"nope"}`); got != 400 {
		t.Fatalf("bad level: want 400, got %d", got)
	}

	// "ok" + empty message clears
	if got := set(auth.Principal{Component: "apps/calendar"}, "", `{"level":"ok"}`); got != 200 {
		t.Fatalf("clear: want 200, got %d", got)
	}
	if _, ok := list(auth.Principal{Owner: true})["apps/calendar"]; ok {
		t.Fatal("status should be cleared")
	}

	// transient report does not persist
	if got := set(auth.Principal{Component: "apps/email"}, "", `{"level":"info","message":"ping","transient":true}`); got != 200 {
		t.Fatalf("transient: want 200, got %d", got)
	}
	if s := list(auth.Principal{Owner: true})["apps/email"]; s.Level != "warn" {
		t.Fatalf("transient must not overwrite stored status, got %+v", s)
	}
}
