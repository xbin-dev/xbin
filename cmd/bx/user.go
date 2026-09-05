package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// cmdUser manages human users (plans/multi-user.md). Needs admin / the
// xbin:users capability — a terminal runs as the root admin token, so it
// always works from a shell.
//
//	bx user ls
//	bx user add <id> [--admin] [--tiles a=terminal,b=read,lib/*] [--create sales/*]
//	                 [--term-api] [--term-net] [--email a@b.c]  (prompts for password)
//	                 [--invite | --sso]   invite link / SSO-only account (needs --email)
//	                 [--org o[:level[:create[:admin]]]]…   join orgs at creation (D53)
//	bx user set <id> [--admin|--user] [--tiles …] [--create …] [--email a@b.c]
//	                 [--term-api|--no-term-api] [--term-net|--no-term-net] [--password]
//	bx user signout <id>   end every session + terminal token ("sign out everywhere")
//	bx user rm  <id>
//
// --tiles maps paths (or prefix/* patterns) to access levels read|write|
// terminal (D16); a bare path means write. --create lists path patterns the
// user may create tiles under. --term-api / --term-net grant a non-admin's
// terminals the live tile-API token / internet egress (D17). New accounts
// also receive the workspace's new-account defaults (`bx defaults`, D52).
func cmdUser(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx user ls | add <id> [flags] [--invite|--sso] [--org o[:level]]… | set <id> [flags] | invite <id> | signout <id> | rm <id>")
	}
	switch args[0] {
	case "ls":
		var out struct {
			Users []struct {
				ID, Name, Role   string
				Email            string
				RoleVia          string
				Tiles            map[string]string
				CanCreate        []string
				TermAPI, TermNet bool
				Disabled         bool
				InvitePending    bool
				LastLogin        int64
				LastLoginVia     string
			} `json:"users"`
		}
		if err := apiJSON("GET", "/api/xbin/users", nil, &out); err != nil {
			return err
		}
		// Org memberships per user (best-effort). A trailing * marks a
		// membership synced from an IdP group (D53).
		memberships := map[string][]string{}
		if orgs, err := fetchOrgs(); err == nil {
			for _, o := range orgs {
				for _, m := range o.Members {
					tag := o.ID + ":" + m.Level
					if m.Admin {
						tag = o.ID + "(admin)"
					}
					if m.Via == "sso" {
						tag += "*"
					}
					memberships[m.ID] = append(memberships[m.ID], tag)
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
			role := u.Role
			if u.RoleVia == "sso" {
				role += "*" // admin by IdP-group rule
			}
			line := fmt.Sprintf("%-14s %-8s %-40s %s", u.ID, role, access, u.Name)
			if u.Email != "" {
				line += "  <" + u.Email + ">"
			}
			if ms := memberships[u.ID]; len(ms) > 0 {
				line += "  [" + strings.Join(ms, ",") + "]"
			}
			if u.LastLogin > 0 {
				line += "  last:" + agoShort(u.LastLogin) + " via " + u.LastLoginVia
			} else {
				line += "  never signed in"
			}
			if u.Disabled {
				line += "  DISABLED"
			} else if u.InvitePending {
				line += "  invited"
			}
			fmt.Println(line)
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
			case "--invite":
				wantPw = false // create credential-less → the server mints an invite link
			case "--sso":
				// Pre-provision an SSO-only account (D52): credential-less, no
				// invite — the bound --email signs in through the IdP.
				wantPw = false
				body["sso"] = true
			case "--disable":
				body["disabled"] = true
			case "--enable":
				body["disabled"] = false
			case "--name":
				i++
				body["name"] = args[i]
			case "--email":
				// Binds an SSO identity (docs/auth.md §SSO); "" clears.
				i++
				body["email"] = args[i]
			case "--org":
				// Join an org at creation (add only; repeatable):
				// <org>[:level[:create[:admin]]] — D53.
				i++
				if i >= len(args) {
					return fmt.Errorf("--org needs <org>[:level[:create[:admin]]]")
				}
				parts := strings.Split(args[i], ":")
				o := map[string]any{"org": parts[0], "level": "read"}
				if len(parts) > 1 && parts[1] != "" {
					o["level"] = parts[1]
				}
				if len(parts) > 2 {
					o["create"] = parts[2] == "create" || parts[2] == "true"
				}
				if len(parts) > 3 {
					o["admin"] = parts[3] == "admin" || parts[3] == "true"
				}
				orgs, _ := body["orgs"].([]map[string]any)
				body["orgs"] = append(orgs, o)
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
			pw, err := readPassphrase(fmt.Sprintf("password for %s (empty → invite link): ", id))
			if err != nil {
				return err
			}
			body["password"] = pw // empty = invite flow (D22)
		}
		method, path := "POST", "/api/xbin/users"
		if args[0] == "set" {
			method, path = "PATCH", "/api/xbin/users/"+id
		}
		var out struct {
			InviteURL string `json:"inviteUrl"`
		}
		if err := apiJSON(method, path, body, &out); err != nil {
			return err
		}
		fmt.Printf("%s %s\n", map[string]string{"add": "created", "set": "updated"}[args[0]], id)
		if body["sso"] == true {
			fmt.Printf("SSO account — signs in through the IdP as %v (no password, no invite)\n", body["email"])
		}
		printInvite(out.InviteURL)
		return nil

	case "invite":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx user invite <id>")
		}
		var out struct {
			InviteURL string `json:"inviteUrl"`
		}
		if err := apiJSON("POST", "/api/xbin/users/"+args[1]+"/invite", nil, &out); err != nil {
			return err
		}
		printInvite(out.InviteURL)
		return nil

	case "signout":
		// Sign out everywhere (D53): every browser session + terminal token
		// of the user ends; the account itself is untouched.
		if len(args) < 2 {
			return fmt.Errorf("usage: bx user signout <id>")
		}
		var out struct {
			Dropped int `json:"dropped"`
		}
		if err := apiJSON("DELETE", "/api/xbin/users/"+args[1]+"/sessions", nil, &out); err != nil {
			return err
		}
		fmt.Printf("signed out %s everywhere (%d session(s) ended)\n", args[1], out.Dropped)
		return nil

	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx user rm <id>")
		}
		var out struct {
			OrphanedTiles []string `json:"orphanedTiles"`
		}
		if err := apiJSON("DELETE", "/api/xbin/users/"+args[1], nil, &out); err != nil {
			return err
		}
		fmt.Println("removed", args[1])
		for _, t := range out.OrphanedTiles {
			fmt.Printf("  · %s fell to workspace-owned — re-assign with bx owner %s --transfer …\n", t, t)
		}
		return nil
	}
	return fmt.Errorf("unknown: bx user %s", strings.Join(args, " "))
}

// agoShort renders a unix time as a coarse age ("3d", "2h", "5m").
func agoShort(unix int64) string {
	s := time.Now().Unix() - unix
	switch {
	case s < 60:
		return "now"
	case s < 3600:
		return fmt.Sprintf("%dm", s/60)
	case s < 86400:
		return fmt.Sprintf("%dh", s/3600)
	default:
		return fmt.Sprintf("%dd", s/86400)
	}
}

// printInvite shows a freshly minted invite link (single-use, 72h; the admin
// delivers it — there is no self-signup).
func printInvite(url string) {
	if url == "" {
		return
	}
	base, _ := transport()
	fmt.Printf("invite link (single-use, 72h — send it to them):\n  %s%s\n", base, url)
}
