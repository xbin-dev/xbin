package main

// harnesses_test.go — hello.images[].harnesses (docs/sandbox-manager.md
// §hello): the default image lists the base rootfs's coding agents, a saved
// config keeps exactly the harnesses it was saved with (none, from before
// them), the operators' entries are checked; and every command's
// environment says it runs in a sandbox the way coding agents check it.

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
)

func helloHarnesses(t *testing.T, c sandboxcontract.Caller) map[string][]sandboxcontract.Harness {
	t.Helper()
	var h struct {
		Images []struct {
			ID        string
			Harnesses []sandboxcontract.Harness
		}
	}
	c.Call("GET", "/hello?protocol=1", nil, 200, &h)
	out := map[string][]sandboxcontract.Harness{}
	for _, im := range h.Images {
		out[im.ID] = im.Harnesses
	}
	return out
}

func TestHarnesses(t *testing.T) {
	t.Parallel()
	tm := newTestManager(t, "")
	a := tm.tg.As(t, "apps/harness-a")
	base := helloHarnesses(t, a)["base"]
	got := map[string]string{}
	for _, h := range base {
		got[h.ID] = strings.Join(h.Argv, " ") + " | " + h.Login
		if h.Title == "" {
			t.Errorf("harness %s has no title", h.ID)
		}
	}
	want := map[string]string{
		"claude":   "claude-agent-acp | CLAUDE_CODE_REMOTE=1 claude /login",
		"codex":    "codex-acp | codex login --device-auth",
		"gemini":   "gemini --acp | NO_BROWSER=true gemini",
		"opencode": "opencode acp | opencode auth login",
	}
	if len(got) != len(want) {
		t.Fatalf("the default image's harnesses: %+v", base)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("harness %s: %q, want %q", id, got[id], w)
		}
	}
	// the tools name what the base has, the agents' CLIs among them — and no
	// command it lacks (chromium is Playwright's browser, not a command)
	tools := defaultConfig().Images[0].Tools
	for _, x := range []string{"git", "go", "node", "python3", "rg", "playwright", "claude", "codex", "gemini", "opencode"} {
		if !slices.Contains(tools, x) {
			t.Errorf("the base image's tools lack %s: %v", x, tools)
		}
	}
	if slices.Contains(tools, "chromium") {
		t.Errorf("tools name chromium, which isn't a command: %v", tools)
	}

	// an image of the operators' own lists none unless they say so; theirs
	// come out as they set them
	tm.setConfig(t, func(c *Config) {
		c.Images = append(c.Images, Image{ID: "plain", Title: "Plain"},
			Image{ID: "mine", Harnesses: []Harness{{ID: "aider", Title: " Aider ", Argv: []string{"aider-acp", "--x"}}}})
	})
	hs := helloHarnesses(t, a)
	if len(hs["plain"]) != 0 || len(hs["base"]) != 4 {
		t.Fatalf("an image without harnesses: %+v", hs)
	}
	if m := hs["mine"]; len(m) != 1 || m[0].ID != "aider" || m[0].Title != "Aider" || strings.Join(m[0].Argv, " ") != "aider-acp --x" || m[0].Login != "" {
		t.Fatalf("the operators' harness: %+v", m)
	}

	// PUT /ops/config checks them
	c := defaultConfig()
	for _, bad := range []string{
		`{"images": [{"id": "a", "harnesses": [{"id": ""}]}]}`,
		`{"images": [{"id": "a", "harnesses": [{"id": "bad id"}]}]}`,
		`{"images": [{"id": "a", "harnesses": [{"id": "x"}, {"id": "x"}]}]}`,
		`{"images": [{"id": "a", "harnesses": [{"id": "x", "argv": ["acp", ""]}]}]}`,
		`{"images": [{"id": "a", "harnesses": [{"id": "x", "login": "a\nb"}]}]}`,
	} {
		if _, err := c.merge([]byte(bad)); err == nil {
			t.Errorf("merged %s", bad)
		}
	}
	next, err := c.merge([]byte(`{"images": [{"id": "a", "harnesses": [{"id": "claude"}]}]}`))
	if err != nil || len(next.Images[0].Harnesses) != 1 || next.Images[0].Harnesses[0].ID != "claude" {
		t.Fatalf("a harness with its id alone: %+v %v", next.Images, err)
	}
}

// A config saved before harnesses (or without them) is what it was: its
// images list none — the first one doesn't pick up the default image's
// (decoding into the default config's slice would) — and one saved with them
// keeps them.
func TestHarnessesSavedConfig(t *testing.T) {
	t.Parallel()
	open := func(t *testing.T, saved string) Config {
		st, err := openStore(filepath.Join(t.TempDir(), "db.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.close() })
		if saved != "" {
			if err := st.putSetting("config", saved); err != nil {
				t.Fatal(err)
			}
		}
		c, err := st.config()
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := open(t, ""); len(c.Images[0].Harnesses) != 4 {
		t.Fatalf("no config saved: the default's harnesses, got %+v", c.Images[0])
	}
	old := `{"mode": "vm", "images": [{"id": "base", "title": "B", "default": true, "tools": ["git"]}, {"id": "two"}], "sizes": [{"id": "s", "memMiB": 1024, "vcpus": 1, "diskGiB": 10}], "layout": {"workdir": "/work", "home": "/home/dev", "user": "dev", "uid": 1000, "gid": 1000, "shell": "/bin/bash"}}`
	c := open(t, old)
	if len(c.Images) != 2 || c.Images[0].Harnesses != nil || c.Images[1].Harnesses != nil {
		t.Fatalf("a config saved without harnesses gained some: %+v", c.Images)
	}
	var withThem Config
	if err := json.Unmarshal([]byte(old), &withThem); err != nil {
		t.Fatal(err)
	}
	withThem.Images[1].Harnesses = []Harness{{ID: "codex"}}
	b, _ := json.Marshal(withThem)
	if c := open(t, string(b)); c.Images[0].Harnesses != nil || len(c.Images[1].Harnesses) != 1 || c.Images[1].Harnesses[0].ID != "codex" {
		t.Fatalf("a config saved with harnesses: %+v", c.Images)
	}
	// a first image saved with argvs of its own: decoded over the default
	// config's, they never reach the defaults (baseHarnesses)
	withThem.Images[0].Harnesses = []Harness{{ID: "claude", Argv: []string{"not-claude"}}, {ID: "codex", Argv: []string{"x", "y"}}}
	b, _ = json.Marshal(withThem)
	if c := open(t, string(b)); len(c.Images[0].Harnesses) != 2 || c.Images[0].Harnesses[0].Argv[0] != "not-claude" {
		t.Fatalf("a first image saved with argvs: %+v", c.Images[0])
	}
	if d := defaultConfig().Images[0].Harnesses; strings.Join(d[0].Argv, " ") != "claude-agent-acp" || strings.Join(d[1].Argv, " ") != "codex-acp" {
		t.Fatalf("a saved config changed the default image's harnesses: %+v", d)
	}
	// Claude Code's sign-in: a new (unsaved) config's under
	// CLAUDE_CODE_REMOTE=1; one saved with the login before it keeps that
	if c := open(t, ""); c.Images[0].Harnesses[0].Login != "CLAUDE_CODE_REMOTE=1 claude /login" {
		t.Fatalf("the default claude login: %q", c.Images[0].Harnesses[0].Login)
	}
	withThem.Images[0].Harnesses = []Harness{{ID: "claude", Title: "Claude Code", Argv: []string{"claude-agent-acp"}, Login: "claude /login"}}
	b, _ = json.Marshal(withThem)
	if c := open(t, string(b)); c.Images[0].Harnesses[0].Login != "claude /login" {
		t.Fatalf("a saved login changed: %+v", c.Images[0].Harnesses)
	}
}

// Every command runs with IS_SANDBOX=1 beside IN_SANDBOX=1.
func TestSandboxEnv(t *testing.T) {
	t.Parallel()
	d := defaultsOf(record{ID: "sb-1", Name: "one", Home: "/home/dev", User: "dev"})
	for k, v := range map[string]string{"IN_SANDBOX": "1", "IS_SANDBOX": "1", "SANDBOX_ID": "sb-1", "SANDBOX_NAME": "one", "HOME": "/home/dev"} {
		if d.Env[k] != v {
			t.Errorf("defaults %s=%q, want %q", k, d.Env[k], v)
		}
	}
	tm := newTestManager(t, "")
	a := tm.tg.As(t, "apps/env-a")
	sb := a.Create(map[string]any{"name": "env"})
	if got := a.Sh(sb.ID, `echo "$IN_SANDBOX$IS_SANDBOX"`); got != "11\n" {
		t.Fatalf("a command's environment: %q", got)
	}
}
