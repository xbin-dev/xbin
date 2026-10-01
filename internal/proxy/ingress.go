package proxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
)

// The ingress last hop (plans/ingress.md ING-5): forward an admitted public
// request to the routed tile's backend socket. The route table + path
// allowlist upstream (ingress.HTTPHandler) decided WHAT may pass; this
// enforces what the backend sees — the anonymous `ingress` principal, on the
// tile's own paths, with every credential and spoofable identity stripped.

// HeaderIngressHost carries the public hostname the request arrived on (the
// Host header is also preserved; this survives any client-set Host games).
const HeaderIngressHost = "X-XBin-Ingress-Host"

// ForwardIngress proxies one admitted public request to rt's backend. The
// runtime listener passes viaTerminator=false (xbind saw the client
// directly, so it stamps X-Forwarded-*); a terminator tile's forward socket
// passes true (the terminator already stamped them and its RemoteAddr is a
// meaningless unix peer).
//
// Public traffic reaches only the tile's primary (D127d): rt.Component is an
// exact tile path, looked up as it is (a '+' in it is never a deployment
// qualifier), and Ensure and Track resolve the primary at the runner's
// funnel (09-fabric F3), so a tile's non-primary deployments receive no
// ingress, whichever deployment is primary. Nothing here names a
// deployment: the request carries no X-XBin-Deployment (it is stripped
// with every inbound X-XBin-*), and the response is the primary's.
func (px *Proxy) ForwardIngress(w http.ResponseWriter, r *http.Request, rt ingress.Route, viaTerminator bool) {
	comp, ok := px.Reg.Component(rt.Component)
	if !ok || !comp.HasBackend() || comp.IsTemplate() {
		http.Error(w, "this site is not being served right now", http.StatusServiceUnavailable)
		return
	}
	if state := px.Reg.LifecycleState(comp.Path); state != registry.StateEnabled {
		http.Error(w, "this site is not being served right now", http.StatusServiceUnavailable)
		return
	}

	// The ingress principal, structurally: no inbound X-XBin-* survives (an
	// outside client or a compromised terminator can't claim an identity),
	// and the workspace session cookie never reaches a tile backend (same
	// apex domain would otherwise leak it to whatever the tile runs).
	for k := range r.Header {
		if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Xbin-") {
			r.Header.Del(k)
		}
	}
	stripCookie(r, auth.CookieName)
	stripCookie(r, auth.SessionCookieHostName)
	r.Header.Set(HeaderFrom, auth.IngressFrom)
	r.Header.Set(HeaderIngressHost, rt.Host)

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	gen, err := px.Runner.EnsureGen(ctx, comp)
	if err != nil {
		// Build/spawn detail stays inside the workspace — anonymous clients
		// get a plain 502.
		http.Error(w, "this site is not available right now", http.StatusBadGateway)
		return
	}
	release := px.Runner.Track(comp.Path)
	defer release()

	outQuery := r.URL.Query()
	outQuery.Del("frame") // never let the browser-auth credential shape leak inward
	rawQuery := outQuery.Encode()

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "xbin"
			pr.Out.URL.Path = r.URL.Path // already cleaned by ingress.HTTPHandler
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = rawQuery
			pr.Out.Host = pr.In.Host // the tile sees its public hostname
			if !viaTerminator {
				pr.SetXForwarded()
			}
			if u := pr.In.Header.Get("Upgrade"); u != "" {
				pr.Out.Header.Set("Connection", "Upgrade")
				pr.Out.Header.Set("Upgrade", u)
			}
		},
		// A request the generation's retirement cut off goes to the
		// primary's generation now (rerouting).
		Transport: &rerouting{px: px, gen: gen, again: func() (generation, error) {
			return px.Runner.EnsureGen(ctx, comp)
		}},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "this site hit an internal error", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

// stripCookie removes one cookie by name, keeping the tile's own cookies —
// public visitors may well carry app-level sessions for the tile itself.
// stripCookies drops the named cookies, leaving the Cookie header untouched
// when none is there.
func stripCookies(r *http.Request, names ...string) {
	for _, c := range r.Cookies() {
		if slices.Contains(names, c.Name) {
			for _, n := range names {
				stripCookie(r, n)
			}
			return
		}
	}
}

func stripCookie(r *http.Request, name string) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, c := range cookies {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
}
