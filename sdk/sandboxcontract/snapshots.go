package sandboxcontract

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

// --- snapshots and clones ------------------------------------------------------------------

var snapshotChecks = []check{
	{"snapshots", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "snap"})
		id, wd := sb.ID, sb.Workdir
		a.Put(id, wd+"/f", "v1", "")
		var s1, again Snapshot
		a.Call("POST", "/sandboxes/"+id+"/snapshots", map[string]any{"name": "one", "clientId": "c"}, http.StatusCreated, &s1)
		if s1.ID == "" || s1.Name != "one" || s1.Created == 0 {
			t.Fatalf("snapshot: %+v", s1)
		}
		a.Call("POST", "/sandboxes/"+id+"/snapshots", map[string]any{"name": "one", "clientId": "c"}, http.StatusOK, &again)
		if again.ID != s1.ID {
			t.Fatalf("a repeated clientId: %+v", again)
		}
		a.Refused("POST", "/sandboxes/"+id+"/snapshots", map[string]any{"name": "two", "clientId": "c"}, 409, "exists")
		var l struct{ Snapshots []Snapshot }
		a.Call("GET", "/sandboxes/"+id+"/snapshots", nil, 200, &l)
		if len(l.Snapshots) != 1 || l.Snapshots[0].ID != s1.ID {
			t.Fatalf("snapshots: %+v", l)
		}
		// restore: the files roll back and the execs are killed
		a.Put(id, wd+"/f", "v2", "")
		a.Put(id, wd+"/g", "new", "")
		x := a.Exec(id, map[string]any{"cmd": "sleep 30"})
		var r Sandbox
		a.Call("POST", "/sandboxes/"+id+"/snapshots/"+s1.ID+"/restore", nil, 200, &r)
		if r.ID != id {
			t.Fatalf("restore answers the sandbox: %+v", r)
		}
		if got := a.Read(id, wd+"/f"); got != "v1" {
			t.Fatalf("restored: %q", got)
		}
		a.Refused("GET", "/sandboxes/"+id+"/files/stat?path="+q(wd+"/g"), nil, 404, "not-found")
		eventually(t, 3*time.Second+e.tg.grace(), "a restore kills the execs", func() bool {
			var g Exec
			a.Call("GET", "/sandboxes/"+id+"/execs/"+x.ID, nil, 200, &g)
			return g.State == "killed" || g.State == "lost"
		})
		// delete
		a.Call("DELETE", "/sandboxes/"+id+"/snapshots/"+s1.ID, nil, http.StatusNoContent, nil)
		a.Call("GET", "/sandboxes/"+id+"/snapshots", nil, 200, &l)
		if len(l.Snapshots) != 0 {
			t.Fatalf("after delete: %+v", l)
		}
		a.Refused("POST", "/sandboxes/"+id+"/snapshots/"+s1.ID+"/restore", nil, 404, "not-found")
		a.Refused("DELETE", "/sandboxes/"+id+"/snapshots/"+s1.ID, nil, 404, "not-found")
	}},
	{"clones", func(t *testing.T, e *env) {
		if !e.has("clone") {
			t.Skip("no clone capability")
		}
		a, b := e.as("a"), e.as("b")
		sb := a.Create(map[string]any{"name": "origin"})
		id, wd := sb.ID, sb.Workdir
		a.Put(id, wd+"/f", "v1", "")
		var s1 Snapshot
		a.Call("POST", "/sandboxes/"+id+"/snapshots", map[string]any{"name": "one"}, http.StatusCreated, &s1)
		// of the sandbox now, of a snapshot
		a.Put(id, wd+"/f", "v3", "")
		c1 := a.Create(map[string]any{"name": "clone", "from": map[string]any{"sandbox": id}})
		if c1.ID == id || a.Read(c1.ID, c1.Workdir+"/f") != "v3" {
			t.Fatalf("a clone of the sandbox: %+v", c1)
		}
		c2 := a.Create(map[string]any{"name": "from-snap", "from": map[string]any{"sandbox": id, "snapshot": s1.ID}})
		if got := a.Read(c2.ID, c2.Workdir+"/f"); got != "v1" {
			t.Fatalf("a clone of the snapshot: %q", got)
		}
		a.Put(id, wd+"/f", "v4", "")
		if got := a.Read(c1.ID, c1.Workdir+"/f"); got != "v3" {
			t.Fatalf("a clone is a copy: %q", got)
		}
		b.Refused("POST", "/sandboxes", map[string]any{"name": "steal", "from": map[string]any{"sandbox": id}}, 404, "not-found")
		a.Refused("POST", "/sandboxes", map[string]any{"name": "x", "from": map[string]any{"sandbox": id, "snapshot": "nope"}}, 404, "not-found")
	}},
}

// --- capabilities ----------------------------------------------------------------------------

// optional capabilities, and a request of each that its absence refuses
var optionalCaps = []string{"tar", "tty", "stdio", "snapshots", "clone", "archive", "ports"}

var capChecks = []check{
	{"missing", func(t *testing.T, e *env) {
		f, fresh := e.fresh(Knobs{Caps: []string{"exec", "files"}})
		missing := []string{}
		for _, c := range optionalCaps {
			if !slices.Contains(f.hello.Caps, c) {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			if fresh {
				t.Fatalf("a Fresh manager asked for caps exec, files offers %v", f.hello.Caps)
			}
			t.Skip("the manager offers every capability (a Target.Fresh honouring Knobs.Caps checks their absence)")
		}
		a := f.as("a")
		sb := a.Create(map[string]any{"name": "plain"})
		for _, c := range missing {
			if slices.Contains(sb.Caps, c) {
				t.Fatalf("a sandbox's caps %v offer %s, hello's %v don't", sb.Caps, c, f.hello.Caps)
			}
		}
		p := "/sandboxes/" + sb.ID
		refusals := map[string][]struct {
			method, path string
			body         any
		}{
			"tar": {{"GET", p + "/tar?path=" + q(sb.Workdir), nil}, {"PUT", p + "/tar?path=" + q(sb.Workdir), []byte{}}},
			"snapshots": {{"GET", p + "/snapshots", nil}, {"POST", p + "/snapshots", map[string]any{"name": "s"}},
				{"POST", p + "/snapshots/s1/restore", nil}, {"DELETE", p + "/snapshots/s1", nil}},
			"clone":   {{"POST", "/sandboxes", map[string]any{"name": "c", "from": map[string]any{"sandbox": sb.ID}}}},
			"archive": {{"POST", p + "/archive", nil}, {"POST", p + "/thaw", nil}},
			"ports":   {{"GET", p + "/ports/8000/", nil}, {"POST", p + "/ports/8000/x", []byte{}}},
		}
		for _, c := range missing {
			switch c {
			case "tty":
				noTTY(t, a)
				continue
			case "stdio":
				noStdio(t, a)
				continue
			}
			for _, r := range refusals[c] {
				a.Refused(r.method, r.path, r.body, 501, "unsupported")
			}
		}
		a.Sh(sb.ID, "true") // exec and files still work
		a.Put(sb.ID, sb.Workdir+"/f", "x", "")
	}},
}
