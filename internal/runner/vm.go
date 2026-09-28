package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/vm"
)

// VM backends (plans/vm-sandbox.md): a manifest with "vm" runs its backend
// in a Firecracker microVM — the same binds (FUSE over vsock), the same
// network policy (the relay, outside the VM), its sockets on a guest-local
// run dir bridged over vsock. Nothing about the proxy, the gateway or the
// logs changes; what can't cross (host networking, provider splices, GPUs,
// an env layer) is an error, never a silent namespace fallback.

// vmHealthTimeout covers a cold boot, a first rootfs image build and the
// backend's own start.
const vmHealthTimeout = 60 * time.Second

// vmDumpWait bounds the shim's dump on a health timeout (it asks the guest
// agent, which answers within seconds, or says it didn't).
const vmDumpWait = 10 * time.Second

// dumpVM asks a VM generation that never listened to write what the guest is
// doing to the backend's log — its file requests in flight, every guest
// process's kernel stack, the agent's goroutines, the console
// (internal/sandbox/vm/host/dump_linux.go) — and to quit.
func (r *Runner) dumpVM(inst *instance) {
	if inst.cmd.Process == nil {
		return
	}
	wait := vmDumpWait
	if r.VM != nil && r.VM.Status().Emulated {
		wait *= 3
	}
	_ = inst.cmd.Process.Signal(syscall.SIGQUIT)
	select {
	case <-inst.waitCh:
	case <-time.After(wait):
	}
}

// vmRes is what a VM generation holds until it exits.
type vmRes struct {
	release       func()
	leafMiB       int
	memMiB, vcpus int
	emulated      bool
	stopping      bool
}

type vmState struct {
	mu  sync.Mutex
	res map[string]vmRes // by the generation's listen socket
}

// wantsVM reports whether c's backend runs in a VM here. Given a deployment
// view, its own code's "vm" decides.
func (r *Runner) wantsVM(c *registry.Component) bool {
	return r.Isolate && c.Manifest.VM.Enabled() && sandboxable(c.Manifest.Runtime)
}

func (r *Runner) healthFor(c *registry.Component) time.Duration {
	if !r.wantsVM(c) {
		return healthTimeout
	}
	if r.VM != nil && r.VM.Status().Emulated {
		return 3 * vmHealthTimeout // a software-emulated guest boots and starts slower
	}
	return vmHealthTimeout
}

// vmApply turns the backend's sandbox spec into a VM sandbox under the
// workspace policy (sizes clamped to it) and reserves its memory.
func (r *Runner) vmApply(c *registry.Component, spec *sandbox.Spec, dir, sock, gw string) error {
	if r.VM == nil {
		return sbx.Refuse(errors.New(`this tile asks for a VM ("vm" in xbin.json), but VM sandboxes need isolation and KVM`))
	}
	p := r.VM.Policy()
	if !p.Backends {
		return sbx.Refuse(errors.New(`this tile asks for a VM ("vm" in xbin.json), but an admin hasn't enabled VM backends (PUT /api/xbin/vm/policy)`))
	}
	if st := r.VM.Status(); !st.Available {
		return sbx.Refuse(errors.New("this tile asks for a VM, but VM sandboxes can't run here: " + st.Reason))
	}
	if c.Manifest.Setup != "" {
		return errors.New(`"vm" can't be combined with "setup" yet — install the dependencies at start inside the VM (it is root), or drop "vm"`)
	}
	mem, cpus := guestSize(p, c.Manifest.VM)
	release, err := r.vmReserve(c, p, mem)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := r.VM.Apply(ctx, spec, vm.Options{
		VCPUs: cpus, MemMiB: mem, Hostname: hostname(c.Path),
		Local: []string{dir}, Listen: sock, Gateway: gw,
	}); err != nil {
		release()
		return err
	}
	r.vms.mu.Lock()
	if r.vms.res == nil {
		r.vms.res = map[string]vmRes{}
	}
	emulated := spec.VM != nil && spec.VM.QEMU != "" // the VMM Apply chose
	r.vms.res[sock] = vmRes{release: release, leafMiB: mem + r.VM.OverheadMiB(), memMiB: mem, vcpus: cpus, emulated: emulated}
	r.vms.mu.Unlock()
	return nil
}

// guestSize is a guest's memory and vCPUs: the policy's, lowered by the
// manifest's "vm" sizes, never raised.
func guestSize(p vm.Policy, o *registry.VMOpt) (mem, cpus int) {
	mem, cpus = p.MemMiB, p.VCPUs
	if o == nil {
		return mem, cpus
	}
	if o.MemMiB > 0 && o.MemMiB < mem {
		mem = o.MemMiB
	}
	if o.VCPUs > 0 && o.VCPUs < cpus {
		cpus = o.VCPUs
	}
	return mem, cpus
}

// vmReserve books a guest of mem for a generation spawning from view c,
// always to its tile: a deployment is never a budget owner, so UsedBy stays
// per tile (07-runtime §10.2). Primary first (P25): a non-primary view's
// reservation leaves room for the primary's next guest and never preempts
// (vm.NonPrimary); the primary's may stop the tile's non-primary guests
// when the budget can't admit it otherwise (vm.PrimaryFirst).
func (r *Runner) vmReserve(c *registry.Component, p vm.Policy, mem int) (release func(), err error) {
	opt := vm.PrimaryFirst(r.stopNonPrimaryGuests(c.Path))
	if c.Deployment != "" {
		opt = vm.NonPrimary(r.primaryGuestMiB(c.Path, p))
	}
	return r.VM.Reserve(c.Path, mem, opt)
}

// primaryGuestMiB is the memory of tile's primary's next guest: the
// registry's component describes the primary's code (07-runtime §5.1); 0
// when that code runs no VM here.
func (r *Runner) primaryGuestMiB(tile string, p vm.Policy) int {
	if r.Reg == nil {
		return 0
	}
	c, ok := r.Reg.Component(tile)
	if !ok || !r.wantsVM(c) {
		return 0
	}
	mem, _ := guestSize(p, c.Manifest.VM)
	return mem
}

// stopNonPrimaryGuests is the primary-first callback of tile's primary's
// reservations (vm.PrimaryFirst): when the tile's non-primary deployments'
// running guests together hold at least short, it stops them, all at once,
// and returns once their reservations are given back (a generation's exit
// releases its VM before its stop returns); otherwise it stops nothing and
// returns false. Reserve calls it holding none of its books, and it takes
// no runner lock across a stop, so the exits it waits for never wait on it.
func (r *Runner) stopNonPrimaryGuests(tile string) func(short vm.Usage) bool {
	return func(short vm.Usage) bool {
		primary := r.primary(tile)
		var victims []*state
		var held vm.Usage
		for _, s := range r.allStates(tile) {
			if s.dep == primary {
				continue
			}
			s.mu.Lock()
			inst := s.cur
			s.mu.Unlock()
			if inst == nil {
				continue
			}
			r.vms.mu.Lock()
			res, ok := r.vms.res[inst.sock]
			r.vms.mu.Unlock()
			if ok {
				held.VMs++
				held.MemMiB += res.memMiB
				victims = append(victims, s)
			}
		}
		if len(victims) == 0 || held.VMs < short.VMs || held.MemMiB < short.MemMiB {
			return false
		}
		var wg sync.WaitGroup
		for _, s := range victims {
			slog.Info("stopping a non-primary VM backend to make room for its tile's primary", "component", tile, "deployment", s.dep)
			wg.Add(1)
			go func() { defer wg.Done(); r.stopState(s) }()
		}
		wg.Wait()
		return true
	}
}

// vmLeafBytes is a VM generation's cgroup cap. Generations of one
// deployment share its leaf, and blue/green briefly runs two, so the cap
// covers two VMs; each guest still can't use more than its own memory.
func (r *Runner) vmLeafBytes(sock string) (int64, bool) {
	r.vms.mu.Lock()
	defer r.vms.mu.Unlock()
	res, ok := r.vms.res[sock]
	return int64(2*res.leafMiB) << 20, ok
}

// vmRelease gives a generation's reservation back when it exits.
func (r *Runner) vmRelease(sock string) {
	r.vms.mu.Lock()
	res, ok := r.vms.res[sock]
	delete(r.vms.res, sock)
	r.vms.mu.Unlock()
	if ok {
		res.release()
	}
}

// stopFirst reports whether c's old generation must stop before the new one
// starts: a VM backend with file-backed resources (sqlite, filesystem) sees
// them through the VM file server, where two guests' caches — a WAL's shared memory above all —
// are not coherent with each other. c is the deployment view the new
// generation spawns from, so its own code decides "vm", and its deployment's
// own data binds (dataBinds: main's at their paths, another deployment's
// from its namespace) the resources. It applies per deployment: the old
// generation is the same deployment's, and two deployments never share file
// resources (07-runtime §12). Binds that can't be made fail the start
// itself, and the old generation keeps serving.
func (r *Runner) stopFirst(c *registry.Component) bool {
	if !r.wantsVM(c) {
		return false
	}
	env, _ := r.envFor(c, r.viewDeployment(c))
	binds, err := r.dataBinds(c, env)
	return err == nil && len(binds) > 0
}

// viewDeployment names the deployment a view describes: its Deployment, or
// the primary's for the registry's own component and the primary's views.
func (r *Runner) viewDeployment(c *registry.Component) string {
	if c.Deployment != "" {
		return c.Deployment
	}
	return r.primary(c.Path)
}

// hostname names a VM after its tile: lowercase letters, digits, dashes.
func hostname(comp string) string {
	var b strings.Builder
	for _, ch := range strings.ToLower(path.Base("/" + comp)) {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if h == "" {
		return "xbin-vm"
	}
	if len(h) > 63 {
		h = h[:63]
	}
	return h
}

// restorePrevious restarts the checkpoint deployment dep ran before a
// deploy that stopped it first (a VM with file resources) and then failed,
// from its kept artifact, as that deployment (resolveGenFor, inspect.go):
// "keeps running its previous code" holds as "restarts its previous code"
// (§8.4). A generation of the work tree has no checkpoint to go back to: the
// deployment stays down with the error until a deploy succeeds.
func (r *Runner) restorePrevious(c *registry.Component, s *state, dep string, old *instance) {
	if old.code.Tree == "" {
		return
	}
	r.emit(c.Path, dep, "build-start", "")
	code := old.served()
	g, err := r.resolveGenFor(c, dep, code)
	var inst *instance
	if err == nil {
		s.mu.Lock()
		s.gen++
		gen := s.gen
		s.mu.Unlock()
		if inst, err = r.spawnFor(g.view, dep, g.bin, gen); err != nil {
			g.release()
		}
	}
	if err != nil {
		err = fmt.Errorf("restarting its previous checkpoint %s failed too: %w", codeName(code), err)
		s.mu.Lock()
		s.lastErr = err
		s.mu.Unlock()
		r.emit(c.Path, dep, "build-error", err.Error())
		return
	}
	inst.code, inst.root, inst.artifact = code, g.root, g.artifact
	s.mu.Lock()
	s.cur = inst
	s.lastReq = r.now()
	s.mu.Unlock()
	r.watchGen(c, s, dep, inst, g.release)
	r.emit(c.Path, dep, "build-ok", "")
}
