// Command xbin-qa-proxy is a tiny front proxy for the QA box (deploy/qa).
//
// It listens on XBIN_QA_LISTEN (default 127.0.0.1:9988) and reverse-proxies to
// xbind at XBIN_QA_BACKEND (default http://127.0.0.1:8642). While xbind is not
// ready — down for an auto-update restart, or the updater has written a state
// file — it serves a small auto-refreshing "Updating…" page instead of the
// browser's connection-refused error. Readiness is GET <backend>/healthz == 200
// (which xbind only answers once it has finished booting).
//
// It deliberately does NOT add X-Forwarded-Proto: the console session cookie
// flips to Secure on X-Forwarded-Proto=https, which would break login over the
// plain-HTTP SSH tunnel. WebSocket upgrades are proxied (FlushInterval=-1).
package main

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	listen := env("XBIN_QA_LISTEN", "127.0.0.1:9988")
	backend := env("XBIN_QA_BACKEND", "http://127.0.0.1:8642")
	statePath := env("XBIN_QA_STATE", "/run/xbin-qa/state")

	bu, err := url.Parse(backend)
	if err != nil {
		fmt.Fprintln(os.Stderr, "xbin-qa-proxy: bad XBIN_QA_BACKEND:", err)
		os.Exit(2)
	}

	// Readiness poller: a red/green flag the request path reads without blocking.
	var ready atomic.Bool
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		hz := strings.TrimRight(backend, "/") + "/healthz"
		for {
			resp, err := client.Get(hz)
			ok := err == nil && resp.StatusCode == http.StatusOK
			if resp != nil {
				resp.Body.Close()
			}
			ready.Store(ok)
			time.Sleep(time.Second)
		}
	}()

	rp := httputil.NewSingleHostReverseProxy(bu)
	rp.FlushInterval = -1 // stream SSE/WS chunks immediately
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		// backend vanished mid-request (restart): show the page for a navigation,
		// otherwise just fail (a WS/XHR will retry on its own).
		if r.Header.Get("Upgrade") == "" {
			serveUpdating(w, statePath)
			return
		}
		http.Error(w, "backend unavailable", http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Show the updating page whenever xbind isn't ready, or an update is in
		// flight (the updater writes a state file for the whole window so the
		// progress is visible, not just during the few seconds of downtime).
		if r.Header.Get("Upgrade") == "" && (!ready.Load() || readState(statePath) != "") {
			serveUpdating(w, statePath)
			return
		}
		rp.ServeHTTP(w, r)
	})

	fmt.Fprintf(os.Stderr, "xbin-qa-proxy: %s -> %s (state %s)\n", listen, backend, statePath)
	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "xbin-qa-proxy:", err)
		os.Exit(1)
	}
}

// readState returns the updater's one-line status (empty when no update is in
// flight or the file is absent).
func readState(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func serveUpdating(w http.ResponseWriter, statePath string) {
	msg := readState(statePath)
	if msg == "" {
		msg = "Restarting…"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Retry-After", "2")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	// Not fmt.Fprintf: the template's CSS carries a literal % (border-radius:50%).
	_, _ = w.Write([]byte(strings.Replace(updatingHTML, "{{MSG}}", html.EscapeString(msg), 1)))
}

const updatingHTML = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="2">
<title>xbin — updating</title>
<style>
  :root { color-scheme: dark; }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center;
    background:#1b1e24; color:#d4d9e0; font:15px/1.5 system-ui, sans-serif; }
  .card { text-align:center; padding:32px 40px; }
  .spin { width:38px; height:38px; margin:0 auto 20px; border:3px solid #363c45;
    border-top-color:#f5a623; border-radius:50%; animation:spin 0.9s linear infinite; }
  @keyframes spin { to { transform:rotate(360deg); } }
  h1 { font-size:17px; font-weight:600; margin:0 0 8px; }
  p { margin:0; color:#868f9a; font:13px/1.5 ui-monospace, monospace; }
</style></head>
<body><div class="card">
  <div class="spin"></div>
  <h1>xbin is updating</h1>
  <p>{{MSG}}</p>
  <p style="margin-top:10px;opacity:.7">this page refreshes automatically</p>
</div></body></html>
`
