package deployments

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// codeFx is the operations fixture with the broker's hooks the code
// operations call recorded: the files a deployment keeps (DropDeploymentFiles),
// its registrations (DropRegistrations) and the runner's stops.
type codeFx struct {
	*opsFx
	mu    sync.Mutex
	calls []string // "drop-files tile/dep (record holds it: bool)", "drop-regs tile/dep", "stop tile/dep"
}

func newCodeFx(t *testing.T, isolated bool) *codeFx {
	t.Helper()
	f := &codeFx{opsFx: newOpsFx(t, isolated)}
	f.hook()
	return f
}

// hook installs the recorders on the booted plane.
func (f *codeFx) hook() {
	f.p.DropDeploymentFiles = func(tile, dep string) error {
		f.note("drop-files " + tile + "/" + dep + " held=" + boolStr(f.p.HasDeployment(tile, dep)))
		return nil
	}
	f.p.DropRegistrations = func(tile, dep string) error { f.note("drop-regs " + tile + "/" + dep); return nil }
	f.p.Run = &stoppingRunner{fakeRunner: f.run, stopped: func(tile, dep string) { f.note("stop " + tile + "/" + dep) }}
}

func (f *codeFx) note(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

// took returns the calls recorded since the last take.
func (f *codeFx) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// stoppingRunner is the fake runner with StopDeployment recorded.
type stoppingRunner struct {
	*fakeRunner
	stopped func(tile, dep string)
}

func (r *stoppingRunner) StopDeployment(tile, dep string) { r.stopped(tile, dep) }

// add adds dep to tile as pr and returns the answer.
func (f *codeFx) add(pr auth.Principal, req *AddRequest) AddAnswer {
	f.t.Helper()
	res, err := f.do(pr, OpAdd, req)
	if err != nil {
		f.t.Fatalf("add %+v: %v", req, err)
	}
	a, ok := res.(AddAnswer)
	if !ok {
		f.t.Fatalf("add answered %T", res)
	}
	f.settle(req.Tile, a.Answer)
	return a
}

// dry runs a dry run and returns its impact.
func (f *codeFx) dry(pr auth.Principal, op Op, req any) Impact {
	f.t.Helper()
	res, err := f.do(pr, op, req)
	if err != nil {
		f.t.Fatalf("dry %s %+v: %v", op, req, err)
	}
	d, ok := res.(DryRunAnswer)
	if !ok {
		f.t.Fatalf("dry %s answered %T", op, res)
	}
	return d.Impact
}

// wantErr checks an error's status and text prefix.
func wantErr(t *testing.T, what string, err error, status int, prefix string) {
	t.Helper()
	code, msg := errStatus(err)
	if code != status || !strings.HasPrefix(msg, prefix) {
		t.Errorf("%s: %d %q, want %d %q…", what, code, msg, status, prefix)
	}
}

// readerForms lists the reader forms among evs.
func readerForms(evs []events.Event) []string {
	var out []string
	for _, e := range evs {
		switch e.Data.(type) {
		case recordReaderEvent, deployReaderEvent:
			b, _ := json.Marshal(e.Data)
			out = append(out, string(b))
		}
	}
	return out
}

func cp(r *Record, dep string) string {
	if d := r.Deployments[dep]; d != nil && d.Checkpoint != nil {
		return *d.Checkpoint
	}
	return ""
}

// covers 05-model §5 D119c D119d D119e D127h D127i D119i — the code operations
// beyond main on a literal of the plane's inputs, no broker: adding a
// deployment (a dry run on a tile without a record captures nothing and
// creates no store; the add is that tile's opt-in: the record, main still on
// the work tree, the new deployment pinned to a fresh checkpoint, what an
// earlier deployment of the name left dropped before the record commits, its
// code prepared without starting); attaching live reload to it (the former
// target pinned at request time, the new one following the work tree);
// promoting its code onto main with the reviewed expect; removing it
// (confirmed, never main or the primary, the record first, then the stop,
// the drops, the view repository). On a Go tile the new deployment's code is
// prepared through the runner and a pinned → pinned promotion swaps after
// its build. Readers get a record event only when the reader view changed.
// The requests' refusals answer 11-contract §1.14's texts.
func TestDeployPlaneOperationsCode(t *testing.T) {
	t.Run("a static tile: add, attach, promote, remove", func(t *testing.T) {
		f := newCodeFx(t, false)
		im := f.dry(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", DryRun: true})
		if im.Code != nil || f.st.Exists(opSite) || f.rec(opSite) != nil || len(f.took()) != 0 || im.Affects != "nobody" {
			t.Fatalf("a zero-state dry add: %+v; store %v, record %+v", im, f.st.Exists(opSite), f.rec(opSite))
		}
		f.evs.take()

		ans := f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
		r := f.rec(opSite)
		tree1 := cp(r, "dev")
		switch {
		case r == nil || r.Seq != 1 || r.LiveReload != "main" || r.Primary != "main" || cp(r, "main") != "" || tree1 == "":
			t.Fatalf("record after add = %+v", r)
		case r.Deployments["dev"].Created == "" || r.Deployments["dev"].By != "owner" || r.Deployments["main"].By != "owner":
			t.Errorf("stamps: %+v %+v", r.Deployments["dev"], r.Deployments["main"])
		case ans.Deploy == nil || ans.Deploy.How != "add" || ans.Deploy.Deployment != "dev" || ans.Deploy.Result != resultOK ||
			ans.Deploy.Checkpoint != "c:"+tree1[:7] || ans.Joins != nil:
			t.Errorf("add's answer = %+v %+v", ans.Deploy, ans.Joins)
		}
		if got := f.took(); !reflect.DeepEqual(got, []string{"drop-files " + opSite + "/dev held=false"}) {
			t.Errorf("add called %v, want the stale files dropped before the record holds dev", got)
		}
		if code, err := f.p.CodeFor(opSite, "dev"); err != nil || code.Tree != tree1 {
			t.Errorf("CodeFor(dev) = %+v %v", code, err)
		}
		if dep, on := f.p.LiveReload(opSite); dep != "main" || !on {
			t.Errorf("LiveReload after add = %q %v", dep, on)
		}
		if v := viewOf(f, opSite); v["dev"] != tree1 || len(v) != 1 {
			t.Errorf("view repository = %v", v)
		}
		if c := f.p.CapsOf(opSite); c.TileUsed != 1 || c.WorkspaceUsed != 1 || c.Tile != 3 || c.Workspace != 24 {
			t.Errorf("caps = %+v", c)
		}
		evs := f.evs.take()
		if rf := readerForms(evs); len(rf) != 0 {
			t.Errorf("readers learned of the add: %v", rf)
		}
		if !hasEvent(evs, `deployments {"op":"record","seq":1,"by":"owner","what":["deployments"]} `+opSite) ||
			!hasEvent(evs, `deployments {"op":"reload","deployment":"dev"} `+opSite) || hasEvent(evs, `reload {} `+opSite) {
			t.Errorf("add's events: %v", evs)
		}
		if _, err := os.Stat(filepath.Join(f.root, "data", "deployments", util.TileKey(opSite), journalFile)); err != nil {
			t.Errorf("the opt-in's journal: %v", err)
		}

		// Attach live reload to dev: main pinned where it stands.
		imA := f.dry(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev", DryRun: true})
		if imA.Code == nil || imA.Code.Deployment != "dev" || imA.Code.To != "work-tree" || imA.Affects != "deployment" {
			t.Errorf("a dry attach: %+v", imA)
		}
		att := f.must(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"})
		r = f.rec(opSite)
		if r.LiveReload != "dev" || r.LastLiveReload != "dev" || cp(r, "dev") != "" || cp(r, "main") != tree1 || r.Seq != 2 {
			t.Fatalf("record after attach = %+v", r)
		}
		if att.Deploy == nil || att.Deploy.How != "attach" || att.Deploy.Deployment != "dev" || !att.Deploy.FollowsWorkTree {
			t.Errorf("attach's answer = %+v", att.Deploy)
		}
		if !hasEntry(f.st.logged(opSite), "main", "attach", tree1) {
			t.Errorf("deploy log after attach = %+v", f.st.logged(opSite))
		}
		if dep, on := f.p.LiveReload(opSite); dep != "dev" || !on {
			t.Errorf("LiveReload after attach = %q %v", dep, on)
		}
		evs = f.evs.take()
		if rf := readerForms(evs); len(rf) == 0 || rf[0] != `{"op":"record","what":["liveReload","deployments"]}` {
			t.Errorf("readers after attach (main pinned): %v", rf)
		}
		if again := f.must(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"}); again.Deploy != nil || f.rec(opSite).Seq != 2 {
			t.Errorf("attaching again: %+v", again)
		}

		// Promote dev's work tree onto main, reviewed.
		f.write(opSite+"/index.html", "<h1>v2</h1>")
		imP := f.dry(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main", DryRun: true})
		if imP.Code == nil || imP.Code.Deployment != "main" || imP.Code.From != "c:"+tree1[:7] || imP.Code.WorkTreeAt == "" ||
			imP.Affects != "everyone" || imP.PausesLiveReload {
			t.Fatalf("a dry promote: %+v", imP)
		}
		f.write(opSite+"/index.html", "<h1>v3</h1>")
		_, err := f.do(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main", Expect: imP.Code.To})
		wantErr(t, "a promote whose work tree moved since its review", err, http.StatusConflict, "the code changed since you reviewed "+imP.Code.To+" (now c:")
		f.write(opSite+"/index.html", "<h1>v2</h1>")
		f.evs.take()
		pro := f.must(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main", Expect: imP.Code.To})
		r = f.rec(opSite)
		tree2 := cp(r, "main")
		if !strings.HasPrefix(tree2, strings.TrimPrefix(imP.Code.To, "c:")) || r.LiveReload != "dev" || cp(r, "dev") != "" {
			t.Fatalf("record after promote = %+v", r)
		}
		if pro.Deploy == nil || pro.Deploy.How != "promote" || pro.Deploy.From != "dev" || pro.Deploy.Deployment != "main" || pro.Deploy.Result != resultOK {
			t.Errorf("promote's answer = %+v", pro.Deploy)
		}
		if !hasEvent(f.evs.take(), `reload {} `+opSite) {
			t.Error("promoting onto the primary announced no bare reload")
		}
		if ans := f.must(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main"}); !ans.Unchanged {
			t.Errorf("promoting the same code again: %+v", ans)
		}

		// Remove dev: it held live reload, so live reload pauses.
		_, err = f.do(ownerP, OpRemove, &RemoveRequest{Tile: opSite, Deployment: "dev"})
		wantErr(t, "remove without confirm", err, http.StatusBadRequest, `removing dev deletes its data, secrets and logs: send confirm:"erase" to proceed`)
		imR := f.dry(ownerP, OpRemove, &RemoveRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmErase, DryRun: true})
		if imR.Data != "erase" || !imR.PausesLiveReload || !reflect.DeepEqual(imR.Stops, []string{"dev"}) || f.rec(opSite).Deployments["dev"] == nil {
			t.Errorf("a dry remove: %+v", imR)
		}
		f.took()
		f.evs.take()
		if ans := f.must(ownerP, OpRemove, &RemoveRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmErase}); ans.Deploy != nil {
			t.Errorf("remove answered a deploy: %+v", ans)
		}
		r = f.rec(opSite)
		if r.Deployments["dev"] != nil || r.LiveReload != "" || r.LastLiveReload != "main" || cp(r, "main") != tree2 {
			t.Fatalf("record after remove = %+v", r)
		}
		want := []string{"stop " + opSite + "/dev", "drop-regs " + opSite + "/dev", "drop-files " + opSite + "/dev held=false"}
		if got := f.took(); !reflect.DeepEqual(got, want) {
			t.Errorf("remove called %v, want %v", got, want)
		}
		if v := viewOf(f, opSite); v["dev"] != "" {
			t.Errorf("the view repository still holds dev: %v", v)
		}
		if code, err := f.p.CodeFor(opSite, "dev"); err == nil {
			t.Errorf("CodeFor(removed dev) = %+v", code)
		}
		evs = f.evs.take()
		if !hasEvent(evs, `deployments {"op":"record","seq":`+itoa(r.Seq)+`,"by":"owner","what":["liveReload","deployments"]} `+opSite) ||
			len(readerForms(evs)) != 0 {
			t.Errorf("remove's events: %v", evs)
		}

		// main and the primary are never removed.
		_, err = f.do(ownerP, OpRemove, &RemoveRequest{Tile: opSite, Deployment: "main", Confirm: ConfirmErase})
		wantErr(t, "removing main", err, http.StatusConflict, "main can't be removed")
		f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "exp", From: FromPrimary})
		if _, err := f.p.idx.commit(opSite, -1, func(r *Record) error { r.Primary = "exp"; return nil }); err != nil {
			t.Fatal(err)
		}
		_, err = f.do(ownerP, OpRemove, &RemoveRequest{Tile: opSite, Deployment: "exp", Confirm: ConfirmErase})
		wantErr(t, "removing the primary", err, http.StatusConflict, "exp is the primary of "+opSite)
		_, err = f.do(ownerP, OpRemove, &RemoveRequest{Tile: opSite, Deployment: "nope", Confirm: ConfirmErase})
		wantErr(t, "removing an unknown deployment", err, http.StatusNotFound, opSite+` has no deployment "nope"`)
	})

	t.Run("a Go tile: prepared through the runner, promoted after a swap", func(t *testing.T) {
		f := newCodeFx(t, true)
		f.settle(opAPI, f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})) // main's pause runs on its lane: done before dev's deploy is the runner's last
		tree1 := cp(f.rec(opAPI), "main")
		ans := f.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "dev", From: FromPrimary})
		if e := f.wait(opAPI, ans.Deploy.ID); cp(f.rec(opAPI), "dev") != tree1 || e.From != "main" || e.Result != resultOK || e.How != "add" {
			t.Fatalf("add from the primary: %+v, record %+v", e, f.rec(opAPI))
		}
		if d := lastDeploy(f); d != opAPI+"/dev@"+tree1 {
			t.Errorf("the new deployment's code went through %q", d)
		}
		f.write(opAPI+"/main.go", "package main // v2\n")
		dep := f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Deployment: "dev"})
		f.settle(opAPI, dep)
		tree2 := cp(f.rec(opAPI), "dev")
		if tree2 == tree1 || tree2 == "" {
			t.Fatalf("dev after its deploy: %q", tree2)
		}
		f.evs.take()
		pro := f.must(ownerP, OpPromote, &PromoteRequest{Tile: opAPI, From: "dev", To: "main"})
		e := f.wait(opAPI, pro.Deploy.ID)
		if e.Result != resultOK || e.How != "promote" || e.From != "dev" || cp(f.rec(opAPI), "main") != tree2 {
			t.Fatalf("promote: %+v, main now %q", e, cp(f.rec(opAPI), "main"))
		}
		if d := lastDeploy(f); d != opAPI+"/main@"+tree2 {
			t.Errorf("the promotion deployed %q", d)
		}
		if ans := f.must(ownerP, OpPromote, &PromoteRequest{Tile: opAPI, From: "dev", To: "main"}); !ans.Unchanged {
			t.Errorf("promoting the same code again: %+v", ans)
		}
		// main → dev: dev gets main's code back, pinned → pinned.
		f.settle(opAPI, f.must(ownerP, OpRollback, &RollbackRequest{Tile: opAPI, Deployment: "main", Checkpoint: "c:" + tree1}))
		f.settle(opAPI, f.must(ownerP, OpPromote, &PromoteRequest{Tile: opAPI, From: "main", To: "dev"}))
		waitFor(t, "dev on main's code", func() bool { return cp(f.rec(opAPI), "dev") == tree1 })
	})

	t.Run("adding with live reload attached", func(t *testing.T) {
		f := newCodeFx(t, false)
		im := f.dry(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", Attach: true, DryRun: true})
		if im.Code != nil || f.st.Exists(opSite) {
			t.Fatalf("a zero-state dry add with attach: %+v, store %v", im, f.st.Exists(opSite))
		}
		ans := f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev", Attach: true})
		r := f.rec(opSite)
		if r.LiveReload != "dev" || cp(r, "dev") != "" || cp(r, "main") == "" || !ans.Deploy.FollowsWorkTree || ans.Deploy.How != "add" {
			t.Fatalf("add with attach: %+v, record %+v", ans.Deploy, r)
		}
		if log := f.st.logged(opSite); len(log) != 2 || !hasEntry(log, "main", "attach", cp(r, "main")) || !hasEntry(log, "dev", "add", cp(r, "main")) {
			t.Errorf("deploy log: %+v", log)
		}
		// Paused: the new deployment takes live reload, nothing else moves.
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		before := f.rec(opSite)
		f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "exp", Attach: true})
		r = f.rec(opSite)
		if r.LiveReload != "exp" || cp(r, "exp") != "" || cp(r, "dev") != cp(before, "dev") || cp(r, "main") != cp(before, "main") {
			t.Errorf("a paused add with attach: %+v", r)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		f := newCodeFx(t, false)
		for _, c := range []struct {
			what   string
			op     Op
			req    any
			status int
			msg    string
		}{
			{"a bad name", OpAdd, &AddRequest{Tile: opSite, Deployment: "Dev"}, 400, badNameMsg},
			{"no name", OpAdd, &AddRequest{Tile: opSite}, 400, "bad request body: deployment is required"},
			{"a bad from", OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", From: "yesterday"}, 400, `bad request body: from takes`},
			{"a bad id", OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", From: "c:xyz"}, 400, `"c:xyz" is not a checkpoint id`},
			{"a bad data", OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", Data: "copy"}, 400, `bad request body: data takes`},
			{"attach with from", OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", From: FromPrimary, Attach: true}, 400, "attach:true makes"},
			{"main", OpAdd, &AddRequest{Tile: opSite, Deployment: "main"}, 409, opSite + ` already has a deployment "main"`},
			{"a zero-state checkpoint", OpAdd, &AddRequest{Tile: opSite, Deployment: "dev", From: "c:1234567"}, 404, opSite + " has no checkpoint c:1234567"},
			{"attach on a zero-state tile", OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"}, 409, opSite + " has no deployments yet"},
			{"remove on a zero-state tile", OpRemove, &RemoveRequest{Tile: opSite, Deployment: "dev", Confirm: ConfirmErase}, 409, opSite + " has no deployments yet"},
			{"promote on a zero-state tile", OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main"}, 409, opSite + " has no deployments yet"},
			{"promote without to", OpPromote, &PromoteRequest{Tile: opSite, From: "dev"}, 400, "bad request body: from and to are required"},
			{"an unknown tile", OpAdd, &AddRequest{Tile: "apps/nope", Deployment: "dev"}, 404, "no such tile: apps/nope"},
		} {
			_, err := f.do(ownerP, c.op, c.req)
			wantErr(t, c.what, err, c.status, c.msg)
		}
		if f.rec(opSite) != nil || f.st.Exists(opSite) {
			t.Fatal("a refusal made deployment state")
		}

		f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		seq := f.rec(opSite).Seq
		for _, c := range []struct {
			what   string
			op     Op
			req    any
			status int
			msg    string
		}{
			{"a taken name", OpAdd, &AddRequest{Tile: opSite, Deployment: "dev"}, 409, opSite + ` already has a deployment "dev"`},
			{"an unknown checkpoint", OpAdd, &AddRequest{Tile: opSite, Deployment: "exp", From: "c:1234567"}, 404, opSite + " has no checkpoint c:1234567"},
			{"a stale seq", OpAdd, &AddRequest{Tile: opSite, Deployment: "exp", Seq: ptr(seq - 1)}, 409, "the deployments of " + opSite + " changed (seq "},
			{"attach while paused", OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"}, 409, "live reload is paused: resume it onto dev instead"},
			{"attach to an unknown deployment", OpAttach, &AttachRequest{Tile: opSite, Deployment: "exp"}, 404, opSite + ` has no deployment "exp"`},
			{"promote onto itself", OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "dev"}, 409, "promotion moves one deployment's code onto another"},
			{"promote from nowhere", OpPromote, &PromoteRequest{Tile: opSite, From: "exp", To: "main"}, 404, opSite + ` has no deployment "exp"`},
			{"promote a qualified target twice", OpPromote, &PromoteRequest{Tile: opSite + "+dev", From: "main", To: "exp"}, 400, `the tile ref names deployment "dev" and the body "exp"`},
			{"a stale expect", OpPromote, &PromoteRequest{Tile: opSite, From: "dev", To: "main", Expect: "c:0000000"}, 409, "the code changed since you reviewed c:0000000"},
			{"seed from a person without the manager gate", OpAdd, &AddRequest{Tile: opSite, Deployment: "exp", Data: DataSeed},
				403, "seeding a deployment's data is a tile manager's act"},
		} {
			pr := ownerP
			if strings.HasPrefix(c.what, "seed") {
				pr = userP("dev", opSite, "terminal")
			}
			_, err := f.do(pr, c.op, c.req)
			wantErr(t, c.what, err, c.status, c.msg)
		}
		_, err := f.do(terminalP("dev", opSite, "terminal"), OpAdd, &AddRequest{Tile: opSite, Deployment: "exp", Data: DataSeed})
		wantErr(t, "seed from a terminal token", err, 403, "seeding a deployment's data is a tile manager's act, done in a person's own session")
		_, err = f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "exp", Data: DataSeed})
		wantErr(t, "seed without confirm", err, 400, `seeding exp copies main's data, which may be personal: send confirm:"copy-data" to proceed`)
		_, err = f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "exp", Data: DataSeed, Confirm: ConfirmCopyData})
		wantErr(t, "seed, confirmed", err, http.StatusConflict, "apps/site is in the workspace scope, whose resources have one namespace: exp has no data of its own to seed")
		if f.rec(opSite).Seq != seq {
			t.Errorf("a refusal moved the record: seq %d → %d", seq, f.rec(opSite).Seq)
		}
	})

	t.Run("removing ends its deploys", func(t *testing.T) {
		f := newCodeFx(t, true)
		f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI})
		f.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "dev", From: FromPrimary})
		hold := make(chan struct{})
		f.run.set(func(r *fakeRunner) { r.before = hold })
		f.write(opAPI+"/main.go", "package main // v2\n")
		first := f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Deployment: "dev"})
		waitRunning(t, f.opsFx, opAPI, first.Deploy.ID)
		f.write(opAPI+"/main.go", "package main // v3\n")
		second := f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Deployment: "dev"})
		f.must(ownerP, OpRemove, &RemoveRequest{Tile: opAPI, Deployment: "dev", Confirm: ConfirmErase})
		for _, id := range []int64{first.Deploy.ID, second.Deploy.ID} {
			if e := f.wait(opAPI, id); e.Result != resultCancelled || !strings.Contains(e.Error, "dev was removed") {
				t.Errorf("deploy %d after the removal: %+v", id, e)
			}
		}
		n := len(runnerCommitErrs(f.opsFx))
		f.run.set(func(r *fakeRunner) { r.before = nil })
		close(hold)
		// Another commit (an earlier op's, still settling) may land first:
		// wait for the held deploy's, the one refused.
		refused := func() bool {
			for _, err := range runnerCommitErrs(f.opsFx)[n:] {
				if err != nil {
					return true
				}
			}
			return false
		}
		waitFor(t, "the held deploy's commit", refused)
		if errs := runnerCommitErrs(f.opsFx); !refused() || f.rec(opAPI).Deployments["dev"] != nil {
			t.Errorf("the held deploy's commit after the removal: %v", errs)
		}
		if df := f.p.Deploys(t.Context(), opAPI, "dev"); !reflect.DeepEqual(df, DeployFacts{}) {
			t.Errorf("a removed deployment's facts: %+v", df)
		}
	})
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

// covers D127e — promotion moves code only: B's switches, limits, the tile's
// edges and primary and every other deployment stay as they were, nothing
// of B's data, vault or registrations is dropped, and only B's pointer
// moves. From a deployment that follows the work tree the promotion deploys
// exactly the checkpoint named in expect (the dry run's impact.code.to), and
// a work tree that moved since answers 409; from a pinned one, expect must
// name its checkpoint.
func TestPromoteMovesCodeOnly(t *testing.T) {
	f := newCodeFx(t, false)
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev"})
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "exp"})
	f.must(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "dev"})
	if _, err := f.p.idx.commit(opSite, -1, func(r *Record) error {
		r.Edges = map[string]string{"slot:llm": EdgeBlock}
		m := r.Deployments["main"]
		m.Limits = map[string]int64{LimitPids: 64}
		e := r.Deployments["exp"]
		off := false
		e.Deliveries, e.AlwaysOn, e.Limits = &off, true, map[string]int64{LimitMemMiB: 256}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.took()
	strip := func(r *Record, moved string) *Record { // all but seq, the next deploy id and moved's code
		c := r.Clone()
		c.Seq, c.NextDeploy = 0, 0
		c.Deployments[moved].Checkpoint = nil
		return c
	}

	for _, step := range []struct{ from, to string }{{"dev", "main"}, {"dev", "exp"}, {"main", "exp"}, {"exp", "main"}} {
		f.write(opSite+"/index.html", "<h1>"+step.from+"→"+step.to+"</h1>")
		before := f.rec(opSite)
		im := f.dry(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: step.from, To: step.to, DryRun: true})
		if im.Data != "none" || im.Code.Deployment != step.to {
			t.Fatalf("%s → %s dry: %+v", step.from, step.to, im)
		}
		if before.Deployments[step.from].Checkpoint == nil { // reviewed, then the work tree moves
			f.write(opSite+"/index.html", "<h1>moved</h1>")
			_, err := f.do(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: step.from, To: step.to, Expect: im.Code.To})
			wantErr(t, step.from+" → "+step.to+" after the work tree moved", err, http.StatusConflict, "the code changed since you reviewed")
			f.write(opSite+"/index.html", "<h1>"+step.from+"→"+step.to+"</h1>")
		} else if _, err := f.do(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: step.from, To: step.to, Expect: "c:0000000"}); err == nil {
			t.Errorf("%s → %s with an expect that isn't %s's checkpoint passed", step.from, step.to, step.from)
		}
		f.must(ownerP, OpPromote, &PromoteRequest{Tile: opSite, From: step.from, To: step.to, Expect: im.Code.To})
		after := f.rec(opSite)
		if got := strings.TrimPrefix(im.Code.To, "c:"); !strings.HasPrefix(cp(after, step.to), got) {
			t.Errorf("%s → %s put %s, reviewed %s", step.from, step.to, cp(after, step.to), im.Code.To)
		}
		if !reflect.DeepEqual(strip(before, step.to), strip(after, step.to)) {
			t.Errorf("%s → %s changed more than %s's code:\n%+v\n%+v", step.from, step.to, step.to, before, after)
		}
		if calls := f.took(); len(calls) != 0 {
			t.Errorf("%s → %s touched %v", step.from, step.to, calls)
		}
	}
}

// covers D127j T3 — the + rule on the deployment side, for every creator
// (admins included): no deployment b on apps/site while a component exists
// at apps/site+b (409, nothing written); other names are free, and main and
// taken names are refused. A request naming apps/site+b acts on that tile,
// today's resolution, and that tile, its own name holding '+', gets no
// deployments (D127j, decided 2026-09-28), dry or not; it may still pause live
// reload.
func TestAddDeploymentNameCollision(t *testing.T) {
	f := newCodeFx(t, false)
	f.write(opSite+"+b/index.html", "<h1>b</h1>")
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	for _, pr := range []auth.Principal{ownerP, userP("dev", opSite, "terminal"), terminalP("dev", opSite, "terminal")} {
		_, err := f.do(pr, OpAdd, &AddRequest{Tile: opSite, Deployment: "b"})
		wantErr(t, "adding b as "+pr.From(), err, http.StatusConflict, "a tile exists at "+opSite+"+b; pick another name")
		_, err = f.do(pr, OpAdd, &AddRequest{Tile: opSite, Deployment: "b", DryRun: true})
		wantErr(t, "a dry add of b as "+pr.From(), err, http.StatusConflict, "a tile exists at "+opSite+"+b")
	}
	if f.rec(opSite) != nil || f.st.Exists(opSite) || len(f.took()) != 0 {
		t.Fatal("a refused add left deployment state")
	}
	f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "c"})
	if !f.p.HasDeployment(opSite, "c") || f.p.HasDeployment(opSite, "b") {
		t.Error("the free name wasn't added")
	}
	const plusTile = "apps/site+b's name holds '+', which names a tile deployment in URLs: it can't get deployments"
	for _, dry := range []bool{true, false} {
		_, err := f.do(ownerP, OpAdd, &AddRequest{Tile: opSite + "+b", Deployment: "d", DryRun: dry})
		wantErr(t, fmt.Sprintf("adding d to apps/site+b (dry %v)", dry), err, http.StatusConflict, plusTile)
	}
	if f.p.HasDeployment(opSite+"+b", "d") || f.rec(opSite+"+b") != nil {
		t.Error("apps/site+b got a deployment")
	}
	f.must(ownerP, OpPause, &PauseRequest{Tile: opSite + "+b"})
	if f.rec(opSite+"+b") == nil {
		t.Error("apps/site+b, today's tile, can't pause live reload")
	}
}

// covers T10 D127q — the admission caps on non-primary deployments
// (07-runtime §10.3): three per tile, twenty-four per workspace, each a 409
// of kind policy that changes nothing, which the state's allowed entry
// reports before anyone asks; removing one frees a place.
func TestDeploymentCountCaps(t *testing.T) {
	f := newCodeFx(t, false)
	var tiles []string
	for i := 0; i < 8; i++ {
		tile := "caps/t" + string(rune('a'+i))
		f.write(tile+"/index.html", "<h1>"+tile+"</h1>")
		tiles = append(tiles, tile)
	}
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"d1", "d2", "d3"} {
		f.add(ownerP, &AddRequest{Tile: opSite, Deployment: name})
	}
	seq := f.rec(opSite).Seq
	_, err := f.do(ownerP, OpAdd, &AddRequest{Tile: opSite, Deployment: "d4"})
	wantErr(t, "a fourth", err, http.StatusConflict, opSite+" has 3 non-primary deployments, the most allowed here")
	var e *Error
	if !errorsAs(err, &e) || e.Kind != KindPolicy || f.rec(opSite).Seq != seq {
		t.Errorf("the tile cap: %+v, seq %d → %d", e, seq, f.rec(opSite).Seq)
	}
	s := f.p.subjectOf(opSite, "")
	if c := f.p.Allowed(OpAdd, s); c.OK || c.Kind != KindPolicy || !strings.Contains(c.Why, "the most allowed here") {
		t.Errorf("Allowed at the tile cap: %+v", c)
	}
	if c := f.p.Policy(OpAdd, s); c.OK {
		t.Errorf("Policy at the tile cap: %+v", c)
	}

	// The workspace: 3 on apps/site, 3 on each of seven more, then 24.
	for _, tile := range tiles[:7] {
		for _, name := range []string{"d1", "d2", "d3"} {
			f.add(ownerP, &AddRequest{Tile: tile, Deployment: name})
		}
	}
	if c := f.p.CapsOf(tiles[7]); c.WorkspaceUsed != 24 || c.TileUsed != 0 {
		t.Fatalf("caps = %+v", c)
	}
	_, err = f.do(ownerP, OpAdd, &AddRequest{Tile: tiles[7], Deployment: "d1"})
	wantErr(t, "the 25th", err, http.StatusConflict, "the workspace has 24 non-primary deployments, the most allowed here")
	if f.rec(tiles[7]) != nil || f.st.Exists(tiles[7]) {
		t.Error("a refused opt-in left state")
	}
	f.must(ownerP, OpRemove, &RemoveRequest{Tile: tiles[0], Deployment: "d1", Confirm: ConfirmErase})
	f.add(ownerP, &AddRequest{Tile: tiles[7], Deployment: "d1"})
}

func errorsAs(err error, e **Error) bool {
	x, ok := err.(*Error)
	if ok {
		*e = x
	}
	return ok
}

// covers D127k T14 — chrome and xbin-capable tiles may pause live reload but
// can't have a non-primary deployment (409, kind policy, nothing written):
// a tile whose work tree asks for chrome, one whose pinned primary's code
// asks for it after the work tree stopped asking, one granted xbin:users,
// root and shell; and, read from the code at the request, a tile one of
// whose non-primary deployments runs code that asks for chrome, and a new
// deployment from a checkpoint that asks for it.
func TestP19RefusesChromeAndXbinTiles(t *testing.T) {
	f := newCodeFx(t, false)
	f.write("apps/chrome/xbin.json", `{"chrome":true}`)
	f.write("apps/chrome/index.html", "<h1>c</h1>")
	f.write("apps/gov/index.html", "<h1>g</h1>")
	f.write("apps/sneak/index.html", "<h1>s</h1>")
	f.write("xbin.json", `{"schema":1,"grants":[{"from":"apps/gov","target":"xbin:users","role":"writer"}]}`)
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	refused := func(tile, why string, allowed bool) {
		t.Helper()
		_, err := f.do(ownerP, OpAdd, &AddRequest{Tile: tile, Deployment: "dev"})
		wantErr(t, "adding to "+tile, err, http.StatusConflict, tile+" can't have non-primary deployments: "+why)
		var e *Error
		if !errorsAs(err, &e) || e.Kind != KindPolicy {
			t.Errorf("%s: kind %+v", tile, e)
		}
		if c := f.p.Allowed(OpAdd, f.p.subjectOf(tile, "")); c.OK != allowed {
			t.Errorf("Allowed(add) on %s: %+v", tile, c)
		}
	}
	refused("apps/chrome", chromeWhy, false)
	refused("apps/gov", "it holds the xbin:users grant, which governs the workspace", false)
	for _, tile := range []string{"apps/chrome", "apps/gov"} {
		f.must(ownerP, OpPause, &PauseRequest{Tile: tile})
		if r := f.rec(tile); r == nil || len(r.Deployments) != 1 {
			t.Errorf("%s paused: %+v", tile, r)
		}
	}
	// The work tree stops asking; the pinned primary's code still does.
	f.write("apps/chrome/xbin.json", `{}`)
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	refused("apps/chrome", chromeWhy, false)
	for _, tile := range []string{"root", "shell"} {
		if why := f.p.singleDeployment(tile); why != "it is the workspace's chrome" {
			t.Errorf("singleDeployment(%s) = %q", tile, why)
		}
	}

	// A non-primary deployment's code asks for chrome.
	f.add(ownerP, &AddRequest{Tile: "apps/sneak", Deployment: "dev"})
	f.write("apps/sneak/xbin.json", `{"chrome":true}`)
	f.must(ownerP, OpDeploy, &DeployRequest{Tile: "apps/sneak", Deployment: "dev"})
	chromeTree := cp(f.rec("apps/sneak"), "dev")
	if err := os.Remove(filepath.Join(f.root, "apps/sneak/xbin.json")); err != nil {
		t.Fatal(err)
	}
	if err := f.reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	seq := f.rec("apps/sneak").Seq
	_, err := f.do(ownerP, OpAdd, &AddRequest{Tile: "apps/sneak", Deployment: "exp"})
	wantErr(t, "a sibling of chrome code", err, http.StatusConflict, "apps/sneak can't have non-primary deployments: "+chromeWhy)
	// … and a new deployment from a checkpoint that asks for it.
	f.must(ownerP, OpRemove, &RemoveRequest{Tile: "apps/sneak", Deployment: "dev", Confirm: ConfirmErase})
	_, err = f.do(ownerP, OpAdd, &AddRequest{Tile: "apps/sneak", Deployment: "exp", From: "c:" + chromeTree})
	wantErr(t, "a checkpoint of chrome code", err, http.StatusConflict, "apps/sneak can't have non-primary deployments: "+chromeWhy)
	if r := f.rec("apps/sneak"); r.Deployments["exp"] != nil || r.Seq != seq+1 {
		t.Errorf("a refused add committed: %+v", r)
	}
	f.add(ownerP, &AddRequest{Tile: "apps/sneak", Deployment: "exp"}) // the work tree's own code doesn't ask
}

// covers T16 D119d D119e — what lands in the work tree (a builtin update in any
// mode, an applied PR: both write files there) reaches only the live reload
// target: with live reload attached to dev, a save drives dev alone, dev
// serves and runs the work tree, and main keeps serving and running its
// pinned checkpoint; while live reload is paused a save reaches no
// deployment, and every deployment keeps its code.
func TestWorkTreeWritersReachOnlyLiveTarget(t *testing.T) {
	for _, tile := range []string{opSite, opAPI} {
		t.Run(tile, func(t *testing.T) {
			f := newCodeFx(t, true)
			index := tile + "/index.html"
			f.write(index, "<h1>v1</h1>")
			f.add(ownerP, &AddRequest{Tile: tile, Deployment: "dev", Attach: true})
			f.drained(tile) // main's pin runs on its lane after the add answers; left running it writes into the workspace as the test's cleanup removes it
			c, _ := f.reg.Component(tile)
			f.write(index, "<h1>update</h1>") // the builtin update or the PR, in the work tree

			if dep, on := f.p.LiveReload(tile); dep != "dev" || !on {
				t.Fatalf("LiveReload = %q %v, want dev", dep, on)
			}
			mainCode, _ := f.p.CodeFor(tile, "main")
			devCode, _ := f.p.CodeFor(tile, "dev")
			if mainCode.WorkTree || mainCode.Tree == "" || !devCode.WorkTree {
				t.Errorf("CodeFor: main %+v, dev %+v", mainCode, devCode)
			}
			if got := served(t, f, c, "main"); got != "<h1>v1</h1>" {
				t.Errorf("main serves %q after the work tree changed", got)
			}
			if got := served(t, f, c, "dev"); got != "<h1>update</h1>" {
				t.Errorf("dev serves %q, want the work tree", got)
			}

			f.settle(tile, f.must(ownerP, OpPause, &PauseRequest{Tile: tile}))
			f.write(index, "<h1>another</h1>")
			if dep, on := f.p.LiveReload(tile); dep != "" || on {
				t.Errorf("LiveReload while paused = %q %v", dep, on)
			}
			if got := served(t, f, c, "dev"); got != "<h1>update</h1>" {
				t.Errorf("dev serves %q while paused", got)
			}
			if got := served(t, f, c, "main"); got != "<h1>v1</h1>" {
				t.Errorf("main serves %q while paused", got)
			}
		})
	}
}

// served reads index.html from where deployment dep of c serves its files.
func served(t *testing.T, f *codeFx, c *registry.Component, dep string) string {
	t.Helper()
	root, _, err := f.p.CodeRoot(c, dep)
	if err != nil {
		t.Fatalf("CodeRoot(%s): %v", dep, err)
	}
	b, err := os.ReadFile(filepath.Join(root, "index.html"))
	if err != nil {
		t.Fatalf("%s's index.html: %v", dep, err)
	}
	return string(b)
}

// viewOf is the pinned set the view repository holds for tile.
func viewOf(f *codeFx, tile string) map[string]string {
	f.st.mu.Lock()
	defer f.st.mu.Unlock()
	return cloneMap(f.st.views[tile])
}

// lastDeploy is the fake runner's last Deploy, "tile/dep@tree".
func lastDeploy(f *codeFx) string {
	f.run.mu.Lock()
	defer f.run.mu.Unlock()
	return f.run.deploys[len(f.run.deploys)-1]
}

// hasEntry reports whether the log holds an ok entry of how on dep putting tree there.
func hasEntry(log []attempt, dep, how, tree string) bool {
	for _, a := range log {
		if a.Deployment == dep && a.How == how && a.Tree == tree && a.Result == resultOK {
			return true
		}
	}
	return false
}
