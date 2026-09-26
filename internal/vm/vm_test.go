package vm

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

func TestExportsFromBinds(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws")
	tile := filepath.Join(ws, "tiles", "t1")
	os.MkdirAll(tile, 0o755)
	file := filepath.Join(dir, "bx")
	os.WriteFile(file, []byte("x"), 0o755)
	sock := filepath.Join(dir, "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	got, err := exports([]sandbox.Bind{
		{Src: tile, Dst: tile},                        // nested: sorted after its parent
		{Src: ws, Dst: ws, RO: true},                  // parent
		{Dst: filepath.Join(ws, ".xbin"), Mask: true}, // masks stay host-side
		{Src: sock, Dst: sock},                        // sockets can't cross 9P
		{Src: file, Dst: "/opt/xbin/bin/bx", RO: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[len(got)-1].Path != tile {
		t.Fatalf("exports = %+v, want 3 with %s (nested) last and no mask/socket", got, tile)
	}
	for _, m := range got {
		switch m.Path {
		case ws:
			if !m.RO || m.File {
				t.Errorf("workspace export %+v", m)
			}
		case tile:
			if m.RO || m.File {
				t.Errorf("tile export %+v", m)
			}
		case "/opt/xbin/bin/bx":
			if !m.File || !m.RO {
				t.Errorf("file export %+v", m)
			}
		}
	}

	if _, err := exports([]sandbox.Bind{{Src: "/dev/null", Dst: "/dev/null"}}); err == nil {
		t.Errorf("a device node was exported")
	}
	if _, err := exports([]sandbox.Bind{{Src: ws, Dst: "/"}}); err == nil {
		t.Errorf("/ was exported")
	}
	if _, err := exports([]sandbox.Bind{{Src: ws, Dst: sandbox.VMDir + "/run"}}); err == nil {
		t.Errorf("the VM plumbing dir was exported")
	}
}

func TestNewcCpio(t *testing.T) {
	var b bytes.Buffer
	writeNewc(&b, "init", 0o100755, []byte("abc"))
	writeNewc(&b, "TRAILER!!!", 0, nil)
	s := b.String()
	if !strings.HasPrefix(s, "070701") || !strings.Contains(s, "init\x00") || !strings.Contains(s, "TRAILER!!!\x00") {
		t.Fatalf("bad cpio: %q", s)
	}
	if b.Len()%4 != 0 {
		t.Fatalf("cpio not 4-byte aligned: %d", b.Len())
	}
	// header (110) + "init\0" (5) → pad to 116; data 3 → pad to 4
	if s[116:119] != "abc" {
		t.Fatalf("file data misplaced: %q", s[110:124])
	}
}
