package wsinterop

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/xbin-dev/xbin/sdk/ws"
)

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31 + i>>10)
	}
	return b
}

var sizes = []int{0, 1, 125, 126, 0xffff, 0x10000, 1<<20 + 7, 6 << 20}

func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

// An sdk/ws client against a gorilla server.
func TestSDKClientGorillaServer(t *testing.T) {
	t.Parallel()
	pongFromClient := make(chan string, 1)
	serverDone := make(chan error, 1)
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, WriteBufferSize: 1024}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.SetReadLimit(64 << 20)
		c.SetPongHandler(func(s string) error { pongFromClient <- s; return nil })
		for {
			typ, msg, err := c.ReadMessage()
			if err != nil {
				serverDone <- err
				return
			}
			switch string(msg) {
			case "ping me":
				_ = c.WriteControl(websocket.PingMessage, []byte("from gorilla"), time.Now().Add(time.Second))
			case "fragments":
				// a message in 1 KiB frames (the write buffer's size)
				wr, _ := c.NextWriter(websocket.BinaryMessage)
				_, _ = wr.Write(pattern(10_000))
				_ = wr.Close()
				continue
			}
			if err := c.WriteMessage(typ, msg); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c, resp, err := ws.Dial(context.Background(), wsURL(srv), nil, nil)
	if err != nil {
		t.Fatalf("dial: %v (%v)", err, resp)
	}
	pongs := make(chan string, 1)
	c.SetPongHandler(func(b []byte) { pongs <- string(b) })
	for _, n := range sizes {
		want := pattern(n)
		if err := c.WriteMessage(ws.BinaryMessage, want); err != nil {
			t.Fatal(err)
		}
		if typ, got, err := c.ReadMessage(); err != nil || typ != ws.BinaryMessage || !bytes.Equal(got, want) {
			t.Fatalf("%d bytes: type %d, %d back, %v", n, typ, len(got), err)
		}
	}
	// fragmented by gorilla, reassembled here
	c.WriteMessage(ws.TextMessage, []byte("fragments"))
	if _, got, err := c.ReadMessage(); err != nil || !bytes.Equal(got, pattern(10_000)) {
		t.Fatalf("fragments: %d bytes, %v", len(got), err)
	}
	// our ping: gorilla's default handler answers
	c.Ping([]byte("from sdk"))
	c.WriteMessage(ws.TextMessage, []byte("after ping"))
	if _, got, err := c.ReadMessage(); err != nil || string(got) != "after ping" {
		t.Fatalf("%q %v", got, err)
	}
	if p := <-pongs; p != "from sdk" {
		t.Fatalf("pong %q", p)
	}
	// gorilla's ping: answered while we read
	c.WriteMessage(ws.TextMessage, []byte("ping me"))
	if _, got, err := c.ReadMessage(); err != nil || string(got) != "ping me" {
		t.Fatalf("%q %v", got, err)
	}
	c.WriteMessage(ws.TextMessage, []byte("sync")) // gorilla handles the pong on its next read
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if p := <-pongFromClient; p != "from gorilla" {
		t.Fatalf("gorilla got pong %q", p)
	}
	// the close handshake
	start := time.Now()
	c.CloseWith(ws.CloseGoingAway, "bye")
	var ce *websocket.CloseError
	if err := <-serverDone; !errors.As(err, &ce) || ce.Code != websocket.CloseGoingAway || ce.Text != "bye" {
		t.Fatalf("gorilla saw %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the close wasn't answered")
	}
}

// A gorilla client against an sdk/ws server.
func TestGorillaClientSDKServer(t *testing.T) {
	t.Parallel()
	serverErr := make(chan error, 1)
	serverPongs := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := ws.Upgrade(w, r, &ws.UpgradeOptions{MaxMessageSize: 8 << 20})
		if err != nil {
			return
		}
		defer c.Close()
		c.SetPongHandler(func(b []byte) { serverPongs <- string(b) })
		for {
			typ, msg, err := c.ReadMessage()
			if err != nil {
				serverErr <- err
				return
			}
			if string(msg) == "ping me" {
				c.Ping([]byte("from sdk"))
			}
			if err := c.WriteMessage(typ, msg); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	d := websocket.Dialer{WriteBufferSize: 1024} // a client message goes out in 1 KiB frames
	c, _, err := d.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadLimit(64 << 20)
	pongs := make(chan string, 1)
	c.SetPongHandler(func(s string) error { pongs <- s; return nil })
	for _, n := range sizes {
		want := pattern(n)
		if err := c.WriteMessage(websocket.BinaryMessage, want); err != nil {
			t.Fatal(err)
		}
		if typ, got, err := c.ReadMessage(); err != nil || typ != websocket.BinaryMessage || !bytes.Equal(got, want) {
			t.Fatalf("%d bytes: type %d, %d back, %v", n, typ, len(got), err)
		}
	}
	c.WriteMessage(websocket.TextMessage, []byte("héllo"))
	if typ, got, err := c.ReadMessage(); err != nil || typ != websocket.TextMessage || string(got) != "héllo" {
		t.Fatalf("text: %q %v", got, err)
	}
	// gorilla's ping: our reader answers
	c.WriteControl(websocket.PingMessage, []byte("from gorilla"), time.Now().Add(time.Second))
	c.WriteMessage(websocket.TextMessage, []byte("after ping"))
	if _, got, err := c.ReadMessage(); err != nil || string(got) != "after ping" {
		t.Fatalf("%q %v", got, err)
	}
	if p := <-pongs; p != "from gorilla" {
		t.Fatalf("pong %q", p)
	}
	// our ping: gorilla answers while it reads
	c.WriteMessage(websocket.TextMessage, []byte("ping me"))
	if _, got, err := c.ReadMessage(); err != nil || string(got) != "ping me" {
		t.Fatalf("%q %v", got, err)
	}
	c.WriteMessage(websocket.TextMessage, []byte("sync"))
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if p := <-serverPongs; p != "from sdk" {
		t.Fatalf("the server's pong %q", p)
	}
	// over the server's limit: closed with 1009
	c.WriteMessage(websocket.BinaryMessage, make([]byte, 9<<20))
	if _, _, err := c.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("over the limit: %v", err)
	}
	if err := <-serverErr; !errors.Is(err, ws.ErrMessageTooBig) {
		t.Fatalf("the server: %v", err)
	}

	// the close handshake, gorilla first
	c2, _, err := d.Dial(wsURL(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	c2.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4000, "done"))
	if err := <-serverErr; !ws.IsClose(err, 4000) || err.(*ws.CloseError).Text != "done" {
		t.Fatalf("the server saw %v", err)
	}
	if _, _, err := c2.ReadMessage(); !websocket.IsCloseError(err, 4000) {
		t.Fatalf("the echoed close: %v", err)
	}
}
