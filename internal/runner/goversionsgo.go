package runner

// goversionsgo.go — what the upgrade check of D166 (goversionscheck.go)
// runs and reads: whether an earlier xbind built a Go tile, the go command
// on a tile's go.work (`go list -deps` of its entry, `go list -m` of a
// shared go.work that fails), confined as the tile's build runs (D78), and
// the verdict on a shared go.work the go command refuses as a whole.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// sharedWorkError: the shared go.work itself can't be made or loaded — the
// workspace's, not one tile's.
type sharedWorkError struct{ msg string }

func (e *sharedWorkError) Error() string { return e.msg }

// goBuiltBefore reports whether an earlier xbind built a Go tile of the
// workspace with the shared go.work: a work-tree binary
// (.xbin/build/<key>/bin, a regular file), or a checkpoint artifact — a
// pinned primary's, shared or protected — whose build.json records no
// workspace of its own (D166's builds record "tile").
func (r *Runner) goBuiltBefore() bool {
	for _, c := range r.components() {
		if c.Manifest.Runtime != "go" && workTreeView(c).Manifest.Runtime != "go" {
			continue
		}
		fi, err := os.Lstat(filepath.Join(r.Root, ".xbin", "build", util.CompKey(c.Path), "bin"))
		if err == nil && fi.Mode().IsRegular() {
			return true
		}
		for _, pr := range []products{r.sharedProducts(c.Path), r.protectedProducts(c.Path)} {
			if sharedGoWorkArtifact(pr.base, pr.arts, c.Path) {
				return true
			}
		}
	}
	return false
}

// sharedGoWorkArtifact reports whether base/arts holds a checkpoint
// artifact of tile built with the workspace's go.work. Nothing on the way
// is followed: arts sits where a work-tree build may write.
func sharedGoWorkArtifact(base, arts, tile string) bool {
	fd, err := unix.Open(filepath.Join(base, arts), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), arts)
	names, _ := f.Readdirnames(-1)
	f.Close()
	for _, n := range names {
		if !fullTree(n) {
			continue
		}
		if rec, _ := artifactRecordIn(base, arts, n); rec != nil && rec.Tile == tile && rec.Workspace != ownWorkspace {
			return true
		}
	}
	return false
}

// listShared lists what c's entry links built with the shared go.work.
// When that fails and `go list -m` refuses the same go.work, the go.work
// is at fault — the workspace's, not c's: a *sharedWorkError, which the
// pass says once, and lists no other tile with that go.work for.
func (g *GoVersions) listShared(ctx context.Context, c *registry.Component, entry string, w deps.Work, probes *sharedProbes) ([]modVer, error) {
	uses := map[string]bool{}
	for _, m := range w.Uses {
		uses[filepath.Clean(m.Dir)] = true
	}
	shared, err := deps.SharedWork(g.Run.Reg, deps.SDKPath(), func(dir string) bool { return uses[dir] })
	if err != nil {
		return nil, probes.fail(fmt.Sprintf("the workspace's go.work: %v", err))
	}
	sum := sha256.Sum256(shared)
	key := hex.EncodeToString(sum[:])
	if err := probes.known(key); err != nil {
		return nil, err
	}
	had, err := g.listModules(ctx, c, entry, shared, nil)
	if err == nil {
		return had, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if perr := probes.run(key, func() error { return g.probeWork(ctx, c, shared) }); perr != nil {
		return nil, perr
	}
	return nil, fmt.Errorf("built with the workspace's go.work: %w", err)
}

// inputsOf is what decides the versions of entry's build in w: the
// running xbind (its SDK), the entry and w's inputs (deps.Work.Inputs).
func (g *GoVersions) inputsOf(entry string, w deps.Work) string {
	sum := sha256.Sum256([]byte(g.Version + "\x00" + entry + "\x00" + w.Inputs()))
	return hex.EncodeToString(sum[:])
}

// sharedProbes are one pass's verdicts on the shared go.works it listed
// with (by content): whether `go list -m` loads each.
type sharedProbes struct {
	mu    sync.Mutex
	m     map[string]*sharedProbe
	first string // the first failure: the workspace's error
}

type sharedProbe struct {
	once sync.Once
	done bool
	err  error
}

// fail notes a failure of the shared go.work itself.
func (p *sharedProbes) fail(msg string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.first == "" {
		p.first = msg
	}
	return &sharedWorkError{msg}
}

// known is the verdict on the go.work key when a probe found it fails.
func (p *sharedProbes) known(key string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pr := p.m[key]; pr != nil && pr.done && pr.err != nil {
		return pr.err
	}
	return nil
}

// run probes the go.work key once.
func (p *sharedProbes) run(key string, probe func() error) error {
	p.mu.Lock()
	pr := p.m[key]
	if pr == nil {
		pr = &sharedProbe{}
		p.m[key] = pr
	}
	p.mu.Unlock()
	pr.once.Do(func() {
		var err error
		if perr := probe(); perr != nil {
			err = p.fail("the workspace's shared go.work doesn't load: " + perr.Error())
		}
		p.mu.Lock()
		pr.done, pr.err = true, err
		p.mu.Unlock()
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return pr.err
}

func (p *sharedProbes) failed() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.first
}

// listModules is the check's list of c's modules (Runner.listModules).
func (g *GoVersions) listModules(ctx context.Context, c *registry.Component, entry string, gowork []byte, pins []deps.Pin) ([]modVer, error) {
	if g.list != nil {
		return g.list(ctx, c, entry, gowork, pins)
	}
	return g.Run.listModules(ctx, c, entry, gowork, pins)
}

// probeWork is the check's verdict on a go.work (Runner.probeWork).
func (g *GoVersions) probeWork(ctx context.Context, c *registry.Component, gowork []byte) error {
	if g.probe != nil {
		return g.probe(ctx, c, gowork)
	}
	return g.Run.probeWork(ctx, c, gowork)
}

// listModules lists the modules c's entry links built with the go.work
// gowork, and pins required besides the tile's go.mod (a module of their
// own the go.work uses: deps.PinGoMod): `go list -deps` (goList).
func (r *Runner) listModules(ctx context.Context, c *registry.Component, entry string, gowork []byte, pins []deps.Pin) ([]modVer, error) {
	out, err := r.goList(ctx, c, entry, gowork, pins, listArgs(entry))
	if err != nil {
		return nil, err
	}
	return parseModList(out), nil
}

// probeWork reports whether the go command loads the go.work gowork:
// `go list -m`, its modules — no build, no module graph, nothing
// downloaded — run as c's lists run (goList).
func (r *Runner) probeWork(ctx context.Context, c *registry.Component, gowork []byte) error {
	_, err := r.goList(ctx, c, goEntry(c.Manifest), gowork, nil, []string{"list", "-m"})
	return err
}

// goList runs `go <args>` as c's build runs, with the go.work gowork and
// pins: confined (D78: go reads the tile's go.mod and code as
// configuration) with the build's binds, caches, network and settings
// (goBuildCmd), or as the build runs with isolation off — writing only its
// own directory beside the tile's caches, versions/, where its go.work and
// go.work.sum live. Its standard output.
func (r *Runner) goList(ctx context.Context, c *registry.Component, entry string, gowork []byte, pins []deps.Pin, args []string) ([]byte, error) {
	gocache, _ := goCaches(r.Root, c.Path)
	cacheDir := filepath.Dir(gocache)
	vfd, err := openArtifacts(cacheDir, "versions") // bound into no build; the lists write in it
	if err != nil {
		return nil, err
	}
	defer unix.Close(vfd)
	dir := filepath.Join(cacheDir, "versions")
	content := gowork
	if len(pins) > 0 {
		goLine := deps.WorkGoLine(gowork)
		if goLine == "" {
			goLine = "1.18" // a go.work without one is go 1.18's
		}
		pfd, err := openArtifacts(dir, "pin")
		if err != nil {
			return nil, err
		}
		err = writeAt(pfd, "go.mod", deps.PinGoMod(goLine, pins))
		unix.Close(pfd)
		if err != nil {
			return nil, err
		}
		content = deps.WithUse(gowork, filepath.Join(dir, "pin"))
	}
	if err := writeAt(vfd, "go.work", content); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(vfd, "go.work.sum", &st, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		if seed := seedWorkSum(filepath.Join(cacheDir, "work"), "", r.Root); len(seed) > 0 {
			if err := writeAt(vfd, "go.work.sum", seed); err != nil {
				return nil, err
			}
		}
	}
	gw := filepath.Join(dir, "go.work")
	var cmd confine.Cmd
	if r.Isolate {
		var dirs []string
		cmd, dirs, err = r.goBuildCmd(c, entry, goBuild{outDir: dir})
		if err != nil {
			return nil, err
		}
		for _, d := range dirs {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return nil, err
			}
		}
		cmd.Argv = append([]string{cmd.Argv[0]}, args...)
		cmd.Env = setEnv(cmd.Env, "GOWORK", gw)
		cmd.Env = setEnv(cmd.Env, "GOFLAGS", readonlyFlags(goFlags()))
	} else {
		// as the build runs with isolation off (build.go): xbind's go and
		// environment, the workspace's build cache — no sandbox exists
		cmd = confine.Cmd{Argv: append([]string{"go"}, args...), Dir: c.Dir, ReadOnlyDir: true, Env: []string{
			"GOCACHE=" + filepath.Join(r.Root, ".xbin", "cache", "go-build"),
			"GOWORK=" + gw,
			"GOFLAGS=" + readonlyFlags(os.Getenv("GOFLAGS")),
		}}
	}
	cmd.Timeout, cmd.MaxOutput = 10*time.Minute, 4<<20
	res, err := confine.Run(ctx, cmd)
	if err != nil {
		msg := strings.TrimSpace(string(res.Stderr))
		if msg == "" {
			msg = err.Error()
		}
		if len(msg) > 2000 {
			msg = "…" + msg[len(msg)-2000:]
		}
		return nil, errors.New(msg)
	}
	return res.Stdout, nil
}

// listArgs is `go list` of entry's packages and their dependencies' modules.
func listArgs(entry string) []string {
	return []string{"list", "-buildvcs=false", "-deps", "-f", listFormat, entry}
}

// readonlyFlags is GOFLAGS with -mod=readonly unless it sets -mod (a
// build's default either way: the list never edits a go.mod).
func readonlyFlags(flags string) string {
	if strings.Contains(flags, "-mod=") {
		return strings.TrimSpace(flags)
	}
	return strings.TrimSpace(flags + " -mod=readonly")
}

// setEnv sets k=v in env, replacing k's entries.
func setEnv(env []string, k, v string) []string {
	out := env[:0:0]
	for _, e := range env {
		if !strings.HasPrefix(e, k+"=") {
			out = append(out, e)
		}
	}
	return append(out, k+"="+v)
}
