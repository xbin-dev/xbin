package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// podman wraps the rootless podman CLI for this tile. It pins storage to the
// persistent `storage` resource (so images/containers survive restarts, not
// the throwaway tmpfs upper) and the runroot to a tmpfs, and runs daemonless.
// Storage driver: overlay-via-fuse-overlayfs when the sandbox has /dev/fuse
// (xbind binds it for cap:containers tiles) — a build step then writes only
// its diff, where vfs copies the ENTIRE rootfs chain per layer, which is
// brutally slow through the encrypted store. vfs stays the fallback.
// Everything is overridable via env for tuning on a given host.
type podman struct {
	bin     string
	root    string   // --root: persistent image/container store
	runroot string   // --runroot: tmpfs runtime state
	driver  string   // --storage-driver
	network string   // default --network for new containers
	base    []string // global flags prepended to every invocation
	env     []string
}

// container is one dev box, as reported by `podman ps`.
type container struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`   // running | exited | created
	Status  string `json:"status"`  // human status ("Up 3 minutes")
	Created string `json:"created"` // ISO-ish
}

func newPodman(storageDir, runtimeDir string) *podman {
	root := filepath.Join(storageDir, "store")
	runroot := filepath.Join(runtimeDir, "containers")
	_ = os.MkdirAll(root, 0o700)
	_ = os.MkdirAll(runroot, 0o700)

	driver := os.Getenv("DEVBOX_STORAGE_DRIVER")
	if driver == "" {
		driver = "vfs"
		if _, err := exec.LookPath("fuse-overlayfs"); err == nil {
			if _, err := os.Stat("/dev/fuse"); err == nil {
				driver = "overlay"
			}
		}
	}
	network := envOr("DEVBOX_NETWORK", "") // "" = podman default (pasta/slirp)
	p := &podman{
		bin:     envOr("DEVBOX_PODMAN", "podman"),
		root:    root,
		runroot: runroot,
		driver:  driver,
		network: network,
	}
	p.base = []string{
		"--root", root, "--runroot", runroot,
		"--storage-driver", driver,
		"--cgroup-manager", envOr("DEVBOX_CGROUP_MANAGER", "cgroupfs"),
	}
	if driver == "overlay" {
		// Rootless overlay needs the userspace mounter — native kernel
		// overlay can't whiteout on a FUSE-backed store.
		if fo, err := exec.LookPath("fuse-overlayfs"); err == nil {
			p.base = append(p.base, "--storage-opt", "overlay.mount_program="+fo)
		}
	}
	// Podman wants HOME (config/state) + a tmpfs XDG_RUNTIME_DIR.
	p.env = append(os.Environ(),
		"HOME="+storageDir,
		"XDG_RUNTIME_DIR="+runtimeDir,
		"XDG_DATA_HOME="+filepath.Join(storageDir, "data"),
		"XDG_CONFIG_HOME="+filepath.Join(storageDir, "config"),
	)
	return p
}

// command builds a podman *exec.Cmd (used both for capture and PTY-attached
// interactive exec).
func (p *podman) command(args ...string) *exec.Cmd {
	cmd := exec.Command(p.bin, append(append([]string{}, p.base...), args...)...)
	cmd.Env = p.env
	return cmd
}

// run captures combined output — for control-plane calls (create/rm/ps).
func (p *podman) run(args ...string) (string, error) {
	out, err := p.command(args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("podman %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// version returns podman's version (a cheap liveness/setup check).
func (p *podman) version() (string, error) {
	out, err := p.run("version", "--format", "{{.Client.Version}}")
	return strings.TrimSpace(out), err
}

// create launches a detached dev box kept alive with `sleep infinity` (so you
// can exec into it repeatedly); most dev images (ubuntu/debian/alpine/fedora)
// carry sleep. cmd overrides the keep-alive command when non-empty.
func (p *podman) create(name, image string, cmd []string) (string, error) {
	args := []string{"run", "-d", "--name", name}
	if p.network != "" {
		args = append(args, "--network", p.network)
	}
	// No inner cgroup management: the tile's own cgroup already bounds every
	// container it spawns (plans/containers.md), and rootless cgroup v2
	// delegation is often unavailable.
	args = append(args, "--cgroups", "disabled", image)
	if len(cmd) > 0 {
		args = append(args, cmd...)
	} else {
		args = append(args, "sleep", "infinity")
	}
	return p.run(args...)
}

func (p *podman) remove(name string) (string, error) {
	return p.run("rm", "-f", "-t", "2", name)
}

func (p *podman) start(name string) (string, error) { return p.run("start", name) }
func (p *podman) stop(name string) (string, error)  { return p.run("stop", "-t", "3", name) }

// list returns all dev boxes (running + stopped).
func (p *podman) list() ([]container, error) {
	out, err := p.run("ps", "-a", "--format", "json")
	if err != nil {
		return nil, err
	}
	// podman ps --format json emits an array of rich objects; decode the fields
	// we surface.
	var raw []struct {
		Names   []string `json:"Names"`
		Image   string   `json:"Image"`
		State   string   `json:"State"`
		Status  string   `json:"Status"`
		Created any      `json:"CreatedAt"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parse podman ps: %w", err)
	}
	cs := make([]container, 0, len(raw))
	for _, r := range raw {
		name := ""
		if len(r.Names) > 0 {
			name = r.Names[0]
		}
		cs = append(cs, container{
			Name: name, Image: r.Image, State: r.State,
			Status: r.Status, Created: fmt.Sprint(r.Created),
		})
	}
	return cs, nil
}

// exists reports whether a container by that name is present (any state).
func (p *podman) exists(name string) bool {
	out, err := p.run("container", "exists", name)
	_ = out
	return err == nil
}

// running reports whether the named container is currently running.
func (p *podman) running(name string) bool {
	out, err := p.run("inspect", "-f", "{{.State.Running}}", name)
	return err == nil && strings.TrimSpace(out) == "true"
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// validName guards container names (they double as SSH usernames): dns-ish,
// no shell/path surprises.
func validName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case (r == '-' || r == '_' || r == '.') && i > 0:
		default:
			return false
		}
	}
	return true
}
