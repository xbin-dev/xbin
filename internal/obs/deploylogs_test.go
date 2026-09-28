package obs

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// writeDepLog writes deployment dep (not main) of comp's backend log where
// the runner keeps it.
func writeDepLog(t *testing.T, o *Plane, comp, dep, content string) string {
	t.Helper()
	dir := filepath.Join(o.Root, ".xbin", "deploy", util.TileKey(comp), "d", dep)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "backend.log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// logPeople is a users store with a terminal-level, a write-level and a
// read-level user on apps/calendar, as principals.
func logPeople(t *testing.T) (term, writer, reader auth.Principal) {
	t.Helper()
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	person := func(id, level string) auth.Principal {
		t.Helper()
		u, err := st.Upsert(users.User{ID: id, Role: users.RoleUser, Tiles: map[string]string{"apps/calendar": level}}, "password")
		if err != nil {
			t.Fatal(err)
		}
		a, _ := st.Access(id)
		return auth.Principal{UserID: id, User: u, Access: a, Via: "session"}
	}
	return person("tom", users.LevelTerminal), person("wes", users.LevelWrite), person("rae", users.LevelRead)
}

// covers D127h T7 — /logs?deployment= reads that deployment's log: main's
// from today's file whether or not it is the primary, another's from
// .xbin/deploy/<TileKey>/d/<name>/backend.log, followed too; the default is
// the caller's bound deployment (a frame or instance principal's, a session's
// named target), else the primary; a non-primary answer, and every answer
// to a request that named a deployment, carries X-XBin-Deployment, and a
// default answer for the primary carries none; an unknown deployment is
// 404, a malformed name 400; the gate is today's (admins, the tile's own
// principals, terminal level); a deployment directory swapped for a symlink
// is never followed out.
func TestLogsPerDeployment(t *testing.T) {
	o := newDepRig(t)
	term, writer, _ := logPeople(t)
	writeLog(t, o.Plane, "apps/calendar", "main log\n")
	writeDepLog(t, o.Plane, "apps/calendar", "dev", "dev log\n")
	writeLog(t, o.Plane, "apps/email", "email main log\n")
	writeDepLog(t, o.Plane, "apps/email", "dev", "email dev log\n")

	for _, c := range []struct {
		what   string
		p      auth.Principal
		query  string
		body   string
		header string
	}{
		{"the owner, no name", ownerP, "component=apps/calendar", "main log\n", ""},
		{"the owner, dev", ownerP, "component=apps/calendar&deployment=dev", "dev log\n", "dev"},
		{"the owner, main by name", ownerP, "component=apps/calendar&deployment=main", "main log\n", "main"},
		{"a terminal-level person, dev", term, "component=apps/calendar&deployment=dev", "dev log\n", "dev"},
		{"dev's backend, no name", calDev, "component=apps/calendar", "dev log\n", "dev"},
		{"dev's backend, its own name", calDev, "component=apps/calendar&deployment=dev", "dev log\n", "dev"},
		{"main's backend, no name", calMain, "component=apps/calendar", "main log\n", ""},
		{"a session targeting dev, no name", auth.Principal{Component: "apps/calendar", Via: "terminal", Deployment: "dev"},
			"component=apps/calendar", "dev log\n", "dev"},
		{"a session targeting dev, main by name", auth.Principal{Component: "apps/calendar", Via: "terminal", Deployment: "dev"},
			"component=apps/calendar&deployment=main", "main log\n", "main"},
		{"a session following the primary", calTerm, "component=apps/calendar", "main log\n", ""},
		// apps/email's primary is dev: the default is dev's log, main's is named
		{"the owner, a dev primary", ownerP, "component=apps/email", "email dev log\n", ""},
		{"the owner, main beside it", ownerP, "component=apps/email&deployment=main", "email main log\n", "main"},
		{"main's backend beside a dev primary", emailMn, "component=apps/email", "email main log\n", "main"},
	} {
		w := getLogs(t, o.Plane, c.p, c.query)
		if w.Code != 200 || w.Body.String() != c.body || w.Header().Get("X-XBin-Deployment") != c.header {
			t.Errorf("%s: %d %q echo %q, want %q echo %q", c.what, w.Code, w.Body, w.Header().Get("X-XBin-Deployment"), c.body, c.header)
		}
		if _, set := w.Header()["X-Xbin-Deployment"]; set != (c.header != "") {
			t.Errorf("%s: echo header present %v", c.what, set)
		}
	}

	for _, c := range []struct {
		what  string
		p     auth.Principal
		query string
		code  int
		text  string
	}{
		{"an unknown deployment", ownerP, "component=apps/calendar&deployment=nope", 404, `apps/calendar has no deployment \"nope\"`},
		{"a malformed name", ownerP, "component=apps/calendar&deployment=Dev!", 400, "deployment names are"},
		{"a writer, dev", writer, "component=apps/calendar&deployment=dev", 403, "terminal-level"},
		{"a gone deployment's backend", calGone, "component=apps/calendar", 404, `no deployment \"gone\"`},
		{"dev with no log yet", ownerP, "component=apps/calendar&deployment=evil", 404, "no logs yet"},
		// D127j: a query never names a deployment as tile+name
		{"a qualified component", ownerP, "component=apps/calendar%2Bdev", 400, "a deployment is named with deployment=, not tile+name"},
		{"an unescaped qualified component", ownerP, "component=apps/calendar+dev", 400, "a deployment is named with deployment=, not tile+name"},
	} {
		w := getLogs(t, o.Plane, c.p, c.query)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.text) || w.Header().Get("X-XBin-Deployment") != "" {
			t.Errorf("%s: %d %s, want %d %q and no echo", c.what, w.Code, w.Body, c.code, c.text)
		}
	}

	// a deployment's directory swapped for a symlink out of the deploy state
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "backend.log"), []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(o.Root, ".xbin", "deploy", util.TileKey("apps/calendar"), "d", "evil")); err != nil {
		t.Fatal(err)
	}
	if w := getLogs(t, o.Plane, ownerP, "component=apps/calendar&deployment=evil"); w.Code != 404 || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("a symlinked deployment directory: %d %q", w.Code, w.Body)
	}

	// follow streams dev's log as it grows
	old := logPoll
	logPoll = 10 * time.Millisecond
	defer func() { logPoll = old }()
	p := writeDepLog(t, o.Plane, "apps/calendar", "dev", "initial\n")
	r := httptest.NewRequest("GET", "/logs?component=apps/calendar&deployment=dev&follow=1", nil)
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(auth.WithPrincipal(ctx, ownerP))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { o.apiLogs(w, r); close(done) }()
	time.Sleep(40 * time.Millisecond)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("appended\n")
	f.Close()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("follow did not return after the client went away")
	}
	if !strings.Contains(w.Body.String(), "initial") || !strings.Contains(w.Body.String(), "appended") || w.Header().Get("X-XBin-Deployment") != "dev" {
		t.Fatalf("follow of dev's log: %q echo %q", w.Body, w.Header().Get("X-XBin-Deployment"))
	}
}

// covers D127h T7 — a non-primary log's audience (08-data §14): the primary's
// frame principal, minted for the tile's readers, can't read dev's log, nor
// can dev's frame read main's; another tile's principal and a person below
// terminal level read neither. (The /tile-status half of 08-data's row is
// internal/boot/api.go's.)
func TestNonPrimaryLogsAudience(t *testing.T) {
	o := newDepRig(t)
	_, writer, reader := logPeople(t)
	writeLog(t, o.Plane, "apps/calendar", "main log\n")
	writeDepLog(t, o.Plane, "apps/calendar", "dev", "dev log\n")
	mainFrame := auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "rae"}
	devFrame := auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "wes", Deployment: "dev"}
	emailFrame := auth.Principal{Component: "apps/email", Via: "frame", Deployment: "dev"}
	for _, c := range []struct {
		what  string
		p     auth.Principal
		query string
		code  int
	}{
		{"the primary's frame, dev", mainFrame, "component=apps/calendar&deployment=dev", 403},
		{"dev's frame, main", devFrame, "component=apps/calendar&deployment=main", 403},
		{"dev's backend, main", calDev, "component=apps/calendar&deployment=main", 403},
		{"another tile's frame, dev", emailFrame, "component=apps/calendar&deployment=dev", 403},
		{"a writer, dev", writer, "component=apps/calendar&deployment=dev", 403},
		{"a reader, dev", reader, "component=apps/calendar&deployment=dev", 403},
		{"a reader, the primary", reader, "component=apps/calendar", 403},
	} {
		w := getLogs(t, o.Plane, c.p, c.query)
		if w.Code != c.code || strings.Contains(w.Body.String(), " log\n") {
			t.Errorf("%s: %d %q, want %d", c.what, w.Code, w.Body, c.code)
		}
	}
	// each frame reads its own deployment's
	if w := getLogs(t, o.Plane, mainFrame, "component=apps/calendar"); w.Body.String() != "main log\n" {
		t.Errorf("the primary's frame, its own: %d %q", w.Code, w.Body)
	}
	if w := getLogs(t, o.Plane, devFrame, "component=apps/calendar"); w.Body.String() != "dev log\n" {
		t.Errorf("dev's frame, its own: %d %q", w.Code, w.Body)
	}
}
