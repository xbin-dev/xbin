package boot

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/sealed"
)

const wsAgentPath = "xbin/wsagent"

// The release's table registers the workspace agent in every xbind — and a
// workspace that does nothing sees nothing new. Until an admin has turned
// workspace agents on AND given someone access, a person who isn't an admin
// gets no agent, no chip (the state the chrome hides on), no notice, no row
// in a listing, and no way to its page or API; nothing of it is built or
// started. Who gets one is access that names the component: a pattern, even
// "*": terminal, gives nothing. An admin sees it as theirs to set up, and
// PUT /delegate/workspace {who} is the control the chrome's third step uses.
func TestWorkspaceAgentIsInertUntilSetUp(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := probeWorkspace(t)
	// a production xbind whose install has no build of the agent: the row is
	// there and held, as it is in a workspace run without sandboxes
	d := startSealed(t, ws, sealed.Table(), production(filepath.Join(t.TempDir(), "no-sealed-bin")))
	admin := d.addUser("adm", nil)
	if code, body := d.do("PATCH", "/api/xbin/users/adm", map[string]any{"role": "admin"}); code != 200 {
		t.Fatalf("making an admin: %d %s", code, body)
	}
	alice := d.addUser("alice", map[string]string{"*": "terminal", "xbin/*": "read"})
	state := func(tok string) map[string]any {
		t.Helper()
		code, body := d.do("GET", "/api/xbin/delegate/state", nil, tok)
		var out map[string]any
		if code != 200 || json.Unmarshal([]byte(body), &out) != nil {
			t.Fatalf("GET /delegate/state: %d %s", code, body)
		}
		return out
	}
	nothing := func(when string) {
		t.Helper()
		st := state(alice)
		if st["access"] != false || st["agent"] != nil || st["who"] != nil || st["held"] != nil {
			t.Errorf("%s: a person who wasn't given an agent reads %v", when, st)
		}
		for _, c := range [][2]string{
			{"GET", "/api/xbin/frame-token?component=" + wsAgentPath}, {"GET", "/c/" + wsAgentPath + "/"},
			{"GET", "/api/" + wsAgentPath + "/ws/state"}, {"POST", "/api/" + wsAgentPath + "/ws/sessions"},
		} {
			if code, body := d.do(c[0], c[1], nil, alice); code != 403 && code != 404 {
				t.Errorf("%s: %s %s as a person without an agent: %d %.200s", when, c[0], c[1], code, body)
			}
		}
		if code, body := d.do("GET", "/api/xbin/alerts", nil, alice); code != 200 || strings.Contains(body, "agent-page") || strings.Contains(body, "/xbin/agent") {
			t.Errorf("%s: their alerts: %d %s", when, code, body)
		}
		for _, q := range []string{"/components", "/components?sealed=1", "/partitions", "/screens", "/whoami", "/tile-status?component=" + wsAgentPath} {
			code, body := d.do("GET", "/api/xbin"+q, nil, alice)
			if strings.Contains(q, "tile-status") { // asked about by name: refused, as a tile they can't read is
				if code < 400 {
					t.Errorf("%s: GET %s answers them: %d %.300s", when, q, code, body)
				}
				continue
			}
			if code != 200 || strings.Contains(body, wsAgentPath) {
				t.Errorf("%s: GET %s names it to them: %d %.300s", when, q, code, body)
			}
		}
	}
	nothing("a workspace that did nothing")
	if code, body := d.do("GET", "/api/xbin/components", nil); code != 200 || strings.Contains(body, wsAgentPath) {
		t.Errorf("the owner's plain listing shows it (old shells and apps read that): %d", code)
	}
	// an admin: theirs to set up, held here, and nobody chosen yet
	st := state(admin)
	who, _ := st["who"].(map[string]any)
	if st["workspace"] != false || st["access"] != true || st["agent"] != wsAgentPath || who == nil || who["chosen"] != false || st["held"] != "unavailable" || st["heldWhy"] == nil {
		t.Errorf("an admin before setup: %v", st)
	}

	// the switch alone gives nobody an agent
	put := func(tok string, body map[string]any) (int, string) {
		return d.do("PUT", "/api/xbin/delegate/workspace", body, tok)
	}
	if code, body := put(alice, map[string]any{"enabled": true}); code != 403 {
		t.Errorf("a non-admin turning workspace agents on: %d %s", code, body)
	}
	if code, body := put(alice, map[string]any{"who": "everyone"}); code != 403 {
		t.Errorf("a non-admin choosing who gets one: %d %s", code, body)
	}
	if code, body := put(admin, map[string]any{"who": "all"}); code != 400 {
		t.Errorf("an unknown who: %d %s", code, body)
	}
	if code, body := put(admin, map[string]any{"enabled": true}); code != 200 || !strings.Contains(body, `"chosen":false`) {
		t.Fatalf("turning it on: %d %s", code, body)
	}
	nothing("workspace agents on, nobody chosen")

	// "me": the admin's own entry, and still nobody else
	if code, body := put(admin, map[string]any{"who": "me"}); code != 200 || !strings.Contains(body, `"entries":1`) || !strings.Contains(body, `"everyone":false`) || !strings.Contains(body, `"chosen":true`) {
		t.Fatalf("who: me: %d %s", code, body)
	}
	nothing("the admin chose themselves")

	// "everyone": the workspace defaults name it, and alice has one — held
	// here, which she is told without xbind's reason
	if code, body := put(admin, map[string]any{"who": "everyone"}); code != 200 || !strings.Contains(body, `"everyone":true`) {
		t.Fatalf("who: everyone: %d %s", code, body)
	}
	st = state(alice)
	if st["access"] != true || st["agent"] != wsAgentPath || st["held"] != "unavailable" || st["heldWhy"] != nil || st["who"] != nil {
		t.Errorf("a person who was given one: %v", st)
	}
	if code, body := d.do("GET", "/api/xbin/alerts", nil, alice); code != 200 || !strings.Contains(body, "agent-page") {
		t.Errorf("the notice that names its page, for a person who has one: %d %s", code, body)
	}
	if code, body := d.do("GET", "/api/xbin/defaults", nil); code != 200 || !strings.Contains(body, `"`+wsAgentPath+`":"read"`) {
		t.Errorf("the workspace defaults after: %d %s", code, body)
	}
	// "chosen": the defaults' entry goes, the admin's own stays
	if code, body := put(admin, map[string]any{"who": "chosen"}); code != 200 || !strings.Contains(body, `"everyone":false`) || !strings.Contains(body, `"entries":1`) {
		t.Fatalf("who: chosen: %d %s", code, body)
	}
	nothing("taken back from everyone")
	// one person, by an ordinary access entry
	if code, body := d.do("PATCH", "/api/xbin/users/alice", map[string]any{"tiles": map[string]string{"*": "terminal", wsAgentPath: "read"}}); code != 200 {
		t.Fatalf("an access entry for alice: %d %s", code, body)
	}
	if st = state(alice); st["access"] != true || st["agent"] != wsAgentPath {
		t.Errorf("alice, by an entry that names it: %v", st)
	}
	// …and still in none of the listings a shell or the app that predates
	// system components reads (the shipped iOS app: components, screens,
	// tile status, her partitions, bindings, grants): a row there would be
	// a tile with a dead terminal and a dead code tab. ?sealed=1 is the
	// switch a client that knows them sends.
	// (Not /partitions: a person's own data in it is theirs to see and
	// reset there, as for any tile that keeps people's data apart — and an
	// admin's "interfaces to bind" names its unbound slot: docs/compat.md.)
	for _, path := range []string{"/components", "/screens", "/tile-status", "/grants", "/access-matrix", "/term/sessions", "/backends", "/runtime", "/resources"} {
		for who, tok := range map[string]string{"a person who has one": alice, "an admin": admin} {
			code, body := d.do("GET", "/api/xbin"+path, nil, tok)
			if code == 200 && strings.Contains(body, wsAgentPath) {
				t.Errorf("GET %s lists the agent to %s: %.300s", path, who, body)
			}
		}
	}
	if code, body := d.do("GET", "/api/xbin/components?sealed=1", nil, alice); code != 200 || !strings.Contains(body, wsAgentPath) {
		t.Errorf("GET /components?sealed=1 for a person who has one: %d %.200s", code, body)
	}
	// the switch off: she keeps her entry and has no agent to talk to
	if code, body := put(admin, map[string]any{"enabled": false}); code != 200 {
		t.Fatalf("turning it off: %d %s", code, body)
	}
	if st = state(alice); st["workspace"] != false || st["workspaceAt"] == nil {
		t.Errorf("after the switch went off: %v", st)
	}
	if code, body := d.do("GET", "/api/xbin/alerts", nil, alice); code != 200 || strings.Contains(body, "agent-page") {
		t.Errorf("the notice while workspace agents are off: %d %s", code, body)
	}

	// through all of it nothing of the agent was built or started
	for _, dir := range []string{filepath.Join(ws, ".xbin", "build"), filepath.Join(ws, ".xbin", "run")} {
		_ = filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
			if err == nil && strings.Contains(filepath.Base(p), "wsagent") {
				t.Errorf("%s exists: something of the agent was built or run", p)
			}
			return nil
		})
	}
	if code, body := d.do("GET", "/api/xbin/sealed", nil); code != 200 || !strings.Contains(body, `"path":"`+wsAgentPath+`"`) || strings.Contains(body, `"backend":"healthy"`) || strings.Contains(body, `"backend":"starting"`) || strings.Contains(body, `"backend":"building"`) {
		t.Errorf("GET /sealed: %d %s", code, body)
	}
}

// agentRelease is the release's own row with one file of the agent's page
// changed: what the next release's row is, as far as xbind can tell.
func agentRelease(t *testing.T, mark string) []sealed.Spec {
	t.Helper()
	tab := sealed.Table()
	if mark == "" {
		return tab
	}
	next := fstest.MapFS{}
	err := fs.WalkDir(tab[0].Source, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(tab[0].Source, p)
		if err != nil {
			return err
		}
		if p == "index.html" {
			b = append(b, []byte("\n<!-- "+mark+" -->\n")...)
		}
		next[p] = &fstest.MapFile{Data: b}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tab[0].Source = next
	return tab
}

// An upgrade to a release whose agent differs swaps the agent with it, on
// the real row: the page an admin is served is the new release's (its code
// id moves, the old tree is gone from .xbin/sealed), and the backend xbind
// will run is the new source's build and no other — the previous release's
// binary, still in bin/sealed after a half-finished install, holds the
// component for want of its build instead of being run.
func TestWorkspaceAgentFollowsTheRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace twice")
	}
	quiet(t)
	ws := probeWorkspace(t)
	r1, r2 := agentRelease(t, ""), agentRelease(t, "release two")
	s1, err := sealed.Load(r1)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := sealed.Load(r2)
	if err != nil {
		t.Fatal(err)
	}
	c1, _ := s1.Component(wsAgentPath)
	c2, _ := s2.Component(wsAgentPath)
	if c1.Digest == c2.Digest || sealed.BuildName(c1) == sealed.BuildName(c2) {
		t.Fatalf("a changed page gives the same release: %s / %s", sealed.BuildName(c1), sealed.BuildName(c2))
	}
	// bin/sealed as release one's install left it: its build, listed
	bin := t.TempDir()
	prog := []byte("#!/bin/sh\nexit 1\n")
	if err := os.WriteFile(filepath.Join(bin, sealed.BuildName(c1)), prog, 0o755); err != nil {
		t.Fatal(err)
	}
	sum, size, _ := sealed.FileSum(filepath.Join(bin, sealed.BuildName(c1)))
	man, _ := json.Marshal(sealed.BuildManifest{Schema: 1, Builds: map[string]sealed.BuildEntry{
		sealed.BuildName(c1): {Component: wsAgentPath, Source: c1.Digest, SHA256: sum, Size: size},
	}})
	if err := os.WriteFile(filepath.Join(bin, sealed.ManifestName), man, 0o644); err != nil {
		t.Fatal(err)
	}
	type view struct {
		page, code, build, hold string
		trees                   []string
	}
	look := func(table []sealed.Spec) view {
		t.Helper()
		d := startSealed(t, ws, table, production(bin))
		defer d.stop()
		var v view
		code, page := d.do("GET", "/c/"+wsAgentPath+"/index.html", nil)
		if code != 200 {
			t.Fatalf("the agent's page for the owner: %d %.200s", code, page)
		}
		v.page = page
		if _, rest, ok := strings.Cut(page, `name="xbin-code" content="`); ok {
			v.code, _, _ = strings.Cut(rest, `"`)
		}
		var out struct {
			Components []struct {
				Path, Build string
				Hold        *struct{ Code, Reason string }
			}
		}
		_, body := d.do("GET", "/api/xbin/sealed", nil)
		_ = json.Unmarshal([]byte(body), &out)
		for _, c := range out.Components {
			if c.Path == wsAgentPath {
				v.build = c.Build
				if c.Hold != nil {
					v.hold = c.Hold.Code + ": " + c.Hold.Reason
				}
			}
		}
		ents, _ := os.ReadDir(filepath.Join(ws, ".xbin", "sealed", "wsagent"))
		for _, e := range ents {
			v.trees = append(v.trees, e.Name())
		}
		return v
	}
	one := look(r1)
	if one.code == "" || len(one.trees) != 1 || one.build != "release" || strings.Contains(one.hold, "no-build") {
		t.Fatalf("release one: code %q trees %v build %q hold %q", one.code, one.trees, one.build, one.hold)
	}
	two := look(r2)
	if !strings.Contains(two.page, "<!-- release two -->") || strings.Contains(one.page, "<!-- release two -->") {
		t.Error("the page after the upgrade is not the new release's")
	}
	if two.code == "" || two.code == one.code {
		t.Errorf("the page's code id: %q before, %q after", one.code, two.code)
	}
	if len(two.trees) != 1 || two.trees[0] == one.trees[0] {
		t.Errorf(".xbin/sealed/wsagent after the upgrade: %v (before: %v)", two.trees, one.trees)
	}
	// (without sandboxes the hold named first is that one; the row's build
	// says what the install has for this source)
	if two.build != "none" || two.hold == "" {
		t.Errorf("release two beside release one's build: build %q, hold %q; want no build of its own, and a hold", two.build, two.hold)
	}
}

// A store written by a newer release than the one running holds the agent
// (an xbin that was downgraded): nothing of its data is opened, an admin is
// told why, and a person who has one reads "older" — the chrome's "waiting
// for a newer xbin" — with the rest of their agent's chrome as it was.
func TestWorkspaceAgentHeldByNewerData(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := probeWorkspace(t)
	stamp := filepath.Join(ws, "data", "sealed", "wsagent.json")
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stamp, []byte(`{"data":2,"release":"v9.9.9","digest":"feedface","at":"2026-11-15T00:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := startSealed(t, ws, sealed.Table(), production(filepath.Join(t.TempDir(), "no-sealed-bin")))
	code, body := d.do("GET", "/api/xbin/sealed", nil)
	if code != 200 || !strings.Contains(body, `"code":"`+sealed.HoldNewerData+`"`) {
		t.Fatalf("GET /sealed with a newer stamp: %d %s", code, body)
	}
	admin := d.addUser("adm", nil)
	if code, body := d.do("PATCH", "/api/xbin/users/adm", map[string]any{"role": "admin"}); code != 200 {
		t.Fatalf("making an admin: %d %s", code, body)
	}
	alice := d.addUser("alice", map[string]string{wsAgentPath: "read"})
	if code, body := d.do("PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true}, admin); code != 200 {
		t.Fatalf("turning workspace agents on: %d %s", code, body)
	}
	for who, tok := range map[string]string{"an admin": admin, "a person who has one": alice} {
		code, body := d.do("GET", "/api/xbin/delegate/state", nil, tok)
		var st map[string]any
		_ = json.Unmarshal([]byte(body), &st)
		if code != 200 || st["held"] != "older" || st["agent"] != wsAgentPath || (st["heldWhy"] != nil) != (tok == admin) {
			t.Errorf("%s: %d %v", who, code, st)
		}
	}
	// its page's own call to its backend is refused
	_, tok := d.do("GET", "/api/xbin/frame-token?component="+wsAgentPath, nil, alice)
	var ft struct{ Token string }
	_ = json.Unmarshal([]byte(tok), &ft)
	req, _ := http.NewRequest("GET", d.base+"/api/"+wsAgentPath+"/ws/state", nil)
	req.Header.Set("X-XBin-Frame-Token", ft.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if ft.Token == "" || resp.StatusCode < 400 { // (this xbind has no sandboxes either, which is the reason named first)
		t.Errorf("its API while held: %d %s", resp.StatusCode, rb)
	}
	// the stamp keeps its format and its writer (the mode its data was made
	// in is taken down beside them: it had none)
	var kept struct {
		Data    int
		Release string
		Mode    string
	}
	if b, err := os.ReadFile(stamp); err != nil || json.Unmarshal(b, &kept) != nil || kept.Data != 2 || kept.Release != "v9.9.9" || kept.Mode != "user" {
		t.Errorf("the stamp after the boot: %s %v", b, err)
	}
}
