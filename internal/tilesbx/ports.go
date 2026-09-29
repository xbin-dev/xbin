package tilesbx

// ports.go — the ports capability (D135): ANY
// /sandboxes/{name}/ports/{port}/{path...}, an HTTP reverse proxy from the
// manager to a server listening on the sandbox's own loopback, WebSocket
// upgrades included. Each request is its own "port" connection to the
// sandbox's agent (proto.Hello{Kind: "port"}): the agent dials
// 127.0.0.1:{port} (else [::1]) inside the sandbox and splices. The
// direction is inbound only — xbind to the sandbox; nothing in the sandbox
// reaches xbind through it — and nothing of xbin's crosses: the manager's
// credential, cookies, X-XBin-* and forwarding headers are dropped on the
// way in, X-XBin-* and Set-Cookie on the way out.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// RefNotListening is the ports route's refusal when nothing accepted a
// connection on the port (502): the contract's shape, a refusal of its own.
const RefNotListening = "not-listening"

// portDialWait bounds opening a port connection: the agent's reply (its
// own dial of the loopback is bounded at 5 s per address).
var portDialWait = 15 * time.Second

// portHello bounds the agent's reply line.
const portReplyMax = 4 << 10

// ServePort answers ANY /sandboxes/{name}/ports/{port}/{path...}: the
// request, proxied to the sandbox's loopback port. The sandbox must be
// running — a stopped one has no server to reach, so it isn't started
// (409 state). 502 not-listening when nothing accepts on the port.
func (m *Manager) ServePort(w http.ResponseWriter, r *http.Request) {
	port, tail, err := portTarget(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	k, ok := m.portGate(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if _, ok := m.lookup(w, k, name); !ok {
		return
	}
	run, release, err := m.holdRunning(k, name)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer release() // the request (or its upgraded tunnel) holds off the idle stop
	a := run.client()
	if a == nil {
		writeErr(w, &Error{Refusal: RefState, State: StateStopping, Msg: fmt.Sprintf("sandbox %q stopped", name)})
		return
	}
	// Dial first, so a port nothing listens on is the contract's refusal
	// rather than the proxy's bare 502.
	c, err := a.Port(port, portDialWait)
	if err != nil {
		writeErr(w, err)
		return
	}
	var once bool
	proxyPort(w, r, port, tail, func() (net.Conn, error) {
		if once { // keep-alives are off: one request, one connection
			return nil, errors.New("one connection per request")
		}
		once = true
		return c, nil
	})
	if !once {
		c.Close()
	}
}

// portTarget reads {port} (1–65535) and the escaped tail below it; the tail
// keeps its encoding, but no segment of it may decode to "." or "..".
func portTarget(r *http.Request) (int, string, error) {
	ps := r.PathValue("port")
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != ps {
		return 0, "", refuse(RefInvalid, "port %q must be a number from 1 to 65535", ps)
	}
	// /sandboxes/{name}/ports/{port}/{tail}: the tail as the client sent it
	segs := strings.SplitN(r.URL.EscapedPath(), "/", 6) // "", sandboxes, name, ports, port, tail
	tail := ""
	if len(segs) == 6 {
		tail = segs[5]
	}
	for _, s := range strings.Split(tail, "/") {
		if d, err := url.PathUnescape(s); err != nil || d == "." || d == ".." {
			return 0, "", refuse(RefInvalid, "the path has a dot segment or a bad escape")
		}
	}
	return port, tail, nil
}

// portGate is manager()'s gate for the ports route: hygiene applies to the
// route's own segments ({name}, "ports", {port}) — the tail is the
// sandbox server's, forwarded as it came.
func (m *Manager) portGate(w http.ResponseWriter, r *http.Request) (Key, bool) {
	name := r.PathValue("name")
	for _, seg := range []string{name, r.PathValue("port")} {
		up := strings.ToUpper(seg)
		if seg == "." || seg == ".." || strings.Contains(seg, `\`) || strings.Contains(seg, "/") ||
			strings.Contains(up, "%2F") || strings.Contains(up, "%2E") || strings.Contains(up, "%5C") {
			writeErr(w, refuse(RefInvalid, "the path has a dot segment or an encoded /, . or \\ in a segment"))
			return Key{}, false
		}
	}
	if err := validName(name); err != nil {
		writeErr(w, err)
		return Key{}, false
	}
	p := auth.PrincipalOf(r)
	if !m.Manages(p) {
		writeErr(w, refuse(RefNotAllowed, msgNotManager))
		return Key{}, false
	}
	if !m.isolated {
		writeErr(w, refuse(RefUnsupported, msgIsolation))
		return Key{}, false
	}
	k, err := keyOf(p)
	if err != nil {
		writeErr(w, err)
		return Key{}, false
	}
	return k, true
}

// holdRunning holds k's sandbox's run, only while it is running now: no
// start, no wait.
func (m *Manager) holdRunning(k Key, name string) (*run, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.defs.get(k, name); !ok {
		return nil, nil, refuse(RefNotFound, "no sandbox %q", name)
	}
	b := m.boxLocked(k, name)
	if busy := busyLocked(name, b); busy != nil {
		return nil, nil, busy
	}
	if r := b.run; r != nil && b.state == StateRunning {
		if release, ok := m.holdLocked(r); ok {

			return r, release, nil
		}
	}
	return nil, nil, &Error{Refusal: RefState, State: b.state,
		Msg: fmt.Sprintf("sandbox %q is %s: nothing listens in a sandbox that isn't running (start it, then its server)", name, b.state)}
}

// Port opens a "port" connection to TCP port on the sandbox's loopback: the
// agent's reply first (wait bounds it), then the raw stream. Nothing
// listening is 502 not-listening; an agent that closes without a reply
// predates ports (501 unsupported).
func (a *agentClient) Port(port int, wait time.Duration) (net.Conn, error) {
	c, err := a.open(proto.Hello{Kind: "port", Port: port})
	if err != nil {
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(wait))
	br := bufio.NewReaderSize(c, portReplyMax)
	line, err := br.ReadSlice('\n')
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		if errors.Is(err, io.EOF) {
			return nil, refuse(RefUnsupported, "the sandbox's agent doesn't serve ports (it predates them): restart the sandbox")
		}
		return nil, &Error{Refusal: RefUnavailable, RetryAfter: time.Second, Msg: "the sandbox's agent didn't answer a port connection: " + err.Error()}
	}
	var rep proto.PortReply
	if err := json.Unmarshal(line, &rep); err != nil || (!rep.OK && rep.Error == "") {
		c.Close()
		return nil, refuse(RefUnavailable, "the sandbox's agent answered a port connection with %q", strings.TrimSpace(string(line)))
	}
	if !rep.OK {
		c.Close()
		why := "nothing accepts connections on port " + strconv.Itoa(port) + " in the sandbox"
		if !rep.Refused {
			why = "port " + strconv.Itoa(port) + " in the sandbox: " + rep.Error
		}
		return nil, &Error{Refusal: RefNotListening, Msg: why + " (a server must listen on 127.0.0.1 or ::1, or on all addresses)"}
	}
	return &bufConn{Conn: c, r: br}, nil
}

// bufConn reads through the reader that took the reply line.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

// CloseWrite passes a half-close on (a unix socket's).
func (b *bufConn) CloseWrite() error {
	if cw, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// portDrop are request headers that never reach a sandbox: xbin's
// credentials and identities, and where the request came from.
var portDrop = []string{"Authorization", "Proxy-Authorization", "Cookie", "Sbx-User",
	"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"}

// proxyPort proxies r to the sandbox server on port through dial's one
// connection: path "/"+tail (escaped as it came), r's raw query, Host
// localhost:{port} (what a dev server allows by default).
func proxyPort(w http.ResponseWriter, r *http.Request, port int, tail string, dial func() (net.Conn, error)) {
	host := "localhost:" + strconv.Itoa(port)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			u := &url.URL{Scheme: "http", Host: host, RawQuery: pr.In.URL.RawQuery}
			if p, err := url.PathUnescape("/" + tail); err == nil {
				u.Path = p
				if u.EscapedPath() != "/"+tail {
					u.RawPath = "/" + tail
				}
			}
			pr.Out.URL, pr.Out.Host = u, host
			h := pr.Out.Header
			if up := pr.In.Header.Get("Upgrade"); up != "" { // (Rewrite mode strips hop-by-hop headers)
				h.Set("Connection", "Upgrade")
				h.Set("Upgrade", up)
			}
			for _, k := range portDrop {
				h.Del(k)
			}
			dropXBinHeaders(h)
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Set-Cookie")
			dropXBinHeaders(res.Header)
			return nil
		},
		Transport: &http.Transport{
			DialContext:           func(context.Context, string, string) (net.Conn, error) { return dial() },
			DisableKeepAlives:     true,
			DisableCompression:    true,
			ResponseHeaderTimeout: 0, // a long-poll is the server's to end
		},
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the caller hung up
			}
			writeErr(w, &Error{Refusal: RefNotListening, Msg: fmt.Sprintf("the server on port %d in the sandbox didn't answer: %v", port, err)})
		},
	}
	rp.ServeHTTP(w, r)
}

// dropXBinHeaders removes every X-XBin-* header.
func dropXBinHeaders(h http.Header) {
	for k := range h {
		if len(k) >= 7 && strings.EqualFold(k[:7], "X-XBin-") {
			delete(h, k)
		}
	}
}
