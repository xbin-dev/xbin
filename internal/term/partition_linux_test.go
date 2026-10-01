//go:build linux && integration

// Run with: go test -tags=integration -run '^TestPartitionLayer' ./internal/term/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs from `make rootfs`); skips otherwise.
package term

import (
	"context"
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
	"github.com/xbin-dev/xbin/internal/util"
)

// runIn opens an isolated terminal on rel as p and runs script in it; it
// returns the output up to the marker the script's last command prints.
func runIn(t *testing.T, m *Manager, p auth.Principal, rel, script string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	}))
	defer srv.Close()
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
	defer c.Close()
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(script+"; echo RUN-''DONE\n")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !strings.Contains(out.String(), "RUN-DONE") {
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		mt, b, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("the session never finished (%q): %v", out.String(), err)
		}
		if mt == websocket.BinaryMessage {
			out.Write(b)
		} else if strings.Contains(string(b), `"op":"exit"`) {
			t.Fatalf("the session ended: %s (%q)", b, out.String())
		}
	}
	return out.String()
}

// covers PD-22 S16 — in a real sandbox (06 §Tests term): a person's
// terminal on a partitioned tile starts in their $HOME, says its partition
// in XBIN_PARTITION, and writes its system changes to their own layer
// (.xbin/term-part/<TileKey>/<pkey>), never the tile's; another person's
// terminal there doesn't see them; a switch's wipe removes both layers
// (confined) and keeps the tile's own.
func TestPartitionLayerIsolated(t *testing.T) {
	rootfs := layerRootfs(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	confine.Configure(rootfs)
	t.Cleanup(func() {
		_ = confine.RemoveAll(context.Background(), root)
		confine.Configure("")
	})
	m := NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs
	(&partHooks{}).install(m)

	ana, bob := termAdmin("ana"), termAdmin("bob")
	out := runIn(t, m, ana, "apps/p", `mkdir -p /opt/mark && echo ana > /opt/mark/f; echo "cwd=$(pwd) part=$XBIN_PARTITION"`)
	if want := "cwd=" + HomeDir(root, "ana") + " part=user:ana"; !strings.Contains(out, want) {
		t.Errorf("ana's terminal: want %q in %q", want, out)
	}
	anaLayer := filepath.Join(root, ".xbin", "term-part", util.TileKey("apps/p"), "k-ana")
	if b, err := os.ReadFile(filepath.Join(anaLayer, "upper", "opt", "mark", "f")); err != nil || strings.TrimSpace(string(b)) != "ana" {
		t.Errorf("ana's change isn't in her layer: %v %q", err, b)
	}
	if _, err := os.Lstat(filepath.Join(root, ".xbin", "term", termKey("apps/p"), "upper", "opt", "mark")); err == nil {
		t.Error("ana's change landed in the tile's own layer")
	}
	out = runIn(t, m, bob, "apps/p", `ls /opt/mark/f 2>&1; echo "part=$XBIN_PARTITION"`)
	if !strings.Contains(out, "No such file") || !strings.Contains(out, "part=user:bob") {
		t.Errorf("bob's terminal sees ana's layer, or isn't his partition: %q", out)
	}
	if err := m.StopTileSessions("apps/p"); err != nil {
		t.Fatal(err)
	}
	got, err := m.WipePartitionTile("apps/p", false)
	if err != nil || got.Layers != 2 {
		t.Fatalf("wipe: %+v %v", got, err)
	}
	if _, err := os.Lstat(anaLayer); !os.IsNotExist(err) {
		t.Errorf("ana's layer survived the wipe: %v", err)
	}
}
