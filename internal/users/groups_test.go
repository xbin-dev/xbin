package users

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// groupsStore: admin boss, users ann/bob, orgs sales (rule sales@ →
// developer) and infra (rules infra@ → admin, oncall@ → viewer+create).
func groupsStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, dir := defaultsStore(t)
	for _, id := range []string{"ann", "bob"} {
		if _, err := s.Upsert(User{ID: id, Email: id + "@corp.com"}, "password123"); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range []string{"sales", "infra"} {
		if _, err := s.UpsertOrg(Org{ID: o}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetOrgSSOGroups("sales", []GroupRule{{Group: "Sales@corp.com", Level: LevelTerminal, Create: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgSSOGroups("infra", []GroupRule{
		{Group: "infra@corp.com", Level: LevelTerminal, Create: true, Admin: true},
		{Group: "oncall@corp.com", Create: true},
	}); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func member(t *testing.T, s *Store, org, id string) (Member, bool) {
	t.Helper()
	o, ok := s.Org(org)
	if !ok {
		t.Fatalf("no org %s", org)
	}
	return o.Member(id)
}

// Add on first sync, re-set when the rule changes, removed when the group
// disappears; nothing persisted when nothing changed.
func TestSyncAddUpdateRemove(t *testing.T) {
	s, dir := groupsStore(t)
	rep, err := s.SyncSSOGroups("ann", []string{" sales@CORP.com ", "sales@corp.com", "x@corp.com"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Added) != 1 || rep.Added[0].Org != "sales" || rep.Added[0].Level != LevelTerminal || !rep.Changed {
		t.Fatalf("first sync: %+v", rep)
	}
	if !equalStrings(rep.Groups, []string{"sales@CORP.com", "x@corp.com"}) {
		t.Fatalf("normalized groups: %v", rep.Groups)
	}
	m, ok := member(t, s, "sales", "ann")
	if !ok || m.Via != MemberViaSSO || m.Level != LevelTerminal || !m.Create || len(m.ViaGroups) != 1 || m.ViaGroups[0] != "Sales@corp.com" {
		t.Fatalf("synced row: %+v %v", m, ok)
	}
	u, _ := s.Get("ann")
	if !equalStrings(u.SSOGroups, rep.Groups) {
		t.Fatalf("SSOGroups recorded: %v", u.SSOGroups)
	}

	// Same input again: a no-op that must not rewrite the file.
	before, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	rep2, err := s.SyncSSOGroups("ann", []string{"x@corp.com", "sales@corp.com"}, true)
	if err != nil || rep2.Changed || len(rep2.Added)+len(rep2.Updated)+len(rep2.Removed) != 0 {
		t.Fatalf("no-op sync: %+v %v", rep2, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "users.json"))
	if string(before) != string(after) {
		t.Fatal("no-op sync rewrote users.json")
	}

	// Rule edit → re-set at the next sign-in (Suspended survives).
	if _, err := s.SetOrgMember("sales", "ann", MemberPatch{Suspended: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgSSOGroups("sales", []GroupRule{{Group: "sales@corp.com", Level: LevelWrite}}); err != nil {
		t.Fatal(err)
	}
	rep3, _ := s.SyncSSOGroups("ann", []string{"sales@corp.com"}, true)
	m, _ = member(t, s, "sales", "ann")
	if len(rep3.Updated) != 1 || m.Level != LevelWrite || m.Create || !m.Suspended || m.Via != MemberViaSSO {
		t.Fatalf("re-sync: %+v row %+v", rep3, m)
	}

	// Group gone → membership gone.
	rep4, _ := s.SyncSSOGroups("ann", nil, true)
	if len(rep4.Removed) != 1 || rep4.Removed[0] != "sales" {
		t.Fatalf("removal: %+v", rep4)
	}
	if _, ok := member(t, s, "sales", "ann"); ok {
		t.Fatal("synced row must be removed when the group disappears")
	}
	if u, _ := s.Get("ann"); len(u.SSOGroups) != 0 {
		t.Fatalf("SSOGroups must follow the provider: %v", u.SSOGroups)
	}
	// Rule deleted → same.
	s.SyncSSOGroups("ann", []string{"sales@corp.com"}, true)
	if err := s.SetOrgSSOGroups("sales", nil); err != nil {
		t.Fatal(err)
	}
	if rep, _ := s.SyncSSOGroups("ann", []string{"sales@corp.com"}, true); len(rep.Removed) != 1 {
		t.Fatalf("rule removal must drop the synced row: %+v", rep)
	}
}

// Manual rows are never touched by sync — even when a rule matches (manual
// wins, reported) or the group is gone; detach turns a synced row manual.
func TestSyncManualWinsAndDetach(t *testing.T) {
	s, _ := groupsStore(t)
	if _, err := s.SetOrgMember("sales", "bob", MemberPatch{Level: strp(LevelRead)}); err != nil {
		t.Fatal(err)
	}
	rep, _ := s.SyncSSOGroups("bob", []string{"sales@corp.com"}, true)
	if len(rep.SkippedManual) != 1 || len(rep.Added) != 0 {
		t.Fatalf("manual must win: %+v", rep)
	}
	m, _ := member(t, s, "sales", "bob")
	if m.Level != LevelRead || m.Via != "" {
		t.Fatalf("manual row edited by sync: %+v", m)
	}
	rep, _ = s.SyncSSOGroups("bob", nil, true)
	if len(rep.Removed) != 0 {
		t.Fatalf("manual row removed by sync: %+v", rep)
	}
	if _, ok := member(t, s, "sales", "bob"); !ok {
		t.Fatal("manual row gone")
	}

	// Detach: synced → manual, then immune.
	s.SyncSSOGroups("ann", []string{"infra@corp.com"}, true)
	if m, _ := member(t, s, "infra", "ann"); m.Via != MemberViaSSO || !m.Admin {
		t.Fatalf("expected synced infra admin: %+v", m)
	}
	if _, err := s.SetOrgMember("infra", "ann", MemberPatch{Detach: true}); err != nil {
		t.Fatal(err)
	}
	m, _ = member(t, s, "infra", "ann")
	if m.Via != "" || m.ViaGroups != nil || !m.Admin {
		t.Fatalf("detach must clear provenance only: %+v", m)
	}
	if rep, _ := s.SyncSSOGroups("ann", nil, true); len(rep.Removed) != 0 {
		t.Fatalf("detached row removed: %+v", rep)
	}
	// A whole-list edit (PATCH /orgs) keeps provenance and ignores the
	// client's via.
	s.SyncSSOGroups("bob", []string{"infra@corp.com"}, true)
	o, _ := s.Org("infra")
	var members []Member
	for _, x := range o.Members {
		x.Via = "forged"
		x.ViaGroups = nil
		members = append(members, x)
	}
	members = append(members, Member{ID: "boss", Level: LevelRead, Via: MemberViaSSO})
	if _, err := s.UpsertOrg(Org{ID: "infra", Members: members}); err != nil {
		t.Fatal(err)
	}
	if m, _ := member(t, s, "infra", "bob"); m.Via != MemberViaSSO || len(m.ViaGroups) != 1 {
		t.Fatalf("whole-list edit must keep provenance: %+v", m)
	}
	if m, _ := member(t, s, "infra", "boss"); m.Via != "" {
		t.Fatalf("whole-list edit must not forge provenance: %+v", m)
	}
	if o, _ := s.Org("infra"); len(o.SSOGroups) != 2 {
		t.Fatal("UpsertOrg must keep the rules")
	}
}

// Several rules on one org union: highest level, create OR, admin OR.
func TestSyncRuleUnion(t *testing.T) {
	s, _ := groupsStore(t)
	rep, _ := s.SyncSSOGroups("ann", []string{"oncall@corp.com", "infra@corp.com"}, true)
	if len(rep.Added) != 1 {
		t.Fatalf("union: %+v", rep)
	}
	m, _ := member(t, s, "infra", "ann")
	if m.Level != LevelTerminal || !m.Create || !m.Admin || len(m.ViaGroups) != 2 {
		t.Fatalf("union row: %+v", m)
	}
	// Losing the admin group keeps the membership at the remaining rule.
	rep, _ = s.SyncSSOGroups("ann", []string{"oncall@corp.com"}, true)
	m, _ = member(t, s, "infra", "ann")
	if len(rep.Updated) != 1 || m.Level != LevelRead || !m.Create || m.Admin || len(m.ViaGroups) != 1 {
		t.Fatalf("narrowed row: %+v %+v", rep, m)
	}
}

// Workspace admin by rule: granted with provenance, revoked when the group
// goes — never for a hand-promoted admin, never for the last enabled one.
func TestSyncAdminRole(t *testing.T) {
	s, _ := groupsStore(t)
	if err := s.SetSSO(&SSOConfig{Kind: "oidc", Issuer: "https://idp", ClientID: "c",
		AdminGroups: []string{" Admins@corp.com ", "admins@corp.com", ""}}); err != nil {
		t.Fatal(err)
	}
	if got := s.SSO().AdminGroups; len(got) != 1 || got[0] != "Admins@corp.com" {
		t.Fatalf("admin groups normalized: %v", got)
	}
	rep, _ := s.SyncSSOGroups("ann", []string{"admins@corp.com"}, true)
	u, _ := s.Get("ann")
	if !rep.AdminGranted || u.Role != RoleAdmin || u.RoleVia != MemberViaSSO {
		t.Fatalf("grant: %+v %+v", rep, u)
	}
	// Group gone, another enabled admin (boss) exists → revoked.
	rep, _ = s.SyncSSOGroups("ann", nil, true)
	u, _ = s.Get("ann")
	if !rep.AdminRevoked || u.Role != RoleUser || u.RoleVia != "" {
		t.Fatalf("revoke: %+v %+v", rep, u)
	}
	// Hand-promoted admin: never demoted, never re-stamped.
	if _, err := s.Upsert(User{ID: "bob", Role: RoleAdmin, Email: "bob@corp.com"}, ""); err != nil {
		t.Fatal(err)
	}
	s.SyncSSOGroups("bob", []string{"admins@corp.com"}, true)
	if u, _ := s.Get("bob"); u.RoleVia != "" {
		t.Fatalf("hand-promoted admin re-stamped: %+v", u)
	}
	rep, _ = s.SyncSSOGroups("bob", nil, true)
	if u, _ := s.Get("bob"); rep.AdminRevoked || u.Role != RoleAdmin {
		t.Fatalf("hand-promoted admin demoted: %+v %+v", rep, u)
	}
	// Last enabled admin: boss and bob disabled, ann is rule-admin → blocked.
	s.SyncSSOGroups("ann", []string{"admins@corp.com"}, true)
	for _, id := range []string{"boss", "bob"} {
		u, _ := s.Get(id)
		u.Disabled = true
		if _, err := s.Upsert(*u, ""); err != nil {
			t.Fatal(err)
		}
	}
	rep, _ = s.SyncSSOGroups("ann", nil, true)
	u, _ = s.Get("ann")
	if rep.AdminBlocked != "last-admin" || rep.AdminRevoked || u.Role != RoleAdmin || u.RoleVia != MemberViaSSO {
		t.Fatalf("last-admin guard: %+v %+v", rep, u)
	}
	// Re-enable boss → the pending revoke goes through at the next sign-in.
	b, _ := s.Get("boss")
	b.Disabled = false
	s.Upsert(*b, "")
	rep, _ = s.SyncSSOGroups("ann", nil, true)
	if !rep.AdminRevoked {
		t.Fatalf("retry after guard: %+v", rep)
	}
	// A manual role change through Upsert drops the provenance.
	s.SyncSSOGroups("ann", []string{"admins@corp.com"}, true)
	a, _ := s.Get("ann")
	a.Role = RoleUser
	s.Upsert(*a, "")
	if u, _ := s.Get("ann"); u.RoleVia != "" {
		t.Fatalf("manual demotion must clear RoleVia: %+v", u)
	}
}

// A fetch failure records the error and changes nothing; the next success
// clears it. Sign-in facts survive an Upsert (API update) and a reload.
func TestSyncErrorAndSignInFacts(t *testing.T) {
	s, dir := groupsStore(t)
	s.SyncSSOGroups("ann", []string{"sales@corp.com"}, true)
	if err := s.RecordSSOSyncError("ann", strings.Repeat("x", 500)); err != nil {
		t.Fatal(err)
	}
	u, _ := s.Get("ann")
	if len(u.SSOSyncError) != maxSSOSyncErrorLen || len(u.SSOGroups) != 1 {
		t.Fatalf("error recorded: %d %v", len(u.SSOSyncError), u.SSOGroups)
	}
	if _, ok := member(t, s, "sales", "ann"); !ok {
		t.Fatal("membership must survive a fetch failure")
	}
	if err := s.TouchLogin("ann", "sso"); err != nil {
		t.Fatal(err)
	}
	// An API-style update (whole-row Upsert) keeps the sign-in facts.
	u, _ = s.Get("ann")
	u.Name = "Ann"
	u.LastLogin, u.SSOGroups, u.SSOSyncError = 0, nil, ""
	if _, err := s.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	u, _ = s.Get("ann")
	if u.LastLogin == 0 || u.LastLoginVia != "sso" || len(u.SSOGroups) != 1 || u.SSOSyncError == "" {
		t.Fatalf("sign-in facts lost on Upsert: %+v", u)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	u2, _ := s2.Get("ann")
	if u2.LastLogin != u.LastLogin || u2.LastLoginVia != "sso" || !equalStrings(u2.SSOGroups, u.SSOGroups) || u2.SSOSyncError != u.SSOSyncError {
		t.Fatalf("sign-in facts lost on reload: %+v", u2)
	}
	if m, ok := member(t, s2, "sales", "ann"); !ok || m.Via != MemberViaSSO {
		t.Fatalf("provenance lost on reload: %+v %v", m, ok)
	}
	if o, _ := s2.Org("infra"); len(o.SSOGroups) != 2 {
		t.Fatal("rules lost on reload")
	}
	rep, _ := s2.SyncSSOGroups("ann", []string{"sales@corp.com"}, true)
	if u, _ := s2.Get("ann"); u.SSOSyncError != "" || !rep.Changed {
		t.Fatalf("success must clear the error: %+v %+v", u, rep)
	}
	if got := s2.KnownSSOGroups(); len(got) != 1 || got[0] != "sales@corp.com" {
		t.Fatalf("known groups: %v", got)
	}
	if !s2.SSOGroupRulesExist() {
		t.Fatal("rules exist")
	}
}

// Single-membership primitives and rule validation.
func TestSetOrgMemberAndRules(t *testing.T) {
	s, _ := groupsStore(t)
	if _, err := s.SetOrgMember("nope", "ann", MemberPatch{}); err == nil {
		t.Fatal("unknown org accepted")
	}
	if _, err := s.SetOrgMember("sales", "ghost", MemberPatch{}); err == nil {
		t.Fatal("unknown user accepted")
	}
	if _, err := s.SetOrgMember("sales", "ann", MemberPatch{Level: strp("owner")}); err == nil {
		t.Fatal("bad level accepted")
	}
	m, err := s.SetOrgMember("sales", "ann", MemberPatch{Create: boolp(true)})
	if err != nil || m.Level != LevelRead || !m.Create || m.Admin {
		t.Fatalf("new manual row: %+v %v", m, err)
	}
	m, _ = s.SetOrgMember("sales", "ann", MemberPatch{Level: strp(LevelTerminal), Admin: boolp(true)})
	if m.Level != LevelTerminal || !m.Create || !m.Admin {
		t.Fatalf("partial patch: %+v", m)
	}
	if _, err := s.RemoveOrgMember("sales", "bob"); err != ErrNotMember {
		t.Fatalf("non-member: %v", err)
	}
	if _, err := s.RemoveOrgMember("sales", "ann"); err != nil {
		t.Fatal(err)
	}
	if _, ok := member(t, s, "sales", "ann"); ok {
		t.Fatal("still a member")
	}
	for _, bad := range [][]GroupRule{
		{{Group: " "}},
		{{Group: "a"}, {Group: "A"}},
		{{Group: "a", Level: "root"}},
	} {
		if err := s.SetOrgSSOGroups("sales", bad); err == nil {
			t.Fatalf("bad rules accepted: %+v", bad)
		}
	}
	if err := s.SetOrgSSOGroups("nope", nil); err == nil {
		t.Fatal("unknown org accepted")
	}
	if err := s.SetOrgSSOGroups("sales", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgSSOGroups("infra", nil); err != nil {
		t.Fatal(err)
	}
	if s.SSOGroupRulesExist() {
		t.Fatal("no rules left")
	}
}

// SSO-only mode needs SSO and dies with it.
func TestPasswordLoginDisabledGuard(t *testing.T) {
	s, dir := groupsStore(t)
	if err := s.SetPasswordLoginDisabled(true); err == nil {
		t.Fatal("must need SSO")
	}
	if err := s.SetSSO(&SSOConfig{Kind: "github", ClientID: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPasswordLoginDisabled(true); err != nil || !s.PasswordLoginDisabled() {
		t.Fatalf("enable: %v", err)
	}
	s2, _ := Open(dir)
	if !s2.PasswordLoginDisabled() {
		t.Fatal("not persisted")
	}
	if err := s2.SetSSO(nil); err != nil || s2.PasswordLoginDisabled() {
		t.Fatal("removing SSO must clear SSO-only mode")
	}
}

func strp(s string) *string { return &s }
func boolp(b bool) *bool    { return &b }
