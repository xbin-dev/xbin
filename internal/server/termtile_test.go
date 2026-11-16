package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// A terminal layer's status and reset (/ws/term/env?cwd=) of a directory
// inside a tile are decided on that tile, as a session there is: its exact
// entries, not the directory's own path, which only patterns match.
func TestTermEnvInsideATileIsTheTiles(t *testing.T) {
	h, s := termServer(t)
	s.Term.TileAt = func(rel string) (string, bool) {
		if rel == "apps/x" || strings.HasPrefix(rel, "apps/x/") {
			return "apps/x", true
		}
		return "", false
	}
	for _, u := range []users.User{
		{ID: "pam", Tiles: map[string]string{"apps/*": users.LevelTerminal, "apps/x": users.LevelNone}},
		{ID: "tess", Tiles: map[string]string{"apps/x": users.LevelTerminal}},
	} {
		if _, err := s.Auth.Users.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	get := func(id, cwd string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie("GET", "/ws/term/env?cwd="+cwd, "", s.Auth.NewSession(id, "")))
		return w.Code
	}
	for cwd, want := range map[string][2]int{"apps/x": {403, 200}, "apps/x/src": {403, 200}, "apps/x/src/deep/": {403, 200}} {
		if got := get("pam", cwd); got != want[0] {
			t.Errorf("pam (a pattern beside an exact none) at %s: %d, want %d", cwd, got, want[0])
		}
		if got := get("tess", cwd); got != want[1] {
			t.Errorf("tess (terminal on the tile) at %s: %d, want %d", cwd, got, want[1])
		}
	}
}
