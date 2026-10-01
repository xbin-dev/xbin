package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// build produces a runnable entry from c's work tree. For go it compiles;
// for node/python it just validates the entry file exists (the interpreter
// is the "binary"). A checkpoint builds through buildCode.
func (r *Runner) build(c *registry.Component) (string, error) {
	switch c.Manifest.Runtime {
	case "go":
		entry := goEntry(c.Manifest)
		out := filepath.Join(r.Root, ".xbin", "build", util.CompKey(c.Path), "bin")
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}
		if r.Isolate {
			// in a sandbox, never as xbind (D78, build.go); fully static
			// (CGO_ENABLED=0) so the backend runs on any sandbox rootfs,
			// independent of the base image's glibc (plans/isolation-impl.md)
			if err := r.buildConfined(c, entry, out); err != nil {
				return "", err
			}
			r.GoVersions.Built(c.Path)
			return out, nil
		}
		// the build's own workspace here too (D166): the same module graph
		// as under isolation, whatever the root go.work says
		gowork, work := "off", (*deps.Work)(nil)
		if w, ok := r.buildWork(c, entry, "", nil); ok {
			gocache, _ := goCaches(r.Root, c.Path)
			gw, err := writeBuildWork(filepath.Join(filepath.Dir(gocache), "work"), w.GoWork, r.Root)
			if err != nil {
				return "", err
			}
			gowork, work = gw, &w
		}
		cmd := exec.Command("go", "build", "-o", out, entry) // exec-ok: isolation off — no sandbox exists; backends run as xbind too
		cmd.Dir = c.Dir
		cmd.Env = append(os.Environ(),
			"GOCACHE="+filepath.Join(r.Root, ".xbin", "cache", "go-build"),
			"GOWORK="+gowork,
		)
		if outp, err := cmd.CombinedOutput(); err != nil {
			return "", &BuildError{Output: withHint(string(outp), work)}
		}
		r.GoVersions.Built(c.Path)
		return out, nil
	case "node", "python":
		entry := interpEntry(c.Manifest)
		p := filepath.Join(c.Dir, filepath.FromSlash(entry))
		if _, err := os.Stat(p); err != nil {
			return "", &BuildError{Output: fmt.Sprintf("entry %s not found (set \"entry\" in xbin.json)", entry)}
		}
		return p, nil
	default:
		return "", fmt.Errorf("unknown runtime %q", c.Manifest.Runtime)
	}
}

func goEntry(m registry.Manifest) string {
	if m.Entry != "" {
		return m.Entry
	}
	return "./backend"
}

func interpEntry(m registry.Manifest) string {
	switch {
	case m.Entry != "":
		return m.Entry
	case m.Runtime == "node":
		return "backend/server.js"
	}
	return "backend/server.py"
}

// goBuild is what one confined Go build shows and writes beyond the
// standard binds of runGoBuild.
type goBuild struct {
	dirFrom string         // the code root shown at the tile's path; "" = the work tree
	out     string         // the binary (-o)
	outDir  string         // the one directory of .xbin/build the build may write
	extra   []sandbox.Bind // after the standard binds: other components' code, a go.work, masks
	// gocache and modcache are the build's Go caches; "" = the tile's own
	// (goCaches), shared by its deployments. A protected primary's build
	// has its own (07-runtime §3.4).
	gocache, modcache string
	// work is the build's own workspace (D166, gowork.go); nil for a
	// work-tree build = the work tree's, which runGoBuild renders. A
	// checkpoint build's is its plan's (nil: the tree holds no module).
	work *deps.Work
	// gowork is where runGoBuild wrote work's go.work, beside the build's
	// caches (writeBuildWork), bound read-write with its go.work.sum; "" =
	// the tile holds no Go module: GOWORK=off.
	gowork string
}

// buildConfined compiles c's work tree in a throwaway sandbox (runGoBuild).
// A tile with checkpoint artifacts beside out (c/) has them masked, so no
// work-tree build ever writes one (07-runtime §3.1); a zero-state tile has
// no c/ and gets today's binds.
func (r *Runner) buildConfined(c *registry.Component, entry, out string) error {
	g := goBuild{out: out, outDir: filepath.Dir(out)}
	if arts := filepath.Join(g.outDir, "c"); realDir(arts) {
		g.extra = append(g.extra, confine.Mask(arts))
	}
	return r.runGoBuild(c, entry, g)
}

// runGoBuild compiles a Go backend inside a throwaway sandbox (D78).
// `go build` reads the tile's content as configuration — VCS stamping runs
// git with the tile's .git/config (whose core.fsmonitor runs anything),
// go.mod steers downloads and replacements — so it never runs as xbind.
//
// What the build sees, chosen so a build behaves exactly as it did on the
// host: the host's own Go toolchain (read-only, same version as before), the
// workspace read-only with its secret dirs masked (.xbin, data, homes — so
// every module the build uses resolves unchanged), the SDK, and the build's
// own go.work (D166, gowork.go): the tile's modules, the SDK and the
// workspace modules the tile reaches — never the root go.work, whose one
// module graph let any tile's go.mod steer every build. What it may write is
// only the tile's own: its output dir, its own build and module caches (a
// shared cache would let one tile's build plant code in another's) and its
// workspace's go.work.sum.
// Modules the shared cache already holds are served from it read-only as a
// file:// GOPROXY — no network, no re-download; new ones come from the
// network: public addresses only (XBIN_BUILD_NET=host shares the host's, for
// a GOPROXY or private modules on the LAN). VCS stamping is off: nothing a
// tile's repo says runs even inside the sandbox. A checkpoint build shows its
// tree at the tile's path (g.dirFrom, confine's DirFrom), which only a
// sandbox can: a direct run refuses it (confine.ErrNeedsIsolation).
func (r *Runner) runGoBuild(c *registry.Component, entry string, g goBuild) error {
	gocache := g.gocache
	if gocache == "" {
		gocache, _ = goCaches(r.Root, c.Path)
	}
	if g.work == nil && g.dirFrom == "" { // a checkpoint's is its plan's, nil when it holds no module
		if w, ok := r.buildWork(c, entry, "", nil); ok {
			g.work = &w
		}
	}
	if g.work != nil {
		gw, err := writeBuildWork(filepath.Join(filepath.Dir(gocache), "work"), g.work.GoWork, r.Root) // beside the build's caches
		if err != nil {
			return err
		}
		g.gowork = gw
	}
	cmd, dirs, err := r.goBuildCmd(c, entry, g)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	release := buildTurn(c) // a non-primary deployment's build waits its turn
	defer release()
	res, err := confine.Run(context.Background(), cmd)
	if err != nil {
		if _, ok := confine.ExitCode(err); ok || strings.Contains(err.Error(), "timed out") {
			return &BuildError{Output: withHint(strings.TrimSpace(string(res.Stdout)+string(res.Stderr)+"\n"+timeoutNote(err)), g.work)}
		}
		return fmt.Errorf("build sandbox: %w", err)
	}
	return nil
}

// goBuildCmd is runGoBuild's confined run, and the directories it writes
// that must exist before it starts. It is pure: it reads the toolchain and
// the host paths it binds, and makes and runs nothing.
func (r *Runner) goBuildCmd(c *registry.Component, entry string, g goBuild) (confine.Cmd, []string, error) {
	tc, err := hostToolchain()
	if err != nil {
		return confine.Cmd{}, nil, &BuildError{Output: "go toolchain: " + err.Error()}
	}
	gocache, modcache := goCaches(r.Root, c.Path)
	if g.gocache != "" {
		gocache, modcache = g.gocache, g.modcache
	}
	dirs := []string{gocache, modcache}
	if g.dirFrom == "" {
		dirs = append(dirs, g.outDir) // a checkpoint build's is made beneath its artifacts dir
	}
	binds := []sandbox.Bind{
		confine.RO(tc.goroot),
		confine.RO(r.Root),
		confine.MaskOpen(filepath.Join(r.Root, ".xbin")), // secrets; the tile's own dirs below nest back in
		confine.Mask(filepath.Join(r.Root, "data")),
		confine.Mask(filepath.Join(r.Root, "homes")),
		confine.RW(g.outDir),
		confine.RW(gocache),
		confine.RW(modcache),
	}
	if g.gowork != "" {
		binds = append(binds, confine.RW(filepath.Dir(g.gowork)))
	}
	proxy := tc.goproxy
	if tc.download != "" {
		binds = append(binds, confine.RO(tc.download))
		proxy = (&url.URL{Scheme: "file", Path: tc.download}).String() + "," + proxy
	}
	if sdk := deps.SDKPath(); sdk != "" && !within(sdk, r.Root) && pathExists(sdk) {
		binds = append(binds, confine.RO(sdk))
	}
	binds = append(binds, g.extra...)
	env := []string{
		"PATH=" + filepath.Join(tc.goroot, "bin") + ":/usr/local/bin:/usr/bin:/bin",
		"GOCACHE=" + gocache, "GOMODCACHE=" + modcache, "GOPATH=/tmp/go", "GOPROXY=" + proxy,
		"CGO_ENABLED=0", "GOTELEMETRY=off",
	}
	if g.gowork != "" {
		env = append(env, "GOWORK="+g.gowork)
	} else {
		env = append(env, "GOWORK=off") // no Go module of the tile's own: never the root go.work
	}
	for _, k := range passGoEnv { // the operator's module settings keep applying
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	// the tile's module cache stays deletable by xbind (Go makes it read-only)
	env = append(env, "GOFLAGS="+goFlags())
	net := confine.NetInternet
	if os.Getenv("XBIN_BUILD_NET") == "host" {
		net = confine.NetHost
	}
	return confine.Cmd{
		Argv: []string{tc.gobin, "build", "-buildvcs=false", "-o", g.out, entry},
		Dir:  c.Dir, DirFrom: g.dirFrom, ReadOnlyDir: true, Binds: binds, Env: env, Net: net,
		Timeout: 20 * time.Minute, MaxOutput: 1 << 20,
	}, dirs, nil
}

// goCaches are a tile's own Go build and module caches, shared by its
// deployments (07-runtime §3.1).
func goCaches(root, tile string) (gocache, modcache string) {
	cache := filepath.Join(root, ".xbin", "cache", "tile", util.CompKey(tile))
	return filepath.Join(cache, "go-build"), filepath.Join(cache, "mod")
}

func goFlags() string { return strings.TrimSpace(os.Getenv("GOFLAGS") + " -modcacherw") }

// readRegular reads a regular file of at most max bytes, never through a
// symlink (the workspace's root is people's to write).
func readRegular(p string, max int64) ([]byte, bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	return b, err == nil
}

func timeoutNote(err error) string {
	if strings.Contains(err.Error(), "timed out") {
		return err.Error()
	}
	return ""
}

// passGoEnv are the operator's Go module settings, passed into the build
// sandbox when set in xbind's environment (GOFLAGS too, extended above).
var passGoEnv = []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GONOSUMCHECK", "GOSUMDB",
	"GOINSECURE", "GOVCS", "GOTOOLCHAIN", "GOAUTH", "GOAMD64", "GOARM64", "GOEXPERIMENT"}

// nonPrimaryBuilds is the one build limiter (07-runtime §10.3; D127q;
// 06-security T10 item 4): at most max(1, NumCPU/4) builds for non-primary
// deployments at once, workspace-wide, shared by every tile: Go builds and
// env-layer setups. A primary's build never waits for it: it builds as
// every build does today, so the zero state keeps its timing and the
// primary always goes first.
var nonPrimaryBuilds = make(chan struct{}, max(1, runtime.NumCPU()/4))

// buildTurn waits for a turn of the build limiter for a build of view c,
// and returns its release. A build for the primary (c.Deployment empty)
// takes no turn.
func buildTurn(c *registry.Component) func() {
	if c.Deployment == "" {
		return func() {}
	}
	return NonPrimaryBuildTurn()
}

// NonPrimaryBuildTurn waits for a turn of the build limiter and returns its
// idempotent release, for work on a non-primary deployment's code that
// counts as a build outside the runner: the deployments plane's
// materialization of its checkpoint (07-runtime §10.3).
func NonPrimaryBuildTurn() func() {
	nonPrimaryBuilds <- struct{}{}
	var once sync.Once
	return func() { once.Do(func() { <-nonPrimaryBuilds }) }
}

// realDir reports whether p is a directory, not a symlink to one.
func realDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

type toolchain struct {
	gobin, goroot string
	goproxy       string // the host's GOPROXY list
	download      string // the host module cache's download dir ("" = none)
	version       string // its GOVERSION, recorded in build.json
}

var (
	tcOnce sync.Once
	tcVal  toolchain
	tcErr  error
)

// hostToolchain finds the Go that xbind would have built with on the host:
// `go` on xbind's PATH, and its GOROOT, GOPROXY, module cache and version.
func hostToolchain() (toolchain, error) {
	tcOnce.Do(func() {
		gobin, err := exec.LookPath("go")
		if err != nil {
			tcErr = err
			return
		}
		gobin, _ = filepath.Abs(gobin)
		if p, err := filepath.EvalSymlinks(gobin); err == nil {
			gobin = p
		}
		cmd := exec.Command(gobin, "env", "GOROOT", "GOPROXY", "GOMODCACHE", "GOVERSION") // exec-ok: xbind's own toolchain, run in / — no workspace input
		cmd.Dir = "/"
		outb, err := cmd.Output()
		if err != nil {
			tcErr = fmt.Errorf("go env: %w", err)
			return
		}
		f := strings.Split(strings.TrimRight(string(outb), "\n"), "\n")
		if len(f) < 4 || f[0] == "" {
			tcErr = errors.New("go env: unexpected output")
			return
		}
		tcVal = toolchain{gobin: gobin, goroot: f[0], goproxy: f[1], version: f[3]}
		if tcVal.goproxy == "" {
			tcVal.goproxy = "https://proxy.golang.org,direct"
		}
		if dl := filepath.Join(f[2], "cache", "download"); f[2] != "" && pathExists(dl) {
			tcVal.download = dl
		}
		if !within(gobin, tcVal.goroot) {
			tcErr = fmt.Errorf("go binary %s is outside its GOROOT %s", gobin, tcVal.goroot)
		}
	})
	return tcVal, tcErr
}
