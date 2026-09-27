package deployments

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers P5 PO-15 SC-OPT-OUT Z12 — the plane half of
// TestOptOutReturnsToZeroState: pausing live reload, reloading now and
// resuming on a main-only tile removes the record, the deploy journal and
// the view repository; the checkpoint store and its deploy log are the only
// leftovers. The tile then answers every hook — the code it runs, the env
// and remap, live reload, the fetch remote's record question, the registry's
// composition, where it serves from, its state — exactly as a never-opted-in
// twin does, and nothing more names it in an event.
func TestOptOutReturnsToZeroState(t *testing.T) {
	for _, iso := range []struct {
		name, tile, twin string
		files            map[string]string
	}{
		{"static", opSite, "apps/twin", map[string]string{"xbin.json": `{}`, "index.html": "<h1>v1</h1>"}},
		{"go", opAPI, "apps/gotwin", map[string]string{"xbin.json": `{"runtime":"go"}`, "main.go": "package main\n"}},
	} {
		t.Run(iso.name, func(t *testing.T) {
			f := newOpsFx(t, true)
			for rel, body := range iso.files {
				f.write(iso.twin+"/"+rel, body)
			}
			if err := f.reg.Rescan(); err != nil {
				t.Fatal(err)
			}
			f.p.TileEnv = func(c *registry.Component) []string { return []string{"XBIN_COMPONENT=" + c.Path} }
			tile := iso.tile

			a := f.must(ownerP, OpPause, &PauseRequest{Tile: tile})
			f.wait(tile, a.Deploy.ID)
			if !fileExists(viewDir(f.root, tile)) || !fileExists(filepath.Join(journalDir(f.root, tile), journalFile)) {
				t.Fatal("pausing made no view repository or journal")
			}
			f.write(tile+"/extra.txt", "edit\n")
			a = f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: tile})
			f.wait(tile, a.Deploy.ID)
			f.must(ownerP, OpResume, &ResumeRequest{Tile: tile})
			f.evs.take()
			if r := f.rec(tile); r != nil {
				t.Fatalf("resume on a main-only tile kept its record: %+v", r)
			}

			// On disk: the store and nothing else.
			for _, gone := range []string{recordPath(f.root, tile), journalDir(f.root, tile), viewDir(f.root, tile), recordDir(f.root)} {
				if fileExists(gone) {
					t.Errorf("%s survived the opt-out", gone)
				}
			}
			if !fileExists(storeDir(f.root, tile)) || len(f.st.logged(tile)) != 3 {
				t.Errorf("the store (%v) and its deploy log (%d entries) must stay", fileExists(storeDir(f.root, tile)), len(f.st.logged(tile)))
			}
			if got := f.p.DeploymentLeftovers(tile); !reflect.DeepEqual(got, []string{"checkpoint store"}) {
				t.Errorf("leftovers = %v", got)
			}

			// Every hook answers as the twin's.
			c, _ := f.reg.Component(tile)
			tw, _ := f.reg.Component(iso.twin)
			if c.WorkTree != nil || c.Kept || !reflect.DeepEqual(c.Manifest, tw.Manifest) || c.HasIndex != tw.HasIndex {
				t.Errorf("the registry composes %+v, the twin %+v", c, tw)
			}
			type answers struct {
				Code      any
				CodeErr   string
				Env       []string
				Remap     any
				Live      string
				Attached  bool
				Record    bool
				Pinned    bool
				Root      string
				RootPin   bool
				Primary   string
				HasMain   bool
				Leftovers []string
			}
			of := func(tile string, c *registry.Component) answers {
				var a answers
				code, err := f.p.CodeFor(tile, util.MainDeployment)
				a.Code = code
				if err != nil {
					a.CodeErr = err.Error()
				}
				env, remap := f.p.EnvFor(c, util.MainDeployment)
				a.Env = []string{strings.Replace(strings.Join(env, ","), tile, "<tile>", 1)}
				a.Remap = remap
				a.Live, a.Attached = f.p.LiveReload(tile)
				a.Record = f.p.HasRecord(tile)
				_, a.Pinned = f.p.PinnedPrimary(tile)
				root, pin, _ := f.p.CodeRoot(c, "")
				a.Root, a.RootPin = strings.TrimPrefix(root, c.Dir), pin
				a.Primary, a.HasMain = f.p.Primary(tile), f.p.HasDeployment(tile, util.MainDeployment)
				return a
			}
			if got, want := of(tile, c), of(iso.twin, tw); !reflect.DeepEqual(got, want) {
				t.Errorf("the opted-out tile answers\n  %+v\nits twin\n  %+v", got, want)
			}
			for _, op := range []Op{OpPause, OpResume, OpReloadNow, OpDeploy, OpRollback} {
				s, ts := f.p.subjectOf(tile, util.MainDeployment), f.p.subjectOf(iso.twin, util.MainDeployment)
				s.Tile, ts.Tile = "", ""
				if s != ts {
					t.Errorf("%s's subject %+v, its twin's %+v", op, s, ts)
				}
				if a, b := f.p.Policy(op, f.p.subjectOf(tile, util.MainDeployment)), f.p.Policy(op, f.p.subjectOf(iso.twin, util.MainDeployment)); a != b {
					t.Errorf("Policy(%s) = %+v, the twin's %+v", op, a, b)
				}
			}
			if df := f.p.Deploys(t.Context(), tile, util.MainDeployment); !reflect.DeepEqual(df, DeployFacts{}) {
				t.Errorf("an opted-out tile's deploy facts = %+v", df)
			}
			if es, _ := f.p.Log(t.Context(), tile, "", 0, 0); len(es) != 0 {
				t.Errorf("an opted-out tile's log reads %d entries: its store is inert", len(es))
			}
			if evs := f.evs.take(); len(evs) != 0 {
				t.Errorf("reading an opted-out tile published %+v", evs)
			}
			if _, err := os.Lstat(filepath.Join(f.root, "data", "deployments")); err == nil {
				t.Error("data/deployments survived")
			}
		})
	}
}
