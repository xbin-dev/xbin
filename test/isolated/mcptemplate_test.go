//go:build linux && integration

package isolated

// mcptemplate_test.go — "give an assistant a dice tool", end to end with no
// coding agent (docs/mcp.md; the journey's G9a, G4, G10): on a real
// `xbind --isolate`, the builtin mcp-server template instantiated as
// apps/dice and the agent template as apps/assistant; the assistant's mcp
// slot bound to apps/dice and its llm slot to xbin:llm, the workspace's LLM
// connector, whose upstream is hack/fakeopenai.
//
//   - apps/dice serves MCP through xbind from its first build: tools/list
//     names roll_dice, a tools/call rolls;
//   - GET /bindings: the assistant's llm is required; once it is bound, its
//     other unbound slots are optional (G10's fold);
//   - the assistant's GET /tools names roll_dice from apps/dice, bound to
//     the mcp slot and answering (G4);
//   - asked "Roll a d20 for me." (with the scripted model's `ws:` line for
//     the call a model would make), the chat agent calls roll_dice on
//     apps/dice and its answer shows the roll.
//
// The daemon's vault is unsealed by passphrase (the agent template's
// database is an encrypted resource). It gets GOPROXY=off: both backends build from the module cache
// alone (the template has no third-party module; the agent template's are
// cached by every earlier run), so a missing module fails the build rather
// than reaching the network. Run it on a dev box (user namespaces, a rootfs):
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestMCPServerTemplateLive$' ./test/isolated/

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestMCPServerTemplateLive(t *testing.T) {
	t.Parallel()
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Env: []string{"GOPROXY=off", "XBIN_VAULT_PASSPHRASE=xbindtest-vault"}})
	if d.IsRemote() {
		t.Skip("needs this tree's xbind (a local daemon)")
	}
	const dice, assistant = "apps/dice", "apps/assistant"
	fake := xbindtest.FakeOpenAI(t, a)
	d.Must(t, "PUT", "/api/xbin/llm/upstreams/fake", map[string]any{"baseURL": fake + "/v1", "key": "sk-fake-MCP-TEMPLATE-0000"}, 200)
	d.Must(t, "PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": "fake/fake-chat"}}, 200)

	d.Must(t, "POST", "/api/xbin/templates/new", map[string]any{"source": "mcp-server", "path": dice}, 200)
	d.Must(t, "POST", "/api/xbin/templates/new", map[string]any{"source": "agent", "path": assistant, "partition": false}, 200)
	d.WaitComponent(t, dice)
	d.WaitComponent(t, assistant)

	// the dice tile answers MCP through xbind as soon as it is built
	rpc := func(method, params string) (int, map[string]any) {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `"`
		if params != "" {
			body += `,"params":` + params
		}
		r := d.Call(t, "POST", "/api/"+dice+"/mcp", json.RawMessage(body+"}"))
		var out map[string]any
		_ = json.Unmarshal(r.Body, &out)
		return r.Status, out
	}
	xbindtest.Eventually(t, 5*time.Minute, "apps/dice's tools/list names roll_dice", func() (bool, string) {
		code, out := rpc("tools/list", "")
		b, _ := json.Marshal(out)
		return code == 200 && strings.Contains(string(b), `"name":"roll_dice"`), fmt.Sprint(code, " ", string(b))
	})
	if code, out := rpc("tools/call", `{"name":"roll_dice","arguments":{"sides":6,"count":2}}`); code != 200 ||
		!regexp.MustCompile(`rolled 2d6: \d, \d \(total \d+\)`).MatchString(fmt.Sprint(out["result"])) {
		t.Fatalf("a tools/call on apps/dice: %d %v", code, out)
	}

	// G10: the assistant's llm is required; bound, the rest are optional
	pending := func() map[string]map[string]any {
		var b struct {
			Pending []map[string]any `json:"pending"`
		}
		d.Must(t, "GET", "/api/xbin/bindings", nil, 200).Decode(t, &b)
		out := map[string]map[string]any{}
		for _, p := range b.Pending {
			if p["component"] == assistant {
				out[fmt.Sprint(p["slot"])] = p
			}
		}
		return out
	}
	if p := pending(); p["llm"]["required"] != true || p["mcp"]["optional"] != nil {
		t.Fatalf("the assistant's slots, nothing bound: %v", p)
	}
	d.Must(t, "POST", "/api/xbin/bindings", map[string]any{"component": assistant, "slot": "mcp", "providers": []string{dice}}, 200)
	d.Must(t, "POST", "/api/xbin/bindings", map[string]any{"component": assistant, "slot": "llm", "providers": []string{"xbin:llm"}}, 200)
	if p := pending(); p["llm"] != nil || p["mcp"] != nil || len(p) == 0 {
		t.Fatalf("the assistant's slots once llm and mcp are bound: %v", p)
	} else {
		for slot, row := range p {
			if row["optional"] != true {
				t.Errorf("the assistant's %s, its required slot bound: %v", slot, row)
			}
		}
	}

	// G4: the assistant says what a conversation would be offered — roll_dice from apps/dice
	var tools struct {
		Classes []struct {
			ID    string
			Tools []struct{ Name, Source, Server, Call string }
		}
		MCP []struct {
			Name, Via, Slot, Tile, Error string
			Reachable                    bool
			Tools                        []string
		}
	}
	xbindtest.Eventually(t, 5*time.Minute, "the assistant's GET /tools names roll_dice from apps/dice", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+assistant+"/tools", nil)
		if r.Status != 200 || json.Unmarshal(r.Body, &tools) != nil {
			return false, r.String()
		}
		for _, s := range tools.MCP {
			if s.Name == dice && s.Via == "slot" && s.Slot == "mcp" && s.Tile == dice && s.Reachable && len(s.Tools) == 1 && s.Tools[0] == "roll_dice" {
				return true, ""
			}
		}
		return false, r.String() // the backend restarts with the binding; its first answer may be before it
	})
	found := false
	for _, c := range tools.Classes {
		for _, tl := range c.Tools {
			if c.ID == "internal" && tl.Name == "roll_dice" && tl.Source == "mcp" && tl.Server == dice && tl.Call == "mcp:"+dice+":roll_dice" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the internal class doesn't offer roll_dice from apps/dice: %+v", tools.Classes)
	}

	// the chat: the person asks, the scripted model calls the tool a model would
	ask := "Roll a d20 for me.\nws: " + mcpWire(dice, "roll_dice") + ` {"sides":20}`
	var run struct{ ID int64 }
	d.Must(t, "POST", "/api/"+assistant+"/ask", map[string]any{"text": ask}, 200).Decode(t, &run)
	rolled := regexp.MustCompile(`rolled 1d20: (\d+)`)
	var view struct {
		Run      struct{ Status string }
		Messages []struct{ Role, Content, Name string }
	}
	xbindtest.Eventually(t, 3*time.Minute, "the assistant's answer shows the roll", func() (bool, string) {
		r := d.Call(t, "GET", fmt.Sprintf("/api/%s/runs/%d", assistant, run.ID), nil)
		if r.Status != 200 || json.Unmarshal(r.Body, &view) != nil {
			return false, r.String()
		}
		for i := len(view.Messages) - 1; i >= 0; i-- {
			if m := view.Messages[i]; m.Role == "assistant" && rolled.MatchString(m.Content) {
				return true, ""
			}
		}
		return view.Run.Status == "error", r.String()
	})
	var called, answered string
	for _, m := range view.Messages {
		switch {
		case m.Role == "tool" && strings.Contains(m.Name, "roll_dice"):
			called = m.Content
		case m.Role == "assistant" && rolled.MatchString(m.Content):
			answered = m.Content
		}
	}
	if !rolled.MatchString(called) || answered == "" || rolled.FindStringSubmatch(called)[1] != rolled.FindStringSubmatch(answered)[1] {
		t.Fatalf("the roll came from apps/dice's roll_dice: tool %q, answer %q (%s)", called, answered, view.Run.Status)
	}
	n := rolled.FindStringSubmatch(answered)[1]
	t.Logf("the assistant called roll_dice on %s and answered %q (rolled %s)", dice, clipLine(answered), n)
}

// mcpWire is the name the agent template sends the model for an MCP tool
// (its context.go wireToolName): what a provider won't take replaced by _,
// and an FNV-1a hash of the name appended.
func mcpWire(server, tool string) string {
	name := "mcp:" + server + ":" + tool
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return regexp.MustCompile(`[^A-Za-z0-9_-]+`).ReplaceAllString(name, "_") + fmt.Sprintf("_%08x", h.Sum32())
}

func clipLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
