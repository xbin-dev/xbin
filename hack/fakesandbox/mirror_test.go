package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// The agent template's tests run against a byte-identical copy of fsb.go
// (package main there too, hence the fsb prefixes): keep them one file.
func TestMirror(t *testing.T) {
	t.Parallel()
	const copyPath = "builtin-templates/agent/_backend/fsb_fake_test.go"
	src, err := os.ReadFile("fsb.go")
	if err != nil {
		t.Fatal(err)
	}
	cp, err := os.ReadFile(filepath.Join("..", "..", copyPath))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s doesn't exist (yet): nothing to keep in step", copyPath)
	}
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(src, cp) {
		t.Fatalf("%s differs from hack/fakesandbox/fsb.go — the reference manager is one file; from the repository root:\n\n\tcp hack/fakesandbox/fsb.go %s", copyPath, copyPath)
	}
}
