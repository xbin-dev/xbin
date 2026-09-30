package xbin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mailGateway serves the mail routes as answer says, recording each call.
func mailGateway(t *testing.T, answer func(method, path string) (int, string)) func() []string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		mu.Unlock()
		code, body := answer(r.Method, r.URL.Path)
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok")
	clientOnce = sync.Once{}
	t.Cleanup(func() { clientOnce = sync.Once{} })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// Mail, Inbox and Ack speak the partition mail routes: their request shapes,
// the answers they read, and an older xbind (404) named as the missing
// feature.
func TestMailHelpers(t *testing.T) {
	calls := mailGateway(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/api/xbin/partitions/mail":
			return 200, `{"ok":true,"id":"0123456789abcdef01234567"}`
		case method == "GET":
			return 200, `{"items":[{"id":"0123456789abcdef01234567","from":"global","topic":"dm","data":{"a":1},` +
				`"at":"2026-09-30T10:00:00Z","expires":"2026-10-07T10:00:00Z"}],"more":false}`
		}
		return 200, `{"ok":true}`
	})
	id, err := MailWith("user:alice", "dm", map[string]int{"a": 1}, MailOptions{TTL: 90 * time.Minute, Source: "apps/hooks#gh"})
	if err != nil || id != "0123456789abcdef01234567" {
		t.Fatalf("MailWith: %q %v", id, err)
	}
	if _, err := Mail("global", "up", nil); err != nil {
		t.Fatal(err)
	}
	items, err := Inbox("abc", 10)
	if err != nil || len(items) != 1 || items[0].From != "global" || string(items[0].Data) != `{"a":1}` ||
		!items[0].At.Equal(time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("Inbox: %+v %v", items, err)
	}
	if err := Ack(items[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := Ack(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /api/xbin/partitions/mail {"data":{"a":1},"source":"apps/hooks#gh","to":"user:alice","topic":"dm","ttl":5400}`,
		`POST /api/xbin/partitions/mail {"data":null,"to":"global","topic":"up"}`,
		`GET /api/xbin/partitions/mail?after=abc&limit=10 `,
		`POST /api/xbin/partitions/mail/ack {"ids":["0123456789abcdef01234567"]}`,
	}
	if got := calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var bell MailBell
	if err := json.Unmarshal([]byte(`{"partition":"user:alice","pending":3}`), &bell); err != nil || bell.Pending != 3 || bell.Partition != "user:alice" {
		t.Errorf("MailBell: %+v %v", bell, err)
	}
}

// InboxPage carries the answer's more: a page cut short by xbind's byte cap
// says more wait, so a handler doesn't stop at a short page.
func TestMailInboxPage(t *testing.T) {
	calls := mailGateway(t, func(method, path string) (int, string) {
		return 200, `{"items":[{"id":"0123456789abcdef01234567","from":"user:alice","topic":"big","data":null,` +
			`"at":"2026-09-30T10:00:00Z","expires":"2026-10-07T10:00:00Z"}],"more":true}`
	})
	pg, err := InboxPage("", 50)
	if err != nil || len(pg.Items) != 1 || !pg.More || pg.Items[0].From != "user:alice" {
		t.Fatalf("InboxPage: %+v %v", pg, err)
	}
	if got := calls(); len(got) != 1 || got[0] != "GET /api/xbin/partitions/mail?limit=50 " {
		t.Errorf("calls %q", got)
	}
	mailGateway(t, func(string, string) (int, string) { return 404, "404 page not found" })
	if _, err := InboxPage("", 0); err == nil || !strings.Contains(err.Error(), "partition-mail/1") {
		t.Errorf("InboxPage on an older xbind: %v", err)
	}
}

func TestMailOlderXbind(t *testing.T) {
	mailGateway(t, func(method, path string) (int, string) {
		if method == "POST" && path == "/api/xbin/partitions/mail" {
			return 404, `{"error":"no such person here: user:zed"}`
		}
		return 404, "404 page not found"
	})
	if _, err := Inbox("", 0); err == nil || !strings.Contains(err.Error(), "partition-mail/1") {
		t.Errorf("Inbox on an older xbind: %v", err)
	}
	if err := Ack("x"); err == nil || !strings.Contains(err.Error(), "partition-mail/1") {
		t.Errorf("Ack on an older xbind: %v", err)
	}
	if _, err := Mail("user:zed", "t", nil); err == nil || !strings.Contains(err.Error(), "no such person here") || strings.Contains(err.Error(), "partition-mail/1") {
		t.Errorf("Mail to nobody: %v", err)
	}
}

// The Context variants send what the plain calls send.
func TestMailContextShapes(t *testing.T) {
	calls := mailGateway(t, func(method, path string) (int, string) {
		switch {
		case method == "POST" && path == "/api/xbin/partitions/mail":
			return 200, `{"ok":true,"id":"0123456789abcdef01234567"}`
		case method == "GET":
			return 200, `{"items":[{"id":"0123456789abcdef01234567","from":"user:alice","topic":"up","data":1,` +
				`"at":"2026-09-30T10:00:00Z","expires":"2026-10-07T10:00:00Z"}],"more":true}`
		}
		return 200, `{"ok":true}`
	})
	ctx := context.Background()
	if id, err := MailContext(ctx, "global", "up", 1); err != nil || id != "0123456789abcdef01234567" {
		t.Fatalf("MailContext: %q %v", id, err)
	}
	if _, err := MailWithContext(ctx, "user:alice", "dm", nil, MailOptions{TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	pg, err := InboxPageContext(ctx, "abc", 5)
	if err != nil || len(pg.Items) != 1 || !pg.More || pg.Items[0].From != "user:alice" {
		t.Fatalf("InboxPageContext: %+v %v", pg, err)
	}
	if items, err := InboxContext(ctx, "", 0); err != nil || len(items) != 1 {
		t.Fatalf("InboxContext: %+v %v", items, err)
	}
	if err := AckContext(ctx, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := AckContext(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST /api/xbin/partitions/mail {"data":1,"to":"global","topic":"up"}`,
		`POST /api/xbin/partitions/mail {"data":null,"to":"user:alice","topic":"dm","ttl":3600}`,
		`GET /api/xbin/partitions/mail?after=abc&limit=5 `,
		`GET /api/xbin/partitions/mail `,
		`POST /api/xbin/partitions/mail/ack {"ids":["a","b"]}`,
	}
	if got := calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// stalledGateway is a hung xbind until the test ends: it answers nothing
// (body false), or a 200 whose body never ends (body true).
func stalledGateway(t *testing.T, body bool) {
	t.Helper()
	release := make(chan struct{})
	mailGateway(t, func(string, string) (int, string) { <-release; return 200, `{}` })
	if body {
		sock := filepath.Join(t.TempDir(), "stall.sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"items":[`))
			_ = http.NewResponseController(w).Flush()
			<-release
		})}
		go srv.Serve(ln)
		t.Cleanup(func() { srv.Close() })
		t.Setenv("XBIN_GATEWAY", sock)
	}
	t.Cleanup(func() { close(release) }) // first: the servers close after (cleanups run last-first)
}

// A hung xbind: each Context variant gives up when its ctx ends, with an
// error wrapping ctx's — whether xbind never answers or stalls in its
// answer's body. (An ack answered 200 is done, whatever its body.)
func TestMailContextHungXbind(t *testing.T) {
	calls := []struct {
		name string
		call func(ctx context.Context) error
		body bool // it reads a 200's body
	}{
		{"MailContext", func(ctx context.Context) error { _, err := MailContext(ctx, "global", "t", nil); return err }, true},
		{"MailWithContext", func(ctx context.Context) error {
			_, err := MailWithContext(ctx, "global", "t", nil, MailOptions{TTL: time.Minute})
			return err
		}, true},
		{"InboxPageContext", func(ctx context.Context) error { _, err := InboxPageContext(ctx, "", 0); return err }, true},
		{"InboxContext", func(ctx context.Context) error { _, err := InboxContext(ctx, "", 0); return err }, true},
		{"AckContext", func(ctx context.Context) error { return AckContext(ctx, "x") }, false},
	}
	for _, stall := range []string{"no-answer", "stalled-body"} {
		t.Run(stall, func(t *testing.T) {
			stalledGateway(t, stall == "stalled-body")
			for _, c := range calls {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				start := time.Now()
				err := c.call(ctx)
				cancel()
				if took := time.Since(start); took > 5*time.Second {
					t.Errorf("%s took %v", c.name, took)
				}
				if stall == "stalled-body" && !c.body {
					if err != nil {
						t.Errorf("%s answered 200: %v", c.name, err)
					}
					continue
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("%s: %v, want an error wrapping the deadline", c.name, err)
				}
			}
		})
	}
}
