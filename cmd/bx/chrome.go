package main

import (
	"errors"
	"fmt"
	"strings"
)

// bx chrome — the tiles that ask to run as trusted workspace chrome, and the
// workspace admin's approvals (D118, docs/auth.md §Who is calling). A tile's
// `chrome: true` is only a request: its own terminals and agents can write
// its xbin.json, and chrome acts as whoever opens the tile.

const chromeUsage = `  bx chrome [ls] | approve <tile> | revoke <tile>
                                        trusted chrome: requests + admin approvals
`

// chromeRow mirrors GET /api/xbin/chrome's rows.
type chromeRow struct {
	Path                                          string
	Requested, Approved, Shipped, Chrome, Missing bool
}

// state is one row's plain-language status.
func (t chromeRow) state() string {
	switch {
	case t.Chrome && t.Shipped:
		return "chrome (shipped)"
	case t.Chrome:
		return "chrome (approved)"
	case t.Missing:
		return "approved, but no component is there now — bx chrome revoke " + t.Path
	case t.Requested:
		return "asks for chrome — runs sandboxed until an admin approves it (bx chrome approve " + t.Path + ")"
	case t.Approved:
		return "approved, inactive: its xbin.json does not ask for chrome"
	}
	return "not chrome"
}

func cmdChrome(args []string) error {
	sub := "ls"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "ls":
		if len(args) > 0 {
			return errors.New("usage: bx chrome ls")
		}
		var out struct{ Tiles []chromeRow }
		if err := apiJSON("GET", "/api/xbin/chrome", nil, &out); err != nil {
			return err
		}
		if len(out.Tiles) == 0 {
			fmt.Println("no tile asks for chrome (root and shell are chrome implicitly)")
			return nil
		}
		for _, t := range out.Tiles {
			fmt.Printf("%-40s %s\n", t.Path, t.state())
		}
		return nil
	case "approve", "revoke":
		if len(args) != 1 || isFlag(args[0]) {
			return fmt.Errorf("usage: bx chrome %s <tile>", sub)
		}
		var row chromeRow
		body := map[string]any{"path": strings.Trim(args[0], "/"), "approved": sub == "approve"}
		if err := apiJSON("PUT", "/api/xbin/chrome", body, &row); err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", row.Path, row.state())
		if sub == "approve" && row.Chrome {
			fmt.Println("its documents now run unsandboxed with the session cookie, acting as whoever opens the tile — every writer of it is trusted like the shell")
		}
		return nil
	}
	return fmt.Errorf("bx chrome: unknown subcommand %q (ls|approve|revoke)", sub)
}

// doctorChrome: tiles whose xbin.json asks for chrome that no admin approved
// (they run sandboxed until one does), and approvals naming no component.
func doctorChrome(warn func(string, ...any)) {
	var comps []struct {
		Path            string `json:"path"`
		ChromeRequested bool   `json:"chromeRequested"`
	}
	if apiJSON("GET", "/api/xbin/components", nil, &comps) == nil {
		for _, c := range comps {
			if c.ChromeRequested {
				warn("%s asks for chrome in its xbin.json, but no workspace admin approved it — it runs sandboxed (D118). If you trust every writer of it as much as the shell: bx chrome approve %s", c.Path, c.Path)
			}
		}
	}
	var out struct{ Tiles []chromeRow }
	if apiJSON("GET", "/api/xbin/chrome", nil, &out) == nil { // admin credentials; skipped otherwise
		for _, t := range out.Tiles {
			if t.Missing {
				warn("chrome approval for %s names no component (moved or deleted) — bx chrome revoke %s", t.Path, t.Path)
			}
		}
	}
}
