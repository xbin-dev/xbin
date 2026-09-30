package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A device code is the person's who asked for it: the page and code go to
// them (the 202, GET /runs/{id}/harness, asking again) and nowhere else —
// not stored, not in anyone's run, view, tree or Needs, where anyone who
// sees the conversation (a viewer, a participant the sandbox refuses)
// could enter it first and sign the sandbox's harness in as themselves.
// Everyone sees who started it.
func TestHarnessDeviceCodeIsTheRequesters(t *testing.T) {
	ag, mux, _ := harnessFixture(t, false, "--require-login", "--device-ms=1500")
	shared := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "shared", Egress: "internet", Members: []string{"bob"}})
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "echo shared", "class": "coding",
		"harness": map[string]any{"provider": "fake"}, "sandbox": map[string]any{"ref": sandboxRef("apps/cs", shared.ID)}})
	if w.Code != 200 {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	var run Run
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	parkOf(t, ag, run.ID, "login")
	for _, m := range []map[string]string{{"user": "bob", "role": "participant"}, {"user": "carol", "role": "participant"},
		{"user": "dave", "role": "viewer"}} {
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", run.ID), m); w.Code != 200 {
			t.Fatalf("members: %d %s", w.Code, w.Body)
		}
	}
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	w = callAs(t, mux, asAlice, "POST", path, map[string]any{"method": "fake-device", "confirm": true})
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"url":"https://example.invalid/device"`) || !strings.Contains(w.Body.String(), "FAKE-1234") {
		t.Fatalf("alice's device code: %d %s", w.Code, w.Body)
	}
	leaks := func(s string) bool {
		return strings.Contains(s, "FAKE-1234") || strings.Contains(s, "example.invalid/device")
	}
	// stored nowhere: no table's cell (the client's snapshot, which keeps
	// the accepted url question for a successor, included)
	for _, tbl := range scanStrings(t, ag.db, `SELECT name FROM sqlite_master WHERE type='table'`) {
		rows, err := ag.db.sql.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if s := fmt.Sprintf("%s", v); leaks(s) {
					rows.Close()
					t.Fatalf("the code is stored (%s.%s): %s", tbl, cols[i], clip(s, 300))
				}
			}
		}
		rows.Close()
	}
	// nobody else reads it; everyone reads who started it
	for _, c := range []caller{asBob, asCarol, asDave, asAlice} {
		for _, p := range []string{fmt.Sprintf("/runs/%d", run.ID), fmt.Sprintf("/runs/%d/view", run.ID), fmt.Sprintf("/runs/%d/tree", run.ID), "/needs"} {
			w := callAs(t, mux, c, "GET", p, nil)
			if w.Code != 200 || leaks(w.Body.String()) {
				t.Fatalf("%s reads %s: %d %s", c.user, p, w.Code, w.Body)
			}
		}
		if c == asAlice {
			continue
		}
		w := callAs(t, mux, c, "GET", fmt.Sprintf("/runs/%d/harness", run.ID), nil)
		var g struct {
			Harness struct {
				Login struct{ Device map[string]any }
			}
		}
		if w.Code != 200 || leaks(w.Body.String()) || json.Unmarshal(w.Body.Bytes(), &g) != nil || jsonOf(g.Harness.Login.Device) != `{"by":"alice"}` {
			t.Fatalf("%s's GET …/harness: %d %s", c.user, w.Code, w.Body)
		}
	}
	// the requester reads it again, and gets it again by asking again
	w = callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/harness", run.ID), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"device":{"url":"https://example.invalid/device","message":"Enter code FAKE-1234`) ||
		!strings.Contains(w.Body.String(), `"by":"alice"`) {
		t.Fatalf("alice's GET …/harness: %d %s", w.Code, w.Body)
	}
	w = callAs(t, mux, asAlice, "POST", path, map[string]any{"method": "fake-device", "confirm": true})
	if w.Code != 202 || !strings.Contains(w.Body.String(), "FAKE-1234") {
		t.Fatalf("alice asking again: %d %s", w.Code, w.Body)
	}
	// bob may use the sandbox: his ask meets the sign-in under way
	if w := callAs(t, mux, asBob, "POST", path, map[string]any{"method": "fake-device", "confirm": true}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "already under way") || leaks(w.Body.String()) {
		t.Fatalf("bob's device code: %d %s", w.Code, w.Body)
	}
	hwait(t, "the device sign-in", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo shared")
	})
	if w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/harness", run.ID), nil); leaks(w.Body.String()) {
		t.Fatalf("after the sign-in: %s", w.Body)
	}
}
