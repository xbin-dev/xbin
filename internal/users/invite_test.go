package users

import (
	"strings"
	"testing"
)

// The D22 invite lifecycle: mint → verify → redeem sets the password and is
// single-use; expiry and re-minting invalidate; hashes never leave the store.
func TestInviteLifecycle(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Credential-less creation: no self-signup, but also no password yet.
	u, err := s.UpsertInvited(User{ID: "erin", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify("erin", "anything"); ok {
		t.Fatal("credential-less user must not verify")
	}
	if _, err := s.UpsertInvited(User{ID: "erin"}); err == nil {
		t.Fatal("duplicate create must fail (mint a new invite instead)")
	}

	tok, err := s.CreateInvite(u.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 30 {
		t.Fatalf("token too short: %q", tok)
	}
	// The store never exposes the hash.
	pub, _ := s.Get("erin")
	if p := pub.Public(); p.InviteHash != "" {
		t.Fatal("Public() must strip the invite hash")
	}
	if !pub.InvitePending() {
		t.Fatal("invite must be pending")
	}
	if got := s.List()[0].InviteHash; got != "" {
		t.Fatal("List() must strip the invite hash")
	}

	// Verify-without-consume greets the invitee.
	if iu, ok := s.InviteUser(tok); !ok || iu.ID != "erin" {
		t.Fatalf("InviteUser: %v %v", iu, ok)
	}
	if _, ok := s.InviteUser("wrong-token"); ok {
		t.Fatal("wrong token must not resolve")
	}

	// Weak password refused, invite NOT consumed.
	if _, err := s.RedeemInvite(tok, "short"); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("weak password: %v", err)
	}
	// Redeem sets the password + signs-in-able.
	if _, err := s.RedeemInvite(tok, "erin-password-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify("erin", "erin-password-1"); !ok {
		t.Fatal("redeemed password must verify")
	}
	// Single-use.
	if _, err := s.RedeemInvite(tok, "erin-password-2"); err == nil {
		t.Fatal("second redeem must fail")
	}
	if _, ok := s.Verify("erin", "erin-password-1"); !ok {
		t.Fatal("failed re-redeem must not clobber the password")
	}

	// Re-minting invalidates the previous link; the password keeps working.
	t1, _ := s.CreateInvite("erin", 0)
	t2, _ := s.CreateInvite("erin", 0)
	if _, err := s.RedeemInvite(t1, "erin-password-3"); err == nil {
		t.Fatal("re-minted: the old link must be dead")
	}
	if _, ok := s.Verify("erin", "erin-password-1"); !ok {
		t.Fatal("password must survive minting an invite")
	}
	if _, err := s.RedeemInvite(t2, "erin-password-3"); err != nil {
		t.Fatal(err)
	}

	// Expiry: freeze the clock past the deadline.
	t3, _ := s.CreateInvite("erin", 0)
	old := timeNow
	timeNow = func() int64 { return old() + int64(InviteTTL.Seconds()) + 10 }
	defer func() { timeNow = old }()
	if _, ok := s.InviteUser(t3); ok {
		t.Fatal("expired invite must not resolve")
	}
	if _, err := s.RedeemInvite(t3, "erin-password-4"); err == nil {
		t.Fatal("expired invite must not redeem")
	}
}
