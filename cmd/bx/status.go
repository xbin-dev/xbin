package main

// status.go — `bx status` (a tile's runtime metrics, or the admin's global
// view) and `bx logs` (a backend's log).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// cmdStatus prints one tile's runtime metrics (default: the terminal's own
// tile, via $XBIN_COMPONENT). `bx status <component>` targets another (admin,
// or self). `bx status --all` is the admin global view. Read-only.
func cmdStatus(args []string) error {
	all := false
	comp := os.Getenv("XBIN_COMPONENT")
	for _, a := range args {
		if a == "--all" {
			all = true
		} else if a != "" {
			comp = a
		}
	}
	// No tile context (a host shell, not a tile terminal) or --all → the admin
	// global view. In a tile terminal, $XBIN_COMPONENT scopes it to that tile.
	if all || comp == "" {
		var backends, status map[string]any
		if err := apiJSON("GET", "/api/xbin/backends", nil, &backends); err != nil {
			return err
		}
		_ = apiJSON("GET", "/api/xbin/status", nil, &status)
		b, _ := json.MarshalIndent(map[string]any{"backends": backends, "status": status}, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	var st tileStatus
	if err := apiJSON("GET", "/api/xbin/tile-status?component="+url.QueryEscape(comp), nil, &st); err != nil {
		return err
	}
	printTileStatus(&st)
	return nil
}

type tileStatus struct {
	Component string `json:"component"`
	Backend   *struct {
		State       string   `json:"state"`
		Gen         int      `json:"gen"`
		Restarts    int      `json:"restarts"`
		ActiveConns int      `json:"activeConns"`
		UptimeSec   int64    `json:"uptimeSec"`
		RSSKB       int64    `json:"rssKb"`
		FDs         int      `json:"fds"`
		Threads     int      `json:"threads"`
		CPUSec      float64  `json:"cpuSec"`
		Error       string   `json:"error"`
		Egress      []string `json:"egress"`
		Cgroup      *struct {
			MemCurrent  int64 `json:"memCurrent"`
			MemMax      int64 `json:"memMax"`
			PidsCurrent int64 `json:"pidsCurrent"`
		} `json:"cgroup"`
	} `json:"backend"`
	Net struct { // the effective network (D54)
		Ref    string   `json:"netRef"`
		Mode   string   `json:"net"`
		Rules  []string `json:"netRules"`
		Source string   `json:"netSource"`
		Note   string   `json:"netNote"`
	} `json:"net"`
	Disk struct {
		UsageBytes int64 `json:"usageBytes"`
		QuotaBytes int64 `json:"quotaBytes"`
		Blocked    bool  `json:"blocked"`
	} `json:"disk"`
	Alerts []struct {
		Level   string `json:"level"`
		Message string `json:"message"`
	} `json:"alerts"`
}

func printTileStatus(s *tileStatus) {
	fmt.Println(s.Component)
	if b := s.Backend; b != nil {
		up := ""
		if b.UptimeSec > 0 {
			up = " · up " + (time.Duration(b.UptimeSec) * time.Second).String()
		}
		rs := ""
		if b.Restarts > 0 {
			rs = fmt.Sprintf(" · %d restart(s)", b.Restarts)
		}
		fmt.Printf("  backend    %s · gen %d%s%s\n", b.State, b.Gen, up, rs)
		if b.Error != "" {
			fmt.Printf("  error      %s\n", b.Error)
		}
		mem := ""
		if b.Cgroup != nil {
			mem = fmt.Sprintf("   mem %s", humanBytesBx(b.Cgroup.MemCurrent))
			if b.Cgroup.MemMax > 0 {
				mem += " / " + humanBytesBx(b.Cgroup.MemMax)
			}
			mem += fmt.Sprintf("   pids %d", b.Cgroup.PidsCurrent)
		}
		fmt.Printf("  cpu        %.1fs%s   fds %d   conns %d\n", b.CPUSec, mem, b.FDs, b.ActiveConns)
		if len(b.Egress) > 0 {
			fmt.Printf("  egress     %s\n", strings.Join(b.Egress, ", "))
		}
	} else {
		fmt.Println("  backend    not running")
	}
	if n := s.Net; n.Ref != "" || n.Mode != "" {
		line := n.Ref
		if line == "" {
			line = "unbound"
		}
		if n.Source != "" {
			line += " → " + n.Source
		}
		if n.Mode != "" {
			line += " (" + n.Mode + ")"
		}
		fmt.Printf("  net        %s\n", line)
		if n.Note != "" {
			fmt.Printf("  ⚠ net      %s\n", n.Note)
		}
	}
	dq := ""
	if s.Disk.QuotaBytes > 0 {
		dq = " / " + humanBytesBx(s.Disk.QuotaBytes)
	}
	blk := ""
	if s.Disk.Blocked {
		blk = "  ⛔ writes blocked"
	}
	fmt.Printf("  disk       %s%s%s\n", humanBytesBx(s.Disk.UsageBytes), dq, blk)
	for _, a := range s.Alerts {
		icon := "⚡"
		if a.Level == "crit" {
			icon = "⚠"
		}
		fmt.Printf("  alert      %s %s\n", icon, a.Message)
	}
}

func humanBytesBx(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func cmdLogs(args []string) error {
	follow := false
	var comp string
	for _, a := range args {
		if a == "-f" {
			follow = true
		} else {
			comp = a
		}
	}
	if comp == "" {
		return fmt.Errorf("usage: bx logs [-f] <component>")
	}
	ws := workspaceRoot()
	if ws == "" {
		return fmt.Errorf("not inside a xbin workspace")
	}
	if fi, err := os.Stat(filepath.Join(ws, ".xbin", "log")); err != nil || !fi.IsDir() {
		// .xbin is masked in an isolated terminal (internal/term/binds.go),
		// so the file can't answer: xbind serves the same log (16-open-questions Q23).
		return logsFromAPI(comp, follow)
	}
	path := filepath.Join(ws, ".xbin", "log", util.CompKey(comp)+".log")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no logs for %s yet (%s)", comp, path)
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi != nil && fi.Size() > 64<<10 && follow {
		_, _ = f.Seek(-64<<10, io.SeekEnd)
	}
	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}
	for follow {
		time.Sleep(300 * time.Millisecond)
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return err
		}
	}
	return nil
}

// logsFromAPI streams a backend's log from GET /logs, what `bx logs` does
// where it can't read .xbin/log itself: the whole log up to the route's
// 1 MiB tail, or with -f the last 64 KiB and then everything appended.
func logsFromAPI(comp string, follow bool) error {
	q := url.Values{"component": {comp}}
	if follow {
		q.Set("follow", "1")
	} else {
		q.Set("tail", strconv.Itoa(1<<20))
	}
	base, client := transport()
	c := *client
	if follow {
		c.Timeout = 0 // a follow streams until it is interrupted
	}
	req, err := http.NewRequest("GET", base+"/api/xbin/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if tok := ownerToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s (%s)", e.Error, resp.Status)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	_, err = io.Copy(os.Stdout, resp.Body)
	return err
}
