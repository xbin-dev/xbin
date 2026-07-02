// bx — the buxon workspace CLI, available in every terminal session.
// A thin client of buxond's API (BUXON_URL + BUXON_TOKEN from the session
// env) plus local scaffolding. Builder reference: /docs/bx.md.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/magik6k/buxon/internal/util"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "ls":
		err = cmdLs()
	case "new":
		err = cmdNew(os.Args[2:])
	case "tile":
		err = cmdTile(os.Args[2:])
	case "logs":
		err = cmdLogs(os.Args[2:])
	case "doctor":
		err = cmdDoctor()
	case "api":
		err = cmdAPI(os.Args[2:])
	case "grant":
		err = cmdGrant(os.Args[2:])
	case "grants":
		err = cmdGrants()
	case "vault":
		err = cmdVault(os.Args[2:])
	case "cron":
		err = cmdCron(os.Args[2:])
	case "status":
		err = cmdStatus()
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bx:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bx — buxon workspace CLI (docs: /docs/bx.md)

  bx ls                                 list components
  bx status                             backend states, terminals
  bx new <path> [--runtime go|node|python|cgi] [--expose]
                                        scaffold a component
  bx tile ls | import <name> [as <path>]
                                        list/install builtin tiles
  bx logs [-f] <component>              show backend logs
  bx api <component>                    roles + API.md of a component
  bx grants                             grant table + pending requests
  bx grant <caller> <target>:<role>     approve/add a grant
  bx grant --revoke <caller> <target>:<role>
  bx vault ls|get|set|rm <component> [key] [value]
  bx cron ls                            scheduled jobs
  bx doctor                             check the workspace for problems
`)
	os.Exit(2)
}

// --- buxond API plumbing ---

func api(method, path string, body any) (*http.Response, error) {
	base := os.Getenv("BUXON_URL")
	if base == "" {
		base = "http://127.0.0.1:8642"
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("BUXON_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return (&http.Client{Timeout: 30 * time.Second}).Do(req)
}

func apiJSON(method, path string, body, out any) error {
	resp, err := api(method, path, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var e struct{ Error string }
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return fmt.Errorf("%s (%s)", e.Error, resp.Status)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func workspaceRoot() string {
	if ws := os.Getenv("BUXON_WORKSPACE"); ws != "" {
		return ws
	}
	// Walk up from cwd looking for buxon.json.
	dir, _ := os.Getwd()
	for d := dir; d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "buxon.json")); err == nil {
			if _, err := os.Stat(filepath.Join(d, ".buxon")); err == nil {
				return d
			}
		}
	}
	return ""
}

type compInfo struct {
	Path        string            `json:"path"`
	Scope       string            `json:"scope"`
	Runtime     string            `json:"runtime"`
	HasIndex    bool              `json:"hasIndex"`
	Roles       map[string]string `json:"roles"`
	Deps        []string          `json:"deps"`
	ManifestErr string            `json:"manifestError"`
}

func components() ([]compInfo, error) {
	var out []compInfo
	err := apiJSON("GET", "/api/buxon/components", nil, &out)
	return out, err
}

// --- commands ---

func cmdLs() error {
	comps, err := components()
	if err != nil {
		return err
	}
	for _, c := range comps {
		rt := c.Runtime
		if rt == "" {
			rt = "static"
		}
		flags := ""
		if len(c.Roles) > 0 {
			var rs []string
			for r := range c.Roles {
				rs = append(rs, r)
			}
			sort.Strings(rs)
			flags = " exposes:" + strings.Join(rs, ",")
		}
		if c.ManifestErr != "" {
			flags += " MANIFEST-ERROR"
		}
		fmt.Printf("%-40s %-8s%s\n", c.Path, rt, flags)
	}
	return nil
}

func cmdStatus() error {
	var backends map[string]any
	if err := apiJSON("GET", "/api/buxon/backends", nil, &backends); err != nil {
		return err
	}
	var status map[string]any
	if err := apiJSON("GET", "/api/buxon/status", nil, &status); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]any{"backends": backends, "status": status}, "", "  ")
	fmt.Println(string(b))
	return nil
}

func cmdLogs(args []string) error {
	follow := false
	var comp string
	for _, a := range args {
		if a == "-f" {
			follow = true
		} else {
			comp = a
		}
	}
	if comp == "" {
		return fmt.Errorf("usage: bx logs [-f] <component>")
	}
	ws := workspaceRoot()
	if ws == "" {
		return fmt.Errorf("not inside a buxon workspace")
	}
	path := filepath.Join(ws, ".buxon", "log", util.CompKey(comp)+".log")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no logs for %s yet (%s)", comp, path)
	}
	defer f.Close()
	if fi, _ := f.Stat(); fi != nil && fi.Size() > 64<<10 && follow {
		_, _ = f.Seek(-64<<10, io.SeekEnd)
	}
	if _, err := io.Copy(os.Stdout, f); err != nil {
		return err
	}
	for follow {
		time.Sleep(300 * time.Millisecond)
		if _, err := io.Copy(os.Stdout, f); err != nil {
			return err
		}
	}
	return nil
}

func cmdAPI(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx api <component>")
	}
	var out struct {
		Component compInfo `json:"component"`
		APIDoc    string   `json:"apiDoc"`
	}
	if err := apiJSON("GET", "/api/buxon/components/"+args[0], nil, &out); err != nil {
		return err
	}
	c := out.Component
	fmt.Printf("component: %s   runtime: %s   scope: %s\n", c.Path, orStatic(c.Runtime), orDash(c.Scope))
	if len(c.Roles) > 0 {
		fmt.Println("\nroles:")
		var rs []string
		for r := range c.Roles {
			rs = append(rs, r)
		}
		sort.Strings(rs)
		for _, r := range rs {
			fmt.Printf("  %-8s %s\n", r, c.Roles[r])
		}
	}
	if out.APIDoc != "" {
		fmt.Println("\n" + strings.TrimSpace(out.APIDoc))
	} else if len(c.Roles) > 0 {
		fmt.Println("\n(no API.md — the author should add one; see /docs/elements.md)")
	}
	return nil
}

func cmdGrants() error {
	var out struct {
		Grants  []map[string]string `json:"grants"`
		Pending []map[string]string `json:"pending"`
	}
	if err := apiJSON("GET", "/api/buxon/grants", nil, &out); err != nil {
		return err
	}
	fmt.Println("granted:")
	for _, g := range out.Grants {
		fmt.Printf("  %-30s → %s : %s\n", g["from"], g["target"], g["role"])
	}
	if len(out.Pending) > 0 {
		fmt.Println("pending (approve with bx grant <caller> <target>:<role>):")
		for _, g := range out.Pending {
			fmt.Printf("  %-30s → %s : %s\n", g["from"], g["target"], g["role"])
		}
	}
	return nil
}

func cmdGrant(args []string) error {
	revoke := false
	var rest []string
	for _, a := range args {
		if a == "--revoke" {
			revoke = true
		} else {
			rest = append(rest, a)
		}
	}
	// Split on the LAST colon: targets may be "res:apps/calendar/bus".
	i := -1
	if len(rest) == 2 {
		i = strings.LastIndex(rest[1], ":")
	}
	if i <= 0 || i == len(rest[1])-1 {
		return fmt.Errorf("usage: bx grant [--revoke] <caller> <target>:<role>")
	}
	body := map[string]string{"from": rest[0], "target": rest[1][:i], "role": rest[1][i+1:]}
	method := "POST"
	if revoke {
		method = "DELETE"
	}
	if err := apiJSON(method, "/api/buxon/grants", body, nil); err != nil {
		return err
	}
	fmt.Println("ok")
	return nil
}

func cmdVault(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: bx vault status|unseal|seal | ls|get|set|rm <component> [key] [value]")
	}
	// Barrier lifecycle ops take no component.
	switch args[0] {
	case "status":
		var st struct {
			Initialized bool `json:"initialized"`
			Sealed      bool `json:"sealed"`
			Insecure    bool `json:"insecure"`
		}
		if err := apiJSON("GET", "/api/buxon/vault-status", nil, &st); err != nil {
			return err
		}
		switch {
		case st.Insecure:
			fmt.Println("insecure: NO encryption at rest (plaintext on disk). Set BUXON_VAULT_PASSPHRASE to enable the barrier.")
		case st.Sealed:
			fmt.Println("sealed: encrypted, locked. Run: bx vault unseal")
		default:
			fmt.Println("unsealed: encryption at rest active")
		}
		return nil
	case "unseal":
		pass, err := readPassphrase("vault passphrase (creates the barrier on first use): ")
		if err != nil {
			return err
		}
		var out struct {
			Created bool `json:"created"`
		}
		if err := apiJSON("POST", "/api/buxon/vault-unseal", map[string]string{"passphrase": pass}, &out); err != nil {
			return err
		}
		if out.Created {
			fmt.Println("vault barrier created and unsealed — existing secrets encrypted. Keep this passphrase safe: it cannot be recovered.")
		} else {
			fmt.Println("vault unsealed")
		}
		return nil
	case "seal":
		if err := apiJSON("POST", "/api/buxon/vault-seal", map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Println("vault sealed")
		return nil
	}

	if len(args) < 2 {
		return fmt.Errorf("usage: bx vault status|unseal|seal | ls|get|set|rm <component> [key] [value]")
	}
	op, comp := args[0], args[1]
	switch op {
	case "ls":
		var out struct {
			Keys []string `json:"keys"`
		}
		if err := apiJSON("GET", "/api/buxon/vault/"+comp, nil, &out); err != nil {
			return err
		}
		for _, k := range out.Keys {
			fmt.Println(k)
		}
	case "get":
		if len(args) < 3 {
			return fmt.Errorf("usage: bx vault get <component> <key>")
		}
		var out struct {
			Value string `json:"value"`
		}
		if err := apiJSON("GET", "/api/buxon/vault/"+comp+"/"+args[2], nil, &out); err != nil {
			return err
		}
		fmt.Println(out.Value)
	case "set":
		if len(args) < 3 {
			return fmt.Errorf("usage: bx vault set <component> <key> [value] (reads stdin if omitted)")
		}
		val := ""
		if len(args) >= 4 {
			val = args[3]
		} else {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			val = strings.TrimRight(string(b), "\n")
		}
		if err := apiJSON("PUT", "/api/buxon/vault/"+comp+"/"+args[2],
			map[string]string{"value": val}, nil); err != nil {
			return err
		}
		fmt.Println("ok")
	case "rm":
		if len(args) < 3 {
			return fmt.Errorf("usage: bx vault rm <component> <key>")
		}
		if err := apiJSON("DELETE", "/api/buxon/vault/"+comp+"/"+args[2], nil, nil); err != nil {
			return err
		}
		fmt.Println("ok")
	default:
		return fmt.Errorf("unknown vault op %q", op)
	}
	return nil
}

func cmdCron(args []string) error {
	if len(args) < 1 || args[0] != "ls" {
		return fmt.Errorf("usage: bx cron ls")
	}
	var out struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := apiJSON("GET", "/api/buxon/cron/jobs", nil, &out); err != nil {
		return err
	}
	for _, j := range out.Jobs {
		fmt.Printf("%-24v %-16v %v%v (role %v)\n", j["name"], j["schedule"], j["component"], j["path"], j["role"])
	}
	return nil
}

func orStatic(s string) string {
	if s == "" {
		return "static"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
