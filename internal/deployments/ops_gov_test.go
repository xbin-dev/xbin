package deployments

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
)

// govFx is the code operations' fixture with a runner that has the facets
// the governance acts use (Reassign, Restart, WakeAlwaysOn, a deployment's
// status) and the governance and data hooks installed as recorders: every
// call lands in codeFx's call list, in order.
type govFx struct {
	*codeFx
	gr *govRunner
	// seedErr, resetErr: what the data hooks answer; claimants: the tiles
	// they name (default: the tile alone).
	seedErr, resetErr error
	claimants         []string
}

// govRunner is the fake runner with the runner's governance facets.
type govRunner struct {
	*fakeRunner
	note        func(string)
	gmu         sync.Mutex
	status      map[string]string // "tile/dep" → a DeploymentStatus state; absent: healthy
	reassignErr error
}

func (r *govRunner) Reassign(ctx context.Context, c *registry.Component, from, to string) error {
	r.note("reassign " + c.Path + " " + from + "→" + to)
	r.gmu.Lock()
	defer r.gmu.Unlock()
	return r.reassignErr
}

func (r *govRunner) Restart(ctx context.Context, c *registry.Component, dep string, progress runner.DeployProgress) error {
	r.note("restart " + c.Path + "/" + dep)
	return nil
}

func (r *govRunner) WakeAlwaysOn()                   { r.note("wake") }
func (r *govRunner) StopDeployment(tile, dep string) { r.note("stop " + tile + "/" + dep) }

func (r *govRunner) DeploymentStatus(tile, dep string) runner.DeploymentState {
	r.gmu.Lock()
	defer r.gmu.Unlock()
	if st := r.status[tile+"/"+dep]; st != "" {
		return runner.DeploymentState{State: st}
	}
	return runner.DeploymentState{State: "healthy"}
}

func (r *govRunner) setStatus(key, state string) {
	r.gmu.Lock()
	defer r.gmu.Unlock()
	if r.status == nil {
		r.status = map[string]string{}
	}
	r.status[key] = state
}

// The edges govFx's broker knows: id → the values each takes.
var govEdges = map[string][]string{"grant:apps/leads": {EdgeRead, EdgeBlock}, "slot:net": {EdgeInherit, EdgeBlock}}

func newGovFx(t *testing.T, isolated bool) *govFx {
	t.Helper()
	f := &govFx{codeFx: newCodeFx(t, isolated)}
	f.gr = &govRunner{fakeRunner: f.run, note: f.note}
	f.p.Run = f.gr
	f.hooks(f.p)
	return f
}

// hooks installs the recording governance and data hooks on p.
func (f *govFx) hooks(p *Plane) {
	p.SetGovHooks(func(h *GovHooks) {
		h.SessionsProtected = func(tile string) (int, int) { f.note("sessions-protected " + tile); return 1, 0 }
		h.SessionsReassigned = func(tile, from, to string) (int, int) {
			f.note("sessions-reassigned " + tile + " " + from + "→" + to)
			return 0, 0
		}
		h.RoutesReassigned = func(tile, from, to string) { f.note("routes-reassigned " + tile + " " + from + "→" + to) }
		h.ValidateEdge = func(tile, edge, policy string) error {
			values, ok := govEdges[edge]
			switch {
			case !ok:
				return &Error{Status: http.StatusNotFound, Msg: tile + " has no edge " + edge}
			case policy != EdgeDefault && !slices.Contains(values, policy):
				return &Error{Status: http.StatusBadRequest, Msg: edge + " takes " + strings.Join(values, " or ")}
			}
			return nil
		}
		h.EdgeRestarts = func(tile, edge string) bool { return edge == "slot:net" }
		h.DiskCeiling = func() int64 { return 50 << 30 }
		h.SeedData = func(pr auth.Principal, req SeedRequest, authorize func(string) error, stop func(string, string)) (SeedFacts, error) {
			claimants, err := f.dataHook("seed", req.Tile, req.Deployment, actor(pr), req.Stop, req.DryRun, authorize, stop, f.seedErr)
			facts := SeedFacts{Claimants: claimants}
			for _, t := range claimants {
				facts.Stops = append(facts.Stops, t+"+"+req.Deployment)
				if req.Stop {
					facts.Stops = append(facts.Stops, t+"+main")
				}
			}
			return facts, err
		}
	})
	p.ResetData = func(tile, dep, by string, vault, dry bool, authorize func(string) error, stop func(string, string)) ([]string, error) {
		return f.dataHook("reset", tile, dep, by, vault, dry, authorize, stop, f.resetErr)
	}
	p.DataOf = func(tile, dep string) *DataState { return &DataState{State: "empty", Reset: true} }
	p.VaultCopy = func(pr auth.Principal, req VaultCopyRequest) (VaultCopyAnswer, error) {
		f.note("vault-copy " + req.Tile + "/" + req.Deployment + " " + strings.Join(req.Keys, ",") + " dry=" + boolStr(req.DryRun))
		return VaultCopyAnswer{Copied: req.Keys, Missing: []string{}}, nil
	}
	p.RunNow = func(ctx context.Context, tile, dep, job string) (Delivery, error) {
		f.note("run-now " + tile + "/" + dep + " " + job)
		return Delivery{Status: 200, MS: 7}, nil
	}
}

// dataHook is the seed and reset hooks: it judges every other claimant,
// stops each claimant's dep unless dry, and records the call.
func (f *govFx) dataHook(act, tile, dep, by string, flag, dry bool, authorize func(string) error, stop func(string, string), fail error) ([]string, error) {
	claimants := f.claimants
	if claimants == nil {
		claimants = []string{tile}
	}
	for _, c := range claimants {
		if c == tile {
			continue
		}
		if err := authorize(c); err != nil {
			return nil, err
		}
	}
	if fail != nil {
		return nil, fail
	}
	f.note(act + " " + tile + "/" + dep + " by=" + by + " flag=" + boolStr(flag) + " dry=" + boolStr(dry))
	if !dry {
		for _, c := range claimants {
			stop(c, dep)
		}
	}
	return claimants, nil
}

// waitCall waits for a call starting with prefix among those recorded since
// the last take, and returns every call recorded meanwhile.
func (f *govFx) waitCall(prefix string) []string {
	f.t.Helper()
	var all []string
	for i := 0; i < 500; i++ {
		all = append(all, f.took()...)
		for _, c := range all {
			if strings.HasPrefix(c, prefix) {
				return all
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("no call %q; saw %q", prefix, all)
	return nil
}

// answerOf is an operation's committed answer.
func answerOf(t *testing.T) func(res any, err error) Answer {
	return func(res any, err error) Answer {
		t.Helper()
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		switch a := res.(type) {
		case Answer:
			return a
		case PrimaryAnswer:
			return a.Answer
		case ProtectAnswer:
			return a.Answer
		}
		t.Fatalf("answered %T", res)
		return Answer{}
	}
}

// covers 05-model §5 P13 P14 P22 P28 T6 — the governance and data acts on a
// literal of the plane's inputs, no broker: an edge's policy (501 until the
// broker's edge check is installed; 404 and 400 as the broker judges; set,
// unchanged, default; a change that applies at a spawn restarts the tile's
// running non-primary deployments, never the primary); the deliveries switch
// (never on the primary, which reaches RegistrationsActive at once); limits
// (LimitsFor for that deployment alone); seed, reset, vault copy and run now
// delegating to their planes (confirm tokens first, the primary refused, the
// claimants stopped and told with op data, dry runs changing nothing). Each
// committed setting is a record event without a reader form.
func TestDeployPlaneOperationsGov(t *testing.T) {
	f := newGovFx(t, false)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	f.took()
	f.evs.take()
	do := func(op Op, req any) (any, error) { t.Helper(); return f.do(ownerP, op, req) }
	seq := func() int64 { return f.rec(opSite).Seq }

	t.Run("edge policy", func(t *testing.T) {
		g := newGovFx(t, false)
		g.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
		g.p.SetGovHooks(func(h *GovHooks) { h.ValidateEdge = nil })
		_, err := g.do(ownerP, OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/leads", Policy: EdgeRead})
		wantErr(t, "without the broker's check", err, http.StatusNotImplemented, "setting an edge policy isn't built")

		_, err = do(OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/nope", Policy: EdgeRead})
		wantErr(t, "an unknown edge", err, http.StatusNotFound, "apps/site has no edge grant:apps/nope")
		_, err = do(OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/leads", Policy: EdgeInherit})
		wantErr(t, "a value the edge doesn't take", err, http.StatusBadRequest, "grant:apps/leads takes read or block")
		if _, err := do(OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/leads", Policy: EdgeRead, DryRun: true}); err != nil || f.rec(opSite).Edges != nil {
			t.Fatalf("dry run: %v, edges %v", err, f.rec(opSite).Edges)
		}
		before := seq()
		if a := answerOf(t)(do(OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/leads", Policy: EdgeRead, Seq: ptr(before)})); a.Unchanged {
			t.Fatal("setting read answered unchanged")
		}
		if r := f.rec(opSite); r.Seq != before+1 || !reflect.DeepEqual(r.Edges, map[string]string{"grant:apps/leads": EdgeRead}) ||
			!reflect.DeepEqual(f.p.EdgePolicies(opSite), r.Edges) {
			t.Fatalf("record after the edge = %+v", r)
		}
		evs := f.evs.take()
		if len(evs) != 1 || !reflect.DeepEqual(evs[0].Data, recordEvent{Op: "record", Seq: before + 1, By: "owner", What: []string{"edges"}}) || len(readerForms(evs)) != 0 {
			t.Errorf("events = %+v", evs)
		}
		if a := answerOf(t)(do(OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/leads", Policy: EdgeRead})); !a.Unchanged {
			t.Error("setting it again changed something")
		}
		answerOf(t)(do(OpEdge, &EdgeRequest{Tile: opSite, Edge: "grant:apps/leads", Policy: EdgeDefault}))
		if r := f.rec(opSite); r.Edges != nil {
			t.Errorf("default left %v", r.Edges)
		}
		if calls := f.took(); len(calls) != 0 {
			t.Errorf("a static tile's edges restarted %q", calls)
		}
		f.evs.take()

		// A backend tile: the net edge restarts dev, which is up, never main.
		g = newGovFx(t, true)
		g.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "dev"})
		g.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "idle"})
		g.gr.setStatus(opAPI+"/idle", "idle")
		g.took()
		answerOf(t)(g.do(ownerP, OpEdge, &EdgeRequest{Tile: opAPI, Edge: "slot:net", Policy: EdgeBlock}))
		if calls := g.waitCall("restart "); !slices.Equal(calls, []string{"restart apps/api/dev"}) {
			t.Errorf("the net edge restarted %q", calls)
		}
		answerOf(t)(g.do(ownerP, OpEdge, &EdgeRequest{Tile: opAPI, Edge: "grant:apps/leads", Policy: EdgeBlock}))
		time.Sleep(50 * time.Millisecond)
		if calls := g.took(); len(calls) != 0 {
			t.Errorf("an edge judged per call restarted %q", calls)
		}
	})

	t.Run("deliveries", func(t *testing.T) {
		if fires, _ := f.p.RegistrationsActive(opSite, "dev"); fires {
			t.Fatal("dev fires before its switch")
		}
		_, err := do(OpDeliveries, &SwitchRequest{Tile: opSite, Deployment: "main", On: ptr(true)})
		wantErr(t, "the primary", err, http.StatusConflict, "main is the primary of apps/site — its cron jobs")
		_, err = do(OpDeliveries, &SwitchRequest{Tile: opSite, Deployment: "dev"})
		wantErr(t, "no on", err, http.StatusBadRequest, "bad request body: on is required")
		_, err = do(OpDeliveries, &SwitchRequest{Tile: opSite, Deployment: "nope", On: ptr(true)})
		wantErr(t, "no such deployment", err, http.StatusNotFound, `apps/site has no deployment "nope"`)
		answerOf(t)(do(OpDeliveries, &SwitchRequest{Tile: opSite, Deployment: "dev", On: ptr(true)}))
		if fires, routes := f.p.RegistrationsActive(opSite, "dev"); !fires || routes || !f.rec(opSite).Deployments["dev"].Deliveries {
			t.Errorf("deliveries on: fires %v routes %v", fires, routes)
		}
		if evs := f.evs.take(); len(evs) != 1 || evs[0].Data.(recordEvent).What[0] != "deliveries" {
			t.Errorf("events = %+v", evs)
		}
		answerOf(t)(do(OpDeliveries, &SwitchRequest{Tile: opSite, Deployment: "dev", On: ptr(false)}))
		if fires, _ := f.p.RegistrationsActive(opSite, "dev"); fires {
			t.Error("deliveries off still fire")
		}
		f.evs.take()
	})

	t.Run("limits", func(t *testing.T) {
		f.p.TileLimits = cgroup.Limits{MemMax: 2 << 30, PidsMax: 512}
		answerOf(t)(do(OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: LimitsPatch{MemMiB: Override{Set: true, Value: ptr(int64(512))}}}))
		if dev, main := f.p.LimitsFor(opSite, "dev"), f.p.LimitsFor(opSite, "main"); dev.MemMax != 512<<20 || dev.PidsMax != 512 || main.MemMax != 2<<30 {
			t.Errorf("LimitsFor dev %+v main %+v", dev, main)
		}
		if evs := f.evs.take(); len(evs) != 1 || evs[0].Data.(recordEvent).What[0] != "limits" || len(readerForms(evs)) != 0 {
			t.Errorf("events = %+v", evs)
		}
		answerOf(t)(do(OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev", Limits: LimitsPatch{MemMiB: Override{Set: true}}}))
		if l := f.rec(opSite).Deployments["dev"].Limits; l != nil {
			t.Errorf("null left %v", l)
		}
		_, err := do(OpLimits, &LimitsRequest{Tile: opSite, Deployment: "dev"})
		wantErr(t, "no limit", err, http.StatusBadRequest, "bad request body: limits names none")
		f.evs.take()
	})

	t.Run("seed", func(t *testing.T) {
		_, err := do(OpSeed, &SeedRequest{Tile: opSite, Deployment: "dev"})
		wantErr(t, "no confirm", err, http.StatusBadRequest, `seeding dev copies main's data, which may be personal: send confirm:"copy-data"`)
		_, err = do(OpSeed, &SeedRequest{Tile: opSite, Deployment: "main", Confirm: ConfirmCopyData})
		wantErr(t, "the primary", err, http.StatusConflict, "main is the primary of apps/site")
		im := f.dry(ownerP, OpSeed, &SeedRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmCopyData, DryRun: true})
		if im.Data != "seed" || !slices.Equal(im.Stops, []string{"dev"}) || im.Affects != "deployment" {
			t.Errorf("dry run = %+v", im)
		}
		answerOf(t)(do(OpSeed, &SeedRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmCopyData, Stop: true}))
		if calls := f.took(); !slices.Equal(calls, []string{"seed apps/site/dev by=owner flag=false dry=true",
			"seed apps/site/dev by=owner flag=true dry=false", "stop apps/site/dev"}) {
			t.Errorf("calls = %q", calls)
		}
		if evs := f.evs.take(); len(evs) != 1 || !reflect.DeepEqual(evs[0].Data, dataEvent{Op: "data", Deployment: "dev", State: "empty"}) {
			t.Errorf("events = %+v", evs)
		}
		stopped := &SeedRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmCopyData, Stop: true, DryRun: true}
		if im := f.dry(ownerP, OpSeed, stopped); !slices.Equal(im.Stops, []string{"dev", "main"}) {
			t.Errorf("a stopped seed's dry run stops %q, want dev and the primary", im.Stops)
		}
		f.took()
		g := newGovFx(t, false)
		g.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
		g.p.SetGovHooks(func(h *GovHooks) { h.SeedData = nil })
		_, err = g.do(ownerP, OpSeed, &SeedRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmCopyData})
		wantErr(t, "without the seed", err, http.StatusNotImplemented, "seeding a deployment's data isn't built")
	})

	t.Run("reset", func(t *testing.T) {
		_, err := do(OpReset, &ResetRequest{Tile: opSite, Deployment: "dev"})
		wantErr(t, "no confirm", err, http.StatusBadRequest, `resetting dev erases its data: send confirm:"erase-data"`)
		answerOf(t)(do(OpReset, &ResetRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmEraseData, Vault: true}))
		if calls := f.took(); !slices.Equal(calls, []string{"reset apps/site/dev by=owner flag=true dry=false", "stop apps/site/dev"}) {
			t.Errorf("calls = %q", calls)
		}
		f.evs.take()
		// Another claimant of the namespace blocks a caller who can't act there.
		f.claimants = []string{opSite, opNode}
		defer func() { f.claimants = nil }()
		dev := userP("dev", opSite, "terminal")
		_, err = f.do(dev, OpReset, &ResetRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmEraseData})
		wantErr(t, "a claimant blocks", err, http.StatusForbidden, "resetting a deployment's data needs terminal-level access on apps/node")
		if calls := f.took(); len(calls) != 0 {
			t.Errorf("a blocked reset called %q", calls)
		}
		im := f.dry(ownerP, OpReset, &ResetRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmEraseData, DryRun: true})
		if im.Data != "erase" || !slices.Equal(im.Stops, []string{"dev", "apps/node+dev"}) {
			t.Errorf("dry run = %+v", im)
		}
		f.took()
		f.resetErr = &Error{Status: http.StatusConflict, Kind: KindState, Msg: "dev's data is being seeded"}
		_, err = do(OpReset, &ResetRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmEraseData})
		f.resetErr = nil
		wantErr(t, "data busy", err, http.StatusConflict, "dev's data is being seeded")
	})

	t.Run("vault copy and run now", func(t *testing.T) {
		res, err := do(OpVaultCopy, &VaultCopyRequest{Tile: opSite + "+dev", Keys: []string{"K"}})
		if a, ok := res.(VaultCopyAnswer); err != nil || !ok || !slices.Equal(a.Copied, []string{"K"}) {
			t.Fatalf("vault copy = %+v, %v", res, err)
		}
		if _, err := do(OpVaultCopy, &VaultCopyRequest{Tile: opSite, Deployment: "dev", All: true, DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if calls := f.took(); !slices.Equal(calls, []string{"vault-copy apps/site/dev K dry=false", "vault-copy apps/site/dev  dry=true"}) {
			t.Errorf("calls = %q", calls)
		}
		_, err = do(OpVaultCopy, &VaultCopyRequest{Tile: opSite, Deployment: "main", All: true})
		wantErr(t, "into the primary", err, http.StatusConflict, "main is the primary of apps/site")

		res, err = do(OpRunNow, &RunNowRequest{Tile: opSite, Deployment: "dev", Job: "nightly"})
		if a, ok := res.(RunNowAnswer); err != nil || !ok || a.Delivery.Status != 200 {
			t.Fatalf("run now = %+v, %v", res, err)
		}
		_, err = do(OpRunNow, &RunNowRequest{Tile: opSite, Deployment: "main", Job: "nightly"})
		wantErr(t, "the primary's job", err, http.StatusConflict, "main is the primary of apps/site: its jobs fire on schedule")
		_, err = do(OpRunNow, &RunNowRequest{Tile: opSite, Deployment: "dev"})
		wantErr(t, "no job", err, http.StatusBadRequest, "bad request body: job is required")
		if _, err := do(OpRunNow, &RunNowRequest{Tile: opSite, Deployment: "dev", Job: "nightly", DryRun: true}); err != nil {
			t.Fatal(err)
		}
		if calls := f.took(); !slices.Equal(calls, []string{"run-now apps/site/dev nightly"}) {
			t.Errorf("calls = %q", calls)
		}
		f.p.RunNow = nil
		_, err = do(OpRunNow, &RunNowRequest{Tile: opSite, Deployment: "dev", Job: "nightly"})
		wantErr(t, "without the broker", err, http.StatusNotImplemented, "running a job now isn't built")
	})

	t.Run("a tile without a record", func(t *testing.T) {
		for _, c := range []struct {
			op  Op
			req any
		}{
			{OpEdge, &EdgeRequest{Tile: opNode, Edge: "slot:net", Policy: EdgeBlock}},
			{OpDeliveries, &SwitchRequest{Tile: opNode, Deployment: "main", On: ptr(true)}},
			{OpLimits, &LimitsRequest{Tile: opNode, Deployment: "main", Limits: LimitsPatch{Pids: Override{Set: true, Value: ptr(int64(9))}}}},
			{OpReset, &ResetRequest{Tile: opNode, Deployment: "main", Confirm: ConfirmEraseData}},
			{OpPrimary, &PrimaryRequest{Tile: opNode, Deployment: "main", Confirm: ConfirmDataStays}},
		} {
			_, err := do(c.op, c.req)
			wantErr(t, string(c.op), err, http.StatusConflict, "apps/node has no deployments yet")
		}
		if f.rec(opNode) != nil || fileExists(recordPath(f.root, opNode)) {
			t.Error("a refusal on a tile without a record wrote one")
		}
	})
}

// fileExistsIn reports whether rel exists under root, following nothing.
func fileExistsIn(root, rel string) bool {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return !errors.Is(err, os.ErrNotExist)
}

// eventsOf lists the data of evs of type typ.
func eventsOf(evs []events.Event, typ string) []any {
	var out []any
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e.Data)
		}
	}
	return out
}
