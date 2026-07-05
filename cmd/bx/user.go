package main

import (
	"fmt"
	"strings"
)

// cmdUser manages human users (plans/multi-user.md). Needs admin / the
// xbin:users capability — a terminal runs as the root admin token, so it
// always works from a shell.
//
//	bx user ls
//	bx user add <id> [--admin] [--tiles a,b] [--terminal]   (prompts for password)
//	bx user set <id> [--admin|--user] [--tiles a,b] [--terminal|--no-terminal] [--password]
//	bx user rm  <id>
func cmdUser(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx user ls | add <id> [flags] | set <id> [flags] | rm <id>")
	}
	switch args[0] {
	case "ls":
		var out struct {
			Users []struct {
				ID, Name, Role string
				Tiles          []string
				Terminal       bool
			} `json:"users"`
		}
		if err := apiJSON("GET", "/api/xbin/users", nil, &out); err != nil {
			return err
		}
		for _, u := range out.Users {
			tiles := "all"
			if u.Role != "admin" {
				tiles = strings.Join(u.Tiles, ",")
				if tiles == "" {
					tiles = "-"
				}
			}
			term := ""
			if u.Terminal || u.Role == "admin" {
				term = "term"
			}
			fmt.Printf("%-14s %-8s %-5s %-30s %s\n", u.ID, u.Role, term, tiles, u.Name)
		}
		return nil

	case "add", "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx user %s <id> [flags]", args[0])
		}
		id := args[1]
		body := map[string]any{"id": id}
		wantPw := args[0] == "add"
		var tilesSet bool
		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--admin":
				body["role"] = "admin"
			case "--user":
				body["role"] = "user"
			case "--terminal":
				body["terminal"] = true
			case "--no-terminal":
				body["terminal"] = false
			case "--password":
				wantPw = true
			case "--name":
				i++
				body["name"] = args[i]
			case "--tiles":
				i++
				tiles := []string{}
				for _, t := range strings.Split(args[i], ",") {
					if t = strings.TrimSpace(t); t != "" {
						tiles = append(tiles, t)
					}
				}
				body["tiles"] = tiles
				tilesSet = true
			default:
				return fmt.Errorf("unknown flag %s", args[i])
			}
		}
		_ = tilesSet
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
