package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The agent template's and the sandbox-terminal tile's tests run against a
// byte-identical copy of fsb.go (package main there too, hence the fsb
// prefixes): keep them one file.
func TestMirror(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("fsb.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, copyPath := range []string{
		"builtin-templates/agent/_backend/fsb_fake_test.go",
		"builtin-tiles/sandbox-terminal/backend/fsb_fake_test.go",
	} {
		cp, err := os.ReadFile(filepath.Join("..", "..", copyPath))
		if errors.Is(err, fs.ErrNotExist) {
			t.Logf("%s doesn't exist (yet): nothing to keep in step", copyPath)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(src, cp) {
			t.Errorf("%s differs from hack/fakesandbox/fsb.go — the reference manager is one file; from the repository root:\n\n\tcp hack/fakesandbox/fsb.go %s", copyPath, copyPath)
		}
	}
}
