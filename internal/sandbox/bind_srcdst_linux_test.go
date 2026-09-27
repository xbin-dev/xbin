//go:build linux && integration

// Run with: go test -tags=integration ./internal/sandbox/
// Needs unprivileged user namespaces (skips if unavailable).
package sandbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covers P9 P16 T18 — a Bind whose Src differs from its Dst: the sandbox sees
// Src's content at Dst (a directory and a single file), read-only when RO —
// nothing written reaches the source — and read-write otherwise. Order
// follows sortBinds, not the list: a broad read-only bind listed after a
// deeper one never shadows it, and between equal destinations the later
// one wins. The deeper bind's mount point is missing from the broad bind's
// source, so the init makes it there (and remounts the broad bind read-only
// only after): the empty directory it leaves is the whole footprint.
func TestBindSrcNotDst(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	dir := t.TempDir()
	lower := filepath.Join(dir, "lower")
	mkdir(t, lower)
	buildBin(t, filepath.Join(lower, "probe"), bindProbeSrc)

	tree := func(name string, files map[string]string) string {
		p := filepath.Join(dir, name)
		mkdir(t, p)
		for f, body := range files {
			mkdir(t, filepath.Dir(filepath.Join(p, f)))
			if err := os.WriteFile(filepath.Join(p, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	deep := tree("deep", map[string]string{"a.txt": "from-deep"})
	broad := tree("broad", map[string]string{"b.txt": "from-broad"})
	rw := tree("rw", nil)
	first := tree("first", map[string]string{"f": "first"})
	second := tree("second", map[string]string{"f": "second"})
	file := filepath.Join(tree("files", map[string]string{"cfg": "from-file"}), "cfg")

	out, err := runBindProbe(t, lower, []Bind{
		{Src: deep, Dst: "/shown/here", RO: true},
		{Src: rw, Dst: "/state"},
		{Src: first, Dst: "/same", RO: true},
		{Src: second, Dst: "/same", RO: true},
		{Src: file, Dst: "/etc/xbin/cfg", RO: true},
		{Src: broad, Dst: "/shown", RO: true}, // listed last, mounted first
	},
		"read:/shown/here/a.txt", "read:/shown/b.txt", "write:/shown/here/new", "write:/shown/new",
		"write:/state/x", "read:/same/f", "read:/etc/xbin/cfg", "write:/etc/xbin/cfg")
	if err != nil {
		t.Fatalf("sandbox run: %v\n%s", err, out)
	}
	for op, want := range map[string]string{
		"read:/shown/here/a.txt": "from-deep",  // Src's content at another path, not shadowed by the later broad bind
		"read:/shown/b.txt":      "from-broad", // the broad bind itself
		"write:/shown/here/new":  "ERR",        // read-only
		"write:/shown/new":       "ERR",        // the broad bind is read-only too, once the nesting is done
		"write:/state/x":         "ok",         // read-write
		"read:/same/f":           "second",     // equal destinations: the later bind wins
		"read:/etc/xbin/cfg":     "from-file",  // a file bind, Src ≠ Dst
		"write:/etc/xbin/cfg":    "ERR",
	} {
		if got := out[op]; got != want {
			t.Errorf("%s = %q, want %q", op, got, want)
		}
	}
	if b, err := os.ReadFile(filepath.Join(rw, "x")); err != nil || string(b) != "w" {
		t.Errorf("a read-write write did not reach its source: %q %v", b, err)
	}
	for _, p := range []string{filepath.Join(deep, "new"), filepath.Join(broad, "new")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("a read-only bind let a write through to %s", p)
		}
	}
	if b, _ := os.ReadFile(file); string(b) != "from-file" {
		t.Errorf("a read-only file bind's source changed: %q", b)
	}
	ents, err := os.ReadDir(filepath.Join(broad, "here"))
	if err != nil || len(ents) != 0 {
		t.Errorf("the nested mount point in the broad bind's source: %v %v (want an empty directory)", ents, err)
	}
}

// covers T18 P16 — a bind that lands inside another bind's tree never
// follows a symlink to its mount point (a checkpoint can hold one where a
// nested component goes): an absolute or relative symlink above the mount
// point, or at it, fails the start naming the path, and nothing is made
// where the symlink points; a file in the way fails too. A missing mount
// point is made (a directory, or an empty file for a file bind) inside the
// read-only tree, whose read-only remount waits until the nested binds are
// mounted, and the nested bind keeps its own read-only flag.
func TestNestedBindMountPointNoFollow(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	dir := t.TempDir()
	lower := filepath.Join(dir, "lower")
	mkdir(t, lower)
	buildBin(t, filepath.Join(lower, "probe"), bindProbeSrc)

	outside := filepath.Join(dir, "outside") // where the checkpoint's symlinks point
	ckpt := filepath.Join(dir, "ckpt")       // bound read-only at /app
	nested := filepath.Join(dir, "nested")   // a nested component's own code
	state := filepath.Join(dir, "state")
	for _, d := range []string{outside, ckpt, nested, state, filepath.Join(ckpt, "d")} {
		mkdir(t, d)
	}
	for f, body := range map[string]string{filepath.Join(nested, "main.go"): "nested-code", filepath.Join(ckpt, "file"): "x",
		filepath.Join(dir, "cfg"): "from-file"} {
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{"abs": outside, "rel": "../outside", "d/up": "../../outside"} {
		if err := os.Symlink(target, filepath.Join(ckpt, link)); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct{ dst, want string }{
		{"/app/abs/sub", "nested mount point /app/abs: a symlink is in the way"},
		{"/app/rel/sub", "nested mount point /app/rel: a symlink is in the way"},
		{"/app/d/up/sub", "nested mount point /app/d/up: a symlink is in the way"},
		{"/app/abs", "nested mount point /app/abs: a symlink is in the way"},
		{"/app/file/sub", "nested mount point /app/file: not a directory"},
	} {
		t.Run(c.dst, func(t *testing.T) {
			out, err := runBindProbe(t, lower, []Bind{
				{Src: ckpt, Dst: "/app", RO: true},
				{Src: nested, Dst: c.dst, RO: true},
			}, "read:"+c.dst+"/main.go")
			if err == nil {
				t.Fatalf("the start succeeded through a symlink or file: %v", out)
			}
			if !strings.Contains(out["_output"], c.want) {
				t.Errorf("the failure does not name the path: want %q in\n%s", c.want, out["_output"])
			}
			if ents, _ := os.ReadDir(outside); len(ents) != 0 {
				t.Errorf("the init made %v where a symlink pointed", ents)
			}
		})
	}

	out, err := runBindProbe(t, lower, []Bind{
		{Src: ckpt, Dst: "/app", RO: true},
		{Src: nested, Dst: "/app/sub/comp", RO: true},
		{Src: filepath.Join(dir, "cfg"), Dst: "/app/d/cfg.json", RO: true},
		{Src: state, Dst: "/app/d/state"}, // read-write inside the read-only tree
	}, "read:/app/sub/comp/main.go", "read:/app/d/cfg.json", "write:/app/sub/comp/new", "write:/app/new", "write:/app/d/state/x")
	if err != nil {
		t.Fatalf("nested binds at missing mount points: %v\n%s", err, out["_output"])
	}
	for op, want := range map[string]string{
		"read:/app/sub/comp/main.go": "nested-code",
		"read:/app/d/cfg.json":       "from-file",
		"write:/app/sub/comp/new":    "ERR", // the nested bind keeps its read-only flag
		"write:/app/new":             "ERR", // the enclosing tree is read-only once the nesting is done
		"write:/app/d/state/x":       "ok",
	} {
		if got := out[op]; got != want {
			t.Errorf("%s = %q, want %q", op, got, want)
		}
	}
	for _, p := range []string{filepath.Join(ckpt, "sub", "comp"), filepath.Join(ckpt, "d", "state")} {
		if fi, err := os.Lstat(p); err != nil || !fi.IsDir() {
			t.Errorf("mount point %s: %v (want a directory)", p, err)
		}
	}
	if fi, err := os.Lstat(filepath.Join(ckpt, "d", "cfg.json")); err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
		t.Errorf("file mount point: %v (want an empty file)", err)
	}
	for _, p := range []string{filepath.Join(ckpt, "new"), filepath.Join(nested, "new")} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("a read-only bind let a write through to %s", p)
		}
	}
}

// covers PO-11 T18 — today's nested binds keep their meaning through the
// no-follow mount points: a tile terminal's list (term/binds.go) — the
// workspace read-only, .xbin and data under sealed covers, homes under an
// open one with the user's own $HOME nested back on top, the session's
// component read-write inside the read-only workspace, an unreadable tile
// covered — still hides what it hid and writes only where it wrote.
func TestNestedBindsTerminalShape(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	dir := t.TempDir()
	lower := filepath.Join(dir, "lower")
	mkdir(t, lower)
	buildBin(t, filepath.Join(lower, "probe"), bindProbeSrc)
	root := filepath.Join(dir, "ws")
	for _, d := range []string{".xbin", "data", "homes/u", "homes/v", "apps/x", "apps/y", "apps/z"} {
		mkdir(t, filepath.Join(root, d))
	}
	for f, body := range map[string]string{".xbin/token": "T", "data/vault": "D", "homes/v/secret": "V", "apps/y/code": "Y", "apps/z/code": "Z"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	at := func(rel string) string { return filepath.Join(root, rel) }
	out, err := runBindProbe(t, lower, []Bind{
		{Src: root, Dst: root, RO: true},
		{Dst: at(".xbin"), Mask: true, RO: true},
		{Dst: at("data"), Mask: true, RO: true},
		{Dst: at("homes"), Mask: true},
		{Src: at("homes/u"), Dst: at("homes/u")},
		{Src: at("apps/x"), Dst: at("apps/x")},
		{Dst: at("apps/z"), Mask: true, RO: true},
	}, "read:"+at(".xbin/token"), "read:"+at("data/vault"), "read:"+at("homes/v/secret"), "read:"+at("apps/y/code"),
		"read:"+at("apps/z/code"), "write:"+at("homes/u/w"), "write:"+at("apps/x/w"), "write:"+at("w"), "write:"+at("apps/y/w"))
	if err != nil {
		t.Fatalf("terminal-shaped sandbox: %v\n%s", err, out["_output"])
	}
	for op, want := range map[string]string{
		"read:" + at(".xbin/token"): "ERR", "read:" + at("data/vault"): "ERR", "read:" + at("homes/v/secret"): "ERR",
		"read:" + at("apps/y/code"): "Y", "read:" + at("apps/z/code"): "ERR",
		"write:" + at("homes/u/w"): "ok", "write:" + at("apps/x/w"): "ok", "write:" + at("w"): "ERR", "write:" + at("apps/y/w"): "ERR",
	} {
		if got := out[op]; got != want {
			t.Errorf("%s = %q, want %q", op, got, want)
		}
	}
}

// runBindProbe runs the bind probe with ops over a sandbox of lower and
// binds, returning its report ("_output" holds everything it printed).
func runBindProbe(t *testing.T, lower string, binds []Bind, ops ...string) (map[string]string, error) {
	t.Helper()
	spec := &Spec{
		Lower:   []string{lower},
		Binds:   binds,
		Entry:   "/probe",
		Argv:    append([]string{"/probe"}, ops...),
		Env:     []string{"PATH=/"},
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
	}
	cmd, h, err := Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Cleanup()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err = cmd.Start()
	if err == nil {
		if err = h.SetupUserns(); err != nil {
			_ = cmd.Process.Kill()
		}
		if werr := cmd.Wait(); err == nil {
			err = werr
		}
	}
	res := map[string]string{}
	if err == nil {
		if jerr := json.Unmarshal(lastJSONLine(buf.Bytes()), &res); jerr != nil {
			t.Fatalf("probe output not JSON: %v\n%s", jerr, buf.Bytes())
		}
	} else if strings.Contains(err.Error(), "operation not permitted") {
		t.Skipf("sandbox creation denied by environment: %v\n%s", err, buf.Bytes())
	}
	res["_output"] = buf.String()
	return res, err
}

// bindProbeSrc reports, per argument, what "read:<path>" read (or ERR) and
// whether "write:<path>" could write ("ok" or ERR).
const bindProbeSrc = `package main

import (
	"encoding/json"
	"os"
	"strings"
)

func main() {
	r := map[string]string{}
	for _, a := range os.Args[1:] {
		op, p, _ := strings.Cut(a, ":")
		switch op {
		case "read":
			if b, err := os.ReadFile(p); err != nil {
				r[a] = "ERR"
			} else {
				r[a] = string(b)
			}
		case "write":
			if err := os.WriteFile(p, []byte("w"), 0o644); err != nil {
				r[a] = "ERR"
			} else {
				r[a] = "ok"
			}
		}
	}
	json.NewEncoder(os.Stdout).Encode(r)
}
`
