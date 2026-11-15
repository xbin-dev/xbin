package sandboxcontract

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// --- the LLM connector (llm) ----------------------------------------------------

// llmChecks: a manager that offers `llm` gives a sandbox created with llm:
// true the workspace's LLM connector, and says where the sandbox reaches it
// (llm.url, an OpenAI base URL); a sandbox that didn't ask has no `llm`,
// and neither does any sandbox of a manager that doesn't offer it. The
// suite can't call a connector — it has no key — so what it checks is what
// a consumer reads to decide whether to hand a coding agent a key.
var llmChecks = []check{
	{"asked", func(t *testing.T, e *env) {
		if !e.has("llm") {
			t.Skip("the manager doesn't offer llm (llm/never checks that it then never claims one)")
		}
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "coder", "llm": true})
		check := func(where string, sb Sandbox) {
			t.Helper()
			if sb.LLM == nil || sb.LLM.URL == "" {
				t.Fatalf("%s: a sandbox created with llm: true has no llm.url: %+v", where, sb.LLM)
			}
			u, err := url.Parse(sb.LLM.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || strings.HasSuffix(sb.LLM.URL, "/") {
				t.Fatalf("%s: llm.url %q: an http(s) base URL without credentials, a query or a trailing slash", where, sb.LLM.URL)
			}
		}
		check("create", sb)
		check("get", a.Get(sb.ID))
		check("list", a.List()[sb.ID])
		// a change of something else leaves it: the connector is the sandbox's from its creation on
		var patched Sandbox
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"labels": map[string]string{"k": "v"}}, 200, &patched)
		check("after a PATCH", patched)
		// the names a coding agent reads the address and the key by get
		// into a command, whatever else of the workspace's a manager refuses
		if e.has("exec") {
			r := a.Run(sb.ID, map[string]any{"cmd": `printf '%s|%s' "$XBIN_LLM_URL" "$XBIN_LLM_KEY"`,
				"env": map[string]string{"XBIN_LLM_URL": sb.LLM.URL, "XBIN_LLM_KEY": "xbl_contract-check-not-a-key"}})
			if r.ExitCode == nil || *r.ExitCode != 0 || r.Stdout == nil || r.Stdout.Head != sb.LLM.URL+"|xbl_contract-check-not-a-key" {
				t.Errorf("a command's env with XBIN_LLM_URL and XBIN_LLM_KEY: %+v", r)
			}
		}
		if slices.Contains(e.hello.Egress, "none") && sb.Egress != "none" {
			t.Errorf("the sandbox was created with no egress named, and has %q", sb.Egress)
		}
	}},
	{"never", func(t *testing.T, e *env) {
		a := e.as("a")
		plain := a.Create(map[string]any{"name": "plain"})
		if plain.LLM != nil {
			t.Fatalf("a sandbox that didn't ask for llm has one: %+v", plain.LLM)
		}
		if a.Get(plain.ID).LLM != nil {
			t.Fatalf("get: a sandbox that didn't ask for llm has one")
		}
		if e.has("llm") {
			return
		}
		// a manager without the capability: a consumer doesn't send the
		// member (it reads hello first); one that does must not be told a
		// connector is there
		resp, body, err := a.Do(context.Background(), "POST", "/sandboxes", map[string]any{"name": "asked", "llm": true})
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode/100 != 2 {
			return // refusing the member it doesn't know is this manager's way
		}
		var sb Sandbox
		if err := json.Unmarshal(body, &sb); err != nil {
			t.Fatalf("%v in %s", err, body)
		}
		t.Cleanup(func() { _, _, _ = a.Do(context.Background(), "DELETE", "/sandboxes/"+sb.ID, nil) })
		if sb.LLM != nil {
			t.Fatalf("hello offers no llm, yet a sandbox created with llm: true claims one: %+v", sb.LLM)
		}
	}},
}
