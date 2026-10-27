package main

import (
	"fmt"
	"strconv"
	"strings"
)

// cmdApp is the xbin app's workspace settings (D204): how many devices one
// person may pair, and the app tunnel.
//
//	bx app limit [<n>]    the workspace device limit (admin): show, or set 1–32
//	bx app tunnel …       the app tunnel (admin; docs/app-tunnel.md)

func init() { moreCmds["app"] = cmdApp }

const appUsage = `  bx app limit [<n>]                    app devices one person may pair: show,
                                        or set the workspace default (1–32);
                                        per person: bx user set <id> --max-devices
  bx app tunnel [status] | on | off | test
                                        the xbin app's iroh tunnel (admin)
  bx app tunnel relay n0|<https-url>    n0's public relays, or your iroh-relay
  bx app tunnel token generate-next|set-next <t>|promote|drop-next|set-current <t>|clear
                                        rotate a custom relay's access token
  bx app tunnel only on|off             refuse the app outside the tunnel
`

func cmdApp(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage:\n%s", appUsage)
	}
	switch args[0] {
	case "limit":
		return cmdAppLimit(args[1:])
	case "tunnel":
		return cmdAppTunnel(args[1:])
	}
	return fmt.Errorf("usage:\n%s", appUsage)
}

type limitRow struct {
	User     string `json:"user"`
	Name     string `json:"name"`
	Override *int   `json:"override"`
	Limit    int    `json:"limit"`
	Count    int    `json:"count"`
}

func cmdAppLimit(args []string) error {
	var out struct {
		Default   int        `json:"default"`
		Max       int        `json:"max"`
		AtLimit   []limitRow `json:"atLimit"`
		Overrides []limitRow `json:"overrides"`
	}
	switch len(args) {
	case 0:
		if err := apiJSON("GET", "/api/xbin/devices/limits", nil, &out); err != nil {
			return err
		}
	case 1:
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("usage: bx app limit [<n>] (1–32)")
		}
		if err := apiJSON("PUT", "/api/xbin/devices/limits", map[string]int{"default": n}, &out); err != nil {
			return err
		}
	default:
		return fmt.Errorf("usage: bx app limit [<n>] (1–32)")
	}
	fmt.Printf("app devices per person: %d (at most %d)\n", out.Default, out.Max)
	for _, r := range out.Overrides {
		fmt.Printf("  %s: %d (own limit), %d paired\n", r.User, *r.Override, r.Count)
	}
	for _, r := range out.AtLimit {
		if r.Override == nil {
			fmt.Printf("  %s: at the limit, %d paired\n", r.User, r.Count)
		}
	}
	return nil
}

type tunnelView struct {
	Enabled    bool `json:"enabled"`
	TunnelOnly bool `json:"tunnelOnly"`
	Relay      struct {
		Mode      string `json:"mode"`
		URL       string `json:"url"`
		TokenHint string `json:"tokenHint"`
		Next      *struct {
			URL       string `json:"url"`
			TokenHint string `json:"tokenHint"`
		} `json:"next"`
	} `json:"relay"`
	Env        string `json:"env"`
	State      string `json:"state"`
	Confined   bool   `json:"confined"`
	Restarts   int    `json:"restarts"`
	Error      string `json:"error"`
	EndpointID string `json:"endpointId"`
	HomeRelay  string `json:"homeRelay"`
	Devices    struct {
		Total, TunnelPaired, Classic, Connected, Synced int
	} `json:"devices"`
	LockedOut *int   `json:"lockedOut"`
	Token     string `json:"token"`
}

func cmdAppTunnel(args []string) error {
	usage := fmt.Errorf("usage:\n%s", appUsage)
	var v tunnelView
	put := func(body any) error { return apiJSON("PUT", "/api/xbin/app-tunnel", body, &v) }
	tokens := func(body map[string]string) error {
		return apiJSON("POST", "/api/xbin/app-tunnel/relay-tokens", body, &v)
	}
	var err error
	switch {
	case len(args) == 0 || args[0] == "status":
		err = apiJSON("GET", "/api/xbin/app-tunnel", nil, &v)
	case args[0] == "on" || args[0] == "off":
		err = put(map[string]bool{"enabled": args[0] == "on"})
	case args[0] == "only" && len(args) == 2 && (args[1] == "on" || args[1] == "off"):
		err = put(map[string]bool{"tunnelOnly": args[1] == "on"})
	case args[0] == "relay" && len(args) == 2:
		relay := map[string]string{"mode": "n0"}
		if args[1] != "n0" {
			relay = map[string]string{"mode": "custom", "url": args[1]}
		}
		err = put(map[string]any{"relay": relay})
	case args[0] == "token" && len(args) >= 2:
		body := map[string]string{"action": args[1]}
		if args[1] == "set-next" || args[1] == "set-current" {
			if len(args) != 3 {
				return usage
			}
			body["token"] = args[2]
		}
		err = tokens(body)
	case args[0] == "test":
		var out struct {
			Reachable bool   `json:"reachable"`
			Path      string `json:"path"`
			RTTMs     int    `json:"rttMs"`
			Error     string `json:"error"`
		}
		if err := apiJSON("POST", "/api/xbin/app-tunnel/test", map[string]any{}, &out); err != nil {
			return err
		}
		if !out.Reachable {
			return fmt.Errorf("not reachable through the relay: %s", out.Error)
		}
		fmt.Printf("reachable (%s, %d ms)\n", out.Path, out.RTTMs)
		return nil
	default:
		return usage
	}
	if err != nil {
		return err
	}
	printTunnel(v)
	return nil
}

func printTunnel(v tunnelView) {
	on := "off"
	if v.Enabled {
		on = "on"
	}
	fmt.Printf("app tunnel: %s — %s", on, v.State)
	if v.Confined {
		fmt.Print(" (sandboxed)")
	}
	if v.Restarts > 0 {
		fmt.Printf(", %d restarts", v.Restarts)
	}
	fmt.Println()
	if v.Error != "" {
		fmt.Println("  error:", v.Error)
	}
	relay := "n0's public relays"
	if v.Relay.Mode == "custom" {
		relay = v.Relay.URL
		if v.Relay.TokenHint != "" {
			relay += " (token " + v.Relay.TokenHint + ")"
		}
	}
	if v.Env != "" {
		relay = "XBIN_IROH_RELAY: " + v.Env
	}
	fmt.Println("  relay:", relay)
	if n := v.Relay.Next; n != nil {
		fmt.Println("  next:", strings.TrimSpace(n.URL+" "+n.TokenHint), "— promote once devices synced")
	}
	if v.EndpointID != "" {
		fmt.Println("  endpoint:", v.EndpointID, v.HomeRelay)
	}
	only := "no"
	if v.TunnelOnly {
		only = "yes"
	}
	d := v.Devices
	fmt.Printf("  iroh-only: %s · devices: %d paired with the tunnel (%d connected, %d synced), %d classic\n",
		only, d.TunnelPaired, d.Connected, d.Synced, d.Classic)
	if v.LockedOut != nil && *v.LockedOut > 0 {
		fmt.Printf("  %d devices aren't paired with the tunnel and are locked out until they pair again\n", *v.LockedOut)
	}
	if v.Token != "" {
		fmt.Println("  new token (shown once):", v.Token)
	}
}
