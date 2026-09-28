//go:build unix

package registry

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// covers D119g T2 — a checkpoint's xbin.json and scope.json are read through
// fsutil.OpenBeneath on its materialized tree, never followed out of it. An
// xbin.json or scope.json that is a symlink to .xbin/token (relative or
// absolute), a FIFO, or an oversized file is refused, so the deploy that
// would read it is refused; a view of it is refused; neither the errors nor
// the log carry the file's contents, and neither do parse errors or refused
// resource names. index.html and the native entry are found beneath the tree
// only. An in-tree symlink still resolves.
func TestCheckpointManifestReadNoFollow(t *testing.T) {
	const secret = "SECRET-TOKEN-7f3a9c"
	ws := t.TempDir()
	writeTree(t, ws, map[string]string{
		".xbin/token":         secret,
		"apps/crm/xbin.json":  `{"runtime":"static"}`,
		"apps/crm/index.html": `<p>work tree</p>`,
	})
	token := filepath.Join(ws, ".xbin", "token")
	deployDir := filepath.Join(ws, ".xbin", "deploy", "0123456789abcdef0123456789abcdef")
	n := 0
	tree := func(files map[string]string, links map[string]string) string {
		t.Helper()
		n++
		root := filepath.Join(deployDir, "tree"+string(rune('a'+n)))
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTree(t, root, files)
		for name, target := range links {
			p := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	r, err := Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	crm, _ := r.Component("apps/crm")
	noSecret := func(what, s string) {
		t.Helper()
		if strings.Contains(s, secret) {
			t.Errorf("%s carries the file's contents: %q", what, s)
		}
	}
	refused := func(what, root, wantIn string) {
		t.Helper()
		pc, err := ReadCheckpoint(root)
		if err == nil {
			t.Errorf("%s: read, want refused (%+v)", what, pc)
			return
		}
		noSecret(what+": the error", err.Error())
		if !strings.Contains(err.Error(), wantIn) {
			t.Errorf("%s: error %q, want it to say %q", what, err, wantIn)
		}
		_, verr := r.View(crm, ViewCode{Deployment: "dev", Tree: "t-" + filepath.Base(root), Root: root})
		if verr == nil {
			t.Errorf("%s: a view of the checkpoint", what)
		} else {
			noSecret(what+": the view's error", verr.Error())
		}
	}

	refused("xbin.json → ../../../token", tree(nil, map[string]string{"xbin.json": "../../../token"}), "xbin.json leaves the checkpoint")
	refused("xbin.json → /…/.xbin/token", tree(nil, map[string]string{"xbin.json": token}), "xbin.json leaves the checkpoint")
	refused("scope.json → ../../../token", tree(map[string]string{"xbin.json": `{}`}, map[string]string{"scope.json": "../../../token"}), "scope.json leaves the checkpoint")
	refused("a symlinked directory out", tree(nil, map[string]string{"conf": "../../..", "xbin.json": "conf/token"}), "leaves the checkpoint")
	refused("the tree is missing", filepath.Join(deployDir, "gone"), "missing")
	refused("oversized xbin.json", tree(map[string]string{"xbin.json": `{"setup":"` + strings.Repeat("x", checkpointFileMax) + `"}`}, nil), "larger than")
	refused("a resource name outside the rule", tree(map[string]string{"scope.json": `{"resources":{"../` + secret + `":{"type":"kv"},"ok":{"type":"kv"}}}`}, nil), "nothing is provisioned")
	refused("a scope.json that doesn't parse", tree(map[string]string{"scope.json": `{"resources": ` + secret + `}`}, nil), "scope.json does not parse")

	fifo := tree(nil, nil)
	if err := syscall.Mkfifo(filepath.Join(fifo, "xbin.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); refused("a FIFO xbin.json", fifo, "not a regular file") }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reading a FIFO xbin.json blocks")
	}

	// A manifest that doesn't parse is the zero manifest, its error content-free.
	pc, err := ReadCheckpoint(tree(map[string]string{"xbin.json": `{"runtime": ` + secret + `}`}, nil))
	if err != nil || pc.ManifestErr == "" || pc.Manifest.Runtime != "" {
		t.Fatalf("a manifest that doesn't parse: %+v, %v", pc, err)
	}
	noSecret("the parse error", pc.ManifestErr)

	// index.html and the native entry leaving the tree aren't there; an
	// in-tree symlink resolves; a directory named xbin.json is no manifest.
	root := tree(map[string]string{"conf/xbin.json": `{"runtime":"go","native":"./ui/main.js"}`},
		map[string]string{"xbin.json": "conf/xbin.json", "index.html": "../../../token", "ui/main.js": token})
	pc, err = ReadCheckpoint(root)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Manifest.Runtime != "go" || pc.HasIndex || pc.Native != "" || pc.NativeErr == "" {
		t.Errorf("in-tree symlinked manifest, escaping index.html and native entry: %+v", pc)
	}
	root = tree(map[string]string{"xbin.json/x": `{}`, "native.js": `export {}`}, map[string]string{"index.html": "native.js"})
	if pc, err = ReadCheckpoint(root); err != nil || pc.ManifestErr != "" || pc.Native != "native.js" || !pc.HasIndex {
		t.Errorf("a directory named xbin.json, an in-tree index.html link: %+v, %v", pc, err)
	}

	noSecret("the log", logs.String())
}
