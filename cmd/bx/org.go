package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/users"
)

// cmdOrg / cmdOwner / cmdPermset / cmdAccess — ownership & organizations
// (plans/ownership.md, docs/auth.md D24–D28). bx authenticates with the owner
// token, so everything here runs with workspace-admin capability from a shell.
//
//	bx org ls
//	bx org add <id> [--name "…"]
//	bx org set <id> [--name "…"] [--sets +s|-s]… [--allow +t|-t]…
//	bx org member <org> [<user> [--level read|write|terminal] [--create[=false]]
//	                     [--admin[=false]] [--suspend|--unsuspend] | rm <user>]
//	bx org rm  <id>
//	bx org policy [<org>] [--set '<rows json>']
//	bx owner <tile> [--transfer user:<id>|org:<id>|workspace]
//	bx permset ls | set <name> [--allow a,b] [--policy '<json>']
//	                 [--term-api[=false]] [--term-net[=false]] | rm <name>

func cmdOrg(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx org ls | add <id> [flags] | set <id> [flags] | member <org> … | rm <id> | policy [<org>] [--set '<json>']")
	}
	switch args[0] {
	case "ls":
		orgs, err := fetchOrgs()
		if err != nil {
			return err
		}
		for _, o := range orgs {
			var mem []string
			for _, m := range o.Members {
				tag := m.ID + ":" + string(m.Level[0])
				if m.Admin {
					tag += "★"
				} else if m.Create {
					tag += "+"
				}
				mem = append(mem, tag)
			}
			fmt.Printf("%-14s members:[%s] sets:%s owned:%d  %s\n",
				o.ID, strings.Join(mem, " "), strings.Join(o.Sets, ","), len(o.OwnedTiles), o.Name)
			if len(o.ResolvedAllow) > 0 {
				fmt.Printf("               may self-approve: %s\n", strings.Join(o.ResolvedAllow, " "))
			}
		}
		return nil

	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx org add <id> [--name \"…\"]")
		}
		body := map[string]any{"id": args[1]}
		for i := 2; i < len(args); i++ {
			if args[i] == "--name" {
				i++
				body["name"] = args[i]
			}
		}
		if err := apiJSON("POST", "/api/xbin/orgs", body, nil); err != nil {
			return err
		}
		fmt.Println("created org", args[1])
		return nil

	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx org set <id> [--name \"…\"] [--sets +s|-s] [--allow +t|-t]")
		}
		id := args[1]
		body := map[string]any{}
		var setMods, allowMods []string
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--name":
				i++
				body["name"] = args[i]
			case "--sets":
				i++
				setMods = append(setMods, splitList(args[i])...)
			case "--allow":
				i++
				allowMods = append(allowMods, splitList(args[i])...)
			default:
				return fmt.Errorf("unknown flag %s", args[i])
			}
		}
		if len(setMods) > 0 || len(allowMods) > 0 {
			cur, err := findOrg(id)
			if err != nil {
				return err
			}
			if len(setMods) > 0 {
				body["sets"] = applyMods(cur.Sets, setMods)
			}
			if len(allowMods) > 0 {
				body["allow"] = applyMods(cur.Allow, allowMods)
			}
		}
		if err := apiJSON("PATCH", "/api/xbin/orgs/"+id, body, nil); err != nil {
			return err
		}
		fmt.Println("updated", id)
		return nil

	case "member":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx org member <org> [<user> [--level L] [--create[=false]] [--admin[=false]] | rm <user>]")
		}
		org, err := findOrg(args[1])
		if err != nil {
			return err
		}
		rest := args[2:]
		if len(rest) == 0 { // list
			for _, m := range org.Members {
				tag := ""
				if m.Suspended {
					tag = "  SUSPENDED"
				}
				fmt.Printf("%-16s level:%-9s create:%-5v admin:%v%s\n", m.ID, m.Level, m.Create, m.Admin, tag)
			}
			return nil
		}
		members := org.Members
		if rest[0] == "rm" {
			if len(rest) < 2 {
				return fmt.Errorf("usage: bx org member <org> rm <user>")
			}
			out := members[:0]
			for _, m := range members {
				if m.ID != rest[1] {
					out = append(out, m)
				}
			}
			members = out
		} else {
			user := rest[0]
			// Start from the existing entry (or a fresh read-level one).
			entry := memberDoc{ID: user, Level: users.LevelRead}
			idx := -1
			for i, m := range members {
				if m.ID == user {
					entry, idx = m, i
				}
			}
			for i := 1; i < len(rest); i++ {
				switch f, v, _ := strings.Cut(rest[i], "="); f {
				case "--level":
					if v == "" {
						i++
						v = rest[i]
					}
					entry.Level = v
				case "--create":
					entry.Create = v != "false"
				case "--admin":
					entry.Admin = v != "false"
				case "--suspend":
					entry.Suspended = true
				case "--unsuspend":
					entry.Suspended = false
				default:
					return fmt.Errorf("unknown flag %s", rest[i])
				}
			}
			if idx >= 0 {
				members[idx] = entry
			} else {
				members = append(members, entry)
			}
		}
		if err := apiJSON("PATCH", "/api/xbin/orgs/"+args[1], map[string]any{"members": members}, nil); err != nil {
			return err
		}
		fmt.Println("ok")
		return nil

	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx org rm <id>")
		}
		if err := apiJSON("DELETE", "/api/xbin/orgs/"+args[1], nil, nil); err != nil {
			return err
		}
		fmt.Println("removed", args[1])
		return nil

	case "policy":
		path := "/api/xbin/policy"
		rest := args[1:]
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "--") {
			path = "/api/xbin/orgs/" + rest[0] + "/policy"
			rest = rest[1:]
		}
		if len(rest) >= 2 && rest[0] == "--set" {
			var rows []map[string]any
			if err := json.Unmarshal([]byte(rest[1]), &rows); err != nil {
				return fmt.Errorf("--set wants a JSON array of {tiles, deny?, mayCall?}: %w", err)
			}
			if err := apiJSON("PUT", path, map[string]any{"policy": rows}, nil); err != nil {
				return err
			}
			fmt.Println("policy updated")
			return nil
		}
		var out struct {
			Policy []users.PolicyRow `json:"policy"`
		}
		if err := apiJSON("GET", path, nil, &out); err != nil {
			return err
		}
		if len(out.Policy) == 0 {
			fmt.Println("no policy rows (no ceiling)")
			return nil
		}
		for _, r := range out.Policy {
			line := "tiles=" + r.Tiles
			if len(r.Deny) > 0 {
				line += " deny=" + strings.Join(r.Deny, ",")
			}
			if len(r.MayCall) > 0 {
				line += " mayCall=" + strings.Join(r.MayCall, ",")
			}
			fmt.Println(line)
		}
		return nil
	}
	return fmt.Errorf("unknown: bx org %s", strings.Join(args, " "))
}

// cmdOwner shows or transfers a tile's owner (D24).
func cmdOwner(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx owner <tile> [--transfer user:<id>|org:<id>|workspace]")
	}
	tile := strings.Trim(args[0], "/")
	if len(args) >= 3 && args[1] == "--transfer" {
		to := args[2]
		if to == "workspace" {
			to = ""
		}
		if err := apiJSON("POST", "/api/xbin/owner", map[string]any{"tile": tile, "to": to}, nil); err != nil {
			return err
		}
		if to == "" {
			to = "workspace"
		}
		fmt.Println(tile, "→", to)
		return nil
	}
	var out struct {
		Owner string `json:"owner"`
	}
	if err := apiJSON("GET", "/api/xbin/owner?tile="+tile, nil, &out); err != nil {
		return err
	}
	if out.Owner == "" {
		out.Owner = "workspace"
	}
	fmt.Println(out.Owner)
	return nil
}

// cmdPermset manages the reusable org-permission bundles (D28, ws-admin).
func cmdPermset(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx permset ls | set <name> [--allow a,b] [--policy '<json>'] [--term-api[=false]] [--term-net[=false]] | rm <name>")
	}
	switch args[0] {
	case "ls":
		var out struct {
			Sets       map[string]users.PermissionSet `json:"sets"`
			AttachedTo map[string][]string            `json:"attachedTo"`
		}
		if err := apiJSON("GET", "/api/xbin/permission-sets", nil, &out); err != nil {
			return err
		}
		for name, ps := range out.Sets {
			flags := ""
			if ps.TermAPI {
				flags += " term-api"
			}
			if ps.TermNet {
				flags += " term-net"
			}
			fmt.Printf("%-16s allow:[%s]%s attached:[%s] policy-rows:%s\n",
				name, strings.Join(ps.Allow, " "), flags,
				strings.Join(out.AttachedTo[name], ","), strconv.Itoa(len(ps.Policy)))
		}
		return nil
	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx permset set <name> [flags]")
		}
		body := map[string]any{}
		for i := 2; i < len(args); i++ {
			switch f, v, _ := strings.Cut(args[i], "="); f {
			case "--allow":
				if v == "" {
					i++
					v = args[i]
				}
				body["allow"] = splitList(v)
			case "--policy":
				if v == "" {
					i++
					v = args[i]
				}
				var rows []map[string]any
				if err := json.Unmarshal([]byte(v), &rows); err != nil {
					return fmt.Errorf("--policy wants a JSON array: %w", err)
				}
				body["policy"] = rows
			case "--term-api":
				body["termApi"] = v != "false"
			case "--term-net":
				body["termNet"] = v != "false"
			default:
				return fmt.Errorf("unknown flag %s", args[i])
			}
		}
		if err := apiJSON("PUT", "/api/xbin/permission-sets/"+args[1], body, nil); err != nil {
			return err
		}
		fmt.Println("ok")
		return nil
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx permset rm <name>")
		}
		if err := apiJSON("DELETE", "/api/xbin/permission-sets/"+args[1], nil, nil); err != nil {
			return err
		}
		fmt.Println("removed", args[1])
		return nil
	}
	return fmt.Errorf("unknown: bx permset %s", args[0])
}

func cmdAccess(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx access <tile> [set user:<id>=<level>|org:<id>=<level> … | rm user:<id>|org:<id> …] — levels read|write|terminal, or none (user entries: explicit exclude; exact entries override org level/patterns, D31)")
	}
	tile := strings.Trim(args[0], "/")
	if len(args) == 1 {
		var out struct {
			Owner   string `json:"owner"`
			Entries []struct{ Kind, ID, Level, Source string }
		}
		if err := apiJSON("GET", "/api/xbin/access?tile="+tile, nil, &out); err != nil {
			return err
		}
		if out.Owner == "" {
			out.Owner = "workspace"
		}
		fmt.Println("owner:", out.Owner)
		if len(out.Entries) == 0 {
			fmt.Println("no entries — owner/admins only")
		}
		for _, e := range out.Entries {
			fmt.Printf("%-5s %-24s %-9s %s\n", e.Kind, e.ID, e.Level, e.Source)
		}
		// Pending human requests for this tile (D36), best-effort.
		var reqs struct {
			Requests []struct {
				User, Tile, Level, Note string
			} `json:"requests"`
		}
		if err := apiJSON("GET", "/api/xbin/access-requests", nil, &reqs); err == nil {
			for _, q := range reqs.Requests {
				if q.Tile == tile {
					note := ""
					if q.Note != "" {
						note = " — " + q.Note
					}
					fmt.Printf("REQUEST %s wants %s%s (approve: bx access %s approve %s)\n",
						q.User, q.Level, note, tile, q.User)
				}
			}
		}
		return nil
	}
	mode := args[1]
	if mode == "approve" { // D36: approve a pending request → exact ACL entry
		if len(args) < 3 {
			return fmt.Errorf("usage: bx access <tile> approve <user> [level]")
		}
		body := map[string]any{"tile": tile, "user": args[2]}
		if len(args) > 3 {
			body["level"] = args[3]
		}
		var out struct {
			Level string `json:"level"`
		}
		if err := apiJSON("POST", "/api/xbin/access-requests/approve", body, &out); err != nil {
			return err
		}
		fmt.Printf("granted %s %s on %s\n", args[2], out.Level, tile)
		return nil
	}
	if mode == "request" { // D36: file a human access request
		level := "read"
		if len(args) > 2 {
			level = args[2]
		}
		var out struct {
			Level string `json:"level"`
		}
		if err := apiJSON("POST", "/api/xbin/access-requests", map[string]any{"tile": tile, "level": level}, &out); err != nil {
			return err
		}
		fmt.Printf("requested %s on %s — the tile's owner/org admins see it in their queue\n", out.Level, tile)
		return nil
	}
	if mode != "set" && mode != "rm" {
		return fmt.Errorf("usage: bx access <tile> [set …|rm …|request [level]|approve <user> [level]]")
	}
	for _, spec := range args[2:] {
		kind, rest, ok := strings.Cut(spec, ":")
		if !ok || (kind != "user" && kind != "org") {
			return fmt.Errorf("entry %q: want user:<id>[=<level>] or org:<id>[=<level>]", spec)
		}
		id, level, hasLevel := strings.Cut(rest, "=")
		if mode == "rm" {
			level = ""
		} else if !hasLevel {
			level = "write"
		}
		body := map[string]any{"tile": tile, "kind": kind, "id": id, "level": level}
		if err := apiJSON("PUT", "/api/xbin/access", body, nil); err != nil {
			return fmt.Errorf("%s: %w", spec, err)
		}
	}
	fmt.Println("ok")
	return nil
}

// --- shared helpers ---------------------------------------------------------

type memberDoc struct {
	ID        string `json:"id"`
	Level     string `json:"level"`
	Create    bool   `json:"create,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
	Suspended bool   `json:"suspended,omitempty"`
}

type orgDoc struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Members       []memberDoc       `json:"members"`
	Tiles         map[string]string `json:"tiles"`
	Sets          []string          `json:"sets"`
	Allow         []string          `json:"allow"`
	ResolvedAllow []string          `json:"resolvedAllow"`
	OwnedTiles    []string          `json:"ownedTiles"`
}

func fetchOrgs() ([]orgDoc, error) {
	var out struct {
		Orgs []orgDoc `json:"orgs"`
	}
	if err := apiJSON("GET", "/api/xbin/orgs", nil, &out); err != nil {
		return nil, err
	}
	return out.Orgs, nil
}

func findOrg(id string) (*orgDoc, error) {
	orgs, err := fetchOrgs()
	if err != nil {
		return nil, err
	}
	for i := range orgs {
		if orgs[i].ID == id {
			return &orgs[i], nil
		}
	}
	return nil, fmt.Errorf("no such org %q", id)
}

func splitList(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// applyMods applies +item/-item modifications (a bare name means add).
func applyMods(cur, mods []string) []string {
	out := append([]string(nil), cur...)
	for _, m := range mods {
		if strings.HasPrefix(m, "-") {
			v := strings.TrimPrefix(m, "-")
			keep := out[:0]
			for _, e := range out {
				if e != v {
					keep = append(keep, e)
				}
			}
			out = keep
			continue
		}
		v := strings.TrimPrefix(m, "+")
		dup := false
		for _, e := range out {
			if e == v {
				dup = true
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}
