//go:build linux && integration

// Run with: go test -tags=integration -run '^TestConfinedBaseAutoUpdate' ./internal/term/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs); skips otherwise.
package term

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

// Real terminals on the real rootfs (D175). Each tile's layer is stamped
// with an older base than the rootfs's, and a first session — base
// auto-update off, so it runs on that base — installs something the way
// apt does: a tree root owns in the sandbox, chowned to sub-uid 1000 in
// range mode (which xbind can't unlink) with a mode-000 dir in it (which
// xbind can't list). Then:
//
//   - apps/on: a session with the setting still off finds it kept — no
//     line, the stamp the old base's; a session with it on moves the layer:
//     its first output the grey line, the install gone, the stamp the
//     rootfs's, and the old layer, put aside, removed by the confined
//     remover.
//   - apps/gone: the old base then disappears (the layer restamped with a
//     base that isn't installed): off, the terminal refuses to open; on, it
//     moves like the other.
//
// The "older base" is the same rootfs under a second name, `<rootfs>-old1`
// beside a `<rootfs>` link (ResolveBase only looks for the dir), so the
// test needs no second image. Nothing here waits on time: each step reads
// the terminal until a marker only the command's output holds, and each
// session's end is the layer's release (the condition the next start
// needs), polled with a generous bound for a loaded machine.
func TestConfinedBaseAutoUpdate(t *testing.T) {
	real := layerRootfs(t)
	cur := layers.BaseVersion(real)
	if cur == "old1" || cur == "gone" || cur == layers.Legacy {
		t.Skip("the rootfs is unstamped, or stamped with one of the test's names")
	}
	root, bases := t.TempDir(), t.TempDir()
	rootfs := filepath.Join(bases, "rootfs")
	for _, l := range []string{rootfs, rootfs + "-old1"} {
		if err := os.Symlink(real, l); err != nil {
			t.Fatal(err)
		}
	}
	confine.Configure(real)
	t.Cleanup(func() { // whatever a failure left: only a confined rm clears a sub-uid's files
		_ = confine.RemoveAll(context.Background(), root)
		confine.Configure("")
	})

	m := NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs
	var auto atomic.Bool // the workspace setting; sessions open one at a time
	m.BaseAutoUpdate = auto.Load
	t.Cleanup(m.waitMoved)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	}))
	t.Cleanup(srv.Close)

	ranged, _ := sandbox.IDMapStatus(os.Getuid(), os.Getgid())
	install := "mkdir -p /opt/installed/sub && echo x > /opt/installed/sub/f"
	if ranged {
		install += " && chown -R 1000:1000 /opt/installed"
	}
	install += " && chmod 000 /opt/installed/sub && echo INSTALL-''DONE"
	const probe = "test -e /opt/installed/sub && echo LAYER-''KEPT || echo LAYER-''FRESH"
	const line = "terminal moved to the new base image"

	// setup: the tile's layer on old1, with an install a first session made
	setup := func(t *testing.T, rel string) (key, layer string) {
		t.Helper()
		key = termKey(rel)
		layer = filepath.Join(root, ".xbin", "term", key)
		for _, d := range []string{filepath.Join(root, rel), layer} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := layers.Stamp(layer, layers.Stamps{Base: "old1"}); err != nil {
			t.Fatal(err)
		}
		auto.Store(false)
		c := dialTerm(t, srv.URL, rel)
		runInTerm(t, c, install, "INSTALL-DONE")
		endTerm(t, m, c, key)
		if _, err := os.Lstat(filepath.Join(layer, "upper", "opt", "installed", "sub")); err != nil {
			t.Fatalf("the install isn't in the layer's upper: %v", err)
		}
		if ranged {
			if err := os.RemoveAll(filepath.Join(layer, "upper", "opt", "installed")); err == nil {
				t.Fatal("xbind removed the sub-uid's install itself: the test proves nothing")
			}
		}
		return key, layer
	}
	stampOf := func(t *testing.T, layer string) string {
		t.Helper()
		s, err := layers.Read(layer)
		if err != nil {
			t.Fatal(err)
		}
		return s.Base
	}
	// moves opens a session with the setting on and checks the move
	moves := func(t *testing.T, rel, key, layer string) {
		t.Helper()
		auto.Store(true)
		c := dialTerm(t, srv.URL, rel)
		out := runInTerm(t, c, probe, "LAYER-KEPT", "LAYER-FRESH")
		if !strings.Contains(out, "LAYER-FRESH") || !strings.Contains(out, line) {
			t.Fatalf("on: no move — output:\n%s", out)
		}
		if strings.Index(out, line) > strings.Index(out, "LAYER-FRESH") {
			t.Fatalf("the grey line isn't the session's first output:\n%s", out)
		}
		if s := stampOf(t, layer); s != cur {
			t.Fatalf("the moved layer is stamped %q, want %q", s, cur)
		}
		endTerm(t, m, c, key)
		m.waitMoved() // the confined remover's run on the put-aside layer
		ents, err := os.ReadDir(m.movedRoot())
		if err != nil || len(ents) != 0 {
			t.Fatalf("the old layer wasn't removed: %v %v", ents, err)
		}
	}

	t.Run("on", func(t *testing.T) {
		rel := "apps/on"
		key, layer := setup(t, rel)
		auto.Store(false) // kept, on its base
		c := dialTerm(t, srv.URL, rel)
		out := runInTerm(t, c, probe, "LAYER-KEPT", "LAYER-FRESH")
		if !strings.Contains(out, "LAYER-KEPT") || strings.Contains(out, line) {
			t.Fatalf("off: the layer moved — output:\n%s", out)
		}
		endTerm(t, m, c, key)
		if s := stampOf(t, layer); s != "old1" {
			t.Fatalf("off: stamped %q", s)
		}
		moves(t, rel, key, layer)
	})

	t.Run("gone", func(t *testing.T) {
		rel := "apps/gone"
		key, layer := setup(t, rel)
		if err := layers.Stamp(layer, layers.Stamps{Base: "gone"}); err != nil {
			t.Fatal(err)
		}
		auto.Store(false) // refused: its base isn't installed
		if msg := dialRefused(t, srv.URL, rel); !strings.Contains(msg, "not installed") {
			t.Fatalf("off: %q", msg)
		}
		if s := stampOf(t, layer); s != "gone" {
			t.Fatalf("off: stamped %q", s)
		}
		moves(t, rel, key, layer)
	})
}

// dialTerm opens a terminal on rel as the owner, offline.
func dialTerm(t *testing.T, base, rel string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 3 * time.Minute} // a bound for a stuck start, not a timing
	c, resp, err := d.Dial("ws"+strings.TrimPrefix(base, "http")+"/ws/term?net=none&cwd="+rel, nil)
	if err != nil {
		body := ""
		if resp != nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			body = string(b)
		}
		t.Fatalf("open a terminal on %s: %v %s", rel, err, body)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// dialRefused opens a terminal on rel that must be refused, and returns why.
func dialRefused(t *testing.T, base, rel string) string {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 3 * time.Minute}
	c, resp, err := d.Dial("ws"+strings.TrimPrefix(base, "http")+"/ws/term?net=none&cwd="+rel, nil)
	if err == nil {
		c.Close()
		t.Fatalf("a terminal on %s opened", rel)
	}
	if resp == nil {
		t.Fatalf("no answer: %v", err)
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return string(b)
}

// runInTerm types line and reads the terminal until one of the markers
// shows in its output; it returns everything read (the scrollback first).
func runInTerm(t *testing.T, c *websocket.Conn, line string, markers ...string) string {
	t.Helper()
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(line+"\n")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for {
		for _, mk := range markers {
			if strings.Contains(out.String(), mk) {
				return out.String()
			}
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Minute)) // a bound for a stuck sandbox, not a timing
		mt, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("the terminal never answered (%q): %v", out.String(), err)
		}
		if mt == websocket.BinaryMessage {
			out.Write(b)
		} else if strings.Contains(string(b), `"op":"exit"`) {
			t.Fatalf("the session ended: %s (%q)", b, out.String())
		}
	}
}

// endTerm exits the shell and waits for the session to let its layer go:
// its exit frame comes first, the layer's release after its sandbox is
// torn down — what the next start on the layer needs.
func endTerm(t *testing.T, m *Manager, c *websocket.Conn, key string) {
	t.Helper()
	_ = c.WriteMessage(websocket.BinaryMessage, []byte("exit\n"))
	for {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Minute))
		mt, b, err := c.ReadMessage()
		if err != nil {
			break // closed: the session is over
		}
		var ctl map[string]any
		if mt == websocket.TextMessage && json.Unmarshal(b, &ctl) == nil && ctl["op"] == "exit" {
			break
		}
	}
	deadline := time.Now().Add(3 * time.Minute) // a bound for a stuck teardown, not a timing
	for !m.acquireEnv(key) {
		if time.Now().After(deadline) {
			t.Fatal("the ended session never let its layer go")
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.releaseEnv(key)
}
