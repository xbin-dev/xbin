package sandboxcontract

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// --- hello and errors ---------------------------------------------------------

var helloChecks = []check{
	{"hello", func(t *testing.T, e *env) {
		a := e.as("a")
		var h Hello
		a.Call("GET", "/hello?protocol=1", nil, 200, &h)
		if h.Protocol != 1 || !slices.Contains(h.Protocols, 1) || h.Manager.Name == "" || h.Manager.Version == "" {
			t.Fatalf("hello: %+v", h)
		}
		for _, c := range []string{"exec", "files"} { // required in protocol 1
			if !slices.Contains(h.Caps, c) {
				t.Errorf("caps %v lack %s", h.Caps, c)
			}
		}
		for _, c := range e.tg.Caps { // what the target expects
			if !slices.Contains(h.Caps, c) {
				t.Errorf("caps %v lack %s (Target.Caps)", h.Caps, c)
			}
		}
		for _, c := range h.Caps {
			if !slices.Contains([]string{"exec", "files", "tar", "tty", "snapshots", "clone", "archive", "ports"}, c) {
				t.Errorf("caps: unknown %q", c)
			}
		}
		for _, x := range h.Egress {
			if !slices.Contains([]string{"none", "internet", "open"}, x) {
				t.Errorf("egress: unknown %q", x)
			}
		}
		defaults := 0
		for _, im := range h.Images {
			if im.Default {
				defaults++
			}
		}
		if defaults != 1 {
			t.Errorf("images %+v: want one default", h.Images)
		}
		for _, k := range []string{"sandboxes", "runTimeoutMaxMs", "runOutputMax", "execsRunning", "outputRing", "stdinMax", "fileMax", "tarMax", "waitMaxSec"} {
			if _, ok := h.Limits[k]; !ok {
				t.Errorf("limits lack %s: %v", k, h.Limits)
			}
		}
		a.Call("GET", "/hello", nil, 200, nil)
		if r := a.Refused("GET", "/hello?protocol=2", nil, 400, "protocol"); !slices.Contains(r.Protocols, 1) {
			t.Errorf("protocol refusal lists %v", r.Protocols)
		}
		a.Refused("GET", "/no-such-route", nil, 404, "not-found") // errors are JSON everywhere
	}},
}

// --- sandboxes ------------------------------------------------------------------

var ctID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var sandboxChecks = []check{
	{"create-get-list", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": " api-dev ", "labels": map[string]string{"xbin.agent/conversation": "42"}})
		switch {
		case !ctID.MatchString(sb.ID), sb.Name != "api-dev", sb.State != "running", sb.Owner.Via != a.from,
			sb.Owner.User != "", sb.Owner.Asserted, sb.Visibility != "private", sb.Members == nil, len(sb.Members) != 0,
			sb.Shares == nil, sb.Shared, sb.Labels["xbin.agent/conversation"] != "42", !strings.HasPrefix(sb.Workdir, "/"),
			!strings.HasPrefix(sb.Home, "/"), sb.Created == 0, sb.Version < 1, sb.Image.ID == "", sb.Egress == "",
			!slices.Contains(sb.Caps, "exec"), !slices.Contains(sb.Caps, "files"):
			t.Fatalf("created: %+v", sb)
		}
		if got := a.Get(sb.ID); got.ID != sb.ID || got.Name != "api-dev" || got.Version != sb.Version {
			t.Fatalf("get: %+v", got)
		}
		stopped := a.Create(map[string]any{"name": "cold", "start": false})
		if stopped.State != "stopped" {
			t.Fatalf("start:false: %s", stopped.State)
		}
		if l := a.List(); len(l) != 2 || l[sb.ID].Name != "api-dev" || l[stopped.ID].State != "stopped" {
			t.Fatalf("list: %+v", l)
		}
		a.Refused("GET", "/sandboxes/nope", nil, 404, "not-found")
		for _, bad := range []map[string]any{{"name": ""}, {"name": strings.Repeat("n", 65)}, {"name": "x", "image": "no-such"},
			{"name": "x", "size": "no-such"}, {"name": "x", "egress": "everything"}, {"name": "x", "visibility": "world"}} {
			a.Refused("POST", "/sandboxes", bad, 400, "invalid")
		}
		a.Refused("POST", "/sandboxes", map[string]any{"name": "x", "labels": map[string]string{"k": strings.Repeat("v", 1100)}}, 413, "too-large")
	}},
	{"idempotent", func(t *testing.T, e *env) {
		a, b := e.as("a"), e.as("b")
		first := a.Create(map[string]any{"name": "one", "clientId": "c1"})
		var again Sandbox
		a.Call("POST", "/sandboxes", map[string]any{"name": "one", "clientId": "c1"}, http.StatusOK, &again)
		if again.ID != first.ID {
			t.Fatalf("a repeated clientId made %s, want %s", again.ID, first.ID)
		}
		a.Refused("POST", "/sandboxes", map[string]any{"name": "two", "clientId": "c1"}, 409, "exists")
		if other := b.Create(map[string]any{"name": "one", "clientId": "c1"}); other.ID == first.ID { // unique per consumer
			t.Fatalf("another consumer's clientId returned %s", other.ID)
		}
		if n := len(a.List()); n != 1 {
			t.Fatalf("a has %d sandboxes, want 1", n)
		}
	}},
	{"patch-delete", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "p"})
		var p Sandbox
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "renamed", "labels": map[string]string{"k": "v"}, "autoStopMin": 5}, 200, &p)
		if p.Name != "renamed" || p.Labels["k"] != "v" || p.AutoStopMin != 5 || p.Version <= sb.Version {
			t.Fatalf("patched: %+v", p)
		}
		// version: a lost update is refused
		a.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "stale", "version": sb.Version}, 412, "precondition")
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "fresh", "version": p.Version}, 200, &p)
		if p.Name != "fresh" {
			t.Fatalf("a PATCH with the current version: %+v", p)
		}
		// a refused PATCH changes nothing
		a.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "half", "visibility": "world"}, 400, "invalid")
		a.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "half", "size": "no-such"}, 400, "invalid")
		a.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "half", "labels": map[string]string{"k": strings.Repeat("v", 1100)}}, 413, "too-large")
		if got := a.Get(sb.ID); got.Name != "fresh" || got.Version != p.Version {
			t.Fatalf("after refused PATCHes: %+v", got)
		}
		// egress on a running sandbox applies at the next start: egress is what
		// it has now, egressNext what it takes then (and absent when there is none)
		do := func(method, path string, body any) (out Sandbox) {
			t.Helper()
			a.Call(method, "/sandboxes/"+sb.ID+path, body, 200, &out)
			return out
		}
		was, other := p.Egress, "internet"
		if was == "internet" {
			other = "none"
		}
		if slices.Contains(e.hello.Egress, other) { // (a manager with one egress has no change to check)
			egressChanges(t, a, sb, was, other, do)
		}
		a.Call("DELETE", "/sandboxes/"+sb.ID, nil, http.StatusNoContent, nil)
		a.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
		a.Refused("DELETE", "/sandboxes/"+sb.ID, nil, 404, "not-found")
		if len(a.List()) != 0 {
			t.Fatal("a deleted sandbox is still listed")
		}
	}},
}

// egressChanges: a PATCHed egress applies at the next start (egressNext
// until then), at once on a stopped sandbox.
func egressChanges(t *testing.T, a Caller, sb Sandbox, was, other string, do func(method, path string, body any) Sandbox) {
	t.Helper()
	if p := do("PATCH", "", map[string]any{"egress": other}); p.Egress != was || p.EgressNext != other || !p.RestartNeeded {
		t.Fatalf("egress change on a running sandbox: %+v", p)
	}
	if got := a.Get(sb.ID); got.Egress != was || got.EgressNext != other {
		t.Fatalf("a pending egress, read back: %+v", got)
	}
	if p := do("PATCH", "", map[string]any{"egress": was}); p.Egress != was || p.EgressNext != "" || p.RestartNeeded {
		t.Fatalf("egress set back to what it has: %+v", p)
	}
	// a stop keeps it pending — or applies it, as a PATCH of a stopped
	// sandbox does (a substrate may keep no running egress past the stop);
	// either way the start takes it
	do("PATCH", "", map[string]any{"egress": other})
	if p := do("POST", "/stop?wait=5", nil); !(p.Egress == was && p.EgressNext == other) && !(p.Egress == other && p.EgressNext == "") {
		t.Fatalf("a pending egress on a stopped sandbox: %+v", p)
	}
	if p := do("POST", "/start?wait=5", nil); p.Egress != other || p.EgressNext != "" {
		t.Fatalf("started: %+v", p)
	}
	// on a stopped sandbox it applies at once
	do("POST", "/stop?wait=5", nil)
	if p := do("PATCH", "", map[string]any{"egress": was}); p.Egress != was || p.EgressNext != "" || p.RestartNeeded {
		t.Fatalf("egress change on a stopped sandbox: %+v", p)
	}
	// an exec that starts a stopped sandbox applies a pending one too
	do("POST", "/start?wait=5", nil)
	do("PATCH", "", map[string]any{"egress": other})
	do("POST", "/stop?wait=5", nil)
	a.Sh(sb.ID, "true")
	if got := a.Get(sb.ID); got.State != "running" || got.Egress != other || got.EgressNext != "" {
		t.Fatalf("started by an exec: %+v", got)
	}
}

// --- partitions and sharing ---------------------------------------------------------

var partitionChecks = []check{
	{"shares", func(t *testing.T, e *env) {
		a, b := e.as("a"), e.as("b")
		sb := a.Create(map[string]any{"name": "mine", "visibility": "team"})
		// another consumer: it doesn't exist
		b.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
		b.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"name": "x"}, 404, "not-found")
		b.Refused("DELETE", "/sandboxes/"+sb.ID, nil, 404, "not-found")
		b.Refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 404, "not-found")
		b.Refused("GET", "/sandboxes/"+sb.ID+"/files/stat?path="+q(sb.Workdir), nil, 404, "not-found")
		if _, ok := b.List()[sb.ID]; ok {
			t.Fatal("b lists a's sandbox")
		}
		// shared with everyone b serves
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": b.from, "users": "*"}}}, 200, nil)
		if got := b.Get(sb.ID); !got.Shared || got.ID != sb.ID {
			t.Fatalf("b's view of a shared sandbox: %+v", got)
		}
		if got := a.Get(sb.ID); got.Shared {
			t.Fatal("the home consumer sees its own sandbox as shared")
		}
		if l := b.List(); !l[sb.ID].Shared {
			t.Fatalf("b's list: %+v", l)
		}
		if out := b.Sh(sb.ID, "echo via-b"); out != "via-b\n" {
			t.Fatalf("b runs: %q", out)
		}
		if got := b.Verified("zoe").Get(sb.ID); got.ID != sb.ID { // users "*", a team sandbox
			t.Fatalf("zoe via b: %+v", got)
		}
		// only the home consumer changes shares (or deletes)
		b.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{}}, 403, "not-allowed")
		b.Refused("DELETE", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		// shared with some of b's people
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": b.from, "users": []string{"carol"}}}}, 200, nil)
		b.Verified("carol").Get(sb.ID)
		b.Verified("bob").Refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		b.Verified("bob").Refused("POST", "/sandboxes/"+sb.ID+"/run", map[string]any{"cmd": "true"}, 403, "not-allowed")
		if _, ok := b.Verified("bob").List()[sb.ID]; ok {
			t.Fatal("bob lists a sandbox not shared with him")
		}
		if _, ok := b.Verified("carol").List()[sb.ID]; !ok {
			t.Fatal("carol doesn't list a sandbox shared with her")
		}
		b.Get(sb.ID) // b's backend: the consumer polices its own people
		// unshared: gone again
		a.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{}}, 200, nil)
		b.Refused("GET", "/sandboxes/"+sb.ID, nil, 404, "not-found")
	}},
}

// --- people -----------------------------------------------------------------------------

var peopleChecks = []check{
	{"owners", func(t *testing.T, e *env) {
		a := e.as("a")
		asserted := a.Asserting("alice").Create(map[string]any{"name": "asserted"})
		if asserted.Owner.User != "alice" || !asserted.Owner.Asserted || asserted.Owner.Via != a.from {
			t.Fatalf("asserted owner: %+v", asserted.Owner)
		}
		verified := a.Verified("alice").Asserting("mallory").Create(map[string]any{"name": "verified"})
		if verified.Owner.User != "alice" || verified.Owner.Asserted {
			t.Fatalf("a verified person wins over an asserted one: %+v", verified.Owner)
		}
		if own := a.Create(map[string]any{"name": "the consumer's"}); own.Owner.User != "" || own.Owner.Asserted {
			t.Fatalf("a backend call with no person: %+v", own.Owner)
		}
		// an assertion is recorded, not enforced
		a.Asserting("bob").Get(asserted.ID)
		a.Asserting("bob").Run(asserted.ID, map[string]any{"cmd": "true"})
	}},
	{"visibility", func(t *testing.T, e *env) {
		a := e.as("a")
		alice, bob, carol := a.Verified("alice"), a.Verified("bob"), a.Verified("carol")
		sb := alice.Create(map[string]any{"name": "private"})
		// private: the owner and members only, on a verified call
		alice.Get(sb.ID)
		bob.Refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		bob.Refused("POST", "/sandboxes/"+sb.ID+"/execs", map[string]any{"cmd": "true"}, 403, "not-allowed")
		bob.Refused("GET", "/sandboxes/"+sb.ID+"/files/list?path="+q(sb.Workdir), nil, 403, "not-allowed")
		if _, ok := bob.List()[sb.ID]; ok {
			t.Fatal("bob lists alice's private sandbox")
		}
		a.Get(sb.ID)                  // the consumer's backend
		a.Asserting("bob").Get(sb.ID) // … acting for bob: its call, not verified
		// members
		bob.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"members": []string{"bob"}}, 403, "not-allowed")
		alice.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"members": []string{"bob"}}, 200, nil)
		bob.Get(sb.ID)
		if _, ok := bob.List()[sb.ID]; !ok {
			t.Fatal("a member doesn't list the sandbox")
		}
		// only the home consumer's backend or the owner changes who may use it
		bob.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"visibility": "team"}, 403, "not-allowed")
		bob.Refused("PATCH", "/sandboxes/"+sb.ID, map[string]any{"shares": []map[string]any{{"consumer": "apps/x", "users": "*"}}}, 403, "not-allowed")
		carol.Refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		// team: anyone the consumer serves
		alice.Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"visibility": "team"}, 200, nil)
		carol.Get(sb.ID)
		carol.Run(sb.ID, map[string]any{"cmd": "true"})
		a.Asserting("bob").Call("PATCH", "/sandboxes/"+sb.ID, map[string]any{"visibility": "private", "members": []string{}}, 200, nil)
		carol.Refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		bob.Refused("GET", "/sandboxes/"+sb.ID, nil, 403, "not-allowed")
		// a sandbox of the consumer itself has no owner: a verified person needs it team
		own := a.Create(map[string]any{"name": "the consumer's"})
		carol.Refused("GET", "/sandboxes/"+own.ID, nil, 403, "not-allowed")
	}},
}

// --- lifecycle --------------------------------------------------------------------------

var lifecycleChecks = []check{
	{"start-stop", func(t *testing.T, e *env) {
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "life"})
		act := func(action string, body any) Sandbox {
			t.Helper()
			var s Sandbox
			a.Call("POST", "/sandboxes/"+sb.ID+"/"+action, body, 200, &s)
			return s
		}
		v := sb.Version
		if s := act("stop?wait=5", nil); s.State != "stopped" || s.Version <= v {
			t.Fatalf("stop: %+v", s)
		}
		if s := act("start?wait=5", nil); s.State != "running" {
			t.Fatalf("start: %s", s.State)
		}
		// a stopped sandbox starts on an exec or a file operation
		act("stop?wait=5", nil)
		a.Sh(sb.ID, "true")
		if s := a.Get(sb.ID); s.State != "running" {
			t.Fatalf("after a run on a stopped sandbox: %s", s.State)
		}
		act("stop?wait=5", nil)
		a.Put(sb.ID, sb.Workdir+"/f", "x", "")
		if s := a.Get(sb.ID); s.State != "running" {
			t.Fatalf("after a write on a stopped sandbox: %s", s.State)
		}
		act("stop?wait=5", nil)
		x := a.Exec(sb.ID, map[string]any{"cmd": "true"})
		a.Drain(sb.ID, x.ID)
		a.Refused("POST", "/sandboxes/"+sb.ID+"/no-such-action", nil, 404, "not-found")
		// a stop ends the running commands
		x = a.Exec(sb.ID, map[string]any{"cmd": "sleep 30"})
		act("stop?wait=5", nil)
		eventually(t, 3*time.Second+e.tg.grace(), "the exec ends with its sandbox", func() bool {
			var g Exec
			a.Call("GET", "/sandboxes/"+sb.ID+"/execs/"+x.ID, nil, 200, &g)
			return g.State == "killed"
		})
	}},
	{"archive", func(t *testing.T, e *env) {
		if !e.has("archive") {
			t.Skip("no archive capability")
		}
		a := e.as("a")
		sb := a.Create(map[string]any{"name": "cold"})
		act := func(action string, body any) Sandbox {
			t.Helper()
			var s Sandbox
			a.Call("POST", "/sandboxes/"+sb.ID+"/"+action, body, 200, &s)
			return s
		}
		a.Put(sb.ID, sb.Workdir+"/f", "x", "")
		// archived: thawing is explicit
		if s := act("archive?wait=60", nil); s.State != "archived" {
			t.Fatalf("archive: %s", s.State)
		}
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"POST", "/run", map[string]any{"cmd": "true"}},
			{"POST", "/execs", map[string]any{"cmd": "true"}},
			{"GET", "/files/stat?path=" + q(sb.Workdir), nil},
			{"PUT", "/files/content?path=" + q(sb.Workdir+"/g"), []byte("x")},
			{"POST", "/start", nil},
		} {
			if r := a.Refused(c.method, "/sandboxes/"+sb.ID+c.path, c.body, 409, "state"); r.State != "archived" {
				t.Fatalf("%s %s on an archived sandbox: state %q", c.method, c.path, r.State)
			}
		}
		if s := act("thaw?wait=60", nil); s.State != "stopped" {
			t.Fatalf("thaw: %s", s.State)
		}
		if r := a.Refused("POST", "/sandboxes/"+sb.ID+"/thaw", nil, 409, "state"); r.State != "stopped" {
			t.Fatalf("thaw of a stopped sandbox: %+v", r)
		}
		act("archive?wait=60", nil)
		if s := act("thaw?wait=60", map[string]any{"start": true}); s.State != "running" {
			t.Fatalf("thaw {start}: %s", s.State)
		}
		if got := a.Read(sb.ID, sb.Workdir+"/f"); got != "x" {
			t.Fatalf("an archive keeps the contents: %q", got)
		}
	}},
}
