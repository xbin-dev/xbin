package xbin

// sandbox_tty.go — forwarding a manager's contract routes to its tile
// sandboxes, and relaying their terminals. The runtime's routes mirror the
// sandbox-manager contract, so a manager serves output reads, stdin,
// signals, resizes, files, tar and terminals by passing its own request
// through: Forward streams both bodies and tunnels a WebSocket upgrade byte
// for byte (the consumer's masked frames reach xbind unchanged, so frame
// counts and echo acks stay exact), with no WebSocket code of its own.
// DialTTY (sandbox_dialtty.go) is for a manager that drives a terminal
// itself.

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Forward passes a manager's own request through to this sandbox's runtime
// route sub ("execs/<id>/output", "files/content", "tar", "execs/<id>/tty",
// …), with the query q the manager chose — never the consumer's raw query.
// Each "/"-separated segment of sub is escaped; an empty, "." or ".."
// segment is refused (400 invalid) before anything is sent.
//
// It streams both bodies and copies the status and the headers
// (Content-Type, ETag, Content-Length, …), minus Set-Cookie and X-XBin-*.
// It drops the inbound Cookie, Authorization, Sbx-User, X-XBin-* and
// Sec-WebSocket-Extensions: the call carries this tile's credential, never
// the consumer's. A WebSocket upgrade is tunnelled byte for byte. It
// returns once the response (or the tunnel) is done. When xbind can't be
// reached it answers 503 unavailable in the contract's error shape.
//
//	mux.HandleFunc("GET /sbx/sandboxes/{id}/execs/{eid}/output", func(w http.ResponseWriter, r *http.Request) {
//		// … the manager's own checks: the consumer may see sandbox id …
//		q := url.Values{}
//		for _, k := range []string{"since", "max", "waitMs", "encoding"} {
//			if v := r.URL.Query().Get(k); v != "" {
//				q.Set(k, v)
//			}
//		}
//		sbx.Sandbox(id).Forward(w, r, "execs/"+r.PathValue("eid")+"/output", q)
//	})
func (b *Sandbox) Forward(w http.ResponseWriter, r *http.Request, sub string, q url.Values) {
	var segs []string
	if sub != "" {
		segs = strings.Split(sub, "/")
	}
	for i, s := range segs {
		esc, err := segment("route segment", s)
		if err != nil {
			WriteSandboxError(w, err)
			return
		}
		segs[i] = esc
	}
	path, err := b.route(strings.Join(segs, "/"))
	if err != nil {
		WriteSandboxError(w, err)
		return
	}
	b.s.forward(w, r, path, q)
}

// TTYOptions tune an attach to a tty exec. SessionID and SandboxID
// ([A-Za-z0-9._-]{1,64}) replace the exec's and the sandbox's ids in the
// session frame, so the consumer sees the manager's own ids. ForUser is the
// person attaching: the runtime refuses one with noTerminal (D88), at
// every attach.
type TTYOptions struct {
	SessionID string
	SandboxID string
	ForUser   string
}

func (o TTYOptions) values() url.Values {
	q := url.Values{}
	setNonEmpty(q, "sessionId", o.SessionID)
	setNonEmpty(q, "sandboxId", o.SandboxID)
	setNonEmpty(q, "forUser", o.ForUser)
	return q
}

// TTYStart starts a tty exec and attaches to it: Cmd (the login shell when
// empty) in Cwd, at Rows×Cols, as UID/GID, for ForUser; SessionID and
// SandboxID as in TTYOptions.
type TTYStart struct {
	Cwd       string
	Cmd       string
	Rows      int
	Cols      int
	UID       *int
	GID       *int
	ForUser   string
	SessionID string
	SandboxID string
}

func (o TTYStart) values() url.Values {
	q := TTYOptions{SessionID: o.SessionID, SandboxID: o.SandboxID, ForUser: o.ForUser}.values()
	setNonEmpty(q, "cwd", o.Cwd)
	setNonEmpty(q, "cmd", o.Cmd)
	if o.Rows > 0 {
		q.Set("rows", strconv.Itoa(o.Rows))
	}
	if o.Cols > 0 {
		q.Set("cols", strconv.Itoa(o.Cols))
	}
	if o.UID != nil {
		q.Set("uid", strconv.Itoa(*o.UID))
	}
	if o.GID != nil {
		q.Set("gid", strconv.Itoa(*o.GID))
	}
	return q
}

func setNonEmpty(q url.Values, k, v string) {
	if v != "" {
		q.Set(k, v)
	}
}

// RelayTTY relays the consumer's terminal WebSocket (r, a request the
// manager has checked) to tty exec execID: Forward to execs/<id>/tty with
// o as the query. The wire is /ws/term's (docs/protocol.md), end to end.
func (b *Sandbox) RelayTTY(w http.ResponseWriter, r *http.Request, execID string, o TTYOptions) {
	path, err := b.execRoute(execID, "tty")
	if err != nil {
		WriteSandboxError(w, err)
		return
	}
	b.s.forward(w, r, path, o.values())
}

// RelayNewTTY starts a tty exec (the login shell unless o.Cmd) and relays
// the consumer's terminal WebSocket to it: Forward to tty with o as the
// query.
func (b *Sandbox) RelayNewTTY(w http.ResponseWriter, r *http.Request, o TTYStart) {
	path, err := b.route("tty")
	if err != nil {
		WriteSandboxError(w, err)
		return
	}
	b.s.forward(w, r, path, o.values())
}

// forward is Forward to an escaped path below sandboxesURL.
func (s *Sandboxes) forward(w http.ResponseWriter, r *http.Request, path string, q url.Values) {
	target, err := url.Parse(sandboxesURL + path)
	if err != nil {
		WriteSandboxError(w, invalidf("%v", err))
		return
	}
	target.RawQuery = q.Encode()
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			u := *target
			pr.Out.URL = &u
			pr.Out.Host = u.Host
			h := pr.Out.Header
			// Rewrite mode strips hop-by-hop headers before this runs;
			// protocol upgrades (WebSocket) need them restored explicitly.
			if up := pr.In.Header.Get("Upgrade"); up != "" {
				h.Set("Connection", "Upgrade")
				h.Set("Upgrade", up)
			}
			for _, k := range []string{"Cookie", "Authorization", "Sbx-User", "Sec-Websocket-Extensions"} {
				h.Del(k)
			}
			dropXBin(h)
		},
		ModifyResponse: func(res *http.Response) error {
			res.Header.Del("Set-Cookie")
			dropXBin(res.Header)
			return nil
		},
		Transport:     s.c.Transport,
		FlushInterval: -1, // long-polls and tar streams flow as they come
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() != nil {
				return // the consumer hung up
			}
			WriteSandboxError(w, &SandboxError{Status: http.StatusServiceUnavailable, Refusal: "unavailable",
				Message: "xbind didn't answer: " + err.Error()})
		},
	}
	rp.ServeHTTP(w, r)
}

// dropXBin removes every X-XBin-* header: xbind sets its own on a call, and
// a consumer's (or xbind's to the manager) never passes on.
func dropXBin(h http.Header) {
	for k := range h {
		if len(k) >= 7 && strings.EqualFold(k[:7], "X-XBin-") {
			delete(h, k)
		}
	}
}

// WriteSandboxError answers err in the contract's error shape, {error,
// refusal, state?, etag?, retryAfterMs?}: a *SandboxError from a typed call
// as the runtime gave it (so a manager passes a refusal on unchanged),
// anything else as a 500.
func WriteSandboxError(w http.ResponseWriter, err error) {
	var e *SandboxError
	if !errors.As(err, &e) {
		e = &SandboxError{Status: http.StatusInternalServerError, Message: err.Error()}
	}
	body := map[string]any{"error": e.Message}
	if e.Refusal != "" {
		body["refusal"] = e.Refusal
	}
	if e.State != "" {
		body["state"] = e.State
	}
	if e.ETag != "" {
		body["etag"] = e.ETag
	}
	if e.RetryAfter > 0 {
		body["retryAfterMs"] = e.RetryAfter.Milliseconds()
	}
	status := e.Status
	if status < 400 || status > 599 {
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
