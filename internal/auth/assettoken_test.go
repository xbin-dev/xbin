package auth

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

func assetTestAuth(t *testing.T) (*Auth, *users.Store) {
	t.Helper()
	a := testAuth(t)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	if _, err := st.Upsert(users.User{ID: "ana", Role: users.RoleUser,
		Tiles: map[string]string{"apps/a": users.LevelRead, "apps/b": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	return a, st
}

func TestAssetTokenRoundTrip(t *testing.T) {
	a, _ := assetTestAuth(t)
	tok := a.MintAssetToken("apps/a", "ana")
	if strings.ContainsAny(tok, "/|?#%") {
		t.Fatalf("asset token must be one URL path segment: %q", tok)
	}
	g, ok := a.VerifyAssetToken(tok)
	if !ok || g.Tile != "apps/a" || g.UserID != "ana" {
		t.Fatalf("verify: %+v %v", g, ok)
	}
	if time.Until(g.Exp) < AssetTokenTTL-time.Minute {
		t.Fatalf("expiry %v, want ~%v", time.Until(g.Exp), AssetTokenTTL)
	}
	// Tampering with any field breaks the MAC.
	parts := strings.Split(tok, ".")
	for i := 1; i < 6; i++ {
		bad := append([]string(nil), parts...)
		bad[i] = "x" + bad[i]
		if _, ok := a.VerifyAssetToken(strings.Join(bad, ".")); ok {
			t.Fatalf("tampered field %d verified", i)
		}
	}
	// Another workspace's secret: no.
	if _, ok := testAuth(t).VerifyAssetToken(tok); ok {
		t.Fatal("cross-workspace token verified")
	}
}

// Purpose separation: an asset token is not a cookie, not a frame token,
// and neither of those is an asset token.
func TestAssetCredentialPurposes(t *testing.T) {
	a, _ := assetTestAuth(t)
	asset := a.MintAssetToken("apps/a", "ana")
	cookie := a.MintTileCookie("apps/a", "ana", "", time.Hour)
	frame := a.MintFrameToken("apps/a", "ana", time.Minute)
	ticket := a.MintTileTicket("apps/a", "", a.credGeneration(""))
	if _, ok := a.VerifyTileCookie(ticket); ok {
		t.Error("exchange ticket accepted as a tile cookie")
	}
	if _, ok := a.RedeemTileTicket(a.MintTileCookie("apps/a", "", a.credGeneration(""), time.Hour)); ok {
		t.Error("tile cookie redeemed as an exchange ticket")
	}

	if _, ok := a.VerifyTileCookie(asset); ok {
		t.Error("asset token accepted as a tile cookie")
	}
	if _, ok := a.VerifyAssetToken(cookie); ok {
		t.Error("tile cookie accepted as an asset token")
	}
	if _, _, ok := a.VerifyFrameToken(asset); ok {
		t.Error("asset token accepted as a frame token")
	}
	if _, _, ok := a.VerifyFrameToken(cookie); ok {
		t.Error("tile cookie accepted as a frame token")
	}
	if _, ok := a.VerifyAssetToken(frame); ok {
		t.Error("frame token accepted as an asset token")
	}
	// Same payload, other prefix: the MAC input carries the purpose.
	swapped := "c1" + strings.TrimPrefix(asset, "a1")
	if _, ok := a.VerifyTileCookie(swapped); ok {
		t.Error("re-prefixed asset token accepted as a cookie")
	}
}

// Liveness: a deleted or disabled user, the end of the login that loaded
// the document, a sign-out-everywhere, and (for owner-minted credentials) a
// rotated owner token all kill the credential on its next use — the same
// generations frame tokens are bound to (frametoken.go).
func TestAssetCredentialLiveness(t *testing.T) {
	a, st := assetTestAuth(t)

	// Bound to the login session that loaded the document: logout kills it,
	// a sibling session's token lives on.
	sid, other := a.NewSession("ana", ""), a.NewSession("ana", "")
	tok := a.MintAssetTokenFor(principalOf(t, a, sid, ""), "apps/a")
	sibling := a.MintAssetTokenFor(principalOf(t, a, other, ""), "apps/a")
	if g, ok := a.VerifyAssetToken(tok); !ok || !strings.HasPrefix(g.Gen, "s.") {
		t.Fatalf("session-bound token: %+v %v", g, ok)
	}
	a.DropSession(sid)
	if _, ok := a.VerifyAssetToken(tok); ok {
		t.Error("asset token outlived the session that loaded its document")
	}
	if _, ok := a.VerifyAssetToken(sibling); !ok {
		t.Error("logout killed another session's asset token")
	}

	// A tile's own frame principal (a navigation within its tree) passes its
	// token's binding on.
	fp := a.MintFrameTokenFor(principalOf(t, a, other, ""), "apps/a", time.Minute)
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set(FrameTokenHeader, fp)
	framed, ok := a.FromRequest(r)
	if !ok {
		t.Fatal("frame token refused")
	}
	viaFrame := a.MintAssetTokenFor(framed, "apps/a")

	// Bound to the user's generation (no session behind the principal) —
	// and sign-out-everywhere ends the session-bound ones too.
	userGen := a.MintAssetToken("apps/a", "ana")
	if _, ok := a.VerifyAssetToken(userGen); !ok {
		t.Fatal("fresh user-generation token refused")
	}
	a.DropUserSessions("ana")
	for name, tk := range map[string]string{"user generation": userGen, "sibling session": sibling, "frame-minted": viaFrame} {
		if _, ok := a.VerifyAssetToken(tk); ok {
			t.Errorf("%s token survived sign-out-everywhere", name)
		}
	}

	tok = a.MintAssetToken("apps/a", "ana")
	u, _ := st.Get("ana")
	u.Disabled = true
	if _, err := st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.VerifyAssetToken(tok); ok {
		t.Error("disabled user's token verifies")
	}
	if a.UserCanReadTile("ana", "apps/a") {
		t.Error("disabled user reads tiles")
	}

	owner := a.MintAssetToken("apps/a", "")
	if _, ok := a.VerifyAssetToken(owner); !ok {
		t.Fatal("owner token refused")
	}
	if _, err := a.RotateOwnerToken(); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.VerifyAssetToken(owner); ok {
		t.Error("owner-minted token survived an owner-token rotation")
	}
}

func TestUserCanReadTileIsLive(t *testing.T) {
	a, st := assetTestAuth(t)
	if !a.UserCanReadTile("ana", "apps/a") || a.UserCanReadTile("ana", "apps/secret") {
		t.Fatal("initial access wrong")
	}
	if err := st.GrantTile("ana", "apps/secret", users.LevelRead); err != nil {
		t.Fatal(err)
	}
	if !a.UserCanReadTile("ana", "apps/secret") {
		t.Error("a grant did not take effect on the next check")
	}
	if a.UserCanReadTile("nobody", "apps/a") {
		t.Error("unknown user reads")
	}
	if !a.UserCanReadTile("", "apps/anything") {
		t.Error("owner reads everything")
	}
}

func TestTileHostID(t *testing.T) {
	a, _ := assetTestAuth(t)
	id := a.TileHostID("apps/a")
	if id != a.TileHostID("apps/a") {
		t.Fatal("host id not stable")
	}
	if id == a.TileHostID("apps/b") {
		t.Fatal("two tiles share a host id")
	}
	if !strings.HasPrefix(id, "t-") || len(id) != 18 || strings.ToLower(id) != id || strings.Contains(id, "apps") {
		t.Fatalf("host id %q is not an opaque lowercase DNS label", id)
	}
	if testAuth(t).TileHostID("apps/a") == id {
		t.Fatal("host id not keyed by the workspace secret")
	}
}

func TestTilePrincipal(t *testing.T) {
	a, _ := assetTestAuth(t)
	p, ok := a.TilePrincipal("apps/a", "ana", "")
	if !ok || p.Component != "apps/a" || p.UserID != "ana" || p.Via != "frame" || p.Access == nil || p.ReadOnly() {
		t.Fatalf("tile principal: %+v %v", p, ok)
	}
	if p, _ := a.TilePrincipal("apps/a", "ana", "boss"); !p.ReadOnly() {
		t.Fatal("a view-as session's tile principal is not read-only")
	}
	if _, ok := a.TilePrincipal("apps/a", "ghost", ""); ok {
		t.Fatal("principal for an unknown user")
	}
}
