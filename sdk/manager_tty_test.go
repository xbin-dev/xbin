package xbin

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// ManagerTTYURL builds the contract's two terminal routes from typed parts,
// escaping each, and refuses what would name another route.
func TestManagerTTYURL(t *testing.T) {
	for _, c := range []struct {
		endpoint, id string
		o            ManagerTTYOptions
		want         string
	}{
		{"http://xbin/api/apps/mgr", "sb-1", ManagerTTYOptions{}, "ws://xbin/api/apps/mgr/sbx/sandboxes/sb-1/tty"},
		{"http://xbin/api/apps/mgr/", "sb.1_x", ManagerTTYOptions{Cmd: "claude /login", Cwd: "/work", Rows: 30, Cols: 100, User: "alice"},
			"ws://xbin/api/apps/mgr/sbx/sandboxes/sb.1_x/tty?cmd=claude+%2Flogin&cols=100&cwd=%2Fwork&rows=30"},
		{"https://h.example/api/apps/mgr/m/1", "A", ManagerTTYOptions{ExecID: "ab12cd-7"}, "wss://h.example/api/apps/mgr/m/1/sbx/sandboxes/A/execs/ab12cd-7/tty"},
		{"ws://xbin/api/apps/my%20mgr", "sb-1", ManagerTTYOptions{ExecID: "x%2F..?#y z"}, "ws://xbin/api/apps/my%20mgr/sbx/sandboxes/sb-1/execs/x%252F..%3F%23y%20z/tty"},
		{"wss://h.example:8443/m", "sb-1", ManagerTTYOptions{Rows: 5}, "wss://h.example:8443/m/sbx/sandboxes/sb-1/tty?rows=5"},
	} {
		got, err := ManagerTTYURL(c.endpoint, c.id, c.o)
		if err != nil || got != c.want {
			t.Errorf("ManagerTTYURL(%q, %q, %+v) = %q, %v; want %q", c.endpoint, c.id, c.o, got, err, c.want)
		}
	}
	const ok = "http://xbin/api/apps/mgr"
	for _, c := range []struct {
		endpoint, id string
		o            ManagerTTYOptions
	}{
		{"", "sb-1", ManagerTTYOptions{}},
		{"/api/apps/mgr", "sb-1", ManagerTTYOptions{}},
		{"ftp://xbin/api/apps/mgr", "sb-1", ManagerTTYOptions{}},
		{"http://xbin/api/apps/mgr?x=1", "sb-1", ManagerTTYOptions{}},
		{"http://xbin/api/apps/mgr#f", "sb-1", ManagerTTYOptions{}},
		{"http://u:p@xbin/api/apps/mgr", "sb-1", ManagerTTYOptions{}},
		{ok, "", ManagerTTYOptions{}},
		{ok, ".", ManagerTTYOptions{}},
		{ok, "..", ManagerTTYOptions{}},
		{ok, "../sb-2", ManagerTTYOptions{}},
		{ok, "a/b", ManagerTTYOptions{}},
		{ok, "a%2Fb", ManagerTTYOptions{}},
		{ok, "-a", ManagerTTYOptions{}},
		{ok, "a b", ManagerTTYOptions{}},
		{ok, "a?b", ManagerTTYOptions{}},
		{ok, strings.Repeat("a", 65), ManagerTTYOptions{}},
		{ok, "sb-1", ManagerTTYOptions{ExecID: "."}},
		{ok, "sb-1", ManagerTTYOptions{ExecID: ".."}},
		{ok, "sb-1", ManagerTTYOptions{ExecID: "a/b"}},
		{ok, "sb-1", ManagerTTYOptions{ExecID: "a\nb"}},
		{ok, "sb-1", ManagerTTYOptions{ExecID: "e1", Cmd: "sh"}},
		{ok, "sb-1", ManagerTTYOptions{ExecID: "e1", Cwd: "/work"}},
		{ok, "sb-1", ManagerTTYOptions{ExecID: "e1", Rows: 3}},
		{ok, "sb-1", ManagerTTYOptions{Cols: -1}},
	} {
		if got, err := ManagerTTYURL(c.endpoint, c.id, c.o); !isInvalid(err) {
			t.Errorf("ManagerTTYURL(%q, %q, %+v) = %q, %v; want invalid", c.endpoint, c.id, c.o, got, err)
		}
	}
}

// DialManagerTTY dials through the gateway with the tile's credential, the
// person in Sbx-User; a refusal is a *SandboxError, and a route or person
// that would change the call is refused before anything is dialled.
func TestDialManagerTTY(t *testing.T) {
	seen := make(chan *http.Request, 4)
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		if strings.Contains(r.URL.Path, "/sb-gone/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"no such sandbox","refusal":"not-found"}`)
			return
		}
		wsEcho(w, r)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const mgr = "http://xbin/api/apps/mgr"
	c, err := DialManagerTTY(ctx, mgr, "sb-1", ManagerTTYOptions{Cmd: "codex login --device-auth", Rows: 24, Cols: 80, User: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	r := <-seen
	if r.URL.Path != "/api/apps/mgr/sbx/sandboxes/sb-1/tty" || r.URL.RawQuery != "cmd=codex+login+--device-auth&cols=80&rows=24" ||
		r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Sbx-User") != "alice" {
		t.Fatalf("dialled %s (%v)", r.RequestURI, r.Header)
	}
	if typ, msg, err := c.ReadMessage(); err != nil || typ != ws.TextMessage || !strings.Contains(string(msg), `"op":"session"`) {
		t.Fatalf("session frame %d %q %v", typ, msg, err)
	}
	if err := c.WriteMessage(ws.BinaryMessage, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	if typ, msg, err := c.ReadMessage(); err != nil || typ != ws.BinaryMessage || string(msg) != "echo:ls\r" {
		t.Fatalf("echo %d %q %v", typ, msg, err)
	}
	c.Close()
	// an attach, for the consumer itself (no person)
	c, err = DialManagerTTY(ctx, mgr, "sb-1", ManagerTTYOptions{ExecID: "ab12cd-3"})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if r := <-seen; r.URL.Path != "/api/apps/mgr/sbx/sandboxes/sb-1/execs/ab12cd-3/tty" || r.URL.RawQuery != "" || r.Header.Get("Sbx-User") != "" {
		t.Fatalf("attached with %s (%v)", r.RequestURI, r.Header)
	}
	// the manager's refusal, as it said it
	_, err = DialManagerTTY(ctx, mgr, "sb-gone", ManagerTTYOptions{User: "alice"})
	var se *SandboxError
	if !errors.As(err, &se) || se.Status != 404 || !errors.Is(err, ErrSandboxNotFound) || se.Message != "no such sandbox" {
		t.Fatalf("refused: %#v", err)
	}
	<-seen
	// refused before anything is dialled
	for _, bad := range []struct {
		id string
		o  ManagerTTYOptions
	}{
		{"../sb-1", ManagerTTYOptions{}},
		{"sb-1", ManagerTTYOptions{ExecID: "../../sb-2/tty"}},
		{"sb-1", ManagerTTYOptions{User: "alice\r\nX-XBin-From: apps/evil"}},
	} {
		if _, err := DialManagerTTY(ctx, mgr, bad.id, bad.o); !isInvalid(err) {
			t.Fatalf("%q %+v: %v", bad.id, bad.o, err)
		}
	}
	select {
	case r := <-seen:
		t.Fatalf("dialled %s", r.RequestURI)
	default:
	}
}

// rawDrop answers a WebSocket handshake by hand, sends the session frame
// and drops the connection without a close frame: a manager lost.
func rawDrop(w http.ResponseWriter, r *http.Request) {
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(sum[:]))
	session := `{"op":"session","id":"ab12cd-1","sandbox":"sb-1","echoAck":false}`
	brw.Write([]byte{0x81, byte(len(session))})
	brw.WriteString(session)
	brw.Flush()
}

// RelayManagerTTY: the person's WebSocket is relayed to the manager's
// terminal message for message, both ways; nothing of the person's request
// reaches the manager; a close either way closes the other the same way (a
// lost manager: 1011); a refusal comes back before the upgrade, and a
// request that isn't a handshake never reaches the manager.
func TestRelayManagerTTY(t *testing.T) {
	seen := make(chan *http.Request, 8)
	ended := make(chan error, 8) // how the manager's read of the relay ended
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		switch {
		case strings.Contains(r.URL.Path, "/sb-gone/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			io.WriteString(w, `{"error":"no such sandbox","refusal":"not-found"}`)
			return
		case r.URL.Query().Get("cmd") == "drop":
			rawDrop(w, r)
			return
		}
		c, err := ws.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(ws.TextMessage, []byte(`{"op":"session","id":"ab12cd-1","sandbox":"sb-1","echoAck":false}`))
		for {
			typ, msg, err := c.ReadMessage()
			if err != nil {
				ended <- err
				return
			}
			if r.URL.Query().Get("cmd") == "exit" {
				_ = c.WriteMessage(ws.TextMessage, []byte(`{"op":"exit","code":0}`))
				return // Close: 1000
			}
			_ = c.WriteMessage(typ, append([]byte("echo:"), msg...))
		}
	}))
	type relayed struct {
		ManagerTTYRelay
		noted string // what OnSession was told
	}
	answers := make(chan relayed, 8)
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// (a consumer checks the person first; the test's picks the sandbox
		// and command from the query)
		var noted string
		res := RelayManagerTTY(w, r, "http://xbin/api/apps/mgr", r.URL.Query().Get("sb"), ManagerTTYOptions{User: "alice", Cmd: r.URL.Query().Get("cmd"),
			OnSession: func(id string) { noted = id }})
		answers <- relayed{res, noted}
	}))
	defer consumer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	page := func(query string) (*ws.Conn, *http.Response, error) {
		h := http.Header{"Cookie": {"session=secret"}, "Sbx-User": {"mallory"}, "X-Xbin-User": {"mallory"}, "Authorization": {"Bearer page"}}
		return ws.Dial(ctx, "ws"+strings.TrimPrefix(consumer.URL, "http")+"/?"+query, h, nil)
	}

	// relayed both ways, message for message
	c, _, err := page("sb=sb-1&cmd=echo")
	if err != nil {
		t.Fatal(err)
	}
	r := <-seen
	if r.URL.Path != "/api/apps/mgr/sbx/sandboxes/sb-1/tty" || r.URL.RawQuery != "cmd=echo" || r.Header.Get("Authorization") != "Bearer tok" ||
		r.Header.Get("Sbx-User") != "alice" || r.Header.Get("Cookie") != "" || r.Header.Get("X-XBin-User") != "" {
		t.Fatalf("the manager saw %s %v", r.RequestURI, r.Header)
	}
	if typ, msg, err := c.ReadMessage(); err != nil || typ != ws.TextMessage || string(msg) != `{"op":"session","id":"ab12cd-1","sandbox":"sb-1","echoAck":false}` {
		t.Fatalf("session frame %d %q %v", typ, msg, err)
	}
	big := make([]byte, 300<<10) // a paste: one message
	for i := range big {
		big[i] = byte(i)
	}
	for _, m := range []struct {
		typ  int
		data []byte
	}{{ws.BinaryMessage, []byte("ls\r")}, {ws.TextMessage, []byte(`{"op":"resize","cols":90,"rows":30}`)}, {ws.BinaryMessage, big}} {
		if err := c.WriteMessage(m.typ, m.data); err != nil {
			t.Fatal(err)
		}
		typ, msg, err := c.ReadMessage()
		if err != nil || typ != m.typ || string(msg) != "echo:"+string(m.data) {
			t.Fatalf("relayed %d (%d bytes) → %d (%d bytes) %v", m.typ, len(m.data), typ, len(msg), err)
		}
	}
	// the person leaves: the manager is closed the same way
	c.CloseWith(4001, "bye")
	if err := <-ended; !ws.IsClose(err, 4001) {
		t.Fatalf("the manager's end: %v", err)
	}
	if a := <-answers; a.Session != "ab12cd-1" || a.Exited || a.noted != "ab12cd-1" {
		t.Fatalf("a terminal the person left: %+v", a)
	}

	// the terminal ends: its exit frame, then a normal close
	c, _, err = page("sb=sb-1&cmd=exit")
	if err != nil {
		t.Fatal(err)
	}
	<-seen
	c.ReadMessage() // session
	c.WriteMessage(ws.BinaryMessage, []byte("x"))
	var exit struct{ Op string }
	if typ, msg, err := c.ReadMessage(); err != nil || typ != ws.TextMessage || json.Unmarshal(msg, &exit) != nil || exit.Op != "exit" {
		t.Fatalf("exit frame %d %q %v", typ, msg, err)
	}
	if _, _, err := c.ReadMessage(); !ws.IsClose(err, ws.CloseNormalClosure) {
		t.Fatalf("after exit: %v", err)
	}
	if a := <-answers; a.Session != "ab12cd-1" || !a.Exited {
		t.Fatalf("a terminal that exited: %+v", a)
	}

	// the manager lost: 1011, so the terminal reconnects
	c, _, err = page("sb=sb-1&cmd=drop")
	if err != nil {
		t.Fatal(err)
	}
	<-seen
	if _, msg, err := c.ReadMessage(); err != nil || !strings.Contains(string(msg), `"op":"session"`) {
		t.Fatalf("session frame %q %v", msg, err)
	}
	if _, _, err := c.ReadMessage(); !ws.IsClose(err, ws.CloseInternalServerErr) {
		t.Fatalf("a lost manager: %v", err)
	}
	if a := <-answers; a.Session != "ab12cd-1" || a.Exited {
		t.Fatalf("a lost manager's terminal: %+v", a)
	}

	// the manager's refusal, before the upgrade
	_, resp, err := page("sb=sb-gone")
	if err == nil || resp == nil || resp.StatusCode != 404 {
		t.Fatalf("refused: %v %v", resp, err)
	}
	b, _ := io.ReadAll(resp.Body)
	var ref struct{ Error, Refusal string }
	if json.Unmarshal(b, &ref) != nil || ref.Refusal != "not-found" || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("the refusal: %s", b)
	}
	<-seen
	if a := <-answers; a != (relayed{}) {
		t.Fatalf("a refused terminal: %+v", a)
	}

	// not a handshake, or a bad sandbox id: refused, the manager never asked
	for _, q := range []string{"sb=sb-1&cmd=echo", "sb=..%2Fsb-2&cmd=echo"} {
		resp, err := http.Get(consumer.URL + "/?" + q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 400 || !strings.Contains(string(b), `"refusal":"invalid"`) {
			t.Fatalf("GET ?%s: %d %s", q, resp.StatusCode, b)
		}
	}
	if _, resp, err := page("sb=..%2Fsb-2"); err == nil || resp == nil || resp.StatusCode != 400 {
		t.Fatalf("a bad sandbox id: %v %v", resp, err)
	}
	select {
	case r := <-seen:
		t.Fatalf("the manager was asked %s", r.RequestURI)
	default:
	}
}
