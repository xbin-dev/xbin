package builtins

import (
	"testing"
	"testing/fstest"
)

// A bare `go build` inside a backend directory leaves `backend/backend`; that
// ELF must never be copied into a new component (xbind builds into
// .xbin/build/ itself). A cgi handler that happens to be a compiled
// executable is a real entry point and must survive.
func TestRenderTreeSkipsStrayBuildOutput(t *testing.T) {
	elf := append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1}, make([]byte, 64)...)
	src := fstest.MapFS{
		"t/xbin.json":          {Data: []byte(`{"runtime":"go"}`)},
		"t/backend/main.go":    {Data: []byte("package main\n")},
		"t/backend/backend":    {Data: elf}, // stray go build output
		"t/_backend/_backend":  {Data: elf}, // same, agent-template layout
		"t/backend/handler":    {Data: elf}, // a compiled cgi entry: keep
		"t/backend/backend.go": {Data: []byte("package main\n")},
		"t/bin/tool":           {Data: []byte("#!/bin/sh\necho hi\n")},
	}
	files, err := RenderTree(src, "t", "apps/t", "apps/t", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"backend/backend", "_backend/_backend"} {
		if _, ok := files[rel]; ok {
			t.Errorf("%s: stray build output was rendered", rel)
		}
	}
	for _, rel := range []string{"xbin.json", "backend/main.go", "backend/handler", "backend/backend.go", "bin/tool"} {
		if _, ok := files[rel]; !ok {
			t.Errorf("%s: missing from the rendered tree", rel)
		}
	}
}

func TestStrayBuildOutput(t *testing.T) {
	elf := []byte{0x7f, 'E', 'L', 'F', 0, 0}
	cases := []struct {
		rel  string
		data []byte
		want bool
	}{
		{"backend/backend", elf, true},
		{"_backend/_backend", elf, true},
		{"deep/dir/dir", elf, true},
		{"backend/backend", []byte("#!/bin/sh\n"), false}, // a script named like the dir
		{"backend/handler", elf, false},
		{"backend", elf, false}, // no directory component
		{"x/y", elf, false},
		{"backend/backend", []byte{0x7f, 'E'}, false}, // too short to be ELF
	}
	for _, c := range cases {
		if got := strayBuildOutput(c.rel, c.data); got != c.want {
			t.Errorf("strayBuildOutput(%q, %q) = %v, want %v", c.rel, c.data[:min(4, len(c.data))], got, c.want)
		}
	}
}
