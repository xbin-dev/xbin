//go:build linux

package sidecar

import (
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

// withEmbedded makes this test binary the "embedded" sidecar (TestMain
// turns it into fakeiroh when run as xbin-iroh).
func withEmbedded(t *testing.T) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	var gz bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&gz, gzip.BestSpeed)
	_, _ = zw.Write(bin)
	_ = zw.Close()
	old := embedded
	embedded = gz.Bytes()
	t.Cleanup(func() { embedded = old })
	t.Setenv("XBIN_IROH", "")
	return bin
}

func TestExtractToMemfd(t *testing.T) {
	bin := withEmbedded(t)
	x, err := Options{}.openExe("")
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	if x.how != "memfd" {
		t.Skipf("this kernel won't exec a memfd here (%s); the fallbacks are tested below", x.how)
	}
	seals, err := unix.FcntlInt(x.f.Fd(), unix.F_GET_SEALS, 0)
	want := unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if err != nil || seals&want != want {
		t.Fatalf("seals %#x %v, want all four", seals, err)
	}
	if fi, err := x.f.Stat(); err != nil || fi.Size() != int64(len(bin)) {
		t.Fatalf("memfd size %v %v", fi, err)
	}
	if _, err := x.f.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("wrote into a sealed memfd")
	}

	// and it runs from there
	s := New(Options{RunDir: t.TempDir(), noSandbox: true})
	defer s.Stop()
	if err := s.Start(context.Background(), Config{Key: testKey(1), Relay: wire.Relay{Mode: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	if st := waitState(t, s, StateRunning); st.Exec != "memfd" {
		t.Fatalf("exec %q", st.Exec)
	}
}

// When a memfd can't exec (vm.memfd_noexec, an LSM), the binary goes to
// <RunDir>/iroh/, then <ws>/.xbin/iroh/ (a /run mounted noexec); an
// identical copy is reused, a different one replaced.
func TestExtractFallbacks(t *testing.T) {
	bin := withEmbedded(t)
	o := Options{RunDir: t.TempDir(), Workspace: t.TempDir()}
	x, err := o.openExe("rundir")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(o.RunDir, "iroh", "xbin-iroh")
	if x.how != "rundir" {
		t.Fatalf("how %q", x.how)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o700 || fi.Size() != int64(len(bin)) {
		t.Fatalf("%s: %v %v", path, fi, err)
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode())
	}
	x.close()
	// reused when identical
	ino := func(p string) uint64 {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err != nil {
			t.Fatal(err)
		}
		return st.Ino
	}
	before := ino(path)
	x, err = o.openExe("rundir")
	if err != nil || ino(path) != before {
		t.Fatalf("an identical copy was rewritten (%v)", err)
	}
	x.close()
	// replaced when different
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	x, err = o.openExe("rundir")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, bin) {
		t.Fatal("a different binary was kept")
	}
	x.close()

	// no run dir: the workspace's .xbin
	o.RunDir = ""
	x, err = o.openExe("rundir")
	if err != nil {
		t.Fatal(err)
	}
	defer x.close()
	if x.how != "workspace" {
		t.Fatalf("how %q", x.how)
	}
	if _, err := os.Stat(filepath.Join(o.Workspace, ".xbin", "iroh", "xbin-iroh")); err != nil {
		t.Fatal(err)
	}
	if nextKind("memfd") != "rundir" || nextKind("rundir") != "workspace" || nextKind("workspace") != "" || nextKind("path") != "" {
		t.Fatal("nextKind order")
	}
}

func TestExtractPath(t *testing.T) {
	self, _ := os.Executable()
	x, err := Options{Binary: self}.openExe("")
	if err != nil {
		t.Fatal(err)
	}
	x.close()
	if x.how != "path" {
		t.Fatalf("how %q", x.how)
	}
	if _, err := (Options{Binary: filepath.Join(t.TempDir(), "none")}).openExe(""); err == nil {
		t.Fatal("a missing XBIN_IROH opened")
	}
	noexec := filepath.Join(t.TempDir(), "noexec")
	_ = os.WriteFile(noexec, []byte("x"), 0o600)
	if _, err := (Options{Binary: noexec}).openExe(""); err == nil {
		t.Fatal("a non-executable XBIN_IROH was accepted")
	}
}

// Built with -tags xbin_iroh (make build), the embedded gzip is a static
// ELF for this arch.
func TestEmbedded(t *testing.T) {
	if len(embedded) == 0 {
		t.Skip("built without the sidecar (make build embeds it)")
	}
	bin, err := gunzip(embedded)
	if err != nil {
		t.Fatal(err)
	}
	f, err := elf.NewFile(bytes.NewReader(bin))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[runtime.GOARCH]
	if f.Machine != want {
		t.Fatalf("embedded %v, want %v", f.Machine, want)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			t.Fatal("the embedded sidecar is dynamically linked: the bare sandbox has no libc")
		}
	}
}
