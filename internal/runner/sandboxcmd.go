package runner

// sandboxcmd.go — the isolated backend's command: which runtimes run in a
// sandbox, and the sandbox spec a backend generation launches with.

import (
	"fmt"
	"os"
	"os/exec"
	"path"
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
// (plans/isolation.md): the generation's launchSpec, with the components
// nested in pinned code bound back from their own code (nestedBinds), turned
// into a VM sandbox when the tile runs its backend in one (vmApply reserves
// the VM's memory), then sandbox.Launch.
func (r *Runner) sandboxCmd(c *registry.Component, bin, dir, sock string, env []string, pol sandbox.EgressPolicy, envLower string) (*exec.Cmd, *sandbox.Handle, error) {
	nested, err := r.nestedBinds(c)
	if err != nil {
		return nil, nil, err
	}
	spec := r.launchSpec(c, bin, dir, env, pol, envLower, nested...)
	if r.wantsVM(c) {
		if err := r.vmApply(c, spec, dir, sock, filepath.Join(r.RunDir, "gateway.sock")); err != nil {
			return nil, nil, err
		}
	}
	return sandbox.Launch(spec)
}

// launchSpec is the sandbox spec of an isolated backend generation: a
// per-component namespace set over an overlay of r.Rootfs. The component's
// code is read-only at its own path — the work tree, or for pinned code its
// materialized checkpoint (c.CodeRoot) with each nested component's code
// bound after it (nested, from nestedBinds; P9) — its run dir and
// same-scope resource files are read-write, the gateway socket is the one
// door out, and the netns is empty (default-deny egress) unless the tile's
// grants and bindings wire a relay, a splice or the host network. It is
// pure: it reads the runner's hooks and the host paths it binds, and
// launches, reserves and records nothing.
func (r *Runner) launchSpec(c *registry.Component, bin, dir string, env []string, pol sandbox.EgressPolicy, envLower string, nested ...sandbox.Bind) *sandbox.Spec {
	gw := filepath.Join(r.RunDir, "gateway.sock")
	binds := []sandbox.Bind{{Src: codeRoot(c), Dst: c.Dir, RO: true}} // the code, read-only by the bind
	binds = append(binds, nested...)
	binds = append(binds,
		sandbox.Bind{Src: dir, Dst: dir}, // run dir — the listen socket lands here
		sandbox.Bind{Src: gw, Dst: gw},   // the gateway socket (component↔component + RBAC)
	)
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
		entry, argv = rootfsBin(r.Rootfs, "node"), []string{"node", bin} // bin is a script at its canonical path under c.Dir (bound)
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

// codeRoot is the host directory a view's code is in, which its sandboxes
// show at c.Dir: its materialized checkpoint, or the work tree.
func codeRoot(c *registry.Component) string {
	if c.CodeRoot != "" {
		return c.CodeRoot
	}
	return c.Dir
}

// nestedBinds are the binds of the components nested in c's pinned code,
// each at its own path from its own primary's code (showCode), read-only:
// a checkpoint excludes them. The sandbox init makes their mount points
// inside the checkpoint without following a symlink, and fails the start on
// one in the way (06-security T18.3). The work tree shows its nested
// components itself: nothing to bind.
func (r *Runner) nestedBinds(c *registry.Component) ([]sandbox.Bind, error) {
	if c.CodeRoot == "" {
		return nil, nil
	}
	shown, err := r.showCode(c, nil)
	if err != nil {
		return nil, err
	}
	var binds []sandbox.Bind
	for _, s := range shown {
		binds = append(binds, sandbox.Bind{Src: s.root, Dst: s.comp.Dir, RO: true})
	}
	return binds, nil
}

// shownComp is a component a sandbox shows from its primary's code rather
// than through the tree around it.
type shownComp struct {
	comp *registry.Component
	root string // the host directory bound at comp.Dir
	tree string // its checkpoint's tree; "" = its work tree
}

// showCode lists the components a sandbox of c's pinned code binds at their
// own paths, ancestors first, each from its own primary's code: its work
// tree, or its pinned checkpoint (05-model §6). They are every component
// nested in c, since a checkpoint excludes them, and every component of
// modules (a Go build's go.work modules) whose primary is pinned; then, in
// both, what nests in a tree shown from a checkpoint. A work-tree component
// inside a work tree already shown needs no bind of its own.
func (r *Runner) showCode(c *registry.Component, modules map[string]bool) ([]shownComp, error) {
	fromCkpt := map[string]bool{c.Path: true} // what is shown so far: from a checkpoint?
	var out []shownComp
	for _, n := range r.components() {
		if n.Path == c.Path {
			continue
		}
		enc, under := nearestShown(fromCkpt, n.Path)
		if !under && !modules[n.Path] {
			continue
		}
		tree, root, err := r.primaryCode(n)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n.Path, err)
		}
		switch {
		case under && !fromCkpt[enc] && tree == "":
			fromCkpt[n.Path] = false // in the enclosing work tree already
			continue
		case !under && tree == "":
			continue // a module on its work tree: the workspace shows it
		}
		fromCkpt[n.Path] = tree != ""
		out = append(out, shownComp{comp: n, root: root, tree: tree})
	}
	return out, nil
}

// nearestShown is the nearest ancestor of p that shown holds.
func nearestShown(shown map[string]bool, p string) (string, bool) {
	for q := path.Dir(p); q != "." && q != "/"; q = path.Dir(q) {
		if _, ok := shown[q]; ok {
			return q, true
		}
	}
	return "", false
}

// primaryCode is the code n's primary runs: its checkpoint's tree and
// materialized tree, or ("", n.Dir) for its work tree.
func (r *Runner) primaryCode(n *registry.Component) (tree, root string, err error) {
	code, err := r.codeFor(n.Path, r.primary(n.Path))
	switch {
	case err != nil:
		return "", "", err
	case code.WorkTree:
		return "", n.Dir, nil
	case !fullTree(code.Tree):
		return "", "", fmt.Errorf("%q is not a checkpoint tree", code.Tree)
	}
	root, err = r.materialize(n.Path, code.Tree)
	return code.Tree, root, err
}

// components is the registry's components, sorted by path; none without one.
func (r *Runner) components() []*registry.Component {
	if r.Reg == nil {
		return nil
	}
	return r.Reg.Components()
}
