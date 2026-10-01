//go:build linux && integration

// Run with: go test -tags=integration -run '^TestConfinedBaseAutoUpdate' ./internal/term/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs); skips otherwise.
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
	"github.com/xbin-dev/xbin/internal/layers"
)

// Real terminals on the real rootfs (D173). Three tiles, each with a layer
// that has a file in its upper (an apt install) and is stamped with an
// older base than the rootfs's: with base auto-update off, the session runs
// on that base — its file there, its stamp kept; with it on, the session's
// start moves the layer to the current base — the file gone, the stamp the
// rootfs's, and the shell's first output the grey line; and with it on, a
// layer whose base isn't installed at all moves the same way. The "older
// base" is the same rootfs under a second name, `<rootfs>-old1` beside a
// `<rootfs>` link (ResolveBase only looks for the dir), so the test needs
// no second image.
//
// Nothing here waits on time: each step reads the terminal until a marker
// only the command's output holds, and each session's end is the layer's
// release (the condition the next start needs), polled with a generous
// bound for a loaded machine.
func TestConfinedBaseAutoUpdate(t *testing.T) {
	real := layerRootfs(t)
	cur := layers.BaseVersion(real)
	if cur == "old1" || cur == "gone" {
		t.Skip("the rootfs is stamped with one of the test's names")
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

	type tile struct {
		rel, base string
		auto      bool
		wantMoved bool
	}
	tiles := []tile{
		{rel: "apps/off", base: "old1", auto: false},
		{rel: "apps/on", base: "old1", auto: true, wantMoved: true},
		{rel: "apps/gone", base: "gone", auto: true, wantMoved: true},
	}
	auto := map[string]bool{}
	for _, tl := range tiles {
		auto[termKey(tl.rel)] = tl.auto
	}
	m := NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs
	// The setting is the workspace's; per tile here only so one manager
	// serves both answers (the start reads it for the layer it claims).
	var claiming string
	m.BaseAutoUpdate = func() bool { return auto[claiming] }

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	}))
	t.Cleanup(srv.Close)

	for _, tl := range tiles {
		t.Run(tl.rel, func(t *testing.T) {
			key := termKey(tl.rel)
			layer := filepath.Join(root, ".xbin", "term", key)
			if err := os.MkdirAll(filepath.Join(root, tl.rel), 0o755); err != nil {
				t.Fatal(err)
			}
			installed := filepath.Join(layer, "upper", "opt", "installed")
			if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(installed, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := layers.Stamp(layer, layers.Stamps{Base: tl.base}); err != nil {
				t.Fatal(err)
			}

			claiming = key // sessions open one at a time: the hook answers for this tile
			c := dialTerm(t, srv.URL, tl.rel)
			out := runInTerm(t, c, "test -e /opt/installed && echo LAYER-''KEPT || echo LAYER-''FRESH", "LAYER-KEPT", "LAYER-FRESH")
			moved := strings.Contains(out, "terminal layer moved to the new base image")
			fresh := strings.Contains(out, "LAYER-FRESH")
			if moved != tl.wantMoved || fresh != tl.wantMoved {
				t.Fatalf("moved line %v, fresh layer %v; want both %v — output:\n%s", moved, fresh, tl.wantMoved, out)
			}
			if tl.wantMoved && strings.Index(out, "terminal layer moved") > strings.Index(out, "LAYER-FRESH") {
				t.Fatalf("the grey line isn't the session's first output:\n%s", out)
			}
			if !tl.wantMoved {
				if _, err := os.Stat(installed); err != nil {
					t.Fatalf("the kept layer lost its file: %v", err)
				}
			}
			want := tl.base
			if tl.wantMoved {
				want = cur
			}
			if s, err := layers.Read(layer); err != nil || s.Base != want {
				t.Fatalf("the layer is stamped %q (%v), want %q", s.Base, err, want)
			}
			endTerm(t, m, c, key)
		})
	}
}

// dialTerm opens a terminal on rel as the owner, offline.
func dialTerm(t *testing.T, base, rel string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 2 * time.Minute}
	c, resp, err := d.Dial("ws"+strings.TrimPrefix(base, "http")+"/ws/term?net=none&cwd="+rel, nil)
	if err != nil {
		body := ""
		if resp != nil {
			b := make([]byte, 512)
			n, _ := resp.Body.Read(b)
			body = string(b[:n])
		}
		t.Fatalf("open a terminal on %s: %v %s", rel, err, body)
	}
	t.Cleanup(func() { c.Close() })
	return c
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
	deadline := time.Now().Add(3 * time.Minute)
	for !m.acquireEnv(key) {
		if time.Now().After(deadline) {
			t.Fatal("the ended session never let its layer go")
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.releaseEnv(key)
}
