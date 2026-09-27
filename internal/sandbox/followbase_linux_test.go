//go:build linux && integration

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// covers WP-2b — the spec terminals and backends now start with (NoFollow,
// FollowBase, RootHint), over both overlays: a mount point may pass a
// symlink the base rootfs ships — /lib → usr/lib, the absolute /var/run →
// /run — and lands inside the sandbox; one the sandbox's layer planted or
// retargeted fails the start naming the path and the hint, makes nothing
// where it points and doesn't wedge; a backend's env layer (a lower above
// the base) is the sandbox's too; a mask follows the workspace's own
// symlinks inside the sandbox, so a symlinked data/ is still hidden.
func TestFollowBaseMountPoints(t *testing.T) {
	if !Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	fuse := os.Getenv("XBIN_FUSE_OVERLAYFS")
	if !isExecutable(fuse) {
		fuse, _ = filepath.Abs("../../bin/fuse-overlayfs")
	}
	if !isExecutable(fuse) {
		fuse, _ = exec.LookPath("fuse-overlayfs")
	}
	for _, flavour := range []struct{ name, fuse string }{{"kernel", "none"}, {"fuse", fuse}} {
		t.Run(flavour.name, func(t *testing.T) {
			if !isExecutable(flavour.fuse) && flavour.fuse != "none" {
				t.Skip("no fuse-overlayfs (make fuse-overlayfs, XBIN_FUSE_OVERLAYFS or PATH)")
			}
			t.Setenv("XBIN_FUSE_OVERLAYFS", flavour.fuse)
			testFollowBase(t)
		})
	}
}

func testFollowBase(t *testing.T) {
	dir, lower, upper, work := probeRoot(t)
	outside, sdk, run, ws := filepath.Join(dir, "outside"), filepath.Join(dir, "sdk"), filepath.Join(dir, "run"), filepath.Join(dir, "ws")
	for _, d := range []string{outside, sdk, run, filepath.Join(lower, "usr", "lib"), filepath.Join(lower, "var"),
		filepath.Join(ws, ".data-real"), filepath.Join(ws, "tiles")} {
		mkdir(t, d)
	}
	for f, body := range map[string]string{
		filepath.Join(sdk, "go.mod"): "sdk", filepath.Join(run, "sock"): "run",
		filepath.Join(ws, ".data-real", "vault"): "secret", filepath.Join(ws, "tiles", "f"): "tile",
	} {
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, at string) {
		t.Helper()
		_ = os.Remove(at)
		if err := os.Symlink(target, at); err != nil {
			t.Fatal(err)
		}
	}
	link("usr/lib", filepath.Join(lower, "lib")) // the image's own
	link("/run", filepath.Join(lower, "var", "run"))
	link(".data-real", filepath.Join(ws, "data")) // the workspace's own: an operator's
	link("/elsewhere", filepath.Join(ws, "data2"))
	layers := 0
	// fresh is a new persistent layer, for every start after the first: an
	// earlier start made mount points in the last, and a kernel overlay's
	// upper stays busy (EBUSY) a moment after its sandbox exits
	fresh := func() {
		t.Helper()
		layers++
		upper, work = filepath.Join(dir, fmt.Sprintf("upper%d", layers)), filepath.Join(dir, fmt.Sprintf("work%d", layers))
		mkdir(t, upper)
		mkdir(t, work)
	}
	spec := func(ops ...string) *Spec {
		s := probeSpec(lower, upper, work, ops...)
		s.NoFollow, s.FollowBase, s.RootHint = true, true, "reset it"
		s.Binds = []Bind{
			{Src: sdk, Dst: "/lib/xbin/sdk", RO: true},
			{Src: run, Dst: "/var/run/xbin/run"},
			{Src: ws, Dst: "/ws", RO: true},
			{Dst: "/ws/data", Mask: true, RO: true},
			{Dst: "/ws/data2", Mask: true, RO: true},
		}
		return s
	}
	ok := func(s *Spec) {
		t.Helper()
		r, out, err := runProbe(t, s)
		if err != nil {
			t.Fatalf("start: %v\n%s", err, out)
		}
		for op, want := range map[string]string{
			"read:/lib/xbin/sdk/go.mod": "sdk", "read:/usr/lib/xbin/sdk/go.mod": "sdk",
			"read:/var/run/xbin/run/sock": "run", "read:/run/xbin/run/sock": "run",
			"read:/ws/tiles/f": "tile", "read:/ws/data/vault": "ERR", "read:/ws/.data-real/vault": "ERR",
		} {
			if got := r.Ops[op]; got != want {
				t.Errorf("%s = %q, want %q", op, got, want)
			}
		}
	}
	ops := []string{"read:/lib/xbin/sdk/go.mod", "read:/usr/lib/xbin/sdk/go.mod", "read:/var/run/xbin/run/sock",
		"read:/run/xbin/run/sock", "read:/ws/tiles/f", "read:/ws/data/vault", "read:/ws/.data-real/vault"}
	ok(spec(ops...))

	// without FollowBase (a tile sandbox) the image's own link is refused
	fresh()
	strict := spec()
	strict.FollowBase = false
	if _, out, err := runProbe(t, strict); err == nil || !strings.Contains(out, "nested mount point /lib: a symlink is in the way") {
		t.Errorf("strict NoFollow through the image's /lib: %v\n%s", err, out)
	}

	// the layer holding the image's link unchanged: still followed
	fresh()
	link("usr/lib", filepath.Join(upper, "lib"))
	ok(spec(ops...))

	// what the layer planted or retargeted: refused, with the hint
	for _, c := range []struct{ plant, target, want string }{
		{"var/run", outside, "nested mount point /var/run: a symlink is in the way (reset it)"},
		{"lib", outside, "nested mount point /lib: a symlink is in the way (reset it)"},
		{"lib", "../" + filepath.Base(outside), "nested mount point /lib: a symlink is in the way (reset it)"},
		{"proc", outside, "nested mount point /proc: a symlink is in the way (reset it)"},
		{"ws", outside, "nested mount point /ws: a symlink is in the way (reset it)"},
	} {
		fresh()
		mkdir(t, filepath.Join(upper, "var"))
		link(c.target, filepath.Join(upper, c.plant))
		start := time.Now()
		if _, out, err := runProbe(t, spec()); err == nil || !strings.Contains(out, c.want) {
			t.Errorf("planted /%s -> %s: %v, want %q in\n%s", c.plant, c.target, err, c.want, out)
		}
		if d := time.Since(start); d > 20*time.Second {
			t.Errorf("planted /%s: the refusal took %v", c.plant, d)
		}
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Fatalf("planted /%s: the init made %v where it points", c.plant, ents)
		}
	}
	fresh()

	// a backend's env layer is a lower above the base: its links are the
	// sandbox's, and only the base (the last lower) is the image
	env := filepath.Join(dir, "env")
	mkdir(t, filepath.Join(env, "var"))
	link(outside, filepath.Join(env, "var", "run"))
	s := spec()
	s.Lower = []string{env, lower}
	if _, out, err := runProbe(t, s); err == nil || !strings.Contains(out, "nested mount point /var/run: a symlink is in the way (reset it)") {
		t.Errorf("an env layer's /var/run: %v\n%s", err, out)
	}
	link("/run", filepath.Join(env, "var", "run")) // the image's own, re-planted as it is
	fresh()
	s = spec(ops...)
	s.Lower = []string{env, lower}
	ok(s)
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("made %v where a symlink points", ents)
	}

	// the workspace's own homes/ → another dir (an operator's symlink, directly
	// in a Layout bind's source) on another bind's path: followed inside the
	// sandbox, to a dir in the workspace or to another disk (a path the
	// sandbox's root then holds); without Layout it is refused, but without
	// the hint: the layer doesn't hold it, and a reset wouldn't clear it
	mkdir(t, filepath.Join(ws, ".homes-real", "u"))
	layoutSpec := func(ops ...string) *Spec {
		s := spec(ops...)
		s.Binds[2].Layout = true // the /ws bind
		s.Binds = append(s.Binds, Bind{Src: sdk, Dst: "/ws/homes/u"})
		return s
	}
	for _, target := range []string{".homes-real", outside} {
		fresh()
		link(target, filepath.Join(ws, "homes"))
		r, out, err := runProbe(t, layoutSpec("read:/ws/homes/u/go.mod", "read:/ws/tiles/f"))
		if err != nil {
			t.Fatalf("homes → %s: %v\n%s", target, err, out)
		}
		if got := r.Ops["read:/ws/homes/u/go.mod"]; got != "sdk" || r.Ops["read:/ws/tiles/f"] != "tile" {
			t.Errorf("homes → %s: $HOME's bind reads %q", target, got)
		}
		if ents, _ := os.ReadDir(outside); len(ents) != 0 {
			t.Fatalf("homes → %s: made %v on the host", target, ents)
		}
	}
	fresh()
	s = layoutSpec()
	s.Binds[2].Layout = false
	if _, out, err := runProbe(t, s); err == nil || !strings.Contains(out, "nested mount point /ws/homes: a symlink is in the way") ||
		strings.Contains(out, "reset it") {
		t.Errorf("a symlink in the bound workspace, no Layout: %v, want the refusal without the layer's hint in\n%s", err, out)
	}
	// deeper in the Layout bind — a tile's directory, which sandboxes write —
	// a link is refused, the same way
	link("../.data-real", filepath.Join(ws, "tiles", "child"))
	fresh()
	s = layoutSpec()
	s.Binds = append(s.Binds, Bind{Src: sdk, Dst: "/ws/tiles/child/x"})
	if _, out, err := runProbe(t, s); err == nil || !strings.Contains(out, "nested mount point /ws/tiles/child: a symlink is in the way") ||
		strings.Contains(out, "reset it") {
		t.Errorf("a symlink in a tile's directory: %v, want the refusal without the hint in\n%s", err, out)
	}

	// a file mount point the layer holds a symlink at (apt's nvidia-smi, a
	// Debian alternatives link; or one planted at a host file) is covered:
	// the bind shows the host's file, read-only, and the layer keeps its link
	// and its own target untouched
	fresh()
	smi, victim := filepath.Join(dir, "host-smi"), filepath.Join(outside, "victim")
	for f, body := range map[string]string{
		smi: "host-smi", victim: "victim", filepath.Join(upper, "usr", "lib", "nvidia", "current", "nvidia-smi"): "layer-smi",
	} {
		mkdir(t, filepath.Dir(f))
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(t, filepath.Join(upper, "usr", "bin"))
	mkdir(t, filepath.Join(upper, "etc", "alternatives"))
	link("/etc/alternatives/nvidia-smi", filepath.Join(upper, "usr", "bin", "nvidia-smi"))
	link("/usr/lib/nvidia/current/nvidia-smi", filepath.Join(upper, "etc", "alternatives", "nvidia-smi"))
	link(victim, filepath.Join(upper, "usr", "bin", "planted"))
	s = spec("read:/usr/bin/nvidia-smi", "write:/usr/bin/nvidia-smi", "read:/usr/lib/nvidia/current/nvidia-smi", "read:/usr/bin/planted")
	s.Binds = append(s.Binds, Bind{Src: smi, Dst: "/usr/bin/nvidia-smi", RO: true}, Bind{Src: smi, Dst: "/usr/bin/planted", RO: true})
	r, out, err := runProbe(t, s)
	if err != nil {
		t.Fatalf("file mount points on the layer's links: %v\n%s", err, out)
	}
	for op, want := range map[string]string{
		"read:/usr/bin/nvidia-smi": "host-smi", "write:/usr/bin/nvidia-smi": "ERR",
		"read:/usr/lib/nvidia/current/nvidia-smi": "layer-smi", "read:/usr/bin/planted": "host-smi",
	} {
		if got := r.Ops[op]; got != want {
			t.Errorf("%s = %q, want %q", op, got, want)
		}
	}
	for at, want := range map[string]string{"usr/bin/nvidia-smi": "/etc/alternatives/nvidia-smi", "usr/bin/planted": victim} {
		if got, err := os.Readlink(filepath.Join(upper, at)); err != nil || got != want {
			t.Errorf("the layer's %s is now %q (%v), want the link to %s", at, got, err, want)
		}
	}
	for f, want := range map[string]string{victim: "victim", filepath.Join(upper, "usr", "lib", "nvidia", "current", "nvidia-smi"): "layer-smi"} {
		if b, _ := os.ReadFile(f); string(b) != want {
			t.Errorf("%s = %q, want %q", f, b, want)
		}
	}
}
