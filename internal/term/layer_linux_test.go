//go:build linux && integration

// Run with: go test -tags=integration -run '^TestConfined' ./internal/term/
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

// The test binary doubles as the sandbox's re-exec init (TestMain is
// agent_test.go's, which builds binaries first).
func init() {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
}

func layerRootfs(t *testing.T) string {
	t.Helper()
	fs := os.Getenv("XBIN_TEST_ROOTFS")
	if fs == "" {
		fs, _ = filepath.Abs("../../.rootfs")
	}
	for _, tool := range []string{"bin/bash", "usr/bin/find"} {
		if _, err := os.Stat(filepath.Join(fs, tool)); err != nil || !sandbox.Available() {
			t.Skip("no rootfs with bash and find, or no user namespaces")
		}
	}
	return fs
}

// A real, isolated terminal on apps/x (range mode where the host delegates a
// sub-uid range, as this box and the QA box do) leaves a tree owned by
// sub-uid 1000 in its layer's upper — what an apt install leaves — which
// xbind itself can't unlink. ResetEnv kills the session and removes the whole
// layer (confined, with the file capabilities).
func TestConfinedResetEnv(t *testing.T) {
	rootfs := layerRootfs(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	confine.Configure(rootfs)
	t.Cleanup(func() { // whatever a failure left: only a confined rm clears a sub-uid's files
		_ = confine.RemoveAll(context.Background(), root)
		confine.Configure("")
	})
	m := NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs

	ranged, _ := sandbox.IDMapStatus(os.Getuid(), os.Getgid())
	c := liveLayerSession(t, m, "apps/x", ranged)
	layer := filepath.Join(root, ".xbin", "term", termKey("apps/x"))
	owned := filepath.Join(layer, "upper", "opt", "owned", "sub", "f")
	if ranged {
		if err := os.Remove(owned); err == nil {
			t.Fatal("xbind unlinked the sub-uid's file itself: the test proves nothing")
		}
	} else {
		t.Log("single-uid host: no sub-uid-owned files in the layer")
	}

	if err := m.ResetEnv("apps/x"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	waitSessionEnd(t, c)
	if l := m.List(); len(l) != 0 {
		t.Fatalf("sessions left: %v", l)
	}
	if _, err := os.Lstat(layer); !os.IsNotExist(err) {
		t.Fatalf("the layer survived the reset: %v", err)
	}
}

// liveLayerSession opens an isolated terminal on rel through /ws/term, as the
// owner, and has it write /opt/owned/sub/f into its persistent layer's upper
// — chowned to uid 1000 when ranged. It returns the session's socket. (The
// broker's offload test, broker/offload_linux_test.go, opens the same one.)
func liveLayerSession(t *testing.T, m *Manager, rel string, ranged bool) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	}))
	t.Cleanup(srv.Close)
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/term?net=none&cwd="+rel, nil)
	if err != nil {
		body := ""
		if resp != nil {
			b := make([]byte, 512)
			n, _ := resp.Body.Read(b)
			body = string(b[:n])
		}
		t.Fatalf("open a terminal: %v %s", err, body)
	}
	t.Cleanup(func() { c.Close() })
	script := "mkdir -p /opt/owned/sub && echo x > /opt/owned/sub/f"
	if ranged {
		script += " && chown -R 1000:1000 /opt/owned"
	}
	// the marker is split in the typed line, so only the output holds it whole
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(script+" && echo LAYER-''WRITTEN\n")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !strings.Contains(out.String(), "LAYER-WRITTEN") {
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		mt, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("the session never wrote its layer (%q): %v", out.String(), err)
		}
		if mt == websocket.BinaryMessage {
			out.Write(b)
		} else if strings.Contains(string(b), `"op":"exit"`) {
			t.Fatalf("the session ended: %s (%q)", b, out.String())
		}
	}
	return c
}

// waitSessionEnd reads c until the session's exit frame or the socket's close.
func waitSessionEnd(t *testing.T, c *websocket.Conn) {
	t.Helper()
	for {
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
		mt, b, err := c.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("the session outlived its layer's removal")
			}
			return // closed
		}
		if mt == websocket.TextMessage {
			var ctl map[string]any
			if json.Unmarshal(b, &ctl) == nil && ctl["op"] == "exit" {
				return
			}
		}
	}
}
