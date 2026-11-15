package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// tool runs a workspace tool in session s as its model would call it.
func (fx *wsFix) tool(t *testing.T, s *wsSession, name string, args map[string]any) string {
	t.Helper()
	run, err := fx.ag.db.getRun(s.Run)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := fx.ag.db.runConfig(s.Run)
	out, err := fx.ag.runTool(context.Background(), run, cfg, name, args)
	if err != nil {
		return "error: " + err.Error()
	}
	return out
}

// session is a new session of alice's through the route.
func (fx *wsFix) session(t *testing.T, body map[string]any) *wsSession {
	t.Helper()
	var made struct {
		Session struct{ ID int64 } `json:"session"`
	}
	fx.json(t, fx.call(t, wsAlice, "POST", "/ws/sessions", body), 200, &made)
	s, err := fx.ag.db.wsSession(made.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// dropHandle is xbind forgetting s's handle (a day unused, or pushed out).
func (fx *wsFix) dropHandle(s *wsSession) {
	fx.x.mu.Lock()
	defer fx.x.mu.Unlock()
	cur, _ := fx.ag.db.wsSession(s.Run)
	delete(fx.x.handles, cur.Handle)
}

func (fx *wsFix) handleOf(t *testing.T, s *wsSession) *wsHandle {
	t.Helper()
	cur, _ := fx.ag.db.wsSession(s.Run)
	fx.x.mu.Lock()
	defer fx.x.mu.Unlock()
	h := fx.x.handles[cur.Handle]
	if h == nil {
		t.Fatalf("session %d has no live handle (%q)", s.Run, cur.Handle)
	}
	cp := *h
	return &cp
}

// The taint a conversation's handle carries at xbind is the conversation's,
// not the handle's: a handle xbind made again holds every org the
// conversation ever held before anything runs on it — not only the orgs it
// can still reach. Where xbind won't let the model hold them any more, the
// conversation is held: no model call, no tool, and its person is told,
// with the model it was on and a new session as the ways forward.
func TestWsTaintOutlivesTheHandle(t *testing.T) {
	fx := newWsFix(t)
	books := fx.session(t, map[string]any{"name": "Books", "model": "local-llama"})
	fx.tool(t, books, "workspace_map", nil)
	fx.tool(t, books, "workspace_map", nil) // the second read lands the first's taint on record
	cur, _ := fx.ag.db.wsSession(books.Run)
	var col string
	_ = fx.ag.db.q.QueryRow(`SELECT tainted FROM ws_sessions WHERE run_id=?`, books.Run).Scan(&col)
	if !hasStr(cur.Tainted, "finance") || !strings.Contains(col, "finance") {
		t.Fatalf("what the conversation holds isn't on its record: %v (column %q)", cur.Tainted, col)
	}
	// narrowed to Sales, then the handle goes: a map read alone would hold Sales again, not Finance
	fx.json(t, fx.call(t, wsAlice, "PATCH", fmt.Sprintf("/ws/sessions/%d", books.Run), map[string]any{"scope": map[string]any{"orgs": []string{"sales"}, "tiles": []string{}}}), 200, nil)
	fx.dropHandle(books)
	n := len(fx.x.calls)
	fx.tool(t, books, "tile_status", map[string]any{"tile": "apps/orders"})
	if h := fx.handleOf(t, books); !hasStr(h.Tainted, "finance") {
		t.Fatalf("the new handle doesn't hold Finance: %v (calls %v)", h.Tainted, fx.x.calls[n:])
	}
	after := strings.Join(fx.x.calls[n:], " | ")
	if i, j := strings.Index(after, "OpenSession"), strings.Index(after, "Taint "); i < 0 || j < i || strings.Contains(after[i:j], "Map") {
		t.Fatalf("the new handle was used before it held the conversation's taint: %s", after)
	}

	// a handle an older backend made again, without what the conversation held: given the rest before the model may move
	fx.x.mu.Lock()
	for _, h := range fx.x.handles {
		if h.Model == "local-llama" {
			h.Tainted = []string{"sales"}
		}
	}
	fx.x.mu.Unlock()
	w := fx.call(t, wsAlice, "PATCH", fmt.Sprintf("/ws/sessions/%d", books.Run), map[string]any{"model": "big"})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "finance") || !hasStr(fx.handleOf(t, books).Tainted, "finance") {
		t.Fatalf("a move to big with Finance's data, xbind's record short of it: %d %s", w.Code, w.Body)
	}

	// Main on the hosted model has read Sales; then Sales allows private models only, and xbind drops the handle
	main := fx.main(t)
	fx.tool(t, main, "workspace_map", nil)
	fx.tool(t, main, "tile_status", map[string]any{"tile": "apps/orders"})
	_, _ = fx.ag.db.q.Exec(`UPDATE ws_sessions SET conn=json_set(conn, '$.connection.model', json('{"upstream":"up","model":"big-1","private":false}')) WHERE run_id=?`, main.Run)
	fx.x.mu.Lock()
	fx.x.orgs["sales"].PrivateOnly = true
	fx.x.mu.Unlock()
	fx.dropHandle(main)
	if got := fx.tool(t, main, "tile_status", map[string]any{"tile": "apps/calendar"}); !strings.Contains(got, "can't go on on big") || !strings.Contains(got, "sales") {
		t.Fatalf("a tool on a conversation whose model may no longer hold what it holds: %q", got)
	}
	held, _ := fx.ag.db.wsSession(main.Run)
	if held.Held == nil || held.Held.Model != "big" || !hasStr(held.Held.Orgs, "sales") || held.Held.Was != "up/big-1" {
		t.Fatalf("the held session: %+v", held.Held)
	}
	fx.x.mu.Lock()
	for id, h := range fx.x.handles {
		if h.Model == "big" && !hasStr(h.Tainted, "sales") && id != "" {
			t.Errorf("a handle that doesn't hold what the conversation holds is left open: %s %+v", id, h)
		}
	}
	fx.x.mu.Unlock()
	// the person is told, with the ways forward
	var list struct {
		Sessions []struct {
			ID   int64
			Held *struct {
				Model, Reason string
				Orgs          []string
				Offer         struct {
					Model      string
					NewSession struct{ Model string }
				}
			}
		} `json:"sessions"`
	}
	fx.json(t, fx.call(t, wsAlice, "GET", "/ws/sessions", nil), 200, &list)
	var h0 = list.Sessions[0].Held
	if list.Sessions[0].ID != main.Run || h0 == nil || h0.Offer.Model != "up/big-1" || h0.Offer.NewSession.Model != "big" || !strings.Contains(h0.Reason, "Sales allows private models only") {
		t.Fatalf("the held session as the page reads it: %+v", list.Sessions[0])
	}
	// no model call runs on it, and no tool
	f := fakeOf(fx.ag)
	f.on(forRun(main.Run, nil), callTools(tc("x1", "workspace_map", `{}`)))
	before := len(f.callsFor(main.Run))
	fx.say(t, main, "what's overdue?")
	fx.idle(t, main)
	if n := len(f.callsFor(main.Run)); n != before {
		t.Fatalf("%d model calls on a held conversation", n-before)
	}
	// moving it to a model that may hold Sales: a handle on it, holding what the conversation holds; it goes on
	fx.json(t, fx.call(t, wsAlice, "PATCH", fmt.Sprintf("/ws/sessions/%d", main.Run), map[string]any{"model": "local-llama"}), 200, nil)
	moved, _ := fx.ag.db.wsSession(main.Run)
	if moved.Held != nil || moved.Model != "local-llama" || !hasStr(fx.handleOf(t, main).Tainted, "sales") {
		t.Fatalf("after the move: %+v held %+v", moved, moved.Held)
	}
	if cfg, _ := fx.ag.db.runConfig(main.Run); cfg.Pick != "local-llama" {
		t.Fatalf("the conversation's model after the move: %q", cfg.Pick)
	}
	if got := fx.tool(t, main, "tile_status", map[string]any{"tile": "apps/calendar"}); strings.HasPrefix(got, "error:") {
		t.Fatalf("a tool after the move: %q", got)
	}
}

// The column is additive and its migration forward-only: a database from
// the schema before it gains it at the next open, filled from the handle it
// had stored, and opening again changes nothing.
func TestWsTaintedColumnFromPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	d, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	// the previous schema's ws_sessions: no tainted, no held
	for _, q := range []string{`DROP TABLE ws_sessions`, `CREATE TABLE ws_sessions (
		  run_id INTEGER PRIMARY KEY, owner TEXT NOT NULL, main INTEGER NOT NULL DEFAULT 0, handle TEXT NOT NULL DEFAULT '',
		  model TEXT NOT NULL DEFAULT '', scope TEXT NOT NULL DEFAULT '', conn TEXT NOT NULL DEFAULT '', conn_ms INTEGER NOT NULL DEFAULT 0,
		  plan TEXT NOT NULL DEFAULT '', created_ms INTEGER NOT NULL DEFAULT 0, allow TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO ws_sessions (run_id, owner, main, handle, model, conn) VALUES (1, 'alice', 1, 'h-old', 'big', '{"id":"h-old","model":"big","tainted":["finance","sales"]}')`,
		`INSERT INTO ws_sessions (run_id, owner, main, handle, model, conn) VALUES (2, 'alice', 0, 'h-2', 'big', '')`} {
		if _, err := d.q.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.sql.Close()
	for i := 0; i < 2; i++ {
		if d, err = openDB(path); err != nil {
			t.Fatal(err)
		}
		s, err := d.wsSession(1)
		if err != nil || strings.Join(s.Tainted, ",") != "finance,sales" || s.Held != nil {
			t.Fatalf("open %d: the old session's record: %+v %v", i, s, err)
		}
		var col string
		_ = d.q.QueryRow(`SELECT tainted FROM ws_sessions WHERE run_id=1`).Scan(&col)
		if col != `["finance","sales"]` {
			t.Fatalf("open %d: the column: %q", i, col)
		}
		if s2, err := d.wsSession(2); err != nil || len(s2.Tainted) != 0 {
			t.Fatalf("open %d: a session that held nothing: %+v %v", i, s2, err)
		}
		// what is recorded later only grows
		if all, err := d.wsPutHandle(1, wsHandle{ID: "h-new", Model: "big", Tainted: []string{"legal"}}); err != nil || strings.Join(all, ",") != "finance,sales,legal" {
			t.Fatalf("open %d: a new handle's report: %v %v", i, all, err)
		}
		_, _ = d.q.Exec(`UPDATE ws_sessions SET tainted='["finance","sales"]', conn='{"id":"h-old","model":"big","tainted":["finance","sales"]}' WHERE run_id=1`)
		_ = d.sql.Close()
	}
}
