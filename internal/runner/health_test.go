package runner

import (
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// A generation waiting on xbind's answer to a call of its own isn't charged
// for it: the health deadline holds while the call is in flight, so a
// backend that listens once xbind answered becomes healthy, however long
// the answer took (docs/sdk.md → Startup). The listener appears from inside
// waiting, long after the plain timeout: no sleep decides the order.
func TestWaitHealthyNotChargedForXbindCalls(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "g1.sock")
	const timeout = 50 * time.Millisecond
	start := time.Now()
	var ln net.Listener
	waiting := func() bool {
		if ln == nil && time.Since(start) > 6*timeout { // xbind answers, the backend listens
			var err error
			if ln, err = net.Listen("unix", sock); err != nil {
				t.Fatal(err)
			}
			return false
		}
		return ln == nil
	}
	if err := waitHealthy(sock, make(chan struct{}), timeout, waiting); err != nil {
		t.Fatalf("a backend that listened once xbind answered it: %v", err)
	}
	ln.Close()

	// without a call in flight the deadline is today's
	if err := waitHealthy(filepath.Join(t.TempDir(), "none.sock"), make(chan struct{}), timeout, func() bool { return false }); !errors.Is(err, errNeverListened) {
		t.Fatalf("no listener, no call: %v", err)
	}
}

// The grace is bounded: a call xbind never answers fails the start
// xbindCallGrace after the deadline, as a backend that never listened.
func TestWaitHealthyGraceBounded(t *testing.T) {
	defer func(g time.Duration) { xbindCallGrace = g }(xbindCallGrace)
	xbindCallGrace = 100 * time.Millisecond
	start := time.Now()
	err := waitHealthy(filepath.Join(t.TempDir(), "none.sock"), make(chan struct{}), 50*time.Millisecond, func() bool { return true })
	if !errors.Is(err, errNeverListened) {
		t.Fatalf("a call that never ends: %v", err)
	}
	if d := time.Since(start); d < 150*time.Millisecond || d > 30*time.Second {
		t.Fatalf("waited %s: want the timeout plus the grace", d)
	}
}

// A socket that exists but refuses connections is not "never listened".
func TestWaitHealthyListeningButRefusing(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "g1.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close() // the file stays; nothing accepts
	err = waitHealthy(sock, make(chan struct{}), 50*time.Millisecond, nil)
	if err == nil || errors.Is(err, errNeverListened) || !strings.HasPrefix(err.Error(), "timeout dialing backend socket") {
		t.Fatalf("a socket nobody accepts on: %v", err)
	}
}

// A backend that runs but never listened says why it failed, in the error
// and in its log after the start marker: the call to xbind it is still
// waiting on (the route, never the path's data), or that it printed
// nothing — never what it printed.
func TestUnhealthySaysWhatTheBackendWasDoing(t *testing.T) {
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Auth: a}
	v := &registry.Component{Path: "apps/postfix"}
	logPath := filepath.Join(t.TempDir(), "backend.log")
	if err := os.WriteFile(logPath, []byte("--- gen 2 start 2026-11-08T10:00:00Z ---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := &instance{gen: 2, token: "tok-2", started: time.Now(), out: logRef{logPath, logSize(logPath)}}

	// printed nothing, no call in flight
	msg := r.unhealthy(v, inst, errNeverListened).Error()
	for _, want := range []string{"backend did not become healthy: timeout dialing backend socket",
		"running but never started listening", "has not called Serve", "printed nothing since it started", "/docs/sdk.md#startup"} {
		if !strings.Contains(msg, want) {
			t.Errorf("silent backend: %q lacks %q", msg, want)
		}
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "--- gen 2 stopped: timeout dialing backend socket") {
		t.Errorf("the log after the marker: %q", log)
	}

	// waiting on xbind: the call is named by its route
	a.RegisterInstance("tok-2", "apps/postfix")
	req := httptest.NewRequest("PUT", "/api/xbin/kv/res:apps/postfix/kv/secret-key-name", nil)
	req.Header.Set("Authorization", "Bearer tok-2")
	release := a.TrackInstanceCall(req, "PUT /api/xbin/kv/{rest...}")
	msg = r.unhealthy(v, inst, errNeverListened).Error()
	release()
	if !strings.Contains(msg, "it is waiting on xbind: PUT /api/xbin/kv/{rest...} (for ") || strings.Contains(msg, "secret-key-name") {
		t.Errorf("waiting backend: %q", msg)
	}

	// printed something: pointed at its log, its output not repeated
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("password=hunter2\n")
	f.Close()
	inst.out.at = 0
	msg = r.unhealthy(v, inst, errNeverListened).Error()
	if !strings.Contains(msg, "bx logs apps/postfix") || strings.Contains(msg, "hunter2") {
		t.Errorf("a backend that printed: %q", msg)
	}

	// exited and the other failures keep today's text
	if got := r.unhealthy(v, inst, errExited).Error(); got != "backend did not become healthy: "+errExited.Error() {
		t.Errorf("exited: %q", got)
	}
}
