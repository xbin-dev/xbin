package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Policy is the workspace admin's switch and sizing for VM sandboxes
// (plans/vm-sandbox.md). Off by default: nothing changes for a workspace
// until an admin turns VMs on. Once on, anyone who may open a terminal on a
// tile may make it a VM, and a backend opts in with its manifest — a VM is
// stronger isolation than the namespace sandbox, so the gate is its cost
// (memory), which the budget caps.
type Policy struct {
	Terminals bool `json:"terminals"` // VM terminals may be opened
	Backends  bool `json:"backends"`  // backends with "vm" in the manifest run in VMs
	MemMiB    int  `json:"memMiB"`    // guest memory per VM (default DefaultMemMiB)
	VCPUs     int  `json:"vcpus"`     // vCPUs per VM (default DefaultVCPUs)
	MaxVMs    int  `json:"maxVMs"`    // concurrent VMs (default 8)
	BudgetMiB int  `json:"budgetMiB"` // guest memory across running VMs (0 = maxVMs × memMiB)
	DiskGiB   int  `json:"diskGiB"`   // a VM terminal's persistent disk (sparse; default 20)
}

const (
	defaultMaxVMs  = 8
	defaultDiskGiB = 20
)

// withDefaults fills the zero fields.
func (p Policy) withDefaults() Policy {
	if p.MemMiB <= 0 {
		p.MemMiB = DefaultMemMiB
	}
	if p.VCPUs <= 0 {
		p.VCPUs = DefaultVCPUs
	}
	if p.MaxVMs <= 0 {
		p.MaxVMs = defaultMaxVMs
	}
	if p.BudgetMiB <= 0 {
		p.BudgetMiB = p.MaxVMs * p.MemMiB
	}
	if p.DiskGiB <= 0 {
		p.DiskGiB = defaultDiskGiB
	}
	return p
}

// Validate refuses nonsense sizes.
func (p Policy) Validate() error {
	switch {
	case p.MemMiB != 0 && (p.MemMiB < 256 || p.MemMiB > 1<<20):
		return fmt.Errorf("memMiB must be between 256 and 1048576")
	case p.VCPUs < 0 || p.VCPUs > 32:
		return fmt.Errorf("vcpus must be between 1 and 32")
	case p.MaxVMs < 0 || p.MaxVMs > 1024:
		return fmt.Errorf("maxVMs must be between 1 and 1024")
	case p.BudgetMiB < 0:
		return fmt.Errorf("budgetMiB must not be negative")
	case p.DiskGiB < 0 || p.DiskGiB > 4096:
		return fmt.Errorf("diskGiB must be between 1 and 4096")
	}
	return nil
}

func (m *Manager) policyPath() string { return filepath.Join(m.Root, ".xbin", "vm", "policy.json") }

// Policy returns the effective policy (defaults filled).
func (m *Manager) Policy() Policy {
	if m == nil {
		return Policy{}.withDefaults()
	}
	m.pmu.Lock()
	defer m.pmu.Unlock()
	if !m.ploaded {
		if b, err := os.ReadFile(m.policyPath()); err == nil {
			_ = json.Unmarshal(b, &m.policy)
		}
		m.ploaded = true
	}
	return m.policy.withDefaults()
}

// SetPolicy stores p (the zero sizes mean "default").
func (m *Manager) SetPolicy(p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.policyPath()), 0o755); err != nil {
		return err
	}
	tmp := m.policyPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.policyPath()); err != nil {
		return err
	}
	m.pmu.Lock()
	m.policy, m.ploaded = p, true
	m.pmu.Unlock()
	return nil
}

// Usage is what running VMs hold.
type Usage struct {
	VMs    int `json:"vms"`
	MemMiB int `json:"memMiB"`
}

// Reserve admits one VM of memMiB under the policy's count and budget; the
// returned release gives it back when the VM ends.
func (m *Manager) Reserve(memMiB int) (release func(), err error) {
	p := m.Policy()
	m.umu.Lock()
	defer m.umu.Unlock()
	if m.used.VMs+1 > p.MaxVMs {
		return nil, fmt.Errorf("the workspace's VM limit (%d running) is reached — close a VM terminal or ask an admin to raise it", p.MaxVMs)
	}
	if m.used.MemMiB+memMiB > p.BudgetMiB {
		return nil, fmt.Errorf("the workspace's VM memory budget (%d MiB) is spent — close a VM terminal or ask an admin to raise it", p.BudgetMiB)
	}
	m.used.VMs++
	m.used.MemMiB += memMiB
	var once sync.Once
	return func() {
		once.Do(func() {
			m.umu.Lock()
			m.used.VMs--
			m.used.MemMiB -= memMiB
			m.umu.Unlock()
		})
	}, nil
}

// Used reports what running VMs hold.
func (m *Manager) Used() Usage {
	if m == nil {
		return Usage{}
	}
	m.umu.Lock()
	defer m.umu.Unlock()
	return m.used
}

// VMOverheadMiB is what a VM's cgroup leaf holds beyond guest memory: the
// shim, Firecracker, the file server's buffers. An emulated VM's QEMU adds
// its translated-code cache (256 MiB) and the vsock backend.
const (
	VMOverheadMiB       = 192
	EmulatedOverheadMiB = 512
)
