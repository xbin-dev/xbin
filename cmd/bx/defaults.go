package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// cmdDefaults — the workspace's provisioning defaults (docs/auth.md, D27 +
// D52): the visibility baseline every user gets, what a NEW account starts
// with (SSO JIT provisioning lands with exactly this), and the tile-creation
// policy.
//
//	bx defaults
//	bx defaults set [--tile-creation any|org-only]
//	                [--default-tiles p=level,…]          (replaces the baseline)
//	                [--tiles p=level,…] [--create p,…]   (new-account seed; replace)
//	                [--org <org>[:level[:create]]]…       (new-account orgs; replace)
//	                [--term-api[=false]] [--term-net[=false]]
//
// `set` overlays the given flags on the current values (unnamed settings are
// left alone); list-valued flags replace their list.
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
		} `json:"newUsers"`
		TileCreation string `json:"tileCreation"`
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
		fmt.Printf("new accounts:   tiles:%s create:%s orgs:%s term-api:%v term-net:%v\n",
			fmtTiles(nu.Tiles), orEmpty(strings.Join(nu.CanCreate, ",")), orEmpty(strings.Join(orgs, ",")), nu.TermAPI, nu.TermNet)
		return nil
	}
	if args[0] != "set" {
		return fmt.Errorf("usage: bx defaults [set [--tile-creation any|org-only] [--default-tiles p=l,…] [--tiles p=l,…] [--create p,…] [--org o[:level[:create]]]… [--term-api[=false]] [--term-net[=false]]]")
	}
	body := map[string]any{}
	nu := map[string]any{
		"tiles": cur.NewUsers.Tiles, "canCreate": cur.NewUsers.CanCreate,
		"termApi": cur.NewUsers.TermAPI, "termNet": cur.NewUsers.TermNet, "orgs": cur.NewUsers.Orgs,
	}
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
