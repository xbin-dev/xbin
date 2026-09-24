package runner

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/util"
)

// buildConfined compiles a Go backend inside a throwaway sandbox (D78).
// `go build` reads the tile's content as configuration — VCS stamping runs
// git with the tile's .git/config (whose core.fsmonitor runs anything),
// go.mod steers downloads and replacements — so it never runs as xbind.
//
// What the build sees, chosen so a build behaves exactly as it did on the
// host: the host's own Go toolchain (read-only, same version as before), the
// workspace read-only with its secret dirs masked (.xbin, data, homes — so
// go.work and every `use`d module resolve unchanged), the SDK. What it may
// write is only the tile's own: its output dir, and its own build and module
// caches (a shared cache would let one tile's build plant code in another's).
// Modules the shared cache already holds are served from it read-only as a
// file:// GOPROXY — no network, no re-download; new ones come from the
// network: public addresses only (XBIN_BUILD_NET=host shares the host's, for
// a GOPROXY or private modules on the LAN). VCS stamping is off: nothing a
// tile's repo says runs even inside the sandbox.
func (r *Runner) buildConfined(c *registry.Component, entry, out string) error {
	tc, err := hostToolchain()
	if err != nil {
		return &BuildError{Output: "go toolchain: " + err.Error()}
	}
	key := util.CompKey(c.Path)
	cache := filepath.Join(r.Root, ".xbin", "cache", "tile", key)
	gocache, modcache := filepath.Join(cache, "go-build"), filepath.Join(cache, "mod")
	for _, d := range []string{gocache, modcache, filepath.Dir(out)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	binds := []sandbox.Bind{
		confine.RO(tc.goroot),
		confine.RO(r.Root),
		confine.MaskOpen(filepath.Join(r.Root, ".xbin")), // secrets; the tile's own dirs below nest back in
		confine.Mask(filepath.Join(r.Root, "data")),
		confine.Mask(filepath.Join(r.Root, "homes")),
		confine.RW(filepath.Dir(out)),
		confine.RW(gocache),
		confine.RW(modcache),
	}
	proxy := tc.goproxy
	if tc.download != "" {
		binds = append(binds, confine.RO(tc.download))
		proxy = (&url.URL{Scheme: "file", Path: tc.download}).String() + "," + proxy
	}
	if sdk := deps.SDKPath(); sdk != "" && !within(sdk, r.Root) && pathExists(sdk) {
		binds = append(binds, confine.RO(sdk))
	}
	env := []string{
		"PATH=" + filepath.Join(tc.goroot, "bin") + ":/usr/local/bin:/usr/bin:/bin",
		"GOCACHE=" + gocache, "GOMODCACHE=" + modcache, "GOPATH=/tmp/go", "GOPROXY=" + proxy,
		"CGO_ENABLED=0", "GOTELEMETRY=off",
	}
	for _, k := range passGoEnv { // the operator's module settings keep applying
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	// the tile's module cache stays deletable by xbind (Go makes it read-only)
	env = append(env, "GOFLAGS="+strings.TrimSpace(os.Getenv("GOFLAGS")+" -modcacherw"))
	net := confine.NetInternet
	if os.Getenv("XBIN_BUILD_NET") == "host" {
		net = confine.NetHost
	}
	res, err := confine.Run(context.Background(), confine.Cmd{
		Argv: []string{tc.gobin, "build", "-buildvcs=false", "-o", out, entry},
		Dir:  c.Dir, ReadOnlyDir: true, Binds: binds, Env: env, Net: net,
		Timeout: 20 * time.Minute, MaxOutput: 1 << 20,
	})
	if err != nil {
		if _, ok := confine.ExitCode(err); ok || strings.Contains(err.Error(), "timed out") {
			return &BuildError{Output: strings.TrimSpace(string(res.Stdout) + string(res.Stderr) + "\n" + timeoutNote(err))}
		}
		return fmt.Errorf("build sandbox: %w", err)
	}
	return nil
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

type toolchain struct {
	gobin, goroot string
	goproxy       string // the host's GOPROXY list
	download      string // the host module cache's download dir ("" = none)
}

var (
	tcOnce sync.Once
	tcVal  toolchain
	tcErr  error
)

// hostToolchain finds the Go that xbind would have built with on the host:
// `go` on xbind's PATH, and its GOROOT, GOPROXY and module cache.
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
		cmd := exec.Command(gobin, "env", "GOROOT", "GOPROXY", "GOMODCACHE") // exec-ok: xbind's own toolchain, run in / — no workspace input
		cmd.Dir = "/"
		outb, err := cmd.Output()
		if err != nil {
			tcErr = fmt.Errorf("go env: %w", err)
			return
		}
		f := strings.Split(strings.TrimRight(string(outb), "\n"), "\n")
		if len(f) < 3 || f[0] == "" {
			tcErr = errors.New("go env: unexpected output")
			return
		}
		tcVal = toolchain{gobin: gobin, goroot: f[0], goproxy: f[1]}
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
