package server

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// hostStats gathers cheap host metrics for the shell's status footer: CPU
// jiffies (cumulative — the client deltas two polls), memory, and the disk
// the workspace lives on. Best-effort: missing fields are simply absent.
func hostStats(workspaceRoot string) map[string]any {
	out := map[string]any{}

	// CPU: first line of /proc/stat — "cpu user nice system idle iowait …".
	if b, err := os.ReadFile("/proc/stat"); err == nil {
		if line, _, ok := strings.Cut(string(b), "\n"); ok && strings.HasPrefix(line, "cpu ") {
			var total, idle uint64
			for i, f := range strings.Fields(line)[1:] {
				v, _ := strconv.ParseUint(f, 10, 64)
				total += v
				if i == 3 || i == 4 { // idle + iowait
					idle += v
				}
			}
			out["cpuBusy"], out["cpuTotal"] = total-idle, total
		}
	}

	// Memory: MemTotal / MemAvailable from /proc/meminfo (kB).
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			var key string
			switch {
			case strings.HasPrefix(line, "MemTotal:"):
				key = "memTotal"
			case strings.HasPrefix(line, "MemAvailable:"):
				key = "memAvail"
			default:
				continue
			}
			f := strings.Fields(line)
			if len(f) >= 2 {
				v, _ := strconv.ParseUint(f[1], 10, 64)
				out[key] = v * 1024
			}
		}
	}

	// Disk: the filesystem the workspace lives on.
	var st unix.Statfs_t
	if err := unix.Statfs(workspaceRoot, &st); err == nil {
		bs := uint64(st.Bsize)
		out["diskTotal"] = st.Blocks * bs
		out["diskFree"] = st.Bavail * bs
	}

	return out
}
