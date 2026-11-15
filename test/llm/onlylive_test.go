//go:build linux && integration

package llm

// onlylive_test.go — the REAL coding CLIs where the connector alone may
// serve (D226, docs/llm.md "Where only the connector may serve"): the
// rootfs's own OpenCode and Codex (codex-acp), started by xbind's agent
// host in a tile sandbox with NO network, under a model rule that forces
// the connector — with everything a person could have that points
// elsewhere: an OpenCode sign-in in auth.json, a provider of their own in
// their global config, a project config that enables other providers and
// names another model, a Codex config.toml with another model provider.
//
// What must hold for each: the session is on the connector; the models its
// CLI offers are the connector's and nothing else's (OpenCode: no provider
// but "xbin"); a model outside them can't be set; and a turn is answered —
// which, with no network but the LLM-only door, only the connector's
// upstream (hack/fakeopenai) can have done — metered to the person and the
// tile. The upstream is the fake: no provider is signed in to and no real
// key is used.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestConnectorOnlyLive(t *testing.T) {
	a := xbindtest.Require(t)
	rootfs := ""
	for _, p := range []string{os.Getenv("XBIN_ROOTFS"), os.Getenv("XBIN_TEST_ROOTFS"), filepath.Join(a.Repo, ".rootfs")} {
		if p != "" && rootfs == "" {
			if _, err := os.Stat(filepath.Join(p, "usr/local/bin/opencode")); err == nil {
				rootfs = p
			}
		}
	}
	if rootfs == "" {
		t.Skip("no rootfs with the coding CLIs (XBIN_TEST_ROOTFS): the real OpenCode and Codex are what this test runs")
	}
	fake := xbindtest.FakeOpenAI(t, a)
	dir, err := os.MkdirTemp("", "xbindtest-llm-only-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { xbindtest.RemoveTree(dir) })
	ws := filepath.Join(dir, "ws")
	const tile = "apps/only"
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, WS: ws})
	d.WriteTile(t, tile, map[string]string{"xbin.json": `{"title": "Only"}`, "index.html": `<!doctype html><title>only</title>`,
		// the project's own OpenCode config: other providers, another model
		"opencode.json": `{"model": "opencode/big-pickle", "enabled_providers": ["opencode", "anthropic", "mine"]}`})
	must := func(want int, method, path string, body any, hdrs ...xbindtest.Header) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body, hdrs...)
		if r.Status != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, r.Status, want, r)
		}
		return r
	}
	must(200, "PUT", "/api/xbin/llm/upstreams/house", map[string]any{"baseURL": fake + "/v1", "key": "sk-fake-ONLY-house-0000", "models": []string{"fake-chat"}, "private": true})
	must(200, "PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": "house/fake-chat"}})
	must(200, "PUT", "/api/xbin/agents/policy", map[string]any{"models": "private"}) // the connector alone, private models only
	d.AddUser(t, "alice", pw, "user", map[string]string{tile: "terminal"}, map[string]any{"termNet": true, "termApi": true})
	alice := bearer(d.Login(t, "alice", pw))

	// what alice has of her own, all of it pointing somewhere else
	home := filepath.Join(ws, "homes", "alice")
	for path, content := range map[string]string{
		".local/share/opencode/auth.json": `{"anthropic": {"type": "api", "key": "sk-ant-not-a-real-key-0000"}}`,
		".config/opencode/opencode.json": `{"provider": {"mine": {"npm": "@ai-sdk/openai-compatible", "name": "Mine",
			"options": {"baseURL": "http://127.0.0.1:1/v1", "apiKey": "x"}, "models": {"m1": {"name": "m1"}}}}, "small_model": "mine/m1"}`,
		".codex/config.toml": "model_provider = \"elsewhere\"\nmodel = \"elsewhere-model\"\n\n[model_providers.elsewhere]\nname = \"Elsewhere\"\nbase_url = \"http://127.0.0.1:1/v1\"\nwire_api = \"responses\"\n",
	} {
		p := filepath.Join(home, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	events := func(id string, since uint64) []sessionEvent {
		t.Helper()
		var out struct{ Events []sessionEvent }
		must(200, "GET", fmt.Sprintf("/api/xbin/term/sessions/%s/events?since=%d", id, since), nil, alice).Decode(t, &out)
		return out.Events
	}
	usage := func(model string) int64 {
		t.Helper()
		var out struct{ Rows []usageRow }
		must(200, "GET", "/api/xbin/llm/usage?component="+tile+"&model="+model, nil).Decode(t, &out)
		n := int64(0)
		for _, r := range out.Rows {
			if r.User != "alice" || r.Component != tile {
				t.Fatalf("a call metered to someone else: %+v", r)
			}
			n += r.Requests
		}
		return n
	}

	for _, h := range []struct {
		id, prefix string // the harness, and what its CLI writes before a connector model's id
		foreign    string // a model its CLI would otherwise offer
	}{
		{"opencode", "xbin/", "opencode/big-pickle"},
		{"codex", "", "gpt-5.5"},
	} {
		t.Run(h.id, func(t *testing.T) {
			if h.id == "codex" {
				if _, err := os.Stat(filepath.Join(rootfs, "usr/local/bun/bin/codex-acp")); err != nil {
					t.Skip("the rootfs has no codex-acp")
				}
			}
			before := usage("house/fake-chat")
			r := d.Call(t, "POST", "/api/xbin/term/sessions", map[string]any{"cwd": tile, "kind": "agent", "provider": h.id, "net": "none"}, alice)
			if r.Status != 200 {
				t.Fatalf("open %s: %d %s", h.id, r.Status, r)
			}
			var info struct{ ID string }
			_ = json.Unmarshal(r.Body, &info)
			id := info.ID
			t.Cleanup(func() { _ = d.Call(t, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice) })

			// idle, with the options its CLI advertised
			var all []sessionEvent
			var models []string
			current := ""
			xbindtest.Eventually(t, 4*time.Minute, h.id+" idle with its model option", func() (bool, string) {
				since := uint64(0)
				if n := len(all); n > 0 {
					since = all[n-1].Seq
				}
				all = append(all, events(id, since)...)
				idle := false
				for _, e := range all {
					if e.Type != "status" {
						continue
					}
					idle = idle || e.Data["status"] == "idle"
					if e.Data["status"] == "error" || e.Data["status"] == "exited" {
						t.Fatalf("%s did not start: %+v", h.id, e.Data)
					}
					opts, _ := e.Data["options"].([]any)
					for _, o := range opts {
						m, _ := o.(map[string]any)
						if m["category"] != "model" && m["id"] != "model" {
							continue
						}
						current, _ = m["currentValue"].(string)
						models = models[:0]
						vals, _ := m["options"].([]any)
						for _, v := range vals {
							vm, _ := v.(map[string]any)
							s, _ := vm["value"].(string)
							models = append(models, s)
						}
					}
				}
				return idle && current != "", fmt.Sprintf("%d events, model %q, the last: %+v", len(all), current, last(all))
			})
			var row struct{ Session struct{ LLM, Net string } }
			must(200, "GET", "/api/xbin/term/sessions/"+id, nil, alice).Decode(t, &row)
			if row.Session.LLM != "connector" || row.Session.Net != "none" {
				t.Fatalf("the session's row: %+v", row.Session)
			}
			if want := h.prefix + "house/fake-chat"; current != want {
				t.Errorf("%s starts on %q, want the connector's %q — not her config's, not the vendor's", h.id, current, want)
			}
			if h.id == "opencode" { // its CLI knows no provider but the connector's
				for _, m := range models {
					if !strings.HasPrefix(m, "xbin/") {
						t.Errorf("opencode offers %q: a model that isn't the connector's (all: %v)", m, models)
					}
				}
				if len(models) == 0 {
					t.Error("opencode offers no models at all")
				}
			}
			// a model outside the connector's can't be set: not her own, not the vendor's
			for _, m := range []string{h.foreign, "anthropic/claude-sonnet-4-5", "mine/m1", "elsewhere-model"} {
				if r := d.Call(t, "POST", "/api/xbin/term/sessions/"+id+"/options", map[string]any{"id": "model", "value": m}, alice); r.Status != 403 || !strings.Contains(r.String(), "not one of its models") {
					t.Errorf("%s: setting the model to %q: %d %s", h.id, m, r.Status, r)
				}
			}
			if r := d.Call(t, "POST", "/api/xbin/term/sessions/"+id+"/options", map[string]any{"id": "model", "value": h.prefix + "house/fake-chat"}, alice); r.Status != 200 {
				t.Errorf("%s: setting the connector's own model: %d %s", h.id, r.Status, r)
			}

			// a turn: with no network, only the connector's upstream can answer it
			since := all[len(all)-1].Seq
			must(200, "POST", "/api/xbin/term/sessions/"+id+"/prompt", map[string]any{"text": "hello"}, alice)
			var reply strings.Builder
			t.Cleanup(func() { // what the CLI itself said, when the turn didn't go through
				if t.Failed() {
					t.Logf("%s's session log:\n%s", h.id, d.Call(t, "GET", "/api/xbin/term/sessions/"+id+"/log", nil, alice))
				}
			})
			xbindtest.Eventually(t, 2*time.Minute, h.id+"'s turn", func() (bool, string) {
				done := false
				for _, e := range events(id, since) {
					since = e.Seq
					if e.Type == "message.delta" && e.Data["role"] == "agent" {
						s, _ := e.Data["text"].(string)
						reply.WriteString(s)
					}
					if e.Type == "status" && (e.Data["status"] == "error" || e.Data["status"] == "exited") {
						t.Fatalf("%s's turn failed: %+v (so far %q)", h.id, e.Data, reply.String())
					}
					done = done || e.Type == "turn.end"
				}
				return done, reply.String()
			})
			if !strings.Contains(reply.String(), "Hello from the fake model") {
				t.Errorf("%s's answer: %q", h.id, reply.String())
			}
			xbindtest.Eventually(t, 15*time.Second, h.id+"'s calls metered to alice on the private model", func() (bool, string) {
				n := usage("house/fake-chat")
				return n > before, fmt.Sprintf("%d calls (before: %d)", n, before)
			})
			must(204, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice)
		})
	}

	// without the rule the same CLI offers its other providers (what the
	// rule takes away is what the assertions above would otherwise miss)
	must(200, "PUT", "/api/xbin/agents/policy", map[string]any{"models": nil})
	r := d.Call(t, "POST", "/api/xbin/term/sessions", map[string]any{"cwd": tile, "kind": "agent", "provider": "opencode", "net": "none", "llm": "connector"}, alice)
	if r.Status != 200 {
		t.Fatalf("open opencode without the rule: %d %s", r.Status, r)
	}
	var info struct{ ID string }
	_ = json.Unmarshal(r.Body, &info)
	t.Cleanup(func() { _ = d.Call(t, "DELETE", "/api/xbin/term/sessions/"+info.ID, nil, alice) })
	other := false
	xbindtest.Eventually(t, 4*time.Minute, "opencode idle without the rule", func() (bool, string) {
		idle := false
		for _, e := range events(info.ID, 0) {
			if e.Type != "status" {
				continue
			}
			idle = idle || e.Data["status"] == "idle"
			opts, _ := e.Data["options"].([]any)
			for _, o := range opts {
				m, _ := o.(map[string]any)
				vals, _ := m["options"].([]any)
				for _, v := range vals {
					vm, _ := v.(map[string]any)
					s, _ := vm["value"].(string)
					other = other || (m["category"] == "model" && s != "" && !strings.HasPrefix(s, "xbin/"))
				}
			}
		}
		return idle, ""
	})
	if !other {
		t.Error("without the rule opencode offers only the connector's models: the fixture has nothing for the rule to take away")
	}
}
