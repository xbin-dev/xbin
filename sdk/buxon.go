// Package buxon is the Go SDK for buxon component backends.
//
// A minimal backend:
//
//	func main() {
//		mux := http.NewServeMux()
//		mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
//			fmt.Fprintf(w, "hello, %s (role %s)\n", buxon.Caller(r).From, buxon.Caller(r).Role)
//		})
//		mux.Handle("POST /things", buxon.Role("writer", createThing))
//		buxon.Serve(mux)
//	}
//
// The runner injects everything via env: BUXON_SOCKET (where to listen),
// BUXON_COMPONENT (own path), BUXON_GATEWAY + BUXON_TOKEN (how to call other
// elements and buxon APIs), BUXON_RES_* (granted resources). Full builder
// docs: /docs/sdk.md in any buxon workspace.
package buxon

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

// Serve listens on the buxon-provided unix socket and blocks until SIGTERM,
// then drains gracefully (the runner allows 30 s; see docs/elements.md).
func Serve(h http.Handler) {
	sock := os.Getenv("BUXON_SOCKET")
	if sock == "" {
		fmt.Fprintln(os.Stderr, "buxon: BUXON_SOCKET not set (not running under buxond?)")
		os.Exit(1)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		fmt.Fprintln(os.Stderr, "buxon: listen:", err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: h}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "buxon: serve:", err)
		os.Exit(1)
	}
}

// Self returns this component's path (its identity).
func Self() string { return os.Getenv("BUXON_COMPONENT") }

// CallerInfo is the verified identity buxond attached to an inbound request.
type CallerInfo struct {
	From  string // "owner", a component path, or "buxon/cron"
	Role  string // role the caller was granted on this component
	Owner bool
}

// Caller returns the verified caller of an inbound request. Trustworthy
// because buxond strips inbound X-Buxon-* headers before injecting these.
func Caller(r *http.Request) CallerInfo {
	from := r.Header.Get("X-Buxon-From")
	return CallerInfo{From: from, Role: r.Header.Get("X-Buxon-Role"), Owner: from == "owner"}
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

// Client returns an *http.Client that calls through the buxon gateway with
// this instance's identity. Use URLs like:
//
//	resp, err := buxon.Client().Get("http://buxon/api/apps/calendar/events")
//
// Cross-element calls require a grant (docs/auth.md).
//
// There is deliberately NO overall timeout: long-running responses (SSE,
// chunked streams) run until either side closes. Bound individual calls
// with a request context:
//
//	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
//	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
//	resp, err := buxon.Client().Do(req)
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
			Transport: &gatewayTransport{base: tr, token: os.Getenv("BUXON_TOKEN")},
		}
	})
	return sharedClient
}

var (
	clientOnce   sync.Once
	sharedClient *http.Client
)

// GatewayDial opens a raw connection to the buxon gateway socket. Use it to
// speak protocols http.Client can't — e.g. WebSocket to another element
// with any WS library:
//
//	d := websocket.Dialer{NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
//		return buxon.GatewayDial(ctx)
//	}}
//	h := http.Header{"Authorization": {"Bearer " + os.Getenv("BUXON_TOKEN")}}
//	conn, _, err := d.DialContext(ctx, "ws://buxon/api/apps/other/stream", h)
func GatewayDial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, "unix", os.Getenv("BUXON_GATEWAY"))
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
// ("db" → env BUXON_RES_DB). Empty string when not granted.
func Resource(name string) string {
	key := "BUXON_RES_" + strings.ToUpper(strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, name))
	return os.Getenv(key)
}

// Secret reads a key from this component's private vault (docs/auth.md §vault).
func Secret(name string) (string, error) {
	resp, err := Client().Get("http://buxon/api/buxon/vault/" + Self() + "/" + name)
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
	resp, err := Client().Post("http://buxon/api/buxon/bus/publish", "application/json", bytes.NewReader(body))
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
