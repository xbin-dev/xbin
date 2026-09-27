//go:build linux && integration

// Run with: go test -tags=integration -run '^TestTermMountPoints' ./internal/term/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs from `make rootfs`); skips otherwise.
package term

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// covers WP-2b — a real terminal on the shipped rootfs starts with its mount
// points never followed through its persistent layer: the SDK's /opt bind,
// /proc and the rest work, a mount point through the image's own absolute
// /var/run → /run lands inside the sandbox, and a symlink the layer holds
// where a mount point goes (/opt → a host dir, /proc) fails the start with
// the path and the reset hint, makes nothing on the host and doesn't wedge;
// a reset clears it.
func TestTermMountPoints(t *testing.T) {
	rootfs := layerRootfs(t)
	if fi, err := os.Lstat(filepath.Join(rootfs, "var", "run")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Skip("this rootfs doesn't ship /var/run as a symlink")
	}
	root, host := t.TempDir(), t.TempDir()
	sdk, outside := filepath.Join(host, "sdk"), filepath.Join(host, "outside")
	for _, d := range []string{filepath.Join(root, "apps", "x"), sdk, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sdk, "go.mod"), []byte("module sdk-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	confine.Configure(rootfs)
	t.Cleanup(func() {
		_ = confine.RemoveAll(context.Background(), root)
		confine.Configure("")
	})
	m := NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs
	m.ExtraBinds = []sandbox.Bind{
		{Src: sdk, Dst: "/opt/xbin/sdk", RO: true},     // where an install binds it
		{Src: sdk, Dst: "/var/run/xbin-sdk", RO: true}, // through the image's /var/run → /run
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	}))
	t.Cleanup(srv.Close)

	script := "cat /opt/xbin/sdk/go.mod /run/xbin-sdk/go.mod; test -r /proc/self/status && echo PROC-''OK; echo DONE-''MARK; exit\n"
	out, ended := termRun(t, srv.URL, "apps/x", script, "DONE-MARK")
	if ended || strings.Count(out, "module sdk-marker") < 2 || !strings.Contains(out, "PROC-OK") {
		t.Fatalf("a terminal on the rootfs (ended early %v):\n%s", ended, out)
	}
	waitNoSessions(t, m)

	upper := filepath.Join(root, ".xbin", "term", termKey("apps/x"), "upper")
	for _, c := range []struct{ plant, want string }{
		{"opt", "nested mount point /opt: a symlink is in the way (" + termRootHint + ")"},
		{"proc", "nested mount point /proc: a symlink is in the way (" + termRootHint + ")"},
	} {
		// the first session made /opt/xbin/sdk in the layer: the plant replaces it
		if err := os.RemoveAll(filepath.Join(upper, c.plant)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(upper, c.plant)); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		out, ended := termRun(t, srv.URL, "apps/x", "echo NEVER-''HERE\n", "NEVER-HERE")
		if !ended || !strings.Contains(out, c.want) {
			t.Errorf("planted /%s: ended %v, want %q in\n%s", c.plant, ended, c.want, out)
		}
		if d := time.Since(start); d > 30*time.Second {
			t.Errorf("planted /%s: the refusal took %v", c.plant, d)
		}
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Fatalf("planted /%s: made %v on the host where it points", c.plant, ents)
		}
		waitNoSessions(t, m)
		if err := os.Remove(filepath.Join(upper, c.plant)); err != nil {
			t.Fatal(err)
		}
	}

	// the reset the refusal names clears what the layer holds
	if err := os.Symlink(outside, filepath.Join(upper, "opt")); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetEnv("apps/x"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if out, ended := termRun(t, srv.URL, "apps/x", script, "DONE-MARK"); ended || !strings.Contains(out, "module sdk-marker") {
		t.Fatalf("after the reset (ended early %v):\n%s", ended, out)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("made %v on the host", ents)
	}
}

// termRun opens a terminal on rel, types script and reads until the output
// holds marker (false) or the session ends first (true), with what it saw.
func termRun(t *testing.T, url, rel, script, marker string) (string, bool) {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(url, "http")+"/ws/term?net=none&cwd="+rel, nil)
	if err != nil {
		body := ""
		if resp != nil {
			b := make([]byte, 512)
			n, _ := resp.Body.Read(b)
			body = string(b[:n])
		}
		t.Fatalf("open a terminal: %v %s", err, body)
	}
	defer c.Close()
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(script)); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !strings.Contains(out.String(), marker) {
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		mt, b, err := c.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatalf("no %s and no end in 60s (wedged?):\n%s", marker, out.String())
			}
			return out.String(), true // closed
		}
		if mt == websocket.BinaryMessage {
			out.Write(b)
			continue
		}
		var ctl map[string]any
		if json.Unmarshal(b, &ctl) == nil && ctl["op"] == "exit" {
			return out.String(), true
		}
	}
	return out.String(), false
}

// waitNoSessions waits for every session to be gone (the layer is free).
func waitNoSessions(t *testing.T, m *Manager) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); len(m.List()) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("sessions left: %v", m.List())
		}
	}
}
