package broker

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/registry"
)

// multiWorkspace: a communication agent with a multi http slot, an imap
// provider exposing instances, and a plain slack provider.
func multiBroker(t *testing.T) *Broker {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1}`)
	write("apps/agent/xbin.json", `{
		"interfaces": { "channels": { "kind":"http", "service":"comm", "multi":true },
		                "single":   { "kind":"http", "service":"comm" } }}`)
	write("apps/imap/xbin.json", `{
		"expose": {"roles":{"writer":"w"}},
		"provides": { "email": { "kind":"http", "service":"comm", "role":"writer", "instances":true } }}`)
	write("apps/slack/xbin.json", `{
		"expose": {"roles":{"reader":"r"}},
		"provides": { "slack": { "kind":"http", "service":"comm" } }}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIfaceMultiplicity(t *testing.T) {
	b := multiBroker(t)
	do := func(method, path, body string, p auth.Principal) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		switch path {
		case "/bindings":
			b.apiBindingSet(w, r)
		case "/iface-instances":
			b.apiIfaceInstancesSet(w, r)
		}
		return w
	}
	admin := auth.Principal{Owner: true}

	// The imap provider registers its own instances (self-scoped).
	if w := do("PUT", "/iface-instances", `{"instances":{"abc":"/accounts/abc","def":"/accounts/def"}}`,
		auth.Principal{Component: "apps/imap"}); w.Code != 200 {
		t.Fatalf("instance registration: %d %s", w.Code, w.Body.String())
	}
	// Another element cannot set imap's instances.
	if w := do("PUT", "/iface-instances", `{"component":"apps/imap","instances":{}}`,
		auth.Principal{Component: "apps/slack"}); w.Code != 403 {
		t.Fatalf("cross-component instance set: want 403, got %d", w.Code)
	}

	// Bare bind to an instances-provider is rejected; instance bind on the
	// non-instances provider too; unknown instance rejected.
	for body, why := range map[string]string{
		`{"component":"apps/agent","slot":"channels","providers":["apps/imap"]}`:                "bare ref to instances-provide",
		`{"component":"apps/agent","slot":"channels","providers":["apps/slack#x"]}`:             "instance ref to plain provide",
		`{"component":"apps/agent","slot":"channels","providers":["apps/imap#nope"]}`:           "unknown instance",
		`{"component":"apps/agent","slot":"single","providers":["apps/imap#abc","apps/slack"]}`: "multi set on single slot",
	} {
		if w := do("POST", "/bindings", body, admin); w.Code != 400 {
			t.Errorf("%s: want 400, got %d %s", why, w.Code, w.Body.String())
		}
	}

	// Bind the multi slot to both imap instances + slack; the single slot to one instance.
	if w := do("POST", "/bindings",
		`{"component":"apps/agent","slot":"channels","providers":["apps/imap#abc","apps/imap#def","apps/slack"]}`,
		admin); w.Code != 200 {
		t.Fatalf("multi bind: %d %s", w.Code, w.Body.String())
	}
	if w := do("POST", "/bindings",
		`{"component":"apps/agent","slot":"single","provider":"apps/imap#abc"}`, admin); w.Code != 200 {
		t.Fatalf("single instance bind: %d %s", w.Code, w.Body.String())
	}

	// Resolution: multi slot → three endpoints, instance URLs through paths.
	slots := b.HTTPSlots("apps/agent")
	ch := slots["channels"]
	if !ch.Def.Multi || len(ch.Endpoints) != 3 {
		t.Fatalf("channels endpoints: %+v", ch)
	}
	urls := map[string]string{}
	for _, e := range ch.Endpoints {
		key := e.Provider
		if e.Instance != "" {
			key += "#" + e.Instance
		}
		urls[key] = e.URL
	}
	if urls["apps/imap#abc"] != "/api/apps/imap/accounts/abc" ||
		urls["apps/imap#def"] != "/api/apps/imap/accounts/def" ||
		urls["apps/slack"] != "/api/apps/slack" {
		t.Fatalf("urls: %v", urls)
	}
	// A non-multi slot bound to an instance works like any single binding.
	sg := slots["single"]
	if len(sg.Endpoints) != 1 || sg.Endpoints[0].URL != "/api/apps/imap/accounts/abc" {
		t.Fatalf("single slot: %+v", sg)
	}

	// Binding-as-grant holds through instance refs (writer from the provide).
	if role, ok := b.httpBindingRole("apps/agent", "apps/imap"); !ok || role != "writer" {
		t.Fatalf("instance binding must grant the provide's role: %q %v", role, ok)
	}

	// Instance-expanded bind options use the # syntax.
	c, _ := b.Reg.Component("apps/agent")
	opts := b.bindOptions("apps/agent", c.Manifest.Interfaces["channels"])
	var ids []string
	for _, o := range opts {
		ids = append(ids, o.ID)
	}
	want := map[string]bool{"apps/imap#abc": true, "apps/imap#def": true, "apps/slack": true}
	for _, id := range ids {
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("bind options missing %v (got %v)", want, ids)
	}
}
