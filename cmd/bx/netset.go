package main

import (
	"fmt"
	"sort"
	"strings"
)

// cmdNetset — organisation network sets (docs/auth.md §Network sets, D54).
// A set is a named list of reach rules; attach it to orgs with
// `bx org set <org> --net +<set>`. For an org's own tiles the union of its
// sets is the ceiling on net bindings, what its admins may bind without
// asking, and the default egress (`net=org`) of tiles and terminals.
//
//	bx netset ls
//	bx netset set <name> [--rules a,b] [--add <rule>]… [--rm <rule>]…
//	bx netset rm <name>
//
// Rules: internet | internet:<host|host-glob|ip|cidr>[:port] | lan:<ip|cidr>[:port]
// | host | provider:<tile-glob>.
func cmdNetset(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx netset ls | set <name> [--rules a,b] [--add r]… [--rm r]… | rm <name>")
	}
	type netSet struct {
		Rules   []string `json:"rules"`
		Created int64    `json:"created"`
	}
	fetch := func() (map[string]netSet, map[string][]string, error) {
		var out struct {
			Sets       map[string]netSet   `json:"sets"`
			AttachedTo map[string][]string `json:"attachedTo"`
		}
		if err := apiJSON("GET", "/api/xbin/net-sets", nil, &out); err != nil {
			return nil, nil, err
		}
		return out.Sets, out.AttachedTo, nil
	}
	switch args[0] {
	case "ls":
		sets, attached, err := fetch()
		if err != nil {
			return err
		}
		if len(sets) == 0 {
			fmt.Println("no network sets — bx netset set <name> --rules internet,lan:10.0.0.0/8")
			return nil
		}
		names := make([]string, 0, len(sets))
		for n := range sets {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			ns := sets[n]
			tag := ""
			for _, r := range ns.Rules {
				if r == "host" {
					tag = "  ⚠ host networking"
				}
			}
			fmt.Printf("%-16s rules:[%s] attached:[%s]%s\n", n, strings.Join(ns.Rules, " "), strings.Join(attached[n], ","), tag)
		}
		return nil

	case "set":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx netset set <name> [--rules a,b] [--add <rule>]… [--rm <rule>]…")
		}
		name := args[1]
		var rules []string
		replace := false
		var adds, rms []string
		for i := 2; i < len(args); i++ {
			switch f, v, _ := strings.Cut(args[i], "="); f {
			case "--rules":
				if v == "" {
					i++
					if i >= len(args) {
						return fmt.Errorf("--rules needs a comma-separated list")
					}
					v = args[i]
				}
				rules, replace = splitList(v), true
			case "--add":
				if v == "" {
					i++
					if i >= len(args) {
						return fmt.Errorf("--add needs a rule")
					}
					v = args[i]
				}
				adds = append(adds, v)
			case "--rm":
				if v == "" {
					i++
					if i >= len(args) {
						return fmt.Errorf("--rm needs a rule")
					}
					v = args[i]
				}
				rms = append(rms, v)
			default:
				return fmt.Errorf("unknown flag %s", args[i])
			}
		}
		if !replace {
			sets, _, err := fetch()
			if err != nil {
				return err
			}
			rules = append([]string(nil), sets[name].Rules...)
		}
		for _, r := range rms {
			rules = removeString(rules, strings.ToLower(strings.TrimSpace(r)))
		}
		for _, r := range adds {
			r = strings.ToLower(strings.TrimSpace(r))
			if !containsString(rules, r) {
				rules = append(rules, r)
			}
		}
		// The server is authoritative; this just saves a round-trip for the
		// obvious shapes (doctor's allowance check, net class).
		for _, r := range rules {
			if msg := allowEntryProblem("net:" + r); msg != "" {
				return fmt.Errorf("rule %q: %s", r, msg)
			}
		}
		if rules == nil {
			rules = []string{}
		}
		if err := apiJSON("PUT", "/api/xbin/net-sets/"+name, map[string]any{"rules": rules}, nil); err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", name, strings.Join(rules, " "))
		if len(rules) == 0 {
			fmt.Println("note: an empty set reaches nothing — attached orgs' tiles have no egress")
		}
		return nil

	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: bx netset rm <name>")
		}
		if err := apiJSON("DELETE", "/api/xbin/net-sets/"+args[1], nil, nil); err != nil {
			return err
		}
		fmt.Println("removed", args[1])
		return nil
	}
	return fmt.Errorf("unknown: bx netset %s", args[0])
}

func containsString(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}

func removeString(list []string, v string) []string {
	out := list[:0]
	for _, e := range list {
		if e != v {
			out = append(out, e)
		}
	}
	return out
}
