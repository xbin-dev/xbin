package main

// system.go — `bx system`: the system (sealed) components of this xbin
// (docs/sealed.md) — parts of xbin that run like tiles, with the release's
// own code. The tile commands leave them out (`bx ls` asks for tiles), so
// this is where an admin sees each one, whether it runs and why not.
// `bx logs xbin/<name>`, `bx status xbin/<name>`, `bx access`, `bx bind`,
// `bx enable|disable` and `bx limits` take a system component's path as
// they take a tile's.

import (
	"fmt"
	"net/url"
	"strings"
)

type systemSlot struct {
	Slot    string   `json:"slot"`
	Kind    string   `json:"kind"`
	Service string   `json:"service"`
	Bound   []string `json:"bound"`
}

type systemComp struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	Runtime string `json:"runtime"`
	Digest  string `json:"digest"`
	Build   string `json:"build"`
	State   string `json:"state"`
	Hold    *struct {
		Code   string `json:"code"`
		Reason string `json:"reason"`
	} `json:"hold"`
	Backend     string   `json:"backend"`
	Partition   []string `json:"partition"`
	Partitioned bool     `json:"partitioned"`
	DataFormat  int      `json:"dataFormat"`
	Stamp       *struct {
		Data    int    `json:"data"`
		Release string `json:"release"`
		At      string `json:"at"`
	} `json:"stamp"`
	Slots  []systemSlot `json:"slots"`
	Grants []struct {
		Target string `json:"target"`
		Role   string `json:"role"`
	} `json:"grants"`
	Delegate bool `json:"delegate"`
}

// systemState is the one word `bx system` says about a component.
func systemState(c systemComp) string {
	switch {
	case c.State != "" && c.State != "enabled":
		return "off (" + c.State + ")"
	case c.Hold != nil:
		return "held"
	case c.Backend == "healthy":
		return "running"
	case c.Backend == "building", c.Backend == "starting":
		return "starting"
	case c.Backend == "failed":
		return "failed"
	}
	return "ready"
}

func cmdSystem(args []string) error {
	if len(args) > 1 || len(args) == 1 && strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: bx system [<name>]")
	}
	var out struct {
		Release    string       `json:"release"`
		Components []systemComp `json:"components"`
	}
	if err := apiJSON("GET", "/api/xbin/sealed", nil, &out); err != nil {
		return err
	}
	if len(args) == 0 {
		if len(out.Components) == 0 {
			fmt.Printf("xbin %s has no system components\n", out.Release)
			return nil
		}
		for _, c := range out.Components {
			fmt.Printf("%-24s %-12s %s\n", c.Path, systemState(c), c.Title)
			if c.Hold != nil && (c.State == "" || c.State == "enabled") {
				fmt.Printf("  %s %s\n", c.Path, c.Hold.Reason)
			}
		}
		return nil
	}
	want := strings.TrimPrefix(strings.Trim(args[0], "/"), "xbin/")
	for _, c := range out.Components {
		if c.Name != want {
			continue
		}
		fmt.Printf("%s — %s\n", c.Path, c.Title)
		fmt.Println("  system component, managed by xbin: its code is the installed release's")
		fmt.Printf("  state     %s\n", systemState(c))
		if c.Hold != nil {
			fmt.Printf("  held      %s %s\n", c.Path, c.Hold.Reason)
		}
		build := ""
		switch c.Build {
		case "release":
			build = ", the release's build"
		case "development":
			build = ", compiled by this development xbin"
		case "none":
			build = ", no build in this install"
		}
		fmt.Printf("  version   xbin %s (code %s%s)\n", out.Release, c.Digest, build)
		if len(c.Partition) > 0 {
			if c.Partitioned {
				fmt.Println("  people    kept apart: one instance per person")
			} else {
				fmt.Println("  people    one shared instance (a development xbin without sandboxes)")
			}
		}
		if c.Stamp != nil {
			fmt.Printf("  data      format %d (this release reads up to %d), last written by xbin %s\n", c.Stamp.Data, c.DataFormat, c.Stamp.Release)
		}
		for _, s := range c.Slots {
			bound := "not connected"
			if len(s.Bound) > 0 {
				bound = strings.Join(s.Bound, ", ")
			}
			what := s.Service
			if what == "" {
				what = s.Kind
			}
			fmt.Printf("  slot      %s (%s): %s\n", s.Slot, what, bound)
		}
		var acc struct {
			Entries []struct{ Kind, ID, Level, Source string } `json:"entries"`
		}
		if err := apiJSON("GET", "/api/xbin/access?tile="+url.QueryEscape(c.Path), nil, &acc); err == nil {
			if len(acc.Entries) == 0 {
				fmt.Println("  access    workspace admins only")
			}
			for _, e := range acc.Entries {
				fmt.Printf("  access    %s %s (%s)\n", e.Kind, e.ID, e.Source)
			}
		}
		fmt.Printf("  logs      bx logs %s\n", c.Path)
		return nil
	}
	return fmt.Errorf("no system component %q (bx system lists them)", args[0])
}
