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
// a sixth field is refused (what makes a deployment claim unreadable to an
// older verifier); instance and terminal tokens resolve to today's element
// principals. Hand-maintained goldens: changing one is a compat change
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
	sixth := body + "|" + b64("dev")
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
		{"a sixth field, correctly signed", frame(sixth + "|" + a.sign(sixth)), "refused"},
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
