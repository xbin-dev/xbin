package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// dlBranchSt is a tile with dev beside main, live reload on main, from an
// xbind that speaks features.
func dlBranchSt(paused bool, features ...string) dlSt {
	st := dlAttached().with("features", features,
		"deployments", []any{dlDep("main", "", true, dlAllCan()), dlDevDep("c:1111111")})
	if paused {
		st = st.with("liveReload", "", "lastLiveReload", "dev")
	}
	return st
}

var (
	dlOldFeatures = []string{"live-reload/1", "deployments/1"}
	dlNewFeatures = []string{"live-reload/1", "deployments/1", "branches/1"}
)

// covers D131 12-compat — bx sends branches/1's body fields (the branch
// route, add's branch and newBranch, confirm:"other-branch") only to an
// xbind whose state lists it: an older one decodes bodies strictly. There,
// the commands that need it stop before sending anything, and
// --other-branch, which an older xbind has nothing to confirm for, is left
// out.
func TestBxBranchesFeatureGate(t *testing.T) {
	for _, c := range []struct {
		name     string
		features []string
		paused   bool
		args     []string
		answers  []string // POST routes answered, dry and real
		want     []string
		code     int
		says     string
	}{
		{name: "add --branch, older xbind", features: dlOldFeatures,
			args: []string{"deployment", "add", "apps/x", "qa", "--branch", "feature", "--yes"},
			want: []string{dlGetURI}, code: exitFailed, says: "lists no branches/1"},
		{name: "branch, older xbind", features: dlOldFeatures,
			args: []string{"deployment", "branch", "apps/x", "dev", "feature", "--yes"},
			want: []string{dlGetURI}, code: exitFailed, says: "upgrade xbind"},
		{name: "resume --other-branch, older xbind", features: dlOldFeatures, paused: true,
			args:    []string{"live-reload", "resume", "apps/x", "--to", "dev", "--other-branch", "--yes"},
			answers: []string{"live-reload/resume"},
			want: []string{dlGetURI,
				`POST /api/xbin/deployments/live-reload/resume {"deployment":"dev","dryRun":true,"tile":"apps/x"}`,
				`POST /api/xbin/deployments/live-reload/resume {"deployment":"dev","tile":"apps/x"}`}},
		{name: "resume --other-branch", features: dlNewFeatures, paused: true,
			args:    []string{"live-reload", "resume", "apps/x", "--to", "dev", "--other-branch", "--yes"},
			answers: []string{"live-reload/resume"},
			want: []string{dlGetURI,
				`POST /api/xbin/deployments/live-reload/resume {"confirm":"other-branch","deployment":"dev","dryRun":true,"tile":"apps/x"}`,
				`POST /api/xbin/deployments/live-reload/resume {"confirm":"other-branch","deployment":"dev","tile":"apps/x"}`}},
		{name: "add --new-branch --attach", features: dlNewFeatures,
			args:    []string{"deployment", "add", "apps/x", "qa", "--new-branch", "feat/x", "--attach", "--yes"},
			answers: []string{"add"},
			want: []string{dlGetURI,
				`POST /api/xbin/deployments/add {"attach":true,"deployment":"qa","dryRun":true,"newBranch":"feat/x","tile":"apps/x"}`,
				`POST /api/xbin/deployments/add {"attach":true,"deployment":"qa","newBranch":"feat/x","tile":"apps/x"}`}},
		{name: "add --seed --branch --other-branch", features: dlNewFeatures,
			args:    []string{"deployment", "add", "apps/x", "qa", "--seed", "--branch", "release", "--other-branch", "--yes"},
			answers: []string{"add"},
			want: []string{dlGetURI,
				`POST /api/xbin/deployments/add {"branch":"release","confirm":"copy-data,other-branch","data":"seed","deployment":"qa","dryRun":true,"tile":"apps/x"}`,
				`POST /api/xbin/deployments/add {"branch":"release","confirm":"copy-data,other-branch","data":"seed","deployment":"qa","seq":7,"tile":"apps/x"}`}},
		{name: "branch", features: dlNewFeatures,
			args:    []string{"deployment", "branch", "apps/x", "dev", "feature/y", "--yes"},
			answers: []string{"branch"},
			want: []string{dlGetURI,
				`POST /api/xbin/deployments/branch {"branch":"feature/y","deployment":"dev","dryRun":true,"tile":"apps/x"}`,
				`POST /api/xbin/deployments/branch {"branch":"feature/y","deployment":"dev","tile":"apps/x"}`}},
		{name: "branch --clear, a qualified ref", features: dlNewFeatures,
			args:    []string{"deployment", "branch", "apps/x+dev", "--clear", "--yes"},
			answers: []string{"branch"},
			want: []string{"GET /api/xbin/deployments?deployment=dev&tile=apps%2Fx",
				`POST /api/xbin/deployments/branch {"branch":null,"dryRun":true,"tile":"apps/x+dev"}`,
				`POST /api/xbin/deployments/branch {"branch":null,"tile":"apps/x+dev"}`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := dlBranchSt(c.paused, c.features...)
			if strings.Contains(strings.Join(c.args, " "), "apps/x+dev") {
				st = st.with("selected", "dev")
			}
			f := newDLFake().on(dlGet, 200, st.json())
			for _, r := range c.answers {
				f.on(dlPost+r+" dry", 200, dlAnswer(st, "impact", dlImpact("", "", false, "nobody")))
				f.on(dlPost+r, 200, dlAnswer(st))
			}
			srv := httptest.NewServer(f)
			defer srv.Close()
			res := dlRun(t, srv.URL, false, "", c.args...)
			if res.code != c.code {
				t.Fatalf("exit %d, want %d\n%s%s", res.code, c.code, res.out, res.err)
			}
			if c.says != "" && !strings.Contains(res.err, c.says) {
				t.Errorf("stderr %q doesn't say %q", res.err, c.says)
			}
			if got := f.take(); !reflect.DeepEqual(got, c.want) {
				t.Errorf("requests\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(c.want, "\n  "))
			}
		})
	}
}

// covers D131 — the 409 naming both branches carries bx's own flag.
func TestBxBranchRefusalHint(t *testing.T) {
	st := dlBranchSt(true, dlNewFeatures...)
	f := newDLFake().on(dlGet, 200, st.json()).on(dlPost+"live-reload/resume dry", http.StatusConflict,
		`{"error":"dev is assigned branch feature, and the work tree is on main: check out feature, or send confirm:\"other-branch\" to use main this time"}`)
	srv := httptest.NewServer(f)
	defer srv.Close()
	res := dlRun(t, srv.URL, false, "", "live-reload", "resume", "apps/x", "--to", "dev", "--yes")
	if res.code != exitFailed || !strings.Contains(res.err, "dev is assigned branch feature") || !strings.Contains(res.err, "with bx: --other-branch") {
		t.Errorf("exit %d, stderr %q", res.code, res.err)
	}
	if res := dlRun(t, srv.URL, false, "", "rollback", "apps/x", "--to", "dev", "--other-branch"); res.code != exitUsage {
		t.Errorf("rollback took --other-branch: exit %d", res.code)
	}
	if res := dlRun(t, srv.URL, false, "", "deployment", "branch", "apps/x", "dev", "-x"); res.code != exitUsage {
		t.Errorf("branch took -x: exit %d", res.code)
	}
}
