// Package xbin is the Go SDK for xbin component backends.
//
// A minimal backend:
//
//	func main() {
//		mux := http.NewServeMux()
//		mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
//			fmt.Fprintf(w, "hello, %s (role %s)\n", xbin.Caller(r).From, xbin.Caller(r).Role)
//		})
//		mux.Handle("POST /things", xbin.Role("writer", createThing))
//		xbin.Serve(mux)
//	}
//
// The runner injects everything via env: XBIN_SOCKET (where to listen),
// XBIN_COMPONENT (own path), XBIN_GATEWAY + XBIN_TOKEN (how to call other
// elements and xbin APIs), XBIN_RES_* (granted resources). Full builder
// docs: /docs/sdk.md in any xbin workspace.
package xbin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Serve listens on the xbin-provided unix socket and blocks until SIGTERM,
// then drains gracefully (the runner allows 30 s; see docs/elements.md).
func Serve(h http.Handler) {
	sock := os.Getenv("XBIN_SOCKET")
	if sock == "" {
		fmt.Fprintln(os.Stderr, "xbin: XBIN_SOCKET not set (not running under xbind?)")
		os.Exit(1)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "xbin: listen:", err)
		os.Exit(1)
	}
	srv := newServer(h)
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "xbin: serve:", err)
		os.Exit(1)
	}
}

// newServer builds the backend's HTTP server. IdleTimeout matters: xbind's
// proxy pools keep-alive connections, and without it every parked idle conn
// held a goroutine + buffers forever (one per RPC, permanently — measured in
// the wild). 120s outlives the proxy's 90s IdleConnTimeout, so the client
// side always closes first. Hijacked conns (WebSocket) are exempt from both
// timeouts, and there is no overall read/write timeout — SSE/chunked streams
// run as long as both sides want.
func newServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		IdleTimeout:       120 * time.Second,
		ReadHeaderTimeout: 30 * time.Second,
	}
}

// Self returns this component's path (its identity).
func Self() string { return os.Getenv("XBIN_COMPONENT") }

// CallerInfo is the verified identity xbind attached to an inbound request.
type CallerInfo struct {
	From  string // "owner", a component path, or "xbin/cron"
	Role  string // role the caller was granted on this component
	Owner bool
}

// Caller returns the verified caller of an inbound request. Trustworthy
// because xbind strips inbound X-XBin-* headers before injecting these.
func Caller(r *http.Request) CallerInfo {
	from := r.Header.Get("X-XBin-From")
	return CallerInfo{From: from, Role: r.Header.Get("X-XBin-Role"), Owner: from == "owner"}
}

// roleRank orders the conventional role names. Custom roles are exact-match.
func roleRank(role string) int {
	switch role {
	case "reader":
		return 1
	case "writer":
		return 2
	case "admin":
		return 3
	}
	return 0
}

// RoleSatisfies reports whether `have` satisfies `want` (admin ⊃ writer ⊃
// reader; custom names must match exactly).
func RoleSatisfies(have, want string) bool {
	if have == want {
		return true
	}
	hr, wr := roleRank(have), roleRank(want)
	return hr > 0 && wr > 0 && hr >= wr
}

// Role guards a handler: 403 unless the caller's granted role satisfies want.
func Role(want string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := Caller(r)
		if !RoleSatisfies(c.Role, want) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintf(w, `{"error":"role %q required (caller %s has %q)","docs":"/docs/auth.md"}`,
				want, c.From, c.Role)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// RoleFunc is Role for plain handler funcs.
func RoleFunc(want string, h http.HandlerFunc) http.Handler { return Role(want, h) }

// Client returns an *http.Client that calls through the xbin gateway with
// this instance's identity. Use URLs like:
//
//	resp, err := xbin.Client().Get("http://xbin/api/apps/calendar/events")
//
// Cross-element calls require a grant (docs/auth.md).
//
// There is deliberately NO overall timeout: long-running responses (SSE,
// chunked streams) run until either side closes. Bound individual calls
// with a request context:
//
//	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
//	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
//	resp, err := xbin.Client().Do(req)
//
// The returned client is shared (connection keep-alive across calls).
func Client() *http.Client {
	clientOnce.Do(func() {
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return GatewayDial(ctx)
			},
			MaxIdleConns:    16,
			IdleConnTimeout: 90 * time.Second,
		}
		sharedClient = &http.Client{
			Transport: &gatewayTransport{base: tr, token: os.Getenv("XBIN_TOKEN")},
		}
	})
	return sharedClient
}

var (
	clientOnce   sync.Once
	sharedClient *http.Client
)

// GatewayDial opens a raw connection to the xbin gateway socket. Use it to
// speak protocols http.Client can't — e.g. WebSocket to another element
// with any WS library:
//
//	d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
//		return xbin.GatewayDial(ctx)
//	}}
//	h := http.Header{"Authorization": {"Bearer " + os.Getenv("XBIN_TOKEN")}}
//	conn, _, err := d.DialContext(ctx, "ws://xbin/api/apps/other/stream", h)
func GatewayDial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "unix", os.Getenv("XBIN_GATEWAY"))
}

type gatewayTransport struct {
	base  http.RoundTripper
	token string
}

func (t *gatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

// Resource returns the DSN/path of a granted resource by its short name
// ("db" → env XBIN_RES_DB). Empty string when not granted.
func Resource(name string) string {
	key := "XBIN_RES_" + strings.ToUpper(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, name))
	return os.Getenv(key)
}

// Secret reads a key from this component's private vault (docs/auth.md §vault).
func Secret(name string) (string, error) {
	resp, err := Client().Get("http://xbin/api/xbin/vault/" + Self() + "/" + name)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("vault: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var v struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return "", err
	}
	return v.Value, nil
}

// Publish sends a message to a granted bus resource
// (e.g. Publish("res:apps/calendar/events", "created", ev)).
func Publish(resource, topic string, data any) error {
	body, err := json.Marshal(map[string]any{"resource": resource, "topic": topic, "data": data})
	if err != nil {
		return err
	}
	resp, err := Client().Post("http://xbin/api/xbin/bus/publish", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("bus publish: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}
