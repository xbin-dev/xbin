//go:build linux

package guest

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dumpBudget bounds the time a dump takes: reading the kernel stack of a
// process caught inside execve can block, and the host waits only so long.
const dumpBudget = 2 * time.Second

// dumpMax bounds its size (it lands in the backend's log).
const dumpMax = 96 << 10

// pfKthread marks a kernel thread in /proc/<pid>/stat's flags.
const pfKthread = 0x00200000

// dump describes what the guest is doing, for the host's log when it gives
// up on the guest (a backend that never listened, host/dump_linux.go): the
// sessions, every process and thread with its kernel wait channel and stack,
// and the agent's own goroutines. A read that blocks past budget leaves the
// dump partial, saying where it stopped.
func (a *agent) dump(budget time.Duration) string {
	var mu sync.Mutex
	var b strings.Builder
	step := "the start"
	w := func(format string, args ...any) {
		mu.Lock()
		if b.Len() < dumpMax {
			fmt.Fprintf(&b, format, args...)
		}
		mu.Unlock()
	}
	at := func(s string) { mu.Lock(); step = s; mu.Unlock() }
	done := make(chan struct{})
	go func() {
		defer close(done)
		at("/proc")
		up, _ := os.ReadFile("/proc/uptime")
		load, _ := os.ReadFile("/proc/loadavg")
		w("guest: up %ss, load %s, %s\n", firstField(string(up)), strings.TrimSpace(string(load)), memLine())
		a.dumpSessions(w, at)
		a.dumpProcs(w, at)
		at("the agent's goroutines")
		buf := make([]byte, 64<<10)
		n := runtime.Stack(buf, true) // stops the world: last
		w("agent goroutines:\n%s\n", buf[:n])
	}()
	select {
	case <-done:
	case <-time.After(budget):
	}
	mu.Lock()
	defer mu.Unlock()
	out := b.String()
	select {
	case <-done:
	default:
		out += fmt.Sprintf("(dump incomplete: stuck reading %s)\n", step)
	}
	if len(out) > dumpMax {
		out = out[:dumpMax] + "\n(dump truncated)\n"
	}
	return out
}

func (a *agent) dumpSessions(w func(string, ...any), at func(string)) {
	a.mu.Lock()
	ids := make([]int, 0, len(a.sessions))
	for id := range a.sessions {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	sort.Ints(ids)
	for _, id := range ids {
		s := a.session(id)
		if s == nil {
			continue
		}
		s.mu.Lock()
		ex, proc, attached := s.ex, s.proc, len(s.streams)
		s.mu.Unlock()
		state := "not started"
		if proc != nil {
			state = "pid " + strconv.Itoa(proc.Pid)
		} else if ex.Session != 0 {
			state = fmt.Sprintf("not started (%d/%d streams attached)", attached, len(streamsOf(ex)))
		}
		w("session %d: %s, argv %q\n", id, state, ex.Argv)
		if ex.Listen != "" {
			at("session " + strconv.Itoa(id) + "'s socket")
			if c, err := net.DialTimeout("unix", ex.Listen, 200*time.Millisecond); err == nil {
				c.Close()
				w("  %s: accepting\n", ex.Listen)
			} else {
				w("  %s: not accepting (%v)\n", ex.Listen, err)
			}
		}
	}
	if a.relay != nil {
		w("FUSE relay: pid %d\n", a.relay.pid)
	}
}

// dumpProcs lists every user process with its threads: state, wait channel
// and kernel stack, threads that look alike folded together.
func (a *agent) dumpProcs(w func(string, ...any), at func(string)) {
	ents, _ := os.ReadDir("/proc")
	var pids []int
	for _, e := range ents {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	role := map[int]string{1: " (the agent)"}
	if a.relay != nil {
		role[a.relay.pid] = " (FUSE relay)"
	}
	a.mu.Lock()
	for id, s := range a.sessions {
		s.mu.Lock()
		if s.proc != nil {
			role[s.proc.Pid] = fmt.Sprintf(" (session %d)", id)
		}
		s.mu.Unlock()
	}
	a.mu.Unlock()
	w("processes:\n")
	for _, pid := range pids {
		dir := "/proc/" + strconv.Itoa(pid)
		at(dir + "/stat")
		st, ok := readStat(dir + "/stat")
		if !ok || st.flags&pfKthread != 0 {
			continue
		}
		w("  %d %s ppid %d state %s%s\n", pid, st.comm, st.ppid, st.state, role[pid])
		tids, _ := os.ReadDir(dir + "/task")
		type group struct {
			tids []string
			key  string
		}
		var groups []*group
		byKey := map[string]*group{}
		for _, t := range tids {
			td := filepath.Join(dir, "task", t.Name())
			at(td)
			ts, _ := readStat(td + "/stat")
			wchan, _ := os.ReadFile(td + "/wchan")
			stack, _ := os.ReadFile(td + "/stack")
			key := fmt.Sprintf("state %s wchan %s\n%s", ts.state, orDash(string(wchan)), indent(string(stack), "        "))
			g := byKey[key]
			if g == nil {
				g = &group{key: key}
				byKey[key] = g
				groups = append(groups, g)
			}
			g.tids = append(g.tids, t.Name())
		}
		for _, g := range groups {
			w("    threads %s: %s", strings.Join(g.tids, ","), g.key)
		}
	}
}

type procStat struct {
	comm, state string
	ppid        int
	flags       uint64
}

// readStat parses /proc/<pid>/stat (comm may hold spaces and parentheses).
func readStat(p string) (procStat, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return procStat{}, false
	}
	s := string(b)
	i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if i < 0 || j < i {
		return procStat{}, false
	}
	f := strings.Fields(s[j+1:])
	if len(f) < 7 {
		return procStat{}, false
	}
	st := procStat{comm: s[i : j+1], state: f[0]}
	st.ppid, _ = strconv.Atoi(f[1])
	st.flags, _ = strconv.ParseUint(f[6], 10, 64)
	return st, true
}

func memLine() string {
	b, _ := os.ReadFile("/proc/meminfo")
	var total, avail string
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "MemTotal:"); ok {
			total = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(l, "MemAvailable:"); ok {
			avail = strings.TrimSpace(v)
		}
	}
	return "memory " + orDash(avail) + " available of " + orDash(total)
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return "?"
}

func orDash(s string) string {
	if s = strings.TrimSpace(s); s == "" || s == "0" {
		return "-"
	}
	return s
}

func indent(s, pre string) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	return pre + strings.ReplaceAll(s, "\n", "\n"+pre) + "\n"
}
