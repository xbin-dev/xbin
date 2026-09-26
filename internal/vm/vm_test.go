package vm

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
		{Src: dir, Dst: "/run/local"},                 // under a local dir: the guest's own
		{Src: tile, Dst: tile},                        // nested: sorted after its parent
		{Src: ws, Dst: ws, RO: true},                  // parent
		{Dst: filepath.Join(ws, ".xbin"), Mask: true}, // masks stay host-side
		{Src: sock, Dst: sock},                        // sockets can't cross 9P
		{Src: file, Dst: "/opt/xbin/bin/bx", RO: true},
	}, []string{"/run/local"})
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

	if _, err := exports([]sandbox.Bind{{Src: "/dev/null", Dst: "/dev/null"}}, nil); err == nil {
		t.Errorf("a device node was exported")
	}
	if _, err := exports([]sandbox.Bind{{Src: ws, Dst: "/"}}, nil); err == nil {
		t.Errorf("/ was exported")
	}
	if _, err := exports([]sandbox.Bind{{Src: ws, Dst: sandbox.VMDir + "/run"}}, nil); err == nil {
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

func TestDecideVMM(t *testing.T) {
	ok := func() error { return nil }
	noKVM := func() error { return errors.New("/dev/kvm is missing") }
	noEmu := func() error { return errors.New("emulation needs qemu-system-x86_64") }
	for _, c := range []struct {
		name, force  string
		kvm, emu     func() error
		avail, emul  bool
		reason, note string
	}{
		{"kvm wins", "", ok, ok, true, false, "", ""},
		{"no kvm: emulate", "", noKVM, ok, true, true, "", "no KVM (/dev/kvm is missing)"},
		{"neither", "", noKVM, noEmu, false, false, "/dev/kvm is missing; emulation needs qemu-system-x86_64", ""},
		{"forced kvm, none", "kvm", noKVM, ok, false, false, "/dev/kvm is missing", ""},
		{"forced emulate", "emulate", ok, ok, true, true, "", "XBIN_VM_ACCEL=emulate"},
		{"forced emulate, none", "emulate", ok, noEmu, false, false, "emulation needs", ""},
	} {
		st := decide(c.force, c.kvm, c.emu)
		if st.Available != c.avail || st.Emulated != c.emul || !strings.Contains(st.Reason, c.reason) ||
			!strings.Contains(st.Note, c.note) || (c.reason == "") != (st.Reason == "") {
			t.Errorf("%s: got %+v", c.name, st)
		}
	}
}

// An initrd is whole pages, whatever the agent's size: QEMU's microvm would
// otherwise load its tail over the ACPI tables in the top page of RAM.
func TestInitrdWholePages(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []int{1, 4000, 5836040, 5838360} {
		agent := filepath.Join(dir, "agent")
		if err := os.WriteFile(agent, bytes.Repeat([]byte{0x7f}, n), 0o755); err != nil {
			t.Fatal(err)
		}
		m := &Manager{Root: filepath.Join(dir, "ws", strconv.Itoa(n)), assets: Assets{Agent: agent}}
		p, err := m.initrd()
		if err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Size()%4096 != 0 {
			t.Errorf("agent of %d bytes: initrd of %d bytes is not whole pages", n, fi.Size())
		}
	}
}
