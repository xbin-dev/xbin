package broker

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/builtins"
)

// covers the review of 2026-11-20 and its re-review (M3) — a workspace
// template is a tile someone writes: instantiating it reads its files as
// they are. A link in it (to an admin's credentials), a FIFO and a file
// over 32 MiB are not copied — each is named in skipped — and the rest is,
// so the instance is made; nothing blocks, and the secret is copied
// nowhere. A file's executable bit comes along (L4).
func TestInstantiateWorkspaceNeverReadsThroughALink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "apps", "tpl")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(src, 0o755))
	must(os.WriteFile(filepath.Join(src, "xbin.json"), []byte("{\"title\": \"t\", \"template\": {}}\n"), 0o644))
	must(os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\n"), 0o755))
	secret := filepath.Join(root, "homes", "admin", "creds")
	must(os.MkdirAll(filepath.Dir(secret), 0o755))
	must(os.WriteFile(secret, []byte("TOKEN-s3cret"), 0o600))
	must(os.Symlink(secret, filepath.Join(src, "index.html")))
	must(syscall.Mkfifo(filepath.Join(src, "pipe.txt"), 0o644))
	big, err := os.Create(filepath.Join(src, "big.bin"))
	must(err)
	must(big.Truncate(33 << 20)) // sparse: nothing is written
	big.Close()
	type res struct {
		path             string
		written, skipped []string
		err              error
	}
	done := make(chan res, 1)
	go func() {
		p, w, s, err := instantiateWorkspace(root, "apps/tpl", "apps/copy", builtins.InstanceOpts{})
		done <- res{p, w, s, err}
	}()
	var r res
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("instantiating blocked")
	}
	if r.err != nil || r.path != "apps/copy" {
		t.Fatalf("the instance wasn't made: %v", r.err)
	}
	said := strings.Join(r.skipped, "\n")
	for _, want := range []string{"index.html: a symbolic link", "pipe.txt: not a regular file", "big.bin: larger than 32 MiB"} {
		if !strings.Contains(said, want) {
			t.Errorf("not said %q:\n%s", want, said)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "apps", "copy", "index.html")); err == nil {
		t.Fatal("the link was copied")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "apps", "copy", "xbin.json")); !strings.Contains(string(b), `"title"`) {
		t.Fatalf("the rest wasn't copied: %q", b)
	}
	if fi, err := os.Stat(filepath.Join(root, "apps", "copy", "run.sh")); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("run.sh's mode: %v %v", fi, err)
	}
	filepath.WalkDir(filepath.Join(root, "apps", "copy"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "s3cret") {
				t.Errorf("the secret was copied into %s", p)
			}
		}
		return nil
	})
}
