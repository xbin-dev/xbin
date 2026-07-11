package main

import (
	"fmt"
	"sort"
	"strings"
)

// cmdUser manages human users (plans/multi-user.md). Needs admin / the
// xbin:users capability — a terminal runs as the root admin token, so it
// always works from a shell.
//
//	bx user ls
//	bx user add <id> [--admin] [--tiles a=terminal,b=read,lib/*] [--create sales/*]
//	                 [--term-api] [--term-net]                  (prompts for password)
//	bx user set <id> [--admin|--user] [--tiles …] [--create …]
//	                 [--term-api|--no-term-api] [--term-net|--no-term-net] [--password]
//	bx user rm  <id>
//
// --tiles maps paths (or prefix/* patterns) to access levels read|write|
// terminal (D16); a bare path means write. --create lists path patterns the
// user may create tiles under. --term-api / --term-net grant a non-admin's
// terminals the live tile-API token / internet egress (D17).
func cmdUser(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx user ls | add <id> [flags] | set <id> [flags] | rm <id>")
	}
	switch args[0] {
	case "ls":
		var out struct {
			Users []struct {
				ID, Name, Role   string
				Tiles            map[string]string
				CanCreate        []string
				TermAPI, TermNet bool
			} `json:"users"`
		}
		if err := apiJSON("GET", "/api/xbin/users", nil, &out); err != nil {
			return err
		}
		// Org/team memberships per user (best-effort; older daemons lack /orgs).
		memberships := map[string][]string{}
		if orgs, err := fetchOrgs(); err == nil {
			for _, o := range orgs {
				for _, a := range o.Admins {
					memberships[a] = append(memberships[a], o.ID+"(admin)")
				}
				for _, m := range o.Members {
					memberships[m] = append(memberships[m], o.ID)
				}
				for _, t := range o.Teams {
					for _, m := range t.Members {
						memberships[m] = append(memberships[m], o.ID+"/"+t.ID)
					}
				}
			}
		}
		for _, u := range out.Users {
			access := "all"
			if u.Role != "admin" {
				var parts []string
				keys := make([]string, 0, len(u.Tiles))
				for k := range u.Tiles {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					parts = append(parts, k+"="+u.Tiles[k])
				}
				for _, c := range u.CanCreate {
					parts = append(parts, "create:"+c)
				}
				if u.TermAPI {
					parts = append(parts, "term-api")
				}
				if u.TermNet {
					parts = append(parts, "term-net")
				}
				access = strings.Join(parts, ",")
				if access == "" {
					access = "-"
				}
			}
			if ms := memberships[u.ID]; len(ms) > 0 {
				access += " [" + strings.Join(ms, ",") + "]"
			}
			fmt.Printf("%-14s %-8s %-40s %s\n", u.ID, u.Role, access, u.Name)
		}
		return nil

	case "add", "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx user %s <id> [flags]", args[0])
		}
		id := args[1]
		body := map[string]any{"id": id}
		wantPw := args[0] == "add"
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--admin":
				body["role"] = "admin"
			case "--user":
				body["role"] = "user"
			case "--term-api":
				body["termApi"] = true
			case "--no-term-api":
				body["termApi"] = false
			case "--term-net":
				body["termNet"] = true
			case "--no-term-net":
				body["termNet"] = false
			case "--password":
				wantPw = true
			case "--name":
				i++
				body["name"] = args[i]
			case "--tiles":
				i++
				tiles := map[string]string{}
				for _, t := range strings.Split(args[i], ",") {
					if t = strings.TrimSpace(t); t == "" {
						continue
					}
					path, level, ok := strings.Cut(t, "=")
					if !ok {
						level = "write" // bare path = the old allow-list power
					}
					tiles[strings.TrimSpace(path)] = strings.TrimSpace(level)
				}
				body["tiles"] = tiles
			case "--create":
				i++
				create := []string{}
				for _, t := range strings.Split(args[i], ",") {
					if t = strings.TrimSpace(t); t != "" {
						create = append(create, t)
					}
				}
				body["canCreate"] = create
			default:
				return fmt.Errorf("unknown flag %s", args[i])
			}
		}
		if wantPw {
			pw, err := readPassphrase(fmt.Sprintf("password for %s: ", id))
			if err != nil {
				return err
			}
			if pw == "" {
				return fmt.Errorf("password required")
			}
			body["password"] = pw
		}
		method, path := "POST", "/api/xbin/users"
		if args[0] == "set" {
			method, path = "PATCH", "/api/xbin/users/"+id
		}
		if err := apiJSON(method, path, body, nil); err != nil {
			return err
		}
		fmt.Printf("%s %s\n", map[string]string{"add": "created", "set": "updated"}[args[0]], id)
		return nil

	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx user rm <id>")
		}
		if err := apiJSON("DELETE", "/api/xbin/users/"+args[1], nil, nil); err != nil {
			return err
		}
		fmt.Println("removed", args[1])
		return nil
	}
	return fmt.Errorf("unknown: bx user %s", strings.Join(args, " "))
}
