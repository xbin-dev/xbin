package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers PD-55 — the workspace policies (plans/partitions 05 §2): both
// switches off until an admin turns one on; GET for people and admins, 403
// for tile code; PUT admin only, partial, publishing `policies`.
func TestWorkspacePoliciesAPI(t *testing.T) {
	b, st := orgFixture(t)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/calendar", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	if p := b.Policies(); p != (WorkspacePolicies{}) {
		t.Fatalf("defaults = %+v, want every switch off", p)
	}
	bob := principalFor(t, st, "bob")
	// terminal and agent sessions both carry a terminal token (MintTerminalTarget):
	// the tile's element principal with the driving person in UserID, bound to
	// the session's target deployment ("" follows the primary)
	bobTerm := auth.Principal{Component: "apps/email", UserID: "bob", Via: "terminal", Access: bob.Access}
	bobAgent := bobTerm
	bobAgent.Deployment = "dev"
	adminTile := auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "bob"}
	for _, tc := range []struct {
		name     string
		p        auth.Principal
		get, put int
	}{
		{"the root token", auth.Principal{Owner: true, Via: "bearer"}, 200, 200},
		{"a workspace admin", principalFor(t, st, "root2"), 200, 200},
		{"the admin tile (xbin:admin)", adminTile, 200, 200},
		{"a person", bob, 200, 403},
		{"an org admin", principalFor(t, st, "carol"), 200, 403},
		{"a person's terminal", bobTerm, 200, 403},
		{"a person's agent session on a deployment", bobAgent, 200, 403},
		{"a tile frame", auth.Principal{Component: "apps/email", Via: "frame", UserID: "bob"}, 403, 403},
		{"a tile instance", auth.Principal{Component: "apps/email", Via: "instance"}, 403, 403},
		{"a cron delivery", auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer"}, 403, 403},
		{"nobody", auth.Principal{}, 403, 403},
	} {
		if w := call(t, b.apiPoliciesGet, tc.p, "GET", "/workspace-policies", "", nil); w.Code != tc.get {
			t.Errorf("%s: GET = %d, want %d: %s", tc.name, w.Code, tc.get, w.Body)
		}
		if w := call(t, b.apiPoliciesPut, tc.p, "PUT", "/workspace-policies", `{"credentialResetConfirm":false}`, nil); w.Code != tc.put {
			t.Errorf("%s: PUT = %d, want %d: %s", tc.name, w.Code, tc.put, w.Body)
		}
	}

	evs, cancel := b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "policies" })
	defer cancel()
	owner := auth.Principal{Owner: true, Via: "bearer"}
	logs := captureLog(t)
	w := call(t, b.apiPoliciesPut, owner, "PUT", "/workspace-policies", `{"partitionConsent":true}`, nil)
	// the audit line says which switch changed, and to what
	if l := logs.String(); !strings.Contains(l, "msg=audit who=owner") || !strings.Contains(l, "partitionConsent=false→true") ||
		!strings.Contains(l, "credentialResetConfirm=false→false") {
		t.Errorf("the PUT's audit line: %s", l)
	}
	var view map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 ||
		view["schema"] != float64(1) || view["partitionConsent"] != true || view["credentialResetConfirm"] != false {
		t.Fatalf("PUT partitionConsent = %d %s", w.Code, w.Body)
	}
	select {
	case <-evs:
	default:
		t.Fatal("a PUT published no policies event")
	}
	// partial: an absent key is left alone
	call(t, b.apiPoliciesPut, owner, "PUT", "/workspace-policies", `{"credentialResetConfirm":true}`, nil)
	if p := b.Policies(); !p.PartitionConsent || !p.CredentialResetConfirm {
		t.Fatalf("after two partial PUTs: %+v", p)
	}
	w = call(t, b.apiPoliciesGet, bob, "GET", "/workspace-policies", "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view["partitionConsent"] != true || view["credentialResetConfirm"] != true {
		t.Fatalf("GET = %s", w.Body)
	}
	for _, body := range []string{`{}`, `{"partitionConsent":"yes"}`, `{"partitionConsent":true,"other":1}`, `{"schema":1}`, `nope`} {
		if w := call(t, b.apiPoliciesPut, owner, "PUT", "/workspace-policies", body, nil); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, w.Code)
		}
	}
	if p := b.Policies(); !p.PartitionConsent || !p.CredentialResetConfirm {
		t.Fatalf("a refused PUT changed the policies: %+v", p)
	}
}

// covers PD-55 — the policies live in their own file, so a users-store
// rewrite (this xbind's, or an older one that keeps only the keys it knows)
// never touches them; a fresh xbind on the same workspace reads them back.
func TestWorkspacePoliciesSurviveUsersRewrite(t *testing.T) {
	b := testBroker(t)
	dataDir := filepath.Join(b.Reg.Root, "data")
	st, err := users.Open(dataDir) // the users store beside the policies, as in production
	if err != nil {
		t.Fatal(err)
	}
	b.Users = st
	if _, err := st.Upsert(users.User{ID: "ann", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	on := true
	if _, _, err := b.setPolicies(policiesPatch{PartitionConsent: &on, CredentialResetConfirm: &on}); err != nil {
		t.Fatal(err)
	}
	// this xbind's own rewrites
	if err := st.SetTileCreation("org-only"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNativeRuntimeDisabled(true); err != nil {
		t.Fatal(err)
	}
	// an older xbind's: it reads users.json and writes back only what it knows
	usersPath := filepath.Join(dataDir, "users.json")
	raw, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	var older struct {
		Users json.RawMessage `json:"users"`
	}
	if err := json.Unmarshal(raw, &older); err != nil {
		t.Fatal(err)
	}
	rewritten, _ := json.Marshal(older)
	if err := os.WriteFile(usersPath, rewritten, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Open(dataDir); err != nil {
		t.Fatal(err)
	}

	fresh := &Broker{Reg: b.Reg} // a restarted xbind's cold cache (New would wait on b's kv lock)
	if p := fresh.Policies(); !p.PartitionConsent || !p.CredentialResetConfirm {
		t.Fatalf("after a users-store rewrite: %+v, want both on", p)
	}
	d := readPoliciesDoc(filepath.Join(dataDir, "workspace-policies.json"))
	if d.problem() != nil || string(d.raw["schema"]) != "1" {
		t.Fatalf("the file: %v (%v)", d.raw, d.problem())
	}
}

// covers PD-55 — a newer xbind's file keeps its keys and schema number
// through this one's rewrite; a hand edit or restore is picked up without a
// restart; each switch is read by its exact key.
func TestWorkspacePoliciesFile(t *testing.T) {
	b := testBroker(t)
	path := b.policiesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	newer := `{"schema":2,"partitionConsent":true,"futureSwitch":{"x":1}}`
	if err := os.WriteFile(path, []byte(newer), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := b.Policies(); !p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("a newer file reads %+v", p)
	}
	on := true
	if _, _, err := b.setPolicies(policiesPatch{CredentialResetConfirm: &on}); err != nil {
		t.Fatal(err)
	}
	d := readPoliciesDoc(path)
	var future map[string]int
	_ = json.Unmarshal(d.raw["futureSwitch"], &future)
	if d.problem() != nil || string(d.raw["schema"]) != "2" || future["x"] != 1 || !d.vals.PartitionConsent || !d.vals.CredentialResetConfirm {
		t.Fatalf("after a rewrite: %v %+v (%v)", d.raw, d.vals, d.problem())
	}

	// a restore puts back an older copy: read on the next check
	if err := os.WriteFile(path, []byte(`{"schema":1,"partitionConsent":false,"credentialResetConfirm":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := b.Policies(); p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("after the file changed on disk: %+v", p)
	}
}

// covers PD-55 — both switches are protections, so a file xbind can't read
// never turns one off: a switch it can't read keeps its last value, or is on
// when none was read (a cold start); the other switch reads as the file says.
// GET and PUT answer 500 (the host path only in the log; people get a generic
// line), PUT never overwrites the file, and admins see it on /alerts.
func TestWorkspacePoliciesFailClosed(t *testing.T) {
	b, st := orgFixture(t)
	path := b.policiesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	owner := auth.Principal{Owner: true, Via: "bearer"}
	bob := principalFor(t, st, "bob")
	alerts := func(p auth.Principal) (kinds []string) {
		var out struct{ Alerts []Alert }
		_ = json.Unmarshal(call(t, b.apiAlerts, p, "GET", "/alerts", "", nil).Body.Bytes(), &out)
		for _, a := range out.Alerts {
			kinds = append(kinds, a.Kind)
		}
		return kinds
	}

	write(`{"schema":1,"partitionConsent":true,"credentialResetConfirm":false}`)
	if p := b.Policies(); !p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("the good file reads %+v", p)
	}
	for _, tc := range []struct {
		name, file string
		want       WorkspacePolicies
	}{
		// the admin's typo while turning the other switch on: the one on stays on
		{"a broken file", `{"partitionConsent": true, "credentialResetConfirm": tru`, WorkspacePolicies{PartitionConsent: true}},
		{"not an object", `[]`, WorkspacePolicies{PartitionConsent: true}},
		{"a value of the wrong type", `{"partitionConsent":"no","credentialResetConfirm":true}`, WorkspacePolicies{PartitionConsent: true, CredentialResetConfirm: true}},
		{"a newer schema's changed type", `{"schema":3,"partitionConsent":{"mode":"off"},"credentialResetConfirm":false}`, WorkspacePolicies{PartitionConsent: true}},
		// encoding/json would read it as the switch; a PUT would then write the
		// right key beside it and answer false while the file said true
		{"a mis-cased key", `{"partitionconsent":false,"credentialResetConfirm":false}`, WorkspacePolicies{PartitionConsent: true}},
	} {
		write(tc.file)
		if p := b.Policies(); p != tc.want {
			t.Errorf("%s: %+v, want %+v (fail closed)", tc.name, p, tc.want)
		}
		w := call(t, b.apiPoliciesGet, owner, "GET", "/workspace-policies", "", nil)
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), policiesRel) || strings.Contains(w.Body.String(), b.Reg.Root) {
			t.Errorf("%s: an admin's GET = %d %s, want 500 naming %s, not the host path", tc.name, w.Code, w.Body, policiesRel)
		}
		w = call(t, b.apiPoliciesGet, bob, "GET", "/workspace-policies", "", nil)
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "an admin must fix") || strings.Contains(w.Body.String(), b.Reg.Root) {
			t.Errorf("%s: a person's GET = %d %s, want the generic 500", tc.name, w.Code, w.Body)
		}
		if w := call(t, b.apiPoliciesPut, owner, "PUT", "/workspace-policies", `{"credentialResetConfirm":false}`, nil); w.Code != http.StatusInternalServerError {
			t.Errorf("%s: PUT = %d, want 500", tc.name, w.Code)
		}
		if got, _ := os.ReadFile(path); string(got) != tc.file {
			t.Errorf("%s: a PUT overwrote the file: %s", tc.name, got)
		}
		if k := alerts(owner); !slices.Contains(k, "policies") {
			t.Errorf("%s: an admin's /alerts = %v, want a policies alert", tc.name, k)
		}
		if k := alerts(bob); slices.Contains(k, "policies") {
			t.Errorf("%s: a person's /alerts = %v, the policies alert is admins'", tc.name, k)
		}
	}

	// fixed by hand: read as it says, no alert
	write(`{"schema":1,"partitionConsent":false,"credentialResetConfirm":false}`)
	if p := b.Policies(); p != (WorkspacePolicies{}) || slices.Contains(alerts(owner), "policies") {
		t.Fatalf("after the fix: %+v %v", p, alerts(owner))
	}
	// the last good values are now off: a broken file keeps them off
	write(`{"partitionConsent": tru`)
	if p := b.Policies(); p != (WorkspacePolicies{}) {
		t.Fatalf("a broken file after both were read off: %+v", p)
	}

	// a cold start (a fresh xbind) on a broken file: every switch on
	cold := &Broker{Reg: b.Reg}
	if p := cold.Policies(); !p.PartitionConsent || !p.CredentialResetConfirm {
		t.Fatalf("a cold start on a broken file: %+v, want both on", p)
	}
	// one bad value on a cold start: that switch on, the other as the file says
	write(`{"partitionConsent":1,"credentialResetConfirm":false}`)
	if p := (&Broker{Reg: b.Reg}).Policies(); !p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("a cold start with one bad value: %+v", p)
	}
}
