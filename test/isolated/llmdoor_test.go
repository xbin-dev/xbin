//go:build linux && integration

package isolated

// llmdoor_test.go — the LLM connector inside a sandbox a manager made
// (D226; docs/llm.md "Sandboxes made by a sandbox manager"), live:
// coding-sandbox on xbind's runtime (setupCS), a consumer tile whose llm
// slot is bound to xbin:llm, and hack/fakeopenai as the connector's
// upstream.
//
//   - the manager's hello offers `llm`; a sandbox created with llm: true —
//     and no egress at all — carries llm.url, the gateway address;
//   - the consumer asks xbind for an LLM key for the person at its page
//     (POST /api/xbin/llm/keys, with nothing but its frame token) and a
//     command in the sandbox, given the key in its environment, lists the
//     models and gets a chat completion through the LLM-only listener — the
//     call metered to that person and the consumer tile;
//   - nothing else is there: no key is 401, another path 404, xbind's own
//     port at the gateway is closed, and a sandbox that didn't ask reaches
//     nothing at that address;
//   - the key ended by its label, the same call is 401.
//
// In a namespace sandbox and, where this box runs VMs, in a VM (the relay
// is the same; so is the door).

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

func TestLLMDoor(t *testing.T) { llmDoor(t, false) }

func TestLLMDoorVM(t *testing.T) { llmDoor(t, true) }

func llmDoor(t *testing.T, vm bool) {
	e, _ := setupCS(t, vm)
	d := e.d
	if d.IsRemote() || !e.people {
		t.Skip("needs this tree's xbind with owner auth on (a local daemon)")
	}
	fake := xbindtest.FakeOpenAI(t, d.A)
	d.Must(t, "PUT", "/api/xbin/llm/upstreams/fake", map[string]any{"baseURL": fake + "/v1", "key": "sk-fake-DOOR-TEST-0000"}, 200)
	d.Must(t, "PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": "fake/fake-chat"}}, 200)

	// the consumer: a page with a sandboxes slot and an llm slot, both bound
	const tile = "apps/llmc"
	if err := d.WriteFiles(tile, map[string]string{
		"xbin.json": `{"interfaces": {"sandboxes": {"kind": "http", "service": "sandbox-manager", "multi": true},
			"llm": {"kind": "http", "service": "openai"}}}`,
		"index.html": "<!doctype html><title>an LLM consumer</title>",
	}); err != nil {
		t.Fatal(err)
	}
	d.WaitComponent(t, tile)
	d.Bind(t, tile, "sandboxes", csTile)
	frame := func() xbindtest.Header { return xbindtest.FrameHeader(e.pageTok(t, tile, "alice")) }
	// not bound to the connector yet: no key
	if r := d.Call(t, "POST", "/api/xbin/llm/keys", map[string]any{"label": "door"}, frame()); r.Status != 403 {
		t.Fatalf("a key for a tile that isn't bound to xbin:llm: %d %s", r.Status, r)
	}
	d.Bind(t, tile, "llm", "xbin:llm")

	c := e.target().As(t, tile).Verified("alice")
	var hello sandboxcontract.Hello
	c.Call("GET", "/hello?protocol=1", nil, 200, &hello)
	if !slices.Contains(hello.Caps, "llm") {
		t.Fatalf("the manager's caps %v lack llm on an xbind that forwards the connector", hello.Caps)
	}
	sb := c.Create(map[string]any{"name": "door", "llm": true})
	if sb.LLM == nil || sb.LLM.URL != "http://10.0.2.2:4180/v1" || sb.Egress != "none" {
		t.Fatalf("the sandbox: llm %+v, egress %q", sb.LLM, sb.Egress)
	}
	if vm && sb.Isolation != "vm" {
		t.Fatalf("isolation %q, want a VM", sb.Isolation)
	}

	// the key: for alice, by the consumer's page alone
	var key struct{ ID, Token, User, Component string }
	d.Must(t, "POST", "/api/xbin/llm/keys", map[string]any{"user": "alice", "label": "door", "sandbox": sb.ID}, 200, frame()).Decode(t, &key)
	if !strings.HasPrefix(key.Token, "xbl_") || key.User != "alice" || key.Component != tile {
		t.Fatalf("the key's grant: user %q component %q", key.User, key.Component)
	}
	if r := d.Call(t, "POST", "/api/xbin/llm/keys", map[string]any{"user": "bob"}, frame()); r.Status != 403 {
		t.Errorf("a key for someone else: %d %s", r.Status, r)
	}

	// from inside the sandbox
	sh := func(id, cmd string, env map[string]string) (int, string) {
		t.Helper()
		r := c.Run(id, map[string]any{"cmd": `U=$XBIN_LLM_URL K=$XBIN_LLM_KEY; ` + cmd, "env": env, "timeoutMs": 60000, "merge": true})
		code := -1
		if r.ExitCode != nil {
			code = *r.ExitCode
		}
		out := ""
		if r.Output != nil {
			out = r.Output.Head + r.Output.Tail
		}
		return code, strings.ReplaceAll(out, key.Token, "<key>")
	}
	// under the names a coding agent is handed them by: the two variables
	// of xbind's a command may carry
	env := map[string]string{"XBIN_LLM_URL": sb.LLM.URL, "XBIN_LLM_KEY": key.Token}
	status := `curl -s -m 20 -o /dev/null -w '%{http_code}' `
	if code, out := sh(sb.ID, status+`-H "Authorization: Bearer $K" "$U/models"`, env); code != 0 || out != "200" {
		t.Fatalf("GET /models through the door: exit %d, %q", code, out)
	}
	code, out := sh(sb.ID, `curl -s -m 30 -H "Authorization: Bearer $K" -H 'Content-Type: application/json' -d '{"messages":[{"role":"user","content":"hello"}]}' "$U/chat/completions"`, env)
	if code != 0 || !strings.Contains(out, "Hello from the fake model.") {
		t.Fatalf("a chat completion through the door: exit %d, %s", code, out)
	}
	for what, c := range map[string][2]string{
		"no key":                  {status + `"$U/models"`, "401"},
		"another credential":      {status + `-H "Authorization: Bearer ` + d.Token() + `" "$U/models"`, "401"},
		"xbind's API at the door": {status + `-H "Authorization: Bearer $K" "${U%/v1}/api/xbin/whoami"`, "404"},
		"the settings":            {status + `-H "Authorization: Bearer $K" "${U%/v1}/api/xbin/llm/config"`, "404"},
		"another route":           {status + `-H "Authorization: Bearer $K" "$U/keys"`, "404"},
	} {
		if code, out := sh(sb.ID, c[0], env); code != 0 || out != c[1] {
			t.Errorf("%s: exit %d, status %q, want %s", what, code, out, c[1])
		}
	}
	// xbind's own listener is not at the gateway: the door is the only port
	u, _ := url.Parse(d.URL)
	if code, out := sh(sb.ID, fmt.Sprintf(`curl -s -m 5 -o /dev/null -w '%%{http_code}' http://10.0.2.2:%s/healthz`, u.Port()), nil); code == 0 || out != "000" {
		t.Errorf("xbind's port at the gateway answered: exit %d, %q", code, out)
	}
	// a sandbox that didn't ask has no door
	plain := c.Create(map[string]any{"name": "plain"})
	if plain.LLM != nil {
		t.Fatalf("a sandbox that didn't ask: %+v", plain.LLM)
	}
	if code, out := sh(plain.ID, status+`-H "Authorization: Bearer $K" "$U/models"`, env); code == 0 || out != "000" {
		t.Errorf("a sandbox without the door reached the connector: exit %d, %q", code, out)
	}

	// metered to the person and the consumer
	xbindtest.Eventually(t, 15*time.Second, "the call metered to alice and the consumer", func() (bool, string) {
		var use struct {
			Rows []struct {
				User, Component, Model string
				Requests               int
			}
		}
		r := d.Call(t, "GET", "/api/xbin/llm/usage?component="+url.QueryEscape(tile), nil)
		_ = json.Unmarshal(r.Body, &use)
		return len(use.Rows) == 1 && use.Rows[0].User == "alice" && use.Rows[0].Component == tile && use.Rows[0].Model == "fake/fake-chat" && use.Rows[0].Requests == 1, r.String()
	})

	// ended by its label: the same call is refused
	d.Must(t, "DELETE", "/api/xbin/llm/keys?label=door", nil, 204, frame())
	if code, out := sh(sb.ID, status+`-H "Authorization: Bearer $K" "$U/models"`, env); code != 0 || out != "401" {
		t.Errorf("the key after it was ended: exit %d, %q", code, out)
	}
}

// TestHarnessOnConnectorLive is the whole chain a coding agent of the agent
// template takes to the workspace's models (D226): the template instance
// with its llm slot bound to xbin:llm creates its sandbox at coding-sandbox
// with the connector asked for, starts the scripted agent — a harness with
// no sign-in anywhere — with a key xbind minted for the instance, and the
// agent, inside the sandbox, gets its answer through the LLM-only listener
// from the connector's upstream (hack/fakeopenai). The instance here is not
// partitioned: it acts for nobody, so the call is metered to the tile alone.
func TestHarnessOnConnectorLive(t *testing.T) {
	e, _, fake := setupHarness(t, false)
	d := e.d
	if d.IsRemote() {
		t.Skip("needs this tree's xbind (a local daemon)")
	}
	upstream := xbindtest.FakeOpenAI(t, d.A)
	d.Must(t, "PUT", "/api/xbin/llm/upstreams/fake", map[string]any{"baseURL": upstream + "/v1", "key": "sk-fake-HARNESS-TEST-0000"}, 200)
	d.Must(t, "PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": "fake/fake-chat"}}, 200)

	// the scripted agent as a harness that needs no sign-in of its own
	// (setupHarness advertises it with --require-login, for the sign-in tests)
	var st struct {
		Config struct{ Images []map[string]any }
	}
	e.ops(t, "GET", "/state", nil, 200, &st)
	im := st.Config.Images[0]
	hs, _ := im["harnesses"].([]any)
	for _, h := range hs {
		if m, ok := h.(map[string]any); ok && m["id"] == "fake" {
			m["argv"] = []string{fakeACPPath}
		}
	}
	e.ops(t, "PUT", "/config", map[string]any{"images": []any{im}}, 200, nil)

	// bound to the connector (the bind restarts the agent's backend): from
	// then on a sandbox it makes asks for the connector
	d.Must(t, "POST", "/api/xbin/bindings", map[string]any{"component": agTile, "slot": "llm", "providers": []string{"xbin:llm"}}, 200)
	asAlice := e.target().As(t, agTile).Verified("alice")
	var box agSandbox
	n := 0
	xbindtest.Eventually(t, 5*time.Minute, "a sandbox the agent made with the connector", func() (bool, string) {
		n++
		var b agSandbox
		r := e.ag(t, "alice", "POST", "/sandboxes", map[string]any{"name": fmt.Sprintf("coder-%d", n), "egress": "internet", "size": "coder"}, 0, nil)
		if r.Status != 201 || json.Unmarshal(r.Body, &b) != nil {
			return false, fmt.Sprint(r.Status, " ", r)
		}
		if got := asAlice.Get(b.ID); got.LLM == nil {
			e.ag(t, "alice", "DELETE", "/sandboxes/"+url.PathEscape(b.Ref), nil, 0, nil) // made before the backend saw its binding
			time.Sleep(2 * time.Second)
			return false, "a sandbox without llm (the agent's backend hasn't restarted with the binding yet)"
		}
		box = b
		return true, ""
	})
	if got := asAlice.Get(box.ID); got.LLM.URL != "http://10.0.2.2:4180/v1" {
		t.Fatalf("the sandbox's llm: %+v", got.LLM)
	}
	asAlice.Put(box.ID, fakeACPPath, string(fake), "&mkdirs=1&mode=755")

	var run struct{ ID int64 }
	e.ag(t, "alice", "POST", "/ask", map[string]any{"text": "llm-call hello", "harness": map[string]string{"provider": "fake"},
		"sandbox": map[string]string{"ref": box.Ref}}, 200, &run)
	id := run.ID
	v := e.waitView(t, "alice", id, "the coding agent's answer", 3*time.Minute, func(v hView) bool {
		return strings.Contains(v.lastAssistant(), "llm: ") || v.Run.Status == "error" || v.summary().State == "failed"
	})
	if answer := v.lastAssistant(); !strings.Contains(answer, "llm: Hello from the fake model.") {
		raw := e.ag(t, "alice", "GET", fmt.Sprintf("/runs/%d/view", id), nil, 0, nil)
		t.Fatalf("the coding agent's call through the connector: %q (%s)\n%s", answer, v.brief(), cut(raw.String(), 3000))
	}
	xbindtest.Eventually(t, 15*time.Second, "the call metered to the agent's tile", func() (bool, string) {
		var use struct {
			Rows []struct {
				User, Component, Model string
				Requests               int
			}
		}
		r := d.Call(t, "GET", "/api/xbin/llm/usage?component="+url.QueryEscape(agTile), nil)
		_ = json.Unmarshal(r.Body, &use)
		for _, row := range use.Rows {
			if row.Component == agTile && row.Model == "fake/fake-chat" && row.Requests >= 1 {
				return true, ""
			}
		}
		return false, r.String()
	})
	// the key is nowhere a page reads
	var view json.RawMessage
	e.ag(t, "alice", "GET", fmt.Sprintf("/runs/%d/view", id), nil, 200, &view)
	if strings.Contains(string(view), "xbl_") {
		t.Error("the run's view shows an LLM key")
	}
	// printed by a tool of the agent's, it is masked
	if got := e.say(t, "alice", id, "printenv XBIN_LLM_KEY", "printenv: ", 3*time.Minute); strings.Contains(got, "xbl_") {
		t.Errorf("the key, printed by the agent, is not masked: %q", got)
	}
}
