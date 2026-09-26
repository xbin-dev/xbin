package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
)

// Strict tile asset gating (plans/tile-asset-auth.md) adds two credentials
// that authenticate nothing but the /c/ static plane:
//
//   - the ASSET TOKEN (mechanism B, --tile-assets=tokens): minted into a tile
//     document's injected <base href="/c/~<tok>/…">, it lets the document's
//     relative subresource loads through. It authorizes only non-document
//     static files of tiles its user can read (checked live on every
//     request), never /api, never HTML, and it never mints a frame token.
//   - the TILE-ORIGIN COOKIE (mechanism A, --tile-assets=origins): set on a
//     tile's own origin (t-<id>.<tiles-domain>) in exchange for a frame
//     token, it makes that origin act as the tile's frame principal.
//
// Both are HMACs under the workspace secret with a PURPOSE TAG in the MAC
// input, so neither can be replayed as the other or as a frame token (whose
// MAC input carries no tag and whose shape — four '|' fields — they don't
// have). Both bind (tile, user, credential generation, expiry).
const (
	assetTokenPurpose = "xbin-asset-token-v1"
	tileCookiePurpose = "xbin-tile-origin-v1"
	tileHostPurpose   = "xbin-tile-host-v1"
	ownerGenPurpose   = "xbin-owner-gen-v1"

	assetTokenPrefix = "a1"
	tileCookiePrefix = "c1"

	// TileCookieName is the tile-origin cookie (host-only on the tile's own
	// origin; HttpOnly, SameSite=Strict — never on the workspace origin).
	TileCookieName = "xbin_tile"

	// AssetTokenTTL bounds an asset token. A document keeps the token it was
	// served with for as long as it stays open (module- and stylesheet-
	// relative URLs keep resolving against it), so it is long; everything
	// that matters is checked live per request anyway: the user exists and
	// is enabled, the credential generation still matches, and the user can
	// read both the minting tile and the tile being loaded.
	AssetTokenTTL = 7 * 24 * time.Hour
	// TileCookieTTL bounds a tile-origin cookie; it slides (the server
	// re-issues it once half has elapsed) while the tile stays in use.
	TileCookieTTL = 12 * time.Hour
)

// AssetGrant is what a verified asset token or tile-origin cookie attributes.
type AssetGrant struct {
	Tile   string // asset token: the tile whose document minted it; cookie: the origin's tile
	UserID string // "" = the owner principal (bootstrap token / --no-auth)
	Gen    string // the credential generation it was minted under
	Exp    time.Time
}

// SetCredentialGeneration installs the per-user credential generation that
// asset tokens and tile-origin cookies are bound to: a value that changes
// when the user's sessions are revoked (sign-out everywhere, a password
// change, a device revoked), killing every credential minted under the old
// value on its next use.
//
// TODO(device-auth, plans/native.md §20 item 2): the device-auth work
// package adds per-user/session generations for frame tokens; wire the same
// counter in here (boot: st.Auth.SetCredentialGeneration(...)). Until then
// the hook is unset, user credentials carry generation "" and revocation
// relies on the live checks (user deleted/disabled, RBAC) and the TTLs.
func (a *Auth) SetCredentialGeneration(f func(userID string) string) { a.credGen = f }

// credGeneration is the current generation for uid ("" = the owner, whose
// generation is derived from the owner token so a rotation kills every
// owner-minted asset credential).
func (a *Auth) credGeneration(uid string) string {
	if uid == "" {
		return a.mac(ownerGenPurpose, a.OwnerTokenValue())[:8]
	}
	if a.credGen == nil {
		return ""
	}
	return a.credGen(uid)
}

// mac is the purpose-tagged HMAC used by the asset-plane credentials:
// base64url of the first 16 bytes of HMAC-SHA256(secret, purpose ‖ 0 ‖ msg).
func (a *Auth) mac(purpose, msg string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

// mintGrant encodes prefix.b64(tile).b64(user).exp.b64(gen).mac — every
// field URL-path safe (base64url has no '.' or '/'), so an asset token can
// sit in a path segment.
func (a *Auth) mintGrant(prefix, purpose, tile, uid string, ttl time.Duration) string {
	enc := base64.RawURLEncoding.EncodeToString
	payload := strings.Join([]string{prefix, enc([]byte(tile)), enc([]byte(uid)),
		strconv.FormatInt(time.Now().Add(ttl).Unix(), 10), enc([]byte(a.credGeneration(uid)))}, ".")
	return payload + "." + a.mac(purpose, payload)
}

// verifyGrant checks shape, MAC and expiry — NOT liveness (grantLive).
func (a *Auth) verifyGrant(prefix, purpose, tok string) (AssetGrant, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 6 || parts[0] != prefix {
		return AssetGrant{}, false
	}
	payload := strings.Join(parts[:5], ".")
	if !hmac.Equal([]byte(a.mac(purpose, payload)), []byte(parts[5])) {
		return AssetGrant{}, false
	}
	exp, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return AssetGrant{}, false
	}
	dec := base64.RawURLEncoding.DecodeString
	tile, err1 := dec(parts[1])
	uid, err2 := dec(parts[2])
	gen, err3 := dec(parts[4])
	if err1 != nil || err2 != nil || err3 != nil || len(tile) == 0 {
		return AssetGrant{}, false
	}
	return AssetGrant{Tile: string(tile), UserID: string(uid), Gen: string(gen), Exp: time.Unix(exp, 0)}, true
}

// grantLive: the credential's user still authenticates (exists, enabled) and
// its generation is current. The owner's generation tracks the owner token.
func (a *Auth) grantLive(g AssetGrant) bool {
	if a.noAuth {
		return true
	}
	if g.UserID != "" {
		if _, ok := a.userSnapshot(g.UserID); !ok {
			return false
		}
	}
	return subtleEqual(g.Gen, a.credGeneration(g.UserID))
}

// MintAssetToken mints the asset token a tile document's <base> carries.
func (a *Auth) MintAssetToken(tile, userID string) string {
	return a.mintGrant(assetTokenPrefix, assetTokenPurpose, tile, userID, AssetTokenTTL)
}

// VerifyAssetToken returns the grant of a valid, unexpired, live asset
// token. The caller still checks RBAC (UserCanReadTile) for both the grant's
// tile and the tile being loaded, on every request.
func (a *Auth) VerifyAssetToken(tok string) (AssetGrant, bool) {
	g, ok := a.verifyGrant(assetTokenPrefix, assetTokenPurpose, tok)
	if !ok || !a.grantLive(g) {
		return AssetGrant{}, false
	}
	return g, true
}

// MintTileCookie mints the value of a tile origin's cookie.
func (a *Auth) MintTileCookie(tile, userID string) string {
	return a.mintGrant(tileCookiePrefix, tileCookiePurpose, tile, userID, TileCookieTTL)
}

// VerifyTileCookie returns the grant of a valid, unexpired, live tile-origin
// cookie. The caller checks that its tile is the origin's tile and that the
// user may still read it.
func (a *Auth) VerifyTileCookie(v string) (AssetGrant, bool) {
	g, ok := a.verifyGrant(tileCookiePrefix, tileCookiePurpose, v)
	if !ok || !a.grantLive(g) {
		return AssetGrant{}, false
	}
	return g, true
}

// TileHostID is a tile's origin label under --tile-assets=origins:
// "t-" + 16 base32 chars of a keyed hash of the tile path. Stable for the
// workspace (the key is .xbin/secret), non-reversible — tile names never
// reach DNS, SNI or certificate logs — and a valid DNS label.
func (a *Auth) TileHostID(tile string) string {
	m := hmac.New(sha256.New, a.secret)
	m.Write([]byte(tileHostPurpose))
	m.Write([]byte{0})
	m.Write([]byte(tile))
	return "t-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(m.Sum(nil)[:10]))
}

// UserCanReadTile is the live per-user read check the strict asset plane
// uses — the USER's access, never an element principal's self-pass: the
// owner (uid "") and --no-auth read everything; a user reads what their
// (org/team-aware) access resolves to, admins everything; a deleted or
// disabled user nothing.
func (a *Auth) UserCanReadTile(uid, tile string) bool {
	if a.noAuth || uid == "" {
		return true
	}
	u, ok := a.userSnapshot(uid)
	if !ok {
		return false
	}
	if acc := a.accessSnapshot(uid); acc != nil {
		return acc.CanReadTile(tile)
	}
	return u.CanReadTile(tile)
}

// TilePrincipal is the frame principal of (tile, user) — exactly what a
// frame token for that pair resolves to — for the tile-origin cookie, which
// makes /api and /ws on a tile's origin act as the tile.
func (a *Auth) TilePrincipal(tile, uid string) (Principal, bool) {
	p := Principal{Component: tile, UserID: uid, Via: "frame"}
	if uid != "" {
		if _, found := a.userSnapshot(uid); !found {
			return Principal{}, false
		}
		p.Access = a.accessSnapshot(uid)
	}
	return p, true
}

// FramePrincipal resolves a frame token to its principal (the tile-origin
// exchange verifies the bootstrap token with it).
func (a *Auth) FramePrincipal(tok string) (Principal, bool) { return a.framePrincipal(tok) }
