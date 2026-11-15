package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	sdkllm "github.com/xbin-dev/xbin/sdk/llm"
)

const hostFile = `{
  // what the host of this xbind sets — and so locks
  "people": "everyone",
  "upstreams": {
    "hosted": {"baseURL": "UPSTREAM/v1", "keyFile": "upstream.key", "models": ["gpt-5", "gpt-5-mini"], "private": true,
               "prices": {"gpt-5": {"in": 1.25, "out": 10}}}
  },
  "defaults": {"model": "hosted/gpt-5", "roles": {"agent": "hosted/gpt-5"}},
  "aliases": {"house": "hosted/gpt-5-mini"},
  "quotas": {"workspace": {"costPerMonth": 100}, "default": {"tokensPerDay": 1000000}, "orgs": {}}
}`

// covers D226 — the two sources merge with the host's on top, and whatever
// the host's file sets is locked: marked in every read, refused in every
// write, while the settings it leaves alone stay the admin's.
func TestHostFileLocks(t *testing.T) {
	up := newUpstream(t)
	host := writeHost(t, t.TempDir(), strings.ReplaceAll(hostFile, "UPSTREAM", up.URL), "sk-host-file-key-0001")
	r := newRig(t, host)

	v := r.must(200, pOwner, "GET", "/llm/config", nil).json(t)
	locks := v["locks"].(map[string]any)
	var got []string
	for path, l := range locks {
		if lm := l.(map[string]any); lm["locked"] != true || lm["reason"] != "set by your host" {
			t.Errorf("lock %s: %v", path, lm)
		}
		got = append(got, path)
	}
	slices.Sort(got)
	want := []string{"aliases.house", "defaults.model", "defaults.roles.agent", "people", "quotas.default", "quotas.orgs", "quotas.workspace", "upstreams.hosted"}
	if !slices.Equal(got, want) {
		t.Errorf("locks: %v\nwant:  %v", got, want)
	}
	if v["people"] != "everyone" || v["enabled"] != true || v["configured"] != true || v["host"].(map[string]any)["active"] != true {
		t.Errorf("the merged view: %v", v)
	}

	// every locked setting refuses its write, naming itself
	refused := func(what string, a answer, paths ...string) {
		t.Helper()
		if a.code != 409 {
			t.Errorf("%s: %d, want 409: %s", what, a.code, a.body)
			return
		}
		m := a.json(t)
		var locked []string
		for _, p := range m["locked"].([]any) {
			locked = append(locked, p.(string))
		}
		slices.Sort(locked)
		if !slices.Equal(locked, paths) || m["reason"] != "set by your host" || !strings.Contains(m["error"].(string), "set by your host") {
			t.Errorf("%s: %s", what, a.body)
		}
	}
	refused("people", r.do(pOwner, "PUT", "/llm/config", map[string]any{"people": "admins"}), "people")
	refused("the default model", r.do(pOwner, "PUT", "/llm/config", map[string]any{"defaults": map[string]any{"model": "x/y"}}), "defaults.model")
	refused("a locked role and alias together", r.do(pOwner, "PUT", "/llm/config", map[string]any{
		"defaults": map[string]any{"roles": map[string]string{"agent": ""}}, "aliases": map[string]string{"house": "hosted/gpt-5"}}), "aliases.house", "defaults.roles.agent")
	refused("changing the host's upstream", r.do(pOwner, "PUT", "/llm/upstreams/hosted", map[string]any{"baseURL": "https://evil.example/v1"}), "upstreams.hosted")
	refused("giving the host's upstream another key", r.do(pOwner, "PUT", "/llm/upstreams/hosted", map[string]any{"key": "sk-mine-000000000000"}), "upstreams.hosted")
	refused("removing the host's upstream", r.do(pOwner, "DELETE", "/llm/upstreams/hosted", nil), "upstreams.hosted")
	refused("the locked quotas", r.do(pOwner, "PUT", "/llm/quotas", map[string]any{"workspace": nil, "default": map[string]any{"tokensPerDay": 5}, "orgs": map[string]any{"eng": map[string]any{"tokensPerDay": -1}}}),
		"quotas.default", "quotas.orgs", "quotas.workspace")
	// nothing of the refused writes took: the calls still go to the host's upstream
	if a := r.do(pUser("bob"), "POST", "/llm/v1/chat/completions", chatBody("", false)); a.code != 200 || up.lastBody()["model"] != "gpt-5" {
		t.Errorf("after the refused writes: %d %s (upstream got %v)", a.code, a.body, up.lastBody())
	}

	// what the file leaves alone is the admin's: a switch, another role and
	// alias, an upstream of their own, per-user quotas
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"enabled": true, "people": "everyone", // a locked setting sent back as it is passes
		"defaults": map[string]any{"roles": map[string]string{"harness": "mine/gpt-5-mini"}}, "aliases": map[string]string{"quick": "house"}})
	r.addUpstream("mine", up, nil)
	q := r.must(200, pOwner, "PUT", "/llm/quotas", map[string]any{"users": map[string]any{"bob": map[string]any{"tokensPerDay": 50}}}).json(t)
	if ql := q["locks"].(map[string]any); len(ql) != 3 || ql["quotas.users"] != nil {
		t.Errorf("quota locks: %v", ql)
	}
	v = r.must(200, pOwner, "GET", "/llm/config", nil).json(t)
	ups := v["upstreams"].([]any)
	if len(ups) != 2 || ups[0].(map[string]any)["source"] != "host" || ups[1].(map[string]any)["source"] != "admin" || ups[1].(map[string]any)["locked"] != false {
		t.Errorf("upstreams: %v", ups)
	}
	if len(v["problems"].([]any)) != 0 {
		t.Errorf("problems: %v", v["problems"])
	}
	// an admin's upstream can't take a host upstream's name's place either way: the host's wins
	if a := r.do(pOwner, "PUT", "/llm/upstreams/xbin", map[string]any{"baseURL": up.URL}); a.code != 400 {
		t.Errorf("the reserved name: %d", a.code)
	}

	// the host's quota is in force over the admin's own default
	st := r.must(200, pUser("zed"), "GET", "/llm", nil).json(t)
	limits := st["quota"].(map[string]any)["limits"].([]any)
	if len(limits) != 1 || limits[0].(map[string]any)["value"] != 1000000.0 || limits[0].(map[string]any)["source"] != "default" {
		t.Errorf("zed's limits: %v", limits)
	}
	// a non-admin reads no configuration
	if a := r.do(pUser("bob"), "GET", "/llm/config", nil); a.code != 403 {
		t.Errorf("GET /llm/config as a person: %d", a.code)
	}
	if a := r.do(pFrame("apps/agent", "ada"), "PUT", "/llm/config", map[string]any{"enabled": false}); a.code != 403 {
		t.Errorf("PUT /llm/config from a tile's frame: %d", a.code)
	}
	// a view-as session changes nothing
	viewAs := pAdmin
	viewAs.Impersonator = "owner"
	if a := r.do(viewAs, "PUT", "/llm/config", map[string]any{"enabled": false}); a.code != 403 {
		t.Errorf("PUT /llm/config while viewing as someone: %d", a.code)
	}
}

// covers D226 — lockAll: every setting is the host's, the admin's own
// settings and upstreams are not in force, and nothing can be added.
func TestHostLockAll(t *testing.T) {
	up := newUpstream(t)
	r := newRig(t, "")
	r.addUpstream("mine", up, nil)
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone", "aliases": map[string]string{"a": "mine/gpt-5"}})
	r.s.Close()

	host := writeHost(t, t.TempDir(), `{"lockAll": true, "upstreams": {"hosted": {"baseURL": "`+up.URL+`"}}}`, "")
	r2 := newRigAt(t, r.dir, host, r.vault)
	v := r2.must(200, pOwner, "GET", "/llm/config", nil).json(t)
	if ups := v["upstreams"].([]any); len(ups) != 1 || ups[0].(map[string]any)["name"] != "hosted" || v["people"] != "admins" || len(v["aliases"].(map[string]any)) != 0 ||
		v["locks"].(map[string]any)["*"] == nil || v["host"].(map[string]any)["lockAll"] != true {
		t.Errorf("lockAll's view: %v", v)
	}
	for what, a := range map[string]answer{
		"a new upstream":                     r2.do(pOwner, "PUT", "/llm/upstreams/another", map[string]any{"baseURL": up.URL}),
		"the people switch":                  r2.do(pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"}),
		"an alias":                           r2.do(pOwner, "PUT", "/llm/config", map[string]any{"aliases": map[string]string{"b": "hosted/x"}}),
		"a quota":                            r2.do(pOwner, "PUT", "/llm/quotas", map[string]any{"users": map[string]any{"bob": map[string]any{"tokensPerDay": 5}}}),
		"the admin's own upstream, shadowed": r2.do(pOwner, "DELETE", "/llm/upstreams/mine", nil),
	} {
		if a.code != 409 || !strings.Contains(string(a.body), "set by your host") {
			t.Errorf("%s under lockAll: %d %s", what, a.code, a.body)
		}
	}
	// without the file the admin's settings are back as they were
	r3 := newRigAt(t, r.dir, "", r.vault)
	v = r3.must(200, pOwner, "GET", "/llm/config", nil).json(t)
	if ups := v["upstreams"].([]any); len(ups) != 1 || ups[0].(map[string]any)["name"] != "mine" || v["people"] != "everyone" {
		t.Errorf("the admin's settings after the host file went: %v", v)
	}
}

// covers D226 — the deployment file is read again when it or a key file
// changes; a change that is invalid is refused and the last good file
// stays in force; a file that is invalid at boot stops the boot.
func TestHostFileReload(t *testing.T) {
	up := newUpstream(t)
	dir := t.TempDir()
	host := writeHost(t, dir, `{"upstreams": {"hosted": {"baseURL": "`+up.URL+`", "keyFile": "upstream.key"}}, "quotas": {"default": {"tokensPerDay": 1000}}}`, "sk-host-key-one-00001")
	r := newRig(t, host)
	touch := func(name, content string) { // a later mtime, whatever the filesystem's resolution
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(time.Duration(len(content)) * time.Second)
		_ = os.Chtimes(p, at, at)
		r.clock.add(2 * hostCheckGap)
	}
	call := func() answer { return r.do(pOwner, "POST", "/llm/v1/chat/completions", chatBody("gpt-5", false)) }
	limit := func() float64 {
		t.Helper()
		st := r.must(200, pOwner, "GET", "/llm", nil).json(t)
		return st["quota"].(map[string]any)["limits"].([]any)[0].(map[string]any)["value"].(float64)
	}
	if a := call(); a.code != 200 || up.auth[len(up.auth)-1] != "Bearer sk-host-key-one-00001" || limit() != 1000 {
		t.Fatalf("at boot: %d, %q, %v", a.code, up.auth, limit())
	}
	// the key file rotates
	touch("upstream.key", "sk-host-key-two-00002\n")
	if a := call(); a.code != 200 || up.auth[len(up.auth)-1] != "Bearer sk-host-key-two-00002" {
		t.Errorf("after the key rotated: %d, %q", a.code, up.auth[len(up.auth)-1])
	}
	// the config changes
	touch("llm.jsonc", `{"upstreams": {"hosted": {"baseURL": "`+up.URL+`", "keyFile": "upstream.key"}}, "quotas": {"default": {"tokensPerDay": 2000}}}`)
	if limit() != 2000 {
		t.Errorf("after the file changed: limit %v", limit())
	}
	// a broken change: refused, the last good one in force, and said so to admins — without the host's paths
	touch("llm.jsonc", `{"upstreams": {"hosted": {"baseURL": "`+up.URL+`", "keyFile": "upstream.key"}}, "quotaz": {}}`)
	if limit() != 2000 {
		t.Errorf("after a broken change: limit %v", limit())
	}
	v := r.must(200, pOwner, "GET", "/llm/config", nil)
	if e, _ := v.json(t)["host"].(map[string]any)["error"].(string); e == "" || strings.Contains(string(v.body), dir) {
		t.Errorf("the refused reload in the view: %s", v.body)
	}
	if !strings.Contains(r.logs.String(), "quotaz") {
		t.Errorf("the refused reload's reason is not in the log: %s", r.logs)
	}
	// fixed again
	touch("llm.jsonc", `{"upstreams": {"hosted": {"baseURL": "`+up.URL+`", "keyFile": "upstream.key"}}, "quotas": {"default": {"tokensPerDay": 3000}}}`)
	if limit() != 3000 || r.must(200, pOwner, "GET", "/llm/config", nil).json(t)["host"].(map[string]any)["error"] != nil {
		t.Errorf("after the fix: limit %v", limit())
	}
	if strings.Contains(r.logs.String(), "sk-host-key") {
		t.Error("a host key appears in the log")
	}

	// at boot an invalid file is an error
	for name, content := range map[string]string{
		"an unknown setting":       `{"upstream": {}}`,
		"an invalid upstream":      `{"upstreams": {"Bad Name": {"baseURL": "https://x"}}}`,
		"a key in the file itself": `{"upstreams": {"a": {"baseURL": "https://x", "hasKey": true}}}`,
		"a missing key file":       `{"upstreams": {"a": {"baseURL": "https://x", "keyFile": "nope.key"}}}`,
		"a URL with a secret":      `{"upstreams": {"a": {"baseURL": "https://user:pw@x"}}}`,
		"a bad quota":              `{"quotas": {"default": {"tokensPerDay": -5}}}`,
		"not JSON":                 `upstreams:`,
	} {
		p := writeHost(t, t.TempDir(), content, "")
		if _, err := New(Options{Dir: t.TempDir(), HostFile: p}); err == nil || !strings.Contains(err.Error(), "XBIN_LLM_CONFIG") {
			t.Errorf("%s: New = %v, want an error naming XBIN_LLM_CONFIG", name, err)
		}
	}
	if _, err := New(Options{Dir: t.TempDir(), HostFile: filepath.Join(t.TempDir(), "absent.json")}); err == nil {
		t.Error("a host file that doesn't exist booted")
	}
}

// covers D226 — what a model name resolves to.
func TestModelResolution(t *testing.T) {
	one := merge(Settings{Upstreams: map[string]Upstream{"main": {BaseURL: "https://a"}}}, nil)
	two := merge(Settings{
		Upstreams: map[string]Upstream{"a": {BaseURL: "https://a", Models: []string{"gpt-5", "shared"}}, "b": {BaseURL: "https://b", Models: []string{"qwen", "shared"}}, "open": {BaseURL: "https://c"}},
		Defaults:  Defaults{Model: "a/gpt-5", Roles: map[string]string{"agent": "fast"}},
		Aliases:   map[string]string{"fast": "b/qwen", "chain": "fast", "loop1": "loop2", "loop2": "loop1", "dangling": "gone/x"},
	}, nil)
	withAny := merge(Settings{Upstreams: map[string]Upstream{"a": {BaseURL: "https://a", Models: []string{"gpt-5"}}}, Aliases: map[string]string{"*": "a/gpt-5"}}, nil)
	for _, c := range []struct {
		snap            *snapshot
		model, up, want string
		code            string
	}{
		{one, "anything-at-all", "main", "anything-at-all", ""}, // a single passthrough upstream takes any name
		{one, "main/gpt-5", "main", "gpt-5", ""},
		{one, "org/model-with/slashes", "main", "org/model-with/slashes", ""},
		{one, "", "", "", "xbin_llm_no_default"},
		{one, "xbin/agent", "", "", "model_not_found"},
		{two, "", "a", "gpt-5", ""},
		{two, "xbin/default", "a", "gpt-5", ""},
		{two, "xbin/agent", "b", "qwen", ""},
		{two, "xbin/harness", "a", "gpt-5", ""}, // a role without a model: the default
		{two, "chain", "b", "qwen", ""},
		{two, "a/gpt-5", "a", "gpt-5", ""},
		{two, "gpt-5", "a", "gpt-5", ""}, // a bare name one upstream lists
		{two, "open/whatever", "open", "whatever", ""},
		{two, "a/qwen", "", "", "model_not_found"},          // not on a's list
		{two, "shared", "", "", "xbin_llm_ambiguous_model"}, // listed by two
		{two, "unknown", "", "", "model_not_found"},         // several upstreams: never guessed
		{two, "loop1", "", "", "xbin_llm_alias_loop"},
		{two, "dangling", "", "", "model_not_found"},
		{withAny, "gpt-6-astra", "a", "gpt-5", ""}, // a CLI's own default, caught by "*"
		{withAny, "a/other", "a", "gpt-5", ""},
	} {
		u, m, err := c.snap.resolve(c.model)
		switch {
		case c.code == "" && (err != nil || u.Name != c.up || m != c.want):
			t.Errorf("resolve(%q) = %s, %q, %v; want %s, %q", c.model, u.Name, m, err, c.up, c.want)
		case c.code != "":
			me, ok := err.(*modelError)
			if !ok || me.code != c.code {
				t.Errorf("resolve(%q) = %v; want code %s", c.model, err, c.code)
			}
		}
	}
	if p := two.problems(); len(p) != 3 { // loop1, loop2, dangling
		t.Errorf("problems: %q", p)
	}
}

// covers D226 — the model list: every upstream's models under its name, a
// passthrough upstream's own list (cached), aliases and default models.
func TestModelList(t *testing.T) {
	r := newRig(t, "")
	up := newUpstream(t)
	r.addUpstream("open", up, nil)
	r.addUpstream("fixed", up, map[string]any{"models": []string{"only-this"}})
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"defaults": map[string]any{"model": "open/gpt-5", "roles": map[string]string{"agent": "fixed/only-this"}},
		"aliases": map[string]string{"fast": "open/gpt-5-mini", "*": "open/gpt-5", "broken": "nowhere/x"}})
	var list sdkllm.ModelList
	a := r.must(200, pOwner, "GET", "/llm/v1/models", nil)
	if err := json.Unmarshal(a.body, &list); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range list.Data {
		ids = append(ids, m.ID+"→"+m.AliasOf)
	}
	// "broken" names no upstream: with a catch-all alias it is what "*" is, and the list says so
	want := []string{"fixed/only-this→", "open/gpt-5→", "open/gpt-5-mini→", "broken→open/gpt-5", "fast→open/gpt-5-mini", "xbin/default→open/gpt-5", "xbin/agent→fixed/only-this"}
	if !slices.Equal(ids, want) {
		t.Errorf("models: %q\nwant:   %q", ids, want)
	}
	if list.Data[1].ContextWindow() != 400000 || list.Data[1].OwnedBy != "open" {
		t.Errorf("the upstream's own model object did not pass through: %+v", list.Data[1])
	}
	r.must(200, pOwner, "GET", "/llm/v1/models", nil)
	if up.modelsN != 1 {
		t.Errorf("the upstream's list was asked for %d times within its cache window", up.modelsN)
	}
	// llm-gw's convention for default models
	p := r.must(200, pOwner, "GET", "/llm/preferred?use=agent", nil).json(t)
	if p["model"] != "fixed/only-this" {
		t.Errorf("preferred agent: %v", p)
	}
	if p := r.must(200, pOwner, "GET", "/llm/preferred?use=summarizing", nil).json(t); p["model"] != "open/gpt-5" {
		t.Errorf("a role without a model falls back to the default: %v", p)
	}
	if p := r.must(200, pOwner, "GET", "/llm/preferred", nil).json(t); p["preferred"].(map[string]any)["default"] != "open/gpt-5" {
		t.Errorf("preferred: %v", p)
	}
	// an upstream whose list fails: named, with detail for admins only
	up.Close()
	r.clock.add(2 * modelsTTL)
	r.must(200, pOwner, "PUT", "/llm/config", map[string]any{"people": "everyone"})
	adm := r.must(200, pOwner, "GET", "/llm/v1/models", nil).json(t)["upstream_errors"].(map[string]any)["open"].(string)
	usr := r.must(200, pUser("bob"), "GET", "/llm/v1/models", nil).json(t)["upstream_errors"].(map[string]any)["open"].(string)
	if usr != "its model list is unavailable" || adm == usr || adm == "" {
		t.Errorf("upstream errors: admin %q, person %q", adm, usr)
	}
}

// covers D226 — the per-call policy seam: a hook that refuses by upstream
// (what org constraints will use the private flag for).
func TestCallPolicyHook(t *testing.T) {
	up := newUpstream(t)
	dir := t.TempDir()
	var seen []string
	s, err := New(Options{Dir: dir, Vault: &fakeVault{inited: true}, IsAdmin: func(p auth.Principal) bool { return p.IsAdmin() },
		Policy: func(c Caller, u UpstreamInfo, model string) error {
			seen = append(seen, c.User+"@"+u.Name+"/"+model)
			if !u.Private {
				return &modelError{msg: "your org allows private models only"}
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &rig{t: t, s: s, dir: dir}
	r.mux = newMux(s)
	r.must(200, pOwner, "PUT", "/llm/upstreams/pub", map[string]any{"baseURL": up.URL, "models": []string{"gpt-5"}})
	r.must(200, pOwner, "PUT", "/llm/upstreams/priv", map[string]any{"baseURL": up.URL, "models": []string{"qwen"}, "private": true})
	a := r.do(pOwner, "POST", "/llm/v1/chat/completions", chatBody("pub/gpt-5", false))
	if a.code != 403 || a.errCode(t) != "xbin_llm_policy" || !strings.Contains(string(a.body), "private models only") || up.calls() != 0 {
		t.Errorf("a refused call: %d %s", a.code, a.body)
	}
	r.must(200, pOwner, "POST", "/llm/v1/chat/completions", chatBody("priv/qwen", false))
	if !slices.Contains(seen, "owner@pub/gpt-5") || !slices.Contains(seen, "owner@priv/qwen") {
		t.Errorf("the policy saw: %q", seen)
	}
	var list sdkllm.ModelList
	_ = json.Unmarshal(r.must(200, pOwner, "GET", "/llm/v1/models", nil).body, &list)
	if len(list.Data) != 1 || list.Data[0].ID != "priv/qwen" {
		t.Errorf("the list hides what the policy refuses: %+v", list.Data)
	}
}
