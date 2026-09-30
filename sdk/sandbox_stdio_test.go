package xbin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// stdioEcho upgrades r and answers a stdio socket's way: the hello frame,
// then every binary frame back as stdout, prefixed "out:".
func stdioEcho(w http.ResponseWriter, r *http.Request) {
	c, err := ws.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.WriteMessage(ws.TextMessage, []byte(`{"op":"hello","id":"ab12cd-1","total":5,"errTotal":0,"state":"running","stdin":true,"split":true}`))
	for {
		typ, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		if typ == ws.BinaryMessage {
			_ = c.WriteMessage(ws.BinaryMessage, append([]byte("out:"), msg...))
		}
	}
}

// DialStdio attaches through the gateway with the tile's credential, the
// offsets as the query; RelayStdio forwards a consumer's socket there; both
// refuse a bad id or offset before anything is sent.
func TestSandboxStdio(t *testing.T) {
	seen := make(chan *http.Request, 4)
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		if strings.Contains(r.URL.Path, "/ab12cd-9/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"exec ab12cd-9 has a terminal","refusal":"invalid"}`)
			return
		}
		stdioEcho(w, r)
	}))
	sb := sbx.Sandbox("sb-1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := sb.DialStdio(ctx, "ab12cd-1", 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	r := <-seen
	if r.URL.Path != "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/stdio" || r.URL.RawQuery != "errSince=2&since=5" || r.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("dialled %s (%s)", r.RequestURI, r.Header.Get("Authorization"))
	}
	_, msg, err := c.ReadMessage()
	var hello StdioFrame
	if err != nil || json.Unmarshal(msg, &hello) != nil || hello.Op != "hello" || hello.ID != "ab12cd-1" || hello.Total != 5 || !hello.Stdin || !hello.Split {
		t.Fatalf("hello %q %v: %+v", msg, err, hello)
	}
	if err := c.WriteMessage(ws.BinaryMessage, []byte("ping\n")); err != nil {
		t.Fatal(err)
	}
	if typ, msg, err := c.ReadMessage(); err != nil || typ != ws.BinaryMessage || string(msg) != "out:ping\n" {
		t.Fatalf("stdout %d %q %v", typ, msg, err)
	}
	c.Close()
	// no offsets: no query
	c, err = sb.DialStdio(ctx, "ab12cd-2", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if r := <-seen; r.URL.RawQuery != "" {
		t.Fatalf("dialled %s", r.RequestURI)
	}
	_, err = sb.DialStdio(ctx, "ab12cd-9", 0, 0)
	var se *SandboxError
	if !errors.As(err, &se) || se.Status != 400 || se.Refusal != "invalid" {
		t.Fatalf("a refused attach: %#v", err)
	}
	<-seen
	for _, bad := range []struct {
		id              string
		since, errSince int64
	}{{"../x", 0, 0}, {"ab12cd-1", -1, 0}, {"ab12cd-1", 0, -2}} {
		if _, err := sb.DialStdio(ctx, bad.id, bad.since, bad.errSince); !isInvalid(err) {
			t.Fatalf("DialStdio(%q, %d, %d): %v", bad.id, bad.since, bad.errSince, err)
		}
	}

	// a manager relaying its consumer's socket: the offsets it chose, never
	// the consumer's query or credentials
	mgr := fakeManager(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bad":
			sb.RelayStdio(w, r, "..", 0, 0)
		case "/neg":
			sb.RelayStdio(w, r, "ab12cd-1", -1, 0)
		default:
			sb.RelayStdio(w, r, "ab12cd-1", 7, 0)
		}
	})
	h := http.Header{"Cookie": {"s=1"}, "Authorization": {"Bearer consumer"}, "X-Xbin-From": {"apps/evil"}}
	c, resp, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(mgr.URL, "http")+"/stdio?since=0&errSince=9", h, nil)
	if err != nil {
		t.Fatal(err, resp)
	}
	defer c.Close()
	r = <-seen
	if r.URL.Path != "/api/xbin/sandboxes/sb-1/execs/ab12cd-1/stdio" || r.URL.RawQuery != "since=7" ||
		r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Cookie") != "" || r.Header.Get("X-XBin-From") != "" {
		t.Fatalf("relayed %s (%v)", r.RequestURI, r.Header)
	}
	if _, msg, err := c.ReadMessage(); err != nil || !strings.Contains(string(msg), `"op":"hello"`) {
		t.Fatalf("hello %q %v", msg, err)
	}
	for _, p := range []string{"/bad", "/neg"} {
		if _, resp, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(mgr.URL, "http")+p, nil, nil); err == nil || resp == nil || resp.StatusCode != 400 {
			t.Fatalf("%s: %v %v", p, resp, err)
		}
	}
	select {
	case r := <-seen:
		t.Fatalf("relayed %s", r.RequestURI)
	default:
	}
}

// ManagerStdioURL builds the contract's stdio route from typed parts;
// DialManagerStdio dials it through the gateway, the person in Sbx-User.
func TestDialManagerStdio(t *testing.T) {
	for _, c := range []struct {
		endpoint, id, eid string
		since, errSince   int64
		want              string
	}{
		{"http://xbin/api/apps/mgr", "sb-1", "ab12cd-1", 0, 0, "ws://xbin/api/apps/mgr/sbx/sandboxes/sb-1/execs/ab12cd-1/stdio"},
		{"https://h.example/api/apps/mgr/", "A", "e 1?", 10, 3, "wss://h.example/api/apps/mgr/sbx/sandboxes/A/execs/e%201%3F/stdio?errSince=3&since=10"},
	} {
		if got, err := ManagerStdioURL(c.endpoint, c.id, c.eid, c.since, c.errSince); err != nil || got != c.want {
			t.Errorf("ManagerStdioURL(%q, %q, %q, %d, %d) = %q, %v; want %q", c.endpoint, c.id, c.eid, c.since, c.errSince, got, err, c.want)
		}
	}
	const mgr = "http://xbin/api/apps/mgr"
	for _, c := range []struct {
		endpoint, id, eid string
		since, errSince   int64
	}{
		{"/api/apps/mgr", "sb-1", "e1", 0, 0}, {mgr, "../x", "e1", 0, 0}, {mgr, "sb-1", "", 0, 0}, {mgr, "sb-1", "..", 0, 0},
		{mgr, "sb-1", "a/b", 0, 0}, {mgr, "sb-1", "e1", -1, 0}, {mgr, "sb-1", "e1", 0, -1},
	} {
		if got, err := ManagerStdioURL(c.endpoint, c.id, c.eid, c.since, c.errSince); !isInvalid(err) {
			t.Errorf("ManagerStdioURL(%+v) = %q, %v; want invalid", c, got, err)
		}
	}
	seen := make(chan *http.Request, 2)
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		stdioEcho(w, r)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := DialManagerStdio(ctx, mgr, "sb-1", "ab12cd-1", ManagerStdioOptions{Since: 4, User: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := <-seen
	if r.URL.Path != "/api/apps/mgr/sbx/sandboxes/sb-1/execs/ab12cd-1/stdio" || r.URL.RawQuery != "since=4" ||
		r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Sbx-User") != "alice" {
		t.Fatalf("dialled %s (%v)", r.RequestURI, r.Header)
	}
	if _, msg, err := c.ReadMessage(); err != nil || !strings.Contains(string(msg), `"op":"hello"`) {
		t.Fatalf("hello %q %v", msg, err)
	}
	if _, err := DialManagerStdio(ctx, mgr, "sb-1", "ab12cd-1", ManagerStdioOptions{User: "a\nb"}); !isInvalid(err) {
		t.Fatalf("a user with a control character: %v", err)
	}
	select {
	case r := <-seen:
		t.Fatalf("dialled %s", r.RequestURI)
	default:
	}
}

// StdioFrame decodes every server frame: stderr's data from base64, a
// signal's exit with no code.
func TestStdioFrame(t *testing.T) {
	var f StdioFrame
	if err := json.Unmarshal([]byte(`{"op":"stderr","off":12,"data":"aGk="}`), &f); err != nil || f.Off != 12 || string(f.Data) != "hi" {
		t.Fatalf("stderr: %+v %v", f, err)
	}
	f = StdioFrame{}
	if err := json.Unmarshal([]byte(`{"op":"exit","code":null,"signal":"KILL","total":3,"errTotal":4}`), &f); err != nil || f.Code != nil || f.Signal != "KILL" || f.Total != 3 || f.ErrTotal != 4 {
		t.Fatalf("exit: %+v %v", f, err)
	}
	f = StdioFrame{}
	if err := json.Unmarshal([]byte(`{"op":"gap","stream":"stdout","from":0,"to":9}`), &f); err != nil || f.Stream != "stdout" || f.To != 9 {
		t.Fatalf("gap: %+v %v", f, err)
	}
}
