package tilesbx

// info.go — what the runtime answers about sandboxes: SandboxInfo, the
// runtime a manager may use (GET /sandboxes/runtime), and the admin's rows.

// Info is one sandbox as the runtime answers it (SandboxInfo).
type Info struct {
	Name          string            `json:"name"`
	UID           string            `json:"uid"` // the sandbox's identity: a re-created name gets another
	State         string            `json:"state"`
	StateDetail   string            `json:"stateDetail"`
	Mode          string            `json:"mode"`
	Accel         string            `json:"accel,omitempty"`
	MemMiB        int               `json:"memMiB"`
	VCPUs         int               `json:"vcpus"`
	DiskGiB       int               `json:"diskGiB"`
	Net           NetInfo           `json:"net"`
	Mounts        []Mount           `json:"mounts"`
	Defaults      Defaults          `json:"defaults"`
	Labels        map[string]string `json:"labels"`
	For           string            `json:"for,omitempty"`
	ForUser       string            `json:"forUser,omitempty"`
	IdleStopMin   int               `json:"idleStopMin"`
	AutoStart     bool              `json:"autoStart"`
	Base          BaseInfo          `json:"base"`
	Users         string            `json:"users"`
	DiskBytes     int64             `json:"diskBytes"`
	Snapshots     int               `json:"snapshots"`
	ExecsRunning  int               `json:"execsRunning"`
	Created       int64             `json:"created"`
	Started       int64             `json:"started,omitempty"`
	LastActive    int64             `json:"lastActive,omitempty"`
	Version       int64             `json:"version"`
	ClientID      string            `json:"clientId,omitempty"`
	RestartNeeded bool              `json:"restartNeeded"`
}

// NetInfo is a sandbox's egress: the class it has, what that reaches (the
// running relay's reach; the class's now when stopped), and a class a
// PATCH set that waits for the next start.
type NetInfo struct {
	Egress     string `json:"egress"`
	Reach      string `json:"reach"`
	EgressNext string `json:"egressNext"`
	Note       string `json:"note"`
}

// BaseInfo is the base image a sandbox's state is pinned to.
type BaseInfo struct {
	Version  string `json:"version"`
	Outdated bool   `json:"outdated"`
}

// live reports a state in which processes exist.
func live(state string) bool {
	return state == StateStarting || state == StateRunning || state == StateStopping
}

// clamp is a definition's sizes under the tile's caps now: a lowered cap
// applies at the next start.
func clamp(d *Def, lim Limits) (mem, vcpus, disk int) {
	ps := lim.PerSandbox
	return min(d.MemMiB, ps.MaxMemMiB), min(d.VCPUs, ps.MaxVCPUs), max(min(d.DiskGiB, ps.MaxDiskGiB), 1)
}

// reachOf is what an egress class reaches now, and why when it's inert.
func (m *Manager) reachOf(k Key, egress string) (reach, note string) {
	if egress == "none" || egress == "" {
		return "none", ""
	}
	for _, c := range m.classes(k) {
		if c.Class == egress {
			return c.Reach, c.Note
		}
	}
	return "none", "the tile no longer declares this sandbox-net slot"
}

// info builds a definition's SandboxInfo. Callers hold m.mu.
func (m *Manager) info(k Key, d *Def) Info {
	b := m.boxOf(k, d.Name)
	lim := m.limitsFor(k.Tile)
	mem, vcpus, disk := clamp(d, lim)
	in := Info{
		Name: d.Name, UID: d.UID, State: b.state, StateDetail: b.detail, Mode: d.Mode,
		MemMiB: mem, VCPUs: vcpus, DiskGiB: disk,
		Net:      NetInfo{Egress: d.Net.Egress},
		Mounts:   d.Mounts,
		Defaults: d.Defaults, Labels: d.Labels, For: d.For, ForUser: d.ForUser,
		IdleStopMin: d.IdleStopMin, AutoStart: d.AutoStart,
		Base:  BaseInfo{Version: d.Base},
		Users: m.users(d.Mode), DiskBytes: b.diskBytes, Snapshots: b.snapshots, ExecsRunning: b.execs.runningCount(),
		Created: d.Created, LastActive: b.lastUsed(), Version: d.Version, ClientID: d.ClientID,
	}
	if in.IdleStopMin == 0 {
		in.IdleStopMin = lim.IdleStopMin
	}
	if in.Mounts == nil {
		in.Mounts = []Mount{}
	}
	if in.Labels == nil {
		in.Labels = map[string]string{}
	}
	if live(b.state) && b.launched != nil {
		ld := b.launched
		in.Accel, in.Started = b.accel, b.started
		in.MemMiB, in.VCPUs, in.DiskGiB = ld.MemMiB, ld.VCPUs, ld.DiskGiB
		in.Net.Egress, in.Net.Reach = ld.Net.Egress, b.reach
		if d.Net.Egress != ld.Net.Egress {
			in.Net.EgressNext = d.Net.Egress
		} else if b.egressNext { // its class's rules widened: they apply at the next start
			in.Net.EgressNext = d.Net.Egress
		}
		in.RestartNeeded = mem != ld.MemMiB || vcpus != ld.VCPUs || disk != ld.DiskGiB ||
			d.Net.Egress != ld.Net.Egress || b.egressNext || !sameMounts(d.Mounts, ld.Mounts)
		return in
	}
	in.Net.Reach, in.Net.Note = m.reachOf(k, d.Net.Egress)
	return in
}

func sameMounts(a, b []Mount) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Runtime is what a manager tile may use now (GET /sandboxes/runtime).
type Runtime struct {
	Enabled     bool          `json:"enabled"`
	Isolation   bool          `json:"isolation"`
	Modes       []ModeInfo    `json:"modes"`
	Unavailable []ModeInfo    `json:"unavailable"`
	Users       string        `json:"users"` // a namespace sandbox's; VM mode is always "any"
	Egress      []EgressClass `json:"egress"`
	Caps        []string      `json:"caps"`
	Limits      RuntimeLimits `json:"limits"`
	Used        Used          `json:"used"`
}

// ModeInfo is a mode that can run (with a VM's acceleration), or why not.
type ModeInfo struct {
	Mode   string `json:"mode"`
	Accel  string `json:"accel,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// RuntimeLimits are the tile's quotas and the runtime's fixed bounds; a
// request above one is clamped to it, a body over one is too-large.
type RuntimeLimits struct {
	Sandboxes       int        `json:"sandboxes"`
	Running         int        `json:"running"`
	MemMiB          int        `json:"memMiB"`
	VCPUs           int        `json:"vcpus"`
	DiskGiB         int        `json:"diskGiB"`
	PerSandbox      PerSandbox `json:"perSandbox"`
	IdleStopMin     int        `json:"idleStopMin"`
	RunTimeoutMaxMs int        `json:"runTimeoutMaxMs"`
	RunOutputMax    int        `json:"runOutputMax"`
	ExecsRunning    int        `json:"execsRunning"`
	OutputRing      int        `json:"outputRing"`
	StdinMax        int        `json:"stdinMax"`
	FileMax         int        `json:"fileMax"`
	TarMax          int        `json:"tarMax"`
	WaitMaxSec      int        `json:"waitMaxSec"`
	Flows           Flows      `json:"flows"`
}

// Flows caps one sandbox's concurrent relay flows (§4): past them a new
// connection is reset at once. Fixed until the relay's caps are
// configurable; the workspace-wide cap is the admin's.
type Flows struct {
	TCP int `json:"tcp"`
	UDP int `json:"udp"`
}

// Used is what the tile holds now.
type Used struct {
	Sandboxes int   `json:"sandboxes"`
	Running   int   `json:"running"`
	MemMiB    int   `json:"memMiB"`
	VCPUs     int   `json:"vcpus"`
	DiskBytes int64 `json:"diskBytes"`
}

// builtCaps are the contract capabilities this runtime serves. The file,
// tar, snapshot and clone routes answer unsupported until they are built.
var builtCaps = []string{"exec", "tty"}

// runtime builds a tile's Runtime. Callers hold m.mu.
func (m *Manager) runtime(k Key) Runtime {
	p := m.policy.get()
	lim := p.For(k.Tile)
	rt := Runtime{
		Enabled: p.On(), Isolation: m.isolated, Users: m.users(ModeNamespace),
		Modes: []ModeInfo{}, Unavailable: []ModeInfo{},
		Egress: append([]EgressClass{{Class: "none", Reach: "none"}}, m.classes(k)...),
		Caps:   append([]string{}, builtCaps...),
		Limits: RuntimeLimits{
			Sandboxes: lim.PerTile.Max, Running: lim.PerTile.Running, MemMiB: lim.PerTile.MemMiB,
			VCPUs: lim.PerTile.VCPUs, DiskGiB: lim.PerTile.DiskGiB, PerSandbox: lim.PerSandbox,
			IdleStopMin: lim.IdleStopMin, RunTimeoutMaxMs: runTimeoutMaxMs, RunOutputMax: runOutputMax,
			ExecsRunning: execsRunningMax, OutputRing: lim.OutputRingMiB << 20, StdinMax: stdinMax,
			FileMax: fileMax, TarMax: tarMax, WaitMaxSec: waitMaxSec,
			Flows: Flows{TCP: flowsTCP, UDP: flowsUDP},
		},
	}
	if m.isolated {
		rt.Modes = append(rt.Modes, ModeInfo{Mode: ModeNamespace})
	} else {
		rt.Unavailable = append(rt.Unavailable, ModeInfo{Mode: ModeNamespace, Reason: "tile sandboxes need isolation (xbind --isolate)"})
	}
	if accel, reason := m.vmMode(); reason == "" {
		rt.Modes = append(rt.Modes, ModeInfo{Mode: ModeVM, Accel: accel})
	} else {
		rt.Unavailable = append(rt.Unavailable, ModeInfo{Mode: ModeVM, Reason: reason})
	}
	rt.Used.Sandboxes = m.defs.count(k)
	for _, b := range m.live[k] {
		if live(b.state) && b.launched != nil {
			rt.Used.Running++
			rt.Used.MemMiB += b.launched.MemMiB
			rt.Used.VCPUs += b.launched.VCPUs
		}
		rt.Used.DiskBytes += b.diskBytes
	}
	return rt
}

// AdminRow is one tile sandbox in the admin's GET /sandboxes
// (tileSandboxes): stopped ones and those of removed tiles too, so they
// can be cleaned up.
type AdminRow struct {
	Tile       string `json:"tile"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Mode       string `json:"mode"`
	Accel      string `json:"accel,omitempty"`
	MemMiB     int    `json:"memMiB"`
	VCPUs      int    `json:"vcpus"`
	DiskGiB    int    `json:"diskGiB"`
	DiskBytes  int64  `json:"diskBytes"`
	For        string `json:"for,omitempty"`
	ForUser    string `json:"forUser,omitempty"`
	LastActive int64  `json:"lastActive,omitempty"`
	TileExists bool   `json:"tileExists"`
}

// AdminList is every tile sandbox definition (one tile's, when tile isn't "").
func (m *Manager) AdminList(tile string) []AdminRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AdminRow{}
	for _, t := range m.defs.tiles() {
		if tile != "" && t != tile {
			continue
		}
		k := Key{Tile: t}
		exists := m.deps.Tiles != nil && m.deps.Tiles.Exists(t)
		for _, d := range m.defs.list(k) {
			in := m.info(k, d)
			out = append(out, AdminRow{Tile: t, Name: d.Name, State: in.State, Mode: d.Mode, Accel: in.Accel,
				MemMiB: in.MemMiB, VCPUs: in.VCPUs, DiskGiB: in.DiskGiB, DiskBytes: in.DiskBytes,
				For: d.For, ForUser: d.ForUser, LastActive: in.LastActive, TileExists: exists})
		}
	}
	return out
}
