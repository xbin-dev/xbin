// Package proxy routes /api/<component-path>/… to component backends:
// long-running runtimes over their unix sockets (blue/green targets from the
// runner), cgi per-request. It enforces the gateway side of the RBAC model:
// strips inbound X-XBin-* headers, consults the policy, and injects the
// verified caller identity (plans/auth.md §3).
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/runner"
)

const (
	HeaderFrom = "X-XBin-From"
	HeaderRole = "X-XBin-Role"
)

// Policy decides whether principal p may call target, and at which role.
// Installed by the broker (phase 4); the default allows owner and
// self-calls only.
type Policy func(p auth.Principal, target *registry.Component) (role string, ok bool)

func DefaultPolicy(p auth.Principal, target *registry.Component) (string, bool) {
	if p.IsAdmin() {
		return "admin", true
	}
	if p.Component != "" && p.Component == target.Path {
		return "admin", true // an element is admin of itself
	}
	return "", false
}

type Proxy struct {
	Reg    *registry.Registry
	Runner *runner.Runner
	Hub    *events.Hub
	Policy Policy

	trMu       sync.Mutex
	transports map[string]*http.Transport // backend socket → pooled transport
	trJanitor  sync.Once
}

// transportFor returns the pooled transport for a backend socket. One
// Transport per socket, cached: previously a FRESH Transport was built per
// proxied request, so every request stranded its keep-alive connection in a
// garbage pool — the backend parked a goroutine + buffers per RPC, forever
// (neither side closed it). Pooling reuses connections; IdleConnTimeout (90s,
// under the SDK server's 120s IdleTimeout) reaps quiet ones. Sockets are
// per-generation (g<N>.sock), so the janitor evicts entries — closing their
// idle conns — once a generation's socket is gone.
func (px *Proxy) transportFor(sock string) *http.Transport {
	px.trMu.Lock()
	defer px.trMu.Unlock()
	if tr, ok := px.transports[sock]; ok {
		return tr
	}
	if px.transports == nil {
		px.transports = map[string]*http.Transport{}
	}
	px.trJanitor.Do(func() {
		go func() {
			for range time.Tick(2 * time.Minute) {
				px.sweepTransports()
			}
		}()
	})
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32, // every request targets the one "xbin" host
		IdleConnTimeout:     90 * time.Second,
	}
	px.transports[sock] = tr
	return tr
}

// sweepTransports drops transports whose backend socket no longer exists (the
// generation was torn down), closing their idle connections.
func (px *Proxy) sweepTransports() {
	px.trMu.Lock()
	defer px.trMu.Unlock()
	for sock, tr := range px.transports {
		if _, err := os.Stat(sock); err != nil {
			tr.CloseIdleConnections()
			delete(px.transports, sock)
		}
	}
}

func (px *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/")
	comp, endpoint, ok := px.Reg.Resolve(rest)
	if !ok {
		jsonErr(w, http.StatusNotFound, "no such component", "")
		return
	}
	if comp.IsTemplate() {
		jsonErr(w, http.StatusNotFound,
			fmt.Sprintf("%s is a template — instantiate it first (Tile Manager → New from template, or `bx template new`)", comp.Path), "")
		return
	}
	if !comp.HasBackend() {
		jsonErr(w, http.StatusNotFound,
			fmt.Sprintf("component %s has no backend (runtime %q)", comp.Path, comp.Manifest.Runtime), "")
		return
	}
	// Lifecycle gate (plans/lifecycle.md): a disabled/offloaded component's
	// backend must not spawn. 409 with the state so callers/the frame can show a
	// placeholder; offloaded also means its data isn't local until restored.
	if state := px.Reg.LifecycleState(comp.Path); state != registry.StateEnabled {
		w.Header().Set("X-XBin-Lifecycle", state)
		msg := fmt.Sprintf("component %s is %s", comp.Path, state)
		if registry.IsOffloaded(state) {
			msg += " — restore it to use (admin)"
		} else {
			msg += " — enable it to use (admin)"
		}
		jsonErr(w, http.StatusConflict, msg, "")
		return
	}

	p := auth.PrincipalOf(r)
	pol := px.Policy
	if pol == nil {
		pol = DefaultPolicy
	}
	role, allowed := pol(p, comp)
	if !allowed {
		jsonErr(w, http.StatusForbidden, fmt.Sprintf(
			"%s is not granted access to %s — declare it in \"uses\" and approve the grant (bx grant, or the grants panel)",
			p.From(), comp.Path), "")
		return
	}

	// Scrub any spoofed identity, then inject the verified one.
	r.Header.Del(HeaderFrom)
	r.Header.Del(HeaderRole)
	r.Header.Del(auth.FrameTokenHeader)
	r.Header.Set(HeaderFrom, p.From())
	r.Header.Set(HeaderRole, role)

	if comp.Manifest.Runtime == "cgi" {
		px.serveCGI(w, r, comp, endpoint)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	sock, err := px.Runner.Ensure(ctx, comp)
	if err != nil {
		var be *runner.BuildError
		if errors.As(err, &be) {
			jsonErr(w, http.StatusBadGateway, "backend build failed", be.Output)
			return
		}
		jsonErr(w, http.StatusBadGateway, err.Error(), "")
		return
	}

	// Hold the backend for the whole connection: SSE and WebSocket streams
	// block in rp.ServeHTTP below, and the idle reaper must not stop a
	// backend that is mid-stream.
	release := px.Runner.Track(comp.Path)
	defer release()

	// The ?frame= auth credential (browser WS attribution) is consumed
	// here; never forward it — the callee could replay it as the caller.
	outQuery := r.URL.Query()
	outQuery.Del("frame")
	rawQuery := outQuery.Encode()

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "xbin" // ignored by the unix transport
			pr.Out.URL.Path = "/" + endpoint
			pr.Out.URL.RawQuery = rawQuery
			pr.Out.Host = "xbin"
			// Rewrite mode strips hop-by-hop headers before this runs;
			// protocol upgrades (WebSocket) need them restored explicitly.
			if u := pr.In.Header.Get("Upgrade"); u != "" {
				pr.Out.Header.Set("Connection", "Upgrade")
				pr.Out.Header.Set("Upgrade", u)
			}
		},
		Transport:     px.transportFor(sock),
		FlushInterval: -1, // stream (SSE etc.)
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			jsonErr(w, http.StatusBadGateway, "backend error: "+err.Error(), "")
		},
	}
	rp.ServeHTTP(w, r)
}

// serveCGI executes the component's entry per request with CGI semantics —
// the zero-lifecycle runtime for scripts (docs/elements.md §runtimes).
func (px *Proxy) serveCGI(w http.ResponseWriter, r *http.Request, comp *registry.Component, endpoint string) {
	entry := comp.Manifest.Entry
	if entry == "" {
		entry = "backend/handler"
	}
	script := filepath.Join(comp.Dir, filepath.FromSlash(entry))
	if fi, err := os.Stat(script); err != nil || fi.IsDir() {
		jsonErr(w, http.StatusNotFound, fmt.Sprintf("cgi entry %s not found", entry), "")
		return
	}
	h := &cgi.Handler{
		Path: script,
		Dir:  comp.Dir,
		Root: "/api/" + comp.Path,
		Env: []string{
			"XBIN_COMPONENT=" + comp.Path,
			"XBIN_FROM=" + r.Header.Get(HeaderFrom),
			"XBIN_ROLE=" + r.Header.Get(HeaderRole),
		},
		InheritEnv: []string{"PATH", "HOME"},
	}
	h.ServeHTTP(w, r)
}

func jsonErr(w http.ResponseWriter, code int, msg, detail string) {
	docs := "/docs/protocol.md"
	if code == http.StatusForbidden {
		docs = "/docs/auth.md"
	}
	b := map[string]string{"error": msg, "docs": docs}
	if detail != "" {
		b["detail"] = detail
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(b)
}
