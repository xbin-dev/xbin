package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/xbin-dev/xbin/internal/limits"
)

// bx limits — tiles' resource limits (D203, docs/bx.md "Tile limits",
// docs/isolation.md §Resource limits): what each tile may use, what its
// manifest asks above the defaults, which tiles met a cap, and an
// approver's decisions — approve an ask, raise a cap, set a value, put one
// back to the default. bx org limits sets an org's per-tile ceilings, the
// most its admins may approve (workspace admins).
//
// Kinds by bx name: memory, disk (the scope's quota), vm-memory, vm-vcpus,
// sandbox-memory, sandbox-vcpus, sandbox-disk — or the API's own names
// (memMiB, …). Sizes are binary: 512M, 4G, 1.5T. Against an xbind without
// tile limits every command exits 6.

const limitsUsage = `  bx limits [ls] [--pending] [--at-cap] [--json]
                                        tiles' resource limits: raised, pending, at cap
  bx limits show <tile> [--json]        each limit: effective, default, decisions, ask, usage, choices
  bx limits approve <tile> [<kind>…]    approve the manifest's pending asks
  bx limits raise <tile> <kind> [+25%|+50%|+100%|<size>] [--restart] [--force]
  bx limits set <tile> <kind> <size> [--force] | reset <tile> <kind> | dismiss <tile> <kind>
                                        kinds: memory disk vm-memory vm-vcpus sandbox-memory
                                        sandbox-vcpus sandbox-disk; --scope <s> for a scope's disk
  bx org limits <org> [--set memory=8G,disk=500G,…] [--rm <kind>]
                                        an org's per-tile ceilings (workspace admins)
`

const noLimitsMsg = "this xbind has no tile limits (no /api/xbin/limits); upgrade xbind"

func init() {
	moreCmds["limits"] = dcCommand(cmdLimits)
	dcUsage["limits"] = "bx limits [ls] | show <tile> | approve <tile> [<kind>…] | raise <tile> <kind> [+50%|<size>] | set <tile> <kind> <size> | reset <tile> <kind> | dismiss <tile> <kind>"
	dcUsage["org limits"] = "bx org limits <org> [--set memory=8G,disk=500G,…] [--rm <kind>]"
}

// limitsArgs is one bx limits (or bx org limits) line: positionals, and
// flags anywhere between them.
type limitsArgs struct {
	pos                                  []string
	scope                                string
	json, pending, atCap, restart, force bool
	set, rm                              []string
}

// parseLimitsArgs reads args; allowed are the flags the subcommand takes.
func parseLimitsArgs(cmd string, args []string, allowed ...string) (limitsArgs, error) {
	var a limitsArgs
	ok := map[string]bool{}
	for _, f := range allowed {
		ok[f] = true
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			a.pos = append(a.pos, args[i+1:]...)
			break
		}
		if !isFlag(arg) {
			a.pos = append(a.pos, arg)
			continue
		}
		name, val, inline := strings.Cut(arg, "=")
		if !ok[name] {
			return a, usageError(cmd, "unknown flag %s", name)
		}
		switch name {
		case "--scope", "--set", "--rm":
			if !inline {
				v, err := nextArg(args, &i)
				if err != nil {
					return a, usageError(cmd, "%v", err)
				}
				val = v
			}
			switch name {
			case "--scope":
				a.scope = strings.Trim(val, "/")
			case "--set":
				a.set = append(a.set, splitList(val)...)
			case "--rm":
				a.rm = append(a.rm, splitList(val)...)
			}
			continue
		}
		if inline {
			return a, usageError(cmd, "%s takes no value", name)
		}
		switch name {
		case "--json":
			a.json = true
		case "--pending":
			a.pending = true
		case "--at-cap":
			a.atCap = true
		case "--restart":
			a.restart = true
		case "--force":
			a.force = true
		}
	}
	return a, nil
}

// limitsTarget is what a command acts on: a tile, or (--scope) a scope.
type limitsTarget struct{ tile, scope string }

func (t limitsTarget) query() string {
	if t.scope != "" {
		return "scope=" + url.QueryEscape(t.scope)
	}
	return "tile=" + url.QueryEscape(t.tile)
}

func (t limitsTarget) body(m map[string]any) map[string]any {
	if t.scope != "" {
		m["scope"] = t.scope
	} else {
		m["tile"] = t.tile
	}
	return m
}

func (t limitsTarget) String() string {
	if t.scope != "" {
		return "scope " + t.scope
	}
	return t.tile
}

// rowTile is the tile whose row holds t's limits: the tile, or the scope's
// root tile (its path; "" for the workspace scope).
func (t limitsTarget) rowTile() string {
	if t.scope != "" && t.scope != "workspace" {
		return strings.ReplaceAll(t.scope, "~", "/")
	}
	return t.tile
}

// target splits a command's positionals: the tile first (none with
// --scope), then want more.
func (a limitsArgs) target(cmd string, min, maxN int) (limitsTarget, []string, error) {
	pos := a.pos
	t := limitsTarget{scope: a.scope}
	if a.scope == "" {
		if len(pos) == 0 {
			return t, nil, usageError(cmd, "name the tile (or --scope <scope> for a scope's disk)")
		}
		t.tile, pos = strings.Trim(pos[0], "/"), pos[1:]
	}
	if len(pos) < min || len(pos) > maxN {
		return t, nil, usageError(cmd, "wrong number of arguments: %s", strings.Join(a.pos, " "))
	}
	return t, pos, nil
}

// parseLimitKind reads a kind by its bx name or API name.
func parseLimitKind(cmd, s string) (limits.Kind, error) {
	if k, ok := limits.Parse(s); ok {
		return k, nil
	}
	return "", usageError(cmd, "%q: a kind — memory, disk, vm-memory, vm-vcpus, sandbox-memory, sandbox-vcpus, sandbox-disk", s)
}

// raiseValue is what "raise" asks for: a percentage of current (+25%, +50%
// — the default — +100%), or a size.
func raiseValue(k limits.Kind, current int64, s string) (int64, error) {
	if s == "" {
		s = "+" + strconv.Itoa(limits.DefaultPct) + "%"
	}
	if p, ok := strings.CutSuffix(strings.TrimPrefix(s, "+"), "%"); ok {
		pct, err := strconv.Atoi(p)
		if err != nil || pct <= 0 || pct > 1000 {
			return 0, fmt.Errorf("%q: a raise like +25%%, +50%% or +100%%", s)
		}
		if current <= 0 {
			return 0, errors.New("the current limit isn't known: give a size")
		}
		return limits.Step(k, current, pct), nil
	}
	return limits.ParseValue(k, s)
}

// ---- the wire ----

// limitsCall is one request of bx limits: an xbind without the route is
// exit 6, in this command's own words.
func limitsCall(method, path string, body any) ([]byte, error) {
	b, err := dcCall(method, path, body)
	var e *dcError
	if errors.As(err, &e) && e.code == exitNoDeployments && e.msg == oldXbindMsg {
		e.msg = noLimitsMsg
	}
	return b, err
}

// limitKindJSON mirrors a row's kind (GET /limits).
type limitKindJSON struct {
	Effective, Default, Max int64
	Enforced, Covered       bool
	Ask                     int64
	Pending, Dormant        bool
	Set, Approved           *limits.Decision
	Usage, Applied          int64
	AppliesAt               string
	Hit                     *limits.Hit
	Note                    string
	Approvers               []string
	CanDecide, CanRevert    bool
	Choices                 []limits.Choice
}

// limitRowJSON mirrors a row.
type limitRowJSON struct {
	Tile, Scope, ScopeKey, Org string
	RootsScope                 bool
	Limits                     map[limits.Kind]limitKindJSON
}

func getLimitRow(tile string) (limitRowJSON, []byte, error) {
	var out struct{ Tile limitRowJSON }
	b, err := limitsCall("GET", "/api/xbin/limits?tile="+url.QueryEscape(tile), nil)
	if err == nil {
		err = decodeAnswer(b, &out)
	}
	return out.Tile, b, err
}

// ---- the commands ----

func cmdLimits(args []string) error {
	sub := "ls"
	if len(args) > 0 && !isFlag(args[0]) {
		sub, args = args[0], args[1:]
	}
	cmd := "limits"
	switch sub {
	case "ls":
		a, err := parseLimitsArgs(cmd, args, "--pending", "--at-cap", "--json")
		if err != nil {
			return err
		}
		if len(a.pos) > 0 {
			return usageError(cmd, "bx limits ls takes no arguments")
		}
		return limitsList(a)
	case "show":
		a, err := parseLimitsArgs(cmd, args, "--json", "--scope")
		if err != nil {
			return err
		}
		t, _, err := a.target(cmd, 0, 0)
		if err != nil {
			return err
		}
		return limitsShow(t, a.json)
	case "approve":
		a, err := parseLimitsArgs(cmd, args, "--scope")
		if err != nil {
			return err
		}
		t, kinds, err := a.target(cmd, 0, len(limits.Kinds))
		if err != nil {
			return err
		}
		return limitsApprove(t, kinds)
	case "raise", "set":
		a, err := parseLimitsArgs(cmd, args, "--scope", "--restart", "--force")
		if err != nil {
			return err
		}
		lo := 1
		if sub == "set" {
			lo = 2
		}
		t, rest, err := a.target(cmd, lo, 2)
		if err != nil {
			return err
		}
		k, err := parseLimitKind(cmd, rest[0])
		if err != nil {
			return err
		}
		val := ""
		if len(rest) == 2 {
			val = rest[1]
		}
		return limitsDecide(sub, t, k, val, a.restart, a.force)
	case "reset", "dismiss":
		a, err := parseLimitsArgs(cmd, args, "--scope")
		if err != nil {
			return err
		}
		t, rest, err := a.target(cmd, 1, 1)
		if err != nil {
			return err
		}
		k, err := parseLimitKind(cmd, rest[0])
		if err != nil {
			return err
		}
		if sub == "reset" {
			b, err := limitsCall("DELETE", "/api/xbin/limits?"+t.query()+"&kind="+string(k), nil)
			if err != nil {
				return err
			}
			var out struct{ Tile limitRowJSON }
			if err := decodeAnswer(b, &out); err != nil {
				return err
			}
			fmt.Fprintf(dcOut, "%s %s: back to the default (%s)\n", t, k.Meta().Alias, limits.Format(k, out.Tile.Limits[k].Default))
			return nil
		}
		if _, err := limitsCall("POST", "/api/xbin/limits/dismiss?"+t.query()+"&kind="+string(k), nil); err != nil {
			return err
		}
		fmt.Fprintf(dcOut, "%s %s: the cap hit is dismissed for everyone\n", t, k.Meta().Alias)
		return nil
	}
	return usageError(cmd, "unknown subcommand %q (ls|show|approve|raise|set|reset|dismiss)", sub)
}

func limitsList(a limitsArgs) error {
	b, err := limitsCall("GET", "/api/xbin/limits", nil)
	if err != nil {
		return err
	}
	if a.json {
		printRaw(dcOut, b)
		return nil
	}
	var out struct{ Tiles []limitRowJSON }
	if err := decodeAnswer(b, &out); err != nil {
		return err
	}
	n := 0
	for _, row := range out.Tiles {
		for _, k := range limits.Kinds {
			lk, ok := row.Limits[k]
			if !ok {
				continue
			}
			raised := lk.Set != nil || lk.Approved != nil
			switch {
			case a.pending || a.atCap:
				if !(a.pending && lk.Pending || a.atCap && lk.Hit != nil) {
					continue
				}
			case !raised && !lk.Pending && lk.Hit == nil:
				continue
			}
			n++
			fmt.Fprintf(dcOut, "%-32s %-15s %s\n", row.Tile, k.Meta().Alias, limitSummary(k, lk))
		}
	}
	if n == 0 {
		fmt.Fprintln(dcOut, "no tile you can see has a raised, pending or at-cap limit (bx limits show <tile> for one tile's)")
	}
	return nil
}

// limitSummary is a kind on one line.
func limitSummary(k limits.Kind, lk limitKindJSON) string {
	parts := []string{limits.Format(k, lk.Effective) + " (default " + limits.Format(k, lk.Default) + ")"}
	if d := lk.Set; d != nil {
		parts = append(parts, fmt.Sprintf("%s by %s", sourceWord(d.Source), d.By))
	}
	if d := lk.Approved; d != nil {
		word := "ask approved"
		if lk.Dormant {
			word = "ask approved (dormant: the manifest no longer asks)"
		}
		parts = append(parts, fmt.Sprintf("%s by %s", word, d.By))
	}
	if lk.Pending {
		parts = append(parts, "asks "+limits.Format(k, lk.Ask)+": pending")
	}
	if h := lk.Hit; h != nil {
		parts = append(parts, fmt.Sprintf("AT CAP %d× since %s", h.Count, h.Since.UTC().Format("Jan 2 15:04 UTC")))
	}
	if lk.Note != "" {
		parts = append(parts, lk.Note)
	}
	return strings.Join(parts, " · ")
}

func sourceWord(s string) string {
	switch s {
	case limits.SourceRaise:
		return "raised"
	case limits.SourceSet:
		return "set"
	}
	return s
}

func limitsShow(t limitsTarget, asJSON bool) error {
	if t.rowTile() == "" {
		return &dcError{code: exitUsage, msg: "the workspace scope has no tile row: bx limits ls lists what you may see"}
	}
	row, b, err := getLimitRow(t.rowTile())
	if err != nil {
		return err
	}
	if asJSON {
		printRaw(dcOut, b)
		return nil
	}
	head := row.Tile
	if row.Scope != "" {
		head += "  scope " + row.Scope
		if row.RootsScope {
			head += " (roots it)"
		}
	}
	if row.Org != "" {
		head += "  org " + row.Org
	}
	fmt.Fprintln(dcOut, head)
	if len(row.Limits) == 0 {
		fmt.Fprintln(dcOut, "  no limit applies to it")
	}
	for _, k := range limits.Kinds {
		lk, ok := row.Limits[k]
		if !ok {
			continue
		}
		fmt.Fprintf(dcOut, "  %-15s %s\n", k.Meta().Alias, limitSummary(k, lk))
		if lk.Usage > 0 {
			fmt.Fprintf(dcOut, "  %-15s uses %s\n", "", limits.Format(k, lk.Usage))
		}
		if lk.Applied > 0 {
			fmt.Fprintf(dcOut, "  %-15s %s runs now; applies at the backend's next start (--restart: now)\n", "", limits.Format(k, lk.Applied))
		}
		if lk.Hit != nil && lk.Hit.Detail != "" {
			fmt.Fprintf(dcOut, "  %-15s %s\n", "", lk.Hit.Detail)
		}
		var ch []string
		for _, c := range lk.Choices {
			s := fmt.Sprintf("+%d%% %s", c.Pct, limits.Format(k, c.Value))
			switch {
			case !c.Fits || !c.Approvable:
				s += " (" + c.Why + ")"
			case c.Default:
				s += " (default)"
			}
			ch = append(ch, s)
		}
		if len(ch) > 0 {
			fmt.Fprintf(dcOut, "  %-15s raise: %s\n", "", strings.Join(ch, ", "))
		} else if len(lk.Approvers) > 0 && !lk.CanDecide {
			fmt.Fprintf(dcOut, "  %-15s approved by: %s\n", "", strings.Join(lk.Approvers, ", "))
		}
	}
	return nil
}

func limitsApprove(t limitsTarget, names []string) error {
	if t.rowTile() == "" {
		return &dcError{code: exitUsage, msg: "the workspace scope asks nothing: only a tile's manifest does"}
	}
	row, _, err := getLimitRow(t.rowTile())
	if err != nil {
		return err
	}
	var kinds []limits.Kind
	for _, n := range names {
		k, err := parseLimitKind("limits", n)
		if err != nil {
			return err
		}
		kinds = append(kinds, k)
	}
	if len(kinds) == 0 {
		for _, k := range limits.Kinds {
			if row.Limits[k].Pending {
				kinds = append(kinds, k)
			}
		}
		if len(kinds) == 0 {
			fmt.Fprintf(dcOut, "%s: nothing pending\n", row.Tile)
			return nil
		}
	}
	for _, k := range kinds {
		lk := row.Limits[k]
		if !lk.Pending {
			return &dcError{code: exitFailed, msg: fmt.Sprintf("%s %s: no pending ask", row.Tile, k.Meta().Alias)}
		}
		v := lk.Ask
		if lk.Max > 0 {
			v = min(v, lk.Max)
		}
		if err := postLimit(t, k, v, limits.SourceAsk, false, false); err != nil {
			return err
		}
	}
	return nil
}

func limitsDecide(sub string, t limitsTarget, k limits.Kind, val string, restart, force bool) error {
	var v int64
	var err error
	if sub == "set" {
		if v, err = limits.ParseValue(k, val); err != nil {
			return usageError("limits", "%v", err)
		}
	} else {
		current := int64(0)
		if t.rowTile() != "" && (val == "" || strings.HasSuffix(val, "%")) {
			row, _, err := getLimitRow(t.rowTile())
			if err != nil {
				return err
			}
			lk, ok := row.Limits[k]
			if !ok {
				return &dcError{code: exitFailed, msg: fmt.Sprintf("%s has no %s limit", t, k.Meta().Label)}
			}
			current = lk.Effective
		}
		if v, err = raiseValue(k, current, val); err != nil {
			return usageError("limits", "%v", err)
		}
	}
	source := limits.SourceRaise
	if sub == "set" {
		source = limits.SourceSet
	}
	return postLimit(t, k, v, source, restart, force)
}

func postLimit(t limitsTarget, k limits.Kind, v int64, source string, restart, force bool) error {
	body := t.body(map[string]any{"kind": string(k), "value": v, "source": source})
	if restart {
		body["restart"] = true
	}
	if force {
		body["force"] = true
	}
	b, err := limitsCall("POST", "/api/xbin/limits", body)
	if err != nil {
		return err
	}
	var out struct{ Tile limitRowJSON }
	if err := decodeAnswer(b, &out); err != nil {
		return err
	}
	lk := out.Tile.Limits[k]
	fmt.Fprintf(dcOut, "%s %s: %s\n", t, k.Meta().Alias, limitSummary(k, lk))
	if lk.AppliesAt == "next-start" && !restart {
		fmt.Fprintf(dcOut, "applies at the backend's next start — bx limits raise %s %s %s --restart restarts it now\n",
			t.tile, k.Meta().Alias, limits.Format(k, v))
	}
	return nil
}

// ---- bx org limits ----

func cmdOrgLimits(args []string) error {
	cmd := "org limits"
	a, err := parseLimitsArgs(cmd, args, "--set", "--rm", "--json")
	if err != nil {
		return err
	}
	if len(a.pos) != 1 {
		return usageError(cmd, "name the org")
	}
	org := a.pos[0]
	if len(a.set) == 0 && len(a.rm) == 0 {
		b, err := limitsCall("GET", "/api/xbin/orgs", nil)
		if err != nil {
			return err
		}
		var out struct {
			Orgs []struct {
				ID     string
				Limits map[limits.Kind]int64
			}
		}
		if err := decodeAnswer(b, &out); err != nil {
			return err
		}
		for _, o := range out.Orgs {
			if o.ID != org {
				continue
			}
			if a.json {
				bs, _ := json.Marshal(map[string]any{"limits": o.Limits})
				printRaw(dcOut, bs)
				return nil
			}
			if len(o.Limits) == 0 {
				fmt.Fprintf(dcOut, "%s: no ceilings — its admins can't raise a tile above the defaults\n", org)
			}
			for _, k := range sortedLimitKinds(o.Limits) {
				fmt.Fprintf(dcOut, "%-15s %s per tile\n", k.Meta().Alias, limits.Format(k, o.Limits[k]))
			}
			return nil
		}
		return &dcError{code: exitFailed, msg: "no such org (or you don't manage it): " + org}
	}
	body, err := orgLimitsBody(a.set, a.rm)
	if err != nil {
		return usageError(cmd, "%v", err)
	}
	b, err := limitsCall("PUT", "/api/xbin/orgs/"+url.PathEscape(org)+"/limits", body)
	if err != nil {
		return err
	}
	if a.json {
		printRaw(dcOut, b)
		return nil
	}
	var out struct{ Limits map[limits.Kind]int64 }
	if err := decodeAnswer(b, &out); err != nil {
		return err
	}
	fmt.Fprintf(dcOut, "%s's per-tile ceilings:", org)
	if len(out.Limits) == 0 {
		fmt.Fprint(dcOut, " none")
	}
	for _, k := range sortedLimitKinds(out.Limits) {
		fmt.Fprintf(dcOut, " %s=%s", k.Meta().Alias, limits.Format(k, out.Limits[k]))
	}
	fmt.Fprintln(dcOut)
	return nil
}

// orgLimitsBody is PUT /orgs/{org}/limits' body from --set kind=value,…
// and --rm kind.
func orgLimitsBody(set, rm []string) (map[string]any, error) {
	body := map[string]any{}
	for _, kv := range set {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			return nil, fmt.Errorf("--set %q: kind=size, like memory=8G", kv)
		}
		k, kok := limits.Parse(strings.TrimSpace(name))
		if !kok {
			return nil, fmt.Errorf("--set %q: %q isn't a kind (memory, disk, vm-memory, vm-vcpus, sandbox-memory, sandbox-vcpus, sandbox-disk)", kv, name)
		}
		v, err := limits.ParseValue(k, val)
		if err != nil {
			return nil, fmt.Errorf("--set %s: %v", kv, err)
		}
		body[string(k)] = v
	}
	for _, name := range rm {
		k, kok := limits.Parse(name)
		if !kok {
			return nil, fmt.Errorf("--rm %q isn't a kind", name)
		}
		body[string(k)] = nil
	}
	return body, nil
}

// sortedLimitKinds is m's kinds in display order.
func sortedLimitKinds(m map[limits.Kind]int64) []limits.Kind {
	var out []limits.Kind
	for _, k := range limits.Kinds {
		if _, ok := m[k]; ok {
			out = append(out, k)
		}
	}
	return out
}
