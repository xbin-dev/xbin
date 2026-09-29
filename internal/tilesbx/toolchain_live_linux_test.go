//go:build linux && integration

package tilesbx

// The rootfs toolchains in a tile sandbox (D134): a command run through the
// sandbox's shell finds node, npx, go and bun on its PATH, and Playwright
// finds the browsers the rootfs ships — what an agent's `bash` gets.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

func TestLiveToolchain(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("unprivileged user namespaces unavailable")
	}
	rootfs := os.Getenv("XBIN_TEST_ROOTFS")
	if rootfs == "" {
		rootfs, _ = filepath.Abs("../../.rootfs")
	}
	if _, err := os.Stat(filepath.Join(rootfs, "usr", "local", "node", "bin", "node")); err != nil {
		t.Skip("no rootfs with node ($XBIN_TEST_ROOTFS or .rootfs)")
	}
	fuse := findFuseOverlayfs()
	if fuse == "" {
		fuse = "none"
	}
	t.Setenv("XBIN_FUSE_OVERLAYFS", fuse)
	le := newLiveEnv(t, liveBinaries(t), rootfs)
	le.create(map[string]any{"name": "tc-1", "mode": ModeNamespace})

	sh := func(cmd string) string {
		t.Helper()
		r := le.liveRun("tc-1", map[string]any{"cmd": cmd})
		if r.ExitCode == nil || *r.ExitCode != 0 {
			t.Fatalf("%s: %+v\nstdout %+v\nstderr %+v", cmd, r, r.Stdout, r.Stderr)
		}
		return strings.TrimSpace(r.Stdout.Head)
	}
	if v := sh("node --version"); !strings.HasPrefix(v, "v") {
		t.Errorf("node --version: %q", v)
	}
	if p := sh("command -v go"); p != "/usr/local/go/bin/go" {
		t.Errorf("go: %q", p)
	}
	if _, err := os.Stat(filepath.Join(rootfs, playwrightBrowsers)); err != nil {
		t.Logf("the rootfs has no %s: Playwright unchecked", playwrightBrowsers)
		return
	}
	if p := sh("echo $PLAYWRIGHT_BROWSERS_PATH"); p != playwrightBrowsers {
		t.Errorf("PLAYWRIGHT_BROWSERS_PATH: %q", p)
	}
	if v := sh("npx --no-install playwright --version"); !strings.Contains(v, "Version") {
		t.Errorf("npx playwright --version: %q", v)
	}
	// the browsers it ships are the ones this Playwright looks for
	if p := sh(`node -e 'const {chromium}=require("/usr/local/node/lib/node_modules/playwright"); const p=chromium.executablePath(); require("fs").accessSync(p); console.log(p)'`); !strings.HasPrefix(p, playwrightBrowsers+"/") {
		t.Errorf("chromium.executablePath: %q", p)
	}
}
