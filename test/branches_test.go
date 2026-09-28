//go:build integration

package test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// covers D131 — branch-assigned deployments on an isolated xbind, where the
// git switch and every capture run confined: add's newBranch creates and
// checks out the branch in the tile; saves on it reach dev; a checkout of
// another branch followed by a save deploys nothing, pauses live reload with
// dev still serving its last save on its branch, and op branch says so;
// resuming dev from the other branch is refused until confirmed "this
// time"; newBranch never takes an existing branch.
func TestBranchAssignedDeployments(t *testing.T) {
	t.Parallel()
	d := startIsolatedDaemon(t, isoOpts{})
	a := d.dl()
	const tile = "apps/branches"
	dir := filepath.Join(d.WS, filepath.FromSlash(tile))
	for _, f := range [][2]string{ // the manifest last
		{"index.html", "<!doctype html><p>branches</p>\n"},
		{probeFile, "v1"},
		{"xbin.json", `{"title":"branches"}` + "\n"},
	} {
		must(t, saveIfChanged(filepath.Join(dir, f[0]), f[1]))
	}
	a.waitServed(t, tile, "static", "v1", 30*time.Second)
	head := filepath.Join(dir, ".git", "HEAD")
	if !waitFor(func() bool { _, err := os.Stat(head); return err == nil }, 20*time.Second) {
		for _, args := range [][]string{{"init", "-q", "-b", "main"}} { // xbind makes one per component; this box's didn't yet
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	gitIn("add", "-A")
	gitIn("commit", "-q", "--allow-empty", "-m", "v1")
	ev := a.tape(t)

	// newBranch: a confined git switch in the tile, then dev follows it.
	ans, e := a.op(t, "add", tile, "deployment", "dev", "attach", true, "newBranch", "feature")
	if e.Result != "ok" || ans.State.LiveReload != "dev" {
		t.Fatalf("add dev with newBranch: %+v, state %+v", e, ans.State)
	}
	if b, _ := os.ReadFile(head); strings.TrimSpace(string(b)) != "ref: refs/heads/feature" {
		t.Fatalf("after newBranch HEAD is %q", b)
	}
	branchOf := func() (dep, wt string) {
		t.Helper()
		code, body := a.do("GET", "/api/xbin/deployments?tile="+tile, "")
		var st struct {
			WorkTree    struct{ Branch string } `json:"workTree"`
			Deployments []struct {
				Name   string `json:"name"`
				Branch string `json:"branch"`
			} `json:"deployments"`
		}
		if code != 200 || json.Unmarshal([]byte(body), &st) != nil {
			t.Fatalf("state: %d %s", code, body)
		}
		for _, x := range st.Deployments {
			if x.Name == "dev" {
				dep = x.Branch
			}
		}
		return dep, st.WorkTree.Branch
	}
	if dep, wt := branchOf(); dep != "feature" || wt != "feature" {
		t.Fatalf("dev's branch %q, the work tree's %q", dep, wt)
	}
	c, _, raw := a.post(t, "add", dlBody(tile, "deployment", "qa", "from", "primary", "newBranch", "main"))
	if c != 409 || !strings.Contains(raw, "already has a branch main") {
		t.Errorf("newBranch onto main: %d %s", c, raw)
	}

	// A save on feature reaches dev, once checked and captured.
	m := ev.mark()
	must(t, saveIfChanged(filepath.Join(dir, probeFile), "v2"))
	if _, ok := ev.wait(m, func(e dlEvent) bool {
		return e.Component == tile && strings.Contains(e.raw, `"op":"reload","deployment":"dev"`)
	}, 30*time.Second); !ok {
		t.Fatalf("a save on feature never reached dev: %s", ev.describe(m, tile))
	}
	if c, b := a.do("GET", "/c/"+tile+"+dev/"+probeFile, ""); c != 200 || b != "v2" {
		t.Fatalf("dev serves %d %q, want v2", c, b)
	}

	// Another branch, then a save: nothing deploys, live reload pauses, dev
	// keeps v2.
	gitIn("add", "-A")
	gitIn("commit", "-q", "-m", "v2")
	gitIn("switch", "-q", "main")
	m = ev.mark()
	must(t, saveIfChanged(filepath.Join(dir, probeFile), "v3"))
	got, ok := ev.wait(m, func(e dlEvent) bool { return e.Component == tile && e.op() == "branch" }, 30*time.Second)
	if !ok {
		t.Fatalf("no op branch after the switch: %s", ev.describe(m, tile))
	}
	var be struct {
		Deployment, Assigned, WorkTree, Related string
		Paused                                  bool
	}
	_ = json.Unmarshal(got.Data, &be)
	if be.Deployment != "dev" || be.Assigned != "feature" || be.WorkTree != "main" || !be.Paused {
		t.Errorf("op branch %s", got.raw)
	}
	st := a.state(t, tile)
	if st.LiveReload != "" || st.LastLiveReload != "dev" || st.pinned("dev") == "" {
		t.Fatalf("after the switch: live reload %q, last %q, dev pinned %q", st.LiveReload, st.LastLiveReload, st.pinned("dev"))
	}
	if !waitFor(func() bool { c, b := a.do("GET", "/c/"+tile+"+dev/"+probeFile, ""); return c == 200 && b == "v2" }, 30*time.Second) {
		c, b := a.do("GET", "/c/"+tile+"+dev/"+probeFile, "")
		t.Fatalf("dev serves %d %q after the switch, want its last save on feature (v2)", c, b)
	}

	// Resuming dev from main: refused, then taken this time.
	c, _, raw = a.post(t, "live-reload/resume", dlBody(tile, "deployment", "dev"))
	if c != 409 || !strings.Contains(raw, "dev is assigned branch feature, and the work tree is on main") {
		t.Fatalf("resume from main: %d %s", c, raw)
	}
	a.op(t, "live-reload/resume", tile, "deployment", "dev", "confirm", "other-branch")
	if !waitFor(func() bool { c, b := a.do("GET", "/c/"+tile+"+dev/"+probeFile, ""); return c == 200 && b == "v3" }, 30*time.Second) {
		t.Fatal("dev never followed main's work tree after the confirmed resume")
	}
}
