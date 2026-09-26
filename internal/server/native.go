package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/xbin-dev/xbin/internal/registry"
)

// NativeRuntimeVersion is the generation of native runtime documents this
// xbind serves — whoami's native.runtime, which tells the xbin app it may
// open tiles advertising a native entry natively (docs/elements.md §Native
// app UI). An xbind without it: every tile is a web tile.
const NativeRuntimeVersion = 1

// AppClientHeader marks a request made by the xbin app ("app/<version>").
// It changes one thing: injected documents carry the WebSocket origin
// (appWSOriginMeta), since a page the app loads from its custom scheme has
// none to derive. Harmless when spoofed — it only names the host the client
// itself reached.
const AppClientHeader = "X-XBin-Client"

// nativeInfo is the /components `native` object of a tile with a native
// app UI.
type nativeInfo struct {
	Entry string `json:"entry"` // tile-relative module path, e.g. "native.js"
}

// nativeOf reports a component's native UI entry, nil when it has none.
// Trusted chrome never gets one: it acts as the signed-in human, which a
// frame-token runtime can't (the app opens chrome in the browser).
func (s *Server) nativeOf(c *registry.Component) *nativeInfo {
	if c == nil || c.Native == "" || !sandboxedFrame(c.Path, c) {
		return nil
	}
	return &nativeInfo{Entry: c.Native}
}

// nativeRuntimeRequest reports a GET/HEAD for a native runtime document
// (?native=1).
func nativeRuntimeRequest(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) && r.URL.Query().Get("native") == "1"
}

// serveNativeRoute handles a ?native=1 request on the /c/ plane, already
// authorized exactly like the tile's index.html. It reports false for
// requests that are not for a runtime document (a file URL with the query
// is served as usual).
func (s *Server) serveNativeRoute(w http.ResponseWriter, r *http.Request, cleaned string) bool {
	comp, rest, ok := s.Reg.Resolve(cleaned)
	isRoot := ok && rest == ""
	if !strings.HasSuffix(r.URL.Path, "/") {
		if !isRoot {
			return false
		}
		// The document imports its entry relative to the tile directory.
		http.Redirect(w, r, r.URL.Path+"/?"+r.URL.RawQuery, http.StatusMovedPermanently)
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !isRoot {
		http.Error(w, "no such tile", http.StatusNotFound)
		return true
	}
	ni := s.nativeOf(comp)
	if ni == nil {
		why := "this tile has no native app UI (no native.js, and no \"native\" in its xbin.json)"
		switch {
		case !sandboxedFrame(comp.Path, comp):
			why = "trusted chrome has no native runtime"
		case comp.NativeErr != "":
			why = comp.NativeErr
		}
		http.Error(w, why, http.StatusNotFound)
		return true
	}
	doc := nativeRuntimeDoc(comp.Path, s.headInjection(r, comp, comp.Path, nil), ni.Entry, r.URL.Query().Get("preview") == "1")
	s.documentHeaders(w, r, comp.Path, comp)
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(doc))
	}
	return true
}

// nativeRuntimeDoc is the xbind-generated document a native tile runs in
// (the xbin app loads it in a hidden WebView): the tile page's D4 injection
// — same identity, sandbox and window.xbin — plus the xbin-native marker, a
// viewport, and one module script that loads the template layer and then
// the tile's entry, relative to the tile directory. With preview, the
// reference renderer's host (/vendor/xb/preview-host.js — optional: a
// missing one is logged, not fatal) loads before the entry so a browser
// can draw what the app would.
func nativeRuntimeDoc(compPath, inject, entry string, preview bool) string {
	var b strings.Builder
	b.WriteString("<!doctype html>\n<html><head>\n<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s (native)</title>", htmlEscape(compPath))
	b.WriteString(inject)
	b.WriteString("<meta name=\"xbin-native\" content=\"1\">\n")
	if preview {
		b.WriteString("<meta name=\"xbin-native-preview\" content=\"1\">\n")
	}
	spec, _ := json.Marshal("./" + entry) // HTML-safe: < > & are \u-escaped
	b.WriteString("<script type=\"module\">\nimport '/vendor/xb-native.js';\n")
	if preview {
		b.WriteString("try { await import('/vendor/xb/preview-host.js'); } catch (e) { console.warn('[xbin] native preview host unavailable:', e); }\n")
	}
	fmt.Fprintf(&b, "await import(%s);\n</script>\n</head><body></body></html>\n", spec)
	return b.String()
}

// wsHost is a Host header safe to echo as a WebSocket origin: a DNS name,
// IPv4 or bracketed IPv6 address, and an optional port.
var wsHost = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)

// appWSOriginMeta is the xbin-ws-origin meta for a document the xbin app
// requested: the WebSocket origin of the host the app reached (wss behind
// TLS, including a TLS-terminating proxy that says so), which xbin-client
// prefers over location — a custom-scheme page has no ws origin to derive.
// Empty for browsers, so their documents are byte-for-byte unchanged.
func appWSOriginMeta(r *http.Request) string {
	if !strings.HasPrefix(r.Header.Get(AppClientHeader), "app/") || !wsHost.MatchString(r.Host) {
		return ""
	}
	scheme := "ws"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "wss"
	}
	return fmt.Sprintf("<meta name=\"xbin-ws-origin\" content=\"%s://%s\">\n", scheme, htmlEscape(r.Host))
}
