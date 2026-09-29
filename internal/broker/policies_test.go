package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
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
	bobTerm, bobAgent := bob, bob
	bobTerm.Component, bobTerm.Via = "apps/email", "terminal"
	bobAgent.Component, bobAgent.Via = "apps/email", "agent"
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
		{"a person's agent session", bobAgent, 200, 403},
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
	w := call(t, b.apiPoliciesPut, owner, "PUT", "/workspace-policies", `{"partitionConsent":true}`, nil)
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
	if _, err := b.setPolicies(policiesPatch{PartitionConsent: &on, CredentialResetConfirm: &on}); err != nil {
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
	doc, _, err := readPoliciesDoc(filepath.Join(dataDir, "workspace-policies.json"))
	if err != nil || string(doc["schema"]) != "1" {
		t.Fatalf("the file: %v (%v)", doc, err)
	}
}

// covers PD-55 — a newer xbind's file keeps its keys and schema number
// through this one's rewrite; a hand edit or restore is picked up without a
// restart; an unreadable file reads as the defaults, and is never
// overwritten.
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
	if _, err := b.setPolicies(policiesPatch{CredentialResetConfirm: &on}); err != nil {
		t.Fatal(err)
	}
	doc, p, err := readPoliciesDoc(path)
	var future map[string]int
	_ = json.Unmarshal(doc["futureSwitch"], &future)
	if err != nil || string(doc["schema"]) != "2" || future["x"] != 1 || !p.PartitionConsent || !p.CredentialResetConfirm {
		t.Fatalf("after a rewrite: %v %+v (%v)", doc, p, err)
	}

	// a restore puts back an older copy: read on the next check
	if err := os.WriteFile(path, []byte(`{"schema":1,"partitionConsent":false,"credentialResetConfirm":false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if p := b.Policies(); p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("after the file changed on disk: %+v", p)
	}

	broken := []byte(`{"partitionConsent": tru`)
	if err := os.WriteFile(path, broken, 0o644); err != nil {
		t.Fatal(err)
	}
	if p := b.Policies(); p != (WorkspacePolicies{}) {
		t.Fatalf("an unreadable file reads %+v, want the defaults", p)
	}
	owner := auth.Principal{Owner: true, Via: "bearer"}
	if w := call(t, b.apiPoliciesGet, owner, "GET", "/workspace-policies", "", nil); w.Code != http.StatusInternalServerError {
		t.Errorf("GET on an unreadable file = %d, want 500", w.Code)
	}
	if w := call(t, b.apiPoliciesPut, owner, "PUT", "/workspace-policies", `{"partitionConsent":true}`, nil); w.Code != http.StatusInternalServerError {
		t.Errorf("PUT on an unreadable file = %d, want 500", w.Code)
	}
	if got, _ := os.ReadFile(path); string(got) != string(broken) {
		t.Fatalf("a PUT overwrote an unreadable file: %s", got)
	}
}
