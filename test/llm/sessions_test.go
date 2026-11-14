//go:build linux && integration

package llm

// sessions_test.go — agent sessions on the LLM connector, end to end (D226,
// docs/llm.md "Coding agents and terminals"): an `xbind --isolate` built
// from this tree with auth on, hack/fakeopenai as the connector's upstream,
// and the scripted agent (hack/fakeacp, as the provider `fake`) started by
// the real agent host in a real tile sandbox — a harness that takes the
// connector the way Codex does, and calls it itself (`llm-call`).
//
// TestSessionsOnConnector, in order:
//   - the launcher's listing says what a session would run on;
//   - a session of a signed-in person with no sign-in of the harness runs
//     on the connector: the agent, inside its sandbox, calls the model with
//     the key xbind injected, and the call is metered to that person and the
//     tile — not to the tile's API token, which the agent never holds as a
//     key;
//   - the key is the session's: good while it lives, 401 once it ends, and
//     nothing but the model routes ever accepted it;
//   - a person who is signed in keeps their sign-in (own-first), and asking
//     for the connector is honoured;
//   - a session with no network says the connector can't be reached, and
//     asking for it there is refused;
//   - under a model rule (the workspace allows private models only) a
//     session runs on the connector alone, starts on a model the rule
//     allows, and its key can't call another — whatever is asked with it;
//     a harness the connector can't serve is not offered there;
//   - a session in a VM sandbox (Firecracker, where this box has KVM)
//     reaches the connector the same way.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

type sessionEvent struct {
	Seq  uint64
	Type string
	Data map[string]any
}

func TestSessionsOnConnector(t *testing.T) {
	a := xbindtest.Require(t)
	fake := xbindtest.FakeOpenAI(t, a)

	// the workspace is ours to name: the scripted agent lives in the tile's
	// own directory, which its sandbox mounts at the same path
	dir, err := os.MkdirTemp("", "xbindtest-llm-sessions-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { xbindtest.RemoveTree(dir) }) // after the daemon's own cleanup
	ws := filepath.Join(dir, "ws")
	const tile = "apps/hook"
	agentBin := filepath.Join(ws, tile, ".bin", "fakeacp")
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, WS: ws, Env: []string{"XBIN_AGENT_FAKE=" + agentBin}})
	d.WriteTile(t, tile, map[string]string{"xbin.json": `{"title": "Hook"}`, "index.html": `<!doctype html><title>hook</title>`})
	build := exec.Command("go", "build", "-o", agentBin, "./hack/fakeacp")
	build.Dir, build.Env = a.Repo, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hack/fakeacp: %v\n%s", err, out)
	}

	must := func(want int, method, path string, body any, hdrs ...xbindtest.Header) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body, hdrs...)
		if r.Status != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, r.Status, want, r)
		}
		return r
	}
	// two upstreams in front of the same fake: one public, one marked private
	must(200, "PUT", "/api/xbin/llm/upstreams/pub", map[string]any{"baseURL": fake + "/v1", "key": "sk-fake-SESSIONS-pub-0000", "models": []string{"fake-chat"}})
	must(200, "PUT", "/api/xbin/llm/upstreams/house", map[string]any{"baseURL": fake + "/v1", "key": "sk-fake-SESSIONS-house-0000", "models": []string{"fake-chat"}, "private": true})
	must(200, "PUT", "/api/xbin/llm/config", map[string]any{"defaults": map[string]any{"model": "pub/fake-chat"}})

	d.AddUser(t, "alice", pw, "user", map[string]string{tile: "terminal"}, map[string]any{"termNet": true, "termApi": true})
	alice := bearer(d.Login(t, "alice", pw))

	type llmRow struct {
		Use, Note, Reason string
		Connector, Forced bool
		SignedIn          *bool
	}
	providers := func(q string) map[string]struct {
		Unavailable string
		LLM         *llmRow
	} {
		t.Helper()
		var rows []struct {
			ID, Unavailable string
			LLM             *llmRow
		}
		must(200, "GET", "/api/xbin/agent/providers"+q, nil, alice).Decode(t, &rows)
		out := map[string]struct {
			Unavailable string
			LLM         *llmRow
		}{}
		for _, r := range rows {
			out[r.ID] = struct {
				Unavailable string
				LLM         *llmRow
			}{r.Unavailable, r.LLM}
		}
		return out
	}
	open := func(body map[string]any) (string, xbindtest.Resp) {
		t.Helper()
		b := map[string]any{"cwd": tile, "kind": "agent", "provider": "fake"}
		for k, v := range body {
			b[k] = v
		}
		r := d.Call(t, "POST", "/api/xbin/term/sessions", b, alice)
		var info struct{ ID string }
		_ = json.Unmarshal(r.Body, &info)
		if r.Status == 200 {
			t.Cleanup(func() { _ = d.Call(t, "DELETE", "/api/xbin/term/sessions/"+info.ID, nil, alice) })
		}
		return info.ID, r
	}
	events := func(id string, since uint64) []sessionEvent {
		t.Helper()
		var out struct{ Events []sessionEvent }
		must(200, "GET", fmt.Sprintf("/api/xbin/term/sessions/%s/events?since=%d", id, since), nil, alice).Decode(t, &out)
		return out.Events
	}
	// wait reads the session's log until pred holds for an event, and
	// answers it with every event read.
	wait := func(id, what string, pred func(e sessionEvent) bool) (sessionEvent, []sessionEvent) {
		t.Helper()
		var all []sessionEvent
		var hit *sessionEvent
		xbindtest.Eventually(t, 90*time.Second, what, func() (bool, string) {
			since := uint64(0)
			if n := len(all); n > 0 {
				since = all[n-1].Seq
			}
			for _, e := range events(id, since) {
				all = append(all, e)
				if hit == nil && pred(e) {
					e := e
					hit = &e
				}
			}
			return hit != nil, fmt.Sprintf("%d events, the last: %+v", len(all), last(all))
		})
		return *hit, all
	}
	idle := func(id string) []sessionEvent {
		t.Helper()
		_, all := wait(id, "the session idle", func(e sessionEvent) bool { return e.Type == "status" && e.Data["status"] == "idle" })
		return all
	}
	// say prompts the agent and answers its reply's text.
	say := func(id, text string) string {
		t.Helper()
		before := events(id, 0)
		since := uint64(0)
		if n := len(before); n > 0 {
			since = before[n-1].Seq
		}
		must(200, "POST", "/api/xbin/term/sessions/"+id+"/prompt", map[string]any{"text": text}, alice)
		var reply strings.Builder
		xbindtest.Eventually(t, 90*time.Second, "the turn's end after "+text, func() (bool, string) {
			done := false
			for _, e := range events(id, since) {
				since = e.Seq
				if e.Type == "message.delta" && e.Data["role"] == "agent" {
					s, _ := e.Data["text"].(string)
					reply.WriteString(s)
				}
				done = done || e.Type == "turn.end"
			}
			return done, reply.String()
		})
		return reply.String()
	}
	row := func(id string) (llm, net string) {
		t.Helper()
		var v struct{ Session struct{ LLM, Net string } }
		must(200, "GET", "/api/xbin/term/sessions/"+id, nil, alice).Decode(t, &v)
		return v.Session.LLM, v.Session.Net
	}
	usage := func(q string) []usageRow {
		t.Helper()
		var out struct{ Rows []usageRow }
		must(200, "GET", "/api/xbin/llm/usage"+q, nil).Decode(t, &out)
		return out.Rows
	}
	// keyCall makes a chat call with an LLM key from outside any sandbox.
	keyCall := func(key, model string) (int, string) {
		t.Helper()
		raw, _ := json.Marshal(chat(model, "hello", false))
		req, _ := http.NewRequest("POST", d.URL+"/api/xbin/llm/v1/chat/completions", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var e struct{ Error struct{ Code string } }
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return resp.StatusCode, e.Error.Code
	}

	// ---- the launcher's listing, before any session looked at alice's home ----
	if p := providers("?cwd=" + tile); p["fake"].LLM == nil || p["fake"].LLM.Use != "own-first" || !p["fake"].LLM.Connector || p["fake"].LLM.SignedIn != nil ||
		p["opencode"].LLM.Use != "own-first" || p["claude"].LLM.Use != "own" || p["claude"].LLM.Connector || p["gemini"].LLM.Connector {
		t.Fatalf("the listing: fake %+v opencode %+v claude %+v", p["fake"].LLM, p["opencode"].LLM, p["claude"].LLM)
	}

	// ---- no sign-in: the connector serves, from inside the sandbox ----
	id, r := open(nil)
	if r.Status != 200 {
		t.Fatalf("open: %d %s", r.Status, r)
	}
	all := idle(id)
	if llm, net := row(id); llm != "connector" || net == "none" {
		t.Fatalf("the session's row: llm %q net %q", llm, net)
	}
	said := false
	for _, e := range all {
		said = said || (e.Type == "notice" && e.Data["llm"] == "connector")
	}
	if !said {
		t.Errorf("the session doesn't say the connector serves it: %+v", all)
	}
	if got := say(id, "llm-call hello"); got != "llm: Hello from the fake model." {
		t.Fatalf("the agent's call through the connector: %q", got)
	}
	xbindtest.Eventually(t, 10*time.Second, "the call metered to alice and the tile", func() (bool, string) {
		rows := usage("?component=" + tile)
		return len(rows) == 1 && rows[0].User == "alice" && rows[0].Component == tile && rows[0].Model == "pub/fake-chat" &&
			rows[0].Requests == 1 && rows[0].InputTokens == 100 && rows[0].OutputTokens == 20, fmt.Sprintf("%+v", rows)
	})
	// the key is the session's own: an LLM key, not the terminal token; good
	// for the model routes and nothing else; dead with the session
	key := strings.TrimPrefix(say(id, "printenv XBIN_LLM_KEY"), "printenv: ")
	tok := strings.TrimPrefix(say(id, "printenv XBIN_TOKEN"), "printenv: ")
	if !strings.HasPrefix(key, "xbl_") || key == tok {
		t.Fatalf("the injected key is not an LLM key of its own (the terminal token: %v)", key == tok)
	}
	if url := strings.TrimPrefix(say(id, "printenv XBIN_LLM_URL"), "printenv: "); !strings.HasSuffix(url, "/api/xbin/llm/v1") {
		t.Errorf("XBIN_LLM_URL %q", url)
	}
	if code, _ := keyCall(key, ""); code != 200 {
		t.Fatalf("the session's key while it lives: %d", code)
	}
	for _, path := range []string{"/api/xbin/whoami", "/api/xbin/term/sessions", "/api/xbin/llm/config", "/api/xbin/llm/usage"} {
		if r := d.Call(t, "GET", path, nil, bearer(key)); r.Status != 401 {
			t.Errorf("GET %s with an LLM key: %d", path, r.Status)
		}
	}
	must(204, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice)
	xbindtest.Eventually(t, 20*time.Second, "the key dead with its session", func() (bool, string) {
		code, why := keyCall(key, "")
		return code == 401, fmt.Sprint(code, " ", why)
	})
	// that session's host looked at alice's home: the listing knows now
	if p := providers("?cwd=" + tile); p["fake"].LLM.Use != "connector" || p["fake"].LLM.SignedIn == nil || *p["fake"].LLM.SignedIn || p["claude"].LLM.Use != "signin" {
		t.Errorf("the listing after a session looked: fake %+v claude %+v", p["fake"].LLM, p["claude"].LLM)
	}

	// ---- signed in: the person's own sign-in keeps serving ----
	creds := filepath.Join(ws, "homes", "alice", ".fakeacp", "credentials")
	if err := os.MkdirAll(filepath.Dir(creds), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(creds, []byte("fake-login\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, _ = open(nil)
	idle(id)
	if llm, _ := row(id); llm != "own" {
		t.Fatalf("signed in, own-first: llm %q", llm)
	}
	if got := say(id, "llm-call hello"); got != "llm: no connector" {
		t.Fatalf("signed in: the agent's environment still carries the connector: %q", got)
	}
	must(204, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice)
	// …unless the session asks for the connector
	id, _ = open(map[string]any{"llm": "connector"})
	idle(id)
	if llm, _ := row(id); llm != "connector" {
		t.Fatalf("signed in, the session chose the connector: llm %q", llm)
	}
	must(204, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice)
	if err := os.RemoveAll(filepath.Dir(creds)); err != nil {
		t.Fatal(err)
	}

	// ---- no network: no route to xbind, and the session says so ----
	id, r = open(map[string]any{"net": "none"})
	if r.Status != 200 {
		t.Fatalf("open offline: %d %s", r.Status, r)
	}
	all = idle(id)
	said = false
	for _, e := range all {
		text, _ := e.Data["text"].(string)
		said = said || (e.Type == "notice" && strings.Contains(text, "net: none"))
	}
	if llm, net := row(id); !said || llm != "" || net != "none" {
		t.Errorf("an offline session: said %v, llm %q, net %q — %+v", said, llm, net, all)
	}
	if got := say(id, "llm-call hello"); got != "llm: no connector" {
		t.Errorf("an offline session's environment: %q", got)
	}
	must(204, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice)
	if _, r := open(map[string]any{"net": "none", "llm": "connector"}); r.Status != 409 || !strings.Contains(r.String(), "net: none") {
		t.Errorf("asking for the connector offline: %d %s", r.Status, r)
	}

	// ---- a model rule: the connector alone, and only the models it allows ----
	must(200, "PUT", "/api/xbin/agents/policy", map[string]any{"models": "private"})
	p := providers("?cwd=" + tile + "&all=1")
	if p["fake"].LLM == nil || p["fake"].LLM.Use != "connector" || !p["fake"].LLM.Forced || !strings.Contains(p["fake"].LLM.Reason, "private models only") ||
		p["fake"].Unavailable != "" || p["claude"].Unavailable == "" || p["gemini"].Unavailable == "" {
		t.Fatalf("the listing under a model rule: fake %+v (unavailable %q), claude unavailable %q", p["fake"].LLM, p["fake"].Unavailable, p["claude"].Unavailable)
	}
	if _, r := open(map[string]any{"llm": "own"}); r.Status != 403 || !strings.Contains(r.String(), "private models only") {
		t.Errorf("choosing one's own sign-in under a model rule: %d %s", r.Status, r)
	}
	if _, r := open(map[string]any{"provider": "claude"}); r.Status != 403 {
		t.Errorf("a harness the connector can't serve, under a model rule: %d %s", r.Status, r)
	}
	id, r = open(nil)
	if r.Status != 200 {
		t.Fatalf("open under the model rule: %d %s", r.Status, r)
	}
	idle(id)
	if model := strings.TrimPrefix(say(id, "printenv FAKEACP_LLM_MODEL"), "printenv: "); model != "house/fake-chat" {
		t.Fatalf("the model the session starts on: %q, want the private upstream's (the default is not allowed)", model)
	}
	if got := say(id, "llm-call hello"); got != "llm: Hello from the fake model." {
		t.Fatalf("the agent's call under the model rule: %q", got)
	}
	key = strings.TrimPrefix(say(id, "printenv XBIN_LLM_KEY"), "printenv: ")
	if code, why := keyCall(key, "pub/fake-chat"); code != 403 || why != "xbin_llm_policy" {
		t.Fatalf("the session's key calling a model the rule doesn't allow: %d %s", code, why)
	}
	if code, why := keyCall(key, "house/fake-chat"); code != 200 {
		t.Fatalf("the session's key calling a private model: %d %s", code, why)
	}
	xbindtest.Eventually(t, 10*time.Second, "the private model's calls metered", func() (bool, string) {
		rows := usage("?component=" + tile + "&model=" + "house/fake-chat")
		return len(rows) == 1 && rows[0].User == "alice" && rows[0].Requests == 2, fmt.Sprintf("%+v", rows)
	})
	must(204, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice)
	must(200, "PUT", "/api/xbin/agents/policy", map[string]any{"models": nil})

	// ---- a VM sandbox reaches the connector the same way (where this box runs VMs on KVM) ----
	var vm struct{ Status xbindtest.VMStatus }
	must(200, "GET", "/api/xbin/vm", nil).Decode(t, &vm)
	if !vm.Status.Available || vm.Status.Emulated {
		t.Logf("the VM session is skipped: VMs unavailable or emulated here (%s%s)", vm.Status.Reason, vm.Status.Note)
	} else {
		must(200, "PUT", "/api/xbin/vm/policy", map[string]any{"terminals": true, "budgetMiB": 8192})
		id, r = open(map[string]any{"vm": true})
		if r.Status != 200 {
			t.Fatalf("open in a VM: %d %s", r.Status, r)
		}
		idle(id)
		if llm, _ := row(id); llm != "connector" {
			t.Errorf("a VM session's row: llm %q", llm)
		}
		if got := say(id, "llm-call hello"); got != "llm: Hello from the fake model." {
			t.Errorf("the agent's call from a VM sandbox: %q", got)
		}
		must(204, "DELETE", "/api/xbin/term/sessions/"+id, nil, alice)
	}

	// no key in xbind's log
	if log, err := os.ReadFile(d.LogPath()); err == nil && bytes.Contains(log, []byte(key)) {
		t.Error("an LLM key is in xbind's log")
	}
}

func last(evs []sessionEvent) any {
	if len(evs) == 0 {
		return nil
	}
	return evs[len(evs)-1]
}
