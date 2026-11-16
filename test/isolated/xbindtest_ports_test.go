//go:build linux && integration

package isolated

import (
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// The harness's own port handling (xbindtest's daemon.go). A port is
// picked before xbind binds it, seconds later; when something else has it
// by then — here a server that answers /healthz, as another test's xbind
// would — the harness used to take that answer for its own daemon and the
// test ran against a stranger. It now waits for ITS xbind, and a first boot
// that lost its port moves to another.
func TestHarnessTakenPort(t *testing.T) {
	a := xbindtest.Require(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stranger := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok\n")) // /healthz, and everything else
	})}
	go stranger.Serve(ln)
	defer stranger.Close()
	taken := ln.Addr().String()

	d := xbindtest.Start(t, a, xbindtest.Options{TryAddrs: []string{taken}})
	if d.Addr == taken {
		t.Fatalf("the daemon is said to listen on %s, which a stranger holds", taken)
	}
	resp, err := http.Get(d.URL + "/api/xbin/whoami")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"admin"`) {
		t.Fatalf("%s doesn't answer as xbind: %d %s", d.URL, resp.StatusCode, body)
	}
	if !strings.Contains(d.LogTail(200), "address already in use") {
		t.Fatalf("the first boot didn't lose its port: the test proves nothing\n%s", d.LogTail(40))
	}
}

// Daemons started at once — parallel tests, parallel test binaries — never
// get the same port from the harness: each holds its port's reservation
// while it lives.
func TestHarnessPortsDistinct(t *testing.T) {
	a := xbindtest.Require(t)
	const n = 4
	addrs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			addrs[i] = xbindtest.Start(t, a, xbindtest.Options{}).Addr
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, addr := range addrs {
		if addr == "" || seen[addr] {
			t.Fatalf("addresses %v", addrs)
		}
		seen[addr] = true
		resp, err := http.Get("http://" + addr + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%s: %v", addr, err)
		}
		resp.Body.Close()
	}
}
