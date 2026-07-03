//go:build !linux

package cgroup

// Usage is a snapshot of one cgroup's resource accounting.
type Usage struct {
	MemCurrent  int64 `json:"memCurrent"`
	MemMax      int64 `json:"memMax"`
	CPUUsec     int64 `json:"cpuUsec"`
	PidsCurrent int64 `json:"pidsCurrent"`
}

// Manager is a no-op off Linux.
type Manager struct{}

func New() *Manager                           { return &Manager{} }
func (m *Manager) Enabled() bool              { return false }
func (m *Manager) Add(string, int)            {}
func (m *Manager) Usage(string) (Usage, bool) { return Usage{}, false }
func (m *Manager) Remove(string)              {}
