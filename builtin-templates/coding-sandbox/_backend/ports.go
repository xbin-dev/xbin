package main

// ports.go — the contract's ports capability (D135): ANY
// /sbx/sandboxes/{id}/ports/{port}/{path…}, an HTTP proxy (WebSocket
// upgrades too) to a server on the sandbox's own loopback. The manager
// checks what exec checks — the consumer's partition, the person rules —
// and that the sandbox runs (a port route never starts one: its server
// would be gone anyway), then forwards the consumer's request to the
// runtime's ports route with the SDK's typed PortRoute: the consumer's
// escaped path and query go on unchanged below that one port, never
// anywhere else. Offered while the substrate serves it (runtime caps
// "ports") and its boxes can forward (the xbin backend's *xbin.Sandbox).

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// portForwarder is a Box that forwards to its runtime's routes: the xbin
// backend's *xbin.Sandbox. A backend whose boxes aren't doesn't offer
// ports.
type portForwarder interface {
	Forward(w http.ResponseWriter, r *http.Request, rt xbin.SandboxRoute, q url.Values)
}

func (m *Manager) portRoutes(x *http.ServeMux) {
	x.HandleFunc("/sbx/sandboxes/{id}/ports/{port}/{path...}", m.portProxy)
	// the port's root without its slash: xbind hands a backend its path
	// with no trailing slash, and the mux would otherwise answer a redirect
	// to a path of ours the consumer can't reach
	x.HandleFunc("/sbx/sandboxes/{id}/ports/{port}", m.portProxy)
}

// portsOffered: the substrate serves ports and its boxes forward.
func (m *Manager) portsOffered(rt *xbin.SandboxRuntime) bool {
	if rt == nil || !contains(rt.Caps, "ports") {
		return false
	}
	_, ok := m.backend().Sandbox("x").(portForwarder)
	return ok
}

func (m *Manager) portProxy(w http.ResponseWriter, r *http.Request) {
	if !m.hasCap(w, r, "ports", "ports") {
		return
	}
	ps := r.PathValue("port")
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != ps {
		fail(w, http.StatusBadRequest, "invalid", "port "+strconv.Quote(ps)+" must be a number from 1 to 65535")
		return
	}
	// the escaped tail below /sbx/sandboxes/{id}/ports/{port}/
	segs := strings.SplitN(r.URL.EscapedPath(), "/", 7)
	tail := ""
	if len(segs) == 7 {
		tail = segs[6]
	}
	rec, c, ok := m.find(w, r)
	if !ok {
		return
	}
	if c.lookOnly {
		fail(w, http.StatusForbidden, "not-allowed", "a port of a sandbox is for its consumers, not this page's readers")
		return
	}
	if rec.Overlay != "" {
		failState(w, "the sandbox is "+rec.Overlay, rec.Overlay)
		return
	}
	if err := m.runningNow(r, &rec); err != nil {
		writeErr(w, err, &rec)
		return
	}
	f, ok := m.box(rec).(portForwarder)
	if !ok {
		fail(w, http.StatusNotImplemented, "unsupported", "this substrate has no ports")
		return
	}
	f.Forward(w, r, xbin.PortRoute(port, tail, r.URL.RawQuery), nil)
}

// runningNow: the sandbox runs (confirmed within the live TTL, as ready
// does), else 409 state — never a start.
func (m *Manager) runningNow(r *http.Request, rec *record) error {
	ttl := m.LiveTTL
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	m.mu.Lock()
	fresh := time.Since(m.live[rec.ID]) < ttl
	m.mu.Unlock()
	if fresh {
		return nil
	}
	in, err := m.backend().Get(r.Context(), rec.Runtime)
	if err != nil {
		return err
	}
	if in.State != "running" {
		return &xbin.SandboxError{Status: http.StatusConflict, Refusal: "state", State: in.State,
			Message: "the sandbox is " + in.State + ": nothing listens in a sandbox that isn't running (start it, then its server)"}
	}
	m.mu.Lock()
	m.live[rec.ID] = time.Now()
	m.mu.Unlock()
	return nil
}
