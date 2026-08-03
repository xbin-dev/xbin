//go:build !linux

package cgroup

// Usage is a snapshot of one cgroup's resource accounting.
type Usage struct {
	MemCurrent  int64 `json:"memCurrent"`
	MemMax      int64 `json:"memMax"`
	CPUUsec     int64 `json:"cpuUsec"`
	PidsCurrent int64 `json:"pidsCurrent"`
}

// Limits are per-component caps (no-op off Linux).
type Limits struct {
	MemMax    int64
	PidsMax   int64
	CPUWeight int64
}

// Manager is a no-op off Linux.
type Manager struct{}

func New() *Manager                                    { return &Manager{} }
func (m *Manager) Enabled() bool                       { return false }
func (m *Manager) SetLimits(Limits)                    {}
func (m *Manager) Add(string, int)                     {}
func (m *Manager) Usage(string) (Usage, bool)          { return Usage{}, false }
func (m *Manager) AtLimit(string) (int64, int64, bool) { return 0, 0, false }
func (m *Manager) Procs(string) ([]int, bool)          { return nil, false }
func (m *Manager) Remove(string)                       {}
