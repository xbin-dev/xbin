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
const envSetupPATH = "PATH=/usr/local/go/bin:/usr/local/node/bin:/usr/local/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// envLayerDir is the per-component, per-hash directory holding the env layer.
// Empty when the component declares no setup or isolation is off.
func (r *Runner) envLayerDir(c *registry.Component) string {
	if strings.TrimSpace(c.Manifest.Setup) == "" || !r.Isolate || r.Rootfs == "" {
		return ""
	}
	return filepath.Join(r.Root, ".xbin", "env", util.CompKey(c.Path), r.envHash(c))
}

// envHash keys the layer on the setup script + a base-rootfs identity, so both a
// script edit and a base-rootfs rebuild invalidate it. c is the deployment's
// view, whose setup is its own code's (deployment-level, 05-model §6), so
// deployments with the same script share one layer.
func (r *Runner) envHash(c *registry.Component) string { return r.setupHash(c.Manifest.Setup) }

func (r *Runner) setupHash(setup string) string {
	id := r.Rootfs
	if fi, err := os.Stat(filepath.Join(r.Rootfs, "etc", "os-release")); err == nil {
		id = fmt.Sprintf("%s:%d", r.Rootfs, fi.ModTime().UnixNano())
	}
	sum := sha256.Sum256([]byte(setup + "\x00" + id))
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

	if c.Deployment == "" {
		r.Hub.Publish(events.Event{Type: "build-start", Component: c.Path, Text: "setting up environment…"})
	} else {
		r.emit(c.Path, c.Deployment, "build-start", "setting up environment…") // never an old type (C2)
	}

	spec := r.envSetupSpec(c, upper, work)

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
	r.gcEnvLayers(c, r.envKeep(c, filepath.Base(dir)))
	return upper, nil
}

// envSetupSpec is the sandbox a view's setup script runs in: the base rootfs
// under a fresh upper, internet egress, and the deployment's own code
// read-only at the tile's canonical path — a pinned deployment's checkpoint,
// never the work tree (06-security T17).
func (r *Runner) envSetupSpec(c *registry.Component, upper, work string) *sandbox.Spec {
	return &sandbox.Spec{
		Lower:   []string{r.Rootfs},
		Upper:   upper,
		Work:    work,
		Binds:   []sandbox.Bind{{Src: codeRoot(c), Dst: c.Dir, RO: true}}, // source available to setup
		Entry:   "/bin/sh",
		Argv:    []string{"sh", "-exc", c.Manifest.Setup},
		Env:     []string{envSetupPATH, "HOME=/root", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive", "XBIN_COMPONENT=" + c.Path},
		Cwd:     c.Dir,
		HostUID: os.Getuid(),
		HostGID: os.Getgid(),
		Net:     "relay", // net:internet for the build
	}
}

// envKeep is the set of c's tile's env-layer hashes still referenced: the
// layer just built, the one its running generation stacks, and those of the
// primary's current code (the registry's component: its checkpoint's setup
// while pinned) and of the work tree, the live reload target's. A pinned
// deployment's restart then finds its layer (06-security T17).
func (r *Runner) envKeep(c *registry.Component, built string) map[string]bool {
	keep := map[string]bool{built: true}
	r.mu.Lock()
	s := r.states[c.Path]
	r.mu.Unlock()
	if s != nil {
		s.mu.Lock()
		if s.cur != nil && s.cur.envHash != "" {
			keep[s.cur.envHash] = true
		}
		s.mu.Unlock()
	}
	if r.Reg == nil {
		return keep
	}
	if rc, ok := r.Reg.Component(c.Path); ok {
		for _, m := range []registry.Manifest{rc.Manifest, rc.WorkTreeManifest()} {
			if strings.TrimSpace(m.Setup) != "" {
				keep[r.setupHash(m.Setup)] = true
			}
		}
	}
	return keep
}

// gcEnvLayers removes a component's env-layer hashes that keep doesn't name.
func (r *Runner) gcEnvLayers(c *registry.Component, keep map[string]bool) {
	base := filepath.Join(r.Root, ".xbin", "env", util.CompKey(c.Path))
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() && !keep[e.Name()] {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}
