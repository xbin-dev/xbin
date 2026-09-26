//go:build linux

package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// A FIFO — or a symlink to one — in a tile never blocks xbind: not the /c/
// plane in any mode, not the tile asset report (bx doctor). Review PoC:
// every such request hung, leaking a goroutine and an fd each.
func TestFIFOsNeverBlock(t *testing.T) {
	for _, mode := range allModes {
		w := newAssetWS(t, mode)
		if err := syscall.Mkfifo(filepath.Join(w.root, "apps/a/pipe.js"), 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		if err := os.Symlink("pipe.js", filepath.Join(w.root, "apps/a/x.js")); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, p := range []string{"/c/apps/a/pipe.js", "/c/apps/a/x.js"} {
				if r := w.assetReq(mode, p, "ana"); r.StatusCode != 404 {
					t.Errorf("%s %s: %d", mode, p, r.StatusCode)
				}
			}
			r := httptest.NewRequest("GET", "/api/xbin/tile-assets?component=apps/a", nil)
			acc, _ := w.st.Access("ana")
			r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "ana", Access: acc, Via: "session"}))
			rec := httptest.NewRecorder()
			w.s.apiTileAssets(rec, r)
			var out map[string]any
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
				t.Errorf("%s report: %d", mode, rec.Code)
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: a request blocked on a FIFO", mode)
		}
	}
}
