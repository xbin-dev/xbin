package confine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// covers D119c D119g SC-ZERO Z8 PO-11 — every confined run xbind makes today keeps
// its bind list: the working directory bound at itself, first, read-only
// exactly when the caller says ReadOnlyDir, and the caller's binds after it,
// verbatim and in order. The rows are the shapes today's users build (the
// site is named on each), so a new bind destination (a host path shown at
// Dir) must default to Dir itself for all of them. A run without isolation
// ignores the binds, as today, so each shape also still runs directly — a
// caller whose bind already lands elsewhere (the git import's ~/.ssh) must
// not start failing on a host without --isolate. The expectations are
// hand-maintained goldens: changing one is a compat change (12-compat).
func TestConfineBindDestinationDefault(t *testing.T) {
	// the bind helpers themselves
	for _, c := range []struct {
		got, want sandbox.Bind
	}{
		{RO("/p"), sandbox.Bind{Src: "/p", Dst: "/p", RO: true}},
		{RW("/p"), sandbox.Bind{Src: "/p", Dst: "/p"}},
		{Mask("/p"), sandbox.Bind{Dst: "/p", Mask: true, RO: true}},
		{MaskOpen("/p"), sandbox.Bind{Dst: "/p", Mask: true}},
	} {
		if c.got != c.want {
			t.Errorf("helper: got %+v, want %+v", c.got, c.want)
		}
	}

	tmp := t.TempDir()
	dir := filepath.Join(tmp, "tile")    // a tile's work tree, a clone target, a git dir
	root := filepath.Join(tmp, "ws")     // the workspace
	other := filepath.Join(tmp, "other") // a second host path a run reads
	for _, d := range []string{dir, root, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rows := []struct {
		site string
		cmd  Cmd
		want []sandbox.Bind
	}{
		{"broker/code.go runGitIn, boot/workspace.go repo init, builtins/updates.go (Git, nil binds)",
			Cmd{Dir: dir},
			[]sandbox.Bind{{Src: dir, Dst: dir}}},
		{"broker/templaterepo.go fetch from a template repo (runGitWith)",
			Cmd{Dir: dir, Binds: []sandbox.Bind{RO(other)}},
			[]sandbox.Bind{{Src: dir, Dst: dir}, {Src: other, Dst: other, RO: true}}},
		{"GitRead",
			Cmd{Dir: dir, ReadOnlyDir: true},
			[]sandbox.Bind{{Src: dir, Dst: dir, RO: true}}},
		{"broker/gitimport.go remoteGit, with the daemon's ~/.ssh",
			Cmd{Dir: dir, Net: NetHost, Binds: []sandbox.Bind{{Src: filepath.Join(other, ".ssh"), Dst: "/root/.ssh", RO: true}}},
			[]sandbox.Bind{{Src: dir, Dst: dir}, {Src: filepath.Join(other, ".ssh"), Dst: "/root/.ssh", RO: true}}},
		{"broker/gitimport.go remoteGit, no ~/.ssh",
			Cmd{Dir: dir, Net: NetHost},
			[]sandbox.Bind{{Src: dir, Dst: dir}}},
		{"term/agentdiff.go (a private git dir over a read-only work tree)",
			Cmd{Dir: dir, Binds: []sandbox.Bind{RO(other)}},
			[]sandbox.Bind{{Src: dir, Dst: dir}, {Src: other, Dst: other, RO: true}}},
		{"term/agentdiff_full.go",
			Cmd{Dir: dir, ReadOnlyDir: true, MaxOutput: 1 << 20},
			[]sandbox.Bind{{Src: dir, Dst: dir, RO: true}}},
		{"runner/build.go buildConfined (the tile read-only, secrets masked, its own caches)",
			Cmd{Dir: dir, ReadOnlyDir: true, Net: NetInternet, Binds: []sandbox.Bind{
				RO("/usr/lib/go"), RO(root),
				MaskOpen(filepath.Join(root, ".xbin")), Mask(filepath.Join(root, "data")), Mask(filepath.Join(root, "homes")),
				RW(filepath.Join(root, ".xbin/build/k")), RW(filepath.Join(root, ".xbin/cache/tile/k/go-build")),
				RW(filepath.Join(root, ".xbin/cache/tile/k/mod")), RO("/go/pkg/mod/cache/download"), RO("/opt/sdk"),
			}},
			[]sandbox.Bind{
				{Src: dir, Dst: dir, RO: true},
				{Src: "/usr/lib/go", Dst: "/usr/lib/go", RO: true},
				{Src: root, Dst: root, RO: true},
				{Dst: filepath.Join(root, ".xbin"), Mask: true},
				{Dst: filepath.Join(root, "data"), Mask: true, RO: true},
				{Dst: filepath.Join(root, "homes"), Mask: true, RO: true},
				{Src: filepath.Join(root, ".xbin/build/k"), Dst: filepath.Join(root, ".xbin/build/k")},
				{Src: filepath.Join(root, ".xbin/cache/tile/k/go-build"), Dst: filepath.Join(root, ".xbin/cache/tile/k/go-build")},
				{Src: filepath.Join(root, ".xbin/cache/tile/k/mod"), Dst: filepath.Join(root, ".xbin/cache/tile/k/mod")},
				{Src: "/go/pkg/mod/cache/download", Dst: "/go/pkg/mod/cache/download", RO: true},
				{Src: "/opt/sdk", Dst: "/opt/sdk", RO: true},
			}},
		{"vm/image.go rootfs image build",
			Cmd{Dir: dir, Binds: []sandbox.Bind{RO("/opt/vm/mkfs.ext4"), RO(other)}},
			[]sandbox.Bind{{Src: dir, Dst: dir}, {Src: "/opt/vm/mkfs.ext4", Dst: "/opt/vm/mkfs.ext4", RO: true}, {Src: other, Dst: other, RO: true}}},
		{"no Dir: nothing is bound for it",
			Cmd{Binds: []sandbox.Bind{RO(other)}},
			[]sandbox.Bind{{Src: other, Dst: other, RO: true}}},
	}
	for _, r := range rows {
		if got := r.cmd.binds(); !reflect.DeepEqual(got, r.want) {
			t.Errorf("%s: bind list changed\n got: %+v\nwant: %+v", r.site, got, r.want)
		}
	}

	// Without isolation the same shapes run directly, in Dir, binds ignored.
	if Isolated() {
		t.Fatal("confinement is on in a unit test")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh for the direct-run half")
	}
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.cmd.Dir == "" {
			continue
		}
		c := r.cmd
		c.Argv = []string{"sh", "-c", "pwd -P"}
		res, err := Run(context.Background(), c)
		if err != nil {
			t.Errorf("%s: a direct run of today's shape failed: %v", r.site, err)
			continue
		}
		if got := strings.TrimSpace(string(res.Stdout)); got != want {
			t.Errorf("%s: a direct run ran in %q, want %q", r.site, got, want)
		}
	}
}

// covers D119h T12 D119g PO-11 — direct mode (isolation off) never runs a job on
// the work tree when it asked for another path at its destination: a Cmd
// whose DirFrom names another directory than Dir, or that carries a bind
// made by At (even one whose source and destination are one path), returns
// ErrNeedsIsolation and runs nothing — neither in Dir, nor in DirFrom, nor
// anywhere else. DirFrom spelling out Dir itself, and a bind the caller
// builds by hand whatever its destination (the git import's ~/.ssh), keep
// today's direct run. In a sandbox the bind list shows DirFrom at Dir —
// read-only exactly with ReadOnlyDir, still first — and At's binds reach the
// sandbox unmarked, in the caller's order.
func TestDirectModeRefusesRebind(t *testing.T) {
	if Isolated() {
		t.Fatal("confinement is on in a unit test")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	tmp := t.TempDir()
	work := filepath.Join(tmp, "ws", "tile") // the work tree at the canonical path
	ckpt := filepath.Join(tmp, "ckpt")       // a checkpoint's materialized tree
	other := filepath.Join(tmp, "other")
	marks := filepath.Join(tmp, "marks") // the probe touches $MARK wherever it runs
	for _, d := range []string{work, ckpt, other, marks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	probe := []string{"sh", "-c", `touch "$MARK" && pwd -P`}
	ran := func(mark string) bool { _, err := os.Lstat(mark); return err == nil }

	refused := []struct {
		name string
		cmd  Cmd
	}{
		{"DirFrom another dir", Cmd{Dir: work, DirFrom: ckpt}},
		{"DirFrom another dir, read-only", Cmd{Dir: work, DirFrom: ckpt, ReadOnlyDir: true}},
		{"a bind made by At", Cmd{Dir: work, Binds: []sandbox.Bind{RO(other), At(ckpt, filepath.Join(work, "nested"), true)}}},
		{"At with source and destination one path", Cmd{Dir: work, Binds: []sandbox.Bind{At(other, other, false)}}},
		{"At without a Dir", Cmd{Binds: []sandbox.Bind{At(ckpt, filepath.Join(tmp, "go.work"), true)}}},
	}
	for i, r := range refused {
		t.Run(r.name, func(t *testing.T) {
			mark := filepath.Join(marks, fmt.Sprint("refused-", i))
			c := r.cmd
			c.Argv, c.Env = probe, []string{"MARK=" + mark}
			res, err := Run(context.Background(), c)
			if !errors.Is(err, ErrNeedsIsolation) {
				t.Fatalf("want ErrNeedsIsolation, got %v (stdout %q)", err, res.Stdout)
			}
			if ran(mark) || len(res.Stdout) > 0 {
				t.Fatal("the refused job ran anyway")
			}
		})
	}

	t.Run("DirFrom without a Dir", func(t *testing.T) {
		mark := filepath.Join(marks, "nodir")
		_, err := Run(context.Background(), Cmd{DirFrom: ckpt, Argv: probe, Env: []string{"MARK=" + mark}})
		if err == nil || ran(mark) {
			t.Fatalf("a DirFrom with nothing to show it at ran: %v", err)
		}
	})

	want, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []struct {
		name string
		cmd  Cmd
	}{
		{"DirFrom is Dir", Cmd{Dir: work, DirFrom: work, ReadOnlyDir: true}},
		{"DirFrom is Dir, with a trailing slash", Cmd{Dir: work, DirFrom: work + "/"}},
		{"a hand-built bind elsewhere (the git import's ~/.ssh)", Cmd{Dir: work, Net: NetHost,
			Binds: []sandbox.Bind{{Src: filepath.Join(other, ".ssh"), Dst: "/root/.ssh", RO: true}}}},
	}
	for i, r := range allowed {
		t.Run(r.name, func(t *testing.T) {
			mark := filepath.Join(marks, fmt.Sprint("allowed-", i))
			c := r.cmd
			c.Argv, c.Env = probe, []string{"MARK=" + mark}
			res, err := Run(context.Background(), c)
			if err != nil {
				t.Fatalf("today's direct run failed: %v", err)
			}
			if got := strings.TrimSpace(string(res.Stdout)); got != want {
				t.Fatalf("ran in %q, want %q", got, want)
			}
			if !ran(mark) {
				t.Fatal("the probe did not run")
			}
		})
	}

	// What a sandbox is handed.
	nested := filepath.Join(work, "nested")
	for _, r := range []struct {
		cmd  Cmd
		want []sandbox.Bind
	}{
		{Cmd{Dir: work, DirFrom: ckpt},
			[]sandbox.Bind{{Src: ckpt, Dst: work}}},
		{Cmd{Dir: work, DirFrom: ckpt, ReadOnlyDir: true, Binds: []sandbox.Bind{
			RO(other), At(other, nested, true), Mask(filepath.Join(work, ".xbin")), At(ckpt, filepath.Join(tmp, "go.work"), false),
		}},
			[]sandbox.Bind{
				{Src: ckpt, Dst: work, RO: true},
				{Src: other, Dst: other, RO: true},
				{Src: other, Dst: nested, RO: true},
				{Dst: filepath.Join(work, ".xbin"), Mask: true, RO: true},
				{Src: ckpt, Dst: filepath.Join(tmp, "go.work")},
			}},
	} {
		if got := r.cmd.binds(); !reflect.DeepEqual(got, r.want) {
			t.Errorf("bind list\n got: %+v\nwant: %+v", got, r.want)
		}
	}
	if At(other, other, true) == RO(other) {
		t.Error("a bind made by At is indistinguishable from a hand-built one")
	}
}
