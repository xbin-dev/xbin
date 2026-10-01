package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/wssettings"
)

// putFile writes a file atomically (an editor's save, an older xbind's
// write): the settings cache sees the change whatever its size.
func putFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
}

// covers PD-55, D180 — who reads the partitioned tiles' switches, with the
// broker's own IsAdmin (the routes are the server's, over the same rule):
// admins (the admin tile through xbin:admin included) and a person through
// their session, terminal or agent session; never other tile code. The
// partitions listing carries them by the same rule. Both off until set.
func TestWorkspacePoliciesReaders(t *testing.T) {
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
	for _, tc := range []struct {
		name        string
		p           auth.Principal
		read, admin bool
	}{
		{"the root token", auth.Principal{Owner: true, Via: "bearer"}, true, true},
		{"a workspace admin", principalFor(t, st, "root2"), true, true},
		{"the admin tile (xbin:admin)", auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "bob"}, true, true},
		{"a person", bob, true, false},
		{"an org admin", principalFor(t, st, "carol"), true, false},
		{"a person's terminal", bobTerm, true, false},
		{"a person's agent session on a deployment", bobAgent, true, false},
		{"a tile frame", auth.Principal{Component: "apps/email", Via: "frame", UserID: "bob"}, false, false},
		{"a tile instance", auth.Principal{Component: "apps/email", Via: "instance"}, false, false},
		{"a cron delivery", auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer"}, false, false},
		{"nobody", auth.Principal{}, false, false},
	} {
		if got := b.canReadPolicies(tc.p); got != tc.read {
			t.Errorf("%s: reads the switches = %v, want %v", tc.name, got, tc.read)
		}
		if got := b.IsAdmin(tc.p); got != tc.admin {
			t.Errorf("%s: sets them (IsAdmin) = %v, want %v", tc.name, got, tc.admin)
		}
		var out map[string]any
		_ = json.Unmarshal(call(t, b.apiPartitionsList, tc.p, "GET", "/partitions", "", nil).Body.Bytes(), &out)
		if _, listed := out["policies"]; listed != tc.read {
			t.Errorf("%s: the partitions listing carries the switches = %v, want %v", tc.name, listed, tc.read)
		}
	}
}

// covers PD-55, D180 — the switches live in the settings file, not
// users.json, so a users-store rewrite (this xbind's, or an older one that
// keeps only the keys it knows) never touches them; a fresh xbind on the
// same workspace reads them back, and the pre-D180 file holds them too.
func TestWorkspacePoliciesSurviveUsersRewrite(t *testing.T) {
	b := testBroker(t)
	dataDir := filepath.Join(b.Reg.Root, "data")
	st, err := users.Open(dataDir) // the users store beside the settings, as in production
	if err != nil {
		t.Fatal(err)
	}
	b.Users = st
	if _, err := st.Upsert(users.User{ID: "ann", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.settings().Apply(wssettings.Patch{PartitionConsent: boolp(true), CredentialResetConfirm: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTileCreation("org-only"); err != nil {
		t.Fatal(err)
	}
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
	var v0366 map[string]any // what a downgrade to v0.3.66 reads
	b2, _ := os.ReadFile(filepath.Join(dataDir, wssettings.PoliciesFile))
	if err := json.Unmarshal(b2, &v0366); err != nil || v0366["schema"] != float64(1) || v0366["partitionConsent"] != true || v0366["credentialResetConfirm"] != true {
		t.Fatalf("the pre-D180 file: %s (%v)", b2, err)
	}
}

// covers D180 — an upgrade from v0.3.66 reads its data/workspace-policies.json
// (imported into the settings file on the first read); a hand edit or
// restore of the settings file is picked up without a restart.
func TestWorkspacePoliciesUpgrade(t *testing.T) {
	b := testBroker(t)
	data := filepath.Join(b.Reg.Root, "data")
	putFile(t, filepath.Join(data, wssettings.PoliciesFile), `{"schema":1,"partitionConsent":true,"credentialResetConfirm":false}`)
	if p := b.Policies(); !p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("v0.3.66's file reads %+v", p)
	}
	settings := filepath.Join(data, "workspace-settings.json")
	if raw, _ := os.ReadFile(settings); !strings.Contains(string(raw), `"partitionConsent": true`) {
		t.Fatalf("the import wasn't saved: %s", raw)
	}
	// a restore puts back an older copy: read on the next check
	putFile(t, settings, `{"partitionConsent":false,"credentialResetConfirm":true,"policiesFileSha256":"`+sum(t, filepath.Join(data, wssettings.PoliciesFile))+`"}`)
	if p := b.Policies(); p.PartitionConsent || !p.CredentialResetConfirm {
		t.Fatalf("after the file changed on disk: %+v", p)
	}
}

func sum(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

// covers PD-55, D180 — a setting that can't be read: both switches are
// protections, so a cold start counts them on and a warm one keeps their
// last values; admins see a crit alert (people none) until the file is
// fixed; base auto-update alone unreadable is a warn.
func TestWorkspaceSettingsAlert(t *testing.T) {
	b, st := orgFixture(t)
	path := b.settings().Path()
	owner := auth.Principal{Owner: true, Via: "bearer"}
	bob := principalFor(t, st, "bob")
	alerts := func(p auth.Principal) map[string]Alert {
		var out struct{ Alerts []Alert }
		_ = json.Unmarshal(call(t, b.apiAlerts, p, "GET", "/alerts", "", nil).Body.Bytes(), &out)
		m := map[string]Alert{}
		for _, a := range out.Alerts {
			m[a.Kind] = a
		}
		return m
	}
	putFile(t, path, `{"partitionConsent":true,"credentialResetConfirm":false}`)
	if p := b.Policies(); !p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("the good file reads %+v", p)
	}
	putFile(t, path, `{"partitionConsent": true, "credentialResetConfirm": tru`)
	if p := b.Policies(); !p.PartitionConsent || p.CredentialResetConfirm {
		t.Fatalf("a broken file after a read: %+v, want the last values", p)
	}
	a, ok := alerts(owner)["workspace-settings"]
	if !ok || a.Level != "crit" || !strings.Contains(a.Message, "data/workspace-settings.json") || strings.Contains(a.Message, b.Reg.Root) ||
		!strings.Contains(a.Message, "base auto-update is off and each partitioned tiles' switch") {
		t.Errorf("an admin's alert: %+v", a)
	}
	if _, ok := alerts(bob)["workspace-settings"]; ok {
		t.Error("a person sees the settings alert; it is admins'")
	}
	if p := (&Broker{Reg: b.Reg}).Policies(); !p.PartitionConsent || !p.CredentialResetConfirm {
		t.Fatalf("a cold start on a broken file: %+v, want both on", p)
	}
	putFile(t, path, `{"baseAutoUpdate":"no"}`)
	if a := alerts(owner)["workspace-settings"]; a.Level != "warn" || !strings.HasSuffix(a.Message, "until then base auto-update is off") {
		t.Errorf("base auto-update alone: %+v", a)
	}
	putFile(t, path, `{}`)
	if _, ok := alerts(owner)["workspace-settings"]; ok || b.Policies() != (WorkspacePolicies{}) {
		t.Fatalf("after the fix: %+v %+v", alerts(owner), b.Policies())
	}
}
