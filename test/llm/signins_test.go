//go:build linux && integration

package llm

// signins_test.go — a person's saved coding-agent sign-in, end to end
// (D230, docs/agent-signins.md): an `xbind --isolate` built from this tree
// with auth on, and the scripted agent (hack/fakeacp, the provider `fake`,
// run with --require-login so it signs in the way Claude Code does)
// started by the real agent host in a real tile sandbox. No connector is
// configured: the sign-in is all a session has. It lives beside the
// connector's session tests because it shares their daemon helpers.
//
// TestSavedSignInInSandbox, in order:
//   - a person pastes a (fake) token; no answer carries it, and it is
//     sealed on disk;
//   - their session on a tile only they can open a terminal on starts
//     signed in with it — inside the sandbox, in the agent's environment
//     alone: the agent host's own environment and a terminal the agent
//     opens don't hold it — and says so; what xbind keeps of the session
//     is masked;
//   - a workspace admin can't drive or read that session, and can end it;
//   - once someone else may open a terminal on the tile, the running
//     session is ended, and the next start runs without the sign-in and
//     says who;
//   - the sign-in removed: later starts don't have it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// An obviously fake token in Claude Code's setup-token shape.
const e2eSavedToken = "sk-ant-oat01-FAKEFAKEFAKEFAKEFAKEFAKE-e2e-4b7a"

func TestSavedSignInInSandbox(t *testing.T) {
	a := xbindtest.Require(t)
	dir, err := os.MkdirTemp("", "xbindtest-signins-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { xbindtest.RemoveTree(dir) })
	ws := filepath.Join(dir, "ws")
	const tile = "apps/hook"
	agentBin := filepath.Join(ws, tile, ".bin", "fakeacp")
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, WS: ws, Env: []string{"XBIN_AGENT_FAKE=" + agentBin + " --require-login"}})
	d.WriteTile(t, tile, map[string]string{"xbin.json": `{"title": "Hook"}`, "index.html": `<!doctype html><title>hook</title>`})
	build := exec.Command("go", "build", "-o", agentBin, "./hack/fakeacp")
	build.Dir, build.Env = a.Repo, append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hack/fakeacp: %v\n%s", err, out)
	}
	d.AddUser(t, "alice", pw, "user", map[string]string{tile: "terminal"}, map[string]any{"termNet": true, "termApi": true})
	d.AddUser(t, "root2", pw, "admin", nil)
	alice, admin := bearer(d.Login(t, "alice", pw)), bearer(d.Login(t, "root2", pw))

	var answers bytes.Buffer // every answer of the sign-in routes: none may carry the secret
	call := func(who xbindtest.Header, method, path string, body any) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body, who)
		answers.Write(r.Body)
		return r
	}
	must := func(want int, who xbindtest.Header, method, path string, body any) xbindtest.Resp {
		t.Helper()
		r := call(who, method, path, body)
		if r.Status != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, r.Status, want, r)
		}
		return r
	}
	// the tile is alice's own (written to disk above, it is the workspace's:
	// a tile whose source isn't only hers would ask for her trust first)
	must(200, admin, "POST", "/api/xbin/owner", map[string]any{"tile": tile, "to": "user:alice"})
	events := func(who xbindtest.Header, id string, since uint64) []sessionEvent {
		t.Helper()
		var out struct{ Events []sessionEvent }
		must(200, who, "GET", fmt.Sprintf("/api/xbin/term/sessions/%s/events?since=%d", id, since), nil).Decode(t, &out)
		return out.Events
	}
	open := func(body map[string]any) string {
		t.Helper()
		b := map[string]any{"cwd": tile, "kind": "agent", "provider": "fake"}
		for k, v := range body {
			b[k] = v
		}
		var info struct{ ID string }
		must(200, alice, "POST", "/api/xbin/term/sessions", b).Decode(t, &info)
		t.Cleanup(func() { _ = d.Call(t, "DELETE", "/api/xbin/term/sessions/"+info.ID, nil, alice) })
		xbindtest.Eventually(t, 90*time.Second, "the session idle", func() (bool, string) {
			evs := events(alice, info.ID, 0)
			for _, e := range evs {
				if e.Type == "status" && e.Data["status"] == "idle" {
					return true, ""
				}
			}
			return false, fmt.Sprintf("%d events, the last: %+v", len(evs), last(evs))
		})
		return info.ID
	}
	say := func(id, text string) string {
		t.Helper()
		before := events(alice, id, 0)
		since := uint64(0)
		if n := len(before); n > 0 {
			since = before[n-1].Seq
		}
		must(200, alice, "POST", "/api/xbin/term/sessions/"+id+"/prompt", map[string]any{"text": text})
		var reply strings.Builder
		xbindtest.Eventually(t, 90*time.Second, "the turn's end after "+text, func() (bool, string) {
			done := false
			for _, e := range events(alice, id, since) {
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
	type signinRow struct{ Source, ID, Name, Why string }
	rowOf := func(id string) *signinRow {
		t.Helper()
		var v struct{ Session struct{ Signin *signinRow } }
		must(200, alice, "GET", "/api/xbin/term/sessions/"+id, nil).Decode(t, &v)
		return v.Session.Signin
	}

	// ---- saved: sealed, and never answered ----
	var list struct {
		Available bool
		Vault     string
	}
	must(200, alice, "GET", "/api/xbin/agent/signins", nil).Decode(t, &list)
	if !list.Available || list.Vault != "open" {
		t.Fatalf("the store on an isolated workspace: %+v", list)
	}
	var saved struct {
		Signin struct{ ID, Name, Kind, Env string }
	}
	must(201, alice, "POST", "/api/xbin/agent/signins", map[string]any{"harness": "fake", "secret": e2eSavedToken, "name": "Personal"}).Decode(t, &saved)
	if saved.Signin.Kind != "setup-token" || saved.Signin.Env != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Fatalf("saved: %+v", saved.Signin)
	}
	_ = filepath.Walk(filepath.Join(ws, "data", "signins"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("FAKEFAKE")) {
				t.Errorf("%s holds the secret in the clear", p)
			}
		}
		return nil
	})

	// ---- her session, in a real sandbox, signed in with it ----
	id := open(nil)
	if got := say(id, "whoami"); got != "account: token …4b7a" {
		t.Fatalf("the agent signed in with %q", got)
	}
	if r := rowOf(id); r == nil || r.Source != "saved" || r.Name != "Personal" || r.Why != "" {
		t.Fatalf("the session's row: %+v", r)
	}
	if got := say(id, "printenv CLAUDE_CODE_OAUTH_TOKEN"); got != "printenv: [saved sign-in]" {
		t.Fatalf("the agent's own word on its variable, as xbind keeps it: %q", got)
	}
	// a terminal the agent opens (through the host) has no such variable
	say(id, "run: printenv CLAUDE_CODE_OAUTH_TOKEN | wc -c")
	evs, _ := json.Marshal(events(alice, id, 0))
	if bytes.Contains(evs, []byte("FAKEFAKE")) {
		t.Fatalf("the session's events hold the secret")
	}
	if !bytes.Contains(evs, []byte("Using your saved sign-in Personal.")) {
		t.Errorf("the session doesn't say what it runs on: %s", evs)
	}
	var uses struct {
		Uses []struct{ Name, Tile, Session, Origin string }
	}
	must(200, alice, "GET", "/api/xbin/agent/signins/uses", nil).Decode(t, &uses)
	if len(uses.Uses) != 1 || uses.Uses[0].Tile != tile || uses.Uses[0].Session != id || uses.Uses[0].Origin != "person" {
		t.Fatalf("the use log: %+v", uses.Uses)
	}

	// ---- an admin: no pass into it, but they may end it ----
	for _, path := range []string{"/events", "/log", ""} {
		if r := d.Call(t, "GET", "/api/xbin/term/sessions/"+id+path, nil, admin); r.Status != 403 {
			t.Errorf("an admin GET …%s of a session on a saved sign-in: %d", path, r.Status)
		}
	}
	if r := d.Call(t, "POST", "/api/xbin/term/sessions/"+id+"/prompt", map[string]any{"text": "printenv CLAUDE_CODE_OAUTH_TOKEN"}, admin); r.Status != 403 {
		t.Errorf("an admin prompts it: %d %s", r.Status, r)
	}
	var people struct {
		People []struct {
			User    string
			Total   int
			Signins []map[string]any
		}
	}
	must(200, admin, "GET", "/api/xbin/agent/signins/people", nil).Decode(t, &people)
	if len(people.People) != 1 || people.People[0].User != "alice" || people.People[0].Total != 1 || people.People[0].Signins[0]["name"] != nil {
		t.Fatalf("what an admin sees: %+v", people.People)
	}

	// ---- someone else can now change the tile: its source isn't only hers any more — the session is ended, and the next asks for her trust ----
	d.AddUser(t, "bob", pw, "user", map[string]string{tile: "terminal"})
	xbindtest.Eventually(t, 30*time.Second, "the session ended once bob could change the tile", func() (bool, string) {
		r := d.Call(t, "GET", "/api/xbin/term/sessions/"+id, nil, alice)
		return r.Status == 404, fmt.Sprint(r.Status)
	})
	id = open(nil)
	r := rowOf(id)
	if r == nil || r.Source == "saved" || !strings.Contains(r.Why, tile+" isn't only yours — bob can change it") {
		t.Fatalf("a session on a tile bob can change: %+v", r)
	}
	// "I trust this tile's source": hers to say, for the tile as she is shown it — not an admin's for her, and not for a tile that changed since
	question := func(who xbindtest.Header) string {
		t.Helper()
		var v struct {
			Harnesses []struct {
				ID   string
				Here *struct {
					Trust *struct {
						Basis   string
						Editors []string
					}
				}
			}
		}
		must(200, who, "GET", "/api/xbin/agent/signins?tile="+tile, nil).Decode(t, &v)
		for _, h := range v.Harnesses {
			if h.ID == "fake" && h.Here != nil && h.Here.Trust != nil {
				return h.Here.Trust.Basis
			}
		}
		t.Fatalf("no trust question for %s: %+v", tile, v)
		return ""
	}
	trusted := func() int {
		var v struct{ Trusted []struct{ Holds bool } }
		must(200, alice, "GET", "/api/xbin/agent/signins", nil).Decode(t, &v)
		n := 0
		for _, row := range v.Trusted {
			if row.Holds {
				n++
			}
		}
		return n
	}
	if r := call(admin, "POST", "/api/xbin/agent/signins/trust", map[string]any{"tile": tile, "basis": question(admin), "shown": "signin+home"}); r.Status == 201 && trusted() != 0 {
		t.Fatal("an admin's answer became alice's trust")
	}
	if r := call(alice, "POST", "/api/xbin/agent/signins/trust", map[string]any{"tile": tile}); r.Status != 400 {
		t.Fatalf("an answer with no basis: %d %s", r.Status, r)
	}
	if r := call(alice, "POST", "/api/xbin/agent/signins/trust", map[string]any{"tile": tile, "basis": "tb_0000", "shown": "signin+home"}); r.Status != 409 || trusted() != 0 {
		t.Fatalf("an answer to another question than the tile's: %d %s", r.Status, r)
	}
	must(201, alice, "POST", "/api/xbin/agent/signins/trust", map[string]any{"tile": tile, "basis": question(alice), "shown": "signin+home"})
	onTrust := open(nil)
	if r := rowOf(onTrust); r == nil || r.Source != "saved" {
		t.Fatalf("after she trusted the tile: %+v", r)
	}
	if got := say(onTrust, "whoami"); got != "account: token …4b7a" {
		t.Fatalf("on the trusted tile the agent signed in with %q", got)
	}
	// someone NEW can change it: her answer is void from that moment — the session ends — and stays void when they are gone again
	d.AddUser(t, "carol", pw, "user", map[string]string{tile: "write"})
	xbindtest.Eventually(t, 30*time.Second, "the session ended once carol could change the tile", func() (bool, string) {
		r := d.Call(t, "GET", "/api/xbin/term/sessions/"+onTrust, nil, alice)
		return r.Status == 404, fmt.Sprint(r.Status)
	})
	var hers struct {
		Trusted []struct {
			ID, Why string
			Holds   bool
		}
	}
	must(200, alice, "GET", "/api/xbin/agent/signins", nil).Decode(t, &hers)
	if len(hers.Trusted) != 1 || hers.Trusted[0].Holds || hers.Trusted[0].Why != "someone new could change the tile" {
		t.Fatalf("her trusted tiles once carol can change it: %+v", hers.Trusted)
	}
	must(200, admin, "PUT", "/api/xbin/access", map[string]any{"tile": tile, "kind": "user", "id": "carol", "level": ""})
	if trusted() != 0 {
		t.Fatal("carol came and went, and alice's old answer holds again")
	}
	must(200, admin, "PUT", "/api/xbin/access", map[string]any{"tile": tile, "kind": "user", "id": "carol", "level": "write"})
	var provs []struct {
		ID    string
		Saved *struct {
			Name, Why string
			Inject    bool
		}
	}
	must(200, alice, "GET", "/api/xbin/agent/providers?cwd="+tile, nil).Decode(t, &provs)
	for _, p := range provs {
		if p.ID == "fake" && (p.Saved == nil || p.Saved.Inject || !strings.Contains(p.Saved.Why, "bob") || !strings.Contains(p.Saved.Why, "carol")) {
			t.Errorf("the launcher's word on the tile others can change: %+v", p.Saved)
		}
	}
	if evs, _ := json.Marshal(events(alice, id, 0)); bytes.Contains(evs, []byte("FAKEFAKE")) || !bytes.Contains(evs, []byte("isn't used here")) {
		t.Errorf("the shared tile's session: %s", evs)
	}
	must(204, alice, "DELETE", "/api/xbin/term/sessions/"+id, nil)

	// ---- removed ----
	must(200, alice, "DELETE", "/api/xbin/agent/signins/"+saved.Signin.ID, nil)
	provs = nil
	must(200, alice, "GET", "/api/xbin/agent/providers?cwd="+tile, nil).Decode(t, &provs)
	for _, p := range provs {
		if p.Saved != nil {
			t.Errorf("%s still names a saved sign-in: %+v", p.ID, p.Saved)
		}
	}

	if bytes.Contains(answers.Bytes(), []byte("FAKEFAKE")) {
		t.Fatal("an answer carried the secret")
	}
	if log, err := os.ReadFile(d.LogPath()); err == nil && bytes.Contains(log, []byte("FAKEFAKE")) {
		t.Fatal("xbind's log holds the secret")
	}
}
