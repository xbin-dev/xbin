package boot

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/server"
)

// covers D131 12-compat — the branches/1 wire on a booted workspace: the
// feature listed, workTree.branch and each deployment's branch and
// branchOverride in the state, add's branch, the 409 naming both branches
// and confirm:"other-branch", the branch route (null clears; absent and
// unknown keys are 400s). And why a client gates on the feature: an
// xbind that predates it decodes the same bodies strictly and refuses the
// new fields.
func TestBranchesWire(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	d := zsBoot(t, zsWorkspace(t))
	owner := "Bearer " + d.owner
	head := func(b string) {
		p := filepath.Join(d.ws, "apps/zs/.git/HEAD")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("ref: refs/heads/"+b+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	state := func() map[string]any {
		t.Helper()
		code, b := d.do(t, "GET", "/api/xbin/deployments?tile=apps/zs", owner)
		if code != http.StatusOK {
			t.Fatalf("state: %d %s", code, b)
		}
		var s map[string]any
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	row := func(s map[string]any, name string) map[string]any {
		for _, r := range s["deployments"].([]any) {
			if m := r.(map[string]any); m["name"] == name {
				return m
			}
		}
		t.Fatalf("no deployment %s in %v", name, s["deployments"])
		return nil
	}
	post := func(path, body string, want int, says ...string) {
		t.Helper()
		code, b := d.post(t, "/api/xbin/deployments/"+path, body, owner)
		if code != want {
			t.Fatalf("POST %s %s: %d %s, want %d", path, body, code, b, want)
		}
		for _, s := range says {
			if !strings.Contains(string(b), s) {
				t.Errorf("POST %s: %s doesn't say %q", path, b, s)
			}
		}
	}

	head("feature")
	s := state()
	var features []string
	for _, f := range s["features"].([]any) {
		features = append(features, f.(string))
	}
	if !slices.Contains(features, "branches/1") {
		t.Fatalf("features %v lacks branches/1", features)
	}
	if wt, _ := s["workTree"].(map[string]any); wt == nil || wt["branch"] != "feature" {
		t.Errorf("the zero state's workTree for the owner: %v", s["workTree"])
	}

	post("add", `{"tile":"apps/zs","deployment":"dev","attach":true,"branch":"feature"}`, 200)
	s = state()
	if r := row(s, "dev"); r["branch"] != "feature" || r["branchOverride"] != nil {
		t.Errorf("dev's row: branch %v, override %v", r["branch"], r["branchOverride"])
	}
	if r := row(s, "main"); r["branch"] != nil {
		t.Errorf("main's row carries a branch: %v", r["branch"])
	}
	if can, _ := row(s, "dev")["can"].(map[string]any); can == nil || can["branch"] == nil {
		t.Errorf("dev's can lacks branch: %v", row(s, "dev")["can"])
	}

	post("live-reload/pause", `{"tile":"apps/zs"}`, 200)
	head("main")
	post("live-reload/resume", `{"tile":"apps/zs","deployment":"dev"}`, 409,
		"dev is assigned branch feature, and the work tree is on main", `confirm:\"other-branch\"`)
	post("live-reload/resume", `{"tile":"apps/zs","deployment":"dev","confirm":"other-branch"}`, 200)
	if r := row(state(), "dev"); r["branchOverride"] != "main" || r["liveReload"] != true {
		t.Errorf("after resume with other-branch: %v", r)
	}
	if wt := state()["workTree"].(map[string]any); wt["branch"] != "main" {
		t.Errorf("workTree %v", wt)
	}

	post("branch", `{"tile":"apps/zs","deployment":"dev"}`, 400, "branch is required")
	post("branch", `{"tile":"apps/zs","deployment":"dev","branch":"x","other":1}`, 400, "unknown field")
	post("branch", `{"tile":"apps/zs","deployment":"main","branch":"x"}`, 409, "takes no assigned branch")
	post("branch", `{"tile":"apps/zs","deployment":"dev","branch":null}`, 200)
	if r := row(state(), "dev"); r["branch"] != nil || r["branchOverride"] != nil {
		t.Errorf("after branch:null: %v", r)
	}

	// An xbind before branches/1: the same bodies, decoded strictly.
	type addM2 struct {
		Tile, Deployment, From, Data, Confirm string
		Attach, DryRun                        bool
		Seq                                   *int64
	}
	type resumeM1 struct {
		Tile, Deployment string
		DryRun           bool
		Seq              *int64
	}
	for _, c := range []struct {
		body string
		into any
	}{
		{`{"tile":"apps/zs","deployment":"qa","branch":"feature"}`, &addM2{}},
		{`{"tile":"apps/zs","deployment":"qa","newBranch":"feature"}`, &addM2{}},
		{`{"tile":"apps/zs","deployment":"dev","confirm":"other-branch"}`, &resumeM1{}},
	} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(c.body))
		if err := server.DecodeJSON(r, c.into); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("an older xbind's decode of %s: %v, want an unknown field", c.body, err)
		}
	}
}
