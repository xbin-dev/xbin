package backup

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// covers T2 T11 (08-data §11.6) — TreeBeneath, the walk every new archive
// section uses, writes only the regular files beneath its directory, in name
// order and with today's header fields: a symlink out of the tree (to a
// file or a directory), an in-tree symlink, and a FIFO are skipped by their
// own type, never opened; keep selects files and prunes directories; a
// missing directory adds nothing.
func TestTreeBeneathNeverFollows(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for rel, body := range map[string]string{"b.txt": "bb\n", "a/one": "1\n", "a/two": "22\n", "skip/x": "x\n"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	for rel, target := range map[string]string{
		"leak":      filepath.Join(outside, "secret"),
		"leakdir":   outside,
		"a/inner":   "one",
		"a/escapes": "../../" + filepath.Base(outside) + "/secret",
	} {
		if err := os.Symlink(target, filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "a", "pipe"), 0o600); err != nil {
		t.Skipf("no FIFOs here: %v", err)
	}

	var buf bytes.Buffer
	w := NewWriter(&buf)
	keep := func(rel string, isDir bool) bool { return rel != "skip" }
	if err := w.TreeBeneath("p/", dir, keep); err != nil {
		t.Fatal(err)
	}
	if err := w.TreeBeneath("p/", filepath.Join(dir, "missing"), nil); err != nil {
		t.Fatalf("a missing directory: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	var got []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		if h.Typeflag != tar.TypeReg || h.Linkname != "" || h.Uid != 0 || h.ModTime.Unix() != 0 {
			t.Errorf("%s: header carries more than name, mode, size and type: %+v", h.Name, h)
		}
		got = append(got, fmt.Sprintf("%s %o %q", h.Name, h.Mode, body))
	}
	want := strings.Join([]string{`p/a/one 640 "1\n"`, `p/a/two 640 "22\n"`, `p/b.txt 640 "bb\n"`}, "\n")
	if strings.Join(got, "\n") != want {
		t.Errorf("TreeBeneath wrote\n%s\nwant\n%s", strings.Join(got, "\n"), want)
	}
}

// covers P5 — a manifest without a deployment section marshals as today's,
// with no deployments key; one with it names what the archive holds, and
// the section reads back.
func TestManifestDeploymentSection(t *testing.T) {
	plain, err := json.Marshal(Manifest{Schema: 1, Component: "apps/x"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte("deployments")) {
		t.Errorf("a manifest without a section carries one: %s", plain)
	}
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if err := w.Manifest(Manifest{Component: "apps/x", Deployments: &Deployments{Record: true}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if d := r.M.Deployments; r.M.Schema != 1 || d == nil || !d.Record || d.Checkpoints || d.Archives != nil {
		t.Errorf("read back schema %d, section %+v", r.M.Schema, d)
	}
}
