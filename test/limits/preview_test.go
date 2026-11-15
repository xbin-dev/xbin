//go:build linux && integration

package limits

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// A preview's browser runs in a cgroup leaf of its own with a memory cap
// (D234, XBIN_PREVIEW_MEM): a page that eats memory is killed there — the
// answer is 507, xbind and the next render are untouched.
func TestPreviewMemoryCap(t *testing.T) {
	a := xbindtest.Require(t)
	if _, err := os.Stat(filepath.Join(a.Rootfs, "usr/local/ms-playwright")); err != nil {
		t.Skip("the rootfs has no Chromium")
	}
	base := xbindCgroup(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Cgroup: base, Auth: true, Env: []string{"XBIN_PREVIEW_MEM=320M"}})
	d.WriteTile(t, "apps/hog", map[string]string{"xbin.json": "{}", "index.html": `<!doctype html><meta name="xbin-ready" content="manual"><title>hog</title><h1>Hog</h1>
<script type="module">
const hog = [];
for (;;) { const a = new Uint8Array(24 << 20); a.fill(1); hog.push(a); await new Promise((r) => setTimeout(r, 2)); }
</script>`})
	d.WriteTile(t, "apps/ok", map[string]string{"xbin.json": "{}", "index.html": `<!doctype html><title>ok</title><h1>Fine</h1>`})
	d.WaitComponent(t, "apps/hog")
	d.WaitComponent(t, "apps/ok")
	d.AddUser(t, "ann", "ann-password-1", "user", map[string]string{"apps/hog": "read", "apps/ok": "read"})
	ann := xbindtest.H("Authorization", "Bearer "+d.Login(t, "ann", "ann-password-1"))

	var st struct {
		Available, MemoryCap bool
		MemMax               int64
	}
	d.Must(t, http.MethodGet, "/api/xbin/preview", nil, 200, ann).Decode(t, &st)
	if !st.Available || !st.MemoryCap || st.MemMax != 320<<20 {
		t.Fatalf("status: %+v", st)
	}
	r := d.Call(t, http.MethodPost, "/api/xbin/preview", map[string]any{"tile": "apps/hog"}, ann)
	if r.Status != http.StatusInsufficientStorage || !strings.Contains(r.String(), "ran out of memory") || !strings.Contains(r.String(), "320 MiB") {
		t.Fatalf("a page that eats memory: %d %s", r.Status, r)
	}
	r = d.Call(t, http.MethodPost, "/api/xbin/preview", map[string]any{"tile": "apps/ok"}, ann)
	var ok struct{ Snapshot string }
	if r.Status == 200 {
		r.Decode(t, &ok)
	}
	if !strings.Contains(ok.Snapshot, "Fine") {
		t.Fatalf("the render after it: %d %q", r.Status, ok.Snapshot)
	}
	// no leaf is left behind
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		if strings.Contains(e.Name(), "preview-") {
			t.Errorf("a render's cgroup leaf is left: %s", e.Name())
		}
	}
}
