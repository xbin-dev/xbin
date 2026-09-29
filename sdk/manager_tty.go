package xbin

// manager_tty.go — a sandbox manager's terminals, for consumer tiles
// (docs/sandbox-manager.md §Terminals). A consumer's backend opens a
// terminal in one of a bound manager's sandboxes through xbind, with its
// instance credential (the binding's consumer role), naming the person it
// acts for in Sbx-User (asserted: the manager records it and doesn't verify
// it). It drives the terminal itself (DialManagerTTY) or relays it to its
// own page or app (RelayManagerTTY) — after checking that person may use
// the sandbox: the manager can't.
//
// Not to be confused with sandbox_tty.go: a MANAGER relaying its
// consumer's terminal on to xbind's runtime.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/sdk/ws"
)

// ManagerTTYOptions choose a terminal on a manager's sandbox: a new one
// (GET …/sbx/sandboxes/{id}/tty) or, with ExecID, an attach to a tty exec
// (GET …/sbx/sandboxes/{id}/execs/{eid}/tty).
type ManagerTTYOptions struct {
	// ExecID attaches to that tty exec: one a POST …/execs {"tty": true}
	// started, or an earlier terminal's session id. Empty: start one.
	ExecID string
	// Cmd runs on a new terminal (the sandbox's login shell when empty), in
	// Cwd (the manager's default when empty), Rows × Cols (the manager's
	// default when 0). An attach takes none of them — send a resize frame.
	Cmd  string
	Cwd  string
	Rows int
	Cols int
	// User is the person the consumer acts for, sent as Sbx-User: the
	// manager records it and doesn't verify it. "" is the consumer itself.
	// Check the person may use the sandbox before you open it for them.
	User string
	// Client dials the manager (nil: Client() — through the gateway, with
	// this instance's credential).
	Client *http.Client
}

// managerTTYMax bounds a message either way of a manager's terminal (a
// keystroke batch, a paste, an output chunk): over it the connection closes
// with 1009.
const managerTTYMax = 4 << 20

// managerTTYDial bounds RelayManagerTTY's handshake with the manager: a
// terminal on a stopped sandbox starts it first.
const managerTTYDial = 60 * time.Second

// sandboxIDRE is the contract's sandbox id grammar
// (docs/sandbox-manager.md §The sandbox).
var sandboxIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ManagerTTYURL is the WebSocket URL of a terminal on sandbox sandboxID of
// the manager at endpoint — the url of its binding (XBIN_IFACE_<SLOT>, or
// XBIN_IFACE_<SLOT>_URL: "http://xbin/api/apps/coding-sandbox"). It builds
// the contract's route from typed parts alone: a sandbox id outside the
// contract's grammar ([A-Za-z0-9][A-Za-z0-9._-]{0,63}), an exec id that
// isn't one path segment (empty, ".", "..", or with a "/" or a control
// character) or an attach given Cmd, Cwd, Rows or Cols is refused (a
// *SandboxError, invalid); every segment and query value is escaped here,
// so no id reaches another route. o.User and o.Client aren't part of it.
func ManagerTTYURL(endpoint, sandboxID string, o ManagerTTYOptions) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", invalidf("endpoint %s isn't a manager's url (http://xbin/api/<manager>)", quoteID(endpoint))
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", invalidf("endpoint %s isn't a manager's url (http://xbin/api/<manager>)", quoteID(endpoint))
	}
	if !sandboxIDRE.MatchString(sandboxID) {
		return "", invalidf("sandbox id %s isn't one (the contract's are [A-Za-z0-9][A-Za-z0-9._-]{0,63})", quoteID(sandboxID))
	}
	if o.Rows < 0 || o.Cols < 0 {
		return "", invalidf("rows and cols are positive (0: the manager's default)")
	}
	route := "/sbx/sandboxes/" + sandboxID + "/tty"
	q := url.Values{}
	if o.ExecID != "" {
		if o.ExecID == "." || o.ExecID == ".." || strings.ContainsFunc(o.ExecID, func(r rune) bool { return r == '/' || r < 0x20 || r == 0x7f }) {
			return "", invalidf("exec id %s isn't one path segment", quoteID(o.ExecID))
		}
		if o.Cmd != "" || o.Cwd != "" || o.Rows != 0 || o.Cols != 0 {
			return "", invalidf("an attach to exec %s takes no Cmd, Cwd, Rows or Cols (send a resize frame)", quoteID(o.ExecID))
		}
		route = "/sbx/sandboxes/" + sandboxID + "/execs/" + url.PathEscape(o.ExecID) + "/tty"
	} else {
		setNonEmpty(q, "cmd", o.Cmd)
		setNonEmpty(q, "cwd", o.Cwd)
		if o.Rows > 0 {
			q.Set("rows", strconv.Itoa(o.Rows))
		}
		if o.Cols > 0 {
			q.Set("cols", strconv.Itoa(o.Cols))
		}
	}
	s := u.Scheme + "://" + u.Host + strings.TrimSuffix(u.EscapedPath(), "/") + route
	if len(q) > 0 {
		s += "?" + q.Encode()
	}
	return s, nil
}

// DialManagerTTY opens a terminal on sandbox sandboxID of the manager at
// endpoint (ManagerTTYURL) as this consumer, for o.User, and returns the
// connection: /ws/term's wire (docs/protocol.md §The terminal wire) — binary
// frames of terminal bytes both ways, the session frame
// {"op":"session","id":<exec id>,…} first (the id attaches again, as
// ExecID), {"op":"resize","cols","rows"} and {"op":"ping","t"} from you,
// {"op":"exit","code"} at the end, then a close. Keep one goroutine
// reading. A refusal — the manager's (not-found, not-allowed, state,
// unsupported, …) or xbind's (no binding: 403) — is a *SandboxError; so is
// a route ManagerTTYURL refused. ctx bounds the handshake only.
func DialManagerTTY(ctx context.Context, endpoint, sandboxID string, o ManagerTTYOptions) (*ws.Conn, error) {
	u, err := ManagerTTYURL(endpoint, sandboxID, o)
	if err != nil {
		return nil, err
	}
	if o.User != "" && strings.ContainsFunc(o.User, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return nil, invalidf("user %s has a control character", quoteID(o.User))
	}
	hc := o.Client
	if hc == nil {
		hc = Client()
	}
	h := http.Header{}
	if o.User != "" {
		h.Set("Sbx-User", o.User)
	}
	c, resp, err := ws.Dial(ctx, u, h, &ws.DialOptions{Client: hc, MaxMessageSize: managerTTYMax})
	if err != nil {
		if resp != nil && resp.StatusCode >= http.StatusBadRequest {
			return nil, sandboxError(resp)
		}
		return nil, err
	}
	return c, nil
}

// RelayManagerTTY serves a person's terminal WebSocket (r, a request of
// your page or app that you have checked: the person may use this sandbox)
// with a terminal on sandbox sandboxID of the manager at endpoint —
// DialManagerTTY for o.User, then the upgrade of r, then every message
// relayed unchanged both ways (the /ws/term wire end to end: keystrokes and
// output, resize, ping and pong, the session and exit frames) until either
// end closes; the other is closed the same way (a lost manager: 1011, so a
// terminal reconnects). It returns when both are closed.
//
// Nothing of r reaches the manager — no header, no query, not its
// handshake: the relay dials anew, with this tile's credential and
// Sbx-User. A
// request that isn't a WebSocket handshake is answered 400 invalid, and a
// refusal (the manager's or xbind's) as it came, in the contract's error
// shape, both before anything is upgraded or started. Leaving doesn't end
// the command (the contract's terminals outlive their clients): DELETE
// …/execs/{id} does, or its own exit.
//
//	mux.HandleFunc("GET /runs/{id}/terminal", func(w http.ResponseWriter, r *http.Request) {
//		person, sb, ok := mayUseSandbox(w, r) // your checks, first
//		if !ok {
//			return
//		}
//		xbin.RelayManagerTTY(w, r, sb.Endpoint, sb.ID, xbin.ManagerTTYOptions{User: person, Cmd: "claude /login"})
//	})
func RelayManagerTTY(w http.ResponseWriter, r *http.Request, endpoint, sandboxID string, o ManagerTTYOptions) {
	if why := notHandshake(r); why != "" { // before anything starts at the manager
		WriteSandboxError(w, invalidf("%s", why))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), managerTTYDial)
	mc, err := DialManagerTTY(ctx, endpoint, sandboxID, o)
	cancel()
	if err != nil {
		var se *SandboxError
		if !errors.As(err, &se) {
			if r.Context().Err() != nil {
				return // the person hung up
			}
			se = &SandboxError{Status: http.StatusServiceUnavailable, Refusal: "unavailable",
				Message: "the sandbox manager didn't answer: " + err.Error()}
		}
		WriteSandboxError(w, se)
		return
	}
	pc, err := ws.Upgrade(w, r, &ws.UpgradeOptions{MaxMessageSize: managerTTYMax,
		Error: func(w http.ResponseWriter, _ *http.Request, status int, reason string) {
			WriteSandboxError(w, &SandboxError{Status: status, Refusal: "invalid", Message: reason})
		}})
	if err != nil {
		_ = mc.Close()
		return
	}
	relayTerminal(pc, mc)
}

// notHandshake says why r isn't a WebSocket handshake Upgrade takes ("" when
// it is): checked before the manager is dialled, so a bad request never
// leaves a terminal started there.
func notHandshake(r *http.Request) string {
	switch {
	case r.Method != http.MethodGet || !ws.IsUpgrade(r):
		return "a terminal is a WebSocket upgrade"
	case r.Header.Get("Sec-WebSocket-Version") != "13":
		return "WebSocket version 13 only"
	}
	if k, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key")); err != nil || len(k) != 16 {
		return "a bad Sec-WebSocket-Key"
	}
	return ""
}

// relayTerminal copies messages between a person's connection and the
// manager's until one ends, then closes the other the same way.
func relayTerminal(person, manager *ws.Conn) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		relayOneWay(manager, person, ws.CloseInternalServerErr, "the sandbox's terminal dropped")
	}()
	relayOneWay(person, manager, ws.CloseGoingAway, "")
	wg.Wait()
}

// relayOneWay copies src's messages to dst. When src ends, dst is closed
// with src's close code, or with lost and why when src was lost (no close
// frame, or a code no frame may carry). When a write to dst fails, dst's
// reader — the other way — sees how dst ended and closes src.
func relayOneWay(src, dst *ws.Conn, lost int, why string) {
	for {
		typ, msg, err := src.ReadMessage()
		if err != nil {
			code, reason := lost, why
			var ce *ws.CloseError
			if errors.As(err, &ce) {
				switch c := ce.Code; {
				case c == ws.CloseNoStatusReceived:
					code, reason = ws.CloseNormalClosure, ""
				case c >= 1000 && c <= 1003, c >= 1007 && c <= 1014, c >= 3000 && c <= 4999:
					code, reason = c, ce.Text
				}
			}
			_ = dst.CloseWith(code, reason)
			return
		}
		if err := dst.WriteMessage(typ, msg); err != nil {
			if !errors.Is(err, ws.ErrClosed) { // lost, not closing: make its reader see it
				_ = dst.Close()
			}
			return
		}
	}
}
