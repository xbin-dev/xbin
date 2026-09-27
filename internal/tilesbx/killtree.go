package tilesbx

// killtree.go — ending a sandbox whose processes won't die with their
// PID 1. SIGKILL can't end a task waiting on a FUSE request its server has
// already taken, and the pid namespace is torn down only once PID 1 is
// gone: a fuse-overlayfs root that wedged (a stopped server, the root-dir
// create deadlock) keeps the agent — and so the whole sandbox — alive.
// Killing the server ends its connection and wakes every waiter. With a
// cgroup leaf, cgroup.kill reaches every process; without one, the
// sandbox's processes are PID 1's descendants (an orphan in a pid
// namespace is reparented to its PID 1), found in /proc.

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// killTree SIGKILLs every descendant of pid (not pid itself).
func killTree(pid int) {
	for _, p := range descendants(pid) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

// descendants lists pid's descendants from /proc (none off Linux).
func descendants(pid int) []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')') // the comm may hold spaces and parens
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 2 {
			continue
		}
		if pp, err := strconv.Atoi(f[1]); err == nil {
			children[pp] = append(children[pp], p)
		}
	}
	var out []int
	queue := []int{pid}
	seen := map[int]bool{pid: true}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		for _, c := range children[p] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				queue = append(queue, c)
			}
		}
	}
	return out
}
