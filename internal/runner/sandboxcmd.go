package runner

// sandboxcmd.go — the isolated backend's command: which runtimes run in a
// sandbox, and the sandbox spec a backend generation launches with.

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
)

func sandboxable(runtime string) bool {
	switch runtime {
	case "go", "node", "python":
		return true
	}
	return false
}

// sandboxCmd builds and launches the isolated backend command
// (plans/isolation.md): the generation's launchSpec, turned into a VM sandbox
// when the tile runs its backend in one (vmApply reserves the VM's memory),
// then sandbox.Launch.
func (r *Runner) sandboxCmd(c *registry.Component, bin, dir, sock string, env []string, pol sandbox.EgressPolicy, envLower string) (*exec.Cmd, *sandbox.Handle, error) {
	spec := r.launchSpec(c, bin, dir, env, pol, envLower)
	if r.wantsVM(c) {
		if err := r.vmApply(c, spec, dir, sock, filepath.Join(r.RunDir, "gateway.sock")); err != nil {
			return nil, nil, err
		}
	}
	return sandbox.Launch(spec)
}

// launchSpec is the sandbox spec of an isolated backend generation: a
// per-component namespace set over an overlay of r.Rootfs. The component's
// source is read-only, its run dir and same-scope resource files are
// read-write, the gateway socket is the one door out, and the netns is empty
// (default-deny egress) unless the tile's grants and bindings wire a relay,
// a splice or the host network. It is pure: it reads the runner's hooks and
// the host paths it binds, and launches, reserves and records nothing.
func (r *Runner) launchSpec(c *registry.Component, bin, dir string, env []string, pol sandbox.EgressPolicy, envLower string) *sandbox.Spec {
	gw := filepath.Join(r.RunDir, "gateway.sock")
	binds := []sandbox.Bind{
		{Src: c.Dir, Dst: c.Dir, RO: true}, // component source, read-only
		{Src: dir, Dst: dir},               // run dir — the listen socket lands here
		{Src: gw, Dst: gw},                 // the gateway socket (component↔component + RBAC)
	}
	// File-backed resources (sqlite) are handed to the backend as absolute
	// XBIN_RES_* env paths; bind their dirs read-write so they persist.
	binds = append(binds, resourceBinds(env, r.Root)...)

	var entry string
	var argv []string
	switch c.Manifest.Runtime {
	case "go":
		entry = "/run/backend" // the built static binary, bound in
		argv = []string{entry}
		binds = append(binds, sandbox.Bind{Src: bin, Dst: entry, RO: true})
	case "node":
		entry, argv = rootfsBin(r.Rootfs, "node"), []string{"node", bin} // bin is a script under c.Dir (bound)
	case "python":
		entry, argv = rootfsBin(r.Rootfs, "python3"), []string{"python3", bin}
	}

	// Granted GPUs (gpu:*): bind the device nodes + driver libs and add env.
	if r.GPU != nil {
		if gb, genv := gpu.Binds(r.GPU(c)); len(gb) > 0 {
			binds = append(binds, gb...)
			env = append(env[:len(env):len(env)], genv...) // a copy: never into the caller's spare capacity
		}
	}

	// Overlay lowers: the component env layer (setup deps) on top of the base
	// rootfs, so the layer's files win.
	lower := []string{r.Rootfs}
	if envLower != "" {
		lower = []string{envLower, r.Rootfs}
	}
	spec := &sandbox.Spec{
		Lower:        lower,
		Binds:        binds,
		Entry:        entry,
		Argv:         argv,
		Env:          env,
		Cwd:          c.Dir,
		HostUID:      os.Getuid(),
		HostGID:      os.Getgid(),
		Unprivileged: true, // tile backends need no caps: drop them + seccomp block-list
	}
	// Interface wiring (plans/interfaces.md): a net-provider tile gets one TUN per
	// bound client; a component's `net` interface resolves to host-share, a splice
	// through a provider tile, or the relay under a builtin (internet/lan) policy.
	if r.NetRoster != nil {
		spec.NetClients = r.NetRoster(c)
	}
	if r.NetLinks != nil {
		spec.NetLinks = r.NetLinks(c) // lan-ingress legs (plans/ingress.md)
	}
	if r.NetCaps != nil && r.NetCaps(c) {
		spec.NetAdmin = true // net-provider tile (cap:net-admin) keeps net-admin caps
	}
	if r.ContainerCaps != nil && r.ContainerCaps(c) {
		spec.Containers = true // container-host tile (cap:containers): keep caps, minimal seccomp
	}
	if r.NetHost != nil && r.NetHost(c) {
		spec.HostNet = true // net → host builtin (share the host network)
	} else if r.NetTarget != nil {
		if _, addr, gw, ok := r.NetTarget(c); ok {
			spec.Net, spec.NetAddr, spec.NetGw = "splice", addr, gw
		}
	}
	if spec.Net == "" && !spec.HostNet && !pol.Empty() {
		spec.Net = "relay" // granted / bound builtin egress → TUN + userspace relay
	}
	if spec.Net == "" && !spec.HostNet {
		// Ingress plumbing without egress (plans/ingress.md): a bound stream
		// expose / stream interface / lan-ingress leg needs the TUN so xbind
		// can reach in — the relay runs with a deny-all outbound policy.
		if (r.IngressNet != nil && r.IngressNet(c)) || len(spec.NetLinks) > 0 {
			spec.Net = "relay"
		}
	}
	return spec
}
