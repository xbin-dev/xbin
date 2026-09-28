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
func TestPinsDeployIdenticalCode(t *testing.T) {
	f := newGovFx(t, true)
	f.write("xbin.json", `{"schema":1}`)
	deploys := func(n int) []string {
		t.Helper()
		waitFor(t, "the deploys", func() bool { f.run.mu.Lock(); defer f.run.mu.Unlock(); return len(f.run.deploys) >= n })
		f.run.mu.Lock()
		defer f.run.mu.Unlock()
		var out []string
		for i, d := range f.run.deploys {
			out = append(out, strings.SplitN(d, "@", 2)[0]+map[bool]string{true: " identical", false: " new"}[f.run.identical[i]])
		}
		return out
	}

	t.Run("backend", func(t *testing.T) {
		f.add(ownerP, &AddRequest{Tile: opAPI, Deployment: "dev", Attach: true})  // main pinned in place
		f.must(ownerP, OpAttach, &AttachRequest{Tile: opAPI, Deployment: "main"}) // dev pinned in place
		deploys(2)
		protectOf(t)(f.do(ownerP, OpProtect, &ProtectRequest{Tile: opAPI, On: ptr(true)})) // main pinned in place
		deploys(3)
		f.write(opAPI+"/main.go", "package main // edited\n")
		f.settle(opAPI, f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Deployment: "dev"})) // new files
		want := []string{opAPI + "/main identical", opAPI + "/dev identical", opAPI + "/main identical", opAPI + "/dev new"}
		if got := deploys(4); !reflect.DeepEqual(got, want) {
			t.Errorf("the runner's deploys = %v, want %v", got, want)
		}
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
