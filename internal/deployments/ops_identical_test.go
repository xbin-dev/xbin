package deployments

import (
	"reflect"
	"testing"
)

// covers D127h SC-SAFE-DEPLOY — the plane tells the runner when a deploy
// serves the files the deployment already served (runner.Code.Identical,
// 07-runtime §8.5): a pause ships a capture of the work tree it followed, so
// its swap announces no reload; a reload now after an edit ships new files.
func TestPauseDeploysIdenticalCode(t *testing.T) {
	f := newOpsFx(t, true)
	f.settle(opAPI, f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI}))
	f.write(opAPI+"/main.go", "package main // edited\n")
	f.settle(opAPI, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI}))
	f.run.mu.Lock()
	got := append([]bool(nil), f.run.identical...)
	f.run.mu.Unlock()
	if want := []bool{true, false}; !reflect.DeepEqual(got, want) {
		t.Errorf("Deploy's Identical = %v, want %v (pause, then reload now)", got, want)
	}
}
