package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// cmdDefaults — the workspace's provisioning defaults (docs/auth.md, D27 +
// D52 + D88): the visibility baseline every user gets, what a NEW account
// starts with (SSO JIT provisioning lands with exactly this), the
// tile-creation policy, and the live personal defaults.
//
//	bx defaults
//	bx defaults set [--tile-creation any|org-only]
//	                [--default-tiles p=level,…]          (replaces the baseline)
//	                [--tiles p=level,…]                   (new-account seed; replace)
//	                [--org <org>[:level[:create]]]…       (new-account orgs; replace)
//	                [--term-api[=false]] [--term-net[=false]]
//	                [--no-personal-tiles[=false]] [--no-terminal[=false]]
//	                [--sets s,…] [--net-sets n,…]         (new-account personal plane, D88)
//	                [--personal-sets s,…] [--personal-net-sets n,…]
//	                                                      (the LIVE personal defaults, D88)
//
// `set` overlays the given flags on the current values (unnamed settings are
// left alone); list-valued flags replace their list. --create p,… (the
// new-account create patterns) is deprecated and ignored (D82) but accepted.
func cmdDefaults(args []string) error {
	var cur struct {
		DefaultTiles map[string]string `json:"defaultTiles"`
		NewUsers     struct {
			Tiles     map[string]string `json:"tiles"`
			CanCreate []string          `json:"canCreate"`
			TermAPI   bool              `json:"termApi"`
			TermNet   bool              `json:"termNet"`
			Orgs      []struct {
				Org    string `json:"org"`
				Level  string `json:"level"`
				Create bool   `json:"create"`
			} `json:"orgs"`
			NoPersonalTiles bool     `json:"noPersonalTiles"`
			NoTerminal      bool     `json:"noTerminal"`
			Sets            []string `json:"sets"`
			NetSets         []string `json:"netSets"`
		} `json:"newUsers"`
		TileCreation     string `json:"tileCreation"`
		PersonalDefaults struct {
			Sets    []string `json:"sets"`
			NetSets []string `json:"netSets"`
		} `json:"personalDefaults"`
	}
	if err := apiJSON("GET", "/api/xbin/defaults", nil, &cur); err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "ls" {
		fmt.Printf("tile creation:  %s\n", cur.TileCreation)
		fmt.Printf("default tiles:  %s\n", fmtTiles(cur.DefaultTiles))
		nu := cur.NewUsers
		var orgs []string
		for _, o := range nu.Orgs {
			tag := o.Org + ":" + o.Level
			if o.Create {
				tag += ":create"
			}
			orgs = append(orgs, tag)
		}
		fmt.Printf("new accounts:   tiles:%s orgs:%s term-api:%v term-net:%v no-personal-tiles:%v no-terminal:%v sets:%s net-sets:%s\n",
			fmtTiles(nu.Tiles), orEmpty(strings.Join(orgs, ",")), nu.TermAPI, nu.TermNet,
			nu.NoPersonalTiles, nu.NoTerminal, orEmpty(strings.Join(nu.Sets, ",")), orEmpty(strings.Join(nu.NetSets, ",")))
		pd := cur.PersonalDefaults
		fmt.Printf("personal:       sets:%s net-sets:%s   (every user's tiles, live)\n",
			orEmpty(strings.Join(pd.Sets, ",")), orEmpty(strings.Join(pd.NetSets, ",")))
		return nil
	}
	if args[0] != "set" {
		return fmt.Errorf("usage: bx defaults [set [--tile-creation any|org-only] [--default-tiles p=l,…] [--tiles p=l,…] [--org o[:level[:create]]]… [--term-api[=false]] [--term-net[=false]] [--no-personal-tiles[=false]] [--no-terminal[=false]] [--sets s,…] [--net-sets n,…] [--personal-sets s,…] [--personal-net-sets n,…]]")
	}
	body := map[string]any{}
	nu := map[string]any{
		"tiles": cur.NewUsers.Tiles, "canCreate": cur.NewUsers.CanCreate,
		"termApi": cur.NewUsers.TermAPI, "termNet": cur.NewUsers.TermNet, "orgs": cur.NewUsers.Orgs,
		"noPersonalTiles": cur.NewUsers.NoPersonalTiles, "noTerminal": cur.NewUsers.NoTerminal,
		"sets": cur.NewUsers.Sets, "netSets": cur.NewUsers.NetSets,
	}
	personal := map[string]any{"sets": cur.PersonalDefaults.Sets, "netSets": cur.PersonalDefaults.NetSets}
	personalTouched := false
	touched := false
	var orgs []map[string]any
	for i := 1; i < len(args); i++ {
		flag, val, hasVal := strings.Cut(args[i], "=")
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			i++
			if i >= len(args) {
				return "", fmt.Errorf("%s needs a value", flag)
			}
			return args[i], nil
		}
		boolVal := func() (bool, error) {
			if !hasVal {
				return true, nil
			}
			return strconv.ParseBool(val)
		}
		switch flag {
		case "--tile-creation":
			v, err := next()
			if err != nil {
				return err
			}
			body["tileCreation"] = v
		case "--default-tiles":
			v, err := next()
			if err != nil {
				return err
			}
			body["defaultTiles"] = parseTiles(v)
		case "--tiles":
			v, err := next()
			if err != nil {
				return err
			}
			nu["tiles"] = parseTiles(v)
			touched = true
		case "--create":
			v, err := next()
			if err != nil {
				return err
			}
			var pats []string
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					pats = append(pats, p)
				}
			}
			nu["canCreate"] = pats
			touched = true
			fmt.Fprintln(os.Stderr, createDeprecated)
		case "--org":
			v, err := next()
			if err != nil {
				return err
			}
			parts := strings.Split(v, ":")
			o := map[string]any{"org": parts[0], "level": "read"}
			if len(parts) > 1 && parts[1] != "" {
				o["level"] = parts[1]
			}
			if len(parts) > 2 {
				o["create"] = parts[2] == "create" || parts[2] == "true"
			}
			orgs = append(orgs, o)
			touched = true
		case "--term-api":
			v, err := boolVal()
			if err != nil {
				return err
			}
			nu["termApi"] = v
			touched = true
		case "--term-net":
			v, err := boolVal()
			if err != nil {
				return err
			}
			nu["termNet"] = v
			touched = true
		case "--no-personal-tiles", "--no-terminal":
			v, err := boolVal()
			if err != nil {
				return err
			}
			nu[map[string]string{"--no-personal-tiles": "noPersonalTiles", "--no-terminal": "noTerminal"}[flag]] = v
			touched = true
		case "--sets", "--net-sets", "--personal-sets", "--personal-net-sets":
			v, err := next()
			if err != nil {
				return err
			}
			list := orEmptyList(splitList(v))
			switch flag {
			case "--sets":
				nu["sets"], touched = list, true
			case "--net-sets":
				nu["netSets"], touched = list, true
			case "--personal-sets":
				personal["sets"], personalTouched = list, true
			default:
				personal["netSets"], personalTouched = list, true
			}
		default:
			return fmt.Errorf("unknown flag %s", flag)
		}
	}
	if orgs != nil {
		nu["orgs"] = orgs
	}
	if touched {
		body["newUsers"] = nu
	}
	if personalTouched {
		body["personalDefaults"] = personal
	}
	if len(body) == 0 {
		return fmt.Errorf("nothing to set — see bx defaults --help")
	}
	if err := apiJSON("PUT", "/api/xbin/defaults", body, nil); err != nil {
		return err
	}
	fmt.Println("updated workspace defaults")
	return nil
}

// parseTiles turns "a=read,b/*=write,c" into a path→level map (bare = write).
func parseTiles(s string) map[string]string {
	tiles := map[string]string{}
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		path, level, ok := strings.Cut(t, "=")
		if !ok {
			level = "write"
		}
		tiles[strings.TrimSpace(path)] = strings.TrimSpace(level)
	}
	return tiles
}

func fmtTiles(m map[string]string) string {
	if len(m) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func orEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
