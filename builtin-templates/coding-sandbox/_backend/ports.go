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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// oldAgents are the sandboxes whose in-box agent answered a port connection
// "unsupported": it predates ports — the sandbox started under an older
// xbind, or from a VM image from before them. id → when (unix ms). Its view
// says restartNeeded until it starts again (the substrate's Started after
// that): a restart brings the current agent.
var oldAgents sync.Map

// noteOldAgent: a ports answer from the runtime was 501 unsupported.
func noteOldAgent(id string) { oldAgents.Store(id, time.Now().UnixMilli()) }

// agentPredatesPorts: the sandbox's current run was seen refusing ports.
func agentPredatesPorts(id string, info *xbin.SandboxInfo) bool {
	v, ok := oldAgents.Load(id)
	if !ok {
		return false
	}
	if info == nil || info.State != "running" || info.Started > v.(int64) {
		oldAgents.Delete(id) // stopped or started again since: the next run has today's agent
		return false
	}
	return true
}

// agentWatch passes a forward's answer on, noting a runtime refusal that
// says the sandbox's agent predates ports. Unwrap keeps WebSocket upgrades
// working (the reverse proxy hijacks through it).
type agentWatch struct {
	http.ResponseWriter
	code int
	body []byte
}

func (a *agentWatch) WriteHeader(code int) {
	if a.code == 0 && code >= 200 {
		a.code = code
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *agentWatch) Write(b []byte) (int, error) {
	if a.code == 0 {
		a.code = http.StatusOK
	}
	if a.code == http.StatusNotImplemented && len(a.body) < 4<<10 {
		a.body = append(a.body, b[:min(len(b), 4<<10-len(a.body))]...)
	}
	return a.ResponseWriter.Write(b)
}

func (a *agentWatch) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// sawOldAgent: the runtime answered 501 unsupported on a ports route — with
// ports offered (the runtime's caps say so), that is the sandbox's agent.
func (a *agentWatch) sawOldAgent() bool {
	var e struct {
		Refusal string `json:"refusal"`
	}
	return a.code == http.StatusNotImplemented && json.Unmarshal(a.body, &e) == nil && e.Refusal == "unsupported"
}

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
	aw := &agentWatch{ResponseWriter: w}
	f.Forward(aw, r, xbin.PortRoute(port, tail, r.URL.RawQuery), nil)
	if aw.sawOldAgent() {
		noteOldAgent(rec.ID)
	}
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

// --- the pages' port diagnostics ----------------------------------------------------

// portProbeRoutes are the pages' Ports rows (API.md §Ports): whether this
// manager offers ports and why not, and a probe of one port of a sandbox —
// its status, type, refusal and latency, never the page's body. The page
// can't use the contract's ports route for another consumer's sandbox (the
// operators' view) and a reader can't at all, so these check the viewer
// themselves: an operator, or a person the port proxy itself would admit on
// this page (its partition and person rules, with write access).
func (m *Manager) portProbeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ports/{id}", m.uiPorts)
	mux.HandleFunc("GET /ports/{id}/{port}", m.uiPortProbe)
}

// portsWhyNot says why this manager doesn't offer ports ("" when it does).
func (m *Manager) portsWhyNot(ctx context.Context) string {
	rt, err := m.runtime(ctx)
	switch {
	case err != nil:
		return "the sandbox runtime can't be reached: " + errText(err)
	case !contains(rt.Caps, "ports"):
		return "the sandbox runtime (xbind) doesn't serve ports: it predates them, or runs without isolation"
	}
	if _, ok := m.backend().Sandbox("x").(portForwarder); !ok {
		return "this manager's backend (" + m.config().backendName() + ") doesn't forward ports"
	}
	return ""
}

// uiRecord is {id} for the page's viewer, or the refusal answered.
func (m *Manager) uiRecord(w http.ResponseWriter, r *http.Request) (record, bool) {
	c := xbin.Caller(r)
	id := r.PathValue("id")
	m.mu.Lock()
	rec := m.recs[id]
	var cp record
	if rec != nil {
		cp = *rec
	}
	m.mu.Unlock()
	if rec == nil {
		fail(w, http.StatusNotFound, "not-found", "no such sandbox")
		return record{}, false
	}
	if operator(r) {
		return cp, true
	}
	pc := caller{from: c.From, user: c.User, verified: c.User != ""}
	if c.From != m.self || m.self == "" || !cp.visible(pc) {
		fail(w, http.StatusNotFound, "not-found", "no such sandbox")
		return record{}, false
	}
	if !c.UserCanWrite() || !cp.personOK(pc) {
		fail(w, http.StatusForbidden, "not-allowed", "a port of this sandbox is for the people who may use it (with write access to this page), and its operators")
		return record{}, false
	}
	return cp, true
}

// uiPorts: GET /ports/{id} — {offered, why?, restartNeeded}.
func (m *Manager) uiPorts(w http.ResponseWriter, r *http.Request) {
	rec, ok := m.uiRecord(w, r)
	if !ok {
		return
	}
	why := m.portsWhyNot(r.Context())
	out := map[string]any{"offered": why == ""}
	if why != "" {
		out["why"] = why
	}
	if in, err := m.backend().Get(r.Context(), rec.Runtime); err == nil && agentPredatesPorts(rec.ID, in) {
		out["restartNeeded"] = true
		out["why"] = "the sandbox's agent predates ports (it started under an older xbind, or from an older VM image): restart the sandbox"
	}
	writeJSON(w, http.StatusOK, out)
}

// portProbe is one probe's answer.
type portProbe struct {
	OK          bool   `json:"ok"`
	Status      int    `json:"status,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Refusal     string `json:"refusal,omitempty"`
	Error       string `json:"error,omitempty"`
	Path        string `json:"path"`
	Ms          int64  `json:"ms"`
}

// errStop ends a probe's copy once the answer's head is known.
var errStop = errors.New("probe: enough")

// probeWriter keeps a forward's status, type and (for an error) the start
// of its body; a success's body is never read on.
type probeWriter struct {
	h      http.Header
	code   int
	body   bytes.Buffer
	cancel context.CancelFunc
}

func (p *probeWriter) Header() http.Header { return p.h }
func (p *probeWriter) WriteHeader(code int) {
	if p.code == 0 && code >= 200 {
		p.code = code
	}
}
func (p *probeWriter) Write(b []byte) (int, error) {
	if p.code == 0 {
		p.code = http.StatusOK
	}
	if p.code < 400 {
		p.cancel() // the head is all a probe wants: stop the page's body (a stream would go on)
		return 0, errStop
	}
	if p.body.Len() < 4<<10 {
		p.body.Write(b[:min(len(b), 4<<10-p.body.Len())])
	}
	return len(b), nil
}

// uiPortProbe: GET /ports/{id}/{port}?path= — one GET of the page, as the
// port proxy would forward it.
func (m *Manager) uiPortProbe(w http.ResponseWriter, r *http.Request) {
	rec, ok := m.uiRecord(w, r)
	if !ok {
		return
	}
	ps := r.PathValue("port")
	port, err := strconv.Atoi(ps)
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != ps {
		fail(w, http.StatusBadRequest, "invalid", "port "+strconv.Quote(ps)+" must be a number from 1 to 65535")
		return
	}
	page := r.URL.Query().Get("path")
	u, err := url.Parse("/" + strings.TrimPrefix(page, "/"))
	if err != nil || u.Host != "" || strings.Contains(page, "://") {
		fail(w, http.StatusBadRequest, "invalid", "path is the page on the sandbox's server, like /index.html")
		return
	}
	out := portProbe{Path: u.EscapedPath()}
	if u.RawQuery != "" {
		out.Path += "?" + u.RawQuery
	}
	start := time.Now()
	answer := func() {
		out.Ms = time.Since(start).Milliseconds()
		writeJSON(w, http.StatusOK, out)
	}
	if why := m.portsWhyNot(r.Context()); why != "" {
		out.Refusal, out.Error = "unsupported", why
		answer()
		return
	}
	if rec.Overlay != "" {
		out.Refusal, out.Error = "state", "the sandbox is "+rec.Overlay
		answer()
		return
	}
	if err := m.runningNow(r, &rec); err != nil {
		var se *xbin.SandboxError
		out.Refusal, out.Error = "unavailable", strings.ReplaceAll(errText(err), rec.Runtime, rec.ID)
		if errors.As(err, &se) && se.Refusal != "" {
			out.Refusal = se.Refusal
		}
		answer()
		return
	}
	f, ok := m.box(rec).(portForwarder)
	if !ok {
		out.Refusal, out.Error = "unsupported", "this substrate has no ports"
		answer()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
	if err != nil {
		out.Refusal, out.Error = "invalid", err.Error()
		answer()
		return
	}
	pw := &probeWriter{h: http.Header{}, cancel: cancel}
	func() {
		defer func() { // the proxy aborts a copy the probe stopped (errStop) with ErrAbortHandler: that is the point
			if v := recover(); v != nil && v != http.ErrAbortHandler {
				panic(v)
			}
		}()
		f.Forward(pw, req, xbin.PortRoute(port, strings.TrimPrefix(u.EscapedPath(), "/"), u.RawQuery), nil)
	}()
	switch {
	case pw.code == 0:
		out.Refusal, out.Error = "unavailable", "no answer within 20 s"
	default:
		out.Status, out.ContentType, out.OK = pw.code, pw.h.Get("Content-Type"), pw.code < 400
		var e struct {
			Error   string `json:"error"`
			Refusal string `json:"refusal"`
		}
		if !out.OK && strings.HasPrefix(out.ContentType, "application/json") && json.Unmarshal(pw.body.Bytes(), &e) == nil && e.Refusal != "" {
			out.Refusal, out.Error = e.Refusal, strings.ReplaceAll(e.Error, rec.Runtime, rec.ID)
			if pw.code == http.StatusNotImplemented && e.Refusal == "unsupported" {
				noteOldAgent(rec.ID)
			}
		}
	}
	answer()
}
