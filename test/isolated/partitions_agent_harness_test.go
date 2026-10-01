//go:build linux && integration

package isolated

// partitions_agent_harness_test.go — coding agents (D147's harnesses) in a
// partitioned agent, end to end on a real `xbind --isolate` with owner auth
// (W6 of the partitioned-tiles plan, plans/partitions/96-agtt-merge.md §B5;
// the owner's ruling 90 §I15: coding agents only in a person's own
// conversations). coding-sandbox (setupCS: namespace sandboxes, `stdio` and
// `partitions` in its hello) runs the fake adapter (hack/fakeacp, advertised
// by its image as `fake`, the only coding agent it advertises); a copy of
// hack/fakesandbox from before `partitions` is bound beside it; the agent's
// built-in conversations answer through llm-gw from hack/fakeopenai.
//
//   - alice's own conversation (her partition) runs the fake coding agent in
//     a sandbox homed there: its sign-in through the run's relayed terminal,
//     a permission, the answer, over the manager's stdio socket;
//   - GET /sandboxes in her partition marks each row `homed` (and `why`
//     not, the backend's words — W6-U's pick reads them); the catalog keeps
//     her sandbox only, and the old manager makes the coding agents it might
//     have `manager-error` (its refusal names `partitions`);
//   - another partition (bob's) and the global instance dialing the stdio
//     and tty sockets of alice's coding agent's sandbox get 404, and her
//     coding agent goes on after them on the same adapter;
//   - the §I15 refusals (partitions_agent_harness_refusals_test.go);
//   - a halt set at the global instance reaches her coding agent's silent
//     turn in her partition: cancelled, its adapter stopped;
//   - her partition, idle with a coding agent up, keeps one `wake` job
//     registered (never `resume`) — before it is stopped, since a stopping
//     partition's token is revoked before its exit — which brings the
//     partition back at the idle stop's minute to stop the adapter, nobody
//     using the tile meanwhile. (Its usage counts are mailed for the days
//     before today only: W6-A's TestHarnessUsageMail is their check.)
//
// It runs on a dev box only (user namespaces, a rootfs, gocryptfs):
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestPartitionsAgentHarness$' ./test/isolated/

import (
	"context"
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

const phOld = "apps/sbx-old" // hack/fakesandbox from before `partitions`

// phEnv is the test's daemon: coding-sandbox, the old manager, llm-gw and a
// partitioned agent at agTile.
type phEnv struct {
	*csEnv
	fake []byte // hack/fakeacp's binary
}

// agc calls the agent as person ("" = the owner token: the global instance)
// from their page, at the global instance when global — no status check.
func (e *phEnv) agc(t *testing.T, person, method, path string, body any, global bool) xbindtest.Resp {
	t.Helper()
	if global {
		path += map[bool]string{true: "&", false: "?"}[strings.Contains(path, "?")] + "xbin-partition=global"
	}
	return e.d.Call(t, method, "/api/"+agTile+path, body, xbindtest.FrameHeader(e.pageTok(t, agTile, person)))
}

// agm is agc failing the test unless the status is want.
func (e *phEnv) agm(t *testing.T, want int, person, method, path string, body any, global bool) xbindtest.Resp {
	t.Helper()
	r := e.agc(t, person, method, path, body, global)
	if r.Status != want {
		t.Fatalf("%q: %s %s (global %v): %d %s (want %d)", person, method, path, global, r.Status, r, want)
	}
	return r
}

// phRefused checks r is status with words in its error.
func phRefused(t *testing.T, what string, r xbindtest.Resp, status int, words string) {
	t.Helper()
	if r.Status != status || !strings.Contains(r.String(), words) {
		t.Errorf("%s: %d %s (want %d saying %q)", what, r.Status, r, status, words)
	}
}

// setupPartHarness is setupCS with the fake coding agent advertised (the
// image's only one), the old manager, llm-gw on fakeopenai and the agent
// template as a new — partitioned — instance, its sandboxes slot bound to
// both managers and its model fake/fake-chat.
func setupPartHarness(t *testing.T) *phEnv {
	t.Helper()
	cs, _ := setupCS(t, false)
	d := cs.d
	switch {
	case !cs.people:
		t.Skip("no people here (--no-auth): people's partitions are verified people's")
	case d.IsRemote():
		t.Skip("a remote xbind: the test writes tiles and reads the users store")
	}
	if why := cs.partitionsSkip(); why != "" {
		t.Skip(why)
	}
	e := &phEnv{csEnv: cs}
	agTile = "apps/agent"
	e.fake = buildFakeACP(t, d)
	var st struct {
		Config struct{ Images []map[string]any }
	}
	e.ops(t, "GET", "/state", nil, 200, &st)
	if len(st.Config.Images) == 0 {
		t.Fatalf("the manager's images: %+v", st.Config)
	}
	// the fake alone: the catalog's other coding agents then have no image,
	// and the old manager's refusal is why (manager-error)
	im := st.Config.Images[0]
	im["harnesses"] = []any{map[string]any{"id": "fake", "title": "Fake agent (tests)",
		"argv": []string{fakeACPPath, "--steer", "--persist", "--require-login"}, "login": fakeACPPath + " login"}}
	e.ops(t, "PUT", "/config", map[string]any{"images": []any{im}}, 200, nil)
	paSandboxTile(t, d, phOld, false)
	fake := startFakeOpenAI(t, d)

	ok := func(method, path string, body any) xbindtest.Resp {
		t.Helper()
		r := d.Call(t, method, path, body)
		if r.Status/100 != 2 {
			t.Fatalf("%s %s: %d %s", method, path, r.Status, r)
		}
		return r
	}
	const gw = "apps/llm-gw"
	ok("POST", "/api/xbin/builtins/import", map[string]string{"name": "llm-gw", "path": gw})
	d.WaitComponent(t, gw)
	d.Bind(t, gw, "net", "host")
	ok("PUT", "/api/xbin/vault/"+gw+"/api-token-fake", map[string]string{"value": "sk-fake"})
	var inst struct{ Partition []string }
	ok("POST", "/api/xbin/templates/new", map[string]string{"source": "agent", "path": agTile}).Decode(t, &inst)
	if strings.Join(inst.Partition, ",") != "user,global" {
		t.Fatalf("a new agent instance under --isolate: partition %v (want the default user,global)", inst.Partition)
	}
	// a coding agent's conversation from before §I15, at the global instance
	// (partitions_agent_harness_refusals_test.go): a builder's mail topic
	if err := d.WriteFiles(agTile, map[string]string{"_backend/zz_e2e_plant.go": phPlantTopic}); err != nil {
		t.Fatal(err)
	}
	d.WaitComponent(t, agTile)
	ok("POST", "/api/xbin/bindings", map[string]any{"component": agTile, "slot": "llm", "providers": []string{gw}})
	ok("POST", "/api/xbin/bindings", map[string]any{"component": agTile, "slot": "sandboxes", "providers": []string{csTile, phOld}})
	d.Bind(t, agTile, "net", "none")
	xbindtest.Eventually(t, 5*time.Minute, "llm-gw's backend answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+gw+"/config", nil)
		return r.Status == 200 && strings.Contains(string(r.Body), "backends"), fmt.Sprint(r.Status, " ", r)
	})
	ok("PUT", "/api/"+gw+"/config/backend", map[string]string{"name": "fake", "baseURL": "http://" + fake})
	var cfg map[string]any
	xbindtest.Eventually(t, 8*time.Minute, "the agent's global instance answers", func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+agTile+"/config", nil)
		cfg = nil
		if r.Status == 200 {
			_ = json.Unmarshal(r.Body, &cfg)
		}
		return cfg["system"] != nil, fmt.Sprint(r.Status, " ", r)
	})
	cfg["model"] = "fake/fake-chat"
	ok("PUT", "/api/"+agTile+"/config", cfg)
	return e
}

// phPlantTopic plants, at the global instance, what a tree before §I15
// could hold: a shared conversation a coding agent answers (its engine made
// harness by e2e/plant-harness, the run's id its data).
const phPlantTopic = `package main

import (
	"context"
	"encoding/json"
)

func init() {
	mailHandlers["e2e/plant-harness"] = func(_ context.Context, t *DB, it mailItem) error {
		var id int64
		if json.Unmarshal(it.Data, &id) != nil || !globalMode() {
			return nil
		}
		_, err := t.q.Exec(` + "`UPDATE runs SET engine='harness' WHERE id=?`" + `, id)
		return err
	}
}
`

func TestPartitionsAgentHarness(t *testing.T) {
	e := setupPartHarness(t)
	d := e.d
	alice := e.target().As(t, agTile).Verified("alice") // her partition's page, at the manager

	// her own sandbox (homed in her partition), the fake inside; a team one
	// the global instance made
	var box, team agSandbox
	xbindtest.Eventually(t, 3*time.Minute, "alice's partition makes a sandbox", func() (bool, string) {
		r := e.agc(t, "alice", "POST", "/sandboxes", map[string]any{"provider": csTile, "name": "alice-coder", "egress": "internet",
			"clientId": "ph-alice-coder"}, false)
		if r.Status == 201 {
			r.Decode(t, &box)
		}
		return r.Status == 201, fmt.Sprint(r.Status, " ", r)
	})
	e.agm(t, 201, "", "POST", "/sandboxes", map[string]any{"provider": csTile, "name": "team-box", "visibility": "team", "egress": "internet"}, false).
		Decode(t, &team)
	if got := alice.Get(box.ID); got.State != "running" || got.Owner.PartitionID == "" || got.Owner.User != "alice" {
		t.Fatalf("alice's sandbox at the manager: %+v (want running, homed in her partition)", got)
	}
	alice.Put(box.ID, fakeACPPath, string(e.fake), "&mkdirs=1&mode=755")

	t.Run("rows-and-catalog", func(t *testing.T) { phRowsAndCatalog(t, e, box, team) })

	// her own coding agent: a sign-in in its relayed terminal, a permission,
	// the answer
	var run struct {
		ID     int64
		Engine string
	}
	e.agm(t, 200, "alice", "POST", "/ask", map[string]any{"text": "perm", "harness": map[string]string{"provider": "fake"},
		"sandbox": map[string]string{"ref": box.Ref}}, false).Decode(t, &run)
	if run.ID < pa2to40 || run.Engine != "harness" {
		t.Fatalf("alice's coding agent: %+v (want a run of her partition, ≥ 2^40)", run)
	}
	id := run.ID
	v := e.waitView(t, "alice", id, "the sign-in park", 90*time.Second, func(v hView) bool {
		return v.pendingKind() == "login" || v.summary().State == "failed"
	})
	if s := v.summary(); s.State != "login" {
		t.Fatalf("the fake's first turn, signed out: %s", v.brief())
	}
	x := harnessExec(t, alice, box.ID, id, v.summary().Gen)
	if x == nil || x.State != "running" || !x.Split {
		t.Fatalf("the adapter's exec in her sandbox: %+v (want running, split: the stdio socket)", x)
	}
	t.Run("sign-in", func(t *testing.T) {
		conn, r, err := d.Dial(t, e.agentPath(t, fmt.Sprintf("/runs/%d/harness/terminal?login=1&rows=24&cols=80", id), "alice"))
		if err != nil {
			t.Fatalf("her sign-in terminal: %v (%d %s)", err, r.Status, r)
		}
		defer conn.Close()
		if sess := readSession(t, conn); sess.Sandbox != box.ID {
			t.Errorf("the relayed session frame: %+v", sess)
		}
		readUntil(t, conn, "paste the code", 30*time.Second)
		send(t, conn, "fake-code\r")
		readUntil(t, conn, "Signed in.", 30*time.Second)
		if code := readExit(t, conn, 30*time.Second); code == nil || *code != 0 {
			t.Errorf("the sign-in's exit: %v", code)
		}
	})
	e.agm(t, 200, "alice", "POST", fmt.Sprintf("/runs/%d/resume", id), map[string]any{}, false)
	v = e.waitView(t, "alice", id, "the permission park", 90*time.Second, func(v hView) bool {
		return v.pendingKind() == "approval" || v.summary().State == "failed"
	})
	if v.pendingKind() != "approval" {
		t.Fatalf("after the sign-in: %s", v.brief())
	}
	e.agm(t, 200, "alice", "POST", fmt.Sprintf("/runs/%d/approve", id), map[string]any{"approve": true, "park": v.Run.PendingState.Park}, false)
	v = e.waitView(t, "alice", id, "the answer", 90*time.Second, func(v hView) bool {
		return v.Run.Status != "running" && v.pendingKind() == "" && strings.Contains(v.lastAssistant(), "listed")
	})
	gen := v.summary().Gen
	x = harnessExec(t, alice, box.ID, id, gen)
	if x == nil || x.State != "running" || !x.Split {
		t.Fatalf("the adapter after its turn: %+v", x)
	}

	t.Run("saved-signin", func(t *testing.T) { phSavedSignin(t, e, box) }) // D179: partitions_agent_signin_test.go
	t.Run("sockets", func(t *testing.T) { phSockets(t, e, box, id, gen, x.ID) })
	t.Run("refusals", func(t *testing.T) { phRefusals(t, e, box, team, id) }) // partitions_agent_harness_refusals_test.go
	t.Run("halt", func(t *testing.T) { phHalt(t, e, box, id) })
	t.Run("wake", func(t *testing.T) { phWake(t, e, box, id) })
}

// phRowsAndCatalog: GET /sandboxes in alice's partition says of each row
// whether it is homed there (W6-A's `homed`/`why`, which W6-U's pick reads);
// the global instance's rows don't; the old manager is refused there by name;
// the catalog keeps her sandbox only, and the coding agents only the old
// manager might have are manager-error.
func phRowsAndCatalog(t *testing.T, e *phEnv, box, team agSandbox) {
	type row struct {
		Ref, Name string
		Homed     *bool
		Why       *string
	}
	type mgr struct {
		Provider, Refusal, Error string
		OK                       bool
	}
	list := func(person string) (map[string]row, map[string]mgr) {
		t.Helper()
		var out struct {
			Sandboxes []row
			Managers  []mgr
		}
		e.agm(t, 200, person, "GET", "/sandboxes?fresh=1", nil, false).Decode(t, &out)
		rows, mgrs := map[string]row{}, map[string]mgr{}
		for _, r := range out.Sandboxes {
			rows[r.Ref] = r
		}
		for _, m := range out.Managers {
			mgrs[m.Provider] = m
		}
		return rows, mgrs
	}
	rows, mgrs := list("alice")
	if r := rows[box.Ref]; r.Homed == nil || !*r.Homed || r.Why != nil {
		t.Errorf("her own sandbox's row: %+v (want homed, no why)", r)
	}
	if r := rows[team.Ref]; r.Homed == nil || *r.Homed || r.Why == nil ||
		!strings.Contains(*r.Why, "team-box isn't a sandbox of your own space") {
		t.Errorf("the team's sandbox in her partition: %+v (want homed false, why the backend's words)", r)
	}
	if o := mgrs[phOld]; o.OK || o.Refusal != "partitions" || !strings.Contains(o.Error, phOld) {
		t.Errorf("the old manager in her partition: %+v", o)
	}
	if !mgrs[csTile].OK {
		t.Errorf("coding-sandbox in her partition: %+v", mgrs[csTile])
	}
	grows, gmgrs := list("")
	if _, seen := grows[box.Ref]; seen {
		t.Errorf("BUG: the global instance lists alice's sandbox")
	}
	if r, ok := grows[team.Ref]; !ok || r.Homed != nil || r.Why != nil {
		t.Errorf("the team's row at the global instance: %+v, listed %v (want no homed/why there)", r, ok)
	}
	if !gmgrs[phOld].OK {
		t.Errorf("the global instance keeps using the old manager: %+v", gmgrs[phOld])
	}
	if st := e.agc(t, "alice", "POST", "/sandboxes", map[string]any{"provider": phOld, "name": "old-box"}, false).Status; st != 409 {
		t.Errorf("alice making a sandbox at the old manager: %d, want 409", st)
	}

	var cat struct {
		Harnesses []hcEntry
		Probe     struct {
			Ref   string
			Ran   bool
			Error string
		}
	}
	e.agm(t, 200, "alice", "GET", "/harnesses?probe="+url.QueryEscape(box.Ref), nil, false).Decode(t, &cat)
	if cat.Probe.Ref != box.Ref || !cat.Probe.Ran || cat.Probe.Error != "" {
		t.Errorf("the probe of her sandbox: %+v", cat.Probe)
	}
	seen := map[string]hcEntry{}
	for _, h := range cat.Harnesses {
		seen[h.ID] = h
	}
	if f := seen["fake"]; !f.Available || f.Sandboxes[box.Ref].Installed == nil || !*f.Sandboxes[box.Ref].Installed {
		t.Errorf("the fake in her partition: %+v", f)
	} else if _, has := f.Sandboxes[team.Ref]; has {
		t.Errorf("BUG: the catalog in her partition keeps the team's sandbox: %v", f.Sandboxes)
	}
	if c := seen["claude"]; c.Available || c.Reason != "manager-error" || !strings.Contains(c.Why, `"partitions"`) || !strings.Contains(c.Why, phOld) {
		t.Errorf("claude, which only the old manager might have, in her partition: %+v", c)
	}
	var probe struct {
		Probe struct{ Error string }
	}
	e.agm(t, 200, "alice", "GET", "/harnesses?probe="+url.QueryEscape(team.Ref), nil, false).Decode(t, &probe)
	if !strings.Contains(probe.Probe.Error, "isn't a sandbox of your own space") {
		t.Errorf("her probe of the team's sandbox: %+v (want refused: not homed here)", probe.Probe)
	}
	// the global instance offers none (§I15)
	e.agm(t, 200, "", "GET", "/harnesses", nil, false).Decode(t, &cat)
	for _, h := range cat.Harnesses {
		if h.Available || h.Reason != "shared-space" {
			t.Errorf("%s at the global instance: available %v, reason %q (want shared-space)", h.ID, h.Available, h.Reason)
		}
	}
}

// phSockets: bob's partition and the global instance dialing the tty and
// stdio sockets of alice's sandbox (her coding agent's exec) at the manager
// get 404 before any upgrade; nothing is taken over — her coding agent
// answers the next message on the same adapter.
func phSockets(t *testing.T, e *phEnv, box agSandbox, id int64, gen int, execID string) {
	base := "/sandboxes/" + box.ID
	for name, c := range map[string]sandboxcontract.Caller{
		"bob's partition":      e.target().As(t, agTile).Verified("bob"),
		"the global instance":  e.target().As(t, agTile),
		"carol's partition":    e.target().As(t, agTile).Verified("carol"),
		"bob's page elsewhere": e.target().As(t, csCons).Verified("bob"),
	} {
		for _, sub := range []string{"/execs/" + execID + "/stdio", "/execs/" + execID + "/tty", "/tty"} {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			conn, resp, err := c.Dial(ctx, base+sub)
			cancel()
			switch {
			case err == nil:
				conn.Close()
				t.Errorf("BUG: %s dialed %s of alice's coding agent's sandbox", name, sub)
			case resp == nil:
				t.Errorf("%s dialing %s: %v", name, sub, err)
			case resp.StatusCode != 404:
				t.Errorf("%s dialing %s: %d, want 404", name, sub, resp.StatusCode)
			}
		}
	}
	// bob's partition can't reach her run through the agent either
	if r := e.agc(t, "bob", "GET", fmt.Sprintf("/runs/%d/harness/log", id), nil, false); r.Status != 404 {
		t.Errorf("bob reading her coding agent's log: %d %s", r.Status, r)
	}
	e.agm(t, 200, "alice", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "after the dials"}, false)
	v := e.waitView(t, "alice", id, "the answer after the dials", 90*time.Second, func(v hView) bool {
		return v.Run.Status != "running" && strings.Contains(v.lastAssistant(), "echo: after the dials")
	})
	if s := v.summary(); s.Gen != gen {
		t.Errorf("after the refused dials: generation %d (want %d: the same adapter, its stdin still the agent's)", s.Gen, gen)
	}
}

// phHalt: a halt set at the global instance reaches a coding agent's turn in
// alice's partition that says nothing (the fake's stall) — cancelled, its
// adapter stopped — and once it is lifted her next message is answered.
func phHalt(t *testing.T, e *phEnv, box agSandbox, id int64) {
	alice := e.target().As(t, agTile).Verified("alice")
	e.agm(t, 200, "alice", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "stall"}, false)
	v := e.waitView(t, "alice", id, "the stalling turn", 90*time.Second, func(v hView) bool {
		return v.Run.Status == "running" && v.summary().State == "working"
	})
	gen := v.summary().Gen
	t0 := time.Now()
	e.agm(t, 200, "", "PUT", "/halt", map[string]bool{"on": true}, false)
	defer e.agc(t, "", "PUT", "/halt", map[string]bool{"on": false}, false)
	v = e.waitView(t, "alice", id, "the halt reaches her coding agent's silent turn", 60*time.Second, func(v hView) bool {
		return v.Run.Status != "running"
	})
	t.Logf("the halt reached her coding agent's silent turn in %s: %s", time.Since(t0).Round(100*time.Millisecond), v.brief())
	if v.Run.Status != "canceled" {
		t.Errorf("her coding agent under the halt: %s (want canceled)", v.brief())
	}
	xbindtest.Eventually(t, 30*time.Second, "its adapter stopped", func() (bool, string) {
		x := harnessExec(t, alice, box.ID, id, gen)
		return x == nil || x.State != "running", fmt.Sprintf("%+v", x)
	})
	e.agm(t, 200, "", "PUT", "/halt", map[string]bool{"on": false}, false)
	// her partition reads the lifted halt at its next look at conf (a
	// person's request for work meanwhile: 423, as under the halt)
	xbindtest.Eventually(t, time.Minute, "her partition reads the lifted halt", func() (bool, string) {
		r := e.agc(t, "alice", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "slow"}, false)
		return r.Status == 200, fmt.Sprint(r.Status, " ", r)
	})
	e.waitView(t, "alice", id, "her next message, the halt lifted", 2*time.Minute, func(v hView) bool {
		return v.Run.Status != "running" && strings.Contains(v.lastAssistant(), "tick 9")
	})
}

// phWake: alice's partition, idle with her coding agent up, keeps one
// `wake` job registered (never `resume`) — its exit can't: xbind revokes a
// stopping partition's token first — and once it is stopped, at that minute
// xbind's cron brings the partition back, nobody using the tile, which
// stops the idle adapter (its idle time, 2 minutes here, counted from its
// last activity).
func phWake(t *testing.T, e *phEnv, box agSandbox, id int64) {
	d := e.d
	alice := e.target().As(t, agTile).Verified("alice")
	var cfg map[string]any
	e.agm(t, 200, "", "GET", "/config", nil, false).Decode(t, &cfg)
	cfg["harnessIdleMin"] = 2
	e.agm(t, 200, "", "PUT", "/config", cfg, false)
	time.Sleep(5 * time.Second) // her partition reads conf again (confTTL)
	e.agm(t, 200, "alice", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "quick one"}, false)
	v := e.waitView(t, "alice", id, "the turn before the stop", 90*time.Second, func(v hView) bool {
		return v.Run.Status != "running" && strings.Contains(v.lastAssistant(), "echo: quick one") && v.summary().State == "ready"
	})
	gen := v.summary().Gen
	// the partition's own cron jobs (her page of the tile lists them; xbind's
	// API, which starts no backend): the one `wake` at the idle stop's
	// minute, registered while it idles — xbind revokes a stopping
	// partition's token first, so its exit can't leave it (resume_keep.go)
	jobs := func() (names []string, all string) {
		t.Helper()
		var out struct {
			Jobs []struct{ Name, Schedule, Path string }
		}
		d.Must(t, "GET", "/api/xbin/cron/jobs", nil, 200, xbindtest.FrameHeader(e.pageTok(t, agTile, "alice"))).Decode(t, &out)
		for _, j := range out.Jobs {
			names = append(names, j.Name)
			if j.Name == "wake" && (!strings.HasPrefix(j.Schedule, "CRON_TZ=UTC ") || len(strings.Fields(j.Schedule)) != 6) {
				t.Errorf("the wake job's schedule: %q (want a 5-field UTC cron)", j.Schedule)
			}
		}
		return names, fmt.Sprintf("%+v", out.Jobs)
	}
	xbindtest.Eventually(t, 30*time.Second, "her idle partition's wake job", func() (bool, string) {
		names, all := jobs()
		return slices.Equal(names, []string{"wake"}), all
	})
	stopped := time.Now()
	d.Must(t, "POST", "/api/xbin/partitions/stop", map[string]string{"tile": agTile, "partition": "user:alice"}, 200,
		xbindtest.H("Authorization", "Bearer "+e.sess["alice"]))
	if x := harnessExec(t, alice, box.ID, id, gen); x == nil || x.State != "running" {
		t.Fatalf("the idle adapter after her partition stopped: %+v (want running: let go, not killed)", x)
	}
	if names, all := jobs(); !slices.Equal(names, []string{"wake"}) {
		t.Errorf("her stopped partition's cron jobs: %s (want the one wake job: an idle coding agent is no resume work)", all)
	}
	// nobody uses the tile now: the wake brings the partition back, which
	// stops the idle adapter
	xbindtest.Eventually(t, 5*time.Minute, "the woken partition stops the idle adapter", func() (bool, string) {
		x := harnessExec(t, alice, box.ID, id, gen)
		return x == nil || x.State != "running", fmt.Sprintf("%+v, %s after the stop", x, time.Since(stopped).Round(time.Second))
	})
	t.Logf("the idle adapter was stopped %s after her partition stopped", time.Since(stopped).Round(time.Second))
}
