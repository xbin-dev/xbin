package broker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/limits"
)

// The delegation.Policy xbind runs with is the orgs' agent rules (D229)
// and people's agent preferences: what an org's admins set is what the
// delegate gate hears, in the org's own words; a nil preferences store
// starts nothing unasked; and without a policy at all nothing is delegated.
func TestDelegationPolicyIsTheOrgsRules(t *testing.T) {
	fx := agentFixture(t)
	ps := fx.principals()
	const holder = "xbin/wsagent"
	sub := func(tile, model string) delegation.Subject {
		return delegation.Subject{Holder: holder, User: "dave", Tile: tile, Model: model}
	}
	pol := fx.b.DelegationPolicy(nil)
	// no rules anywhere: every tile, and nothing unasked
	for _, tile := range []string{"apps/ledger", "apps/calendar", "apps/email", "apps/notes"} {
		if err := pol.TileAllows(sub(tile, "")); err != nil {
			t.Errorf("%s with no rules: %v", tile, err)
		}
	}
	if _, ok := pol.StartPrefs("dave", "apps/ledger", "claude"); ok {
		t.Error("a start unasked, without a preferences store")
	}
	// finance turns the workspace agent off: its own tile and the one shared into it
	if code, body := fx.agents(ps["dave"], "PUT", "/orgs/finance/agents", `{"workspaceAgent":"off"}`, map[string]string{"org": "finance"}); code != 200 {
		t.Fatalf("finance: %d %s", code, body)
	}
	for tile, want := range map[string]string{"apps/ledger": "Finance has the workspace agent turned off", "apps/calendar": "Finance has the workspace agent turned off", "apps/email": "", "apps/notes": ""} {
		err := pol.TileAllows(sub(tile, ""))
		if (want == "") != (err == nil) || err != nil && !strings.Contains(err.Error(), want) {
			t.Errorf("%s with finance off: %v, want %q", tile, err, want)
		}
		if err := pol.SessionAllows(sub(tile, ""), "claude"); (want == "") != (err == nil) {
			t.Errorf("a session on %s with finance off: %v", tile, err)
		}
	}
	// a harness list, and a model rule the agent's stated model must meet
	if code, body := fx.agents(ps["dave"], "PUT", "/orgs/finance/agents", `{"workspaceAgent":"on","harnesses":["claude"],"models":["local"]}`, map[string]string{"org": "finance"}); code != 200 {
		t.Fatalf("finance: %d %s", code, body)
	}
	if err := pol.SessionAllows(sub("apps/ledger", "local/llama"), "codex"); err == nil || !strings.Contains(err.Error(), "claude (not codex)") {
		t.Errorf("codex on a finance tile: %v", err)
	}
	if err := pol.TileAllows(sub("apps/ledger", "hosted/big")); err == nil {
		t.Error("a finance tile on a model finance doesn't list")
	}
	if err := pol.TileAllows(sub("apps/ledger", "")); err == nil {
		t.Error("a finance tile with no model said")
	}
	if err := pol.TileAllows(sub("apps/ledger", "local/llama")); err != nil {
		t.Errorf("a finance tile on a listed model: %v", err)
	}
	// people's preferences are the store's answer, passed through
	asked := ""
	with := fx.b.DelegationPolicy(func(user, harness string) (string, string, bool, bool) {
		asked = user + " " + harness
		return "plan", "internet", false, harness == "claude"
	})
	if sp, ok := with.StartPrefs("dave", "apps/notes", "claude"); !ok || sp != (delegation.StartPrefs{Mode: "plan", Net: "internet"}) || asked != "dave claude" {
		t.Errorf("start preferences: %+v %v (asked %q)", sp, ok, asked)
	}
	if sp, ok := with.StartPrefs("dave", "apps/notes", "codex"); ok || sp != (delegation.StartPrefs{}) {
		t.Errorf("a harness the person didn't allow: %+v %v", sp, ok)
	}
	// no policy configured: closed
	for name, p := range map[string]delegation.Policy{"none installed": delegation.PolicyOr(nil), "an adapter over nothing": orgAgentRules{}} {
		if p.TileAllows(sub("apps/notes", "")) == nil || p.SessionAllows(sub("apps/notes", ""), "claude") == nil {
			t.Errorf("%s: a tile is allowed", name)
		}
		if _, ok := p.StartPrefs("dave", "apps/notes", "claude"); ok {
			t.Errorf("%s: a session starts unasked", name)
		}
	}
}

// GET /alerts and GET /screens for a person's workspace agent: as the
// person reads them, without the tiles whose org keeps the agent out — and
// without any tile row when the filter isn't installed. The person's own
// reads are untouched.
func TestDelegateListingsLeaveOutBlockedTiles(t *testing.T) {
	fx := agentFixture(t)
	ps := fx.principals()
	b := fx.b
	pol := b.DelegationPolicy(nil)
	hides := func(p auth.Principal, tile string) bool { // the server's DelegateHides
		return p.Agent != "" && pol.TileAllows(delegation.Subject{Holder: p.Agent, User: p.UserID, Tile: tile, Model: p.AgentModel}) != nil
	}
	b.delegateHides = hides
	if code, body := fx.agents(ps["dave"], "PUT", "/orgs/finance/agents", `{"workspaceAgent":"off"}`, map[string]string{"org": "finance"}); code != 200 {
		t.Fatalf("finance: %d %s", code, body)
	}
	dave := principalFor(t, fx.st, "dave")
	agent := dave
	agent.Via, agent.Agent = "delegate", "xbin/wsagent"

	alerts := []Alert{
		{Kind: "disk-low", System: true, Message: "the disk is low"},
		{Kind: "quota", Tile: "apps/ledger", Message: "ledger is over quota"},
		{Kind: "quota", Tile: "apps/email", Message: "email is over quota"},
		{Kind: "memory", Action: &limits.Action{Tile: "apps/calendar"}, Message: "calendar hit its memory cap"},
		{Kind: "odd", Message: "about something, and it doesn't say what"},
	}
	kinds := func(as []Alert) string {
		var out []string
		for _, a := range as {
			out = append(out, a.Kind+":"+a.Tile)
		}
		return strings.Join(out, " ")
	}
	if got := kinds(b.agentAlerts(agent, alerts)); got != "disk-low: quota:apps/email" {
		t.Errorf("the agent's alerts: %q", got)
	}
	if got := b.agentAlerts(dave, alerts); len(got) != len(alerts) {
		t.Errorf("dave's own alerts: %q", kinds(got))
	}
	if w := call(t, b.apiAlerts, agent, "GET", "/alerts", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"alerts"`) {
		t.Errorf("GET /alerts as the agent: %d %s", w.Code, w.Body.String())
	}

	// screens: the default screen, an org's, and the sidebar folders
	if w := call(t, b.apiScreensDefaultPut, ps["root"], "PUT", "/screens/default", `{"tiles":[{"path":"apps/ledger","x":1},{"path":"apps/email","x":2},{"note":"no path"}],"mode":"doc"}`, nil); w.Code != 200 {
		t.Fatalf("the default screen: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiScreensOrgPut, ps["root"], "PUT", "/screens/org", `{"org":"finance","name":"Books","edit":"admins","tiles":[{"path":"apps/ledger"},{"path":"apps/calendar"},{"path":"apps/notes"}]}`, nil); w.Code != 200 {
		t.Fatalf("an org screen: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiScreensFoldersPut, ps["root"], "PUT", "/screens/folders", `{"scope":"ws","folders":[{"id":"f1","name":"Money","items":["apps/ledger","apps/email",7]}],"rev":0}`, nil); w.Code != 200 {
		t.Fatalf("folders: %d %s", w.Code, w.Body.String())
	}
	read := func(p auth.Principal) (def, org, folders string) {
		t.Helper()
		w := call(t, b.apiScreensGet, p, "GET", "/screens", "", nil)
		var out struct {
			Default json.RawMessage `json:"default"`
			Org     []struct {
				Tiles json.RawMessage `json:"tiles"`
			} `json:"org"`
			Folders map[string]struct {
				Folders json.RawMessage `json:"folders"`
			} `json:"folders"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Org) != 1 {
			t.Fatalf("GET /screens: %d %s", w.Code, w.Body.String())
		}
		return string(out.Default), string(out.Org[0].Tiles), string(out.Folders["ws"].Folders)
	}
	def, org, folders := read(agent)
	if def != `{"mode":"doc","tiles":[{"path":"apps/email","x":2}]}` || org != `[{"path":"apps/notes"}]` || folders != `[{"id":"f1","items":["apps/email"],"name":"Money"}]` {
		t.Errorf("the agent's screens:\n default %s\n org %s\n folders %s", def, org, folders)
	}
	def, org, folders = read(dave)
	if !strings.Contains(def, "apps/ledger") || !strings.Contains(def, "no path") || !strings.Contains(org, "apps/ledger") || !strings.Contains(folders, `"apps/ledger","apps/email",7`) {
		t.Errorf("dave's own screens changed:\n default %s\n org %s\n folders %s", def, org, folders)
	}
	// the filter not installed: an agent is shown no tile at all
	b.delegateHides = nil
	def, org, folders = read(agent)
	if strings.Contains(def+org+folders, "apps/") {
		t.Errorf("without the filter, the agent's screens name tiles:\n default %s\n org %s\n folders %s", def, org, folders)
	}
	if got := kinds(b.agentAlerts(agent, alerts)); got != "disk-low:" {
		t.Errorf("without the filter, the agent's alerts: %q", got)
	}
}
