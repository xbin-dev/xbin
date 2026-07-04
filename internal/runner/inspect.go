package runner

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/magik6k/buxon/internal/cgroup"
	"github.com/magik6k/buxon/internal/sandbox/relay"
	"github.com/magik6k/buxon/internal/util"
)

// nsKinds are the namespaces we report per backend.
var nsKinds = []string{"net", "mnt", "pid", "user", "ipc", "uts", "cgroup"}

// NS is one namespace's identity + whether it differs from buxond's (isolated).
type NS struct {
	ID       string `json:"id"`
	Isolated bool   `json:"isolated"`
}

// Backend is the full runtime picture of one component's backend.
type Backend struct {
	Path        string        `json:"path"`
	Runtime     string        `json:"runtime"`
	State       string        `json:"state"`
	Isolated    bool          `json:"isolated"`
	PID         int           `json:"pid,omitempty"`
	Gen         int           `json:"gen"`
	Restarts    int           `json:"restarts"`
	ActiveConns int           `json:"activeConns"`
	UptimeSec   int64         `json:"uptimeSec,omitempty"`
	LastReqSec  int64         `json:"lastReqSec"` // seconds since last request (-1 = never)
	Error       string        `json:"error,omitempty"`
	RSSKB       int64         `json:"rssKb,omitempty"`
	Threads     int           `json:"threads,omitempty"`
	FDs         int           `json:"fds,omitempty"`
	CPUSec      float64       `json:"cpuSec,omitempty"`
	Namespaces  map[string]NS `json:"namespaces,omitempty"`
	Egress      []string      `json:"egress,omitempty"`
	Activity    *relay.Stats  `json:"activity,omitempty"`
	Cgroup      *cgroup.Usage `json:"cgroup,omitempty"`
}

// Inspect returns the runtime picture of every known component backend.
func (r *Runner) Inspect() []Backend {
	self := selfNS()

	r.mu.Lock()
	states := make([]*state, 0, len(r.states))
	for _, s := range r.states {
		states = append(states, s)
	}
	r.mu.Unlock()

	out := make([]Backend, 0, len(states))
	for _, s := range states {
		s.mu.Lock()
		b := Backend{
			Path: s.comp, State: stateName(s), Gen: s.gen, Restarts: len(s.crashes),
			ActiveConns: s.active,
		}
		if c, ok := r.Reg.Component(s.comp); ok {
			b.Runtime = c.Manifest.Runtime
			b.Isolated = r.Isolate && sandboxable(c.Manifest.Runtime)
		}
		if s.lastErr != nil {
			b.Error = s.lastErr.Error()
		}
		if !s.lastReq.IsZero() {
			b.LastReqSec = int64(time.Since(s.lastReq).Seconds())
		} else {
			b.LastReqSec = -1
		}
		inst := s.cur
		s.mu.Unlock()

		if inst != nil {
			b.UptimeSec = int64(time.Since(inst.started).Seconds())
			b.Egress = inst.egress
			if inst.relay != nil {
				st := inst.relay.Stats()
				b.Activity = &st
			}
			if inst.cmd != nil && inst.cmd.Process != nil {
				b.PID = inst.cmd.Process.Pid
				fillProc(&b, self)
			}
			if r.Cgroup != nil {
				if u, ok := r.Cgroup.Usage(util.CompKey(b.Path)); ok {
					b.Cgroup = &u
				}
			}
		}
		out = append(out, b)
	}
	// Stable order (r.states is a map) so the admin runtime view doesn't reshuffle
	// between polls.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func stateName(s *state) string {
	switch {
	case s.building:
		return "building"
	case s.cur != nil:
		return "healthy"
	case s.lastErr != nil:
		return "failed"
	}
	return "idle"
}

// fillProc reads per-process stats from /proc (best-effort; the process may exit
// under us). self is buxond's own namespace ids for the isolation comparison.
func fillProc(b *Backend, self map[string]string) {
	proc := filepath.Join("/proc", strconv.Itoa(b.PID))

	// namespaces
	ns := map[string]NS{}
	for _, k := range nsKinds {
		if link, err := os.Readlink(filepath.Join(proc, "ns", k)); err == nil {
			ns[k] = NS{ID: link, Isolated: self[k] != "" && link != self[k]}
		}
	}
	if len(ns) > 0 {
		b.Namespaces = ns
	}

	// memory + threads from /proc/<pid>/status
	if data, err := os.ReadFile(filepath.Join(proc, "status")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "VmRSS:"):
				b.RSSKB = firstInt(line)
			case strings.HasPrefix(line, "Threads:"):
				b.Threads = int(firstInt(line))
			}
		}
	}

	// cpu from /proc/<pid>/stat (utime+stime jiffies → seconds)
	if data, err := os.ReadFile(filepath.Join(proc, "stat")); err == nil {
		if f := statFields(string(data)); len(f) > 15 {
			utime, _ := strconv.ParseInt(f[13], 10, 64)
			stime, _ := strconv.ParseInt(f[14], 10, 64)
			b.CPUSec = float64(utime+stime) / 100.0 // USER_HZ = 100
		}
	}

	// open fds
	if ents, err := os.ReadDir(filepath.Join(proc, "fd")); err == nil {
		b.FDs = len(ents)
	}
}

func selfNS() map[string]string {
	m := map[string]string{}
	for _, k := range nsKinds {
		if link, err := os.Readlink(filepath.Join("/proc/self/ns", k)); err == nil {
			m[k] = link
		}
	}
	return m
}

func firstInt(line string) int64 {
	for _, f := range strings.Fields(line) {
		if n, err := strconv.ParseInt(f, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// statFields splits /proc/<pid>/stat, treating the parenthesized comm (which
// may contain spaces) as one field so later indices line up.
func statFields(s string) []string {
	open := strings.IndexByte(s, '(')
	close := strings.LastIndexByte(s, ')')
	if open < 0 || close < 0 || close < open {
		return strings.Fields(s)
	}
	out := []string{s[:open-1], s[open+1 : close]}
	return append(out, strings.Fields(s[close+1:])...)
}
