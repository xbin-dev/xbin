package deployments

import (
	"reflect"
	"strings"
	"testing"
)

// covers D127h SC-EVENTS — a pin in place ships the files the deployment
// served, like a pause (runner.Code.Identical, 07-runtime §8.5; 11-contract
// §3.5: a swap reloads the primary only when its code changed): adding a
// deployment with live reload attached pins main where it stands, attaching
// live reload elsewhere pins the old target, and protecting a primary that
// follows the work tree pins it. A backend's pins reach the runner as
// identical code; a static tile's announce no reload, bare or op reload. A
// deploy of new files still reloads.
//
// A backend's pin runs on its deployment's lane after the operation answers
// (the answer names the follow or the protect, not the pin), so each step
// waits for the tile's deploys to drain before the next: attaching main
// while the add's pin of main still waits cancels it, as it should (live
// reload drives main again), and nothing would reach the runner for it.
func TestPinsDeployIdenticalCode(t *testing.T) {
	f := newGovFx(t, true)
	f.write("xbin.json", `{"schema":1}`)

	t.Run("backend", func(t *testing.T) {
		var want []string
		step := func(what string, deploys ...string) {
			t.Helper()
			f.drained(opAPI)
			want = append(want, deploys...)
			f.run.mu.Lock()
			var got []string
			for i, d := range f.run.deploys {
				got = append(got, strings.SplitN(d, "@", 2)[0]+map[bool]string{true: " identical", false: " new"}[f.run.identical[i]])
			}
			f.run.mu.Unlock()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("after %s the runner's deploys = %v, want %v", what, got, want)
			}
		}
		f.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "dev", Attach: true})
		step("adding dev with attach", opAPI+"/main identical") // main pinned in place
		f.must(ownerP, OpAttach, &AttachRequest{Tile: opAPI, Deployment: "main"})
		step("attaching main", opAPI+"/dev identical") // dev pinned in place
		protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opAPI, On: ptr(true)}))
		step("protecting", opAPI+"/main identical") // main pinned in place
		f.write(opAPI+"/main.go", "package main // edited\n")
		f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Deployment: "dev"})
		step("a deploy of new files", opAPI+"/dev new")
	})

	t.Run("static", func(t *testing.T) {
		bare, devReload := `reload {} `+opSite, `deployments {"op":"reload","deployment":"dev"} `+opSite
		f.evs.take()
		f.add(ownerP, &AddRequest{Tile: opSite, Deployment: "dev", Attach: true})
		if evs := f.evs.take(); hasEvent(evs, bare) {
			t.Errorf("add with attach reloaded the primary, whose files didn't change: %v", evs)
		}
		f.must(ownerP, OpAttach, &AttachRequest{Tile: opSite, Deployment: "main"})
		if evs := f.evs.take(); hasEvent(evs, devReload) {
			t.Errorf("attaching main reloaded dev, pinned where it stood: %v", evs)
		}
		protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opSite, On: ptr(true)}))
		if evs := f.evs.take(); hasEvent(evs, bare) {
			t.Errorf("protect reloaded the primary, pinned where it stood: %v", evs)
		}
		f.write(opSite+"/index.html", "<h1>edited</h1>")
		f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Deployment: "dev"})
		if evs := f.evs.take(); !hasEvent(evs, devReload) {
			t.Errorf("a deploy of new files to dev didn't reload it: %v", evs)
		}
	})
}
