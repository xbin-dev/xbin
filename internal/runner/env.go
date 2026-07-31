package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/util"
)

// The component environment layer (plans/component-env.md): a component's
// `setup` script is run once in a sandbox to populate a persisted overlay layer
// (extra system/runtime deps beyond the base rootfs). The layer is keyed by a
// hash of the script + base rootfs; a script change builds a *fresh* layer. The
// running backend then stacks it as a read-only lower.

// envSetupPATH mirrors the rootfs toolchain PATH used elsewhere, so `apt`,
// language package managers, etc. resolve inside the setup sandbox.
const envSetupPATH = "PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// envLayerDir is the per-component, per-hash directory holding the env layer.
// Empty when the component declares no setup or isolation is off.
func (r *Runner) envLayerDir(c *registry.Component) string {
	if strings.TrimSpace(c.Manifest.Setup) == "" || !r.Isolate || r.Rootfs == "" {
		return ""
	}
	return filepath.Join(r.Root, ".xbin", "env", util.CompKey(c.Path), r.envHash(c))
}

// envHash keys the layer on the setup script + a base-rootfs identity, so both a
// script edit and a base-rootfs rebuild invalidate it.
func (r *Runner) envHash(c *registry.Component) string {
	id := r.Rootfs
	if fi, err := os.Stat(filepath.Join(r.Rootfs, "etc", "os-release")); err == nil {
		id = fmt.Sprintf("%s:%d", r.Rootfs, fi.ModTime().UnixNano())
	}
	sum := sha256.Sum256([]byte(c.Manifest.Setup + "\x00" + id))
	return hex.EncodeToString(sum[:])[:16]
}

// ensureEnvLayer builds the component's env layer if it isn't already, and
// returns the read-only lowerdir to stack under the backend ("" = no layer).
// Called single-flight from start(), so its cost is paid on first build / on a
// setup change, surfaced through the normal build events.
func (r *Runner) ensureEnvLayer(c *registry.Component) (string, error) {
	dir := r.envLayerDir(c)
	if dir == "" {
		return "", nil
	}
	upper := filepath.Join(dir, "upper")
	if _, err := os.Stat(filepath.Join(dir, ".ok")); err == nil {
		return upper, nil // already built for this setup hash
	}

	// Fresh build (never re-apply onto a stale layer): start from an empty upper.
	_ = os.RemoveAll(dir)
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(upper, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", err
	}

	r.Hub.Publish(events.Event{Type: "build-start", Component: c.Path, Text: "setting up environment…"})

	spec := &sandbox.Spec{
		Lower:   []string{r.Rootfs},
		Upper:   upper,
		Work:    work,
		Binds:   []sandbox.Bind{{Src: c.Dir, Dst: c.Dir, RO: true}}, // source available to setup
		Entry:   "/bin/sh",
		Argv:    []string{"sh", "-exc", c.Manifest.Setup},
		Env:     []string{envSetupPATH, "HOME=/root", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive", "XBIN_COMPONENT=" + c.Path},
		Cwd:     c.Dir,
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
		Net:     "relay", // net:internet for the build
	}

	logf, _ := os.OpenFile(
		filepath.Join(r.Root, ".xbin", "log", util.CompKey(c.Path)+".log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)

	cmd, h, err := sandbox.Launch(spec)
	if err != nil {
		if logf != nil {
			logf.Close()
		}
		_ = os.RemoveAll(dir)
		return "", err
	}
	if logf != nil {
		fmt.Fprintf(logf, "--- env setup %s ---\n", c.Path)
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	if err := cmd.Start(); err != nil {
		h.Cleanup()
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := h.SetupUserns(); err != nil {
		fmt.Fprintf(logf, "env setup userns: %v\n", err)
	}
	if h.NeedsRelay() {
		if fd, err := h.RecvTUN(); err == nil {
			pol, _ := sandbox.Parse([]string{"net:internet"})
			if rl, err := relay.Start(relay.Config{TunFD: fd, Allow: pol.Allow, Resolver: sandbox.HostResolver()}); err == nil {
				defer rl.Close()
			}
		}
	}
	err = cmd.Wait()
	h.Cleanup()
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("setup script failed (see the component log): %w", err)
	}

	_ = os.WriteFile(filepath.Join(dir, ".ok"), nil, 0o644)
	r.gcEnvLayers(c, dir)
	return upper, nil
}

// gcEnvLayers removes stale env-layer hashes for a component, keeping only keep.
func (r *Runner) gcEnvLayers(c *registry.Component, keep string) {
	base := filepath.Join(r.Root, ".xbin", "env", util.CompKey(c.Path))
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() && filepath.Join(base, e.Name()) != keep {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}
