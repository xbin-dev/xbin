//go:build linux

package assetscan

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO in a tile — or a symlink to one — never blocks a scan (GET
// /api/xbin/tile-assets, bx doctor), and is not reported as an escape.
func TestScanSkipsFIFOs(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"apps/t/xbin.json": `{}`, "apps/t/a.js": `import '/c/apps/t/b.js'`})
	dir := filepath.Join(root, "apps/t")
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.js"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if err := os.Symlink("pipe.js", filepath.Join(dir, "x.js")); err != nil {
		t.Fatal(err)
	}
	done := make(chan Report, 1)
	go func() {
		rep, err := Scan(dir, "apps/t", Options{Root: root})
		if err != nil {
			t.Error(err)
		}
		done <- rep
	}()
	select {
	case rep := <-done:
		for _, f := range rep.Findings {
			if f.File == "x.js" || f.File == "pipe.js" {
				t.Errorf("FIFO reported: %+v", f)
			}
		}
		if rep.Files != 1 {
			t.Errorf("scanned %d files, want 1", rep.Files)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the scan blocked on a FIFO")
	}
}
