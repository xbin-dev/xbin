package broker

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// covers PD-07 S6 06§9 — the held link's edges: it is stored held (no
// token's hash matches it, so an xbind without the gate never redeems it);
// no gate, or a notices file this xbind can't read, keeps it waiting; a
// hold that can't be kept revokes the link (500); a refusal after the admin
// redeemed it answers already-effective; a refusal of a link unused past
// its 24 hours still revokes it.
func TestPartitionHeldLinkEdges(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	b.InstallCredentialGate()
	t.Cleanup(func() { b.Users.SetInviteGate(nil) })
	partRouteCredPolicy(t, f.partWS, true)
	bob, alice := personP(t, f.partWS, "bob"), personP(t, f.partWS, "alice")
	invite := func(want int) (string, map[string]any) {
		t.Helper()
		out := mustCode(t, call(t, b.apiUsersInvite, bob, "POST", "/", "", map[string]string{"id": "alice"}), want, "invite alice")
		tok, _ := out["invite"].(string)
		return tok, out
	}
	later := func() { peopleNow = func() time.Time { return time.Now().Add(credentialHoldFor + time.Minute) } }
	t.Cleanup(func() { peopleNow = time.Now })

	// stored held: users.json carries no hash a token matches
	tok, out := invite(200)
	if out["held"] != true {
		t.Fatalf("not held: %v", out)
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "data", "users.json"))
	if err != nil || !strings.Contains(string(raw), `"`+users.HeldInvitePrefix+users.InviteHashOf(tok)+`"`) || strings.Contains(string(raw), `"`+users.InviteHashOf(tok)+`"`) {
		t.Errorf("the held link isn't stored held: %v", err)
	}
	// no gate (an xbind without it): the link waits, even past its 24 hours
	b.Users.SetInviteGate(nil)
	later()
	if _, err := b.Users.RedeemInvite(tok, "newpassword5"); !errors.Is(err, users.ErrInviteHeld) {
		t.Errorf("a held link redeemed without the gate: %v", err)
	}
	peopleNow = time.Now
	b.InstallCredentialGate()

	// a notices file this xbind can't read: the link waits, past its 24 hours too
	uid := b.storedPartitionUID("alice")
	doc, _ := b.personDocPath(uid)
	good, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(doc, []byte(`{"schema":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	later()
	if _, err := b.Users.RedeemInvite(tok, "newpassword5"); !errors.Is(err, users.ErrInviteHeld) {
		t.Errorf("a held link redeemed with an unreadable notices file: %v", err)
	}
	peopleNow = time.Now
	// ... and a new link can't be held there: it is revoked, the answer 500
	invite(500)
	if u, _ := b.Users.Get("alice"); u.InviteHash != "" {
		t.Errorf("a link whose hold failed stays pending: %q", u.InviteHash)
	}
	if err := os.WriteFile(doc, good, 0o600); err != nil {
		t.Fatal(err)
	}

	// the admin redeems it past the 24 hours; alice then refuses: already effective
	tok, _ = invite(200)
	held := b.heldOf("alice")
	later()
	if _, err := b.Users.RedeemInvite(tok, "adminpassword1"); err != nil {
		t.Fatalf("a link unanswered for 24 h: %v", err)
	}
	peopleNow = time.Now
	res := mustCode(t, call(t, b.apiCredentialConfirm, alice, "POST", "/", `{"id":"`+held[0].ID+`","allow":false}`, nil), 409, "refusing a link already used")
	if res["decision"] != "already-effective" || !strings.Contains(res["error"].(string), "change your password") {
		t.Errorf("the late refusal answered %v", res)
	}
	if len(b.heldOf("alice")) != 0 {
		t.Error("the used link is still listed as waiting")
	}

	// unused past its 24 hours: a refusal still revokes it
	tok, _ = invite(200)
	held = b.heldOf("alice")
	later()
	mustCode(t, call(t, b.apiCredentialConfirm, alice, "POST", "/", `{"id":"`+held[0].ID+`","allow":false}`, nil), 200, "refusing an unused link late")
	if _, err := b.Users.RedeemInvite(tok, "adminpassword2"); !errors.Is(err, users.ErrInvalidInvite) {
		t.Errorf("a refused link redeemed: %v", err)
	}
	peopleNow = time.Now
}

// partRouteCredPolicy writes the workspace policy credentialResetConfirm.
func partRouteCredPolicy(t *testing.T, w *partWS, on bool) {
	t.Helper()
	body := `{"schema":1,"partitionConsent":false,"credentialResetConfirm":` + map[bool]string{true: "true", false: "false"}[on] + `}`
	p := filepath.Join(w.root, "data", "workspace-policies.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Duration(len(body)) * time.Second)
	_ = os.Chtimes(p, later, later)
	if w.b.Policies().CredentialResetConfirm != on {
		t.Fatal("the policy didn't take")
	}
}

// covers PD-07 S6 06§9 10-compat "rebind SSO" — repointing the workspace's
// SSO provider is a credential for every partition holder bound by email:
// policy off, told; policy on, their sign-ins through it held until they
// allow it (or 24 h), Refuse unbinds their email; a secret-only edit and a
// person without partitions are untouched.
func TestPartitionSSOProviderChange(t *testing.T) {
	f := newOpsFx(t)
	b := f.b
	b.InstallCredentialGate()
	t.Cleanup(func() { b.Users.SetInviteGate(nil); b.Users.SetSSOGate(nil) })
	bob, alice := personP(t, f.partWS, "bob"), personP(t, f.partWS, "alice")
	for id, email := range map[string]string{"alice": "alice@example.com", "carol": "carol@example.com"} {
		u, _ := b.Users.Get(id)
		u.Email = email
		if _, err := b.Users.Upsert(*u, ""); err != nil {
			t.Fatal(err)
		}
	}
	sso := func(client, secret string) {
		t.Helper()
		body := `{"sso":{"kind":"oidc","issuer":"http://127.0.0.1:9/idp","clientId":"` + client + `","clientSecret":"` + secret + `"}}`
		mustCode(t, call(t, b.apiAuthSettingsUpdate, bob, "PATCH", "/", body, nil), 200, "sso "+client)
	}
	signIn := func(id string) error {
		u, _ := b.Users.Get(id)
		return b.Users.SSOSignInHeld(*u)
	}

	// policy off: told, not held
	sso("c1", "s1")
	if len(b.noticesOf("alice", "")) != 1 || !slices.Contains(f.pushes, "alice account.credential") || signIn("alice") != nil {
		t.Errorf("policy off: notices %v, pushes %q, held %v", b.noticesOf("alice", ""), f.pushes, signIn("alice"))
	}
	if len(b.noticesOf("carol", "")) != 0 {
		t.Error("carol, who holds no partitions, was told")
	}

	partRouteCredPolicy(t, f.partWS, true)
	sso("c1", "s2") // a new secret: the same provider
	if len(b.heldOf("alice")) != 0 {
		t.Error("a secret-only edit held alice's sign-ins")
	}
	sso("c2", "") // another client: held
	held := b.heldOf("alice")
	if len(held) != 1 || held[0].Kind != credSSO || !errors.Is(signIn("alice"), users.ErrSSOHeld) {
		t.Fatalf("policy on: held %+v, sign-in %v", held, signIn("alice"))
	}
	if signIn("carol") != nil {
		t.Error("carol's sign-in held")
	}
	mustCode(t, call(t, b.apiCredentialConfirm, alice, "POST", "/", `{"id":"`+held[0].ID+`","allow":true}`, nil), 200, "alice allows")
	if signIn("alice") != nil {
		t.Error("an allowed provider change still holds alice's sign-in")
	}

	sso("c3", "")
	held = b.heldOf("alice")
	later := time.Now().Add(credentialHoldFor + time.Minute)
	peopleNow = func() time.Time { return later }
	if signIn("alice") != nil {
		t.Error("a provider change unanswered for 24 h still holds alice's sign-in")
	}
	peopleNow = time.Now
	mustCode(t, call(t, b.apiCredentialConfirm, alice, "POST", "/", `{"id":"`+held[0].ID+`","allow":false}`, nil), 200, "alice refuses")
	if u, _ := b.Users.Get("alice"); u.Email != "" {
		t.Errorf("a refused provider change left alice's email bound: %q", u.Email)
	}
}
