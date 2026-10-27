package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// The app tunnel's transport gate (D204, plans/app-tunnel.md §4): every
// credential × every transport, iroh-only off and on, for the credential
// itself and for what is derived from it (a frame token, an asset token, a
// path ticket).

type gateFixture struct {
	a        *Auth
	required bool
	creds    map[string]cred
}

type cred struct {
	set   func(*http.Request) // puts the credential on a request
	gen   string
	user  string
	frame string // a frame token minted under it ("" for none)
}

const (
	devA = "dev-a" // ann's phone: the tunnel under test is its
	devB = "dev-b" // ann's tablet
)

func newGateFixture(t *testing.T) *gateFixture {
	t.Helper()
	ws := t.TempDir()
	a, err := Load(ws, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ann", "bob"} {
		if _, err := st.Upsert(users.User{ID: id, Tiles: map[string]string{"apps/x": users.LevelRead}}, "password123"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	f := &gateFixture{a: a, creds: map[string]cred{}}
	a.SetTunnelPolicy(func() bool { return f.required })
	bearer := func(name, uid string, o BearerOpts) {
		bs := a.NewBearerSessionOpts(uid, o)
		p, ok := a.bearerSessionPrincipal(bs.Token, "")
		if !ok {
			t.Fatal(name)
		}
		f.creds[name] = cred{set: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+bs.Token) },
			gen: p.Gen, user: uid, frame: a.MintFrameTokenFor(p, "apps/x", time.Minute)}
	}
	cookie := func(name, sid string) {
		sv, ok := a.sessionUser(sid, "", false)
		if !ok {
			t.Fatal(name)
		}
		p := Principal{UserID: sv.userID, Via: "session", Gen: sv.gen}
		f.creds[name] = cred{set: func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: sid}) },
			gen: sv.gen, user: sv.userID, frame: a.MintFrameTokenFor(p, "apps/x", time.Minute)}
	}
	cookie("browser", a.NewSession("ann", ""))
	bearer("app (no device)", "ann", BearerOpts{})
	bearer("device A, direct", "ann", BearerOpts{DeviceID: devA})
	bearer("device A, tunnel", "ann", BearerOpts{DeviceID: devA, Tunnel: true})
	bearer("device B, tunnel", "ann", BearerOpts{DeviceID: devB, Tunnel: true})
	bearer("bob's device, tunnel", "bob", BearerOpts{DeviceID: "dev-bob", Tunnel: true})
	// web-ticket cookies: opened by device A's sessions, on either transport
	for name, parent := range map[string]string{"web of A, direct": "device A, direct", "web of A, tunnel": "device A, tunnel"} {
		h := f.creds[parent].gen[2:]
		sid, err := a.OpenWebSessionOn(WebTicket{UserID: "ann", DeviceID: devA, handle: h}, "", time.Time{}, name == "web of A, tunnel")
		if err != nil {
			t.Fatal(name, err)
		}
		cookie(name, sid)
	}
	owner := a.OwnerTokenValue()
	f.creds["owner token"] = cred{set: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+owner) },
		gen: a.ownerGen(), frame: a.MintFrameToken("apps/x", "", time.Minute)}
	ugen := a.UserGen("ann")
	f.creds["ann's u. frame"] = cred{gen: ugen, user: "ann", frame: a.MintFrameToken("apps/x", "ann", time.Minute)}
	f.creds["bob's u. frame"] = cred{gen: a.UserGen("bob"), user: "bob", frame: a.MintFrameToken("apps/x", "bob", time.Minute)}
	term := a.MintTerminal("apps/x", "ann")
	f.creds["terminal token"] = cred{set: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+term) }}
	a.RegisterInstance("inst-tok", "apps/x")
	f.creds["instance token"] = cred{set: func(r *http.Request) { r.Header.Set("Authorization", "Bearer inst-tok") }}
	return f
}

var (
	direct   = Transport{}
	tunnelA  = Transport{Tunnel: true, DeviceID: devA, UserID: "ann"}
	tunnelB  = Transport{Tunnel: true, DeviceID: devB, UserID: "ann"}
	tunnelBo = Transport{Tunnel: true, DeviceID: "dev-bob", UserID: "bob"}
)

// matrix rows: credential → want on [direct, direct+iroh-only, tunnel A, tunnel B, bob's tunnel]
var gateMatrix = map[string][5]Refusal{
	"browser":              {"", "", "device", "device", "device"},
	"app (no device)":      {"", "required", "device", "device", "device"},
	"device A, direct":     {"", "required", "", "device", "device"},
	"device A, tunnel":     {"bound", "bound", "", "device", "device"},
	"device B, tunnel":     {"bound", "bound", "device", "", "device"},
	"bob's device, tunnel": {"bound", "bound", "device", "device", ""},
	"web of A, direct":     {"", "required", "", "device", "device"},
	"web of A, tunnel":     {"bound", "bound", "", "device", "device"},
	"owner token":          {"", "", "device", "device", "device"},
	"ann's u. frame":       {"", "", "", "", "device"},
	"bob's u. frame":       {"", "", "device", "device", ""},
	"terminal token":       {"", "", "device", "device", "device"},
	"instance token":       {"", "", "device", "device", "device"},
}

func (f *gateFixture) request(t Transport, c cred, frameOnly bool) *http.Request {
	r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
	if frameOnly {
		r.Header.Set(FrameTokenHeader, c.frame)
	} else if c.set != nil {
		c.set(r)
	}
	return r.WithContext(WithTransport(r.Context(), t))
}

func TestTransportGateMatrix(t *testing.T) {
	f := newGateFixture(t)
	transports := []Transport{direct, direct, tunnelA, tunnelB, tunnelBo}
	for name, want := range gateMatrix {
		c := f.creds[name]
		for i, tr := range transports {
			f.required = i == 1
			// the credential itself, through FromRequest
			if c.set != nil {
				_, ok, why := f.a.Authenticate(f.request(tr, c, false))
				if why != want[i] || ok != (want[i] == "") {
					t.Errorf("%s on %d: got ok=%v %q, want %q", name, i, ok, why, want[i])
				}
			}
			// a frame token derived from it
			if c.frame != "" {
				_, ok, why := f.a.Authenticate(f.request(tr, c, true))
				if why != want[i] || ok != (want[i] == "") {
					t.Errorf("%s's frame token on %d: got ok=%v %q, want %q", name, i, ok, why, want[i])
				}
			}
			// an asset token or path ticket carries the same generation
			if c.gen != "" {
				if got := f.a.GenRefusal(tr, c.gen, c.user); got != want[i] {
					t.Errorf("%s's generation on %d: %q, want %q", name, i, got, want[i])
				}
			}
		}
	}
	f.required = false
}

// Derived credentials: an asset token and a path ticket minted under a
// tunnel-bound session carry its generation, and so its binding.
func TestTransportGateDerived(t *testing.T) {
	f := newGateFixture(t)
	c := f.creds["device A, tunnel"]
	p := Principal{UserID: "ann", Via: "device", DeviceID: devA, Gen: c.gen}
	g, ok := f.a.VerifyAssetToken(f.a.MintAssetTokenFor(p, "apps/x"))
	if !ok {
		t.Fatal("asset token")
	}
	if f.a.GenRefusal(direct, g.Gen, g.UserID) != RefuseBound || f.a.GenRefusal(tunnelA, g.Gen, g.UserID) != RefuseNone {
		t.Fatal("asset token binding")
	}
	fp := Principal{Component: "apps/x", UserID: "ann", Via: "frame", Gen: c.gen}
	tok, _, err := f.a.MintPathTicket(fp, "files")
	if err != nil {
		t.Fatal(err)
	}
	pp, _, ok := f.a.VerifyPathTicket(tok)
	if !ok || f.a.GenRefusal(direct, pp.Gen, pp.UserID) != RefuseBound || f.a.GenRefusal(tunnelB, pp.Gen, pp.UserID) != RefuseDevice {
		t.Fatalf("path ticket binding: %+v %v", pp, ok)
	}
}

// A restart keeps the binding: the bound login's orphan (framegens.go)
// still refuses its frame tokens off the tunnel.
func TestTransportGateOrphan(t *testing.T) {
	ws := t.TempDir()
	a, err := Load(ws, false)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := users.Open(t.TempDir())
	st.Upsert(users.User{ID: "ann"}, "password123")
	a.SetUsers(st)
	bs := a.NewBearerSessionOpts("ann", BearerOpts{DeviceID: devA, Tunnel: true})
	p, _ := a.bearerSessionPrincipal(bs.Token, "")
	ft := a.MintFrameTokenFor(p, "apps/x", time.Hour)
	a.FlushGens()
	a2, err := Load(ws, false)
	if err != nil {
		t.Fatal(err)
	}
	a2.SetUsers(st)
	req := func(tr Transport) *http.Request {
		r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
		r.Header.Set(FrameTokenHeader, ft)
		return r.WithContext(WithTransport(context.Background(), tr))
	}
	if _, ok, why := a2.Authenticate(req(direct)); ok || why != RefuseBound {
		t.Fatalf("orphan off the tunnel: %v %q", ok, why)
	}
	if _, ok, _ := a2.Authenticate(req(tunnelA)); !ok {
		t.Fatal("orphan on its tunnel")
	}
	if _, err := filepath.Abs(ws); err != nil {
		t.Fatal(err)
	}
}

// Tunnel codes: never redeemed over POST /devices/enroll (and not burnt by
// the attempt), spent by the pairing for their own user only, voided by
// sign-out-everywhere.
func TestTunnelEnrollCodes(t *testing.T) {
	a, _, _ := webTicketAuth(t)
	code, _, err := a.MintEnrollCodeTunnel("ann", "https://x.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := a.RedeemEnrollCode(code); ok {
		t.Fatal("a tunnel code redeemed over /devices/enroll")
	}
	if a.ConsumeTunnelCode(code, "bob") {
		t.Fatal("another user spent it")
	}
	if !a.ConsumeTunnelCode(code, "ann") || a.ConsumeTunnelCode(code, "ann") {
		t.Fatal("spend once")
	}
	classic, _, _ := a.MintEnrollCode("ann", "https://x.example")
	if a.ConsumeTunnelCode(classic, "ann") {
		t.Fatal("a classic code spent as a tunnel code")
	}
	code2, _, _ := a.MintEnrollCodeTunnel("ann", "https://x.example")
	a.DropUserSessions("ann")
	if a.ConsumeTunnelCode(code2, "ann") {
		t.Fatal("sign-out-everywhere left a tunnel code pending")
	}
}

// The warm set takes a tunnel's per-device key, never a bare "tunnel".
func TestWarmTunnelKeys(t *testing.T) {
	a, _, _ := webTicketAuth(t)
	a.mu.Lock()
	a.warmLocked(tunnelA.ClientKey(), time.Now())
	a.warmLocked("tunnel/", time.Now())
	a.warmLocked("tunnel", time.Now())
	a.mu.Unlock()
	if !a.RecentlyAuthed("tunnel/dev-a") || a.RecentlyAuthed("tunnel/dev-b") || a.RecentlyAuthed("tunnel/") || a.RecentlyAuthed("tunnel") {
		t.Fatal("warm keys")
	}
}

// A tunnel request is never a secure context: no Secure / __Host- cookies.
func TestTunnelNotSecure(t *testing.T) {
	r := httptest.NewRequest("GET", "http://localhost:8080/", nil)
	if !SecureRequest(r) {
		t.Fatal("localhost is secure off the tunnel")
	}
	if SecureRequest(r.WithContext(WithTransport(r.Context(), tunnelA))) {
		t.Fatal("a tunnel request is secure")
	}
}
