package ws

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// echoServer upgrades every request and echoes each message back until the
// client closes; opts tune the upgrade.
func echoServer(t *testing.T, opts *UpgradeOptions) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r, opts)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			typ, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			if err := c.WriteMessage(typ, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

func dial(t *testing.T, u string, opts *DialOptions) *Conn {
	t.Helper()
	c, resp, err := Dial(context.Background(), u, nil, opts)
	if err != nil {
		t.Fatalf("dial %s: %v", u, err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i>>8)
	}
	return b
}

func TestEchoSizes(t *testing.T) {
	t.Parallel()
	c := dial(t, wsURL(echoServer(t, nil)), nil)
	for _, n := range []int{0, 1, 125, 126, 127, 0xffff, 0x10000, 1<<20 + 3, 5 << 20} {
		want := pattern(n)
		if err := c.WriteMessage(BinaryMessage, want); err != nil {
			t.Fatal(err)
		}
		typ, got, err := c.ReadMessage()
		if err != nil || typ != BinaryMessage || !bytes.Equal(got, want) {
			t.Fatalf("%d bytes: type %d, %d bytes back, %v", n, typ, len(got), err)
		}
	}
	if err := c.WriteMessage(TextMessage, []byte("héllo")); err != nil {
		t.Fatal(err)
	}
	if typ, got, err := c.ReadMessage(); err != nil || typ != TextMessage || string(got) != "héllo" {
		t.Fatalf("text: %d %q %v", typ, got, err)
	}
}

// pair is two connected Conns over loopback TCP, the server side first.
func pair(t *testing.T, max int64) (srv, cli *Conn, raw net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan net.Conn)
	go func() {
		c, _ := ln.Accept()
		done <- c
	}()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b := <-done
	srv = newConn(b, b, nil, true, max, "")
	cli = newConn(a, a, nil, false, max, "")
	t.Cleanup(func() { a.Close(); b.Close() })
	return srv, cli, a
}

func TestFragmentsReassemble(t *testing.T) {
	t.Parallel()
	srv, cli, _ := pair(t, 0)
	pongs := make(chan string, 1)
	cli.SetPongHandler(func(b []byte) { pongs <- string(b) })
	go func() { // the client reads, so the server's pong reaches its handler
		for {
			if _, _, err := cli.ReadMessage(); err != nil {
				return
			}
		}
	}()
	// a text message in three fragments with a ping between them
	for _, f := range []struct {
		fin bool
		op  byte
		p   string
	}{{false, TextMessage, "hel"}, {false, opContinuation, "lo, "}, {true, PingMessage, "mid"}, {true, opContinuation, "wörld"}} {
		if err := cli.writeFrame(f.fin, f.op, []byte(f.p)); err != nil {
			t.Fatal(err)
		}
	}
	typ, msg, err := srv.ReadMessage()
	if err != nil || typ != TextMessage || string(msg) != "hello, wörld" {
		t.Fatalf("reassembled: %d %q %v", typ, msg, err)
	}
	select {
	case p := <-pongs:
		if p != "mid" {
			t.Fatalf("pong %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the ping between fragments wasn't answered")
	}
}

// protocol breaches: the reader closes with the RFC's code
func TestProtocolErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		raw  func(cli *Conn, raw net.Conn)
		code int
	}{
		{"unmasked from a client", func(_ *Conn, raw net.Conn) { raw.Write([]byte{0x82, 0x01, 'x'}) }, CloseProtocolError},
		{"reserved bits", func(cli *Conn, _ net.Conn) { cli.writeFrame(true, BinaryMessage|0x40, []byte("x")) }, CloseProtocolError},
		{"an unknown opcode", func(cli *Conn, _ net.Conn) { cli.writeFrame(true, 3, []byte("x")) }, CloseProtocolError},
		{"a lone continuation", func(cli *Conn, _ net.Conn) { cli.writeFrame(true, opContinuation, []byte("x")) }, CloseProtocolError},
		{"a message inside a message", func(cli *Conn, _ net.Conn) {
			cli.writeFrame(false, TextMessage, []byte("a"))
			cli.writeFrame(true, TextMessage, []byte("b"))
		}, CloseProtocolError},
		{"a fragmented ping", func(cli *Conn, _ net.Conn) { cli.writeFrame(false, PingMessage, []byte("x")) }, CloseProtocolError},
		{"a long ping", func(cli *Conn, _ net.Conn) { cli.writeFrame(true, PingMessage, make([]byte, 126)) }, CloseProtocolError},
		{"text that isn't UTF-8", func(cli *Conn, _ net.Conn) { cli.writeFrame(true, TextMessage, []byte{0xff, 0xfe}) }, CloseInvalidPayload},
		{"a bad close code", func(cli *Conn, _ net.Conn) { cli.writeFrame(true, CloseMessage, []byte{0x03, 0xe8 + 4}) }, CloseProtocolError},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, cli, raw := pair(t, 0)
			c.raw(cli, raw)
			if _, _, err := srv.ReadMessage(); err == nil || IsClose(err) {
				t.Fatalf("the server read on: %v", err)
			}
			if _, _, err := srv.ReadMessage(); err == nil { // sticky
				t.Fatal("a second read succeeded")
			}
			if _, _, err := cli.ReadMessage(); !IsClose(err, c.code) {
				t.Fatalf("the client got %v, want a close %d", err, c.code)
			}
		})
	}
	t.Run("masked from a server", func(t *testing.T) {
		t.Parallel()
		srv, cli, _ := pair(t, 0)
		srv.server = false // (its frames go out masked)
		srv.writeFrame(true, BinaryMessage, []byte("x"))
		if _, _, err := cli.ReadMessage(); err == nil || !strings.Contains(err.Error(), "masked frame from a server") {
			t.Fatalf("client: %v", err)
		}
	})
}

func TestMaxMessageSize(t *testing.T) {
	t.Parallel()
	srv, cli, _ := pair(t, 1000)
	go func() {
		cli.WriteMessage(BinaryMessage, make([]byte, 1000))
		cli.writeFrame(false, BinaryMessage, make([]byte, 600)) // 1200 in two fragments
		cli.writeFrame(true, opContinuation, make([]byte, 600))
	}()
	if _, msg, err := srv.ReadMessage(); err != nil || len(msg) != 1000 {
		t.Fatalf("at the limit: %d %v", len(msg), err)
	}
	if _, _, err := srv.ReadMessage(); !errors.Is(err, ErrMessageTooBig) {
		t.Fatalf("over it: %v", err)
	}
	if _, _, err := cli.ReadMessage(); !IsClose(err, CloseMessageTooBig) {
		t.Fatalf("the client: %v", err)
	}
}

func TestPingPong(t *testing.T) {
	t.Parallel()
	c := dial(t, wsURL(echoServer(t, nil)), nil)
	pongs := make(chan string, 2)
	c.SetPongHandler(func(b []byte) { pongs <- string(b) })
	if err := c.Ping([]byte("are you there")); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(TextMessage, []byte("after")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "after" {
		t.Fatalf("%q %v", msg, err)
	}
	select {
	case p := <-pongs:
		if p != "are you there" {
			t.Fatalf("pong %q", p)
		}
	default:
		t.Fatal("the pong (before the echo on the wire) didn't reach the handler")
	}
	if err := c.Ping(make([]byte, 126)); err == nil {
		t.Fatal("a 126-byte ping went out")
	}
}

func TestCloseHandshake(t *testing.T) {
	t.Parallel()
	// the client closes, nobody reading on its side: Close reads the answer itself
	srv, cli, _ := pair(t, 0)
	got := make(chan error, 1)
	go func() {
		_, _, err := srv.ReadMessage()
		got <- err
	}()
	start := time.Now()
	cli.CloseWith(4001, "done here")
	if err := <-got; !IsClose(err, 4001) || err.(*CloseError).Text != "done here" {
		t.Fatalf("server: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("close took %s: the answer wasn't seen", d)
	}
	if err := cli.WriteMessage(TextMessage, []byte("late")); !errors.Is(err, ErrClosed) {
		t.Fatalf("a write after close: %v", err)
	}

	// the server closes while its own reader waits: the reader sees the answer
	srv, cli, _ = pair(t, 0)
	srvRead := make(chan error, 1)
	go func() {
		_, _, err := srv.ReadMessage()
		srvRead <- err
	}()
	cliRead := make(chan error, 1)
	go func() {
		_, _, err := cli.ReadMessage()
		cliRead <- err
	}()
	time.Sleep(20 * time.Millisecond) // (the reader holds its lock)
	start = time.Now()
	srv.Close()
	if err := <-cliRead; !IsClose(err, CloseNormalClosure) {
		t.Fatalf("client: %v", err)
	}
	if err := <-srvRead; !IsClose(err, CloseNormalClosure) {
		t.Fatalf("server's reader: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("close took %s", d)
	}
	srv.Close() // twice is fine
}

func TestDeadlines(t *testing.T) {
	t.Parallel()
	srv, cli, _ := pair(t, 0)
	cli.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := cli.ReadMessage(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read deadline: %v", err)
	}
	// nobody reads the server's side: the write stalls on a full socket
	cli.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	var err error
	for i := 0; i < 1000 && err == nil; i++ {
		err = cli.WriteMessage(BinaryMessage, make([]byte, 1<<20))
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write deadline: %v", err)
	}
	_ = srv
}

// streamOnly hides a net.Conn's deadlines (a stream a custom RoundTripper
// hands over): timers stand in.
type streamOnly struct{ c net.Conn }

func (s streamOnly) Read(p []byte) (int, error)  { return s.c.Read(p) }
func (s streamOnly) Write(p []byte) (int, error) { return s.c.Write(p) }
func (s streamOnly) Close() error                { return s.c.Close() }

func TestDeadlinesWithoutNetConn(t *testing.T) {
	t.Parallel()
	_, cli, raw := pair(t, 0)
	c := newConn(streamOnly{raw}, nil, nil, false, 0, "")
	_ = cli
	c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := c.ReadMessage(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read deadline by timer: %v", err)
	}
	c2 := newConn(streamOnly{raw}, nil, nil, false, 0, "")
	c2.SetReadDeadline(time.Now().Add(time.Hour))
	c2.SetReadDeadline(time.Time{}) // cleared: no timer fires
	c2.closeConn()
}

func TestConcurrentWrites(t *testing.T) {
	t.Parallel()
	srv, cli, _ := pair(t, 0)
	const writers, each = 8, 200
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				msg := bytes.Repeat([]byte{byte(w)}, 100+i*37%3000)
				binary.BigEndian.PutUint32(msg, uint32(w<<16|i))
				if err := cli.WriteMessage(BinaryMessage, msg); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	next := make([]int, writers)
	for range writers * each {
		_, msg, err := srv.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		id := binary.BigEndian.Uint32(msg)
		w, i := int(id>>16), int(id&0xffff)
		if i != next[w] || len(msg) != 100+i*37%3000 || msg[len(msg)-1] != byte(w) {
			t.Fatalf("writer %d message %d (want %d), %d bytes: interleaved", w, i, next[w], len(msg))
		}
		next[w]++
	}
	wg.Wait()
}

func TestHandshakes(t *testing.T) {
	t.Parallel()
	var errs []string
	var mu sync.Mutex
	srv := echoServer(t, &UpgradeOptions{
		Subprotocols: []string{"b", "a"},
		CheckOrigin:  func(r *http.Request) bool { return r.Header.Get("Origin") != "https://evil.example" },
		Header:       http.Header{"X-Hello": {"there"}},
		Error: func(w http.ResponseWriter, r *http.Request, status int, reason string) {
			mu.Lock()
			errs = append(errs, reason)
			mu.Unlock()
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":%q}`, reason)
		},
	})
	// subprotocols: the server's preference among those offered
	c, resp, err := Dial(context.Background(), wsURL(srv), http.Header{"X-Custom": {"1"}, "Host": {"named.test"}}, &DialOptions{Subprotocols: []string{"a", "b"}})
	if err != nil || c.Subprotocol() != "b" || resp.Header.Get("X-Hello") != "there" {
		t.Fatalf("dial: %v, proto %q, %v", err, c.Subprotocol(), resp.Header)
	}
	if h := resp.Request.Host; h != "named.test" || resp.Request.Header.Get("X-Custom") != "1" {
		t.Fatalf("the handshake asked for host %q, headers %v", h, resp.Request.Header)
	}
	c.Close()
	// a plain GET, an old version, a refused origin: answered through Error
	r, _ := http.Get(srv.URL)
	if r.StatusCode != 400 {
		t.Fatalf("a plain GET: %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "8")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusUpgradeRequired || r.Header.Get("Sec-WebSocket-Version") != "13" {
		t.Fatalf("version 8: %d %v", r.StatusCode, r.Header)
	}
	_, resp, err = Dial(context.Background(), wsURL(srv), http.Header{"Origin": {"https://evil.example"}}, nil)
	if !errors.Is(err, ErrBadHandshake) || resp.StatusCode != 403 {
		t.Fatalf("a refused origin: %v", err)
	}
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), "origin") {
		t.Fatalf("the refusal's body: %q", b)
	}
	mu.Lock()
	if len(errs) != 3 {
		t.Fatalf("Error saw %q", errs)
	}
	mu.Unlock()
	// not a WebSocket server at all
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if _, resp, err := Dial(context.Background(), wsURL(plain), nil, nil); !errors.Is(err, ErrBadHandshake) || resp.StatusCode != 404 {
		t.Fatalf("a 404: %v", err)
	}
	if _, _, err := Dial(context.Background(), "ftp://x", nil, nil); err == nil {
		t.Fatal("dialed ftp")
	}
}

// the handshake's context bounds the handshake only
func TestDialContext(t *testing.T) {
	t.Parallel()
	srv := echoServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	c, _, err := Dial(ctx, wsURL(srv), nil, &DialOptions{Client: &http.Client{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancel()
	time.Sleep(20 * time.Millisecond)
	if err := c.WriteMessage(TextMessage, []byte("still here")); err != nil {
		t.Fatal(err)
	}
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "still here" {
		t.Fatalf("after the context ended: %q %v", msg, err)
	}
	// a server that never answers the handshake: the context ends it
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := Dial(ctx, "ws://"+ln.Addr().String()+"/", nil, nil); err == nil {
		t.Fatal("a silent server's handshake succeeded")
	}
}

// TLS: wss:// through a client that trusts the test server
func TestTLS(t *testing.T) {
	t.Parallel()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_, msg, _ := c.ReadMessage()
		c.WriteMessage(BinaryMessage, msg)
	}))
	srv.EnableHTTP2 = true // ALPN offers h2: an upgrade must stay on HTTP/1.1
	srv.StartTLS()
	defer srv.Close()
	c, _, err := Dial(context.Background(), "wss"+strings.TrimPrefix(srv.URL, "https"), nil, &DialOptions{Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.WriteMessage(BinaryMessage, pattern(200000))
	if _, msg, err := c.ReadMessage(); err != nil || !bytes.Equal(msg, pattern(200000)) {
		t.Fatalf("over TLS: %d %v", len(msg), err)
	}
}

// buffered bytes the client sent right behind its handshake aren't lost
func TestServerKeepsEarlyFrames(t *testing.T) {
	t.Parallel()
	srv := echoServer(t, nil)
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var frame bytes.Buffer
	early := newConn(nopRWC{&frame}, nil, nil, false, 0, "")
	early.writeFrame(true, TextMessage, []byte("early"))
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n%s", frame.Bytes())
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("handshake: %v %+v", err, resp)
	}
	c := newConn(conn, conn, br, false, 0, "")
	if _, msg, err := c.ReadMessage(); err != nil || string(msg) != "early" {
		t.Fatalf("the early frame: %q %v", msg, err)
	}
}

type nopRWC struct{ io.ReadWriter }

func (nopRWC) Close() error { return nil }
