package obs

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// prefsCall calls a prefs handler as p; key is the {key} path value.
func prefsCall(t *testing.T, o *depRig, p auth.Principal, method, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/prefs"
	if key != "" {
		path += "/" + key
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.SetPathValue("key", key)
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	switch {
	case method == "PUT":
		o.apiPrefsPut(w, r)
	case method == "DELETE":
		o.apiPrefsDelete(w, r)
	case key == "":
		o.apiPrefsAll(w, r)
	default:
		o.apiPrefsGet(w, r)
	}
	return w
}

// covers P6 P17 — prefs per deployment (05-model §9, 08-data §2): a tile's
// frame principal bound to dev keeps its prefs in
// data/prefs/<CompKey(user)>/.deployments/<TileKey>/dev.json, apart from
// main's, which stay in today's file whether or not main is the primary; a
// session keeps the prefs of its target, following the primary by name, so
// a reassignment doesn't move them; a person's own bucket is unchanged; a
// credential whose deployment is gone gets 404, a session following a
// protected primary 403.
func TestPrefsPerDeployment(t *testing.T) {
	o := newDepRig(t)
	mainFrame := auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "ana"}
	devFrame := auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "ana", Deployment: "dev"}
	person := auth.Principal{UserID: "ana", Via: "session"}
	mainFile := filepath.Join(o.Root, "data", "prefs", util.CompKey("ana"), util.CompKey("apps/calendar")+".json")
	devFile := filepath.Join(o.Root, "data", "prefs", util.CompKey("ana"), ".deployments", util.TileKey("apps/calendar"), "dev.json")
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return "-"
		}
		return strings.Join(strings.Fields(string(b)), "")
	}

	for _, c := range []struct {
		p    auth.Principal
		body string
	}{{mainFrame, `"m"`}, {devFrame, `"d"`}, {person, `"shell"`}} {
		if w := prefsCall(t, o, c.p, "PUT", "layout", c.body); w.Code != 200 {
			t.Fatalf("PUT as %+v: %d %s", c.p, w.Code, w.Body)
		}
	}
	if got := read(mainFile); got != `{"layout":"m"}` {
		t.Errorf("main's file: %s", got)
	}
	if got := read(devFile); got != `{"layout":"d"}` {
		t.Errorf("dev's file: %s", got)
	}
	if got := read(filepath.Join(o.Root, "data", "prefs", util.CompKey("ana"), util.CompKey("root")+".json")); got != `{"layout":"shell"}` {
		t.Errorf("the person's own bucket: %s", got)
	}
	for _, c := range []struct {
		what string
		p    auth.Principal
		want string
	}{
		{"main's frame", mainFrame, `"m"`},
		{"dev's frame", devFrame, `"d"`},
		{"a session following the primary (main)", auth.Principal{Component: "apps/calendar", Via: "terminal", UserID: "ana"}, `"m"`},
		{"a session targeting dev", auth.Principal{Component: "apps/calendar", Via: "terminal", UserID: "ana", Deployment: "dev"}, `"d"`},
	} {
		if w := prefsCall(t, o, c.p, "GET", "layout", ""); w.Code != 200 || w.Body.String() != c.want {
			t.Errorf("%s: %d %s, want %s", c.what, w.Code, w.Body, c.want)
		}
	}

	// the primary moves to dev: main's frame keeps main's, a following session now reads dev's
	o.setPrimary("apps/calendar", "dev")
	if w := prefsCall(t, o, mainFrame, "GET", "layout", ""); w.Body.String() != `"m"` {
		t.Errorf("main's frame beside a dev primary: %s", w.Body)
	}
	follow := auth.Principal{Component: "apps/calendar", Via: "terminal", UserID: "ana"}
	if w := prefsCall(t, o, follow, "GET", "layout", ""); w.Body.String() != `"d"` {
		t.Errorf("a session following a dev primary: %s", w.Body)
	}

	// deleting in dev leaves main's
	if w := prefsCall(t, o, devFrame, "DELETE", "layout", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if read(devFile) != `{}` || read(mainFile) != `{"layout":"m"}` {
		t.Errorf("after dev's delete: dev %s, main %s", read(devFile), read(mainFile))
	}

	// gone and refused
	gone := auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "ana", Deployment: "gone"}
	if w := prefsCall(t, o, gone, "GET", "", ""); w.Code != 404 {
		t.Errorf("a gone deployment's frame: %d %s", w.Code, w.Body)
	}
	o.mu.Lock()
	o.protected["apps/calendar"] = true
	o.mu.Unlock()
	if w := prefsCall(t, o, follow, "PUT", "layout", `"x"`); w.Code != 403 {
		t.Errorf("a session following a protected primary: %d %s", w.Code, w.Body)
	}
}
