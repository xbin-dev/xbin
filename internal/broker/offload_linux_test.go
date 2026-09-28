//go:build linux && integration

// Run with: go test -tags=integration -run '^TestConfined' ./internal/broker/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs from `make rootfs`); skips otherwise.
package broker

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
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/term"
)

// The test binary doubles as the sandbox's re-exec init.
func init() {
	if len(os.Args) > 2 && os.Args[1] == sandbox.InitArg {
		sandbox.RunInit(os.Args[2])
	}
}

// Offload-full of a tile whose terminal is open (WP-9b): the live, isolated
// session is killed before its layer goes, and the layer — with a tree an
// apt install would leave owned by sub-uid 1000, where the host delegates a
// sub-uid range — is removed whole, confined. The same session and check as
// term's TestConfinedResetEnv (term/layer_linux_test.go).
func TestConfinedOffloadFull(t *testing.T) {
	rootfs := os.Getenv("XBIN_TEST_ROOTFS")
	if rootfs == "" {
		rootfs, _ = filepath.Abs("../../.rootfs")
	}
	for _, tool := range []string{"bin/bash", "usr/bin/find"} {
		if _, err := os.Stat(filepath.Join(rootfs, tool)); err != nil || !sandbox.Available() {
			t.Skip("no rootfs with bash and find, or no user namespaces")
		}
	}
	const comp = "apps/calendar"
	b := testBroker(t)
	root := b.Reg.Root
	confine.Configure(rootfs)
	t.Cleanup(func() { // whatever a failure left: only a confined rm clears a sub-uid's files
		_ = confine.RemoveAll(context.Background(), root)
		confine.Configure("")
	})
	b.ProxyHandler = &fakeArchiver{}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"*": {archiveSlot: {{Ref: "apps/archiver"}}}}
	}); err != nil {
		t.Fatal(err)
	}
	m := term.NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs
	b.HoldTermEnv = m.HoldEnv // as boot wires it

	// A terminal on the tile writes its layer and stays open.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.ServeWS(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true})))
	}))
	defer srv.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/term?net=none&cwd="+comp, nil)
	if err != nil {
		t.Fatalf("open a terminal: %v", err)
	}
	defer c.Close()
	ranged, _ := sandbox.IDMapStatus(os.Getuid(), os.Getgid())
	script := "mkdir -p /opt/owned/sub && echo x > /opt/owned/sub/f"
	if ranged {
		script += " && chown -R 1000:1000 /opt/owned"
	}
	if err := c.WriteMessage(websocket.BinaryMessage, []byte(script+" && echo LAYER-''WRITTEN\n")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for !strings.Contains(out.String(), "LAYER-WRITTEN") {
		_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
		mt, data, err := c.ReadMessage()
		if err != nil {
			t.Fatalf("the session never wrote its layer (%q): %v", out.String(), err)
		}
		if mt == websocket.BinaryMessage {
			out.Write(data)
		}
	}
	layer := b.termDir(comp)
	if ranged {
		if err := os.Remove(filepath.Join(layer, "upper", "opt", "owned", "sub", "f")); err == nil {
			t.Fatal("xbind unlinked the sub-uid's file itself: the test proves nothing")
		}
	}
	if l := m.List(); len(l) != 1 {
		t.Fatalf("sessions: %v", l)
	}

	if err := b.offload(comp, true); err != nil {
		t.Fatalf("offload: %v", err)
	}
	if l := m.List(); len(l) != 0 {
		t.Fatalf("the session outlived the offload: %v", l)
	}
	for ended := false; !ended; {
		_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
		mt, data, err := c.ReadMessage()
		if err != nil {
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("the session's socket never ended")
			}
			break // closed
		}
		var ctl map[string]any
		ended = mt == websocket.TextMessage && json.Unmarshal(data, &ctl) == nil && ctl["op"] == "exit"
	}
	if _, err := os.Lstat(layer); !os.IsNotExist(err) {
		t.Fatalf("the layer survived the offload: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, comp, "index.html")); !os.IsNotExist(err) {
		t.Fatalf("the source survived the offload: %v", err)
	}
}
