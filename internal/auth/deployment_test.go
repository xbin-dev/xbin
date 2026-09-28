package auth

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// zsAuth is the zero-state token fixture: an admin, a user writing
// apps/x, and nothing else.
func zsAuth(t *testing.T) *Auth {
	t.Helper()
	a, err := Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleAdmin},
		{ID: "bob", Tiles: map[string]string{"apps/x": users.LevelWrite, "notes+ideas": users.LevelRead}},
	} {
		if _, err := st.Upsert(u, "pw"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	return a
}

// zsGenMask masks the run-to-run part of a credential generation, keeping
// its form: a session handle, a user generation (its counter kept), the
// owner token's.
var zsGenMask = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`^s\.[0-9a-f]{24}$`), "s.<handle>"},
	{regexp.MustCompile(`^u\.[0-9a-f]{8}\.(\d+)$`), "u.<epoch>.$1"},
	{regexp.MustCompile(`^o\.[0-9a-f]{16}$`), "o.<owner>"},
}

func zsMaskGen(g string) string {
	for _, m := range zsGenMask {
		if m.re.MatchString(g) {
			return m.re.ReplaceAllString(g, m.with)
		}
	}
	return g // unmasked: a form the golden doesn't know shows as itself
}

// zsMaskFrameToken renders a frame token with its run-to-run values
// masked, after checking them: the expiry is a unix time about ttl from
// now, the signature is the HMAC of the fields before it.
func zsMaskFrameToken(t *testing.T, a *Auth, tok string, ttl time.Duration) string {
	t.Helper()
	parts := strings.Split(tok, "|")
	if len(parts) < 2 {
		return tok
	}
	body, sig := strings.Join(parts[:len(parts)-1], "|"), parts[len(parts)-1]
	if a.sign(body) != sig {
		t.Errorf("token %q: the last field is not the HMAC of the others", tok)
	}
	if len(parts) >= 3 {
		exp, err := strconv.ParseInt(parts[2], 10, 64)
		if want := time.Now().Add(ttl).Unix(); err != nil || exp < want-60 || exp > want+60 {
			t.Errorf("token %q: expiry %q is not now+%s", tok, parts[2], ttl)
		}
		parts[2] = "<exp>"
	}
	if len(parts) >= 5 {
		parts[3] = zsMaskGen(parts[3])
	}
	parts[len(parts)-1] = "<hmac>"
	return strings.Join(parts, "|")
}

// zsPrincipal renders every field a Principal carries today (pointers as
// set/nil, the generation masked).
func zsPrincipal(p Principal) string {
	return fmt.Sprintf("owner=%v user=%q comp=%q via=%q gen=%s role=%q imp=%q device=%q userSnap=%v access=%v",
		p.Owner, p.UserID, p.Component, p.Via, zsMaskGen(p.Gen), p.Role, p.Impersonator, p.DeviceID, p.User != nil, p.Access != nil)
}

func zsResolve(a *Auth, set func(r *http.Request)) string {
	r := httptest.NewRequest("GET", "/api/xbin/whoami", nil)
	set(r)
	p, ok := a.FromRequest(r)
	if !ok {
		return "refused"
	}
	return zsPrincipal(p)
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// covers PO-6 Z5 SC-ZERO — frame tokens keep today's five-field wire format
// with a bare tile path in field 0, bound to today's three generation forms;
// a sixth field never spells main (main keeps its one, five-field spelling;
// the claim of another deployment is TestFrameTokenDeploymentClaim's);
// instance and terminal tokens resolve to today's element principals. Hand-maintained goldens: changing one is a compat change
// (12-compat.md), never a regeneration.
func TestZeroStateTokens(t *testing.T) {
	a := zsAuth(t)
	const ttl = 15 * time.Minute
	sid := a.NewSession("bob", "192.0.2.1")
	bob, ok := a.FromRequest(func() *http.Request {
		r := httptest.NewRequest("GET", "/x", nil)
		r.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
		return r
	}())
	if !ok {
		t.Fatal("bob's session refused")
	}

	// Minting: the wire format, field by field.
	mints := []struct {
		name, tok, want string
	}{
		{"session-bound", a.MintFrameTokenFor(bob, "apps/x", ttl),
			b64("apps/x") + "|" + b64("bob") + "|<exp>|s.<handle>|<hmac>"},
		{"session-bound, a nested page", a.MintFrameTokenFor(bob, "apps/x/editor", ttl),
			b64("apps/x/editor") + "|" + b64("bob") + "|<exp>|s.<handle>|<hmac>"},
		{"session-bound, a tile whose own path holds +", a.MintFrameTokenFor(bob, "notes+ideas", ttl),
			b64("notes+ideas") + "|" + b64("bob") + "|<exp>|s.<handle>|<hmac>"},
		{"user-bound (no session behind it)", a.MintFrameToken("apps/x", "bob", ttl),
			b64("apps/x") + "|" + b64("bob") + "|<exp>|u.<epoch>.0|<hmac>"},
		{"owner-bound", a.MintFrameToken("apps/x", "", ttl),
			b64("apps/x") + "||<exp>|o.<owner>|<hmac>"},
	}
	for _, m := range mints {
		if got := zsMaskFrameToken(t, a, m.tok, ttl); got != m.want {
			t.Errorf("%s: minted\n  %s\nwant\n  %s", m.name, got, m.want)
		}
		if n := strings.Count(m.tok, "|") + 1; n != 5 {
			t.Errorf("%s: %d fields, want 5", m.name, n)
		}
	}
	if b64("apps/x") != "YXBwcy94" || b64("bob") != "Ym9i" {
		t.Fatal("base64url is not the field encoding the goldens assume")
	}

	// Resolving: what each credential authenticates as.
	frame := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set(FrameTokenHeader, tok) }
	}
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}
	inst := "inst-" + strings.Repeat("7", 40)
	a.RegisterInstance(inst, "apps/x")
	term := a.MintTerminal("apps/x", "bob")
	if !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(term) {
		t.Errorf("terminal token %q is not 24 random bytes in hex", term)
	}
	ownerTerm := a.MintTerminal("notes+ideas", "")
	body := b64("apps/x") + "|" + b64("bob") + "|" + strconv.FormatInt(time.Now().Add(ttl).Unix(), 10) + "|" + bob.Gen
	sixth := body + "|" + b64("main")
	legacy := b64("apps/x") + "|" + b64("bob") + "|" + strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	resolves := []struct {
		name string
		set  func(*http.Request)
		want string
	}{
		{"frame token alone (session-bound)", frame(mints[0].tok),
			`owner=false user="bob" comp="apps/x" via="frame" gen=s.<handle> role="" imp="" device="" userSnap=false access=true`},
		{"frame token, a + path", frame(mints[2].tok),
			`owner=false user="bob" comp="notes+ideas" via="frame" gen=s.<handle> role="" imp="" device="" userSnap=false access=true`},
		{"frame token alone (user-bound)", frame(mints[3].tok),
			`owner=false user="bob" comp="apps/x" via="frame" gen=u.<epoch>.0 role="" imp="" device="" userSnap=false access=true`},
		{"frame token alone (owner-bound)", frame(mints[4].tok),
			`owner=false user="" comp="apps/x" via="frame" gen=o.<owner> role="" imp="" device="" userSnap=false access=false`},
		{"frame token on its session's cookie", func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: sid})
			r.Header.Set(FrameTokenHeader, mints[0].tok)
		}, `owner=false user="bob" comp="apps/x" via="frame" gen=s.<handle> role="" imp="" device="" userSnap=false access=true`},
		{"frame token in ?frame= (WebSockets)", func(r *http.Request) {
			r.URL.RawQuery = "frame=" + strings.ReplaceAll(mints[0].tok, "|", "%7C")
		}, `owner=false user="bob" comp="apps/x" via="frame" gen=s.<handle> role="" imp="" device="" userSnap=false access=true`},
		{"a sixth field naming main, correctly signed", frame(sixth + "|" + a.sign(sixth)), "refused"},
		{"the legacy four-field form (minted before binding)", frame(legacy + "|" + a.sign(legacy)),
			`owner=false user="bob" comp="apps/x" via="frame" gen= role="" imp="" device="" userSnap=false access=true`},
		{"instance token", bearer(inst),
			`owner=false user="" comp="apps/x" via="instance" gen= role="" imp="" device="" userSnap=false access=false`},
		{"terminal token", bearer(term),
			`owner=false user="bob" comp="apps/x" via="terminal" gen= role="" imp="" device="" userSnap=false access=true`},
		{"terminal token, owner-opened, a + path", bearer(ownerTerm),
			`owner=false user="" comp="notes+ideas" via="terminal" gen= role="" imp="" device="" userSnap=false access=false`},
		{"session cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: CookieName, Value: sid}) },
			`owner=false user="bob" comp="" via="session" gen=s.<handle> role="" imp="" device="" userSnap=true access=true`},
		{"owner bearer", bearer(a.OwnerTokenValue()),
			`owner=true user="" comp="" via="bearer" gen=o.<owner> role="" imp="" device="" userSnap=false access=false`},
	}
	for _, c := range resolves {
		if got := zsResolve(a, c.set); got != c.want {
			t.Errorf("%s:\n  got  %s\n  want %s", c.name, got, c.want)
		}
	}

	// Renewal keeps the five fields and the binding.
	fp, ok := a.framePrincipal(mints[0].tok)
	if !ok {
		t.Fatal("frame token refused")
	}
	if got, want := zsMaskFrameToken(t, a, a.MintFrameTokenFor(fp, fp.Component, ttl), ttl), mints[0].want; got != want {
		t.Errorf("renewal: %s, want %s", got, want)
	}

	// The instance map and terminal tokens stay in-memory maps: revoking
	// drops the credential, nothing else answers for it.
	a.RevokeInstance(inst)
	a.RevokeTerminal(term)
	for _, tok := range []string{inst, term} {
		if got := zsResolve(a, bearer(tok)); got != "refused" {
			t.Errorf("a revoked token still resolves: %s", got)
		}
	}
}

// --- the deployment a tile principal is bound to (deployment.go) ---

// dlAuth is zsAuth's fixture with owner auth on or off (--no-auth still
// resolves element credentials).
func dlAuth(t *testing.T, noAuth bool) *Auth {
	t.Helper()
	a := zsAuth(t)
	a.noAuth = noAuth
	return a
}

// dlResolve authenticates a request carrying set's credential, plus what a
// caller might add to steer its deployment: an X-XBin-Deployment header
// (xbind strips inbound X-XBin-*, but auth must not read one regardless)
// and a deployment parameter.
func dlResolve(a *Auth, set func(r *http.Request)) (Principal, bool) {
	r := httptest.NewRequest("GET", "/api/apps/x/?deployment=steered", nil)
	r.Header.Set("X-XBin-Deployment", "steered")
	set(r)
	return a.FromRequest(r)
}

func dlBearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func dlFrame(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(FrameTokenHeader, tok) }
}

// covers D127f D127j PO-6 — a principal's deployment defaults to main. The zero
// Principal names none, and every credential of a tile without a deployment
// record (frame tokens of four and five fields, instance and terminal tokens
// from today's calls, sessions, the owner token, --no-auth) resolves to
// today's principal, Deployment "" included: equal, field for field, to the
// literal the pre-deployment code built, so whoami, which renders the
// principal, answers as it did. There is no deployment principal: From()
// stays the tile path whatever the binding, and no Via names a deployment.
func TestPrincipalDeploymentZeroValue(t *testing.T) {
	if (Principal{}).Deployment != "" {
		t.Fatal("the zero Principal names a deployment")
	}
	for _, noAuth := range []bool{false, true} {
		a := dlAuth(t, noAuth)
		inst := "inst-" + strings.Repeat("0", 40)
		a.RegisterInstance(inst, "apps/x")
		ownerTerm := a.MintTerminal("apps/x", "")
		exact := []struct {
			name string
			set  func(*http.Request)
			want Principal
		}{
			{"instance token (RegisterInstance)", dlBearer(inst), Principal{Component: "apps/x", Via: "instance"}},
			{"terminal token (MintTerminal), owner-opened", dlBearer(ownerTerm), Principal{Component: "apps/x", Via: "terminal"}},
			{"frame token, owner-bound", dlFrame(a.MintFrameToken("apps/x", "", time.Minute)),
				Principal{Component: "apps/x", Via: "frame", Gen: a.ownerGen()}},
		}
		for _, c := range exact {
			if p, ok := dlResolve(a, c.set); !ok || p != c.want {
				t.Errorf("noAuth=%v %s: %+v %v, want %+v", noAuth, c.name, p, ok, c.want)
			}
		}

		sid := a.NewSession("bob", "192.0.2.1")
		bob := principalOf(t, a, sid, "")
		attributed := map[string]func(*http.Request){
			"frame token (session-bound)":   dlFrame(a.MintFrameTokenFor(bob, "apps/x", time.Minute)),
			"frame token (user-bound)":      dlFrame(a.MintFrameToken("apps/x", "bob", time.Minute)),
			"legacy four-field frame token": dlFrame(legacyFrameToken(a, "apps/x", "bob", time.Now().Add(time.Minute))),
			"terminal token (MintTerminal)": dlBearer(a.MintTerminal("apps/x", "bob")),
			"session cookie":                func(r *http.Request) { r.AddCookie(cookieFor(sid)) },
			"owner bearer":                  dlBearer(a.OwnerTokenValue()),
		}
		for name, set := range attributed {
			p, ok := dlResolve(a, set)
			if !ok || p.Deployment != "" {
				t.Errorf("noAuth=%v %s: %+v %v, want Deployment \"\"", noAuth, name, p, ok)
			}
		}
		if noAuth {
			if p, ok := dlResolve(a, func(*http.Request) {}); !ok || p.Deployment != "" || p.Via != "dev" {
				t.Errorf("--no-auth owner: %+v %v", p, ok)
			}
		}
	}

	// From() never carries the deployment: grants, bindings and ownership
	// key on the tile path (D127f).
	for _, via := range []string{"frame", "instance", "terminal"} {
		for _, dep := range []string{"", "main", "dev"} {
			p := Principal{Component: "apps/x", Via: via, Deployment: dep}
			if p.From() != "apps/x" {
				t.Errorf("%s bound to %q: From() = %q, want the tile path", via, dep, p.From())
			}
		}
	}
}

// covers D127g T3 — the instance map resolves a token to (tile, deployment).
// RegisterInstanceDeployment binds the generation's deployment into its
// principal; main, named or not, and today's RegisterInstance mean main
// (Deployment "", the name rule). Two generations of one deployment (the
// blue/green overlap) and generations of two deployments hold separate
// entries that revoke separately; a string that isn't a deployment name
// registers nothing; no header or parameter moves a token to another
// deployment. --no-auth resolves them the same way.
func TestInstanceTokenCarriesDeployment(t *testing.T) {
	for _, noAuth := range []bool{false, true} {
		a := dlAuth(t, noAuth)
		reg := map[string]string{ // token → the deployment its principal carries
			"tok-today": "", "tok-main": "", "tok-empty": "",
			"tok-dev-g1": "dev", "tok-dev-g2": "dev", "tok-exp": "exp-2",
		}
		a.RegisterInstance("tok-today", "apps/x")
		a.RegisterInstanceDeployment("tok-main", "apps/x", "main")
		a.RegisterInstanceDeployment("tok-empty", "apps/x", "")
		a.RegisterInstanceDeployment("tok-dev-g1", "apps/x", "dev")
		a.RegisterInstanceDeployment("tok-dev-g2", "apps/x", "dev")
		a.RegisterInstanceDeployment("tok-exp", "apps/x", "exp-2")
		a.RegisterInstanceDeployment("tok-nested", "apps/x/api", "dev")
		reg["tok-nested"] = "dev"
		for tok, dep := range reg {
			comp := "apps/x"
			if tok == "tok-nested" {
				comp = "apps/x/api"
			}
			want := Principal{Component: comp, Via: "instance", Deployment: dep}
			if p, ok := dlResolve(a, dlBearer(tok)); !ok || p != want {
				t.Errorf("noAuth=%v %s: %+v %v, want %+v", noAuth, tok, p, ok, want)
			}
		}

		for _, bad := range []string{"Dev", "a+b", "x/y", "-dev", "dev|x", "1dev", strings.Repeat("a", 25), "dév"} {
			tok := "tok-bad-" + b64(bad)
			a.RegisterInstanceDeployment(tok, "apps/x", bad)
			if _, ok := a.lookupInstance(tok); ok {
				t.Errorf("noAuth=%v: %q registered an instance token", noAuth, bad)
			}
			if p, _ := dlResolve(a, dlBearer(tok)); p.Via == "instance" {
				t.Errorf("noAuth=%v: a token registered under %q authenticates: %+v", noAuth, bad, p)
			}
		}

		// Revoking one generation leaves the other deployment's and the
		// other generation's tokens as they were.
		a.RevokeInstance("tok-dev-g1")
		if p, _ := dlResolve(a, dlBearer("tok-dev-g1")); p.Via == "instance" {
			t.Errorf("noAuth=%v: a revoked generation still authenticates: %+v", noAuth, p)
		}
		for _, tok := range []string{"tok-dev-g2", "tok-main", "tok-exp"} {
			if p, ok := dlResolve(a, dlBearer(tok)); !ok || p.Deployment != reg[tok] {
				t.Errorf("noAuth=%v: revoking tok-dev-g1 changed %s: %+v %v", noAuth, tok, p, ok)
			}
		}
	}
}

// covers D127g D127j T3 PO-6 — TestFrameTokenBoundToDeployment: the frame-token
// claim is signed. A deployment other than main gets a sixth field inside
// the HMAC and resolves to a frame principal bound to it; main keeps today's
// five fields, named or not, and a legacy four-field token means main
// (TestLegacyFrameTokenUpgrade's pattern). Stripping, replacing, adding or
// re-spelling the claim fails verification. Renewal copies the claim as it
// copies the generation, never onto another tile, and one of the tile's own
// frames mints no token for another deployment of its tile.
func TestFrameTokenDeploymentClaim(t *testing.T) {
	a := zsAuth(t)
	const ttl = 15 * time.Minute
	sid := a.NewSession("bob", "192.0.2.1")
	bob := principalOf(t, a, sid, "")

	// Minting: the wire format, field by field.
	dev := a.MintFrameTokenForDeployment(bob, "apps/x", "dev", ttl)
	mints := []struct{ name, tok, want string }{
		{"session-bound", dev,
			b64("apps/x") + "|" + b64("bob") + "|<exp>|s.<handle>|" + b64("dev") + "|<hmac>"},
		{"session-bound, a nested page", a.MintFrameTokenForDeployment(bob, "apps/x/editor", "exp-2", ttl),
			b64("apps/x/editor") + "|" + b64("bob") + "|<exp>|s.<handle>|" + b64("exp-2") + "|<hmac>"},
		{"user-bound", a.MintFrameTokenDeployment("apps/x", "bob", "dev", ttl),
			b64("apps/x") + "|" + b64("bob") + "|<exp>|u.<epoch>.0|" + b64("dev") + "|<hmac>"},
		{"owner-bound", a.MintFrameTokenDeployment("apps/x", "", "dev", ttl),
			b64("apps/x") + "||<exp>|o.<owner>|" + b64("dev") + "|<hmac>"},
		// main: today's five fields, whichever call mints it
		{"main by name, session-bound", a.MintFrameTokenForDeployment(bob, "apps/x", "main", ttl),
			b64("apps/x") + "|" + b64("bob") + "|<exp>|s.<handle>|<hmac>"},
		{"main unnamed, session-bound", a.MintFrameTokenForDeployment(bob, "apps/x", "", ttl),
			b64("apps/x") + "|" + b64("bob") + "|<exp>|s.<handle>|<hmac>"},
		{"main by name, user-bound", a.MintFrameTokenDeployment("apps/x", "bob", "main", ttl),
			b64("apps/x") + "|" + b64("bob") + "|<exp>|u.<epoch>.0|<hmac>"},
	}
	for _, m := range mints {
		if got := zsMaskFrameToken(t, a, m.tok, ttl); got != m.want {
			t.Errorf("%s: minted\n  %s\nwant\n  %s", m.name, got, m.want)
		}
	}
	if b64("dev") != "ZGV2" {
		t.Fatal("base64url is not the claim encoding the goldens assume")
	}
	for _, bad := range []string{"Dev", "a+b", "x|y", "x/y", "-dev", strings.Repeat("a", 25), "dév"} {
		if tok := a.MintFrameTokenForDeployment(bob, "apps/x", bad, ttl); tok != "" {
			t.Errorf("MintFrameTokenForDeployment(%q) minted %q", bad, tok)
		}
		if tok := a.MintFrameTokenDeployment("apps/x", "bob", bad, ttl); tok != "" {
			t.Errorf("MintFrameTokenDeployment(%q) minted %q", bad, tok)
		}
	}

	// Resolving: the claim binds the frame principal, however it travels.
	resolves := []struct {
		name string
		set  func(*http.Request)
		dep  string
	}{
		{"header", dlFrame(dev), "dev"},
		{"on its session's cookie", func(r *http.Request) { r.AddCookie(cookieFor(sid)); r.Header.Set(FrameTokenHeader, dev) }, "dev"},
		{"?frame= (WebSockets)", func(r *http.Request) {
			r.URL.RawQuery = "frame=" + strings.ReplaceAll(dev, "|", "%7C")
		}, "dev"},
		{"five fields", dlFrame(a.MintFrameTokenFor(bob, "apps/x", ttl)), ""},
		{"the legacy four fields", dlFrame(legacyFrameToken(a, "apps/x", "bob", time.Now().Add(time.Minute))), ""},
	}
	for _, c := range resolves {
		p, ok := dlResolve(a, c.set)
		if !ok || p.Component != "apps/x" || p.UserID != "bob" || p.Via != "frame" || p.Deployment != c.dep {
			t.Errorf("%s: %+v %v, want the apps/x frame of bob bound to %q", c.name, p, ok, c.dep)
		}
		if c.dep != "" && p.Gen != bob.Gen {
			t.Errorf("%s: generation %q, want the session's %q", c.name, p.Gen, bob.Gen)
		}
	}
	if c, u, ok := a.VerifyFrameToken(dev); !ok || c != "apps/x" || u != "bob" {
		t.Errorf("VerifyFrameToken(claim-bearing) = %q %q %v", c, u, ok)
	}
	if p, ok := a.FramePrincipal(dev); !ok || p.Deployment != "dev" {
		t.Errorf("FramePrincipal(claim-bearing) = %+v %v", p, ok)
	}

	// Forgeries: the claim is covered by the HMAC, and main has one spelling.
	parts := strings.Split(dev, "|")
	main5 := strings.Split(a.MintFrameTokenFor(bob, "apps/x", ttl), "|")
	signed := func(fields ...string) string {
		body := strings.Join(fields, "|")
		return body + "|" + a.sign(body)
	}
	head := parts[:4]
	forged := map[string]string{
		"claim stripped, its signature kept":  strings.Join(append(append([]string{}, head...), parts[5]), "|"),
		"claim replaced":                      strings.Join(append(append([]string{}, head...), b64("prod"), parts[5]), "|"),
		"claim added to a main token":         strings.Join(append(append([]string{}, main5[:4]...), b64("dev"), main5[4]), "|"),
		"a signed claim naming main":          signed(append(append([]string{}, head...), b64("main"))...),
		"a signed empty claim":                signed(append(append([]string{}, head...), "")...),
		"a signed claim that isn't a name":    signed(append(append([]string{}, head...), b64("Dev"))...),
		"a signed claim holding +":            signed(append(append([]string{}, head...), b64("a+b"))...),
		"a signed claim, not base64url":       signed(append(append([]string{}, head...), "ZG V2")...),
		"a signed claim, padded base64":       signed(append(append([]string{}, head...), "ZGV2==")...),
		"a signed claim, generation invalid":  signed(parts[0], parts[1], parts[2], "X!", b64("dev")),
		"a signed claim, generation empty":    signed(parts[0], parts[1], parts[2], "", b64("dev")),
		"a signed claim, generation not live": signed(parts[0], parts[1], parts[2], "s.000000000000000000000000", b64("dev")),
		"a seventh field":                     signed(append(append([]string{}, parts[:5]...), b64("x"))...),
		"expired":                             a.MintFrameTokenForDeployment(bob, "apps/x", "dev", -time.Minute),
	}
	for name, tok := range forged {
		if p, ok := dlResolve(a, dlFrame(tok)); ok {
			t.Errorf("%s: resolves as %+v", name, p)
		}
	}
	// Renewal copies the claim and the generation, only onto its own tile.
	fp, ok := a.framePrincipal(dev)
	if !ok {
		t.Fatal("claim-bearing token refused")
	}
	renewed := a.MintFrameTokenFor(fp, "apps/x", ttl)
	if got, want := zsMaskFrameToken(t, a, renewed, ttl), mints[0].want; got != want {
		t.Errorf("renewal: %s, want %s", got, want)
	}
	if strings.Split(renewed, "|")[3] != parts[3] {
		t.Error("renewal changed the generation")
	}
	for _, other := range []string{"apps/y", "apps/x/editor"} {
		if n := strings.Count(a.MintFrameTokenFor(fp, other, ttl), "|") + 1; n != 5 {
			t.Errorf("a dev frame's token for %s has %d fields: the claim crossed tiles", other, n)
		}
	}

	// One of the tile's own frames mints only its bound deployment's token.
	mainFP, ok := a.framePrincipal(strings.Join(main5, "|"))
	if !ok {
		t.Fatal("main token refused")
	}
	own := []struct {
		name string
		p    Principal
		dep  string
		mint bool
	}{
		{"dev frame → dev", fp, "dev", true},
		{"dev frame → main", fp, "main", false},
		{"dev frame → main, unnamed", fp, "", false},
		{"dev frame → another deployment", fp, "prod", false},
		{"main frame → main", mainFP, "main", true},
		{"main frame → dev", mainFP, "dev", false},
		{"a human → any deployment (the server gates the level)", bob, "prod", true},
	}
	for _, c := range own {
		if got := a.MintFrameTokenForDeployment(c.p, "apps/x", c.dep, ttl) != ""; got != c.mint {
			t.Errorf("%s: minted=%v, want %v", c.name, got, c.mint)
		}
	}

	// A claim-bearing token dies with its generation, as a main one does.
	userDev := a.MintFrameTokenDeployment("apps/x", "bob", "dev", ttl)
	if !frameOK(a, userDev) {
		t.Fatal("user-bound claim refused")
	}
	a.DropUserSessions("bob")
	if frameOK(a, userDev) || frameOK(a, dev) {
		t.Error("a claim-bearing token survived sign-out-everywhere")
	}
}

// covers D127m D127p T16 T3 — TestTerminalTargetBinding and
// TestTerminalTokenCannotMintProtectedPrimaryToken, auth's half: a terminal
// or agent token records the session's target server-side. Today's
// MintTerminal follows the primary (""); a named target keeps its name, main
// included; a string that isn't a deployment name mints no token (the
// session opens without one). Nothing a request carries (X-XBin-Deployment,
// a deployment parameter, an edited XBIN_DEPLOYMENT) rebinds it. The frame
// tokens a session mints are its target's alone: a session targeting dev
// never gets main's token, so no session reaches a protected primary's
// self-admin through one — term.ChooseTarget never targets a protected
// primary, and Plane.Addressed leaves a session that follows one bound to
// nothing.
func TestTerminalTokenTarget(t *testing.T) {
	t.Setenv("XBIN_DEPLOYMENT", "steered") // what a shell can edit: never read
	const ttl = 15 * time.Minute
	for _, noAuth := range []bool{false, true} {
		a := dlAuth(t, noAuth)
		toks := []struct {
			name, tok, user, dep string
		}{
			{"MintTerminal", a.MintTerminal("apps/x", "bob"), "bob", ""},
			{"follows the primary", a.MintTerminalTarget("apps/x", "bob", ""), "bob", ""},
			{"main by name", a.MintTerminalTarget("apps/x", "bob", "main"), "bob", "main"},
			{"dev", a.MintTerminalTarget("apps/x", "bob", "dev"), "bob", "dev"},
			{"dev, owner-opened", a.MintTerminalTarget("apps/x", "", "dev"), "", "dev"},
		}
		ps := map[string]Principal{}
		for _, c := range toks {
			if len(c.tok) != 48 {
				t.Fatalf("%s: token %q is not 24 random bytes in hex", c.name, c.tok)
			}
			p, ok := dlResolve(a, dlBearer(c.tok))
			if !ok || p.Component != "apps/x" || p.Via != "terminal" || p.UserID != c.user || p.Deployment != c.dep {
				t.Errorf("noAuth=%v %s: %+v %v, want the apps/x terminal of %q targeting %q", noAuth, c.name, p, ok, c.user, c.dep)
			}
			ps[c.name] = p
		}
		n := len(a.terminals)
		for _, bad := range []string{"Dev", "a+b", "x/y", "-dev", "dev|x", strings.Repeat("a", 25)} {
			if tok := a.MintTerminalTarget("apps/x", "bob", bad); tok != "" {
				t.Errorf("noAuth=%v: target %q minted %q", noAuth, bad, tok)
			}
		}
		if len(a.terminals) != n {
			t.Errorf("noAuth=%v: refused targets left %d entries", noAuth, len(a.terminals)-n)
		}

		// The frame tokens a session mints carry its target, and only it.
		const renew = "(renewal)" // MintFrameTokenFor, which names no deployment
		mints := []struct {
			name, from, dep string
			fields          int
			mint            bool
		}{
			{"a dev session renewing", "dev", renew, 6, true},
			{"a dev session → dev", "dev", "dev", 6, true},
			{"a dev session → main", "dev", "main", 0, false},
			{"a dev session → main, unnamed", "dev", "", 0, false},
			{"a main-named session renewing", "main by name", renew, 5, true},
			{"a main-named session → main", "main by name", "main", 5, true},
			{"a main-named session → dev", "main by name", "dev", 0, false},
			{"a following session renewing: main's spelling", "follows the primary", renew, 5, true},
		}
		for _, c := range mints {
			tok := a.MintFrameTokenFor(ps[c.from], "apps/x", ttl)
			if c.dep != renew {
				tok = a.MintFrameTokenForDeployment(ps[c.from], "apps/x", c.dep, ttl)
			}
			if (tok != "") != c.mint {
				t.Errorf("noAuth=%v %s: minted %q, want minted=%v", noAuth, c.name, tok, c.mint)
				continue
			}
			if !c.mint {
				continue
			}
			if f := strings.Count(tok, "|") + 1; f != c.fields {
				t.Errorf("noAuth=%v %s: %d fields, want %d", noAuth, c.name, f, c.fields)
			}
			fp, ok := a.framePrincipal(tok)
			want, _ := claimName(ps[c.from].Deployment)
			if !ok || fp.Deployment != want || fp.Component != "apps/x" {
				t.Errorf("noAuth=%v %s: the frame resolves as %+v %v, want bound to %q", noAuth, c.name, fp, ok, want)
			}
		}

		// A session or frame bound to a deployment is an element principal:
		// never admin, even when an admin opened it (the auth side of D127k).
		adminDev, _ := dlResolve(a, dlBearer(a.MintTerminalTarget("apps/x", "alice", "dev")))
		adminFrame, _ := a.framePrincipal(a.MintFrameTokenForDeployment(adminDev, "apps/x", "dev", ttl))
		for _, p := range []Principal{adminDev, adminFrame} {
			if p.Deployment != "dev" || p.IsAdmin() {
				t.Errorf("noAuth=%v: alice's %s bound to %q: IsAdmin=%v", noAuth, p.Via, p.Deployment, p.IsAdmin())
			}
		}

		// The session ends: its token and its target go together.
		a.RevokeTerminal(toks[3].tok)
		if p, _ := dlResolve(a, dlBearer(toks[3].tok)); p.Via == "terminal" {
			t.Errorf("noAuth=%v: a revoked session token resolves: %+v", noAuth, p)
		}
	}
}
