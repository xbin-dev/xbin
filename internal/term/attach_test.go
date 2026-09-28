package term

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/auth"
)

// /ws/term end to end over a real PTY (isolation off: a host shell, which
// is what a workspace without sandboxes runs): the adapter's session frame,
// the echo ack, a reattach replaying the scrollback, and the exit frame
// only when the shell ends. The wire itself is internal/termwire's tests.
func TestWSTermOverAPTY(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", "/bin/sh")
	m := NewManager(root, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	}))
	defer srv.Close()
	dial := func(q string) *websocket.Conn {
		c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/term?"+q, nil)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	// read collects terminal bytes until they hold want, noting control ops.
	read := func(c *websocket.Conn, want string) (string, []string) {
		t.Helper()
		var out strings.Builder
		var ops []string
		for !strings.Contains(out.String(), want) {
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			mt, b, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("waiting for %q (got %q, ops %v): %v", want, out.String(), ops, err)
			}
			if mt == websocket.BinaryMessage {
				out.Write(b)
				continue
			}
			var ctl map[string]any
			_ = json.Unmarshal(b, &ctl)
			op, _ := ctl["op"].(string)
			ops = append(ops, op)
			if op == "exit" {
				t.Fatalf("exit while waiting for %q: %s", want, b)
			}
		}
		return out.String(), ops
	}
	hello := func(c *websocket.Conn) map[string]any {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		mt, b, err := c.ReadMessage()
		if err != nil || mt != websocket.TextMessage {
			t.Fatalf("first frame: %v %q", err, b)
		}
		var h map[string]any
		_ = json.Unmarshal(b, &h)
		if h["op"] != "session" || h["echoAck"] != true || h["id"] == "" || h["vm"] != false {
			t.Fatalf("session frame = %s", b)
		}
		return h
	}

	c := dial("cwd=apps/x&net=none")
	id := hello(c)["id"].(string)
	if err := c.WriteMessage(websocket.BinaryMessage, []byte("echo hi-$((6*7))\n")); err != nil {
		t.Fatal(err)
	}
	// the answer, and the input's echo ack (it may come before or after)
	if _, ops := read(c, "hi-42"); !strings.Contains(strings.Join(ops, ","), "ack") {
		for acked := false; !acked; {
			_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
			mt, b, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("no echo ack: %v", err)
			}
			acked = mt == websocket.TextMessage && strings.Contains(string(b), `"op":"ack"`)
		}
	}
	c.Close()

	// the session outlives its socket; a reattach replays the scrollback
	waitClients := func(n int) {
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if l := m.List(); len(l) == 1 && l[0]["clients"] == n {
				return
			}
			if time.Now().After(end) {
				t.Fatalf("clients never reached %d: %v", n, m.List())
			}
		}
	}
	waitClients(0)
	c2 := dial("session=" + id)
	defer c2.Close()
	if h := hello(c2); h["id"] != id {
		t.Fatalf("reattach landed on %v, want %s", h["id"], id)
	}
	read(c2, "hi-42")
	waitClients(1)

	// the shell ends: the exit frame, then the socket closes and the session goes
	if err := c2.WriteMessage(websocket.BinaryMessage, []byte("exit\n")); err != nil {
		t.Fatal(err)
	}
	for {
		_ = c2.SetReadDeadline(time.Now().Add(10 * time.Second))
		mt, b, err := c2.ReadMessage()
		if err != nil {
			t.Fatalf("the socket closed without the exit frame: %v", err)
		}
		if mt == websocket.TextMessage && strings.Contains(string(b), `"exit"`) {
			if string(b) != `{"op":"exit"}` {
				t.Fatalf("exit frame = %s", b)
			}
			break
		}
	}
	for end := time.Now().Add(5 * time.Second); len(m.List()) != 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the session outlived its shell: %v", m.List())
		}
	}
}
