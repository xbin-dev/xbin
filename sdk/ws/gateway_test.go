package ws_test

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/ws"
)

// Dial through xbin.Client(): the handshake goes to the gateway socket with
// the instance's bearer token, as a backend reaches another tile.
func TestDialThroughGateway(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan string, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization") + " " + r.Host + r.URL.Path
		c, err := ws.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			typ, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			c.WriteMessage(typ, msg)
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "tok-123")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := ws.Dial(ctx, "ws://xbin/api/apps/other/stream", nil, &ws.DialOptions{Client: xbin.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if got := <-seen; got != "Bearer tok-123 xbin/api/apps/other/stream" {
		t.Fatalf("the gateway saw %q", got)
	}
	big := make([]byte, 3<<20)
	for i := range big {
		big[i] = byte(i)
	}
	if err := c.WriteMessage(ws.BinaryMessage, big); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := c.ReadMessage(); err != nil || len(msg) != len(big) || msg[len(msg)-1] != big[len(big)-1] {
		t.Fatalf("echo: %d bytes, %v", len(msg), err)
	}
	// the deadline is the gateway connection's own
	c.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("a read past its deadline succeeded")
	}
}
