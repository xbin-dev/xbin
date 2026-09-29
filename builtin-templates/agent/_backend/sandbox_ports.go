// sandbox_ports.go — live previews (D135): preview_port shows the human a
// page a program in the conversation's sandbox serves, live, and
// GET|POST|… /runs/{id}/live/{sbx}/{port}/{path…} serves it — the manager's
// ports route (docs/sandbox-manager.md §Ports), proxied for a participant
// of the run.
//
// What the viewer's browser gets is untrusted: whatever the sandbox serves.
// So every answer carries this tile's own headers, never the sandbox's:
//
//   - Content-Security-Policy: sandbox allow-scripts allow-forms — an
//     opaque origin even opened on its own: no cookies, no storage, no
//     same-origin reach into the tile or xbind. No frame-ancestors: the pane
//     that frames it is itself an opaque origin (a sandboxed tile frame),
//     which no source expression matches — 'self' blocked it in Chromium
//     (test/live-policy.mjs). The path ticket (bound to the viewer's login
//     and address) is what keeps other sites from loading it;
//   - Referrer-Policy: no-referrer, Cache-Control: no-store,
//     X-Content-Type-Options: nosniff;
//   - of the sandbox's headers only content ones pass (liveKeep): never
//     Set-Cookie, Clear-Site-Data, NEL/Report-To, HSTS, Alt-Svc, CORS,
//     WWW-Authenticate or X-XBin-*.
//
// Nothing of the viewer's goes the other way: their cookies, credentials,
// X-XBin-* identity, Referer and forwarding headers are dropped; the agent
// calls the manager as the person who bound the sandbox (sandboxUse, the
// same check every sandbox tool makes). The page reaches this route through
// an xbind path ticket its pane mints (docs/auth.md §Path tickets), so its
// relative URLs resolve below the prefix: a path-prefix proxy, nothing is
// rewritten.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() { sandboxToolNames["preview_port"] = true }

// liveCSP is every live answer's policy (API.md §Live previews).
const liveCSP = "sandbox allow-scripts allow-forms"

// previewPortDesc's first line is pinned (the tool-description audit).
const previewPortDesc = "Show the human a LIVE page served by a program in your sandbox (e.g. python3 -m http.server 8000) — scripts run, in an isolated frame. " +
	"Start the server first as a background job (bash with background:true), listening on 127.0.0.1 or 0.0.0.0 in the sandbox. " +
	"The page is served below a path prefix: use RELATIVE URLs for its scripts, styles and fetches (./app.js, not /app.js); nothing is rewritten. " +
	"It runs with no cookies or storage, can't reach this workspace, and loads anything external from the viewer's browser. " +
	"It stays live while the server runs; the human can reload it. Only people taking part in this conversation can open it."

// sandboxPortSpecs is preview_port, where a sandbox is bound.
func sandboxPortSpecs(cfg Config) []toolSpec {
	if !sandboxToolsOn(cfg) {
		return nil
	}
	return []toolSpec{{Type: "function", Function: funcDef{
		Name:        "preview_port",
		Description: previewPortDesc,
		Parameters: obj([]string{"port"}, map[string]any{
			"port": intProp("the TCP port the server listens on in the sandbox (1-65535)"),
			"path": strProp("the page to open, relative to the server's root (default /), e.g. /index.html or /report/?q=1"),
		}),
	}}}
}

// toolPreviewPort is preview_port: the manager must serve ports, something
// must answer on the port (probed once, as the binder), then a live step
// shows it to the human.
func (ag *Agent) toolPreviewPort(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	port := toInt(args["port"])
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("port must be a number from 1 to 65535")
	}
	p, q, err := livePagePath(str(args["path"]))
	if err != nil {
		return "", err
	}
	use, err := ag.sandboxUse(ctx, rootOf(run), cfg, "")
	if err != nil {
		return "", err
	}
	if !hasStr(use.Hello.Caps, "ports") {
		return "", fmt.Errorf("this sandbox's manager doesn't serve ports, so no live preview here (it or xbind predates them) — render_html shows a static snapshot of an HTML file instead")
	}
	status, ctype, err := probePort(ctx, use, port, p, q)
	if err != nil {
		return "", err
	}
	st := ag.db.journal(run.ID, "live", map[string]any{
		"sandbox": use.ID, "name": use.Box.Name, "port": port, "path": "/" + p + qsuffix(q),
	})
	if ag.eng != nil { // streamed at once: the pane opens while the turn goes on
		ag.eng.emitStep(ag.db, rootOf(run), st)
	}
	note := ""
	if status >= 400 {
		note = fmt.Sprintf(" The server answered %d for that page — check the path.", status)
	}
	return fmt.Sprintf("showing http://localhost:%d/%s%s (%d %s) live to the human in the preview pane.%s "+
		"Scripts run there; a root-absolute URL (/app.js) in the page won't load — make it relative. "+
		"It stays live while the server runs.", port, p, qsuffix(q), status, orStr(ctype, "no content type"), note), nil
}

func qsuffix(q string) string {
	if q == "" {
		return ""
	}
	return "?" + q
}

// livePagePath splits a model's page ("/a/b.html?x=1") into its escaped
// path below the root and its raw query. A scheme or host is refused: the
// page is the sandbox server's.
func livePagePath(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") || strings.HasPrefix(s, "//") {
		return "", "", fmt.Errorf("path is the page on your server, like /index.html — not a URL")
	}
	u, err := url.Parse("/" + strings.TrimPrefix(s, "/"))
	if err != nil || u.Host != "" {
		return "", "", fmt.Errorf("bad path %q", s)
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return "", "", fmt.Errorf("path %q has a . or .. segment", s)
		}
	}
	return strings.TrimPrefix(u.EscapedPath(), "/"), u.RawQuery, nil
}

// probePort asks the page once through the manager, as the binder: its
// status and type, or why nothing answered.
func probePort(ctx context.Context, use *sbxUse, port int, p, q string) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	u := use.Conn.M.URL + "/sbx/sandboxes/" + url.PathEscape(use.ID) + "/ports/" + strconv.Itoa(port) + "/" + p
	if q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, "", err
	}
	if use.Conn.User != "" {
		req.Header.Set("Sbx-User", use.Conn.User)
	}
	resp, err := sbxClient().Do(req)
	if err != nil {
		return 0, "", &sbxError{Provider: use.Conn.M.Provider, Refusal: "unreachable", Msg: err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		var b struct {
			Error   string `json:"error"`
			Refusal string `json:"refusal"`
		}
		// the contract's refusals (the manager's, the runtime's); anything
		// else is the server's own answer, whatever its status
		if json.Unmarshal(raw, &b) == nil && contractRefusals[b.Refusal] {
			e := &sbxError{Provider: use.Conn.M.Provider, Status: resp.StatusCode, Refusal: b.Refusal, Msg: b.Error}
			if b.Refusal == "not-listening" {
				return 0, "", fmt.Errorf("nothing listens on port %d in the sandbox yet: start the server as a background job first (bash background:true), listening on 127.0.0.1 or 0.0.0.0 — then preview_port again (%v)", port, e)
			}
			return 0, "", e
		}
	}
	return resp.StatusCode, resp.Header.Get("Content-Type"), nil
}

// contractRefusals are the refusals a port route answers with.
var contractRefusals = map[string]bool{"not-listening": true, "not-found": true, "not-allowed": true, "state": true,
	"unsupported": true, "unavailable": true, "invalid": true}

// --- the live route ---------------------------------------------------------------

// liveRoutes is registered with the route table's (routes.go): a
// participant's view of a port in a sandbox bound to the run.
func liveRoutes() []routeDef {
	return []routeDef{{"/runs/{id}/live/{sbx}/{port}/{path...}", needParticipant, handleLive}}
}

// liveUses caches, for a few seconds, the sandboxUse of a run's sandbox:
// a page's many loads make one check, not one each.
var liveUses = struct {
	sync.Mutex
	m map[string]liveUse
}{m: map[string]liveUse{}}

type liveUse struct {
	use *sbxUse
	at  time.Time
}

const liveUseTTL = 5 * time.Second

func handleLive(w http.ResponseWriter, r *http.Request) {
	liveHeaders(w.Header())
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != r.PathValue("port") {
		xbin.WriteError(w, http.StatusBadRequest, "the port is a number from 1 to 65535")
		return
	}
	id := pathID(r)
	use, err := agent.liveUse(r.Context(), id, r.PathValue("sbx"))
	if err != nil {
		liveErr(w, err)
		return
	}
	// the escaped tail below /runs/{id}/live/{sbx}/{port}/
	segs := strings.SplitN(r.URL.EscapedPath(), "/", 7) // "", runs, id, live, sbx, port, tail
	tail := ""
	if len(segs) == 7 {
		tail = segs[6]
	}
	if dec, err := url.PathUnescape(tail); err != nil || hasDotSeg(dec) {
		xbin.WriteError(w, http.StatusBadRequest, "the path has a dot segment or a bad escape")
		return
	}
	target, err := url.Parse(use.Conn.M.URL + "/sbx/sandboxes/" + url.PathEscape(use.ID) + "/ports/" + strconv.Itoa(port) + "/" + tail)
	if err != nil {
		xbin.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	target.RawQuery = r.URL.RawQuery
	user := use.Conn.User
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			u := *target
			pr.Out.URL, pr.Out.Host = &u, u.Host
			h := pr.Out.Header
			if up := pr.In.Header.Get("Upgrade"); up != "" { // (Rewrite mode strips hop-by-hop headers)
				h.Set("Connection", "Upgrade")
				h.Set("Upgrade", up)
			}
			for k := range h {
				if liveDropIn(k) {
					delete(h, k)
				}
			}
			if user != "" {
				h.Set("Sbx-User", user)
			}
		},
		ModifyResponse: func(res *http.Response) error {
			res.Trailer = nil // trailers would bypass the allowlist
			keep := http.Header{}
			for k, v := range res.Header {
				if liveKeep[http.CanonicalHeaderKey(k)] || res.StatusCode == http.StatusSwitchingProtocols && liveUpgradeKeep[http.CanonicalHeaderKey(k)] {
					keep[k] = v
				}
			}
			res.Header = keep
			liveHeaders(res.Header)
			return nil
		},
		Transport:     sbxClient().Transport,
		FlushInterval: -1,
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil {
				liveHeaders(w.Header())
				xbin.WriteError(w, http.StatusBadGateway, "the sandbox manager didn't answer: "+err.Error())
			}
		},
	}
	rp.ServeHTTP(no1xx{w}, r)
}

// no1xx drops the informational answers a proxied server sends before its
// response (a 103's headers would reach the browser unfiltered: the
// ReverseProxy copies them before ModifyResponse runs); 101 passes (the
// WebSocket handshake).
type no1xx struct{ http.ResponseWriter }

func (w no1xx) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w no1xx) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// liveHeaders are this tile's, on every live answer (the proxied ones and
// the refusals alike).
func liveHeaders(h http.Header) {
	h.Set("Content-Security-Policy", liveCSP)
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Del("Set-Cookie")
}

// liveKeep are the sandbox server's response headers that pass: what a page
// needs to load, nothing that acts on the workspace's origin.
var liveKeep = map[string]bool{
	"Content-Type": true, "Content-Length": true, "Content-Encoding": true, "Content-Language": true,
	"Content-Range": true, "Accept-Ranges": true, "Etag": true, "Last-Modified": true, "Vary": true,
	"Date": true, "Location": true, "Retry-After": true, "Expires": true,
}

// liveUpgradeKeep pass on a 101 as well: the WebSocket handshake.
var liveUpgradeKeep = map[string]bool{
	"Upgrade": true, "Connection": true, "Sec-Websocket-Accept": true, "Sec-Websocket-Protocol": true,
}

// liveDropIn: a request header that never reaches the sandbox — the
// viewer's credentials and identity, where they came from.
func liveDropIn(k string) bool {
	switch k = http.CanonicalHeaderKey(k); k {
	case "Cookie", "Authorization", "Proxy-Authorization", "Sbx-User", "Referer", "Forwarded",
		"X-Real-Ip", "Sec-Websocket-Extensions":
		return true
	}
	return strings.HasPrefix(k, "X-Xbin-") || strings.HasPrefix(k, "X-Forwarded-")
}

func hasDotSeg(p string) bool {
	for _, s := range strings.Split(p, "/") {
		if s == "." || s == ".." {
			return true
		}
	}
	return false
}

func liveErr(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch sbxRefusal(err) {
	case "not-found", "not-attached", "none":
		status = http.StatusNotFound
	case "not-allowed":
		status = http.StatusForbidden
	case "unbound", "unsupported":
		status = http.StatusNotImplemented
	}
	xbin.WriteError(w, status, err.Error())
}

// liveUse is the use of run's sandbox sbx (its id at the manager), checked
// as a sandbox tool checks it — the binding, the class, the manager, the
// binder's right — and cached for liveUseTTL.
func (ag *Agent) liveUse(ctx context.Context, runID int64, sbx string) (*sbxUse, error) {
	key := strconv.FormatInt(runID, 10) + "\x00" + sbx
	liveUses.Lock()
	if e, ok := liveUses.m[key]; ok && time.Since(e.at) < liveUseTTL {
		liveUses.Unlock()
		return e.use, nil
	}
	liveUses.Unlock()
	run, err := ag.db.getRun(runID)
	if err != nil {
		return nil, &sbxError{Refusal: "not-found", Msg: "no such run"}
	}
	cfg, err := ag.db.runConfig(runID)
	if err != nil {
		return nil, err
	}
	ref := ""
	for _, b := range append(func() []SandboxBinding {
		if cfg.Sandbox != nil {
			return []SandboxBinding{*cfg.Sandbox}
		}
		return nil
	}(), cfg.Attached...) {
		if _, id, ok := splitSandboxRef(b.Ref); ok && id == sbx {
			ref = b.Ref
			break
		}
	}
	if ref == "" {
		return nil, &sbxError{Refusal: "not-attached", Msg: "that sandbox isn't bound to this conversation any more"}
	}
	use, err := ag.sandboxUse(ctx, rootOf(run), cfg, ref)
	if err != nil {
		return nil, err
	}
	if !hasStr(use.Hello.Caps, "ports") {
		return nil, &sbxError{Refusal: "unsupported", Msg: "the sandbox manager doesn't serve ports"}
	}
	liveUses.Lock()
	for k, e := range liveUses.m {
		if time.Since(e.at) >= liveUseTTL {
			delete(liveUses.m, k)
		}
	}
	liveUses.m[key] = liveUse{use: use, at: time.Now()}
	liveUses.Unlock()
	return use, nil
}
