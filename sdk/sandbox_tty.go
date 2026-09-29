package xbin

// sandbox_tty.go — forwarding a manager's contract routes to its tile
// sandboxes, and relaying their terminals. The runtime's routes mirror the
// sandbox-manager contract, so a manager serves output reads, stdin,
// signals, resizes, files, tar and terminals by passing its own request
// through: Forward, to a route built by a typed builder (ExecOutput,
// FilesRoute, TarRoute, …), streams both bodies and tunnels a WebSocket
// upgrade byte for byte (the consumer's masked frames reach xbind
// unchanged, so frame counts and echo acks stay exact), with no WebSocket
// code of its own.
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

// SandboxRoute is one of a sandbox's runtime sub-routes, for Forward. Only
// the builders below make one: each checks its id against the runtime's
// grammar (IsExecID) and escapes every segment itself, so an id a consumer
// supplied can only fail — it never reaches another route, exec or sandbox.
// A route whose id failed makes Forward answer 400 invalid without sending
// anything; so does the zero SandboxRoute. There is no free-form route.
type SandboxRoute struct {
	sub string // escaped, below the sandbox's own route
	err error  // why the builder refused it

	port     bool   // a PortRoute: its query is rawQuery, not Forward's q
	rawQuery string // a PortRoute's query, as the consumer sent it
}

// execSub is exec id's route, plus tail.
func execSub(id, tail string) SandboxRoute {
	e, err := execIDSeg(id)
	if err != nil {
		return SandboxRoute{err: err}
	}
	if tail != "" {
		e += "/" + tail
	}
	return SandboxRoute{sub: "execs/" + e}
}

// ExecRoute is exec id itself, execs/{id}: GET it, or DELETE it (kill it
// and forget it).
func ExecRoute(id string) SandboxRoute { return execSub(id, "") }

// ExecOutput is execs/{id}/output: GET a chunk of its output (?since=,
// max, waitMs, encoding).
func ExecOutput(id string) SandboxRoute { return execSub(id, "output") }

// ExecStdin is execs/{id}/stdin: POST bytes to its stdin (?eof=1).
func ExecStdin(id string) SandboxRoute { return execSub(id, "stdin") }

// ExecSignal is execs/{id}/signal: POST {signal, group}.
func ExecSignal(id string) SandboxRoute { return execSub(id, "signal") }

// ExecResize is execs/{id}/resize: POST {rows, cols}.
func ExecResize(id string) SandboxRoute { return execSub(id, "resize") }

// ExecTTY is execs/{id}/tty: the WebSocket attach (RelayTTY forwards to it
// with TTYOptions as the query).
func ExecTTY(id string) SandboxRoute { return execSub(id, "tty") }

// FilesOp is one of the runtime's file operations, files/{op}.
type FilesOp string

// The file operations (docs/protocol.md §Tile sandboxes).
const (
	FilesStat    FilesOp = "stat"    // GET ?path=
	FilesContent FilesOp = "content" // GET ?path=&offset=&length=; PUT ?path=&mode=&mkdirs=1&ifMatch=&ifNoneMatch=*
	FilesList    FilesOp = "list"    // GET ?path=&limit=
	FilesMkdir   FilesOp = "mkdir"   // POST {path, parents}
	FilesRemove  FilesOp = "remove"  // POST {path, recursive}
	FilesMove    FilesOp = "move"    // POST {from, to, overwrite}
)

// FilesRoute is files/{op}. An op that isn't one of the FilesOp constants
// (say a consumer's path segment, converted) makes a refused route.
func FilesRoute(op FilesOp) SandboxRoute {
	switch op {
	case FilesStat, FilesContent, FilesList, FilesMkdir, FilesRemove, FilesMove:
		return SandboxRoute{sub: "files/" + string(op)}
	}
	return SandboxRoute{err: invalidf("files/%s isn't a file operation", quoteID(string(op)))}
}

// TarRoute is tar: GET a tree as a tar (?path=&exclude=…), or PUT one
// (?path=&mkdirs=1).
func TarRoute() SandboxRoute { return SandboxRoute{sub: "tar"} }

// PortRoute is ports/{port}/{path}: any method, an HTTP proxy to a server
// listening on TCP port (1–65535) on the sandbox's own loopback, WebSocket
// upgrades included (the ports capability, D135; SandboxRuntime.Caps says
// whether this xbind has it). path is the server's path below the port as
// the consumer sent it, still escaped (a leading "/" optional): it goes on
// unchanged, so a consumer's path can only ever reach that port — a
// segment that decodes to "." or "..", a bad escape, or a raw "?" or "#"
// makes a refused route. rawQuery is the server's query, also unchanged:
// Forward sends it in place of its q, which must be nil. Relative URLs in
// the server's pages resolve under whatever prefix the manager serves the
// route at: it is a path-prefix proxy, nothing is rewritten.
func PortRoute(port int, path, rawQuery string) SandboxRoute {
	if port < 1 || port > 65535 {
		return SandboxRoute{err: invalidf("port %d is out of range (1-65535)", port)}
	}
	path = strings.TrimPrefix(path, "/")
	if strings.ContainsAny(path, "?#") || strings.ContainsAny(rawQuery, "#") {
		return SandboxRoute{err: invalidf("a port route's path is escaped: a raw ? or # has no place in it")}
	}
	for _, seg := range strings.Split(path, "/") {
		d, err := url.PathUnescape(seg)
		if err != nil || d == "." || d == ".." {
			return SandboxRoute{err: invalidf("a port route's path has a dot segment or a bad escape")}
		}
	}
	return SandboxRoute{sub: "ports/" + strconv.Itoa(port) + "/" + path, port: true, rawQuery: rawQuery}
}

// Forward passes a manager's own request through to this sandbox's runtime
// route rt (ExecOutput(eid), FilesRoute(xbin.FilesContent), TarRoute(), …),
// with the query q the manager chose — never the consumer's raw query. A
// route the builder refused (an id that fails its grammar: one with a "/",
// "..", "%2F", "?" or "#" among them) is answered 400 invalid, and nothing
// is sent.
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
//		eid := r.PathValue("eid")
//		if !xbin.IsExecID(eid) { // names no exec: the contract's not-found
//			xbin.WriteSandboxError(w, &xbin.SandboxError{Status: 404, Refusal: "not-found", Message: "no such exec"})
//			return
//		}
//		q := url.Values{}
//		for _, k := range []string{"since", "max", "waitMs", "encoding"} {
//			if v := r.URL.Query().Get(k); v != "" {
//				q.Set(k, v)
//			}
//		}
//		sbx.Sandbox(id).Forward(w, r, xbin.ExecOutput(eid), q)
//	})
func (b *Sandbox) Forward(w http.ResponseWriter, r *http.Request, rt SandboxRoute, q url.Values) {
	if rt.err == nil && rt.sub == "" {
		rt.err = invalidf("no route: build one with ExecOutput, FilesRoute, TarRoute, …")
	}
	if rt.err != nil {
		WriteSandboxError(w, rt.err)
		return
	}
	if rt.port && len(q) > 0 {
		WriteSandboxError(w, invalidf("a PortRoute carries its own query: Forward's q must be nil"))
		return
	}
	path, err := b.route(rt.sub)
	if err != nil {
		WriteSandboxError(w, err)
		return
	}
	if rt.port {
		b.s.forwardRaw(w, r, path, rt.rawQuery)
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
// manager has checked) to tty exec execID: Forward to ExecTTY(execID) with
// o as the query. The wire is /ws/term's (docs/protocol.md), end to end.
func (b *Sandbox) RelayTTY(w http.ResponseWriter, r *http.Request, execID string, o TTYOptions) {
	b.Forward(w, r, ExecTTY(execID), o.values())
}

// RelayNewTTY starts a tty exec (the login shell unless o.Cmd) and relays
// the consumer's terminal WebSocket to it: Forward to tty with o as the
// query.
func (b *Sandbox) RelayNewTTY(w http.ResponseWriter, r *http.Request, o TTYStart) {
	b.Forward(w, r, SandboxRoute{sub: "tty"}, o.values())
}

// forward is Forward to an escaped path below sandboxesURL.
func (s *Sandboxes) forward(w http.ResponseWriter, r *http.Request, path string, q url.Values) {
	s.forwardRaw(w, r, path, q.Encode())
}

// forwardRaw is forward with the query as it goes on the wire.
func (s *Sandboxes) forwardRaw(w http.ResponseWriter, r *http.Request, path, rawQuery string) {
	target, err := url.Parse(sandboxesURL + path)
	if err != nil {
		WriteSandboxError(w, invalidf("%v", err))
		return
	}
	target.RawQuery = rawQuery
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
