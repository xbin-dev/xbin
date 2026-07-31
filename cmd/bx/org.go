package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/users"
)

// cmdOrg / cmdTeam / cmdAccess — organizations & teams (plans/orgs.md,
// docs/auth.md). bx authenticates with the owner token, so everything here
// runs with workspace-admin capability from a shell.
//
//	bx org ls
//	bx org add <id> [--name "…"] [--base read|write] [--admin a,b] [--member a,b]
//	bx org set <id> [--name "…"] [--base read|write|none]
//	                [--admin +u|-u]… [--member +u|-u]…
//	bx org rm  <id>
//	bx org policy [<org>]                     show ceiling rows (no org = workspace)
//	bx org policy [<org>] --set '<rows json>' replace them, e.g.
//	    --set '[{"tiles":"*","deny":["net"]},{"tiles":"apps/o/x/*","mayCall":["apps/o/x/*"]}]'

func cmdOrg(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx org ls | add <id> [flags] | set <id> [flags] | rm <id> | policy [<org>] [--set '<json>']")
	}
	switch args[0] {
	case "ls":
		orgs, err := fetchOrgs()
		if err != nil {
			return err
		}
		for _, o := range orgs {
			base := o.BasePermission
			if base == "" {
				base = "-"
			}
			var teams []string
			for _, t := range o.Teams {
				teams = append(teams, t.ID)
			}
			fmt.Printf("%-14s base:%-6s admins:%-20s members:%d teams:%s  %s\n",
				o.ID, base, strings.Join(o.Admins, ","), len(o.Members), strings.Join(teams, ","), o.Name)
		}
		return nil

	case "add", "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx org %s <id> [flags]", args[0])
		}
		id := args[1]
		body := map[string]any{}
		var adminMods, memberMods []string
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--name":
				i++
				body["name"] = args[i]
			case "--base":
				i++
				v := args[i]
				if v == "none" {
					v = ""
				}
				body["basePermission"] = v
			case "--admin":
				i++
				adminMods = append(adminMods, splitList(args[i])...)
			case "--member":
				i++
				memberMods = append(memberMods, splitList(args[i])...)
			default:
				return fmt.Errorf("unknown flag %s", args[i])
			}
		}
		if args[0] == "add" {
			body["id"] = id
			if len(adminMods) > 0 {
				body["admins"] = stripPlus(adminMods)
			}
			if len(memberMods) > 0 {
				body["members"] = stripPlus(memberMods)
			}
			if err := apiJSON("POST", "/api/xbin/orgs", body, nil); err != nil {
				return err
			}
			fmt.Println("created org", id)
			return nil
		}
		// set: +u/-u mods apply against the current lists.
		if len(adminMods) > 0 || len(memberMods) > 0 {
			cur, err := findOrg(id)
			if err != nil {
				return err
			}
			if len(adminMods) > 0 {
				body["admins"] = applyMods(cur.Admins, adminMods)
			}
			if len(memberMods) > 0 {
				body["members"] = applyMods(cur.Members, memberMods)
			}
		}
		if err := apiJSON("PATCH", "/api/xbin/orgs/"+id, body, nil); err != nil {
			return err
		}
		fmt.Println("updated", id)
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

// bx team ls [<org>]
// bx team add <org>/<team> [--name "…"] [--member a,b] [--tiles p=level,…]
//
//	[--create p,…] [--new-tiles read|write|terminal]
//
// bx team set <org>/<team> [--member +u|-u]… [--tiles …] [--create …]
//
//	[--new-tiles L] [--term-api|--no-term-api]
//	[--term-net|--no-term-net] [--name "…"]
//
// bx team rm  <org>/<team>
func cmdTeam(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx team ls [<org>] | add <org>/<team> [flags] | set <org>/<team> [flags] | rm <org>/<team>")
	}
	switch args[0] {
	case "ls":
		orgs, err := fetchOrgs()
		if err != nil {
			return err
		}
		for _, o := range orgs {
			if len(args) > 1 && o.ID != args[1] {
				continue
			}
			for _, t := range o.Teams {
				var parts []string
				keys := make([]string, 0, len(t.Tiles))
				for k := range t.Tiles {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					parts = append(parts, k+"="+t.Tiles[k])
				}
				for _, c := range t.CanCreate {
					parts = append(parts, "create:"+c)
				}
				if t.TermAPI {
					parts = append(parts, "term-api")
				}
				if t.TermNet {
					parts = append(parts, "term-net")
				}
				fmt.Printf("%-24s new-tiles:%-8s members:%-24s %s\n",
					o.ID+"/"+t.ID, t.NewTiles, strings.Join(t.Members, ","), strings.Join(parts, " "))
			}
		}
		return nil

	case "add", "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx team %s <org>/<team> [flags]", args[0])
		}
		org, team, err := users.ParseTeamRef(args[1])
		if err != nil {
			return err
		}
		body := map[string]any{}
		var memberMods []string
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--name":
				i++
				body["name"] = args[i]
			case "--member":
				i++
				memberMods = append(memberMods, splitList(args[i])...)
			case "--tiles":
				i++
				tiles := map[string]string{}
				for _, t := range splitList(args[i]) {
					path, level, ok := strings.Cut(t, "=")
					if !ok {
						level = "write"
					}
					tiles[strings.TrimSpace(path)] = strings.TrimSpace(level)
				}
				body["tiles"] = tiles
			case "--create":
				i++
				body["canCreate"] = splitList(args[i])
			case "--new-tiles":
				i++
				body["newTiles"] = args[i]
			case "--term-api":
				body["termApi"] = true
			case "--no-term-api":
				body["termApi"] = false
			case "--term-net":
				body["termNet"] = true
			case "--no-term-net":
				body["termNet"] = false
			default:
				return fmt.Errorf("unknown flag %s", args[i])
			}
		}
		if args[0] == "add" {
			body["id"] = team
			if len(memberMods) > 0 {
				body["members"] = stripPlus(memberMods)
			}
			if err := apiJSON("POST", "/api/xbin/orgs/"+org+"/teams", body, nil); err != nil {
				return err
			}
			fmt.Println("created team", org+"/"+team)
			return nil
		}
		if len(memberMods) > 0 {
			o, err := findOrg(org)
			if err != nil {
				return err
			}
			var cur []string
			for _, t := range o.Teams {
				if t.ID == team {
					cur = t.Members
				}
			}
			body["members"] = applyMods(cur, memberMods)
		}
		if err := apiJSON("PATCH", "/api/xbin/orgs/"+org+"/teams/"+team, body, nil); err != nil {
			return err
		}
		fmt.Println("updated", org+"/"+team)
		return nil

	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx team rm <org>/<team>")
		}
		org, team, err := users.ParseTeamRef(args[1])
		if err != nil {
			return err
		}
		if err := apiJSON("DELETE", "/api/xbin/orgs/"+org+"/teams/"+team, nil, nil); err != nil {
			return err
		}
		fmt.Println("removed", org+"/"+team)
		return nil
	}
	return fmt.Errorf("unknown: bx team %s", strings.Join(args, " "))
}

// bx access <tile>                                    the tile's resolved ACL
// bx access <tile> set user:bob=write team:o/t=read … set exact entries
// bx access <tile> rm  user:bob team:o/t …            clear exact entries
func cmdAccess(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx access <tile> [set user:<id>=<level>|team:<org>/<team>=<level> … | rm user:<id>|team:<org>/<team> …]")
	}
	tile := strings.Trim(args[0], "/")
	if len(args) == 1 {
		var out struct {
			Org       string `json:"org"`
			OrgAdmins []string
			Entries   []struct{ Kind, ID, Level, Source string }
		}
		if err := apiJSON("GET", "/api/xbin/access?tile="+tile, nil, &out); err != nil {
			return err
		}
		if out.Org != "" {
			fmt.Printf("org: %s (admins: %s)\n", out.Org, strings.Join(out.OrgAdmins, ","))
		} else {
			fmt.Println("org: - (workspace-plane tile)")
		}
		if len(out.Entries) == 0 {
			fmt.Println("no entries — admins only")
			return nil
		}
		for _, e := range out.Entries {
			fmt.Printf("%-5s %-24s %-9s %s\n", e.Kind, e.ID, e.Level, e.Source)
		}
		return nil
	}
	mode := args[1]
	if mode != "set" && mode != "rm" {
		return fmt.Errorf("usage: bx access <tile> [set …|rm …]")
	}
	for _, spec := range args[2:] {
		kind, rest, ok := strings.Cut(spec, ":")
		if !ok || (kind != "user" && kind != "team") {
			return fmt.Errorf("entry %q: want user:<id>[=<level>] or team:<org>/<team>[=<level>]", spec)
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

type orgDoc struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Admins         []string `json:"admins"`
	Members        []string `json:"members"`
	BasePermission string   `json:"basePermission"`
	Teams          []struct {
		ID        string            `json:"id"`
		Name      string            `json:"name"`
		Members   []string          `json:"members"`
		Tiles     map[string]string `json:"tiles"`
		CanCreate []string          `json:"canCreate"`
		NewTiles  string            `json:"newTiles"`
		TermAPI   bool              `json:"termApi"`
		TermNet   bool              `json:"termNet"`
	} `json:"teams"`
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

// applyMods applies +user/-user modifications (a bare name means add).
func applyMods(cur, mods []string) []string {
	set := map[string]bool{}
	for _, c := range cur {
		set[c] = true
	}
	for _, m := range mods {
		switch {
		case strings.HasPrefix(m, "-"):
			delete(set, strings.TrimPrefix(m, "-"))
		default:
			set[strings.TrimPrefix(m, "+")] = true
		}
	}
	out := make([]string, 0, len(set))
	for u := range set {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

func stripPlus(mods []string) []string {
	out := make([]string, 0, len(mods))
	for _, m := range mods {
		if m = strings.TrimPrefix(m, "+"); !strings.HasPrefix(m, "-") && m != "" {
			out = append(out, m)
		}
	}
	return out
}
