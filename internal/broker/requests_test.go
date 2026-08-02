package broker

import (
	"encoding/json"
	"testing"
)

// D36: the human request-access loop — file → visible to the right people →
// approve into an exact entry (or dismiss/withdraw).
func TestAccessRequestLoop(t *testing.T) {
	b, st := orgFixture(t) // sales owns apps/email; carol=admin, alice=read member, dave=outsider
	dave := principalFor(t, st, "dave")
	carol := principalFor(t, st, "carol")
	alice := principalFor(t, st, "alice")

	// dave (no access) requests write on the org tile.
	w := call(t, b.apiRequestCreate, dave, "POST", "/access-requests",
		`{"tile":"apps/email","level":"write","note":"need to drive the mailer"}`, nil)
	if w.Code != 200 {
		t.Fatalf("request create: %d %s", w.Code, w.Body.String())
	}
	// Requesting what you already have refuses.
	w = call(t, b.apiRequestCreate, carol, "POST", "/access-requests",
		`{"tile":"apps/email","level":"read"}`, nil)
	if w.Code != 400 {
		t.Fatalf("already-satisfied request: %d %s", w.Code, w.Body.String())
	}
	// Unknown tiles 404.
	w = call(t, b.apiRequestCreate, dave, "POST", "/access-requests",
		`{"tile":"apps/ghost","level":"read"}`, nil)
	if w.Code != 404 {
		t.Fatalf("unknown tile: %d", w.Code)
	}

	list := func(who string) []map[string]any {
		t.Helper()
		var view struct {
			Requests []map[string]any `json:"requests"`
		}
		w := call(t, b.apiRequestsList, principalFor(t, st, who), "GET", "/access-requests", "", nil)
		if w.Code != 200 {
			t.Fatalf("%s list: %d %s", who, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		return view.Requests
	}
	// carol (org admin of the owning org) sees it with manage=true.
	rows := list("carol")
	if len(rows) != 1 || rows[0]["manage"] != true {
		t.Fatalf("org admin must see+manage the request: %v", rows)
	}
	// alice (plain member, no manage right) sees nothing.
	if rows := list("alice"); len(rows) != 0 {
		t.Fatalf("non-manager must not see others' requests: %v", rows)
	}
	// dave sees his own, marked mine.
	rows = list("dave")
	if len(rows) != 1 || rows[0]["mine"] != true {
		t.Fatalf("requester must see their own: %v", rows)
	}

	// alice cannot approve; carol can (level override to read).
	w = call(t, b.apiRequestApprove, alice, "POST", "/access-requests/approve",
		`{"user":"dave","tile":"apps/email"}`, nil)
	if w.Code != 403 {
		t.Fatalf("non-manager approve: %d", w.Code)
	}
	w = call(t, b.apiRequestApprove, carol, "POST", "/access-requests/approve",
		`{"user":"dave","tile":"apps/email","level":"read"}`, nil)
	if w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	// The exact entry landed and the queue drained.
	if got := principalFor(t, st, "dave"); !got.CanReadTile("apps/email") || got.CanWriteTile("apps/email") {
		t.Fatal("approve must write the exact entry at the approved level")
	}
	if rows := list("carol"); len(rows) != 0 {
		t.Fatalf("approved request must leave the queue: %v", rows)
	}

	// Withdraw: file + self-delete.
	w = call(t, b.apiRequestCreate, dave, "POST", "/access-requests",
		`{"tile":"apps/email","level":"terminal"}`, nil)
	if w.Code != 200 {
		t.Fatalf("re-request: %d %s", w.Code, w.Body.String())
	}
	w = call(t, b.apiRequestDelete, dave, "DELETE", "/access-requests",
		`{"tile":"apps/email"}`, nil)
	if w.Code != 200 {
		t.Fatalf("withdraw: %d %s", w.Code, w.Body.String())
	}
	// Elements can't file.
	el := principalFor(t, st, "dave")
	el.Component = "apps/email"
	w = call(t, b.apiRequestCreate, el, "POST", "/access-requests",
		`{"tile":"apps/calendar","level":"read"}`, nil)
	if w.Code != 403 {
		t.Fatalf("element request must refuse: %d", w.Code)
	}
}
