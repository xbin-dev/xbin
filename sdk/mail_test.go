package xbin

import (
	"encoding/json"
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
