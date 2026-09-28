package boot

// deployfacts_test.go — the state's facts beyond the record (11-contract
// §1.1) in each view (§1.3), and what a reader of a tile with deployments
// sees (D127l; 06-security T7 item 3).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// factSources are sources for every fact beyond the record, each naming the
// deployment it answers for, so a leak of a non-primary one shows as its
// name.
func factSources() factReads {
	return factReads{
		data: func(tile, dep string) *deployments.DataState {
			return &deployments.DataState{State: "seeded", From: "main", By: "user:" + dep}
		},
		vault: func(tile, dep string) (deployments.VaultSummary, error) {
			return deployments.VaultSummary{Keys: 4, Placeholders: len(dep)}, nil
		},
		limits:    func(tile, dep string) cgroup.Limits { return cgroup.Limits{MemMax: 2 << 30, PidsMax: 512} },
		diskQuota: func(string) int64 { return 50 << 30 },
		declared:  func(c *registry.Component, dep string) (bool, bool) { return dep == "dev", true },
		registrations: func(tile, dep string) []deployments.Registration {
			return []deployments.Registration{{Kind: deployments.RegCron, Name: "nightly-" + dep, Schedule: "0 3 * * *", Path: "/tick", Dormant: dep != "main"}}
		},
		wouldNotify: func(tile, dep string) []deployments.WouldNotify {
			return []deployments.WouldNotify{{At: "2026-09-27T12:00:00Z", To: "user:bob", Title: "hello from " + dep}}
		},
		backup: func(tile, dep string) *deployments.BackupSchedule {
			return &deployments.BackupSchedule{Schedule: "0 3 * * *", Retention: 3}
		},
		caps: func(string) deployments.Caps {
			return deployments.Caps{Tile: 3, TileUsed: 1, Workspace: 24, WorkspaceUsed: 1}
		},
		edges: func(string) []deployments.Edge {
			return []deployments.Edge{{ID: "slot:llm", Kind: "http", To: "apps/llm", Policy: "read", Default: "read", Values: []string{"read", "block"}}}
		},
	}
}

// factKeys are the Deployment keys beyond the record.
var factKeys = []string{"data", "vault", "limits", "deliveries", "alwaysOn", "alwaysOnDeclared", "backup", "registrations", "wouldNotify"}

// rowOf is the state's row of deployment name, nil when absent.
func rowOf(s map[string]any, name string) map[string]any {
	for _, r := range s["deployments"].([]any) {
		if m := r.(map[string]any); m["name"] == name {
			return m
		}
	}
	return nil
}

// covers D127l T7 D127h D127i D127n PO-14 — a reader of a tile with deployments sees
// primary-scoped facts only: GET /deployments answers a person with read,
// the primary's frame token and the tile's instance token the reader view
// with none of the facts beyond the record, even with a source for each of
// them that names its deployment, and removing the non-primary deployment
// leaves each reader's answer byte-identical. The write audience gets every
// fact on every deployment (deliveries always on and alwaysOn the code's
// for the primary, deliveries on by default beside it (D127h, revised), never
// the primary's backup schedule or held notifications) and
// caps and edges; a non-primary deployment's own principals get them on
// the primary and their own deployment only, without caps or edges; the
// zero state gets none. The state's status of a deployment beyond the
// primary comes from the runner's rows for it. Through the real daemon, a
// reader's GET /deployments and the reader's /components entry are
// byte-identical whether or not the tile has a deployment beyond main, and
// the summary names only the primary.
func TestReaderSeesPrimaryOnly(t *testing.T) {
	f := newDplFix(t)
	f.api.reads.factReads = factSources()

	// The full view: every fact, on every deployment.
	full := f.get(t, dplWriter, "/deployments?tile=apps/crm", 200)
	main, dev := rowOf(full, "main"), rowOf(full, "dev")
	if main == nil || dev == nil {
		t.Fatalf("the full view's rows: %s", dplJSON(full["deployments"]))
	}
	for _, k := range factKeys {
		if dev[k] == nil {
			t.Errorf("the full view: dev has no %s", k)
		}
		if main[k] == nil && k != "backup" && k != "wouldNotify" {
			t.Errorf("the full view: main has no %s", k)
		}
	}
	if main["backup"] != nil || main["wouldNotify"] != nil {
		t.Errorf("the primary carries a backup schedule or held notifications: %s", dplJSON(main))
	}
	if main["deliveries"] != true || dev["deliveries"] != true || main["alwaysOn"] != false || dev["alwaysOnDeclared"] != true {
		t.Errorf("switches: main %v/%v, dev %v/%v", main["deliveries"], main["alwaysOn"], dev["deliveries"], dev["alwaysOnDeclared"])
	}
	if got := dplJSON(dev["limits"]); got != `{"diskGiB":50,"memMiB":2048,"pids":512}` {
		t.Errorf("dev's limits: %s", got)
	}
	if dplJSON(full["caps"]) != `{"tile":3,"tileUsed":1,"workspace":24,"workspaceUsed":1}` || len(full["edges"].([]any)) != 1 {
		t.Errorf("the full view's caps %s, edges %s", dplJSON(full["caps"]), dplJSON(full["edges"]))
	}

	// A deployment view: the primary's and its own facts; no caps, no edges.
	own := tok("apps/crm", "frame", dplWriter)
	own.Deployment = "dev"
	dv := f.get(t, own, "/deployments?tile=apps/crm", 200)
	if dv["caps"] != nil || dv["edges"] != nil || rowOf(dv, "dev")["vault"] == nil || rowOf(dv, "main")["data"] == nil {
		t.Errorf("the deployment view: %s", dplJSON(dv))
	}

	// The zero state: none.
	zs := f.get(t, dplOwner, "/deployments?tile=apps/zs", 200)
	for _, k := range factKeys {
		if rowOf(zs, "main")[k] != nil {
			t.Errorf("the zero state's main carries %s", k)
		}
	}
	if zs["caps"] != nil || zs["edges"] != nil {
		t.Errorf("the zero state carries caps or edges: %s", dplJSON(zs))
	}

	// Readers: none of it, and nothing that names or counts dev.
	readers := map[string]auth.Principal{"a person with read": dplReader, "the primary's frame token": tok("apps/crm", "frame", dplWriter),
		"the tile's instance token": tok("apps/crm", "instance", dplOwner)}
	answers := map[string][]byte{}
	for who, pr := range readers {
		code, _, body := f.do(t, pr, "GET", "/deployments?tile=apps/crm", "")
		var s map[string]any
		_ = json.Unmarshal(body, &s)
		if code != http.StatusOK || regexp.MustCompile(`\bdev\b`).Match(body) || s["caps"] != nil || s["edges"] != nil ||
			bytes.Contains(body, []byte("nightly")) {
			t.Errorf("%s: %d %s", who, code, body)
		}
		for _, k := range factKeys {
			if bytes.Contains(body, []byte(`"`+k+`"`)) {
				t.Errorf("%s: the reader view carries %s: %s", who, k, body)
			}
		}
		answers[who] = body
	}
	rel := filepath.Join(f.ws, "data", "deployments", util.TileKey("apps/crm")+".json")
	if err := os.WriteFile(rel, []byte(dplRecord("apps/crm", 7, "", "main", false, map[string]string{"main": dplTreeA})), 0o600); err != nil {
		t.Fatal(err)
	}
	f.boot(t)
	f.api.reads.factReads = factSources()
	for who, pr := range readers {
		if _, _, body := f.do(t, pr, "GET", "/deployments?tile=apps/crm", ""); !bytes.Equal(body, answers[who]) {
			t.Errorf("%s: removing dev changed the reader's answer:\n%s\n%s", who, answers[who], body)
		}
	}

	// The state's status beyond the primary is the runner's row for it.
	f.dp.Run = rtStatusRunner{}
	if comp, _ := f.dp.Reg.Component("apps/crm"); runnerStatus(f.dp)(comp, "qa") != (depStatus{State: "failed", Gen: 5, Error: "exit 1"}) ||
		runnerStatus(f.dp)(comp, "gone") != (depStatus{State: "idle"}) {
		t.Errorf("a deployment beyond the primary: %+v", runnerStatus(f.dp)(comp, "qa"))
	}

	if testing.Short() {
		t.Skip("boots a workspace")
	}
	// Through the real daemon: apps/zs with main alone, then with dev too.
	answer := func(deps map[string]string) (comps, state, frame []byte, ownerSees bool) {
		ws := zsWorkspace(t)
		p := filepath.Join(ws, "data", "deployments", util.TileKey("apps/zs")+".json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(dplRecord("apps/zs", 2, "main", "main", false, deps)), 0o600); err != nil {
			t.Fatal(err)
		}
		d := zsBoot(t, ws)
		if _, err := d.st.Users.Upsert(users.User{ID: "ana", Role: users.RoleUser,
			Tiles: map[string]string{"apps/zs": users.LevelRead}}, "password1"); err != nil {
			t.Fatal(err)
		}
		ana := "Cookie: xbin_session=" + d.st.Auth.NewSession("ana", "127.0.0.1")
		code, body := d.do(t, "GET", "/api/xbin/components", ana)
		if code != 200 {
			t.Fatalf("/components: %d %s", code, body)
		}
		comps = zsPick(t, body, "path")
		if code, state = d.do(t, "GET", "/api/xbin/deployments?tile=apps/zs", ana); code != 200 {
			t.Fatalf("/deployments as a reader: %d %s", code, state)
		}
		req, _ := http.NewRequest("GET", d.url+"/api/xbin/deployments?tile=apps/zs", nil)
		req.Header.Set(auth.FrameTokenHeader, d.st.Auth.MintFrameToken("apps/zs", "ana", time.Minute))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		frame, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		_, all := d.do(t, "GET", "/api/xbin/deployments?tile=apps/zs", "Bearer "+d.owner)
		return comps, state, frame, regexp.MustCompile(`"name":"dev"`).Match(all)
	}
	c1, s1, f1, saw1 := answer(map[string]string{"main": ""})
	c2, s2, f2, saw2 := answer(map[string]string{"main": "", "dev": dplTreeB})
	if saw1 || !saw2 {
		t.Fatalf("the fixture: the owner sees dev %v without it, %v with it", saw1, saw2)
	}
	if !bytes.Contains(c1, []byte(`"deployments":{"pinned":false,"primary":"main","protected":false}`)) {
		t.Errorf("the reader's /components entry has no primary summary: %s", c1)
	}
	for what, pair := range map[string][2][]byte{"/components": {c1, c2}, "GET /deployments as a person with read": {s1, s2},
		"GET /deployments as the primary's frame token": {f1, f2}} {
		if !bytes.Equal(pair[0], pair[1]) {
			t.Errorf("%s differs once apps/zs has dev:\n%s\n%s", what, pair[0], pair[1])
		}
	}
}

// rtStatusRunner is a runner whose rows name apps/crm's qa.
type rtStatusRunner struct{ dplRunner }

func (rtStatusRunner) StatusDeployments() map[string]map[string]any {
	return map[string]map[string]any{"apps/crm": {"qa": map[string]any{"state": "failed", "gen": 5, "error": "exit 1"}}}
}
