package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers PO-14 — GET /ws/term/env only ever gains fields: its answer keeps
// today's keys with today's JSON types ({exists, baseOutdated} booleans, vm
// an object with a boolean available), so a newer xbind may add keys but
// never drop, rename or retype one; and its gate is unchanged: terminal
// level on the tile (admins: any), the root layer admin-only, and no element
// principal, the tile's own terminal and frame tokens included, ever passes
// (internal/server/server.go termEnvGate).
func TestTermEnvAdditive(t *testing.T) {
	h, s := termServer(t) // alice and dave admins, bob a user without tiles
	for _, u := range []users.User{
		{ID: "tess", Tiles: map[string]string{"apps/x": users.LevelTerminal}},
		{ID: "walt", Tiles: map[string]string{"apps/x": users.LevelWrite}},
	} {
		if _, err := s.Auth.Users.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	sid := map[string]string{}
	for _, id := range []string{"alice", "bob", "tess", "walt"} {
		sid[id] = s.Auth.NewSession(id, "")
	}
	get := func(r *http.Request) (int, map[string]any) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body
	}
	asUser := func(id, cwd string) (int, map[string]any) {
		return get(withCookie("GET", "/ws/term/env?cwd="+cwd, "", sid[id]))
	}
	asBearer := func(tok, hdr, cwd string) (int, map[string]any) {
		r := withCookie("GET", "/ws/term/env?cwd="+cwd, "", "")
		r.Header.Set(hdr, tok)
		return get(r)
	}
	// today's keys and their types; any further key is allowed
	shape := func(who string, body map[string]any) {
		t.Helper()
		for _, k := range []string{"exists", "baseOutdated"} {
			if _, ok := body[k].(bool); !ok {
				t.Errorf("%s: %q is %T (%v), want a boolean", who, k, body[k], body[k])
			}
		}
		vm, ok := body["vm"].(map[string]any)
		if !ok {
			t.Fatalf("%s: vm is %T (%v), want an object", who, body["vm"], body["vm"])
		}
		if _, ok := vm["available"].(bool); !ok {
			t.Errorf("%s: vm.available is %T, want a boolean", who, vm["available"])
		}
		if r, ok := vm["reason"]; ok {
			if _, isStr := r.(string); !isStr {
				t.Errorf("%s: vm.reason is %T, want a string", who, r)
			}
		}
	}
	for _, who := range []string{"tess", "alice"} {
		code, body := asUser(who, "apps/x")
		if code != http.StatusOK {
			t.Fatalf("%s on apps/x: %d, want 200", who, code)
		}
		shape(who, body)
	}
	if code, body := asUser("alice", ""); code != http.StatusOK {
		t.Fatalf("the root layer as an admin: %d", code)
	} else {
		shape("alice, root layer", body)
	}
	for _, tc := range []struct {
		name string
		code int
	}{
		{"walt", func() int { c, _ := asUser("walt", "apps/x"); return c }()}, // write is not terminal level
		{"bob", func() int { c, _ := asUser("bob", "apps/x"); return c }()},
		{"tess, the root layer", func() int { c, _ := asUser("tess", ""); return c }()},
		{"tess, another tile", func() int { c, _ := asUser("tess", "apps/y"); return c }()},
		{"apps/x's terminal token", func() int {
			c, _ := asBearer("Bearer "+s.Auth.MintTerminal("apps/x", "tess"), "Authorization", "apps/x")
			return c
		}()},
		{"apps/x's owner-driven terminal token", func() int {
			c, _ := asBearer("Bearer "+s.Auth.MintTerminal("apps/x", ""), "Authorization", "apps/x")
			return c
		}()},
		{"apps/x's frame token", func() int {
			c, _ := asBearer(s.Auth.MintFrameToken("apps/x", "tess", time.Minute), auth.FrameTokenHeader, "apps/x")
			return c
		}()},
	} {
		if tc.code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", tc.name, tc.code)
		}
	}
}
