package broker

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/users"
	"github.com/magik6k/xbin/internal/util"
)

func writeLog(t *testing.T, b *Broker, comp, content string) string {
	t.Helper()
	dir := filepath.Join(b.Reg.Root, ".xbin", "log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, util.CompKey(comp)+".log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func getLogs(t *testing.T, b *Broker, p auth.Principal, query string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/logs?"+query, nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	b.apiLogs(w, r)
	return w
}

func TestLogsGateAndTail(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	writeLog(t, b, "apps/calendar", "line one\nline two\nline three\n")

	// Unknown component → 404 before any gate.
	if w := getLogs(t, b, auth.Principal{Owner: true}, "component=apps/nope"); w.Code != 404 {
		t.Fatalf("unknown component: want 404, got %d", w.Code)
	}

	// Admin, self, and terminal-level users read; read/write users and
	// ungranted elements don't.
	if _, err := st.Upsert(users.User{ID: "termu", Role: users.RoleUser,
		Tiles: map[string]string{"apps/calendar": users.LevelTerminal}}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "writeu", Role: users.RoleUser,
		Tiles: map[string]string{"apps/calendar": users.LevelWrite}}, "password"); err != nil {
		t.Fatal(err)
	}
	acc := func(id string) *users.Access { a, _ := st.Access(id); return a }

	allow := []auth.Principal{
		{Owner: true},
		{Component: "apps/calendar"}, // the tile's own backend → self-logs
		{UserID: "termu", Access: acc("termu")},
	}
	for i, p := range allow {
		w := getLogs(t, b, p, "component=apps/calendar")
		if w.Code != 200 || !strings.Contains(w.Body.String(), "line three") {
			t.Fatalf("allow[%d]: %d %q", i, w.Code, w.Body.String())
		}
	}
	deny := []auth.Principal{
		{UserID: "writeu", Access: acc("writeu")}, // write < terminal
		{Component: "apps/email"},                 // another tile
		{},                                        // unauthenticated
	}
	for i, p := range deny {
		if w := getLogs(t, b, p, "component=apps/calendar"); w.Code != 403 {
			t.Fatalf("deny[%d]: want 403, got %d", i, w.Code)
		}
	}

	// tail= caps the returned bytes to the last N (partial line included).
	writeLog(t, b, "apps/calendar", "AAAA\nBBBB\nCCCC\n")
	w := getLogs(t, b, auth.Principal{Owner: true}, "component=apps/calendar&tail=6")
	if b := w.Body.String(); len(b) > 6 || !strings.Contains(b, "CCCC") {
		t.Fatalf("tail=6 returned %q", b)
	}

	// No log file yet, non-follow → 404.
	if w := getLogs(t, b, auth.Principal{Owner: true}, "component=apps/email"); w.Code != 404 {
		t.Fatalf("missing log non-follow: want 404, got %d", w.Code)
	}
}

func TestLogsFollow(t *testing.T) {
	old := logPoll
	logPoll = 10 * time.Millisecond
	defer func() { logPoll = old }()

	b := testBroker(t)
	p := writeLog(t, b, "apps/calendar", "initial\n")

	r := httptest.NewRequest("GET", "/logs?component=apps/calendar&follow=1", nil)
	ctx, cancel := context.WithCancel(r.Context())
	r = r.WithContext(auth.WithPrincipal(ctx, auth.Principal{Owner: true}))
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { b.apiLogs(w, r); close(done) }()

	// Append after the handler has streamed the initial tail.
	time.Sleep(40 * time.Millisecond)
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("appended-line\n")
	f.Close()
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("follow handler did not return after context cancel")
	}
	body := w.Body.String()
	if !strings.Contains(body, "initial") || !strings.Contains(body, "appended-line") {
		t.Fatalf("follow body missing content: %q", body)
	}
}
