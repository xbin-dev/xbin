package auth

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/magik6k/xbin/internal/users"
)

func TestTerminalTokens(t *testing.T) {
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	store, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(store)
	if _, err := store.Upsert(users.User{ID: "alice", Role: users.RoleAdmin, Terminal: true}, "pw"); err != nil {
		t.Fatal(err)
	}

	as := func(tok string) (Principal, bool) {
		r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		return a.FromRequest(r)
	}

	// A tile terminal token is the tile's ELEMENT principal — never admin,
	// even though alice is an admin (min(user, tile) = tile).
	tok := a.MintTerminal("apps/foo", "alice")
	p, ok := as(tok)
	if !ok || p.Component != "apps/foo" || p.Via != "terminal" || p.UserID != "alice" {
		t.Fatalf("terminal principal: %+v ok=%v", p, ok)
	}
	if p.IsAdmin() {
		t.Fatal("a tile terminal token must NOT be admin (the whole point)")
	}
	if p.Owner {
		t.Fatal("terminal token must not be the owner")
	}

	// Deleting the user kills the live shell's API access.
	if err := store.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, ok := as(tok); ok {
		t.Fatal("token naming a deleted user must be rejected")
	}

	// A bootstrap-token session's terminal (no user) still resolves.
	tok2 := a.MintTerminal("apps/bar", "")
	if p, ok := as(tok2); !ok || p.Component != "apps/bar" || p.IsAdmin() {
		t.Fatalf("ownerless terminal token: %+v ok=%v", p, ok)
	}

	// Revocation (session death) kills it.
	a.RevokeTerminal(tok2)
	if _, ok := as(tok2); ok {
		t.Fatal("revoked terminal token must be rejected")
	}

	// A random bearer is still rejected.
	if _, ok := as("nonsense"); ok {
		t.Fatal("garbage bearer accepted")
	}
}

// In --no-auth dev mode terminal tokens still resolve to the element principal
// (not the owner fallback), so dev exercises the same RBAC as production —
// exactly like instance tokens.
func TestTerminalTokensNoAuth(t *testing.T) {
	a, err := Load(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	tok := a.MintTerminal("apps/foo", "")
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	p, ok := a.FromRequest(r)
	if !ok || p.Component != "apps/foo" || p.Owner {
		t.Fatalf("noauth terminal principal: %+v ok=%v", p, ok)
	}
}

// Rotating the owner token invalidates the old one everywhere (bearer +
// cookie) the moment it returns, and rewrites .xbin/token.
func TestRotateOwnerToken(t *testing.T) {
	dir := t.TempDir()
	a, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	old := a.OwnerTokenValue()
	asBearer := func(tok string) bool {
		r := httptest.NewRequest("GET", "/x", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		p, ok := a.FromRequest(r)
		return ok && p.Owner
	}
	if !asBearer(old) {
		t.Fatal("initial owner token must authenticate")
	}
	fresh, err := a.RotateOwnerToken()
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old {
		t.Fatal("rotation must change the token")
	}
	if asBearer(old) {
		t.Fatal("OLD token still authenticates after rotation — the whole point is that leaked copies die")
	}
	if !asBearer(fresh) {
		t.Fatal("new token must authenticate")
	}
	// The file is the source of truth for host-side bx — rewritten.
	b, err := os.ReadFile(filepath.Join(dir, ".xbin", "token"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(b)); got != fresh {
		t.Fatalf(".xbin/token = %q, want the rotated token", got)
	}
	// A restart loads the rotated token.
	a2, err := Load(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if a2.OwnerTokenValue() != fresh {
		t.Fatal("reload must pick up the rotated token")
	}
}

// Sessions expire: after the idle window without activity, or the absolute cap
// regardless of activity — so a stolen cookie can't authenticate forever
// (a live lookup slides the idle window).
func TestSessionExpiry(t *testing.T) {
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	a.sessionIdleTTL = 30 * time.Minute
	a.sessionAbsTTL = 2 * time.Hour

	id := a.NewSession("alice")
	if uid, ok := a.sessionUser(id); !ok || uid != "alice" {
		t.Fatal("fresh session must resolve")
	}

	// Idle past the window → dead.
	a.mu.Lock()
	a.sessions[id].lastActive = time.Now().Add(-31 * time.Minute)
	a.mu.Unlock()
	if _, ok := a.sessionUser(id); ok {
		t.Fatal("idle-expired session must not resolve")
	}
	if _, ok := a.sessions[id]; ok {
		t.Fatal("idle-expired session must be evicted")
	}

	// Active but past the absolute cap → dead even though lastActive is recent.
	id2 := a.NewSession("bob")
	a.mu.Lock()
	a.sessions[id2].created = time.Now().Add(-3 * time.Hour)
	a.sessions[id2].lastActive = time.Now()
	a.mu.Unlock()
	if _, ok := a.sessionUser(id2); ok {
		t.Fatal("absolute-expired session must not resolve")
	}

	// A lookup within the idle window slides it (stays alive across a gap that
	// would have expired from the original login).
	id3 := a.NewSession("carol")
	a.mu.Lock()
	a.sessions[id3].lastActive = time.Now().Add(-20 * time.Minute) // still inside 30m
	a.mu.Unlock()
	if _, ok := a.sessionUser(id3); !ok {
		t.Fatal("session inside idle window must survive")
	}
	a.mu.Lock()
	slid := time.Since(a.sessions[id3].lastActive) < time.Minute
	a.mu.Unlock()
	if !slid {
		t.Fatal("a live lookup must slide lastActive")
	}
}
