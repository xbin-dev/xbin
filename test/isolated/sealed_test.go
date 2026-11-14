//go:build linux && integration

package isolated

// sealed_test.go — sealed components (D227) through a real xbind, as an
// install runs one: built with the fixture table (-tags sealedfixture, the
// rows embedded as a release embeds its own), its backends prebuilt by its
// own `xbind sealed-build` into bin/sealed, booted without --dev under
// --isolate with owner auth and the vault. What the in-process tests
// (internal/boot) can't show: the sandboxed backend from the release's
// build, the frontend from xbind's tree, people's partitions of a sealed
// scope, and the hold when the build is missing.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
	"github.com/xbin-dev/xbin/test/xbindtest"
)

const (
	sealedProbe  = "xbin/probe"  // one instance
	sealedPeople = "xbin/people" // one per person, and global
)

// sealedWho is what the fixture backend's /who answers.
type sealedWho struct {
	Self      string `json:"self"`
	Partition string `json:"partition"`
	User      string `json:"user"`
	Exe       string `json:"exe"`
	Cwd       string `json:"cwd"`
	Code      string `json:"code"`
	Caller    struct{ From, Role, User string }
}

type sealedEnv struct {
	d      *xbindtest.Daemon
	sess   map[string]string
	frames map[string]xbindtest.Header
}

func (e *sealedEnv) as(person string) []xbindtest.Header {
	if person == "" {
		return nil
	}
	return []xbindtest.Header{xbindtest.H("Authorization", "Bearer "+e.sess[person])}
}

// frame is person's frame token of tile ("" = the owner's), minted once.
func (e *sealedEnv) frame(t *testing.T, tile, person string) xbindtest.Header {
	t.Helper()
	k := tile + "\x00" + person
	if h, ok := e.frames[k]; ok {
		return h
	}
	var out struct{ Token string }
	e.d.Must(t, "GET", "/api/xbin/frame-token?component="+tile, nil, 200, e.as(person)...).Decode(t, &out)
	if out.Token == "" {
		t.Fatalf("no frame token of %s for %q", tile, person)
	}
	e.frames[k] = xbindtest.FrameHeader(out.Token)
	return e.frames[k]
}

// who waits for the fixture backend at tile to answer /who through hdrs.
func (e *sealedEnv) who(t *testing.T, tile string, hdrs ...xbindtest.Header) sealedWho {
	t.Helper()
	var r xbindtest.Resp
	xbindtest.Eventually(t, 2*time.Minute, "GET /api/"+tile+"/who answers", func() (bool, string) {
		r = e.d.Call(t, "GET", "/api/"+tile+"/who", nil, hdrs...)
		if r.Status == 401 || r.Status == 403 || r.Status == 404 || r.Status == 409 {
			t.Fatalf("GET /api/%s/who: %d %s", tile, r.Status, r)
		}
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	var w sealedWho
	r.Decode(t, &w)
	return w
}

type sealedRowView struct {
	Path, Build, State, Backend, Digest string
	Partitioned                         bool
	Hold                                *struct{ Code, Reason string }
}

func (e *sealedEnv) rows(t *testing.T) map[string]sealedRowView {
	t.Helper()
	var out struct{ Components []sealedRowView }
	e.d.Must(t, "GET", "/api/xbin/sealed", nil, 200).Decode(t, &out)
	rows := map[string]sealedRowView{}
	for _, r := range out.Components {
		rows[r.Path] = r
	}
	return rows
}

func TestSealedComponentsOnAProductionXbind(t *testing.T) {
	a := xbindtest.Require(t)
	if a.Gocryptfs == "" {
		t.Skip("no gocryptfs: people's partitions keep their data in encrypted volumes")
	}
	a = a.WithSealedFixture(t)
	builds, _ := os.ReadDir(filepath.Join(a.Bin, "sealed"))
	if len(builds) != 2 {
		t.Fatalf("sealed-build made %d backends, want the fixture's two", len(builds))
	}
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true, Production: true,
		Env: []string{"XBIN_VAULT_PASSPHRASE=xbindtest-vault"}})
	e := &sealedEnv{d: d, sess: map[string]string{}, frames: map[string]xbindtest.Header{}}
	for _, id := range []string{"alice", "bob"} {
		d.AddUser(t, id, "pw-"+id+"-5c2e81", "user", map[string]string{sealedPeople: "read", sealedProbe: "read"})
		e.sess[id] = d.Login(t, id, "pw-"+id+"-5c2e81")
	}
	d.AddUser(t, "mallory", "pw-mallory-5c2e81", "user", map[string]string{"*": "terminal"})
	e.sess["mallory"] = d.Login(t, "mallory", "pw-mallory-5c2e81")

	t.Run("listed for those who ask", func(t *testing.T) {
		rows := e.rows(t)
		for _, p := range []string{sealedProbe, sealedPeople} {
			if r := rows[p]; r.Build != "release" || r.State != "enabled" || r.Hold != nil || len(r.Digest) != 24 {
				t.Errorf("GET /sealed %s: %+v", p, r)
			}
		}
		if !rows[sealedPeople].Partitioned || rows[sealedProbe].Partitioned {
			t.Errorf("partitioned: %+v", rows)
		}
		if r := d.Must(t, "GET", "/api/xbin/components", nil, 200); strings.Contains(r.String(), `"xbin/`) {
			t.Errorf("GET /components lists a sealed component unasked: %s", r)
		}
		var comps []struct {
			Path   string
			Sealed bool
		}
		d.Must(t, "GET", "/api/xbin/components?sealed=1", nil, 200, e.as("alice")...).Decode(t, &comps)
		n := 0
		for _, c := range comps {
			if strings.HasPrefix(c.Path, "xbin/") {
				n++
				if !c.Sealed {
					t.Errorf("%s isn't marked sealed", c.Path)
				}
			}
		}
		if n != 2 {
			t.Errorf("alice's /components?sealed=1 has %d sealed rows, want 2", n)
		}
	})

	t.Run("the release's build runs in the sandbox", func(t *testing.T) {
		w := e.who(t, sealedProbe, e.frame(t, sealedProbe, "alice"))
		if w.Self != sealedProbe || w.Code != "fixture-1" || w.Partition != "" || w.Caller.User != "alice" {
			t.Errorf("/who: %+v", w)
		}
		if w.Exe != "/run/backend" {
			t.Errorf("the backend's executable is %s, want the bound release build", w.Exe)
		}
		if !strings.Contains(w.Cwd, "/.xbin/sealed/probe/") {
			t.Errorf("its working directory is %s, want xbind's materialized tree", w.Cwd)
		}
		// nothing was compiled on this host for it
		if _, err := os.Stat(filepath.Join(d.WS, ".xbin", "build", util.CompKey(sealedProbe))); err == nil {
			t.Error("a production xbind made a build directory for a sealed component")
		}
		if _, err := os.Stat(filepath.Join(d.WS, "xbin")); err == nil {
			t.Error("a directory appeared at <workspace>/xbin")
		}
		r := d.Must(t, "GET", "/c/"+sealedProbe+"/", nil, 200, e.as("alice")...)
		if !strings.Contains(r.String(), "fixture-1") || !strings.Contains(r.String(), "xbin-client") {
			t.Errorf("its page: %s", r)
		}
		if rows := e.rows(t); rows[sealedProbe].Backend != "healthy" {
			t.Errorf("GET /sealed after a call: %+v", rows[sealedProbe])
		}
	})

	t.Run("people's partitions of a sealed scope", func(t *testing.T) {
		for _, person := range []string{"alice", "bob"} {
			fr := e.frame(t, sealedPeople, person)
			w := e.who(t, sealedPeople, fr)
			if w.Partition != "user:"+person || w.User != person || w.Self != sealedPeople {
				t.Fatalf("%s's instance: %+v", person, w)
			}
			d.Must(t, "PUT", "/api/"+sealedPeople+"/kv/note", person+"'s note", 200, fr)
		}
		for _, person := range []string{"alice", "bob"} {
			var v struct{ Value string }
			d.Must(t, "GET", "/api/"+sealedPeople+"/kv/note", nil, 200, e.frame(t, sealedPeople, person)).Decode(t, &v)
			if !strings.Contains(v.Value, person+"'s note") {
				t.Errorf("%s reads %q from their partition", person, v.Value)
			}
		}
		// each person's data is in a namespace of its own, under the sealed
		// scope's injective key (never beside a workspace scope's)
		parts, _ := os.ReadDir(filepath.Join(d.WS, "data", "resources-enc", ".partitions", "xbin~people", "main"))
		if len(parts) != 2 {
			t.Errorf("%d partition namespaces under data/resources-enc/.partitions/xbin~people/main, want 2", len(parts))
		}
		var list struct {
			Partitions []struct{ User string }
		}
		r := d.Must(t, "GET", "/api/xbin/partitions?tile="+sealedPeople, nil, 200)
		if json.Unmarshal(r.Body, &list) != nil || !strings.Contains(r.String(), "alice") || !strings.Contains(r.String(), "bob") {
			t.Errorf("GET /partitions?tile=%s: %s", sealedPeople, r)
		}
	})

	t.Run("no way in but its page", func(t *testing.T) {
		// a tile's own authority never reaches it or a person's partition of
		// it: the fixture's other component calls with its backend's token
		fr := e.frame(t, sealedProbe, "alice")
		var relay struct {
			Status int
			Body   string
		}
		d.Must(t, "GET", "/api/"+sealedProbe+"/call?path=/api/"+sealedPeople+"/who", nil, 200, fr).Decode(t, &relay)
		if relay.Status != 403 {
			t.Errorf("a backend's call into a sealed component: %d %s", relay.Status, relay.Body)
		}
		// and the levels above read don't exist, whatever a pattern says
		if r := d.Call(t, "GET", "/ws/term?cwd="+sealedProbe, nil, e.as("mallory")...); r.Status != 403 || !strings.Contains(r.String(), "system component") {
			t.Errorf("a terminal for someone with terminal on *: %d %s", r.Status, r)
		}
		if r := d.Call(t, "POST", "/api/xbin/term/sessions", map[string]any{"cwd": sealedProbe, "kind": "agent", "provider": "claude"}, e.as("mallory")...); r.Status != 400 || !strings.Contains(r.String(), "system component") {
			t.Errorf("an agent session: %d %s", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/xbin/code/tree?component="+sealedProbe, nil); r.Status != 400 {
			t.Errorf("the source, for the owner: %d %s", r.Status, r)
		}
		if r := d.Call(t, "POST", "/api/xbin/grants", map[string]any{"from": "apps/welcome", "target": sealedPeople, "role": "admin"}); r.Status != 400 {
			t.Errorf("a grant on it: %d %s", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/xbin/logs?component="+sealedProbe, nil); r.Status != 200 || !strings.Contains(r.String(), "gen 1 start") {
			t.Errorf("an admin's read of its log: %d %s", r.Status, r)
		}
		if r := d.Call(t, "GET", "/api/xbin/logs?component="+sealedProbe, nil, e.as("alice")...); r.Status != 403 {
			t.Errorf("a reader's read of its log: %d %s", r.Status, r)
		}
	})

	t.Run("held without its build, running again with it", func(t *testing.T) {
		var probe string
		for _, b := range builds {
			if strings.HasPrefix(b.Name(), "probe-") {
				probe = filepath.Join(a.Bin, "sealed", b.Name())
			}
		}
		relogin := func() {
			e.frames = map[string]xbindtest.Header{}
			for _, id := range []string{"alice", "bob"} {
				e.sess[id] = d.Login(t, id, "pw-"+id+"-5c2e81")
			}
		}
		if err := os.Rename(probe, probe+".away"); err != nil { // read at boot: the running xbind keeps its build
			t.Fatal(err)
		}
		d.Restart(t)
		relogin()
		r := d.Call(t, "GET", "/api/"+sealedProbe+"/who", nil, e.frame(t, sealedProbe, "alice"))
		if r.Status != 502 || !strings.Contains(r.String(), "this xbin install has no build of it") {
			t.Errorf("a call without the release's build: %d %s", r.Status, r)
		}
		rows := e.rows(t)
		if h := rows[sealedProbe].Hold; h == nil || h.Code != "no-build" {
			t.Errorf("GET /sealed: %+v", rows[sealedProbe])
		}
		if rows[sealedPeople].Hold != nil {
			t.Errorf("the other component is held too: %+v", rows[sealedPeople])
		}
		if _, err := os.Stat(filepath.Join(d.WS, ".xbin", "build", util.CompKey(sealedProbe))); err == nil {
			t.Error("xbind compiled the component it had no build of")
		}
		// its people's data is as it was
		var v struct{ Value string }
		d.Must(t, "GET", "/api/"+sealedPeople+"/kv/note", nil, 200, e.frame(t, sealedPeople, "bob")).Decode(t, &v)
		if !strings.Contains(v.Value, "bob's note") {
			t.Errorf("bob's note after a restart: %q", v.Value)
		}
		if err := os.Rename(probe+".away", probe); err != nil {
			t.Fatal(err)
		}
		d.Restart(t)
		relogin()
		if w := e.who(t, sealedProbe, e.frame(t, sealedProbe, "alice")); w.Code != "fixture-1" {
			t.Errorf("with its build back: %+v", w)
		}
	})
}
