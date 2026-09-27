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
	MemMax     int64
	PidsMax    int64
	CPUWeight  int64
	NodeWeight int64
}

// The per-tile layout's CPU weights, as on Linux.
const (
	TileWeight       = 100
	PrimaryWeight    = 100
	NonPrimaryWeight = 50
)

// Leaf is one of a tile's leaves (none off Linux).
type Leaf struct {
	Name       string
	Deployment string
}

// TileNode, DeploymentNode and DeploymentLeaf name the per-tile layout's
// nodes, as on Linux.
func TileNode(compKey string) string { return "tile-" + compKey + "/" }

func DeploymentNode(compKey, dep string) string { return "tile-" + compKey + "/d-" + dep }

func DeploymentLeaf(compKey, dep string) string { return DeploymentNode(compKey, dep) + "/backend" }

// DeploymentWeight is a deployment node's cpu.weight, as on Linux.
func DeploymentWeight(primary bool) int64 {
	if primary {
		return PrimaryWeight
	}
	return NonPrimaryWeight
}

// Manager is a no-op off Linux.
type Manager struct{}

func New() *Manager                                      { return &Manager{} }
func (m *Manager) Enabled() bool                         { return false }
func (m *Manager) SetLimits(Limits)                      {}
func (m *Manager) Add(string, int)                       {}
func (m *Manager) AddWith(string, int, Limits)           {}
func (m *Manager) AddMem(string, int, int64)             {}
func (m *Manager) AddMemWith(string, int, Limits, int64) {}
func (m *Manager) Usage(string) (Usage, bool)            { return Usage{}, false }
func (m *Manager) TileUsage(string) (Usage, bool)        { return Usage{}, false }
func (m *Manager) AtLimit(string) (int64, int64, bool)   { return 0, 0, false }
func (m *Manager) Procs(string) ([]int, bool)            { return nil, false }
func (m *Manager) ProcsTree(string) ([]int, bool)        { return nil, false }
func (m *Manager) TileProcs(string) ([]int, bool)        { return nil, false }
func (m *Manager) TileLeaves(string) []Leaf              { return nil }
func (m *Manager) Remove(string)                         {}
