package main

import (
	"fmt"
	"sort"
	"strings"
)

// Ingress commands (plans/ingress.md): publishing tiles.
//
//	bx expose <tile> <slot>=<source> [--host H | --zone '*.Z'] [--listen :P] [--add]
//	bx unexpose <tile> <slot> [--host H | --zone '*.Z' | --listen :P]
//	bx ingress            published endpoints, routes, listener status
//	bx ingress routes     just the host → tile routes

func cmdExpose(args []string) error {
	var pos []string
	body := map[string]any{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--add": // one more route on the endpoint, beside the ones it has (D79)
			body["add"] = true
		case "--host", "--zone", "--listen", "--tcp", "--udp":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value", args[i])
			}
			key := strings.TrimPrefix(args[i], "--")
			if key == "tcp" || key == "udp" {
				key = "listen" // --tcp/--udp are listen-addr aliases (proto comes from the manifest)
			}
			body[key] = args[i+1]
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 2 || !strings.Contains(pos[1], "=") {
		return fmt.Errorf(`usage: bx expose <tile> <slot>=<source> [--host <name> | --zone '*.<suffix>'] [--listen :<port>] [--add]
  sources: "runtime" (xbind's listener / a host port) or an ingress terminator tile
  --add adds a route beside the endpoint's others (more hostnames, more host
  ports); without it the endpoint's routes are replaced by this one
  examples:
    bx expose apps/blog web=apps/traefik --host blog.example.com
    bx expose apps/blog web=apps/traefik --host shop.example.com --add
    bx expose apps/cms  web=apps/traefik --zone '*.sites.example.com'
    bx expose apps/blog web=runtime --host blog.example.com
    bx expose apps/game game=runtime --listen :2456`)
	}
	slot, source, _ := strings.Cut(pos[1], "=")
	body["component"], body["slot"], body["provider"] = pos[0], slot, source
	if err := apiJSON("POST", "/api/xbin/bindings", body, nil); err != nil {
		return err
	}
	fmt.Println("published — check `bx ingress`")
	return nil
}

func cmdUnexpose(args []string) error {
	var pos []string
	body := map[string]string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--host", "--zone", "--listen":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value", args[i])
			}
			body[strings.TrimPrefix(args[i], "--")] = args[i+1]
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	if len(pos) != 2 {
		return fmt.Errorf("usage: bx unexpose <tile> <slot> [--host <name> | --zone '*.<suffix>' | --listen :<port>]\n  with a route: remove just that one; without: every route of the endpoint")
	}
	body["component"], body["slot"] = pos[0], pos[1]
	if err := apiJSON("DELETE", "/api/xbin/bindings", body, nil); err != nil {
		return err
	}
	if len(body) > 2 {
		fmt.Println("route removed")
	} else {
		fmt.Println("unpublished")
	}
	return nil
}

type exposeRouteOut struct {
	Source string `json:"source"`
	Host   string `json:"host"`
	Zone   string `json:"zone"`
	Listen string `json:"listen"`
}

type ingressOut struct {
	Exposes []struct {
		Component string           `json:"component"`
		Slot      string           `json:"slot"`
		Kind      string           `json:"kind"`
		Paths     []string         `json:"paths"`
		Proto     string           `json:"proto"`
		Port      int              `json:"port"`
		Source    string           `json:"source"`
		Host      string           `json:"host"`
		Zone      string           `json:"zone"`
		Listen    string           `json:"listen"`
		Blocked   string           `json:"blocked"`
		Routes    []exposeRouteOut `json:"routes"` // every route (an older xbind: absent — the scalars hold its one)
	} `json:"exposes"`
	Routes []struct {
		Host      string `json:"host"`
		Component string `json:"component"`
		Slot      string `json:"slot"`
		Source    string `json:"source"`
		Zone      string `json:"zone"`
	} `json:"routes"`
	Streams []struct {
		Component string `json:"component"`
		Slot      string `json:"slot"`
		Proto     string `json:"proto"`
		Listen    string `json:"listen"`
		Port      int    `json:"port"`
		Error     string `json:"error"`
		Active    int    `json:"active"`
	} `json:"streams"`
	Forwards []struct {
		Source string `json:"source"`
		Error  string `json:"error"`
	} `json:"forwards"`
	HTTPListener struct {
		Listen string `json:"listen"`
		TLS    bool   `json:"tls"`
	} `json:"httpListener"`
	IngressHosts map[string][]string `json:"ingressHosts"`
}

func cmdIngress(args []string) error {
	var out ingressOut
	if err := apiJSON("GET", "/api/xbin/ingress", nil, &out); err != nil {
		return err
	}
	routesOnly := len(args) > 0 && args[0] == "routes"

	if !routesOnly {
		fmt.Println("published endpoints:")
		if len(out.Exposes) == 0 {
			fmt.Println("  (none — tiles declare \"exposes\" in xbin.json; see /docs/ingress.md)")
		}
		for _, e := range out.Exposes {
			routes := e.Routes
			if len(routes) == 0 && e.Source != "" {
				routes = []exposeRouteOut{{e.Source, e.Host, e.Zone, e.Listen}}
			}
			var states []string // one per route: an endpoint takes many (D79)
			for _, r := range routes {
				if e.Kind == "http" {
					where := r.Host
					if r.Zone != "" {
						where = r.Zone + " (delegated zone)"
					}
					states = append(states, fmt.Sprintf("→ %s  %s", r.Source, where))
					continue
				}
				listen := r.Listen
				if listen == "" {
					listen = fmt.Sprintf(":%d", e.Port)
				}
				states = append(states, fmt.Sprintf("→ host %s/%s → :%d", listen, e.Proto, e.Port))
			}
			switch {
			case e.Blocked != "":
				states = []string{"BLOCKED: " + e.Blocked}
			case len(states) == 0:
				states = []string{"(unbound — not reachable)"}
			}
			detail := ""
			if e.Kind == "http" && len(e.Paths) > 0 {
				detail = "  public: " + strings.Join(e.Paths, " ")
			}
			fmt.Printf("  %-24s %-10s %-6s %s%s\n", e.Component, e.Slot, e.Kind, states[0], detail)
			for _, s := range states[1:] {
				fmt.Printf("  %-24s %-10s %-6s %s\n", "", "", "", s)
			}
		}
	}

	fmt.Println("routes:")
	if len(out.Routes) == 0 {
		fmt.Println("  (none)")
	}
	for _, r := range out.Routes {
		via := r.Source
		if r.Zone != "" {
			via += " (zone " + r.Zone + ")"
		}
		fmt.Printf("  %-32s → %s.%s  via %s\n", r.Host, r.Component, r.Slot, via)
	}
	if routesOnly {
		return nil
	}

	if len(out.Streams) > 0 {
		fmt.Println("stream listeners:")
		for _, s := range out.Streams {
			status := fmt.Sprintf("%d active", s.Active)
			if s.Error != "" {
				status = "ERROR: " + s.Error
			}
			fmt.Printf("  %-20s %s/%s → %s.%s:%d  %s\n", s.Listen, s.Proto, "host", s.Component, s.Slot, s.Port, status)
		}
	}
	if len(out.Forwards) > 0 {
		fmt.Println("terminator forward doors:")
		for _, f := range out.Forwards {
			status := "up"
			if f.Error != "" {
				status = "ERROR: " + f.Error
			}
			fmt.Printf("  %-24s %s\n", f.Source, status)
		}
	}
	if out.HTTPListener.Listen != "" {
		tls := "no TLS (front it or use the traefik tile)"
		if out.HTTPListener.TLS {
			tls = "TLS on"
		}
		fmt.Printf("builtin HTTP listener: %s (%s)\n", out.HTTPListener.Listen, tls)
	} else {
		fmt.Println("builtin HTTP listener: off (start xbind with --ingress-listen)")
	}
	if len(out.IngressHosts) > 0 {
		fmt.Println("zone registrations:")
		comps := make([]string, 0, len(out.IngressHosts))
		for c := range out.IngressHosts {
			comps = append(comps, c)
		}
		sort.Strings(comps)
		for _, c := range comps {
			fmt.Printf("  %-24s %s\n", c, strings.Join(out.IngressHosts[c], " "))
		}
	}
	return nil
}
