package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/resenc"
)

// cmdDoctor checks the workspace for the problems that actually happen:
// manifest errors, dangling deps, missing API.md on exposing components,
// go.work drift, inotify limits, missing toolchains.
func cmdDoctor() error {
	problems := 0
	warn := func(f string, args ...any) {
		problems++
		fmt.Printf("  ✗ "+f+"\n", args...)
	}
	ok := func(f string, args ...any) { fmt.Printf("  ✓ "+f+"\n", args...) }

	ws := workspaceRoot()
	if ws == "" {
		return fmt.Errorf("not inside a xbin workspace (no xbin.json + .xbin found upward)")
	}
	fmt.Println("workspace:", ws)

	comps, err := components()
	if err != nil {
		warn("xbind unreachable: %v (is it running? is XBIN_URL/XBIN_TOKEN set?)", err)
		comps = nil
	} else {
		ok("xbind reachable, %d components", len(comps))
	}

	byPath := map[string]bool{}
	for _, c := range comps {
		byPath[c.Path] = true
	}
	for _, c := range comps {
		if strings.HasPrefix(c.ManifestErr, "scope.json (") { // what xbind refused in the tile's scope (D118)
			warn("%s: %s", c.Path, c.ManifestErr)
		} else if c.ManifestErr != "" {
			warn("%s: xbin.json: %s", c.Path, c.ManifestErr)
		}
		if strings.Contains(c.Path, "+") { // D127j: no new name may hold '+'; this one predates the rule
			warn("%s: its name holds '+', which names a tile deployment in URLs (/c/<tile>+<name>/) — it keeps working, but can't get deployments; clone it to a path without '+' to give it some (/docs/changes/2026-09-28-plus-in-tile-names.md)", c.Path)
		}
		for _, d := range c.Deps {
			if !byPath[d] {
				warn("%s: dep %q does not exist", c.Path, d)
			}
		}
		if len(c.Roles) > 0 {
			if _, err := os.Stat(filepath.Join(ws, filepath.FromSlash(c.Path), "API.md")); err != nil {
				warn("%s exposes roles but has no API.md (bx new scaffolds one; /docs/elements.md)", c.Path)
			}
			for role, desc := range c.Roles {
				if strings.TrimSpace(desc) == "" {
					warn("%s: role %q has no description", c.Path, role)
				}
			}
		}
	}

	// Ownership/org sanity (plans/ownership.md D24-D33). Best-effort: needs
	// admin credentials; silently skipped otherwise.
	if comps != nil {
		have := map[string]bool{}
		for _, c := range comps {
			have[c.Path] = true
		}
		matches := func(pat string) bool {
			for p := range have {
				if matchesTilePat(pat, p) {
					return true
				}
			}
			return false
		}
		// Owner entries pointing at paths with no component are orphans (a
		// moved/deleted tile) — clear or re-assign with bx owner.
		var mx struct {
			Owners map[string]string `json:"owners"`
			Matrix map[string]map[string]struct {
				Level   string                           `json:"level"`
				Explain []struct{ Level, Source string } `json:"explain"`
			} `json:"matrix"`
		}
		if err := apiJSON("GET", "/api/xbin/access-matrix", nil, &mx); err == nil {
			for p, o := range mx.Owners {
				if !have[p] {
					warn("owner entry %q -> %s has no component (moved or deleted) — clear or re-assign with bx owner", p, o)
				}
			}
		}
		// Org shape: admin-less orgs are manageable only by ws-admins;
		// member-less orgs are usually leftovers. Allowance entries that fail
		// the grammar (hand-edited users.json) can never match an approval.
		var ov struct {
			Orgs []struct {
				ID            string            `json:"id"`
				Members       []map[string]any  `json:"members"`
				ResolvedAllow []string          `json:"resolvedAllow"`
				Tiles         map[string]string `json:"tiles"`
				NetSets       []string          `json:"netSets"`
				NetHost       bool              `json:"netHost"`
			} `json:"orgs"`
		}
		if err := apiJSON("GET", "/api/xbin/orgs", nil, &ov); err == nil {
			// Network sets (D54): attachments must exist, rules must parse, and
			// a set granting host networking is worth a loud line.
			var ns struct {
				Sets map[string]struct {
					Rules []string `json:"rules"`
				} `json:"sets"`
			}
			haveSets := apiJSON("GET", "/api/xbin/net-sets", nil, &ns) == nil
			for name, s := range ns.Sets {
				for _, r := range s.Rules {
					if msg := allowEntryProblem("net:" + r); msg != "" {
						warn("network set %q rule %q: %s", name, r, msg)
					}
				}
			}
			for _, o := range ov.Orgs {
				if haveSets {
					for _, n := range o.NetSets {
						if _, ok := ns.Sets[n]; !ok {
							warn("org %q attaches unknown network set %q (bx netset ls)", o.ID, n)
						}
					}
				}
				if o.NetHost {
					warn("org %q gets HOST networking through its network sets — its tiles and terminals share the host stack (no relay, no filtering, no metering)", o.ID)
				}
			}
			// Personal planes (D88): a user's network sets must exist, and a
			// personal network with host is as loud as an org's.
			var us struct {
				Users []struct {
					ID       string   `json:"id"`
					NetSets  []string `json:"netSets"`
					Personal *struct {
						NetRules []string `json:"netRules"`
					} `json:"personal"`
				} `json:"users"`
			}
			if haveSets && apiJSON("GET", "/api/xbin/users", nil, &us) == nil {
				for _, u := range us.Users {
					for _, n := range u.NetSets {
						if _, ok := ns.Sets[n]; !ok {
							warn("user %q holds unknown network set %q (bx user set %s --net-sets -%s)", u.ID, n, u.ID, n)
						}
					}
					if u.Personal != nil && slices.Contains(u.Personal.NetRules, "host") {
						warn("user %q gets HOST networking on their personal tiles through their network sets", u.ID)
					}
				}
			}
			var binds struct {
				Inert      map[string]map[string]string `json:"inert"`
				Components []struct {
					Component  string `json:"component"`
					Interfaces map[string]struct {
						Kind string `json:"kind"`
					} `json:"interfaces"`
				} `json:"components"`
			}
			if apiJSON("GET", "/api/xbin/bindings", nil, &binds) == nil {
				kind := map[string]string{} // comp\x00slot → interface kind
				for _, c := range binds.Components {
					for slot, def := range c.Interfaces {
						kind[c.Component+"\x00"+slot] = def.Kind
					}
				}
				for comp, slots := range binds.Inert {
					for slot, reason := range slots {
						if kind[comp+"\x00"+slot] == "sandbox-net" { // a sandbox manager's network class
							warn("%s %s: sandbox network class is inert — %s (its sandboxes get no network; rebind the class)", comp, slot, reason)
							continue
						}
						warn("%s %s: net binding is inert — %s (widen the org's network set, or bind net=org)", comp, slot, reason)
					}
				}
			}
			for _, o := range ov.Orgs {
				if len(o.Members) == 0 {
					warn("org %q has no members — leftover? (bx org rm, or add members)", o.ID)
				} else {
					hasAdmin := false
					for _, m := range o.Members {
						a, _ := m["admin"].(bool)
						susp, _ := m["suspended"].(bool)
						if a && !susp { // a suspended admin administers nothing (D34)
							hasAdmin = true
						}
					}
					if !hasAdmin {
						warn("org %q has no active org admin — only workspace admins can manage it (bx org member %s <user> --admin)", o.ID, o.ID)
					}
				}
				for _, e := range o.ResolvedAllow {
					if msg := allowEntryProblem(e); msg != "" {
						warn("org %q allowance %q: %s (hand-edited? it can never match an approval)", o.ID, e, msg)
					}
				}
				for pat := range o.Tiles {
					if !matches(pat) {
						warn("org %q share %q matches no component", o.ID, pat)
					}
				}
				// Exact entries clamping BELOW a member's org level are usually
				// stale approvals (D31: exact is authoritative) — the classic
				// "I promoted the team, why is sam still read-only" ticket.
				// Deliberate `none` exclusions don't warn.
				lvl := map[string]string{}
				for _, m := range o.Members {
					id, _ := m["id"].(string)
					l, _ := m["level"].(string)
					if a, _ := m["admin"].(bool); a {
						l = "terminal"
					}
					if susp, _ := m["suspended"].(bool); !susp {
						lvl[id] = l
					}
				}
				for tile, owner := range mx.Owners {
					if owner != "org:"+o.ID {
						continue
					}
					for user, row := range mx.Matrix {
						c, ok := row[tile]
						if !ok || c.Level == "none" || len(c.Explain) == 0 || c.Explain[0].Source != "exact" {
							continue
						}
						if levelRankDoc(lvl[user]) > levelRankDoc(c.Level) {
							warn("%s on %s: exact entry (%s) clamps their org level (%s) — `bx access %s rm user:%s` to follow the org (D31)",
								user, tile, c.Level, lvl[user], tile, user)
						}
					}
				}
			}
		}
		// defaultTiles patterns that match nothing grant nothing.
		var df struct {
			DefaultTiles map[string]string `json:"defaultTiles"`
		}
		if err := apiJSON("GET", "/api/xbin/defaults", nil, &df); err == nil {
			for pat := range df.DefaultTiles {
				if !matches(pat) {
					warn("defaultTiles pattern %q matches no component", pat)
				}
			}
		}
	}

	// Sealed backups' keys (plans/partitions/11 §5): a new machine restores
	// sealed archives only with an exported bundle. Admin credentials only.
	if comps != nil {
		doctorBackupKeys(warn, ok)
		doctorPartitionEdges()         // edges between partitioned tiles, for review (partitionconsent.go)
		doctorPartitions(ws, warn, ok) // modes, isolation, trust, binds, orphans (doctor_partitions.go)
	}

	// go.work ownership.
	if b, err := os.ReadFile(filepath.Join(ws, "go.work")); err == nil {
		if strings.Contains(string(b), "Code generated by xbind") {
			ok("go.work is xbind-managed")
		} else {
			fmt.Println("  · go.work is hand-managed (xbind will not touch it; tile builds keep its go, toolchain, godebug and replace lines — D166)")
		}
	}
	// Go tiles that link older dependency versions since each builds with
	// its own go.mod (D166). Best-effort: needs admin credentials.
	var gv goBuildVersions
	if err := apiJSON("GET", "/api/xbin/go-build-versions", nil, &gv); err == nil {
		doctorGoBuildVersions(gv, warn)
	}

	// Sandbox uid mapping: a terminal reads its own /proc/self/uid_map. A single
	// mapped range of size 1 (e.g. "0 999 1") is single-uid mode, where apt/dpkg
	// can't chown to the system users their post-install scripts create (chown →
	// "Invalid argument"), so packages like systemd/dbus fail to configure. A
	// delegated sub-id range maps a second, wide row and fixes it. Only meaningful
	// inside a sandboxed terminal (the file is absent/other otherwise).
	if b, err := os.ReadFile("/proc/self/uid_map"); err == nil {
		if singleUIDMap(string(b)) {
			warn("sandbox uid mapping is SINGLE-UID: apt/dpkg installs that create system users (systemd, dbus, …) will fail with chown \"Invalid argument\". Delegate a sub-id range to the xbind user (/etc/subuid + /etc/subgid) and install the uidmap package on the host, then restart xbind (deploy/install.sh does this).")
		} else {
			ok("sandbox uid mapping: full sub-id range")
		}
	}

	// Container-store readiness: cap:containers tiles keep their filesystem
	// resources on gocryptfs single-tenant mounts (docs/resources.md), which
	// need (a) the xbin-patched gocryptfs and (b) `user_allow_other` in
	// /etc/fuse.conf (fusermount3 gates the implied -allow_other for
	// non-root). Best-effort: the grants list needs admin credentials.
	var gl struct {
		Grants []struct{ From, Target string } `json:"grants"`
	}
	if err := apiJSON("GET", "/api/xbin/grants", nil, &gl); err == nil {
		hasContainers := false
		for _, g := range gl.Grants {
			if g.Target == "cap:containers" {
				hasContainers = true
				break
			}
		}
		if hasContainers {
			if bin := resenc.Resolve(); bin == "" {
				warn("cap:containers tile(s) but no gocryptfs found — their stores cannot mount (make build / XBIN_GOCRYPTFS)")
			} else if out, _ := exec.Command(bin, "-hh").CombinedOutput(); !strings.Contains(string(out), "xbin-single-tenant") {
				warn("gocryptfs at %s lacks the single-tenant mode container stores need — rebuild it (make gocryptfs applies hack/gocryptfs-patches)", bin)
			} else {
				ok("gocryptfs supports single-tenant container stores")
			}
			if b, err := os.ReadFile("/etc/fuse.conf"); err != nil || !fuseConfAllowsOther(string(b)) {
				warn("container-store mounts need `user_allow_other` in /etc/fuse.conf (root: `echo user_allow_other >> /etc/fuse.conf`; the system installer does this)")
			} else {
				ok("/etc/fuse.conf allows user allow_other")
			}
		}
	}

	// Strict tile asset gating (docs/auth.md): absolute /c/ URLs, inject:false.
	if comps != nil {
		doctorTileAssets(warn, ok)
		doctorChrome(warn) // chrome requests awaiting an admin (chrome.go)
	}

	// inotify budget (the #1 support issue per plans/deployment.md).
	if b, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches"); err == nil {
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if n < 65536 {
			warn("fs.inotify.max_user_watches=%d is low; set it to 524288 on the host (see /docs/getting-started.md)", n)
		} else {
			ok("inotify watch budget: %d", n)
		}
	}

	// Toolchains for runtimes in use.
	needs := map[string]string{}
	for _, c := range comps {
		switch c.Runtime {
		case "go":
			needs["go"] = c.Path
		case "node":
			needs["node"] = c.Path
		case "python":
			needs["python3"] = c.Path
		}
	}
	for bin, user := range needs {
		if _, err := lookPath(bin); err != nil {
			warn("%s not in PATH (needed by %s)", bin, user)
		}
	}

	if problems == 0 {
		fmt.Println("all good")
		return nil
	}
	return fmt.Errorf("%d problem(s)", problems)
}

// goBuildVersions is GET /go-build-versions (docs/protocol.md): the Go
// tiles whose own go.mod links older versions than the shared go.work did.
type goBuildVersions struct {
	Since          string `json:"since"`
	Done           bool   `json:"done"`
	Running        bool   `json:"running"`
	WorkspaceError string `json:"workspaceError"`
	Tiles          []struct {
		Tile    string   `json:"tile"`
		Require []string `json:"require"`
		Minimal bool     `json:"minimal"`
		Changes []struct {
			Module string `json:"module"`
			Had    string `json:"had"`
			Now    string `json:"now"`
		} `json:"changes"`
		Dismissed bool `json:"dismissed"`
	} `json:"tiles"`
	Errors []struct {
		Tile  string `json:"tile"`
		Error string `json:"error"`
	} `json:"errors"`
}

// doctorGoBuildVersions renders the D166 upgrade check: each tile still
// linking older versions is a problem with the lines to add (a dismissed
// one a note), a tile it couldn't compare a note, and a shared go.work the
// go command refused one note for the workspace.
func doctorGoBuildVersions(gv goBuildVersions, warn func(string, ...any)) {
	since := gv.Since
	if since == "" {
		since = "D166"
	}
	if gv.Running {
		fmt.Println("  · the Go build versions check is running (D166; GET /api/xbin/go-build-versions)")
	}
	if gv.WorkspaceError != "" {
		first, _, _ := strings.Cut(strings.TrimSpace(gv.WorkspaceError), "\n")
		fmt.Printf("  · the Go build versions check couldn't compare the tiles it has no baseline of: %s (POST /api/xbin/go-build-versions/check once that is fixed)\n", first)
	}
	for _, t := range gv.Tiles {
		var lines, changes []string
		for _, r := range t.Require {
			lines = append(lines, "`require "+r+"`")
		}
		for _, c := range t.Changes {
			switch {
			case c.Had == "":
				changes = append(changes, c.Module+" (new) "+c.Now)
			case c.Now == "":
				changes = append(changes, c.Module+" "+c.Had+" → not linked")
			default:
				changes = append(changes, c.Module+" "+c.Had+" → "+c.Now)
			}
		}
		raw := ""
		if !t.Minimal {
			raw = " (the raw differing lines: fewer may do)"
		}
		msg := fmt.Sprintf("%s builds with older dependency versions since %s (each Go tile now builds with its own go.mod's versions): add %s to its go.mod to keep what it had%s",
			t.Tile, since, strings.Join(lines, ", "), raw)
		if len(changes) > 0 {
			msg += " — " + strings.Join(changes, ", ")
		}
		if t.Dismissed {
			fmt.Println("  · (dismissed) " + msg)
		} else {
			warn("%s", msg)
		}
	}
	for _, e := range gv.Errors {
		first, _, _ := strings.Cut(strings.TrimSpace(e.Error), "\n")
		fmt.Printf("  · %s: the Go build versions check couldn't compare its builds: %s\n", e.Tile, first)
	}
}

// fuseConfAllowsOther reports whether a fuse.conf enables user_allow_other
// (an uncommented line; fusermount3 parses it the same way).
func fuseConfAllowsOther(conf string) bool {
	for _, line := range strings.Split(conf, "\n") {
		if strings.TrimSpace(line) == "user_allow_other" {
			return true
		}
	}
	return false
}

// singleUIDMap reports whether a /proc/self/uid_map maps only container-root
// (one non-empty row of "<inside> <outside> <count>" with count 1, and no wider
// row). Range mode adds a second row mapping a large count. Empty/odd → not
// flagged (only the clear single-uid case warns).
func singleUIDMap(s string) bool {
	rows := 0
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		rows++
		if n, err := strconv.Atoi(f[2]); err == nil && n > 1 {
			return false // a wide mapping exists → range mode
		}
	}
	return rows == 1
}

// matchesTilePat mirrors the server's tile-pattern semantics (exact, `*`,
// `prefix/*`, single mid-string glob) for doctor's dead-pattern checks.
func matchesTilePat(pat, path string) bool {
	if pat == "*" || pat == path {
		return true
	}
	if strings.HasSuffix(pat, "/*") {
		prefix := strings.TrimSuffix(pat, "/*")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	if i := strings.IndexByte(pat, '*'); i >= 0 {
		pre, suf := pat[:i], pat[i+1:]
		return len(path) >= len(pre)+len(suf) && strings.HasPrefix(path, pre) && strings.HasSuffix(path, suf)
	}
	return false
}

// allowEntryProblem is an ADVISORY mirror of the server-side allowance
// grammar (internal/users parseAllowEntry) for doctor: the API refuses bad
// entries at write time, so anything caught here was hand-edited into
// users.json. "" = looks fine.
func allowEntryProblem(e string) string {
	e = strings.TrimSpace(e)
	if e == "" {
		return "empty entry"
	}
	if e == "xbin" || strings.HasPrefix(e, "xbin:") || strings.HasPrefix(e, "cap:xbin") {
		return "the xbin capability family is never delegable"
	}
	if e == "cap:sandboxes" {
		return "cap:sandboxes is never delegable — only a workspace admin approves a sandbox manager (D120)"
	}
	class, rest, okCut := strings.Cut(e, ":")
	if !okCut || rest == "" {
		return "not <class>:<value>"
	}
	switch class {
	case "res", "gpu", "cap", "tile", "iface":
		return ""
	case "net":
		if rest == "internet" || rest == "host" ||
			strings.HasPrefix(rest, "internet:") || strings.HasPrefix(rest, "lan:") ||
			strings.HasPrefix(rest, "provider:") {
			return ""
		}
		return "net entries are net:internet[:<spec>], net:host, net:lan:<glob> or net:provider:<tile-glob>"
	case "ingress":
		kind, val, _ := strings.Cut(rest, ":")
		if (kind == "host" || kind == "zone" || kind == "listen") && val != "" {
			return ""
		}
		return "ingress entries are ingress:host:/zone:/listen:<value>"
	}
	return "unknown class (res/gpu/cap/net/iface/ingress/tile)"
}

// levelRankDoc mirrors the server's level ordering for doctor's advisories.
func levelRankDoc(l string) int {
	switch l {
	case "read":
		return 1
	case "write":
		return 2
	case "terminal":
		return 3
	}
	return 0
}

func lookPath(bin string) (string, error) {
	for _, dir := range strings.Split(os.Getenv("PATH"), ":") {
		p := filepath.Join(dir, bin)
		if fi, err := os.Stat(p); err == nil && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found")
}
