package xbin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// wsEcho upgrades r and echoes its messages prefixed "echo:", after a
// session frame.
func wsEcho(w http.ResponseWriter, r *http.Request) {
	c, err := ws.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.WriteMessage(ws.TextMessage, []byte(`{"op":"session","id":"e1","sandbox":"sb-1","echoAck":true}`))
	for {
		typ, msg, err := c.ReadMessage()
		if err != nil {
			return
		}
		_ = c.WriteMessage(typ, append([]byte("echo:"), msg...))
	}
}

// DialTTY attaches through the gateway with the tile's credential and the
// options as the query; a refused attach is a *SandboxError.
func TestSandboxDialTTY(t *testing.T) {
	seen := make(chan *http.Request, 2)
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r
		if strings.Contains(r.URL.Path, "/nope/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			io.WriteString(w, `{"error":"alice may not use a terminal","refusal":"not-allowed"}`)
			return
		}
		wsEcho(w, r)
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := sbx.Sandbox("sb-1").DialTTY(ctx, "e1", TTYOptions{SessionID: "s-1", ForUser: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := <-seen
	if r.URL.Path != "/api/xbin/sandboxes/sb-1/execs/e1/tty" || r.URL.RawQuery != "forUser=alice&sessionId=s-1" || r.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("dialled %s (%s)", r.RequestURI, r.Header.Get("Authorization"))
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

	_, err = sbx.Sandbox("sb-1").DialTTY(ctx, "nope", TTYOptions{})
	var se *SandboxError
	if !errors.As(err, &se) || se.Status != 403 || se.Refusal != "not-allowed" {
		t.Fatalf("refused attach: %#v", err)
	}
	<-seen
}

// Forward of a WebSocket upgrade end to end, sdk/ws on both ends: a
// consumer's ws.Dial through the manager's RelayTTY reaches a runtime's
// ws.Upgrade; messages pass both ways unchanged, and the consumer's Cookie,
// Authorization and X-XBin-* don't pass.
func TestSandboxForwardWebSocket(t *testing.T) {
	seen := make(chan http.Header, 1)
	sbx := fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		wsEcho(w, r)
	}))
	sb := sbx.Sandbox("sb-1")
	mgr := fakeManager(t, func(w http.ResponseWriter, r *http.Request) {
		sb.RelayTTY(w, r, "e1", TTYOptions{SessionID: "s-1"})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	h := http.Header{"Cookie": {"s=1"}, "Authorization": {"Bearer consumer"}, "X-Xbin-From": {"apps/evil"}}
	c, resp, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(mgr.URL, "http")+"/tty", h, nil)
	if err != nil {
		t.Fatal(err, resp)
	}
	defer c.Close()
	got := <-seen
	if got.Get("Authorization") != "Bearer tok" || got.Get("Cookie") != "" || got.Get("X-XBin-From") != "" {
		t.Fatalf("forwarded %v", got)
	}
	if _, msg, err := c.ReadMessage(); err != nil || !strings.Contains(string(msg), `"op":"session"`) {
		t.Fatalf("session frame %q %v", msg, err)
	}
	big := make([]byte, 300<<10) // a paste: one frame, relayed as it came
	for i := range big {
		big[i] = byte(i)
	}
	if err := c.WriteMessage(ws.BinaryMessage, big); err != nil {
		t.Fatal(err)
	}
	if typ, msg, err := c.ReadMessage(); err != nil || typ != ws.BinaryMessage || len(msg) != 5+len(big) || msg[len(msg)-1] != big[len(big)-1] {
		t.Fatalf("echo %d, %d bytes, %v", typ, len(msg), err)
	}
}
