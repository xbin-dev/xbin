package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/builtins"
)

// covers the review of 2026-11-20 (instantiation) — a workspace template
// is a tile someone writes: instantiating it reads its files as they are.
// A link in it (to an admin's credentials) fails the copy and a FIFO
// doesn't block it; the secret is copied nowhere.
func TestInstantiateWorkspaceNeverReadsThroughALink(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "apps", "tpl")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "xbin.json"), []byte("{\"title\": \"t\", \"template\": {}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "homes", "admin", "creds")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("TOKEN-s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src, "index.html")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := instantiateWorkspace(root, "apps/tpl", "apps/copy", builtins.InstanceOpts{})
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("instantiating blocked")
	}
	if err == nil {
		t.Fatal("instantiated through a link")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "apps", "copy", "index.html")); strings.Contains(string(b), "s3cret") {
		t.Fatal("the secret was copied into the instance")
	}
}
