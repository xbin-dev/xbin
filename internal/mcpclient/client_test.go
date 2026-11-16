package mcpclient

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/mcpclient/mcpfake"
	"github.com/xbin-dev/xbin/internal/webfetch"
)

// resolver answers names from a table, one answer list per lookup in turn
// (the last repeats).
type resolver struct {
	mu  sync.Mutex
	m   map[string][]string
	n   map[string]int
	all []string
}

func (r *resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq, ok := r.m[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	i := r.n[host]
	r.n[host] = i + 1
	if i >= len(seq) {
		i = len(seq) - 1
	}
	r.all = append(r.all, host)
	return []netip.Addr{netip.MustParseAddr(seq[i])}, nil
}

func names(m map[string][]string) *resolver { return &resolver{m: m, n: map[string]int{}} }

// rig is a fake server over TLS as example.com (its certificate's name),
// and a client that may reach the test's loopback as if it were public —
// the way a test stands in for the internet. Nothing leaves the machine.
type rig struct {
	fake *mcpfake.Server
	srv  *httptest.Server
	c    *Client
}

func newRig(t *testing.T, fake *mcpfake.Server) *rig {
	t.Helper()
	if fake.Tools == nil {
		fake.Tools = mcpfake.Tools()
	}
	srv := httptest.NewUnstartedServer(fake)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the refused handshakes the TLS test makes
	srv.StartTLS()
	t.Cleanup(srv.Close)
	loop := netip.MustParseAddr("127.0.0.1")
	c := &Client{Resolver: names(map[string][]string{"example.com": {"127.0.0.1"}, "other.example.com": {"127.0.0.1"}, "elsewhere.test": {"127.0.0.1"}}),
		Public: func(ip netip.Addr) bool { return ip == loop }, Local: func() []netip.Addr { return nil },
		TLS: srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	return &rig{fake: fake, srv: srv, c: c}
}

func (r *rig) server(path string) Server {
	u, _ := url.Parse(r.srv.URL)
	return Server{URL: &url.URL{Scheme: "https", Host: "example.com:" + u.Port(), Path: path}}
}

func code(err error) string {
	var rf *webfetch.Refusal
	if errors.As(err, &rf) {
		return rf.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

// covers D240 — the wire: initialize, the initialized notification, then
// the session id and the protocol version on every later message, the
// credential on each, the fixed User-Agent; tools listed and called
func TestClientSpeaksStreamableHTTP(t *testing.T) {
	for _, sse := range []bool{false, true} {
		r := newRig(t, &mcpfake.Server{Header: "Authorization", Want: "Bearer tok-123", SSE: sse})
		srv := r.server("/mcp")
		srv.Header, srv.Value, srv.Secret = "Authorization", "Bearer tok-123", "tok-123"
		ctx := context.Background()
		sess, err := r.c.Open(ctx, srv)
		if err != nil {
			t.Fatalf("sse %v: open: %v", sse, err)
		}
		if sess.ID != "fake-session-1" || sess.Version != "2025-06-18" || sess.ServerName != "fake" {
			t.Errorf("session %+v", sess)
		}
		tools, err := r.c.Tools(ctx, srv, sess)
		if err != nil || len(tools) != 2 || tools[0]["name"] != "lookup" {
			t.Fatalf("tools: %v %v", tools, err)
		}
		res, err := r.c.Call(ctx, srv, sess, "post", map[string]any{"id": "T-1"})
		if err != nil || !strings.Contains(string(res), "called post") {
			t.Fatalf("call: %s %v", res, err)
		}
		log := r.fake.Log()
		if got := strings.Join(r.fake.Methods(), ","); got != "initialize,notifications/initialized,tools/list,tools/call" {
			t.Errorf("methods %s", got)
		}
		for i, s := range log {
			if s.Credential != "Bearer tok-123" || s.Agent != webfetch.UserAgent {
				t.Errorf("message %d: %+v", i, s)
			}
			if i > 0 && (s.Session != "fake-session-1" || s.Version != "2025-06-18") {
				t.Errorf("message %d without the session: %+v", i, s)
			}
		}
		if log[0].Session != "" {
			t.Error("initialize carried a session")
		}
	}
}

// covers D240 — a header credential goes in its header; a wrong one is
// mcp-auth, and a server that echoes the credential back — in an error, in
// a result, escaped — shows [secret], never the value
func TestClientCredentialAndScrub(t *testing.T) {
	r := newRig(t, &mcpfake.Server{Header: "X-Api-Key", Want: "k-SECRET-9", Echo: true})
	srv := r.server("/mcp")
	srv.Header, srv.Value, srv.Secret = "X-Api-Key", "k-SECRET-9", "k-SECRET-9"
	ctx := context.Background()
	sess, err := r.c.Open(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.c.Call(ctx, srv, sess, "post", nil)
	if err != nil || strings.Contains(string(res), "SECRET") || !strings.Contains(string(res), "[secret]") {
		t.Fatalf("an echo in the result: %s %v", res, err)
	}
	bad := srv
	bad.Value = "k-WRONG-9-xyz"
	bad.Secret = bad.Value
	_, err = r.c.Open(ctx, bad)
	if code(err) != "mcp-auth" || strings.Contains(err.Error(), "WRONG") {
		t.Errorf("a wrong credential: %v", err)
	}
	// an escaped spelling of the secret is scrubbed too
	esc := scrubJSON(json.RawMessage(`{"t":"k-SECRET-9 and k-SECRET-9"}`), "k-SECRET-9")
	if strings.Contains(string(esc), "SECRET") {
		t.Errorf("escaped: %s", esc)
	}
}

// covers D240 — bounded: an answer past MaxAnswer, an event stream that
// floods past it without answering, a server slower than the time an
// exchange has — each ends the exchange
func TestClientCaps(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, &mcpfake.Server{Huge: 2 << 20})
	srv := r.server("/mcp")
	sess, err := r.c.Open(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Call(ctx, srv, sess, "post", nil); code(err) != "mcp-protocol" {
		t.Errorf("an oversized answer: %v", err)
	}
	r.fake.SSE = true
	if _, err := r.c.Call(ctx, srv, sess, "post", nil); code(err) != "mcp-protocol" {
		t.Errorf("an oversized event: %v", err)
	}
	r.fake.Huge, r.fake.SSE, r.fake.Flood = 0, false, true
	if _, err := r.c.Call(ctx, srv, sess, "post", nil); code(err) != "mcp-protocol" || !strings.Contains(err.Error(), "ran past") {
		t.Errorf("a flood: %v", err)
	}
	r.fake.Flood, r.fake.Delay = false, 2*time.Second
	r.c.Timeout = 300 * time.Millisecond
	start := time.Now()
	if _, err := r.c.Call(ctx, srv, sess, "post", nil); code(err) != "mcp-timeout" || time.Since(start) > 2*time.Second {
		t.Errorf("a slow server: %v after %v", err, time.Since(start))
	}
}

// covers D240 — xbind answers no server: a request from the server in the
// middle of a call (sampling) ends it, and nothing is sent back; a batch
// answer is refused
func TestClientRefusesServerRequests(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, &mcpfake.Server{})
	srv := r.server("/mcp")
	sess, err := r.c.Open(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	r.fake.AskSampling = true
	before := len(r.fake.Log())
	if _, err := r.c.Call(ctx, srv, sess, "post", nil); code(err) != "mcp-protocol" || !strings.Contains(err.Error(), "sampling/createMessage") {
		t.Errorf("a server's request: %v", err)
	}
	if n := len(r.fake.Log()) - before; n != 1 {
		t.Errorf("%d messages for one call: xbind answered the server", n)
	}
	r.fake.AskSampling, r.fake.Batch = false, true
	if _, err := r.c.Call(ctx, srv, sess, "post", nil); code(err) != "mcp-protocol" {
		t.Errorf("a batch: %v", err)
	}
}

// covers D240 — redirects: to another host refused, to the same host (307)
// followed with the POST sent again, a 301 refused
func TestClientRedirects(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, &mcpfake.Server{})
	u, _ := url.Parse(r.srv.URL)
	r.fake.RedirectTo = "https://other.example.com:" + u.Port() + "/mcp"
	if _, err := r.c.Open(ctx, r.server("/mcp")); code(err) != "mcp-redirect" || !strings.Contains(err.Error(), "other.example.com") {
		t.Errorf("another host: %v", err)
	}
	r.fake.RedirectTo = "http://example.com:" + u.Port() + "/mcp"
	if _, err := r.c.Open(ctx, r.server("/mcp")); code(err) != "mcp-redirect" {
		t.Errorf("a scheme downgrade: %v", err)
	}
	r.fake.RedirectTo = ""
	r.fake.Moved = "/mcp/"
	if _, err := r.c.Open(ctx, r.server("/mcp")); code(err) != "mcp-redirect" {
		t.Errorf("a 301: %v", err)
	}
	// the same origin, 307: a fake that redirects /old to /mcp
	mux := http.NewServeMux()
	inner := &mcpfake.Server{Tools: mcpfake.Tools()}
	mux.Handle("/mcp", inner)
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/mcp")
		w.WriteHeader(http.StatusTemporaryRedirect)
	})
	r2 := newRig(t, &mcpfake.Server{})
	r2.srv.Config.Handler = mux
	if _, err := r2.c.Open(ctx, r2.server("/old")); err != nil || len(inner.Log()) == 0 || inner.Log()[0].Method != "initialize" {
		t.Errorf("the same origin: %v %v", err, inner.Log())
	}
}

// covers D240 — the address: a server on loopback is refused at the dial
// unless the admin marked it as on a private network, and a link-local
// address (the metadata endpoint) even then; the URL's host is the one
// name dialled; every exchange checks again (a name that moves to loopback
// reaches nothing)
func TestClientAddresses(t *testing.T) {
	ctx := context.Background()
	fake := &mcpfake.Server{Tools: mcpfake.Tools()}
	srv := httptest.NewServer(fake) // plain http on the host's loopback, as a server on a private network
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	real := &Client{Resolver: names(map[string][]string{"mcp.lan": {"127.0.0.1"}, "meta.lan": {"169.254.169.254"}})} // the real rules: public only, not the host's own
	at := func(host string, private bool) Server {
		return Server{URL: &url.URL{Scheme: "http", Host: host + ":" + u.Port(), Path: "/mcp"}, Private: private}
	}
	if _, err := real.Open(ctx, at("mcp.lan", false)); code(err) != "mcp-address" || !strings.Contains(err.Error(), "private network") {
		t.Errorf("loopback, unmarked: %v", err)
	}
	if _, err := real.Open(ctx, at("127.0.0.1", false)); code(err) != "mcp-address" {
		t.Errorf("a loopback address, unmarked: %v", err)
	}
	if len(fake.Log()) != 0 {
		t.Fatal("an unmarked loopback server was reached")
	}
	if _, err := real.Open(ctx, at("mcp.lan", true)); err != nil || len(fake.Log()) == 0 {
		t.Errorf("loopback, marked private: %v", err)
	}
	if _, err := real.Open(ctx, at("meta.lan", true)); code(err) != "mcp-address" {
		t.Errorf("link-local, marked private: %v", err)
	}
	// rebinding: the name's second answer is loopback — the exchange that resolves it reaches nothing
	var dialled []string
	reb := &Client{Resolver: names(map[string][]string{"moving.example.com": {"93.184.215.14", "127.0.0.1"}}), Local: func() []netip.Addr { return nil },
		DialIP: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialled = append(dialled, addr)
			return nil, errors.New("no network in this test")
		}}
	s := Server{URL: &url.URL{Scheme: "https", Host: "moving.example.com", Path: "/mcp"}}
	if _, err := reb.Open(ctx, s); code(err) != "mcp-unreachable" {
		t.Errorf("first: %v", err)
	}
	if _, err := reb.Open(ctx, s); code(err) != "mcp-address" {
		t.Errorf("rebound to loopback: %v", err)
	}
	if strings.Join(dialled, ",") != "93.184.215.14:443" {
		t.Errorf("dialled %v", dialled)
	}
	// the transport dials the listed host alone
	tr := reb.transport(s)
	if _, err := tr.DialContext(ctx, "tcp", "elsewhere.example.com:443"); code(err) != "mcp-address" {
		t.Errorf("another host through the transport: %v", err)
	}
}

// covers D240 — TLS is verified: a certificate the client doesn't trust,
// or for another name, ends the exchange
func TestClientVerifiesTLS(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, &mcpfake.Server{})
	untrusting := &Client{Resolver: r.c.Resolver, Public: r.c.Public, Local: r.c.Local}
	if _, err := untrusting.Open(ctx, r.server("/mcp")); code(err) != "mcp-unreachable" || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("an untrusted certificate: %v", err)
	}
	u, _ := url.Parse(r.srv.URL)
	other := Server{URL: &url.URL{Scheme: "https", Host: "elsewhere.test:" + u.Port(), Path: "/mcp"}} // (the test certificate is example.com's and its names')
	if _, err := r.c.Open(ctx, other); code(err) != "mcp-unreachable" || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("a certificate for another name: %v", err)
	}
	if len(r.fake.Log()) != 0 {
		t.Error("a message crossed an unverified connection")
	}
	_ = tls.VersionTLS13
}

// covers D240 — a server that forgets the session answers 404 for it:
// ErrSessionGone, for the caller to open a new one
func TestClientSessionGone(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, &mcpfake.Server{ForgetAfter: 3})
	srv := r.server("/mcp")
	sess, err := r.c.Open(ctx, srv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Tools(ctx, srv, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Call(ctx, srv, sess, "post", nil); !errors.Is(err, ErrSessionGone) {
		t.Errorf("a forgotten session: %v", err)
	}
}
