package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// bx settings — the workspace settings (docs/bx.md, D175, D180): GET/PUT
// /api/xbin/workspace-settings, grouped by topic — terminals (base
// auto-update) and partitioned tiles (partition-consent,
// credential-reset-confirm; PD-55). Read: any credential (the partitioned
// tiles' switches: a person or an admin); set: admin. Against an xbind
// before D180 the partitioned tiles' switches come from, and go to, v0.3.66's
// /workspace-policies; `bx policies` is an alias of this command.

const settingsUsage = `  bx settings [ls] [--json] | set <name> on|off… | set --<name>[=true|false]…
                                        workspace settings (set: admin): base-auto-update,
                                        partition-consent, credential-reset-confirm
`

// wsSetting is one setting as bx shows it.
type wsSetting struct {
	name, key, group string
	on, off          string // what it does, on and off
}

var wsSettingsList = []wsSetting{
	{"base-auto-update", "baseAutoUpdate", "Terminals",
		"a terminal on an older base image moves to the current one at its next start (everything outside the workspace files and $HOME reset)",
		"terminals stay on their base image; the terminal window offers the update"},
	{"partition-consent", "partitionConsent", "Partitioned tiles",
		"another partitioned tile uses a person's data only once they allow it",
		"a partitioned tile with a grant on another reaches the data of everyone who can open it"},
	{"credential-reset-confirm", "credentialResetConfirm", "Partitioned tiles",
		"a sign-in link, password or SSO email an admin sets for someone who holds partitions waits for them (or 24 h after they're told)",
		"credential resets work at once"},
}

const policiesRoute = "/api/xbin/workspace-policies"

// settingsView is GET /workspace-settings, the partitioned tiles' switches
// filled in from /workspace-policies by an xbind before D180, and why a
// group is missing: groupNote, by heading.
type settingsView struct {
	view      map[string]any
	groupNote map[string]string
	unified   bool // the xbind serves every setting at /workspace-settings (D180)
}

// getStatus GETs path: its status and JSON body.
func getStatus(path string) (int, map[string]any, error) {
	resp, err := api("GET", path, nil)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if out == nil {
		out = map[string]any{}
	}
	return resp.StatusCode, out, nil
}

// errText is an error answer's message, or its status text.
func errText(body map[string]any, code int) string {
	msg, _ := body["error"].(string)
	return firstOf(msg, http.StatusText(code))
}

// missingRoute: Go's mux 404/405, no error body — an xbind older than the route.
func missingRoute(code int, body map[string]any) bool {
	return (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) && body["error"] == nil
}

func loadSettings() (settingsView, error) {
	sv := settingsView{groupNote: map[string]string{}}
	code, view, err := getStatus("/api/xbin/workspace-settings")
	switch {
	case err != nil:
		return sv, err
	case missingRoute(code, view):
		view = map[string]any{}
		sv.groupNote["Terminals"] = "this xbind has no workspace settings (it predates them); upgrade xbind"
	case code >= 400:
		return sv, fmt.Errorf("GET /api/xbin/workspace-settings: %s (%d)", errText(view, code), code)
	}
	sv.view = view
	if _, ok := view["partitionConsent"]; ok {
		sv.unified = true
		return sv, nil
	}
	// an xbind before D180 keeps them at /workspace-policies — or this
	// credential doesn't read them (tile code), which that route says
	code, pol, err := getStatus(policiesRoute)
	switch {
	case err != nil:
		return sv, err
	case missingRoute(code, pol):
		sv.groupNote["Partitioned tiles"] = "this xbind has no partitioned tiles"
	case code == http.StatusForbidden:
		sv.groupNote["Partitioned tiles"] = "only people and admins read these"
	case code >= 400:
		sv.groupNote["Partitioned tiles"] = "can't be read: " + errText(pol, code)
	default:
		for _, k := range []string{"partitionConsent", "credentialResetConfirm"} {
			view[k] = pol[k]
		}
	}
	return sv, nil
}

func (sv settingsView) print() {
	errs, _ := sv.view["errors"].(map[string]any)
	group := ""
	for _, s := range wsSettingsList {
		if s.group != group {
			group = s.group
			fmt.Println(group)
			if note := sv.groupNote[group]; note != "" {
				fmt.Printf("  (%s)\n", note)
			}
		}
		v, ok := sv.view[s.key].(bool)
		if !ok {
			continue
		}
		state, what := "off", s.off
		if v {
			state, what = "on", s.on
		}
		fmt.Printf("  %-26s %-3s  %s\n", s.name, state, what)
		why, _ := errs[s.key].(string)
		if why == "" && s.key == "baseAutoUpdate" {
			why, _ = sv.view["error"].(string) // an xbind before D180 says it here
		}
		if why != "" {
			fmt.Printf("  %-26s      (can't be read: %s)\n", "", why)
		}
	}
}

func cmdSettings(args []string) error {
	sub := "ls"
	if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "ls":
		asJSON := false
		for _, a := range args {
			if a != "--json" {
				return errors.New("usage: bx settings [ls] [--json]")
			}
			asJSON = true
		}
		sv, err := loadSettings()
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(sv.view)
		}
		sv.print()
		return nil
	case "set":
		body, err := parseSettingsSet(args)
		if err != nil {
			return err
		}
		return putSettings(body)
	}
	return fmt.Errorf("bx settings: unknown subcommand %q (ls|set)", sub)
}

// settingByName is the setting a CLI name (or its wire key) names.
func settingByName(name string) (wsSetting, bool) {
	for _, s := range wsSettingsList {
		if name == s.name || name == s.key {
			return s, true
		}
	}
	return wsSetting{}, false
}

func settingNames() string {
	var n []string
	for _, s := range wsSettingsList {
		n = append(n, s.name)
	}
	return strings.Join(n, ", ")
}

// parseState reads on|off|true|false (and what strconv.ParseBool takes).
func parseState(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%q isn't on, off, true or false", v)
	}
	return b, nil
}

// parseSettingsSet reads `set`'s arguments: `<name> <state>` pairs, and the
// flag form `--<name>[=<state>]` / `--no-<name>` (D175's
// --base-auto-update[=…] among them), mixed freely.
func parseSettingsSet(args []string) (map[string]bool, error) {
	const usage = "usage: bx settings set <name> on|off… | --<name>[=true|false]… (names: %s)"
	body := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if flag, ok := strings.CutPrefix(a, "--"); ok {
			name, val, hasVal := strings.Cut(flag, "=")
			on := true
			if rest, neg := strings.CutPrefix(name, "no-"); neg {
				if hasVal {
					return nil, fmt.Errorf("--%s takes no value", name)
				}
				name, on = rest, false
			} else if hasVal {
				v, err := parseState(val)
				if err != nil {
					return nil, fmt.Errorf("--%s: %v", name, err)
				}
				on = v
			}
			s, ok := settingByName(name)
			if !ok {
				return nil, fmt.Errorf("bx settings set: unknown flag %q (--<name>[=true|false], names: %s)", a, settingNames())
			}
			body[s.key] = on
			continue
		}
		s, ok := settingByName(a)
		if !ok {
			return nil, fmt.Errorf("bx settings set: no setting %q (%s)", a, settingNames())
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf(usage, settingNames())
		}
		on, err := parseState(args[i+1])
		if err != nil {
			return nil, fmt.Errorf("%s: %v", a, err)
		}
		body[s.key] = on
		i++
	}
	if len(body) == 0 {
		return nil, fmt.Errorf(usage, settingNames())
	}
	return body, nil
}

// putSettings sets body (admin): one PUT /workspace-settings; an xbind
// before D180 takes the partitioned tiles' switches at /workspace-policies.
func putSettings(body map[string]bool) error {
	policy := map[string]bool{}
	for _, k := range []string{"partitionConsent", "credentialResetConfirm"} {
		if v, ok := body[k]; ok {
			policy[k] = v
		}
	}
	unified := true
	if len(policy) > 0 {
		sv, err := loadSettings()
		if err != nil {
			return err
		}
		unified = sv.unified
	}
	if !unified {
		for k := range policy {
			delete(body, k)
		}
		if err := apiJSON("PUT", policiesRoute, policy, nil); err != nil {
			return err
		}
	}
	if len(body) > 0 {
		if err := apiJSON("PUT", "/api/xbin/workspace-settings", body, nil); err != nil {
			return err
		}
	}
	sv, err := loadSettings()
	if err != nil {
		return err
	}
	sv.print()
	return nil
}
