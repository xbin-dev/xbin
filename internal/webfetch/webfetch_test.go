package webfetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resolver is a test resolver: names to answers, one answer list per
// lookup in turn (the last repeats).
type resolver struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr
	asked   map[string]int
}

func (r *resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	list, ok := r.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	n := r.asked[host]
	r.asked[host] = n + 1
	if n >= len(list) {
		n = len(list) - 1
	}
	return list[n], nil
}

func newResolver(m map[string][]string) *resolver {
	r := &resolver{answers: map[string][][]netip.Addr{}, asked: map[string]int{}}
	for host, seq := range m {
		for _, one := range seq {
			var addrs []netip.Addr
			for _, a := range strings.Split(one, ",") {
				addrs = append(addrs, netip.MustParseAddr(a))
			}
			r.answers[host] = append(r.answers[host], addrs)
		}
	}
	return r
}

// local is a fetcher that may reach 127.0.0.1 (and only that) — the test
// servers' address — under the name site.test and its friends.
func local(names ...string) *Fetcher {
	m := map[string][]string{}
	for _, n := range names {
		m[n] = []string{"127.0.0.1"}
	}
	return &Fetcher{Resolver: newResolver(m), Public: func(ip netip.Addr) bool { return ip == netip.MustParseAddr("127.0.0.1") || PublicAddr(ip) },
		Local: func() []netip.Addr { return nil }} // (the test servers are on the host's own loopback)
}

// covers D239 — an address of xbin's own host is never dialled, a public
// one included (a cloud VM's public address reaches its own listeners): by
// a name that resolves to it, by the address in the URL, at the connect
func TestFetchRefusesTheHostsOwnAddresses(t *testing.T) {
	own := netip.MustParseAddr("93.184.215.14")
	var dialled []string
	f := &Fetcher{Resolver: newResolver(map[string][]string{"self.example.com": {"93.184.215.14"}}),
		Local: func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("::1"), own} },
		DialIP: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialled = append(dialled, addr)
			return nil, errors.New("no network in this test")
		}}
	if f.Dials(own) || f.Dials(netip.MustParseAddr("::ffff:93.184.215.14")) {
		t.Error("the host's own public address is dialable")
	}
	for _, raw := range []string{"https://self.example.com/", "http://93.184.215.14/"} {
		if _, err := f.Fetch(context.Background(), raw); code(err) != "web-address" {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if err := f.control("tcp4", "93.184.215.14:443", nil); code(err) != "web-address" {
		t.Errorf("the connect to the host's own address: %v", err)
	}
	if len(dialled) != 0 {
		t.Errorf("dialled: %v", dialled)
	}
	// the system's own: whatever Public says, the host's loopback is its own
	sys := &Fetcher{Public: func(netip.Addr) bool { return true }}
	if sys.Dials(netip.MustParseAddr("127.0.0.1")) {
		t.Error("the host's loopback, by its interfaces, is dialable")
	}
}

// at is u with its host replaced by name (the test server's port kept).
func at(srv *httptest.Server, name, path string) string {
	u, _ := url.Parse(srv.URL)
	return "http://" + name + ":" + u.Port() + path
}

func code(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func TestPublicAddr(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "127.8.8.8", "10.1.2.3", "172.16.0.1", "172.31.255.255", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "0.1.2.3", "224.0.0.1", "255.255.255.255", "240.0.0.1", "198.18.0.1", "192.0.2.1", "203.0.113.9",
		"::1", "::", "fd00::1", "fc00::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254",
		"64:ff9b::7f00:1", "2002:7f00:1::1", "2001:db8::1", "2001:0::1", "fec0::1", "100::1",
		// IPv4-compatible (::/96) and IPv4-translated (::ffff:0:0:0/96) forms, and the rest of the IANA registries
		"::127.0.0.1", "::10.0.0.1", "::8.8.8.8", "::ffff:0:7f00:1", "::ffff:0:808:808", "64:ff9b:1::a00:1",
		"2001:0:4136:e378:8000:63bf:3fff:fdd2", "2001:2::1", "2001:20::1", "2001:30::1", "2002:a00:1::1", "3fff::1", "5f00::1"} {
		if PublicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s judged public", a)
		}
	}
	for _, a := range []string{"93.184.215.14", "1.1.1.1", "8.8.8.8", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if !PublicAddr(netip.MustParseAddr(a)) {
			t.Errorf("%s judged not public", a)
		}
	}
	if PublicAddr(netip.MustParseAddr("fe80::1%eth0")) {
		t.Error("an address with a zone judged public")
	}
}

func TestCheckURL(t *testing.T) {
	for _, bad := range []string{"", "ftp://example.com/", "file:///etc/passwd", "gopher://example.com", "javascript:alert(1)", "//example.com/x",
		"http://user:pw@example.com/", "http://user@example.com/", "http:///x", "http://exa mple.com/", "http://example.com/\x00",
		"http://2130706433/", "http://0x7f000001/", "http://0x7f.0.0.1/", "http://0177.0.0.1/", "http://127.1/", "http://017700000001/",
		"http://127.0.0.01/", "http://[fe80::1%25eth0]/", "http://[::1%251]/", "http://example.com:99999/", "http://example.com:0/",
		"http://example.com:8a/", "https://" + strings.Repeat("a", MaxURL) + ".com/"} {
		if _, err := CheckURL(bad); code(err) != "web-url" {
			t.Errorf("CheckURL(%q) = %v, want web-url", bad, err)
		}
	}
	for raw, want := range map[string]string{
		"https://Example.COM/a?b=1#frag": "https://example.com/a?b=1",
		"http://bücher.example/":         "http://xn--bcher-kva.example/",
		"http://93.184.215.14:8080/x":    "http://93.184.215.14:8080/x",
		"http://[2606:4700::0001]/":      "http://[2606:4700::1]/",
		"HTTPS://example.com.":           "https://example.com",
	} {
		u, err := CheckURL(raw)
		if err != nil || u.String() != want {
			t.Errorf("CheckURL(%q) = %v, %v; want %s", raw, u, err, want)
		}
	}
}

func TestPatterns(t *testing.T) {
	p, _ := ParsePattern("Example.com")
	if !p.Match("example.com") || !p.Match("docs.example.com") || p.Match("badexample.com") || p.Match("example.com.evil") {
		t.Errorf("pattern %s matches wrongly", p)
	}
	s, _ := ParsePattern("*.example.com")
	if s.Match("example.com") || !s.Match("a.example.com") {
		t.Errorf("pattern %s matches wrongly", s)
	}
	for _, bad := range []string{"", "*", "http://example.com", "example.com/x", "10.0.0.1", "a@b", "2130706433", "*.*.com"} {
		if _, err := ParsePattern(bad); err == nil {
			t.Errorf("ParsePattern(%q) took it", bad)
		}
	}
}

// fetch from a local server, as a page of the public web
func TestFetchReadsAPage(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title> Prices &amp; plans </title><style>body{}</style><script>alert("x")</script></head>
			<body><h1>Plans</h1><p>Basic costs <b>5 €</b>.</p><!-- a comment --><ul><li>One</li><li>Two</li></ul>
			<a href="/more">More</a> <a href="javascript:x()">no</a><noscript>hidden</noscript></body></html>`)
	}))
	defer srv.Close()
	f := local("site.test")
	res, err := f.Fetch(context.Background(), at(srv, "site.test", "/plans"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != 200 || res.ContentType != "text/html" || res.Title != "Prices & plans" {
		t.Errorf("result: %+v", res)
	}
	for _, want := range []string{"# Plans", "Basic costs 5 €.", "- One", "- Two", "More <" + at(srv, "site.test", "/more") + ">"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, res.Text)
		}
	}
	for _, not := range []string{"alert", "body{}", "a comment", "hidden", "javascript"} {
		if strings.Contains(res.Text, not) {
			t.Errorf("text has %q:\n%s", not, res.Text)
		}
	}
	if got.Get("User-Agent") != UserAgent || got.Get("Cookie") != "" || got.Get("Authorization") != "" || got.Get("Referer") != "" {
		t.Errorf("request headers: %v", got)
	}
}

func TestFetchRefusesLoopbackAndPrivate(t *testing.T) {
	var hit atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1); fmt.Fprint(w, "secret") }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	f := &Fetcher{Resolver: newResolver(map[string][]string{
		"intranet.test": {"10.0.0.5"},
		"local.test":    {"127.0.0.1"},
		"mixed.test":    {"93.184.215.14,127.0.0.1"},
		"meta.test":     {"169.254.169.254"},
		"six.test":      {"::1"},
		"mapped.test":   {"::ffff:127.0.0.1"},
	})}
	// nothing is dialled at all: every address is judged before a socket is made
	var dialled []string
	f.DialIP = func(_ context.Context, _, addr string) (net.Conn, error) {
		dialled = append(dialled, addr)
		return nil, errors.New("no network in this test")
	}
	for _, raw := range []string{srv.URL + "/", "http://localhost:" + u.Port() + "/", "http://app.localhost/", "http://nas.local/", "http://router.home.arpa/",
		"http://[::1]:" + u.Port() + "/", "http://[::ffff:127.0.0.1]:" + u.Port() + "/",
		"http://local.test:" + u.Port() + "/", "http://intranet.test/", "http://mixed.test:" + u.Port() + "/", "http://meta.test/latest/meta-data/",
		"http://six.test:" + u.Port() + "/", "http://mapped.test:" + u.Port() + "/", "http://169.254.169.254/", "http://[fd00::1]/", "http://0.0.0.0:" + u.Port() + "/",
		"http://10.0.0.1/", "http://192.168.0.1/", "http://172.16.0.1/", "http://100.64.0.1/"} {
		if _, err := f.Fetch(context.Background(), raw); code(err) != "web-address" {
			t.Errorf("%s: %v, want web-address", raw, err)
		}
	}
	if hit.Load() != 0 || len(dialled) != 0 {
		t.Errorf("the local server was reached %d times; dialled %v", hit.Load(), dialled)
	}
}

// The default dialer judges the socket's peer itself (its Control): an
// address the resolve-time check let through and that isn't public when
// the socket connects never connects.
func TestFetchControlAtConnect(t *testing.T) {
	var hit atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1) }))
	defer srv.Close()
	var calls atomic.Int32
	f := &Fetcher{Public: func(netip.Addr) bool { return calls.Add(1) == 1 }} // yes at the check, no at the connect
	if _, err := f.Fetch(context.Background(), srv.URL+"/"); code(err) != "web-address" {
		t.Errorf("connect-time check: %v", err)
	}
	if hit.Load() != 0 {
		t.Errorf("the local server was reached")
	}
}

// A name that resolves to a public address when checked and to loopback a
// moment later reaches nothing: the address checked is the address dialled
// (one lookup), and nothing resolves the name again.
func TestFetchDNSRebinding(t *testing.T) {
	var hit atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit.Add(1); fmt.Fprint(w, "secret") }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	res := newResolver(map[string][]string{"rebind.test": {"93.184.215.14", "127.0.0.1"}})
	var mu sync.Mutex
	var dialled []string
	f := &Fetcher{Resolver: res, DialIP: func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialled = append(dialled, addr)
		mu.Unlock()
		if strings.HasPrefix(addr, "127.") {
			var d net.Dialer
			return d.DialContext(ctx, network, addr) // what a resolve-again would reach
		}
		return nil, errors.New("unreachable in this test")
	}}
	_, err := f.Fetch(context.Background(), "http://rebind.test:"+u.Port()+"/")
	if code(err) != "web-unreachable" {
		t.Errorf("rebinding: %v", err)
	}
	if len(dialled) != 1 || dialled[0] != "93.184.215.14:"+u.Port() || hit.Load() != 0 {
		t.Errorf("dialled %v, the local server reached %d times", dialled, hit.Load())
	}
	// the socket's own peer is judged too: an address that isn't public never connects
	if err := (&Fetcher{}).control("tcp4", "127.0.0.1:80", nil); code(err) != "web-address" {
		t.Errorf("control let loopback through: %v", err)
	}
	if err := (&Fetcher{}).control("tcp6", "[::ffff:10.0.0.1]:80", nil); code(err) != "web-address" {
		t.Errorf("control let a mapped private address through: %v", err)
	}
	if err := (&Fetcher{}).control("tcp4", "93.184.215.14:443", nil); err != nil {
		t.Errorf("control refused a public address: %v", err)
	}
}

func TestFetchRedirects(t *testing.T) {
	var cookieSeen atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			cookieSeen.Store(true)
		}
		switch r.URL.Path {
		case "/to-private":
			http.Redirect(w, r, "http://10.0.0.7/admin", http.StatusFound)
		case "/to-loopback6":
			http.Redirect(w, r, "http://[::1]:"+strings.TrimPrefix(srv.URL[strings.LastIndex(srv.URL, ":"):], ":")+"/", http.StatusFound)
		case "/to-file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/to-blocked":
			http.Redirect(w, r, at(srv, "blocked.test", "/"), http.StatusMovedPermanently)
		case "/loop":
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "1"})
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/hop":
			http.SetCookie(w, &http.Cookie{Name: "s", Value: "1"})
			http.Redirect(w, r, at(srv, "other.test", "/end"), http.StatusSeeOther)
		case "/end":
			fmt.Fprint(w, "the end")
		}
	}))
	defer srv.Close()
	f := local("site.test", "other.test", "blocked.test")
	f.Check = func(u *url.URL) error {
		if u.Hostname() == "blocked.test" {
			return errors.New("blocked.test is on this workspace's deny list")
		}
		return nil
	}
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/to-private")); code(err) != "web-address" {
		t.Errorf("a redirect to a private address: %v", err)
	}
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/to-loopback6")); code(err) != "web-address" {
		t.Errorf("a redirect to [::1]: %v", err)
	}
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/to-file")); code(err) != "web-redirect" {
		t.Errorf("a redirect to file: %v", err)
	}
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/to-blocked")); code(err) != "web-host" {
		t.Errorf("a redirect to a host the lists refuse: %v", err)
	}
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/loop")); code(err) != "web-redirect" {
		t.Errorf("a redirect loop: %v", err)
	}
	res, err := f.Fetch(context.Background(), at(srv, "site.test", "/hop"))
	if err != nil || res.Text != "the end" || len(res.Redirects) != 1 || res.FinalURL != at(srv, "other.test", "/end") {
		t.Errorf("a redirect followed: %+v %v", res, err)
	}
	if cookieSeen.Load() {
		t.Error("a cookie was sent back")
	}
}

func TestFetchCaps(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			chunk := strings.Repeat("x", 64<<10)
			for i := 0; i < 48; i++ { // 3 MiB
				if _, err := fmt.Fprint(w, chunk); err != nil {
					return
				}
			}
		case "/slow":
			select {
			case <-stop:
			case <-r.Context().Done():
			}
		case "/png":
			w.Header().Set("Content-Type", "image/png")
			fmt.Fprint(w, "\x89PNG")
		case "/utf16":
			w.Header().Set("Content-Type", "text/plain; charset=utf-16")
			fmt.Fprint(w, "x")
		case "/control":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "a\x1b[2Jb\x00c")
		}
	}))
	defer srv.Close()
	defer close(stop)
	f := local("site.test")
	res, err := f.Fetch(context.Background(), at(srv, "site.test", "/big"))
	if err != nil || !res.Truncated || res.Bytes != MaxBody || len(res.Text) > MaxText {
		t.Errorf("3 MiB: bytes %d, text %d, truncated %v, %v", res.Bytes, len(res.Text), res.Truncated, err)
	}
	f.Timeout = 300 * time.Millisecond
	start := time.Now()
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/slow")); code(err) != "web-timeout" {
		t.Errorf("slow: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the time cap took %v", d)
	}
	f.Timeout = 0
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/png")); code(err) != "web-type" {
		t.Errorf("png: %v", err)
	}
	if _, err := f.Fetch(context.Background(), at(srv, "site.test", "/utf16")); code(err) != "web-type" {
		t.Errorf("utf-16: %v", err)
	}
	res, err = f.Fetch(context.Background(), at(srv, "site.test", "/control"))
	if err != nil || res.Text != "a[2Jbc" {
		t.Errorf("control characters: %q %v", res.Text, err)
	}
}

// the environment's proxy is never used: it would dial past every check
func TestFetchIgnoresProxyEnv(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxied.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "direct") }))
	defer srv.Close()
	res, err := local("site.test").Fetch(context.Background(), at(srv, "site.test", "/"))
	if err != nil || res.Text != "direct" || proxied.Load() != 0 {
		t.Errorf("%+v %v, proxied %d", res, err, proxied.Load())
	}
}

func TestDescribe(t *testing.T) {
	if d := Describe("https://example.com/a/b?token=s3cret"); d != "https://example.com/a/b?…" {
		t.Errorf("Describe: %q", d)
	}
}

// covers D240 — a server an admin marked as on a private network may be at
// a private or loopback address, never at a link-local one (the metadata
// endpoint), multicast, unspecified or broadcast
func TestPrivateOK(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "192.168.1.1", "172.16.0.1", "100.64.0.1", "::1", "fd00::1", "8.8.8.8", "::ffff:10.0.0.1"} {
		if !PrivateOK(netip.MustParseAddr(a)) {
			t.Errorf("%s refused", a)
		}
	}
	for _, a := range []string{"169.254.169.254", "fe80::1", "224.0.0.1", "ff02::1", "0.0.0.0", "::", "255.255.255.255", "0.1.2.3", "::ffff:169.254.169.254", "fe80::1%eth0"} {
		if PrivateOK(netip.MustParseAddr(a)) {
			t.Errorf("%s taken", a)
		}
	}
}
