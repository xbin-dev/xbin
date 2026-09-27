package vm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// Manager prepares VM sandboxes for one workspace.
type Manager struct {
	Root   string // workspace root (.xbin/vm/ holds images and initrds)
	Rootfs string // the base rootfs the guest image is built from
	Bx     string // the daemon's static bx (the shim)

	amu    sync.Mutex
	assets Assets
	aerr   error
	found  bool

	kmu   sync.Mutex
	keys  map[string]*sync.Mutex
	Logf  func(format string, args ...any)
	Debug bool

	pmu     sync.Mutex
	policy  Policy
	ploaded bool
	umu     sync.Mutex
	used    Usage
	byOwner map[string]Usage
	// usedTiles is the part of used that tile sandboxes hold (reserve.go).
	usedTiles Usage

	prmu sync.Mutex
	pr   *probe // the last probe, reused for statusTTL
}

// Status is whether VM sandboxes can start here, and if not, why.
type Status struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Emulated: KVM isn't usable here, so VMs run under QEMU's software
	// emulation — the same guest and isolation, several times slower. Note
	// says why.
	Emulated bool   `json:"emulated,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Status probes KVM and the assets (cheap: an open, an ioctl, a few stats —
// and reused for a few seconds, so an admin page polling it costs nothing).
func (m *Manager) Status() Status {
	if m == nil {
		return Status{Reason: "VM sandboxes need isolation (--isolate)"}
	}
	_, st := m.backend()
	return st
}

// statusTTL is how long a probe is reused: short enough that an admin who
// fixes /dev/kvm's group or drops in Firecracker sees it without a restart.
var statusTTL = 5 * time.Second

// kvmProbe is kvmUsable (a seam for tests).
var kvmProbe = kvmUsable

// probe is one look at what VM sandboxes need here.
type probe struct {
	at       time.Time
	assets   Assets
	status   Status
	missing  []string // the pieces both VMMs need that aren't here
	kvm, emu error    // what each VMM lacks (nil = nothing)
}

// backend decides how a VM runs here: under Firecracker when KVM is usable,
// else under QEMU's emulation when its pieces are shipped (small cloud VMs
// rarely offer nested virtualization). XBIN_VM_ACCEL=kvm or =emulate forces
// one.
func (m *Manager) backend() (Assets, Status) {
	p := m.look()
	return p.assets, p.status
}

func (m *Manager) look() probe {
	m.prmu.Lock()
	defer m.prmu.Unlock()
	if m.pr != nil && time.Since(m.pr.at) < statusTTL {
		return *m.pr
	}
	p := probe{at: time.Now()}
	a, err := m.findAssets()
	p.assets = a
	if err != nil {
		p.status, p.missing = Status{Reason: err.Error()}, a.missing()
	} else {
		if p.kvm = kvmProbe(); p.kvm == nil {
			p.kvm = a.kvm()
		}
		p.emu = a.emulation()
		p.status = decide(os.Getenv("XBIN_VM_ACCEL"), func() error { return p.kvm }, func() error { return p.emu })
	}
	m.pr = &p
	return p
}

// Health is Status with what an admin needs to act on it: which VMM runs,
// what each one lacks here, and where the pieces were found.
type Health struct {
	Status
	Accel     string            `json:"accel,omitempty"`     // kvm | emulate, when VMs can start
	Forced    string            `json:"forced,omitempty"`    // XBIN_VM_ACCEL
	Assets    map[string]string `json:"assets,omitempty"`    // piece → host path, for those found
	Missing   []string          `json:"missing,omitempty"`   // pieces both VMMs need
	KVM       string            `json:"kvm,omitempty"`       // why Firecracker on KVM can't run ("" = it can)
	Emulation string            `json:"emulation,omitempty"` // why QEMU emulation can't run ("" = it can)
}

// Health reports the (cached) probe in full.
func (m *Manager) Health() Health {
	if m == nil {
		return Health{Status: m.Status()}
	}
	p := m.look()
	h := Health{Status: p.status, Forced: os.Getenv("XBIN_VM_ACCEL"), Missing: p.missing, Assets: map[string]string{}}
	if p.status.Available {
		h.Accel = "kvm"
		if p.status.Emulated {
			h.Accel = "emulate"
		}
	}
	if p.kvm != nil {
		h.KVM = p.kvm.Error()
	}
	if p.emu != nil {
		h.Emulation = p.emu.Error()
	}
	a := p.assets
	for k, v := range map[string]string{"firecracker": a.Firecracker, "qemu": a.QEMU, "vhostVsock": a.VsockDev,
		"qemuBios": a.BIOS, "qemuPvh": a.PVH, "kernel": a.Kernel, "agent": a.Agent, "mkfsErofs": a.Mkfs, "bx": a.Bx} {
		if v != "" {
			h.Assets[k] = v
		}
	}
	return h
}

// decide picks the VMM from what KVM and emulation lack (nil = nothing).
func decide(force string, kvm, emulation func() error) Status {
	var noKVM error
	if force == "emulate" {
		noKVM = errors.New("XBIN_VM_ACCEL=emulate")
	} else {
		noKVM = kvm()
	}
	if noKVM == nil {
		return Status{Available: true}
	}
	if force == "kvm" {
		return Status{Reason: noKVM.Error()}
	}
	if err := emulation(); err != nil {
		return Status{Reason: noKVM.Error() + "; " + err.Error()}
	}
	return Status{Available: true, Emulated: true,
		Note: "no KVM (" + noKVM.Error() + "): VMs run under software emulation, several times slower"}
}

// OverheadMiB is what a VM's cgroup leaf holds beyond guest memory here.
func (m *Manager) OverheadMiB() int {
	if m.Status().Emulated {
		return EmulatedOverheadMiB
	}
	return VMOverheadMiB
}

func (m *Manager) findAssets() (Assets, error) {
	m.amu.Lock()
	defer m.amu.Unlock()
	if m.found {
		return m.assets, nil
	}
	a, err := FindAssets(m.Bx)
	if err != nil {
		return a, err
	}
	m.assets, m.found = a, true
	return a, nil
}

// Options size and shape one VM.
type Options struct {
	TTY  bool   // the shim's stdio is a terminal (a shell session)
	Disk string // host path of the persistent upper disk image ("" = a tmpfs upper)
	// Backends: Local dirs are guest-local tmpfs at their host paths instead
	// of mounts from the host (the run dir: sockets must be the guest's own); Listen is the
	// backend's socket (bridged back to the same host path once the guest
	// process listens); Gateway is xbind's socket, bridged into the guest.
	Local           []string
	Listen, Gateway string
	VCPUs           int
	MemMiB          int
	Hostname        string
	// Resident makes it a tile sandbox's VM (plans/tile-sandbox-runtime.md
	// §2.5): no session 1 — the spec's entry is ignored — and the shim
	// routes the connections xbind makes through spec.Agent (the connection
	// factory, required) to the guest's agent until xbind hangs up. There is
	// no gateway and no listen socket in a tile sandbox, and no TTY.
	Resident bool
}

// Defaults for a VM nobody sized.
const (
	DefaultVCPUs  = 2
	DefaultMemMiB = 2048
)

// Paths of the VM's pieces inside the namespace sandbox (never exported).
var (
	inBx       = sandbox.VMDir + "/bin/bx"
	inFC       = sandbox.VMDir + "/bin/firecracker"
	inQEMU     = sandbox.VMDir + "/bin/qemu"
	inVsockDev = sandbox.VMDir + "/bin/vhost-device-vsock"
	inFirmware = sandbox.VMDir + "/fw"
	inKernel   = sandbox.VMDir + "/boot/vmlinux"
	inInitrd   = sandbox.VMDir + "/boot/initrd"
	inImage    = sandbox.VMDir + "/img/rootfs.erofs"
	inDisk     = sandbox.VMDir + "/img/disk.img"
	inRun      = sandbox.VMDir + "/run"
)

// Apply turns spec — built for a namespace sandbox — into a VM sandbox: the
// same binds become the guest's file mounts at the same paths, the entry
// becomes the guest's session 1 (none for a resident VM, whose spec.Agent
// and spec.Lock stay the shim's), and the namespace sandbox shrinks to a
// bare root holding the shim and the VMM (Firecracker, or QEMU emulating). What a VM can't carry is an
// error, never silently dropped: host networking, provider splices and
// lan-ingress links, device nodes (GPUs), env layers.
func (m *Manager) Apply(ctx context.Context, spec *sandbox.Spec, o Options) error {
	a, st := m.backend()
	if !st.Available {
		return sbx.Refuse(fmt.Errorf("%w: %s", ErrUnavailable, st.Reason))
	}
	switch {
	case spec.HostNet:
		return fmt.Errorf("a VM sandbox can't share the host network")
	case spec.Net == "splice" || spec.NetAddr != "" || len(spec.NetClients) > 0 || len(spec.NetLinks) > 0:
		return fmt.Errorf("a VM sandbox can't be spliced to a provider tile or carry lan-ingress links yet")
	case len(spec.Lower) != 1:
		return fmt.Errorf("a VM sandbox can't stack an env layer (setup) yet")
	case o.Resident && spec.Agent == nil:
		return fmt.Errorf("a resident VM needs its connection factory (spec.Agent)")
	case o.Resident && (o.Listen != "" || o.Gateway != "" || o.TTY):
		return fmt.Errorf("a resident VM has no listen socket, gateway or TTY")
	}
	mounts, err := exports(spec.Binds, o.Local)
	if err != nil {
		return err
	}
	image, err := m.image(ctx, spec.Lower[0])
	if err != nil {
		return err
	}
	initrd, err := m.initrd()
	if err != nil {
		return fmt.Errorf("build the VM initramfs: %w", err)
	}
	hs := &proto.HostSpec{
		Kernel:   inKernel,
		Initrd:   inInitrd,
		Image:    inImage,
		RunDir:   inRun,
		VCPUs:    o.VCPUs,
		MemMiB:   o.MemMiB,
		Hostname: o.Hostname,
		Mounts:   mounts,
		Local:    o.Local,
		Listen:   o.Listen,
		Gateway:  o.Gateway,
		Debug:    spec.Debug || m.Debug,
		Resident: o.Resident,
	}
	if !o.Resident {
		hs.Guest = proto.Exec{
			Path:    spec.Entry,
			Argv:    spec.Argv,
			Env:     spec.Env,
			Cwd:     spec.Cwd,
			TTY:     o.TTY,
			Listen:  o.Listen,
			Gateway: o.Gateway,
		}
	}
	if hs.VCPUs <= 0 {
		hs.VCPUs = DefaultVCPUs
	}
	if hs.MemMiB <= 0 {
		hs.MemMiB = DefaultMemMiB
	}
	if hs.Hostname == "" {
		hs.Hostname = spec.Hostname
	}
	if hs.Hostname == "" {
		hs.Hostname = "xbin-vm"
	}
	if spec.Net == "relay" {
		hs.Tap, hs.GuestMAC = proto.TapName, proto.GuestMAC
		hs.Net = &proto.Net{Addr: proto.GuestAddr, Gw: proto.GatewayIP, GwMAC: proto.TapMAC, DNS: proto.GuestDNS, MTU: 1500}
	}
	if st.Emulated {
		hs.QEMU, hs.VsockDev, hs.Firmware = inQEMU, inVsockDev, inFirmware
		spec.Binds = append(spec.Binds,
			sandbox.Bind{Src: a.QEMU, Dst: inQEMU, RO: true},
			sandbox.Bind{Src: a.VsockDev, Dst: inVsockDev, RO: true},
			sandbox.Bind{Src: a.BIOS, Dst: inFirmware + "/bios-microvm.bin", RO: true},
			sandbox.Bind{Src: a.PVH, Dst: inFirmware + "/pvh.bin", RO: true},
		)
	} else {
		hs.Firecracker = inFC
		spec.Binds = append(spec.Binds, sandbox.Bind{Src: a.Firecracker, Dst: inFC, RO: true})
	}
	spec.Binds = append(spec.Binds,
		sandbox.Bind{Src: a.Bx, Dst: inBx, RO: true},
		sandbox.Bind{Src: a.Kernel, Dst: inKernel, RO: true},
		sandbox.Bind{Src: initrd, Dst: inInitrd, RO: true},
		sandbox.Bind{Src: image, Dst: inImage, RO: true},
	)
	if o.Disk != "" {
		spec.Binds = append(spec.Binds, sandbox.Bind{Src: o.Disk, Dst: inDisk})
		hs.Disk = inDisk
	}
	spec.VM = hs
	spec.Lower, spec.Upper, spec.Work = nil, "", ""
	spec.Entry = inBx
	spec.Argv = []string{"bx", "__vm-host", sandbox.VMSpecPath}
	spec.Env = []string{"PATH=" + sandbox.VMDir + "/bin"}
	spec.Cwd = "/"
	// root in the guest is the guest's: the namespace-side privileges these
	// grants unlock are moot, and the VM lockdown replaces them
	spec.NetAdmin, spec.Containers = false, false
	return nil
}

// exports lists the binds the guest mounts, parents first. Masks stay in the
// shim's view (the walk finds them empty); sockets can't cross a filesystem;
// device nodes can't enter a guest; binds at or under a local dir are the
// guest's own.
func exports(binds []sandbox.Bind, local []string) ([]proto.Mount, error) {
	var out []proto.Mount
next:
	for _, b := range binds {
		if b.Mask {
			continue
		}
		dst := path.Clean("/" + b.Dst)
		for _, l := range local {
			if l = path.Clean("/" + l); dst == l || strings.HasPrefix(dst, l+"/") {
				continue next
			}
		}
		if dst == "/" || dst == sandbox.VMDir || strings.HasPrefix(dst, sandbox.VMDir+"/") {
			return nil, fmt.Errorf("a VM sandbox can't export %s", dst)
		}
		fi, err := os.Stat(b.Src)
		if err != nil {
			return nil, fmt.Errorf("bind %s: %w", b.Src, err)
		}
		switch {
		case fi.Mode()&os.ModeSocket != 0:
			continue
		case fi.Mode()&(os.ModeDevice|os.ModeCharDevice) != 0:
			return nil, fmt.Errorf("a VM sandbox can't pass device %s (GPUs stay with namespace sandboxes)", b.Src)
		}
		out = append(out, proto.Mount{Path: dst, RO: b.RO, File: !fi.IsDir()})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.Count(out[i].Path, "/") < strings.Count(out[j].Path, "/")
	})
	return out, nil
}

func (m *Manager) lockKey(k string) func() {
	m.kmu.Lock()
	if m.keys == nil {
		m.keys = map[string]*sync.Mutex{}
	}
	mu := m.keys[k]
	if mu == nil {
		mu = &sync.Mutex{}
		m.keys[k] = mu
	}
	m.kmu.Unlock()
	mu.Lock()
	return mu.Unlock
}

func (m *Manager) logf(format string, args ...any) {
	if m.Logf != nil {
		m.Logf(format, args...)
		return
	}
	slog.Info(fmt.Sprintf(format, args...))
}
