package ws

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
	"strings"
	"time"
)

// the RFC's GUID, hashed with a key into Sec-WebSocket-Accept
const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + acceptGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

// DialOptions tune Dial; the zero value (or nil) works.
type DialOptions struct {
	// Client sends the opening handshake. xbin.Client() reaches tiles and
	// xbind through the gateway with this instance's credential (URLs like
	// ws://xbin/api/apps/other/stream); a client with its own Transport
	// reaches anything else. Nil: http.DefaultClient. Its Timeout bounds
	// the handshake only; redirects aren't followed. Its transport must be
	// an *http.Transport, or pass the 101 response's writable body through.
	Client *http.Client
	// Subprotocols are offered in order (Sec-WebSocket-Protocol).
	Subprotocols []string
	// MaxMessageSize bounds a received message (0: DefaultMaxMessageSize;
	// negative: no limit).
	MaxMessageSize int64
}

// Dial opens a WebSocket connection to u (ws://, wss://, or the http(s)://
// equivalents), sending header with the handshake (a Host in it names the
// host asked for). ctx bounds the handshake, not the connection. The
// server's response comes back too — on a handshake the server refused
// (ErrBadHandshake) it carries the status and up to 4 KiB of the body.
func Dial(ctx context.Context, u string, header http.Header, opts *DialOptions) (*Conn, *http.Response, error) {
	if opts == nil {
		opts = &DialOptions{}
	}
	pu, err := url.Parse(u)
	if err != nil {
		return nil, nil, fmt.Errorf("ws: %w", err)
	}
	switch pu.Scheme {
	case "ws":
		pu.Scheme = "http"
	case "wss":
		pu.Scheme = "https"
	case "http", "https":
	default:
		return nil, nil, fmt.Errorf("ws: can't dial a %q URL", pu.Scheme)
	}
	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	if client.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, client.Timeout)
		defer cancel() // (the transport lets go of a switched connection's context)
	}
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	key := base64.StdEncoding.EncodeToString(nonce[:])

	// the connection the transport used: its deadlines become the Conn's
	var nc net.Conn
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) { nc = i.Conn }})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pu.String(), nil)
	if err != nil {
		return nil, nil, fmt.Errorf("ws: %w", err)
	}
	for k, vs := range header {
		switch http.CanonicalHeaderKey(k) {
		case "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol":
			continue // the handshake's own
		case "Host":
			if len(vs) > 0 {
				req.Host = vs[0] // (the client sends req.Host, never a Host header)
			}
			continue
		}
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	if len(opts.Subprotocols) > 0 {
		req.Header.Set("Sec-WebSocket-Protocol", strings.Join(opts.Subprotocols, ", "))
	}
	hc := *client
	hc.Timeout = 0 // a Timeout would cut the switched connection too; ctx bounds the handshake
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("ws: %w", err)
	}
	refuse := func(why string) (*Conn, *http.Response, error) {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(b))
		return nil, resp, fmt.Errorf("%w: %s", ErrBadHandshake, why)
	}
	switch {
	case resp.StatusCode != http.StatusSwitchingProtocols:
		return refuse(fmt.Sprintf("status %d", resp.StatusCode))
	case !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !headerHasToken(resp.Header, "Connection", "upgrade"):
		return refuse("the response doesn't upgrade to websocket")
	case resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key):
		return refuse("a wrong Sec-WebSocket-Accept")
	case resp.Header.Get("Sec-WebSocket-Extensions") != "":
		return refuse("an extension nobody asked for")
	}
	proto := resp.Header.Get("Sec-WebSocket-Protocol")
	if proto != "" && !slices.Contains(opts.Subprotocols, proto) {
		return refuse("a subprotocol nobody offered: " + proto)
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, resp, errors.New("ws: the client's transport can't switch protocols (an *http.Transport can)")
	}
	return newConn(rwc, nc, nil, false, opts.MaxMessageSize, proto), resp, nil
}

// headerHasToken: one of h[name]'s comma-separated tokens is token.
func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// UpgradeOptions tune Upgrade; the zero value (or nil) works.
type UpgradeOptions struct {
	// Subprotocols the server speaks, in its order of preference; the first
	// the client offered is agreed.
	Subprotocols []string
	// CheckOrigin decides whether a browser's Origin may connect. Nil
	// accepts every origin: behind xbind, which authenticates each call
	// before it reaches a backend (and a sandboxed page's Origin is "null").
	// A server of its own that trusts cookies should check it.
	CheckOrigin func(r *http.Request) bool
	// MaxMessageSize bounds a received message (0: DefaultMaxMessageSize;
	// negative: no limit).
	MaxMessageSize int64
	// Header is added to the 101 response.
	Header http.Header
	// Error answers a request that can't be upgraded (nil: http.Error with
	// the reason) — to answer in your API's error shape.
	Error func(w http.ResponseWriter, r *http.Request, status int, reason string)
}

// IsUpgrade reports whether r asks for a WebSocket.
func IsUpgrade(r *http.Request) bool {
	return headerHasToken(r.Header, "Connection", "upgrade") && strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// Upgrade switches r to WebSocket and takes over its connection (http's
// Hijacker). A request that isn't a WebSocket handshake — or one it
// refuses — is answered through opts.Error and returns an error; the handler
// then writes nothing more.
func Upgrade(w http.ResponseWriter, r *http.Request, opts *UpgradeOptions) (*Conn, error) {
	if opts == nil {
		opts = &UpgradeOptions{}
	}
	fail := func(status int, reason string) (*Conn, error) {
		if opts.Error != nil {
			opts.Error(w, r, status, reason)
		} else {
			http.Error(w, reason, status)
		}
		return nil, errors.New("ws: " + reason)
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	switch {
	case r.Method != http.MethodGet:
		return fail(http.StatusMethodNotAllowed, "a WebSocket handshake is a GET")
	case !IsUpgrade(r):
		return fail(http.StatusBadRequest, "not a WebSocket handshake (Connection: Upgrade, Upgrade: websocket)")
	case r.Header.Get("Sec-WebSocket-Version") != "13":
		w.Header().Set("Sec-WebSocket-Version", "13")
		return fail(http.StatusUpgradeRequired, "WebSocket version 13 only")
	}
	if b, err := base64.StdEncoding.DecodeString(key); err != nil || len(b) != 16 {
		return fail(http.StatusBadRequest, "a bad Sec-WebSocket-Key")
	}
	if opts.CheckOrigin != nil && !opts.CheckOrigin(r) {
		return fail(http.StatusForbidden, "this origin may not connect")
	}
	proto := ""
	offered := map[string]bool{}
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			offered[strings.TrimSpace(p)] = true
		}
	}
	for _, p := range opts.Subprotocols {
		if offered[p] {
			proto = p
			break
		}
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return fail(http.StatusInternalServerError, "can't take over the connection: "+err.Error())
	}
	_ = conn.SetDeadline(time.Time{}) // the server's own read/write timeouts don't apply to a WebSocket
	var b bytes.Buffer
	b.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ")
	b.WriteString(acceptKey(key))
	b.WriteString("\r\n")
	if proto != "" {
		b.WriteString("Sec-WebSocket-Protocol: " + proto + "\r\n")
	}
	extra := opts.Header.Clone()
	for _, k := range []string{"Upgrade", "Connection", "Sec-Websocket-Accept", "Sec-Websocket-Protocol", "Sec-Websocket-Extensions"} {
		delete(extra, k)
	}
	_ = extra.Write(&b)
	b.WriteString("\r\n")
	if _, err := conn.Write(b.Bytes()); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws: %w", err)
	}
	var br = brw.Reader
	if br.Buffered() == 0 {
		br = nil // (a fresh reader of our own size)
	}
	return newConn(conn, conn, br, true, opts.MaxMessageSize, proto), nil
}
