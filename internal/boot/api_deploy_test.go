package boot

// api_deploy_test.go — /backends, /runtime and /tile-status on tiles with
// deployments (11-contract §8; 12-compat C3; 06-security T7 item 4).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// rtRunner is the runner's rows for the runtime reads over dplFix's tiles:
// apps/crm's primary (main, pinned to A) and its dev; apps/node's primary
// (zero state); apps/other runs only qa, beside a primary that isn't
// running.
type rtRunner struct{}

func (rtRunner) Status() map[string]any {
	return map[string]any{
		"apps/crm":  map[string]any{"state": "healthy", "gen": 3},
		"apps/node": map[string]any{"state": "healthy", "gen": 2},
	}
}

func (rtRunner) StatusDeployments() map[string]map[string]any {
	return map[string]map[string]any{
		"apps/crm":   {"dev": map[string]any{"state": "failed", "gen": 5, "error": "exit 1"}},
		"apps/other": {"qa": map[string]any{"state": "healthy", "gen": 1}},
	}
}

func (rtRunner) Inspect() []runner.Backend {
	return []runner.Backend{
		{Path: "apps/crm", Runtime: "node", State: "healthy", Gen: 3, Checkpoint: dplTreeA},
		{Path: "apps/node", Runtime: "node", State: "healthy", Gen: 2},
	}
}

func (rtRunner) InspectDeployments() []runner.Backend {
	return []runner.Backend{
		{Path: "apps/crm", Runtime: "node", State: "failed", Gen: 5, Error: "exit 1", Deployment: "dev"},
		{Path: "apps/other", Runtime: "node", State: "healthy", Gen: 1, Deployment: "qa"},
	}
}

// rtFix is dplFix's plane (plus apps/other, main pinned and qa on live
// reload) under the runtime reads, with rtRunner's rows. The broker's
// admin question passes admins and apps/zs's credentials, as an xbin-admin
// grant on apps/zs would.
func rtFix(t *testing.T) (*dplFix, runtimeReads) {
	t.Helper()
	f := newDplFix(t)
	rel := filepath.Join(f.ws, "data", "deployments", util.TileKey("apps/other")+".json")
	if err := os.WriteFile(rel, []byte(dplRecord("apps/other", 2, "qa", "qa", false,
		map[string]string{"main": dplTreeB, "qa": ""})), 0o600); err != nil {
		t.Fatal(err)
	}
	f.boot(t)
	rd := runtimeReads{run: rtRunner{}, dp: f.dp,
		isAdmin: func(p auth.Principal) bool { return p.IsAdmin() || p.Component == "apps/zs" },
		disk:    func(string) (int64, int64, bool) { return 0, 50 << 30, false },
		alerts:  func(string) any { return nil },
		net:     func(string) any { return map[string]any{} }}
	return f, rd
}

// rtGet serves one GET through h as pr: the status and the JSON body.
func rtGet(t *testing.T, h http.HandlerFunc, pr auth.Principal, target string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	req = req.WithContext(auth.WithPrincipal(req.Context(), pr))
	rec := httptest.NewRecorder()
	h(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m == nil {
		m = map[string]any{"body": rec.Body.String()}
	}
	return rec.Code, m
}

// covers T7 P7 P12 P20 PO-11 PO-14 — the runtime listings name
// deployments beyond the primary only to those who may learn of them
// (admins and people with write in their own session, the tile's terminal
// and agent sessions while their user writes it, and a deployment's own
// principals), never to a reader's frame or instance token nor to another
// tile's credential, even one its grants make an admin. /backends keeps one
// key per tile, the primary's row: a tile with a record adds deployment
// (the primary's name) and, for its write audience, deployments with the
// others' rows; a tile running only another deployment gets the primary's
// idle row to hold it. /runtime keeps one backends[] row per tile, which
// gains deployment on a tile with a record; the others' rows go to
// deploymentBackends[]. /tile-status reports the deployment named, else the
// caller's bound one (a tile's own credential) or the primary: a frame or
// instance token reads only its own, a terminal any while its user writes
// the tile, another tile's credential only the primary; it echoes the
// deployment on a tile with a record or when one was named, and the write
// audience gets the deployments summary. A zero-state tile answers as
// today.
func TestNonPrimaryRowsNeedWrite(t *testing.T) {
	f, rd := rtFix(t)
	elevated := tok("apps/zs", "frame", dplReader) // another tile's credential, admin by its grants

	// /backends.
	if code, _ := rtGet(t, rd.backends, dplWriter, "/backends"); code != http.StatusForbidden {
		t.Errorf("/backends for a writer: %d", code)
	}
	_, all := rtGet(t, rd.backends, dplAdmin, "/backends")
	for tile, want := range map[string]string{
		"apps/crm":   `{"deployment":"main","deployments":{"dev":{"error":"exit 1","gen":5,"state":"failed"}},"gen":3,"state":"healthy"}`,
		"apps/node":  `{"gen":2,"state":"healthy"}`,
		"apps/other": `{"deployment":"main","deployments":{"qa":{"gen":1,"state":"healthy"}},"gen":0,"state":"idle"}`,
	} {
		if got := dplJSON(all[tile]); got != want {
			t.Errorf("/backends %s for an admin: %s, want %s", tile, got, want)
		}
	}
	if len(all) != 3 {
		t.Errorf("/backends for an admin has %d keys, want one per tile running: %v", len(all), all)
	}
	_, el := rtGet(t, rd.backends, elevated, "/backends")
	if got := dplJSON(el); got != `{"apps/crm":{"deployment":"main","gen":3,"state":"healthy"},"apps/node":{"gen":2,"state":"healthy"}}` {
		t.Errorf("/backends for another tile's admin credential: %s", got)
	}

	// /runtime's rows.
	for who, c := range map[string]struct {
		p    auth.Principal
		deps []string
	}{"an admin": {dplAdmin, []string{"apps/crm+dev", "apps/other+qa"}}, "another tile's admin credential": {elevated, nil}} {
		bs := rtRunner{}.Inspect()
		var got []string
		for _, b := range rd.deploymentBackends(c.p, bs) {
			got = append(got, b.Path+"+"+b.Deployment)
		}
		if !slices.Equal(got, c.deps) {
			t.Errorf("/runtime for %s: deploymentBackends %v, want %v", who, got, c.deps)
		}
		if bs[0].Path != "apps/crm" || bs[0].Deployment != "main" || bs[0].Checkpoint != dplTreeA || bs[1].Deployment != "" || len(bs) != 2 {
			t.Errorf("/runtime for %s: backends %+v", who, bs)
		}
	}

	// /tile-status.
	devFrame, devReaderFrame := tok("apps/crm", "frame", dplWriter), tok("apps/crm", "frame", dplReader)
	devFrame.Deployment, devReaderFrame.Deployment = "dev", "dev"
	devInstance := tok("apps/crm", "instance", dplOwner)
	devInstance.Deployment = "dev"
	summary := `{"items":[{"checkpoint":"c:3f2a1c9","gen":3,"name":"main","state":"healthy"},{"gen":5,"name":"dev","state":"failed"}],"liveReload":"dev","primary":"main"}`
	for _, c := range []struct {
		who     string
		p       auth.Principal
		query   string
		status  int
		dep     string // the deployment reported ("-": no deployment key)
		gen     float64
		summary bool
		msg     string
	}{
		{"an admin", dplAdmin, "component=apps/crm", 200, "main", 3, true, ""},
		{"an admin", dplAdmin, "component=apps/crm&deployment=dev", 200, "dev", 5, true, ""},
		{"an admin", dplAdmin, "component=apps/crm&deployment=main", 200, "main", 3, true, ""},
		{"an admin", dplAdmin, "component=apps/crm&deployment=nope", 404, "", 0, false, `apps/crm has no deployment "nope"`},
		{"an admin", dplAdmin, "component=apps/crm&deployment=Dev", 400, "", 0, false, "deployment names are lowercase"},
		{"the tile's terminal", tok("apps/crm", "terminal", dplTerm), "", 200, "main", 3, true, ""},
		{"the tile's terminal", tok("apps/crm", "terminal", dplTerm), "deployment=dev", 200, "dev", 5, true, ""},
		{"a terminal whose user reads", tok("apps/crm", "terminal", dplReader), "", 200, "main", 3, false, ""},
		{"a terminal whose user reads", tok("apps/crm", "terminal", dplReader), "deployment=dev", 403, "", 0, false, "deployments of apps/crm need write access"},
		{"the primary's frame", tok("apps/crm", "frame", dplReader), "", 200, "main", 3, false, ""},
		{"the primary's frame", tok("apps/crm", "frame", dplReader), "deployment=dev", 403, "", 0, false, "a tile's own credentials act only on their own deployment (main)"},
		{"the primary's instance", tok("apps/crm", "instance", dplOwner), "deployment=dev", 403, "", 0, false, "a tile's own credentials act only on their own deployment (main)"},
		{"dev's frame", devFrame, "", 200, "dev", 5, false, ""},
		{"dev's frame", devFrame, "deployment=main", 403, "", 0, false, "a tile's own credentials act only on their own deployment (dev)"},
		{"dev's instance", devInstance, "", 200, "dev", 5, false, ""},
		{"dev's frame of a reader", devReaderFrame, "", 403, "", 0, false, "deployments of apps/crm need write access"},
		{"another tile's admin credential", elevated, "component=apps/crm", 200, "main", 3, false, ""},
		{"another tile's admin credential", elevated, "component=apps/crm&deployment=dev", 403, "", 0, false, "deployments of apps/crm need write access"},
		{"another tile's admin credential", elevated, "component=apps/crm&deployment=nope", 403, "", 0, false, "deployments of apps/crm need write access"},
		{"a zero-state tile, an admin", dplAdmin, "component=apps/node", 200, "-", 2, false, ""},
		{"a zero-state tile, an admin", dplAdmin, "component=apps/node&deployment=main", 200, "main", 2, false, ""},
		{"a zero-state tile, an admin", dplAdmin, "component=apps/node&deployment=dev", 404, "", 0, false, `apps/node has no deployment "dev"`},
		// P17: the deployment rides deployment=, never component=tile+name
		{"a qualified component, an admin", dplAdmin, "component=apps/crm%2Bdev", 400, "", 0, false, "a deployment is named with deployment=, not tile+name"},
		{"an unescaped qualified component, an admin", dplAdmin, "component=apps/crm+dev", 400, "", 0, false, "a deployment is named with deployment=, not tile+name"},
		{"a following terminal of a protected primary", tok("apps/prot", "terminal", dplTerm), "", 403, "", 0, false,
			"the primary of apps/prot is protected: terminal and agent sessions can't target it"},
	} {
		label := c.who + " ?" + c.query
		code, s := rtGet(t, rd.tileStatus, c.p, "/tile-status?"+c.query)
		if c.status != 200 {
			if code != c.status || !strings.HasPrefix(dplString(s["error"]), c.msg) || s["docs"] == nil {
				t.Errorf("%s: %d %v, want %d %q", label, code, s, c.status, c.msg)
			}
			continue
		}
		dep, hasDep := s["deployment"]
		be, _ := s["backend"].(map[string]any)
		switch {
		case code != 200:
			t.Errorf("%s: %d %v", label, code, s)
		case c.dep == "-" && hasDep, c.dep != "-" && dep != c.dep:
			t.Errorf("%s: deployment %v, want %s", label, dep, c.dep)
		case be == nil || be["gen"] != c.gen:
			t.Errorf("%s: backend %v, want gen %v", label, be, c.gen)
		case c.summary && dplJSON(s["deployments"]) != summary:
			t.Errorf("%s: deployments %s, want %s", label, dplJSON(s["deployments"]), summary)
		case !c.summary && s["deployments"] != nil:
			t.Errorf("%s: carries the deployments summary %s", label, dplJSON(s["deployments"]))
		}
	}
	// A tile running nothing reports no backend, and the summary's items
	// say what the runner doesn't run.
	_, pin := rtGet(t, rd.tileStatus, dplAdmin, "/tile-status?component=apps/pin")
	if pin["backend"] != nil || dplJSON(pin["deployments"]) != `{"items":[{"checkpoint":"c:3f2a1c9","gen":0,"name":"main","state":"static"}],"liveReload":"","primary":"main"}` {
		t.Errorf("apps/pin: backend %v deployments %s", pin["backend"], dplJSON(pin["deployments"]))
	}
	// Without the plane every tile is in the zero state.
	rd.dp = nil
	if _, s := rtGet(t, rd.tileStatus, dplAdmin, "/tile-status?component=apps/crm"); s["deployment"] != nil || s["deployments"] != nil {
		t.Errorf("without the plane: %v", s)
	}
	_ = f
}

func dplString(v any) string { s, _ := v.(string); return s }
