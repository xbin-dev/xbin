package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
)

type hcList struct {
	Harnesses []map[string]any `json:"harnesses"`
	Probe     *hcProbe         `json:"probe"`
}

func getHarnesses(t *testing.T, mux *http.ServeMux, c caller, query string) hcList {
	t.Helper()
	w := callAs(t, mux, c, "GET", "/harnesses"+query, nil)
	var l hcList
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatalf("GET /harnesses%s: %d %s", query, w.Code, w.Body)
	}
	return l
}

func (l hcList) entry(t *testing.T, id string) map[string]any {
	t.Helper()
	for _, e := range l.Harnesses {
		if e["id"] == id {
			return e
		}
	}
	t.Fatalf("no %s in %s", id, jsonOf(l))
	return nil
}

func (l hcList) ids() string {
	var s []string
	for _, e := range l.Harnesses {
		s = append(s, e["id"].(string))
	}
	return strings.Join(s, ",")
}

// seenIn is what an entry says was learned about sandbox ref (nil: nothing).
func seenIn(e map[string]any, ref string) map[string]any {
	s, _ := e["sandboxes"].(map[string]any)
	v, _ := s[ref].(map[string]any)
	return v
}

// The catalog's shape (§4.3.10) against a manager advertising the fake: the
// sdk's four even though no image has them, then the advertised one —
// available through the built-in coding class, its modes the fake's, its
// login and title the manager's.
func TestHarnessCatalogShape(t *testing.T) {
	_, mux := accessFixture(t)
	bindSbx(t, "apps/fsb")
	l := getHarnesses(t, mux, asAlice, "")
	if l.ids() != "claude,codex,gemini,opencode,fake" || l.Probe != nil {
		t.Fatalf("ids: %s", l.ids())
	}
	claude := l.entry(t, "claude")
	if claude["available"] != false || claude["reason"] != "no-image" || claude["why"] != "no bound sandbox manager's image has it" ||
		claude["name"] != "Claude Code" || claude["autoMode"] != "acceptEdits" || claude["approveMode"] != "default" ||
		claude["planMode"] != "plan" || claude["defaultMode"] != "default" || claude["setting"] != "approve" ||
		jsonOf(claude["login"]) != `{"command":"CLAUDE_CODE_REMOTE=1 claude /login"}` || jsonOf(claude["images"]) != `[]` ||
		jsonOf(claude["classes"]) != `["coding"]` {
		t.Fatalf("claude: %s", jsonOf(claude))
	}
	if o := l.entry(t, "opencode"); o["autoMode"] != "" || jsonOf(o["modes"]) != `[]` {
		t.Fatalf("opencode: %s", jsonOf(o))
	}
	fake := l.entry(t, "fake")
	want := map[string]any{
		"id": "fake", "name": "Fake agent (tests)", "available": true, "classes": []any{"coding"},
		"images": []any{map[string]any{"provider": "apps/fsb", "manager": "Fake sandboxes (test fixture)", "image": "base",
			"advertised": true, "egress": []any{"internet"}}},
		"modes": []any{map[string]any{"id": "ask", "name": "Ask before acting"}, map[string]any{"id": "auto", "name": "Auto"},
			map[string]any{"id": "yolo", "name": "Yolo", "explicit": true}},
		"defaultMode": "ask", "autoMode": "auto", "approveMode": "ask", "planMode": "ask", "setting": "approve",
		"login": map[string]any{"command": "fakeacp login"}, "sandboxes": map[string]any{},
	}
	if !reflect.DeepEqual(fake, want) {
		t.Fatalf("fake\n got %s\nwant %s", jsonOf(fake), jsonOf(want))
	}
	// options: the last a session of it reported, any conversation
	_ = agent.db.setHarnessOptions("fake", json.RawMessage(`[{"id":"model","name":"Model","currentValue":"fast"}]`))
	if o := getHarnesses(t, mux, asAlice, "").entry(t, "fake")["options"]; jsonOf(o) != `[{"currentValue":"fast","id":"model","name":"Model"}]` {
		t.Fatalf("options: %s", jsonOf(o))
	}
	// a class that allows it and is the caller's default comes first
	t.Cleanup(func() { classStore.Store(nil) })
	dev := map[string]any{"id": "dev", "name": "Dev", "toolsets": []string{"sandbox", "harness"}, "sandboxEgress": []string{"internet"},
		"harnesses": []string{"fake"}}
	if w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": []any{dev}, "default": "dev"}); w.Code != 200 {
		t.Fatalf("PUT /classes: %d %s", w.Code, w.Body)
	}
	if e := getHarnesses(t, mux, asAlice, "").entry(t, "fake"); jsonOf(e["classes"]) != `["dev","coding"]` {
		t.Fatalf("classes: %s", jsonOf(e["classes"]))
	}
}

// Why a harness can't be started, the first that holds: no image (or a
// manager that didn't answer might have it), no class, no egress.
func TestHarnessCatalogReasons(t *testing.T) {
	_, mux := accessFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	// the built-in coding edited without coding agents (a built-in left out
	// of a save comes back as its default, which has them)
	coding := map[string]any{"id": "coding", "name": "Coding", "toolsets": []string{"sandbox", "web", "files"}, "sandboxEgress": []string{"none", "internet"}}
	put := func(classes ...map[string]any) {
		t.Helper()
		if w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": append(classes, coding)}); w.Code != 200 {
			t.Fatalf("PUT /classes: %d %s", w.Code, w.Body)
		}
	}
	reason := func(c caller, id string) (string, string) {
		e := getHarnesses(t, mux, c, "").entry(t, id)
		r, _ := e["reason"].(string)
		why, _ := e["why"].(string)
		return r, why
	}
	mgrs := bindSbx(t, "apps/fsb", "apps/down")
	mgrs["apps/down"].FailNext("hello", 503, "unavailable", "it is down")
	if r, why := reason(asAlice, "claude"); r != "manager-error" || !strings.Contains(why, "apps/down") || !strings.Contains(why, "it is down") {
		t.Fatalf("claude with a manager down: %s %s", r, why)
	}
	if r, _ := reason(asAlice, "fake"); r != "" {
		t.Fatalf("fake is advertised by the one that answered: %s", r)
	}

	bindSbx(t, "apps/fsb")
	// no class allows coding agents
	put()
	if r, why := reason(asAlice, "fake"); r != "no-class" || why != "no class you may use allows coding agents" {
		t.Fatalf("no class: %s %s", r, why)
	}
	// one that allows it, for the managers only
	put(map[string]any{"id": "ops", "name": "Ops", "toolsets": []string{"sandbox", "harness"}, "sandboxEgress": []string{"internet"}, "who": "managers"})
	if r, _ := reason(asAlice, "fake"); r != "no-class" {
		t.Fatalf("alice may not use ops: %s", r)
	}
	if r, _ := reason(asMgr, "fake"); r != "" {
		t.Fatalf("the manager may: %s", r)
	}
	// a class that allows it, but not from this manager
	put(map[string]any{"id": "ops", "name": "Ops", "toolsets": []string{"sandbox", "harness"}, "sandboxEgress": []string{"internet"},
		"managers": []string{"apps/other"}})
	if r, why := reason(asAlice, "fake"); r != "no-egress" || !strings.Contains(why, "no class you may use allows it from Fake sandboxes") {
		t.Fatalf("another manager's class: %s %s", r, why)
	}

	// a manager that offers no egress but none
	noEgress := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/sbx/hello" {
				h.ServeHTTP(w, r)
				return
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			var hello map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &hello)
			hello["egress"] = []string{"none"}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(hello)
		})
	}
	bindSbxWith(t, noEgress, "apps/fsb")
	classStore.Store(nil)
	_ = agent.db.putSetting("classes", "")
	if r, why := reason(asAlice, "fake"); r != "no-egress" ||
		why != "needs internet access — Fake sandboxes (test fixture) offers none (bind its internet class)" {
		t.Fatalf("no egress: %s %s", r, why)
	}
}

// A manager whose hello predates images[].harnesses (none of its images
// says) offers the sdk's four on every image, unadvertised — the probe
// decides — and never the fake.
func TestHarnessCatalogPredatingManager(t *testing.T) {
	_, mux := accessFixture(t)
	mgrs := bindSbx(t, "apps/old")
	mgrs["apps/old"].Harnesses = []fsbHarness{}
	l := getHarnesses(t, mux, asAlice, "")
	for _, id := range []string{"claude", "codex", "gemini", "opencode"} {
		e := l.entry(t, id)
		want := []any{map[string]any{"provider": "apps/old", "manager": "Fake sandboxes (test fixture)", "image": "base",
			"advertised": false, "egress": []any{"internet"}}}
		if e["available"] != true || !reflect.DeepEqual(e["images"], want) {
			t.Fatalf("%s: %s", id, jsonOf(e))
		}
	}
	if l.ids() != "claude,codex,gemini,opencode" {
		t.Fatalf("ids: %s", l.ids())
	}
}

// ?probe=<ref> asks a running sandbox the caller may use which harnesses'
// commands it has — once in 10 minutes — and the catalog carries what it
// learned; a stopped sandbox isn't started, and nobody else's is asked.
func TestHarnessProbe(t *testing.T) {
	_, mux := accessFixture(t)
	t.Cleanup(forgetHarnessProbes)
	forgetHarnessProbes()
	mgrs := bindSbx(t, "apps/fsb")
	m := mgrs["apps/fsb"]
	m.Harnesses = []fsbHarness{{ID: "fake", Argv: []string{"sh"}}, {ID: "mine", Title: "Mine", Argv: []string{"no-such-command-xbin-probe"}},
		{ID: "bad", Title: "no command"}}
	box := mkSandbox(t, "apps/fsb", "alice", sbxCreate{Name: "api"})
	ref := sandboxRef("apps/fsb", box.ID)
	q := "?probe=" + url.QueryEscape(ref)
	runs := func() int { return m.count("POST", "/sbx/sandboxes/"+box.ID+"/run") }

	l := getHarnesses(t, mux, asAlice, q)
	if l.Probe == nil || !l.Probe.Ran || l.Probe.Ref != ref || l.Probe.Error != "" || runs() != 1 {
		t.Fatalf("the probe: %+v (runs %d)", l.Probe, runs())
	}
	if l.ids() != "claude,codex,gemini,opencode,fake,mine" {
		t.Fatalf("an advertised entry without a command is ignored: %s", l.ids())
	}
	if s := seenIn(l.entry(t, "fake"), ref); s == nil || s["installed"] != true || s["at"] == nil || s["signedIn"] != nil {
		t.Fatalf("fake: %s", jsonOf(s))
	}
	if s := seenIn(l.entry(t, "mine"), ref); s == nil || s["installed"] != false {
		t.Fatalf("mine: %s", jsonOf(s))
	}
	if s := l.entry(t, "claude")["sandboxes"]; jsonOf(s) != `{}` {
		t.Fatalf("its image doesn't have claude, so it isn't asked: %s", jsonOf(s))
	}
	if l := getHarnesses(t, mux, asAlice, q); l.Probe == nil || l.Probe.Ran || !l.Probe.Cached || runs() != 1 {
		t.Fatalf("a probe stands 10 minutes: %+v (runs %d)", l.Probe, runs())
	}
	// bob may not see alice's private sandbox: not asked, and not told
	l = getHarnesses(t, mux, asBob, q)
	if l.Probe == nil || l.Probe.Ran || l.Probe.Error != "no such sandbox" || runs() != 1 {
		t.Fatalf("bob's probe: %+v", l.Probe)
	}
	if s := l.entry(t, "fake")["sandboxes"]; jsonOf(s) != `{}` {
		t.Fatalf("bob learns about alice's sandbox: %s", jsonOf(s))
	}
	// a stopped sandbox isn't started by a probe
	other := mkSandbox(t, "apps/fsb", "alice", sbxCreate{Name: "idle"})
	conn, _ := sbxDial("apps/fsb", "alice")
	if _, err := conn.Lifecycle(context.Background(), other.ID, "stop", 10, false); err != nil {
		t.Fatal(err)
	}
	l = getHarnesses(t, mux, asAlice, "?probe="+url.QueryEscape(sandboxRef("apps/fsb", other.ID)))
	if l.Probe == nil || l.Probe.Ran || !strings.Contains(l.Probe.Error, "a probe doesn't start it") ||
		m.count("POST", "/sbx/sandboxes/"+other.ID+"/run") != 0 {
		t.Fatalf("a stopped sandbox: %+v", l.Probe)
	}
	if b, _ := m.Box(other.ID); b.State != "stopped" {
		t.Fatalf("the probe started it: %s", b.State)
	}
	if w := callAs(t, mux, asAlice, "GET", "/harnesses?probe=nope", nil); w.Code != 400 {
		t.Fatalf("a bad ref: %d", w.Code)
	}
	// a manager that predates the field: the sdk's four are asked about
	forgetHarnessProbes()
	forgetHellos()
	m.Harnesses = []fsbHarness{}
	l = getHarnesses(t, mux, asAlice, q)
	if l.Probe == nil || !l.Probe.Ran || runs() != 2 {
		t.Fatalf("probe of a predating manager: %+v", l.Probe)
	}
	for _, id := range []string{"claude", "codex", "gemini", "opencode"} {
		if s := seenIn(l.entry(t, id), ref); s == nil || s["installed"] == nil {
			t.Fatalf("%s: %s", id, jsonOf(s))
		}
	}
	// the binding carries its image's harnesses (nil from a manager that says nothing)
	forgetHellos()
	m.Harnesses = nil
	b, err := prepareBinding(context.Background(), who{kind: whoUser, user: "alice", level: "read"}, Config{Class: classCoding},
		sandboxPick{Ref: ref})
	if err != nil || jsonOf(b.Harnesses) != `["fake"]` {
		t.Fatalf("the binding: %+v %v", b, err)
	}
	forgetHellos()
	m.Harnesses = []fsbHarness{}
	if b, err := prepareBinding(context.Background(), who{kind: whoUser, user: "alice", level: "read"}, Config{Class: classCoding},
		sandboxPick{Ref: ref}); err != nil || b.Harnesses != nil {
		t.Fatalf("the binding from a predating manager: %+v %v", b, err)
	}
}

// The class toolset `harness` needs sandbox and an egress other than none;
// harnesses defaults to all with it and names known coding agents only (the
// sdk's, the fake, those a bound manager advertises, or ones the class was
// already saved with).
func TestHarnessClasses(t *testing.T) {
	_, mux := accessFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	mgrs := bindSbx(t, "apps/fsb")
	mgrs["apps/fsb"].Harnesses = []fsbHarness{{ID: "mine", Argv: []string{"my-acp"}}}
	put := func(want int, cls map[string]any) string {
		t.Helper()
		w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": []any{cls}})
		if w.Code != want {
			t.Fatalf("PUT %v: %d %s", cls, w.Code, w.Body)
		}
		return w.Body.String()
	}
	cls := func(ts []string, egress []string, harnesses any) map[string]any {
		c := map[string]any{"id": "dev", "name": "Dev", "toolsets": ts}
		if egress != nil {
			c["sandboxEgress"] = egress
		}
		if harnesses != nil {
			c["harnesses"] = harnesses
		}
		return c
	}
	const needs = "the harness toolset needs sandbox and an egress other than none — a coding agent must reach its provider"
	for _, c := range []map[string]any{
		cls([]string{"harness", "files"}, nil, nil),
		cls([]string{"harness", "sandbox"}, nil, nil),
		cls([]string{"harness", "sandbox"}, []string{"none"}, nil),
	} {
		if out := put(400, c); !strings.Contains(out, needs) {
			t.Fatalf("%v: %s", c, out)
		}
	}
	if out := put(400, cls([]string{"harness", "sandbox"}, []string{"internet"}, []string{"nope"})); !strings.Contains(out, `class dev: no coding agent \"nope\"`) {
		t.Fatalf("an unknown id: %s", out)
	}
	if out := put(400, cls([]string{"harness", "sandbox"}, []string{"internet"}, []string{"not an id"})); !strings.Contains(out, "isn't a coding agent id") {
		t.Fatalf("a bad id: %s", out)
	}
	put(200, cls([]string{"harness", "sandbox"}, []string{"internet"}, nil))
	dev, _ := currentClasses().find("dev")
	if !dev.Harnesses.All || !dev.allowsHarness("claude") || !dev.allowsHarness("mine") {
		t.Fatalf("harnesses defaults to all with the toolset: %+v", dev)
	}
	put(200, cls([]string{"harness", "sandbox"}, []string{"internet", "none"}, []string{"claude", "fake", "mine", "claude"}))
	dev, _ = currentClasses().find("dev")
	if jsonOf(dev.Harnesses) != `["claude","fake","mine"]` || dev.allowsHarness("codex") || !dev.allowsHarness("mine") {
		t.Fatalf("a list: %s", jsonOf(dev.Harnesses))
	}
	// an editor that doesn't know the field keeps the list
	put(200, cls([]string{"harness", "sandbox", "files"}, []string{"internet"}, nil))
	if dev, _ = currentClasses().find("dev"); jsonOf(dev.Harnesses) != `["claude","fake","mine"]` || !dev.has(tsFiles) {
		t.Fatalf("a save without harnesses: %s", jsonOf(dev.Harnesses))
	}
	// the manager stops advertising mine: the class keeps it, a new name isn't taken
	forgetHellos()
	mgrs["apps/fsb"].Harnesses = []fsbHarness{}
	put(200, cls([]string{"harness", "sandbox", "files"}, []string{"internet"}, []string{"claude", "mine"}))
	c2 := cls([]string{"harness", "sandbox"}, []string{"internet"}, []string{"yours"})
	c2["id"] = "dev2"
	w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": []any{cls([]string{"harness", "sandbox"}, []string{"internet"}, []string{"mine"}), c2}})
	if w.Code != 400 || !strings.Contains(w.Body.String(), `class dev2: no coding agent \"yours\"`) {
		t.Fatalf("a name no one advertises: %d %s", w.Code, w.Body)
	}

	// GET /classes: the built-in coding has the toolset and every agent
	w = callAs(t, mux, asAlice, "GET", "/classes", nil)
	var l struct{ Classes []map[string]any }
	_ = json.Unmarshal(w.Body.Bytes(), &l)
	var coding map[string]any
	for _, c := range l.Classes {
		if c["id"] == classCoding {
			coding = c
		}
	}
	if coding == nil || !strings.Contains(jsonOf(coding["toolsets"]), `"harness"`) || coding["harnesses"] != "all" {
		t.Fatalf("coding: %s", jsonOf(coding))
	}
	for _, c := range l.Classes {
		if c["id"] == classInternal && c["harnesses"] == nil {
			t.Fatalf("every class says its harnesses: %s", jsonOf(c))
		}
	}
	b := builtinState
	if c, _ := b.find(classCoding); !c.allowsHarness("claude") || !c.allowsHarness("fake") {
		t.Fatal("the built-in coding allows every coding agent")
	}
	if c, _ := b.find(classInternal); c.allowsHarness("claude") {
		t.Fatal("internal allows a coding agent")
	}
	// held to the private lane, a class loses its egress — and so its coding agents
	if c, _ := b.find(classCoding); c.clampTo("private").has(tsHarness) {
		t.Fatal("a class clamped to no egress keeps coding agents")
	}
}

// Auto / Always approve is a person's own, per harness: validated, stored in
// the agent, shown in the catalog as `setting`.
func TestHarnessModePrefs(t *testing.T) {
	_, mux := accessFixture(t)
	bindSbx(t, "apps/fsb")
	put := func(c caller, id string, body any) (int, string) {
		t.Helper()
		w := callAs(t, mux, c, "PUT", "/prefs/harness-mode/"+id, body)
		return w.Code, w.Body.String()
	}
	if w := callAs(t, mux, asAlice, "GET", "/prefs/harness-mode", nil); w.Code != 200 || w.Body.String() != "{\"modes\":{}}\n" {
		t.Fatalf("nothing set: %d %q", w.Code, w.Body)
	}
	if code, out := put(asAlice, "claude", map[string]string{"mode": "auto"}); code != 200 || out != "{\"mode\":\"auto\",\"provider\":\"claude\"}\n" {
		t.Fatalf("set: %d %s", code, out)
	}
	for _, c := range []struct {
		id   string
		body any
		want string
	}{
		{"claude", map[string]string{"mode": "yolo"}, `mode is \"auto\" or \"approve\"`},
		{"claude", "auto", `mode is \"auto\" or \"approve\"`},
		{"nope", map[string]string{"mode": "approve"}, `no coding agent \"nope\"`},
		{"opencode", map[string]string{"mode": "auto"}, "OpenCode has no auto mode — it asks as its own settings say"},
	} {
		if code, out := put(asAlice, c.id, c.body); code != 400 || !strings.Contains(out, c.want) {
			t.Errorf("%s %v: %d %s", c.id, c.body, code, out)
		}
	}
	for _, id := range []string{"opencode", "fake"} {
		if code, out := put(asAlice, id, map[string]string{"mode": "approve"}); code != 200 {
			t.Fatalf("%s: %d %s", id, code, out)
		}
	}
	if code, _ := put(asAlice, "fake", map[string]string{"mode": "auto"}); code != 200 {
		t.Fatal("the fake has an auto mode")
	}
	for name, c := range map[string]caller{"element": asElement, "system": asSystem, "view-as": asViewAs, "cron": asCron} {
		code, out := put(c, "claude", map[string]string{"mode": "approve"})
		if code != 403 {
			t.Errorf("%s set a person's setting: %d %s", name, code, out)
		}
		if w := callAs(t, mux, c, "GET", "/prefs/harness-mode", nil); w.Code != 403 {
			t.Errorf("%s read a person's settings: %d", name, w.Code)
		}
		if name != "cron" && !strings.Contains(out, "the setting is a person's own") {
			t.Errorf("%s: %s", name, out)
		}
	}
	w := callAs(t, mux, asAlice, "GET", "/prefs/harness-mode", nil)
	if w.Body.String() != "{\"modes\":{\"claude\":\"auto\",\"fake\":\"auto\",\"opencode\":\"approve\"}}\n" {
		t.Fatalf("alice's: %s", w.Body)
	}
	if s := getHarnesses(t, mux, asAlice, "").entry(t, "claude")["setting"]; s != "auto" {
		t.Fatalf("alice's catalog: %v", s)
	}
	if s := getHarnesses(t, mux, asBob, "").entry(t, "claude")["setting"]; s != "approve" {
		t.Fatalf("bob's catalog: %v", s)
	}
	if agent.db.harnessMode("bob", "claude") != hmApprove || agent.db.harnessMode("alice", "claude") != hmAuto {
		t.Fatal("what the engine reads")
	}
}

// A manager's own argv for a catalog id wins — the probe looks for it — and
// the catalog's name, login and (for its own argv) commands stay.
func TestHarnessProviderAdvertisedArgv(t *testing.T) {
	cat, _ := acp.Lookup("claude")
	p := harnessProvider("claude", &sbxHarness{ID: "claude", Title: "CC", Argv: []string{"claude-agent-acp"}, Login: "claude /login"})
	if p.Name != "Claude Code" || p.LoginCmd != cat.LoginCmd || jsonOf(p.Bins) != `["claude-agent-acp","claude"]` {
		t.Fatalf("the catalog's argv: %+v", p)
	}
	p = harnessProvider("claude", &sbxHarness{ID: "claude", Argv: []string{"/opt/acp/claude", "--stdio"}})
	if jsonOf(p.Argv) != `["/opt/acp/claude","--stdio"]` || jsonOf(p.Bins) != `["/opt/acp/claude"]` || p.LoginCmd != cat.LoginCmd {
		t.Fatalf("a manager's own argv: %+v", p)
	}
	if again, _ := acp.Lookup("claude"); jsonOf(again.Argv) != `["claude-agent-acp"]` || len(again.Bins) != 2 {
		t.Fatalf("the sdk catalog changed: %+v", again)
	}

	_, mux := accessFixture(t)
	t.Cleanup(forgetHarnessProbes)
	forgetHarnessProbes()
	m := bindSbx(t, "apps/fsb")["apps/fsb"]
	m.Harnesses = []fsbHarness{{ID: "codex", Argv: []string{"sh"}}}
	box := mkSandbox(t, "apps/fsb", "alice", sbxCreate{Name: "api"})
	ref := sandboxRef("apps/fsb", box.ID)
	l := getHarnesses(t, mux, asAlice, "?probe="+url.QueryEscape(ref))
	if l.Probe == nil || !l.Probe.Ran {
		t.Fatalf("the probe: %+v", l.Probe)
	}
	if s := seenIn(l.entry(t, "codex"), ref); s == nil || s["installed"] != true {
		t.Fatalf("codex by the manager's command: %s", jsonOf(s))
	}
}

// A class gaining the harness toolset without naming harnesses gets "all"
// (as mcp and managers do), even when it was saved before without the
// toolset; one that had it keeps its list on a save that leaves it out.
func TestHarnessClassGainsToolset(t *testing.T) {
	_, mux := accessFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	bindSbx(t, "apps/fsb")
	put := func(cls map[string]any) {
		t.Helper()
		if w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": []any{cls}}); w.Code != 200 {
			t.Fatalf("PUT %v: %d %s", cls, w.Code, w.Body)
		}
	}
	put(map[string]any{"id": "dev", "name": "Dev", "toolsets": []string{"sandbox"}, "sandboxEgress": []string{"internet"}})
	if dev, _ := currentClasses().find("dev"); jsonOf(dev.Harnesses) != `[]` {
		t.Fatalf("without the toolset: %s", jsonOf(dev.Harnesses))
	}
	put(map[string]any{"id": "dev", "name": "Dev", "toolsets": []string{"sandbox", "harness"}, "sandboxEgress": []string{"internet"}})
	if dev, _ := currentClasses().find("dev"); !dev.Harnesses.All || !dev.allowsHarness("claude") {
		t.Fatalf("gaining the toolset: %s", jsonOf(dev.Harnesses))
	}
	put(map[string]any{"id": "dev", "name": "Dev", "toolsets": []string{"sandbox", "harness"}, "sandboxEgress": []string{"internet"},
		"harnesses": []string{"codex"}})
	put(map[string]any{"id": "dev", "name": "Dev", "toolsets": []string{"sandbox", "harness", "files"}, "sandboxEgress": []string{"internet"}})
	if dev, _ := currentClasses().find("dev"); jsonOf(dev.Harnesses) != `["codex"]` {
		t.Fatalf("a save leaving it out: %s", jsonOf(dev.Harnesses))
	}
}

// Rolled back to a build from before coding agents (v0.3.64), the classes
// this build saved must still save there: that build's editor resends every
// stored class and its normalize refuses a toolset it doesn't know ("class
// coding: unknown toolset \"harness\""), so the harness toolset is stored
// apart from toolsets (the class's `harness`), and read back into it here.
// A class stored with it in toolsets (an earlier build of this program) is
// read as it was; one an older build saved again has lost it.
func TestHarnessClassRollback(t *testing.T) {
	_, mux := accessFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	bindSbx(t, "apps/fsb")
	// v0.3.64's classToolsets
	old := []string{tsFiles, tsRepl, tsWeb, tsInternal, tsSandbox, tsSubagents, tsSchedule, tsThreads, tsSkills}
	coding := map[string]any{"id": "coding", "name": "Coding", "description": "Ours.", "toolsets": []string{"sandbox", "web", "files", "harness"},
		"sandboxEgress": []string{"none", "internet"}}
	dev := map[string]any{"id": "dev", "name": "Dev", "toolsets": []string{"sandbox", "harness", "files"}, "sandboxEgress": []string{"internet"},
		"harnesses": []string{"claude"}}
	plain := map[string]any{"id": "notes", "name": "Notes", "toolsets": []string{"files", "skills"}}
	getClasses := func() map[string]map[string]any {
		t.Helper()
		w := callAs(t, mux, asMgr, "GET", "/classes", nil)
		var l struct{ Classes []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil {
			t.Fatalf("GET /classes: %d %s", w.Code, w.Body)
		}
		out := map[string]map[string]any{}
		for _, c := range l.Classes {
			out[c["id"].(string)] = c
		}
		return out
	}
	check := func(when string) {
		t.Helper()
		// what an older build reads: only toolsets it knows
		var stored struct {
			Classes []struct {
				ID       string   `json:"id"`
				Toolsets []string `json:"toolsets"`
			} `json:"classes"`
		}
		raw := agent.db.getSetting("classes")
		if err := json.Unmarshal([]byte(raw), &stored); err != nil || len(stored.Classes) != 3 {
			t.Fatalf("%s: stored %s (%v)", when, raw, err)
		}
		for _, c := range stored.Classes {
			for _, ts := range c.Toolsets {
				if !hasStr(old, ts) {
					t.Fatalf("%s: class %s stored with toolset %q, which v0.3.64 refuses: %s", when, c.ID, ts, raw)
				}
			}
		}
		// what this build reads: as saved
		got := getClasses()
		if !strings.Contains(jsonOf(got["dev"]["toolsets"]), `"harness"`) || jsonOf(got["dev"]["harnesses"]) != `["claude"]` ||
			!strings.Contains(jsonOf(got["coding"]["toolsets"]), `"harness"`) || got["coding"]["harnesses"] != "all" ||
			got["coding"]["description"] != "Ours." || strings.Contains(jsonOf(got["notes"]["toolsets"]), `"harness"`) {
			t.Fatalf("%s: GET /classes %s", when, jsonOf(got))
		}
		if c, _ := currentClasses().find("dev"); !c.allowsHarness("claude") || c.allowsHarness("codex") || !c.has(tsFiles) {
			t.Fatalf("%s: dev %+v", when, c)
		}
		if c, _ := currentClasses().find("notes"); c.has(tsHarness) {
			t.Fatalf("%s: notes gained coding agents: %+v", when, c)
		}
	}
	if w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": []any{coding, dev, plain}}); w.Code != 200 {
		t.Fatalf("PUT /classes: %d %s", w.Code, w.Body)
	}
	check("saved")
	// the editor sends back what GET said
	got := getClasses()
	if w := callAs(t, mux, asMgr, "PUT", "/classes", map[string]any{"classes": []any{got["coding"], got["dev"], got["notes"]}}); w.Code != 200 {
		t.Fatalf("PUT /classes again: %d %s", w.Code, w.Body)
	}
	check("saved again")
	// the form an earlier build of this program stored: harness in toolsets
	_ = agent.db.putSetting("classes", `{"classes":[{"id":"dev","name":"Dev","toolsets":["sandbox","harness"],"sandboxEgress":["internet"],"harnesses":["codex"]}]}`)
	if c, _ := loadClasses(agent.db).find("dev"); !c.allowsHarness("codex") || c.allowsHarness("claude") {
		t.Fatalf("toolsets holding harness: %+v", c)
	}
	// saved again by an older build: the class has no coding agents
	_ = agent.db.putSetting("classes", `{"classes":[{"id":"dev","name":"Dev","toolsets":["sandbox","files"],"mcp":[],"managers":"all","sandboxEgress":["internet"]}]}`)
	if c, _ := loadClasses(agent.db).find("dev"); c.has(tsHarness) || c.allowsHarness("claude") {
		t.Fatalf("an older build's save: %+v", c)
	}
}
