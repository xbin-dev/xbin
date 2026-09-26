package vm

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
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
}

// Status is whether VM sandboxes can start here, and if not, why.
type Status struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// Status probes KVM and the assets (cheap: an open, an ioctl, a few stats).
func (m *Manager) Status() Status {
	if m == nil {
		return Status{Reason: "VM sandboxes need isolation (--isolate)"}
	}
	if _, err := m.findAssets(); err != nil {
		return Status{Reason: err.Error()}
	}
	if err := kvmUsable(); err != nil {
		return Status{Reason: err.Error()}
	}
	return Status{Available: true}
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
	TTY      bool // the shim's stdio is a terminal (a shell session)
	VCPUs    int
	MemMiB   int
	Hostname string
}

// Defaults for a VM nobody sized.
const (
	DefaultVCPUs  = 2
	DefaultMemMiB = 2048
)

// Paths of the VM's pieces inside the namespace sandbox (never exported).
var (
	inBx     = sandbox.VMDir + "/bin/bx"
	inFC     = sandbox.VMDir + "/bin/firecracker"
	inKernel = sandbox.VMDir + "/boot/vmlinux"
	inInitrd = sandbox.VMDir + "/boot/initrd"
	inImage  = sandbox.VMDir + "/img/rootfs.erofs"
	inRun    = sandbox.VMDir + "/run"
)

// Apply turns spec — built for a namespace sandbox — into a VM sandbox: the
// same binds become the guest's 9P mounts at the same paths, the entry
// becomes the guest's session 1, and the namespace sandbox shrinks to a
// bare root holding the shim and Firecracker. What a VM can't carry is an
// error, never silently dropped: host networking, provider splices and
// lan-ingress links, device nodes (GPUs), env layers.
func (m *Manager) Apply(ctx context.Context, spec *sandbox.Spec, o Options) error {
	a, err := m.findAssets()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if err := kvmUsable(); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	switch {
	case spec.HostNet:
		return fmt.Errorf("a VM sandbox can't share the host network")
	case spec.Net == "splice" || spec.NetAddr != "" || len(spec.NetClients) > 0 || len(spec.NetLinks) > 0:
		return fmt.Errorf("a VM sandbox can't be spliced to a provider tile or carry lan-ingress links yet")
	case len(spec.Lower) != 1:
		return fmt.Errorf("a VM sandbox can't stack an env layer (setup) yet")
	}
	mounts, err := exports(spec.Binds)
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
		Firecracker: inFC,
		Kernel:      inKernel,
		Initrd:      inInitrd,
		Image:       inImage,
		RunDir:      inRun,
		VCPUs:       o.VCPUs,
		MemMiB:      o.MemMiB,
		Hostname:    o.Hostname,
		Mounts:      mounts,
		Debug:       spec.Debug || m.Debug,
		Guest: proto.Exec{
			Path: spec.Entry,
			Argv: spec.Argv,
			Env:  spec.Env,
			Cwd:  spec.Cwd,
			TTY:  o.TTY,
		},
	}
	if hs.VCPUs <= 0 {
		hs.VCPUs = DefaultVCPUs
	}
	if hs.MemMiB <= 0 {
		hs.MemMiB = DefaultMemMiB
	}
	if hs.Hostname == "" {
		hs.Hostname = "xbin-vm"
	}
	if spec.Net == "relay" {
		hs.Tap, hs.GuestMAC = proto.TapName, proto.GuestMAC
		hs.Net = &proto.Net{Addr: proto.GuestAddr, Gw: proto.GatewayIP, GwMAC: proto.TapMAC, DNS: proto.GuestDNS, MTU: 1500}
	}
	spec.Binds = append(spec.Binds,
		sandbox.Bind{Src: a.Bx, Dst: inBx, RO: true},
		sandbox.Bind{Src: a.Firecracker, Dst: inFC, RO: true},
		sandbox.Bind{Src: a.Kernel, Dst: inKernel, RO: true},
		sandbox.Bind{Src: initrd, Dst: inInitrd, RO: true},
		sandbox.Bind{Src: image, Dst: inImage, RO: true},
	)
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

// exports lists the binds the guest mounts over 9P, parents first. Masks stay
// in the shim's view (the walk finds them empty); sockets can't cross 9P;
// device nodes can't enter a guest.
func exports(binds []sandbox.Bind) ([]proto.Mount, error) {
	var out []proto.Mount
	for _, b := range binds {
		if b.Mask {
			continue
		}
		dst := path.Clean("/" + b.Dst)
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
