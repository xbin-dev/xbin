package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootfsBin(t *testing.T) {
	fs := t.TempDir()
	os.MkdirAll(filepath.Join(fs, "usr/local/node/bin"), 0o755)
	os.MkdirAll(filepath.Join(fs, "usr/bin"), 0o755)
	os.WriteFile(filepath.Join(fs, "usr/local/node/bin/node"), nil, 0o755)
	os.WriteFile(filepath.Join(fs, "usr/bin/python3"), nil, 0o755)
	if got := rootfsBin(fs, "node"); got != "/usr/local/node/bin/node" {
		t.Errorf("node at %s", got)
	}
	if got := rootfsBin(fs, "python3"); got != "/usr/bin/python3" {
		t.Errorf("python3 at %s", got)
	}
	if got := rootfsBin(fs, "ruby"); got != "/usr/bin/ruby" {
		t.Errorf("fallback %s", got)
	}
}
