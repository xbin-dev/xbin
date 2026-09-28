package boot

import (
	"encoding/json"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers NP-14-4 PO-14 P5 — registerDeploymentsAPI mounts every route of the
// tile-deployments contract (docs/protocol.md, "Tile deployments"), once; and
// through the real daemon (auth on), every operation the OpenAPI document
// marks reserved answers 501 in the {"error","docs"} shape of every API error
// (an older xbind answers a plain-text 404 or 405 instead) and changes
// nothing: the zero-state workspace gains no deployment state. A route this
// xbind builds — the state, each operation the plane registers, and each
// read whose source xbind wires (the log, the diff, the checkpoint remote)
// — is left to its own tests; the integrator drops its row's reserved marks in
// the merge that builds it (14-implementation §4.3), and until then the
// test names it.
func TestDeploymentRoutesReserved(t *testing.T) {
	want := []string{
		"GET /deployments", "GET /deployments/log", "GET /deployments/diff",
		"POST /deployments/live-reload/pause", "POST /deployments/live-reload/resume",
		"POST /deployments/live-reload/now", "POST /deployments/live-reload/attach",
		"POST /deployments/add", "POST /deployments/remove", "POST /deployments/deploy",
		"POST /deployments/promote", "POST /deployments/rollback",
		"POST /deployments/primary", "POST /deployments/protect", "POST /deployments/edge",
		"POST /deployments/deliveries", "POST /deployments/always-on", "POST /deployments/limits",
		"POST /deployments/seed", "POST /deployments/reset", "POST /deployments/vault-copy",
		"POST /deployments/backup", "GET /deployments/backups", "POST /deployments/restore",
		"POST /deployments/backup-schedule", "POST /deployments/run-now", "POST /deployments/purge",
		"GET /checkpoints/{rest...}",
	}
	srv := &server.Server{}
	registerDeploymentsAPI(srv, &deployments.Plane{})
	got := srv.APIRoutes()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("registerDeploymentsAPI mounts\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	d := zsBoot(t, ws)
	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	reads := planeReads(&deployments.Plane{})
	wired := map[string]bool{"/deployments/log": reads.log != nil && reads.entry != nil,
		"/deployments/diff": reads.diff != nil}
	n := 0
	for p, item := range server.OpenAPI()["paths"].(map[string]any) {
		for m, op := range item.(map[string]any) {
			if op.(map[string]any)["x-xbin-reserved"] != true {
				continue
			}
			method := strings.ToUpper(m)
			o, isOp := strings.CutPrefix(p, "/deployments/")
			read := method == "GET" && (p == "/deployments" || wired[p] || strings.HasPrefix(p, "/checkpoints/") && reads.fetch != nil)
			if read || method == "POST" && isOp && deployments.Registered(deployments.Op(o)) {
				t.Logf("%s %s is built here: its openapi.go row keeps reserved marks for the integrator to drop", method, p)
				continue
			}
			n++
			path := "/api/xbin" + wildcard.ReplaceAllString(p, "x")
			code, body := d.do(t, method, path+"?tile=apps/zs", "Bearer "+d.owner)
			var e map[string]string
			if err := json.Unmarshal(body, &e); code != http.StatusNotImplemented || err != nil {
				t.Errorf("%s %s = %d %s, want 501 JSON", method, path, code, body)
				continue
			}
			if !strings.HasPrefix(e["error"], "reserved for tile deployments") || e["docs"] != "/docs/protocol.md" || len(e) != 2 {
				t.Errorf("%s %s answered %s, want {error: reserved…, docs: /docs/protocol.md}", method, path, body)
			}
		}
	}
	t.Logf("%d reserved operations answer 501", n)

	keys := map[string]bool{}
	for _, c := range d.st.Reg.Components() {
		keys[util.CompKey(c.Path)] = true
	}
	if bad := zsDeploymentState(t, ws, keys); len(bad) > 0 {
		t.Fatalf("a reserved route created deployment state:\n  %s", strings.Join(bad, "\n  "))
	}
}
