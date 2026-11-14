package agentpolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

func rules(t *testing.T, js string) Rules {
	t.Helper()
	var r Rules
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("%s: %v", js, err)
	}
	return r
}

// covers D229 — the wire form: "any" | "private" | a list, on | off; the
// zero rules read back explicit and narrow nothing.
func TestRulesJSON(t *testing.T) {
	b, _ := json.Marshal(Rules{})
	if string(b) != `{"workspaceAgent":"on","harnesses":"any","models":"any"}` {
		t.Fatalf("zero rules: %s", b)
	}
	r := rules(t, `{"workspaceAgent":"off","harnesses":["codex","claude","claude"],"models":"private"}`)
	b, _ = json.Marshal(r)
	if string(b) != `{"workspaceAgent":"off","harnesses":["claude","codex"],"models":"private"}` {
		t.Fatalf("round trip: %s", b)
	}
	if b, _ = json.Marshal(Rules{Harnesses: Only()}); !strings.Contains(string(b), `"harnesses":[]`) {
		t.Errorf("an empty allowlist is a list: %s", b)
	}
	if !rules(t, `{"workspaceAgent":"on","harnesses":"any","models":null}`).IsZero() {
		t.Error("explicit defaults are the zero rules")
	}
	for _, bad := range []string{`{"workspaceAgent":"maybe"}`, `{"harnesses":"some"}`, `{"models":[1]}`, `{"models":7}`} {
		var x Rules
		if json.Unmarshal([]byte(bad), &x) == nil {
			t.Errorf("%s decoded", bad)
		}
	}
}

func TestValidateAndWithin(t *testing.T) {
	known := func(id string) bool { return id == "claude" || id == "codex" }
	for js, want := range map[string]string{
		`{"harnesses":"private"}`:           `"private" is a rule for models`,
		`{"harnesses":["nope"]}`:            "no harness",
		`{"harnesses":["a/b"]}`:             "isn't a harness id",
		`{"models":["has space"]}`:          "isn't an upstream",
		`{"models":["local","or/x/y"]}`:     "",
		`{"harnesses":["claude","codex"]}`:  "",
		`{"models":"private"}`:              "",
		`{"models":["/lead"]}`:              "isn't an upstream",
		`{"harnesses":[],"models":[]}`:      "",
		`{"workspaceAgent":"off"}`:          "",
		`{"models":["a,b"]}`:                "isn't an upstream",
		`{"harnesses":["claude"],"x":true}`: "",
	} {
		err := rules(t, js).Validate(known)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", js, err, want)
		}
	}
	bounds := rules(t, `{"harnesses":["claude","codex"],"models":["local","hosted/small"]}`)
	for js, want := range map[string]string{
		`{}`:                                     "",
		`{"harnesses":["claude"]}`:               "",
		`{"harnesses":["gemini"]}`:               "gemini is outside what the workspace allows (claude, codex)",
		`{"models":"private"}`:                   "",
		`{"models":["local/llama"]}`:             "",
		`{"models":["hosted/small"]}`:            "",
		`{"models":["hosted"]}`:                  "hosted is outside",
		`{"models":["hosted/big"]}`:              "hosted/big is outside",
		`{"workspaceAgent":"off","models":[]}`:   "",
		`{"harnesses":"any","models":"any"}`:     "",
		`{"harnesses":["claude"],"models":[]}`:   "",
		`{"harnesses":["claude","opencode"]}`:    "opencode is outside",
		`{"models":["local","hosted/small"]}`:    "",
		`{"models":["localx"]}`:                  "localx is outside",
		`{"models":["hosted/small/variant"]}`:    "",
		`{"models":["hosted/smaller"]}`:          "hosted/smaller is outside",
		`{"harnesses":[]}`:                       "",
		`{"workspaceAgent":"on"}`:                "",
		`{"models":["local/a","local/b"]}`:       "",
		`{"harnesses":["codex"],"models":"any"}`: "",
	} {
		err := rules(t, js).Within(bounds)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("within %s: %v, want %q", js, err, want)
		}
	}
	if err := rules(t, `{"harnesses":["gemini"],"models":["x"]}`).Within(Rules{}); err != nil {
		t.Errorf("no bounds: %v", err)
	}
}

func TestPatch(t *testing.T) {
	var body map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"workspaceAgent":"off","models":"private","credentials":"connector"}`), &body)
	if _, err := ParsePatch(body); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Errorf("unknown key: %v", err)
	}
	p, err := ParsePatch(body, "credentials")
	if err != nil {
		t.Fatal(err)
	}
	r := Rules{Harnesses: Only("claude")}.Apply(p)
	if !r.WorkspaceAgent.IsOff() || !r.Models.IsPrivate() || r.Harnesses.String() != "claude" {
		t.Errorf("applied: %+v", r)
	}
	body = nil
	_ = json.Unmarshal([]byte(`{"workspaceAgent":null,"models":null,"harnesses":"any"}`), &body)
	p, err = ParsePatch(body)
	if err != nil {
		t.Fatal(err)
	}
	if r = r.Apply(p); !r.IsZero() {
		t.Errorf("null puts a rule back: %+v", r)
	}
}

var (
	private = &ModelRef{Upstream: "local", Model: "llama", Private: true}
	public  = &ModelRef{Upstream: "hosted", Model: "big"}
)

// covers D229 — every layer must allow; the reason names the layer and the
// rule, the way a person reads it.
func TestCheck(t *testing.T) {
	ws := Layer{}
	finance := Layer{ID: "finance", Name: "Finance", Rules: rules(t, `{"models":"private"}`)}
	eng := Layer{ID: "eng", Name: "Engineering", Rules: rules(t, `{"harnesses":["claude","codex"],"models":["hosted/big","local"]}`)}
	legal := Layer{ID: "legal", Rules: rules(t, `{"workspaceAgent":"off","harnesses":[]}`)}
	for _, c := range []struct {
		name   string
		layers []Layer
		ask    Ask
		reason string
		by     string
		rule   string
	}{
		{"no rules", []Layer{ws}, Ask{WorkspaceAgent: true}, "", "", ""},
		{"no rules, a harness", []Layer{ws}, Ask{Harness: "gemini"}, "", "", ""},
		{"private only, a public model", []Layer{ws, finance}, Ask{WorkspaceAgent: true, Model: public}, "Finance allows private models only", "finance", "models"},
		{"private only, a private model", []Layer{ws, finance}, Ask{WorkspaceAgent: true, Model: private}, "", "", ""},
		{"private only, no model", []Layer{ws, finance}, Ask{WorkspaceAgent: true}, "Finance allows private models only, and no model is chosen", "finance", "models"},
		{"a person's session ignores the model rule without a model", []Layer{ws, finance}, Ask{Harness: "claude"}, "", "", ""},
		{"a person's session with a connector model", []Layer{ws, finance}, Ask{Harness: "claude", Model: public}, "Finance allows private models only", "finance", "models"},
		{"a listed model", []Layer{ws, eng}, Ask{WorkspaceAgent: true, Model: public}, "", "", ""},
		{"a listed upstream", []Layer{ws, eng}, Ask{WorkspaceAgent: true, Model: private}, "", "", ""},
		{"an unlisted model", []Layer{ws, eng}, Ask{WorkspaceAgent: true, Model: &ModelRef{Upstream: "hosted", Model: "small"}}, "Engineering allows these models only: hosted/big, local", "eng", "models"},
		{"a listed harness", []Layer{ws, eng}, Ask{Harness: "codex"}, "", "", ""},
		{"an unlisted harness", []Layer{ws, eng}, Ask{Harness: "gemini"}, "Engineering allows these coding agents only: claude, codex (not gemini)", "eng", "harnesses"},
		{"the strictest of two orgs", []Layer{ws, eng, finance}, Ask{WorkspaceAgent: true, Model: public}, "Finance allows private models only", "finance", "models"},
		{"both orgs satisfied", []Layer{ws, eng, finance}, Ask{WorkspaceAgent: true, Model: private}, "", "", ""},
		{"agent off", []Layer{ws, legal}, Ask{WorkspaceAgent: true, Model: private}, "legal has the workspace agent turned off", "legal", "workspaceAgent"},
		{"agent off doesn't stop a person", []Layer{ws, finance, legal}, Ask{}, "", "", ""},
		{"no harness at all", []Layer{ws, legal}, Ask{Harness: "claude"}, "legal allows no coding agents", "legal", "harnesses"},
		{"the workspace's bounds", []Layer{{Rules: rules(t, `{"models":"private"}`)}, eng}, Ask{WorkspaceAgent: true, Model: public}, "This workspace allows private models only", WorkspaceID, "models"},
		{"the workspace switch", []Layer{{Rules: Rules{WorkspaceAgent: Off}}}, Ask{WorkspaceAgent: true}, "This workspace has the workspace agent turned off", WorkspaceID, "workspaceAgent"},
	} {
		d := Check(c.layers, c.ask)
		if d.Allowed != (c.reason == "") || d.Reason != c.reason || d.By != c.by || d.Rule != c.rule {
			t.Errorf("%s: %+v, want %q by %q (%s)", c.name, d, c.reason, c.by, c.rule)
		}
		if (d.Err() == nil) != d.Allowed || (d.Err() != nil && d.Err().Error() != c.reason) {
			t.Errorf("%s: Err() = %v", c.name, d.Err())
		}
	}
	if h := Harnesses([]Layer{ws, eng, {Rules: rules(t, `{"harnesses":["codex","gemini"]}`)}}); h.String() != "codex" {
		t.Errorf("harnesses together: %s", h)
	}
	if h := Harnesses([]Layer{ws, finance}); !h.IsAny() {
		t.Errorf("no harness rule: %s", h)
	}
}

func fixture(t *testing.T) Input {
	return Input{
		Orgs: []Org{
			{ID: "eng", Name: "Engineering"},
			{ID: "finance", Name: "Finance", Rules: rules(t, `{"models":"private"}`)},
			{ID: "legal", Name: "Legal", Rules: rules(t, `{"workspaceAgent":"off"}`)},
			{ID: "ops", Name: "Ops", Rules: rules(t, `{"harnesses":["codex"]}`)},
		},
		Tiles: []Tile{
			{Path: "apps/wiki", Owner: "eng", Orgs: []string{"eng"}},
			{Path: "apps/ci", Owner: "eng", Orgs: []string{"eng", "ops"}},
			{Path: "apps/ledger", Owner: "finance", Orgs: []string{"finance"}},
			{Path: "apps/forecast", Owner: "eng", Orgs: []string{"finance", "eng"}}, // shared into finance: the strictest
			{Path: "apps/contracts", Owner: "legal", Orgs: []string{"legal"}},
			{Path: "apps/notes"},                             // in no org
			{Path: "apps/shared", Orgs: []string{"finance"}}, // workspace-owned, shared into finance
		},
	}
}

func stateOf(v View, tile string) TileState {
	for _, t := range v.Tiles {
		if t.Tile == tile {
			return t
		}
	}
	return TileState{}
}

func orgOf(v View, id string) OrgState {
	for _, o := range v.Orgs {
		if o.ID == id {
			return o
		}
	}
	return OrgState{}
}

// covers D229 — the connection view: connected | hidden | blocked (with the
// reason) | no-access per org and tile, and the totals.
func TestConnection(t *testing.T) {
	in := fixture(t)
	in.Model = public
	v := Connection(in)
	for tile, want := range map[string]State{
		"apps/wiki": Connected, "apps/ci": Connected, "apps/notes": Connected,
		"apps/ledger": Blocked, "apps/forecast": Blocked, "apps/shared": Blocked, "apps/contracts": Blocked,
	} {
		if got := stateOf(v, tile); got.State != want {
			t.Errorf("%s: %s (%s), want %s", tile, got.State, got.Reason, want)
		}
	}
	if s := stateOf(v, "apps/forecast"); s.Reason != "Finance allows private models only" || s.By != "finance" || s.Org != "eng" || len(s.Orgs) != 2 {
		t.Errorf("a tile shared into a stricter org: %+v", s)
	}
	if s := stateOf(v, "apps/contracts"); s.Reason != "Legal has the workspace agent turned off" || s.Rule != "workspaceAgent" {
		t.Errorf("agent off: %+v", s)
	}
	if s := stateOf(v, "apps/ci"); s.Harnesses.String() != "codex" {
		t.Errorf("a tile's harnesses: %s", s.Harnesses)
	}
	if s := stateOf(v, "apps/shared"); s.Org != "finance" {
		t.Errorf("listed under the org it is shared into: %+v", s)
	}
	if v.Counts.Tiles != 3 || v.Counts.Orgs != 1 || v.Counts.Blocked != 4 || v.Counts.NotConnected != 4 ||
		v.Counts.Summary != "3 tiles · 1 org connected · 4 not connected" {
		t.Errorf("counts: %+v", v.Counts)
	}
	if o := orgOf(v, "finance"); o.State != Blocked || o.Reason != "Finance allows private models only" || o.Tiles != 2 || o.Connected != 0 || o.Rules == nil {
		t.Errorf("finance: %+v", o)
	}
	if o := orgOf(v, "eng"); o.State != Connected || o.Tiles != 3 || o.Connected != 2 || o.Rules != nil {
		t.Errorf("eng, partly connected: %+v", o)
	}
	if o := orgOf(v, "ops"); o.State != Connected || o.Tiles != 0 {
		t.Errorf("ops owns nothing here: %+v", o)
	}
	if o := orgOf(v, WorkspaceID); o.Kind != "workspace" || o.Tiles != 1 || o.Connected != 1 {
		t.Errorf("the tiles in no org: %+v", o)
	}
	if last := v.Orgs[len(v.Orgs)-1]; last.ID != WorkspaceID {
		t.Errorf("the no-org group is last: %+v", v.Orgs)
	}

	// a private model connects finance's tiles; legal stays off
	in.Model = private
	v = Connection(in)
	if v.Counts.Tiles != 6 || v.Counts.Orgs != 2 || v.Counts.Blocked != 1 || v.Counts.Summary != "6 tiles · 2 orgs connected · 1 not connected" {
		t.Errorf("private model: %+v", v.Counts)
	}

	// a scope: eng and one finance tile; the rest is hidden by the person —
	// except what is blocked, which says so
	in.Scope = Scope{Orgs: []string{"eng", "nope"}, Tiles: []string{"apps/ledger", "apps/secret"}}
	v = Connection(in)
	for tile, want := range map[string]State{
		"apps/wiki": Connected, "apps/ci": Connected, "apps/forecast": Connected, "apps/ledger": Connected,
		"apps/notes": Hidden, "apps/shared": Hidden, "apps/contracts": Blocked, "apps/secret": NoAccess,
	} {
		if got := stateOf(v, tile); got.State != want {
			t.Errorf("scoped %s: %s, want %s", tile, got.State, want)
		}
	}
	if o := orgOf(v, "finance"); o.State != Connected || o.Connected != 1 || o.Tiles != 2 {
		t.Errorf("an org with one tile picked: %+v", o)
	}
	if o := orgOf(v, "ops"); o.State != Hidden {
		t.Errorf("ops isn't in the scope: %+v", o)
	}
	if o := orgOf(v, WorkspaceID); o.State != Hidden {
		t.Errorf("the no-org group isn't in the scope: %+v", o)
	}
	if o := orgOf(v, "nope"); o.State != NoAccess || o.Tiles != 0 {
		t.Errorf("an org the person can't see: %+v", o)
	}
	if o := orgOf(v, "legal"); o.State != Blocked {
		t.Errorf("blocked beats hidden: %+v", o)
	}
	c := v.Counts
	if c.Tiles != 4 || c.Hidden != 2 || c.Blocked != 1 || c.NoAccess != 1 || c.NotConnected != 4 || c.Orgs != 2 {
		t.Errorf("scoped counts: %+v", c)
	}

	// the tiles in no org are picked as the workspace group
	in.Scope = Scope{Orgs: []string{WorkspaceID}}
	if v = Connection(in); stateOf(v, "apps/notes").State != Connected || stateOf(v, "apps/wiki").State != Hidden || v.Counts.Orgs != 0 ||
		v.Counts.Summary != "1 tile connected · 6 not connected" {
		t.Errorf("the workspace group: %+v", v.Counts)
	}

	// nothing set anywhere: everything connects, with no model at all
	if v = Connection(Input{Orgs: []Org{{ID: "eng", Name: "Engineering"}}, Tiles: []Tile{{Path: "a", Owner: "eng", Orgs: []string{"eng"}}, {Path: "b"}}}); v.Counts.Summary != "2 tiles · 1 org connected" {
		t.Errorf("no rules: %+v", v.Counts)
	}
	// the workspace's bounds reach every tile
	if v = Connection(Input{Bounds: rules(t, `{"models":"private"}`), Tiles: []Tile{{Path: "b"}}, Model: public}); stateOf(v, "b").Reason != "This workspace allows private models only" ||
		stateOf(v, "b").By != WorkspaceID || orgOf(v, WorkspaceID).State != Blocked {
		t.Errorf("bounds: %+v", v)
	}
}

// covers D229 — a model change says what it would connect and disconnect
// before it applies.
func TestWhatIf(t *testing.T) {
	in := fixture(t)
	in.Model = public
	ch := WhatIf(in, private)
	var connect []string
	for _, c := range ch.Connect {
		connect = append(connect, c.Tile)
	}
	if strings.Join(connect, ",") != "apps/forecast,apps/ledger,apps/shared" || len(ch.Disconnect) != 0 || ch.Counts.Tiles != 6 {
		t.Errorf("public → private: connect %v, disconnect %v, %+v", connect, ch.Disconnect, ch.Counts)
	}
	in.Model = private
	ch = WhatIf(in, public)
	if len(ch.Connect) != 0 || len(ch.Disconnect) != 3 || ch.Disconnect[0].Reason != "Finance allows private models only" || ch.Disconnect[0].State != Blocked {
		t.Errorf("private → public: %+v", ch)
	}
	if ch = WhatIf(in, private); len(ch.Connect)+len(ch.Disconnect) != 0 {
		t.Errorf("the same model: %+v", ch)
	}
}

func TestResolveCredentials(t *testing.T) {
	for _, c := range []struct {
		session, saved, ws, want string
		forced                   bool
	}{
		{"", "", "", "own-first", false},
		{"", "", "own-first", "own-first", false},
		{"", "connector", "", "connector", false},
		{"own", "connector", "", "own", false},
		{"connector", "own", "own-first", "connector", false},
		{"own", "own", "connector", "connector", true},
		{"bogus", "", "", "own-first", false},
	} {
		if got, forced := ResolveCredentials(c.session, c.saved, c.ws); got != c.want || forced != c.forced {
			t.Errorf("%+v: %s %v", c, got, forced)
		}
	}
}

// covers D229 round 2 — a rule a workspace admin pinned on an org holds
// whatever the org says, and the reason names it; a tile under an org by
// a share names the share.
func TestPinsAndShares(t *testing.T) {
	pin, own := rules(t, `{"models":"private"}`), rules(t, `{"harnesses":["claude"]}`)
	layers := append([]Layer{{}}, OrgLayers("finance", "Finance", own, pin, "")...)
	d := Check(layers, Ask{WorkspaceAgent: true, Model: public})
	if d.Allowed || d.Reason != "Finance allows private models only (pinned by a workspace admin)" || !d.Pinned || d.By != "finance" {
		t.Errorf("a pinned rule: %+v", d)
	}
	if d = Check(layers, Ask{Harness: "codex"}); d.Allowed || d.Pinned || d.Reason != "Finance allows these coding agents only: claude (not codex)" {
		t.Errorf("the org's own rule beside a pin: %+v", d)
	}
	if d = Check(layers, Ask{WorkspaceAgent: true, Harness: "claude", Model: private}); !d.Allowed {
		t.Errorf("both satisfied: %+v", d)
	}
	// the org's own "any" loosens nothing a pin holds
	if d = Check(append([]Layer{{}}, OrgLayers("finance", "Finance", Rules{}, pin, "")...), Ask{WorkspaceAgent: true, Model: public}); d.Allowed {
		t.Error("an org with no rules of its own escaped its pin")
	}
	if ls := OrgLayers("eng", "Eng", own, Rules{}, ""); len(ls) != 1 || ls[0].Pinned {
		t.Errorf("no pin, one layer: %+v", ls)
	}
	shared := append([]Layer{{}}, OrgLayers("finance", "Finance", rules(t, `{"models":"private"}`), pin, "apps/*")...)
	d = Check(shared, Ask{WorkspaceAgent: true, Model: public})
	if d.Reason != "Finance allows private models only (pinned by a workspace admin; this tile is under Finance by its share apps/*)" || d.Via != "apps/*" {
		t.Errorf("a pinned rule on a shared tile: %+v", d)
	}
	d = Check([]Layer{{}, {ID: "finance", Name: "Finance", Rules: rules(t, `{"workspaceAgent":"off"}`), Via: "*"}}, Ask{WorkspaceAgent: true})
	if d.Reason != "Finance has the workspace agent turned off (this tile is under Finance by its share *)" {
		t.Errorf("a share's note: %+v", d)
	}
}

// covers D229 round 2 — three credential modes, and a model rule that
// binds agent sessions: connector-only wherever a layer's model rule
// isn't any, with the models the rule allows, and a harness that can't run
// on the connector refused with the reason.
func TestSessionLLM(t *testing.T) {
	for _, c := range []struct {
		session, saved, mode, want string
		forced                     bool
	}{
		{"", "", "own-first", "own-first", false},
		{"", "own", "own-first", "own", false},
		{"connector", "own", "own-first", "connector", false},
		{"", "", "connector-first", "connector", false},
		{"", "own", "connector-first", "own", false},
		{"own", "", "connector-first", "own", false},
		{"connector", "own", "connector-first", "connector", false},
		{"own", "own", "connector-only", "connector", true},
		{"own", "own", "connector", "connector", true}, // the name connector-only had before
		{"", "", "nonsense", "own-first", false},
	} {
		if got, forced := ResolveCredentials(c.session, c.saved, c.mode); got != c.want || forced != c.forced {
			t.Errorf("%+v: %s %v", c, got, forced)
		}
	}
	for in, want := range map[string]string{"": "own-first", "own-first": "own-first", "connector-first": "connector-first", "connector-only": "connector-only", "connector": "connector-only"} {
		if got, ok := CredentialMode(in); got != want || !ok {
			t.Errorf("mode %q: %s %v", in, got, ok)
		}
	}
	if _, ok := CredentialMode("sometimes"); ok {
		t.Error("a mode that doesn't exist")
	}

	free := []Layer{{}, {ID: "eng", Name: "Engineering", Rules: rules(t, `{"harnesses":["claude"]}`)}}
	s := Session(free, "", "own")
	if s.Mode != "own-first" || s.Use != "own" || s.Forced || s.Restricted() || s.Choose("connector") != "connector" || s.ModelAllowed(*public) != nil {
		t.Errorf("no model rule: %+v", s)
	}
	if s.HarnessRefusal("gemini", nil) != "" {
		t.Error("no connector is needed where nothing forces it")
	}
	if s = Session(free, "connector-first", ""); s.Use != "connector" || s.Choose("own") != "own" || s.Forced {
		t.Errorf("connector-first: %+v", s)
	}
	fin := []Layer{{}, {ID: "finance", Name: "Finance", Rules: rules(t, `{"models":"private"}`), Via: "apps/*"}}
	s = Session(fin, "own-first", "own")
	if s.Mode != "connector-only" || s.Use != "connector" || !s.Forced || s.Choose("own") != "connector" || !s.Restricted() || s.By != "finance" ||
		s.Reason != "Finance allows private models only (this tile is under Finance by its share apps/*)" {
		t.Errorf("a private-models org forces the connector: %+v", s)
	}
	if err := s.ModelAllowed(*public); err == nil || !strings.HasPrefix(err.Error(), "Finance allows private models only") {
		t.Errorf("a public model there: %v", err)
	}
	if err := s.ModelAllowed(*private); err != nil {
		t.Errorf("a private model there: %v", err)
	}
	listed := Session([]Layer{{Rules: rules(t, `{"models":["local"]}`)}}, "", "")
	if listed.Reason != "This workspace allows these models only: local" || listed.By != WorkspaceID || listed.ModelAllowed(*public) == nil || listed.ModelAllowed(*private) != nil {
		t.Errorf("the bounds' model list: %+v", listed)
	}
	can := func(h string) error {
		if h == "gemini" {
			return errString("its CLI speaks Google's wire only")
		}
		return nil
	}
	if why := s.HarnessRefusal("opencode", can); why != "" {
		t.Errorf("a harness the connector serves: %s", why)
	}
	want := "Finance allows private models only (this tile is under Finance by its share apps/*), so a coding agent here runs on the workspace's LLM connector — and gemini can't: its CLI speaks Google's wire only"
	if why := s.HarnessRefusal("gemini", can); why != want {
		t.Errorf("a harness the connector can't serve:\n got %s\nwant %s", why, want)
	}
	if why := s.HarnessRefusal("claude", nil); !strings.HasSuffix(why, "and claude can't: this xbind has no LLM connector set up") {
		t.Errorf("no connector at all: %s", why)
	}
	only := Session(free, "connector-only", "own")
	if why := only.HarnessRefusal("gemini", can); why != "This workspace runs coding agents on its LLM connector only — and gemini can't: its CLI speaks Google's wire only" || only.Restricted() {
		t.Errorf("the workspace's own connector-only: %s", why)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// covers D229 round 2 — the connection view says what an agent session on
// each tile runs on, which harnesses can't start there, and names a pin
// and a share in a block's reason.
func TestConnectionSessionsAndPins(t *testing.T) {
	in := Input{
		Orgs: []Org{
			{ID: "eng", Name: "Engineering"},
			{ID: "finance", Name: "Finance", Pin: rules(t, `{"models":"private"}`), Rules: rules(t, `{"harnesses":["claude","gemini"]}`)},
		},
		Tiles: []Tile{
			{Path: "apps/wiki", Owner: "eng", Orgs: []string{"eng"}},
			{Path: "apps/ledger", Owner: "finance", Orgs: []string{"finance"}},
			{Path: "apps/plan", Owner: "eng", Orgs: []string{"eng", "finance"}, Via: map[string]string{"finance": "apps/pl*"}},
		},
		Model: public, Harnesses: []string{"claude", "codex", "gemini"},
		Connector: func(h string) error {
			if h == "gemini" {
				return errString("its CLI speaks Google's wire only")
			}
			return nil
		},
	}
	v := Connection(in)
	wiki, ledger, plan := stateOf(v, "apps/wiki"), stateOf(v, "apps/ledger"), stateOf(v, "apps/plan")
	if wiki.Credentials != "own-first" || wiki.CredentialsReason != "" || wiki.Unavailable != nil {
		t.Errorf("a tile with no model rule: %+v", wiki)
	}
	if ledger.State != Blocked || !ledger.Pinned || ledger.Reason != "Finance allows private models only (pinned by a workspace admin)" ||
		ledger.Credentials != "connector-only" || ledger.CredentialsReason != ledger.Reason {
		t.Errorf("a pinned org's tile: %+v", ledger)
	}
	// gemini is on finance's list but can't run on the connector; codex is
	// not on the list at all (the harness rule's business, not this map's)
	if len(ledger.Unavailable) != 1 || !strings.Contains(ledger.Unavailable["gemini"], "gemini can't: its CLI speaks Google's wire only") {
		t.Errorf("harnesses that can't start there: %+v", ledger.Unavailable)
	}
	if plan.State != Blocked || plan.Via != "apps/pl*" || !strings.Contains(plan.Reason, "this tile is under Finance by its share apps/pl*") || plan.Org != "eng" {
		t.Errorf("a tile shared into a pinned org: %+v", plan)
	}
	if o := orgOf(v, "finance"); o.Pin == nil || !o.Pinned || o.State != Blocked || o.Rules == nil {
		t.Errorf("the org's row carries its pin: %+v", o)
	}
	in.Credentials = "connector-only"
	in.Connector = nil
	if w := stateOf(Connection(in), "apps/wiki"); w.Credentials != "connector-only" || len(w.Unavailable) != 3 || w.CredentialsReason != "" {
		t.Errorf("the workspace's connector-only with no connector: %+v", w)
	}
}
