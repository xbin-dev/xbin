package boot

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/vm"
)

// sbxAnswer is GET /sandboxes as these tests read it.
type sbxAnswer struct {
	Sandboxes  []map[string]any `json:"sandboxes"`
	Failures   []map[string]any `json:"failures"`
	Deployment *string          `json:"deployment"`
}

// covers D127h T7 T18 PO-11 — TestSandboxesDeploymentRows (15-test-plan):
// GET /api/xbin/sandboxes?tile= lists every deployment's generations under
// the tile. main's row keeps its ID and carries no deployment, with the
// "tile" stats scope; each non-primary row has the ID
// backend+<name>:<CompKey>:g<gen>, names its deployment, and has stats of
// its own leaf, scope "deployment", one sample for its generations; failures
// name the deployment. ?deployment= narrows rows and failures and is echoed;
// a malformed name is the catalogue's 400; a qualified ?tile= is looked up
// literally. health.vm.usedBy stays keyed by tile. A tile running only main
// is untouched, and non-admins (a user, the tile's own terminal) get
// nothing, as before.
func TestSandboxesDeploymentRows(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	d := zsBoot(t, zsWorkspace(t))
	owner := "Bearer " + d.owner
	const (
		tile   = "apps/zsnode"
		key    = "apps~zsnode-a398c13a" // CompKey, by sha256sum
		other  = "apps/zs"
		oKey   = "apps~zs-0d38f4f5"
		devLog = "the workspace runs 12 non-primary backends, the most allowed at once"
	)
	// the registry as the runner leaves it for a tile running main beside
	// a dev blue/green pair (sbxAdd's rows, internal/runner/sbx_deploy_test.go)
	pid := os.Getpid()
	for _, e := range []sbx.Entry{
		{ID: "backend:" + key + ":g2", Kind: sbx.Backend, Tile: tile, Mode: sbx.Namespace, PID: pid, Gen: 2},
		{ID: "backend+dev:" + key + ":g7", Kind: sbx.Backend, Tile: tile, Deployment: "dev", Mode: sbx.Namespace, PID: pid, Gen: 7},
		{ID: "backend+dev:" + key + ":g8", Kind: sbx.Backend, Tile: tile, Deployment: "dev", Mode: sbx.Namespace, PID: pid, Gen: 8},
		{ID: "backend:" + oKey + ":g1", Kind: sbx.Backend, Tile: other, Mode: sbx.Namespace, PID: pid, Gen: 1},
	} {
		t.Cleanup(d.st.Sbx.Add(e))
	}
	d.st.Sbx.Fail(sbx.Failure{Kind: sbx.Backend, Tile: tile, Deployment: "dev", Mode: sbx.Namespace, Stage: sbx.Refused, Error: devLog})
	d.st.Sbx.Fail(sbx.Failure{Kind: sbx.Backend, Tile: tile, Mode: sbx.Namespace, Stage: sbx.Refused, Error: devLog})

	get := func(q, cred string) (int, sbxAnswer, []byte) {
		t.Helper()
		code, body := d.do(t, "GET", "/api/xbin/sandboxes"+q, cred)
		var a sbxAnswer
		if code == http.StatusOK {
			if err := json.Unmarshal(body, &a); err != nil {
				t.Fatalf("GET %s: %v %s", q, err, body)
			}
		}
		return code, a, body
	}
	ids := func(rows []map[string]any) string {
		var out []string
		for _, r := range rows {
			out = append(out, r["id"].(string))
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	scope := func(r map[string]any) any {
		s, _ := r["stats"].(map[string]any)
		return s["scope"]
	}

	code, a, body := get("?tile="+tile, owner)
	if code != http.StatusOK {
		t.Fatalf("admin: %d %s", code, body)
	}
	if got, want := ids(a.Sandboxes), "backend+dev:"+key+":g7 backend+dev:"+key+":g8 backend:"+key+":g2"; got != want {
		t.Fatalf("rows %s, want %s", got, want)
	}
	var devStats []any
	for _, r := range a.Sandboxes {
		dep, has := r["deployment"]
		switch id := r["id"].(string); {
		case strings.HasPrefix(id, "backend:"):
			if has || r["tile"] != tile || scope(r) != "tile" {
				t.Errorf("main's row: %v", r)
			}
		default:
			if dep != "dev" || r["tile"] != tile || scope(r) != "deployment" {
				t.Errorf("dev's row: %v", r)
			}
			devStats = append(devStats, r["stats"])
		}
	}
	if len(devStats) != 2 {
		t.Fatalf("dev's rows: %v", devStats)
	}
	if s0, s1 := devStats[0].(map[string]any), devStats[1].(map[string]any); s0["mem"] != s1["mem"] || s0["pids"] != s1["pids"] {
		t.Errorf("dev's generations share one sample: %v %v", s0, s1)
	}
	if len(a.Failures) != 2 || a.Failures[0]["deployment"] != nil || a.Failures[1]["deployment"] != "dev" {
		t.Errorf("failures: %v", a.Failures)
	}
	if a.Deployment != nil {
		t.Errorf("an answer asked for no deployment names one: %q", *a.Deployment)
	}

	// ?deployment= narrows and is echoed
	_, a, _ = get("?tile="+tile+"&deployment=dev", owner)
	if ids(a.Sandboxes) != "backend+dev:"+key+":g7 backend+dev:"+key+":g8" || len(a.Failures) != 1 || a.Deployment == nil || *a.Deployment != "dev" {
		t.Errorf("?deployment=dev: %s, failures %v, echo %v", ids(a.Sandboxes), a.Failures, a.Deployment)
	}
	_, a, _ = get("?tile="+tile+"&deployment=main", owner)
	if ids(a.Sandboxes) != "backend:"+key+":g2" || len(a.Failures) != 1 || a.Deployment == nil || *a.Deployment != "main" {
		t.Errorf("?deployment=main: %s, failures %v, echo %v", ids(a.Sandboxes), a.Failures, a.Deployment)
	}
	if s := a.Sandboxes[0]; scope(s) != "tile" {
		t.Errorf("main's row, narrowed, keeps its own sample: %v", s)
	}
	code, _, body = get("?tile="+tile+"&deployment=Dev", owner)
	var e map[string]string
	if err := json.Unmarshal(body, &e); code != http.StatusBadRequest || err != nil || len(e) != 2 || e["docs"] != "/docs/protocol.md" ||
		e["error"] != `deployment names are lowercase letters, digits and "-", start with a letter, at most 24 characters` {
		t.Errorf("a malformed name: %d %s", code, body)
	}
	// a qualified tile string is a literal path here (11-contract §0.3)
	if _, a, _ = get("?tile="+tile+"%2Bdev", owner); len(a.Sandboxes) != 0 {
		t.Errorf("?tile=%s+dev: %v", tile, a.Sandboxes)
	}

	// a tile running only main is untouched: its tile's series, no deployment
	_, a, _ = get("?tile="+other, owner)
	if len(a.Sandboxes) != 1 {
		t.Fatalf("%s: %v", other, a.Sandboxes)
	}
	if r := a.Sandboxes[0]; r["id"] != "backend:"+oKey+":g1" || r["deployment"] != nil || (r["stats"] != nil && scope(r) != "tile") {
		t.Errorf("%s's row: %v", other, r)
	}

	// VM usage stays the tile's: main's guest and dev's are booked to it
	// (the harness daemon has no isolation, hence no VM manager: one here,
	// read through the view the handler answers with)
	d.st.VM = &vm.Manager{Root: d.ws}
	for range 2 {
		release, err := d.st.VM.Reserve(tile, 512)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	h := d.st.sandboxesView(sandboxScope{all: true, tile: tile})["health"].(map[string]any)
	if u := h["vm"].(vmView).UsedBy; len(u) != 1 || u[tile].VMs != 2 || u[tile].MemMiB != 1024 {
		t.Errorf("usedBy stays per tile: %+v", u)
	}

	// non-admins: nothing, as before
	if _, err := d.st.Users.Upsert(users.User{ID: "ana", Role: users.RoleUser, Tiles: map[string]string{tile: users.LevelWrite}}, "password1"); err != nil {
		t.Fatal(err)
	}
	for who, cred := range map[string]string{
		"a user":              "Cookie: xbin_session=" + d.st.Auth.NewSession("ana", "127.0.0.1"),
		"the tile's terminal": "Bearer " + d.st.Auth.MintTerminal(tile, ""),
	} {
		if code, _, body := get("?tile="+tile+"&deployment=dev", cred); code != http.StatusForbidden || strings.Contains(string(body), "backend") {
			t.Errorf("%s: %d %s", who, code, body)
		}
	}
}

func TestSessionWhat(t *testing.T) {
	for _, c := range []struct {
		e    sbx.Entry
		want string
	}{
		{sbx.Entry{Kind: sbx.Terminal, User: "alice", Tile: "apps/x", Mode: sbx.VM}, "a VM terminal of alice on apps/x"},
		{sbx.Entry{Kind: sbx.Agent, Tile: "apps/x"}, "an agent session on apps/x"},
		{sbx.Entry{ID: "tile:apps~x-1a2b3c4d:build", Name: "build", Kind: sbx.Tile, Tile: "apps/x", Mode: sbx.Namespace}, `the tile sandbox "build" of apps/x`},
		{sbx.Entry{ID: "tile+pr-7:apps~x-1a2b3c4d:db", Name: "db", Kind: sbx.Tile, Tile: "apps/x", Mode: sbx.VM}, `the VM tile sandbox "db" of apps/x`},
	} {
		if got := sessionWhat(c.e); got != c.want {
			t.Errorf("sessionWhat(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}
