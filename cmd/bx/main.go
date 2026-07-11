// bx — the xbin workspace CLI, available in every terminal session and inside
// component sandboxes. A thin client of xbind's API plus local scaffolding: it
// talks HTTP via XBIN_URL (terminals / the owner plane) or, inside a component,
// over the XBIN_GATEWAY unix socket (works with no net egress, RBAC-gated by
// XBIN_TOKEN). Builder reference: /docs/bx.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/magik6k/xbin/internal/util"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "ls":
		err = cmdLs()
	case "new":
		err = cmdNew(os.Args[2:])
	case "tile":
		err = cmdTile(os.Args[2:])
	case "template":
		err = cmdTemplate(os.Args[2:])
	case "builtin":
		err = cmdBuiltin(os.Args[2:])
	case "user":
		err = cmdUser(os.Args[2:])
	case "org":
		err = cmdOrg(os.Args[2:])
	case "team":
		err = cmdTeam(os.Args[2:])
	case "access":
		err = cmdAccess(os.Args[2:])
	case "logs":
		err = cmdLogs(os.Args[2:])
	case "doctor":
		err = cmdDoctor()
	case "api":
		err = cmdAPI(os.Args[2:])
	case "grant":
		err = cmdGrant(os.Args[2:])
	case "grants":
		err = cmdGrants()
	case "bind":
		err = cmdBind(os.Args[2:])
	case "iface", "ifaces":
		err = cmdIface()
	case "vault":
		err = cmdVault(os.Args[2:])
	case "cron":
		err = cmdCron(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "enable":
		err = cmdLifecycle(os.Args[2:], "enabled")
	case "disable":
		err = cmdLifecycle(os.Args[2:], "disabled")
	case "offload":
		err = cmdOffload(os.Args[2:])
	case "backup":
		err = cmdBackup(os.Args[2:])
	case "backups":
		err = cmdBackups(os.Args[2:])
	case "restore":
		err = cmdRestore(os.Args[2:])
	case "backup-schedule":
		err = cmdBackupSchedule(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bx:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bx — xbin workspace CLI (docs: /docs/bx.md)

  bx ls                                 list components
  bx status [<component>] [--all]        one tile's runtime metrics (defaults
                                        to this terminal's tile); --all = global
  bx new <path> [--runtime go|node|python|cgi] [--expose]
                                        scaffold a component
  bx tile ls | import <name> [as <path>]
                                        list/install builtin tiles
  bx template ls | new <source> [as <path>]
                                        list/instantiate template components
  bx builtin updates | update <id> [--replace|--merge]
                                        update copied builtins (scaffold, tiles)
  bx logs [-f] <component>              show backend logs
  bx api <component>                    roles + API.md of a component
  bx grants                             grant table + pending requests
  bx grant <caller> <target>:<role>     approve/add a grant
  bx grant --revoke <caller> <target>:<role>
  bx org ls|add|set|rm <id> [flags]     organizations (docs/auth.md)
  bx org policy [<org>] [--set '<json>'] policy-ceiling rows (workspace/org)
  bx team ls|add|set|rm <org>/<team>    teams (union grants within their org)
  bx access <tile> [set|rm user:…|team:…] per-tile access entries
  bx iface                              interface requests, providers, bindings
  bx bind <component> <slot>=<provider> wire an interface to a provider
  bx bind <component> <slot>+=<p[#i]> | <slot>-=<p[#i]>
                                        add/remove on a multi slot (# = instance)
  bx bind --unset <component> <slot>
  bx enable|disable <component>         component lifecycle (plans/lifecycle.md)
  bx offload <component> [--full]       archive + free local bytes
  bx backup <component>                 back up now to the bound archiver
  bx backups <component>                list archived versions
  bx restore <component> [--version v] [--file path]
                                        restore a version, or one file to stdout
  bx backup-schedule [<component> --every 24h|--cron "expr" [--keep N] | --rm]
                                        list/set/remove scheduled backups
  bx vault status|unseal|seal|rekey     encryption-at-rest barrier
  bx vault ls|get|set|rm <component> [key] [value]
  bx cron ls                            scheduled jobs
  bx doctor                             check the workspace for problems
`)
	os.Exit(2)
}

// --- xbind API plumbing ---

func api(method, path string, body any) (*http.Response, error) {
	base, client := transport()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("XBIN_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return client.Do(req)
}

// transport picks how bx reaches xbind:
//   - XBIN_URL set (a terminal / the owner plane) → plain HTTP to that URL.
//   - else XBIN_GATEWAY set (running inside a component sandbox) → the gateway
//     unix socket. This is a component's *only* door to xbind and does not
//     depend on any net egress — an unbound `net` interface (no internet) still
//     leaves `bx api …` working, gated by the element's instance token (RBAC,
//     default-deny). This is the dedicated per-component API tap.
//   - else → the local default (host).
func transport() (string, *http.Client) {
	if base := os.Getenv("XBIN_URL"); base != "" {
		return base, &http.Client{Timeout: 30 * time.Second}
	}
	if gw := os.Getenv("XBIN_GATEWAY"); gw != "" {
		return "http://xbin", &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", gw)
				},
			},
		}
	}
	return "http://127.0.0.1:8642", &http.Client{Timeout: 30 * time.Second}
}

func apiJSON(method, path string, body, out any) error {
	resp, err := api(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// In a tile terminal XBIN_TOKEN is scoped to that tile
		// (plans/terminal-tokens.md) — say so on permission errors instead of
		// letting "admin only" read like a bug.
		hint := ""
		if c := os.Getenv("XBIN_COMPONENT"); resp.StatusCode == http.StatusForbidden && c != "" {
			hint = fmt.Sprintf(" — this terminal is scoped to %s; admin/cross-tile ops need the admin tile or bx from the host", c)
		}
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s (%s)%s", e.Error, resp.Status, hint)
		}
		return fmt.Errorf("%s: %s%s", resp.Status, strings.TrimSpace(string(b)), hint)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func workspaceRoot() string {
	if ws := os.Getenv("XBIN_WORKSPACE"); ws != "" {
		return ws
	}
	// Walk up from cwd looking for xbin.json.
	dir, _ := os.Getwd()
	for d := dir; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "xbin.json")); err == nil {
			if _, err := os.Stat(filepath.Join(d, ".xbin")); err == nil {
				return d
			}
		}
	}
	return ""
}

type compInfo struct {
	Path        string            `json:"path"`
	Scope       string            `json:"scope"`
	Runtime     string            `json:"runtime"`
	HasIndex    bool              `json:"hasIndex"`
	Roles       map[string]string `json:"roles"`
	Deps        []string          `json:"deps"`
	ManifestErr string            `json:"manifestError"`
}

func components() ([]compInfo, error) {
	var out []compInfo
	err := apiJSON("GET", "/api/xbin/components", nil, &out)
	return out, err
}

// --- commands ---

func cmdLs() error {
	comps, err := components()
	if err != nil {
		return err
	}
	for _, c := range comps {
		rt := c.Runtime
		if rt == "" {
			rt = "static"
		}
		flags := ""
		if len(c.Roles) > 0 {
			var rs []string
			for r := range c.Roles {
				rs = append(rs, r)
			}
			sort.Strings(rs)
			flags = " exposes:" + strings.Join(rs, ",")
		}
		if c.ManifestErr != "" {
			flags += " MANIFEST-ERROR"
		}
		fmt.Printf("%-40s %-8s%s\n", c.Path, rt, flags)
	}
	return nil
}

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

func cmdAPI(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx api <component>")
	}
	var out struct {
		Component compInfo `json:"component"`
		APIDoc    string   `json:"apiDoc"`
	}
	if err := apiJSON("GET", "/api/xbin/components/"+args[0], nil, &out); err != nil {
		return err
	}
	c := out.Component
	fmt.Printf("component: %s   runtime: %s   scope: %s\n", c.Path, orStatic(c.Runtime), orDash(c.Scope))
	if len(c.Roles) > 0 {
		fmt.Println("\nroles:")
		var rs []string
		for r := range c.Roles {
			rs = append(rs, r)
		}
		sort.Strings(rs)
		for _, r := range rs {
			fmt.Printf("  %-8s %s\n", r, c.Roles[r])
		}
	}
	if out.APIDoc != "" {
		fmt.Println("\n" + strings.TrimSpace(out.APIDoc))
	} else if len(c.Roles) > 0 {
		fmt.Println("\n(no API.md — the author should add one; see /docs/elements.md)")
	}
	return nil
}

// cmdIface lists interface requests, providers, and current bindings.
func cmdIface() error {
	var out struct {
		Bindings   map[string]map[string]any    `json:"bindings"` // ref string, or []refs (multi)
		Instances  map[string]map[string]string `json:"instances"`
		Components []struct {
			Component  string                    `json:"component"`
			Interfaces map[string]map[string]any `json:"interfaces"`
			Provides   map[string]map[string]any `json:"provides"`
		} `json:"components"`
	}
	if err := apiJSON("GET", "/api/xbin/bindings", nil, &out); err != nil {
		return err
	}
	fmt.Println("providers:")
	for _, c := range out.Components {
		for slot, def := range c.Provides {
			extra := ""
			if inst, _ := def["instances"].(bool); inst {
				ids := make([]string, 0, len(out.Instances[c.Component]))
				for id := range out.Instances[c.Component] {
					ids = append(ids, c.Component+"#"+id)
				}
				sort.Strings(ids)
				extra = " instances: " + strings.Join(ids, ", ")
				if len(ids) == 0 {
					extra = " (no instances registered yet)"
				}
			}
			fmt.Printf("  %-30s provides %s (%v)%s\n", c.Component, slot, def["kind"], extra)
		}
	}
	fmt.Println("requests → binding:")
	for _, c := range out.Components {
		for slot := range c.Interfaces {
			bound := bindingRefs(out.Bindings[c.Component][slot])
			display := strings.Join(bound, ", ")
			if display == "" {
				display = "(unbound — no capability)"
			}
			fmt.Printf("  %-30s %s → %s\n", c.Component, slot, display)
		}
	}
	return nil
}

// bindingRefs normalizes a binding value from the API — a plain ref string,
// or an array of refs for a multi slot — into a list.
func bindingRefs(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// cmdBind wires a component's interface slots to providers:
//
//	bx bind <component> <slot>=<provider> [<slot>=<provider> …]
//	bx bind --unset <component> <slot>
//
// cmdBackupSchedule manages scheduled backups (plans/lifecycle.md LC-5):
//
//	bx backup-schedule                                       list schedules
//	bx backup-schedule <comp> --every 24h [--keep N]         schedule (interval)
//	bx backup-schedule <comp> --cron "0 3 * * *" [--keep N]  schedule (cron)
//	bx backup-schedule <comp> --rm                           remove a schedule
func cmdBackupSchedule(args []string) error {
	if len(args) == 0 {
		var out struct {
			Schedules []struct {
				Component string `json:"component"`
				Schedule  string `json:"schedule"`
				Retention int    `json:"retention"`
			} `json:"schedules"`
		}
		if err := apiJSON("GET", "/api/xbin/backup-schedule", nil, &out); err != nil {
			return err
		}
		if len(out.Schedules) == 0 {
			fmt.Println("no backup schedules")
			return nil
		}
		for _, s := range out.Schedules {
			keep := "keep all"
			if s.Retention > 0 {
				keep = fmt.Sprintf("keep %d", s.Retention)
			}
			fmt.Printf("%-32s %-16s %s\n", s.Component, s.Schedule, keep)
		}
		return nil
	}
	var comp, schedule string
	keep := 0
	rm := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rm":
			rm = true
		case "--cron":
			i++
			if i < len(args) {
				schedule = args[i]
			}
		case "--every":
			i++
			if i < len(args) {
				schedule = "@every " + args[i]
			}
		case "--keep":
			i++
			if i < len(args) {
				fmt.Sscanf(args[i], "%d", &keep)
			}
		default:
			comp = args[i]
		}
	}
	if comp == "" {
		return fmt.Errorf("usage: bx backup-schedule <component> [--every 24h | --cron \"expr\"] [--keep N] | --rm")
	}
	if rm {
		if err := apiJSON("DELETE", "/api/xbin/backup-schedule?component="+comp, nil, nil); err != nil {
			return err
		}
		fmt.Printf("unscheduled %s\n", comp)
		return nil
	}
	if schedule == "" {
		return fmt.Errorf("need --every <dur> or --cron \"<expr>\"")
	}
	if err := apiJSON("POST", "/api/xbin/backup-schedule", map[string]any{"component": comp, "schedule": schedule, "retention": keep}, nil); err != nil {
		return err
	}
	fmt.Printf("scheduled %s (%s)\n", comp, schedule)
	return nil
}

// cmdOffload archives a component and frees local bytes (plans/lifecycle.md):
//
//	bx offload <component> [--full]   (--full also removes source + term-env)
func cmdOffload(args []string) error {
	full := false
	var comp string
	for _, a := range args {
		if a == "--full" {
			full = true
		} else {
			comp = a
		}
	}
	if comp == "" {
		return fmt.Errorf("usage: bx offload <component> [--full]")
	}
	state := "offloaded"
	if full {
		state = "offloaded-full"
	}
	if err := apiJSON("POST", "/api/xbin/lifecycle", map[string]string{"component": comp, "state": state}, nil); err != nil {
		return err
	}
	fmt.Printf("%s %s\n", comp, state)
	return nil
}

func cmdBackup(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bx backup <component>")
	}
	var out struct{ Version string }
	if err := apiJSON("POST", "/api/xbin/backup", map[string]string{"component": args[0]}, &out); err != nil {
		return err
	}
	fmt.Printf("backed up %s → version %s\n", args[0], out.Version)
	return nil
}

func cmdBackups(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bx backups <component>")
	}
	var out struct {
		Versions []struct {
			Version string `json:"version"`
			Time    string `json:"time"`
			Size    int64  `json:"size"`
		} `json:"versions"`
	}
	if err := apiJSON("GET", "/api/xbin/backups?component="+args[0], nil, &out); err != nil {
		return err
	}
	if len(out.Versions) == 0 {
		fmt.Println("no backups")
		return nil
	}
	for _, v := range out.Versions {
		fmt.Printf("%-32s %s\t%d bytes\n", v.Version, v.Time, v.Size)
	}
	return nil
}

// cmdRestore restores a whole version or a single file (--file):
//
//	bx restore <component> [--version v]
//	bx restore <component> --file <path> [--version v]   (streams the file to stdout)
func cmdRestore(args []string) error {
	var comp, version, file string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--version":
			i++
			if i < len(args) {
				version = args[i]
			}
		case "--file":
			i++
			if i < len(args) {
				file = args[i]
			}
		default:
			comp = args[i]
		}
	}
	if comp == "" {
		return fmt.Errorf("usage: bx restore <component> [--version v] [--file path]")
	}
	body := map[string]string{"component": comp, "version": version, "file": file}
	if file != "" {
		resp, err := api("POST", "/api/xbin/restore", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("%s", resp.Status)
		}
		_, err = io.Copy(os.Stdout, resp.Body)
		return err
	}
	if err := apiJSON("POST", "/api/xbin/restore", body, nil); err != nil {
		return err
	}
	fmt.Printf("restored %s\n", comp)
	return nil
}

// cmdLifecycle sets a component's lifecycle state (plans/lifecycle.md).
func cmdLifecycle(args []string, state string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bx %s <component>", map[string]string{"enabled": "enable", "disabled": "disable"}[state])
	}
	if err := apiJSON("POST", "/api/xbin/lifecycle", map[string]string{"component": args[0], "state": state}, nil); err != nil {
		return err
	}
	fmt.Printf("%s %s\n", args[0], state)
	return nil
}

func cmdBind(args []string) error {
	if len(args) >= 3 && args[0] == "--unset" {
		body := map[string]string{"component": args[1], "slot": args[2]}
		if err := apiJSON("DELETE", "/api/xbin/bindings", body, nil); err != nil {
			return err
		}
		fmt.Println("ok")
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: bx bind <component> <slot>=<provider> | <slot>+=<ref> | <slot>-=<ref> …  |  bx bind --unset <component> <slot>")
	}
	comp := args[0]
	for _, pair := range args[1:] {
		// <slot>=<ref> replaces; <slot>+=<ref> adds to and <slot>-=<ref> removes
		// from a multi slot's set. Refs are "<provider>[#<instance>]".
		var op byte
		slot, ref, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("expected <slot>=<provider>, got %q", pair)
		}
		if n := len(slot); n > 0 && (slot[n-1] == '+' || slot[n-1] == '-') {
			op, slot = slot[n-1], slot[:n-1]
		}
		if op == 0 {
			body := map[string]string{"component": comp, "slot": slot, "provider": ref}
			if err := apiJSON("POST", "/api/xbin/bindings", body, nil); err != nil {
				return err
			}
			continue
		}
		// Read-modify-write the slot's current set.
		var cur struct {
			Bindings map[string]map[string]any `json:"bindings"`
		}
		if err := apiJSON("GET", "/api/xbin/bindings", nil, &cur); err != nil {
			return err
		}
		set := bindingRefs(cur.Bindings[comp][slot])
		switch op {
		case '+':
			dup := false
			for _, r := range set {
				dup = dup || r == ref
			}
			if !dup {
				set = append(set, ref)
			}
		case '-':
			out := set[:0]
			for _, r := range set {
				if r != ref {
					out = append(out, r)
				}
			}
			set = out
		}
		method, body := "POST", map[string]any{"component": comp, "slot": slot, "providers": set}
		if len(set) == 0 {
			method, body = "DELETE", map[string]any{"component": comp, "slot": slot}
		}
		if err := apiJSON(method, "/api/xbin/bindings", body, nil); err != nil {
			return err
		}
	}
	fmt.Println("ok")
	return nil
}

func cmdGrants() error {
	var out struct {
		Grants  []map[string]string `json:"grants"`
		Pending []map[string]string `json:"pending"`
	}
	if err := apiJSON("GET", "/api/xbin/grants", nil, &out); err != nil {
		return err
	}
	fmt.Println("granted:")
	for _, g := range out.Grants {
		fmt.Printf("  %-30s → %s : %s\n", g["from"], g["target"], g["role"])
	}
	if len(out.Pending) > 0 {
		fmt.Println("pending (approve with bx grant <caller> <target>:<role>):")
		for _, g := range out.Pending {
			fmt.Printf("  %-30s → %s : %s\n", g["from"], g["target"], g["role"])
		}
	}
	return nil
}

func cmdGrant(args []string) error {
	revoke := false
	var rest []string
	for _, a := range args {
		if a == "--revoke" {
			revoke = true
		} else {
			rest = append(rest, a)
		}
	}
	// Split on the LAST colon: targets may be "res:apps/calendar/bus".
	i := -1
	if len(rest) == 2 {
		i = strings.LastIndex(rest[1], ":")
	}
	if i <= 0 || i == len(rest[1])-1 {
		return fmt.Errorf("usage: bx grant [--revoke] <caller> <target>:<role>")
	}
	body := map[string]string{"from": rest[0], "target": rest[1][:i], "role": rest[1][i+1:]}
	method := "POST"
	if revoke {
		method = "DELETE"
	}
	if err := apiJSON(method, "/api/xbin/grants", body, nil); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func cmdVault(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx vault status|unseal|seal|rekey | ls|get|set|rm <component> [key] [value]")
	}
	// Barrier lifecycle ops take no component.
	switch args[0] {
	case "status":
		var st struct {
			Mode string `json:"mode"`
		}
		if err := apiJSON("GET", "/api/xbin/vault-status", nil, &st); err != nil {
			return err
		}
		switch st.Mode {
		case "unsealed":
			fmt.Println("unsealed: encryption at rest active")
		case "sealed":
			fmt.Println("sealed: encrypted, locked. Run: bx vault unseal")
		case "plaintext":
			fmt.Println("plaintext: NO encryption at rest (dev/--insecure-vault). Run: bx vault unseal to encrypt.")
		default: // unconfigured
			fmt.Println("unconfigured: locked, no encryption set up. Run: bx vault unseal to create the barrier (secret storage is refused until then).")
		}
		return nil
	case "unseal":
		pass, err := readPassphrase("vault passphrase (creates the barrier on first use): ")
		if err != nil {
			return err
		}
		var out struct {
			Created bool `json:"created"`
		}
		if err := apiJSON("POST", "/api/xbin/vault-unseal", map[string]string{"passphrase": pass}, &out); err != nil {
			return err
		}
		if out.Created {
			fmt.Println("vault barrier created and unsealed — existing secrets encrypted. Keep this passphrase safe: it cannot be recovered.")
		} else {
			fmt.Println("vault unsealed")
		}
		return nil
	case "seal":
		if err := apiJSON("POST", "/api/xbin/vault-seal", map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Println("vault sealed")
		return nil
	case "rekey":
		cur, err := readPassphrase("current passphrase: ")
		if err != nil {
			return err
		}
		nw, err := readPassphrase("new passphrase: ")
		if err != nil {
			return err
		}
		if err := apiJSON("POST", "/api/xbin/vault-rekey", map[string]string{"current": cur, "new": nw}, nil); err != nil {
			return err
		}
		fmt.Println("passphrase changed (data key unchanged — no re-encryption needed)")
		return nil
	}

	if len(args) < 2 {
		return fmt.Errorf("usage: bx vault status|unseal|seal|rekey | ls|get|set|rm <component> [key] [value]")
	}
	op, comp := args[0], args[1]
	switch op {
	case "ls":
		var out struct {
			Keys []string `json:"keys"`
		}
		if err := apiJSON("GET", "/api/xbin/vault/"+comp, nil, &out); err != nil {
			return err
		}
		for _, k := range out.Keys {
			fmt.Println(k)
		}
	case "get":
		if len(args) < 3 {
			return fmt.Errorf("usage: bx vault get <component> <key>")
		}
		var out struct {
			Value string `json:"value"`
		}
		if err := apiJSON("GET", "/api/xbin/vault/"+comp+"/"+args[2], nil, &out); err != nil {
			return err
		}
		fmt.Println(out.Value)
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: bx vault set <component> <key> [value] (reads stdin if omitted)")
		}
		val := ""
		if len(args) >= 4 {
			val = args[3]
		} else {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			val = strings.TrimRight(string(b), "\n")
		}
		if err := apiJSON("PUT", "/api/xbin/vault/"+comp+"/"+args[2],
			map[string]string{"value": val}, nil); err != nil {
			return err
		}
		fmt.Println("ok")
	case "rm":
		if len(args) < 3 {
			return fmt.Errorf("usage: bx vault rm <component> <key>")
		}
		if err := apiJSON("DELETE", "/api/xbin/vault/"+comp+"/"+args[2], nil, nil); err != nil {
			return err
		}
		fmt.Println("ok")
	default:
		return fmt.Errorf("unknown vault op %q", op)
	}
	return nil
}

func cmdCron(args []string) error {
	if len(args) < 1 || args[0] != "ls" {
		return fmt.Errorf("usage: bx cron ls")
	}
	var out struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := apiJSON("GET", "/api/xbin/cron/jobs", nil, &out); err != nil {
		return err
	}
	for _, j := range out.Jobs {
		fmt.Printf("%-24v %-16v %v%v (role %v)\n", j["name"], j["schedule"], j["component"], j["path"], j["role"])
	}
	return nil
}

func orStatic(s string) string {
	if s == "" {
		return "static"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
