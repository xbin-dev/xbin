package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The App's JWT is RS256 by its key — PKCS#1 (what GitHub downloads) or
// PKCS#8 — with iat a minute back, exp nine minutes on, iss the client id.
func TestJWTSignsPKCS1AndPKCS8(t *testing.T) {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p8, _ := x509.MarshalPKCS8PrivateKey(k)
	now := time.Unix(1790000000, 0)
	for name, text := range map[string]string{
		"pkcs1": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})),
		"pkcs8": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: p8})),
	} {
		key, err := parseAppKey(text)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		tok, err := signJWT(key, "Iv23li", now)
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(tok, ".")
		if len(parts) != 3 {
			t.Fatalf("%s: %q", name, tok)
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
			t.Fatalf("%s: signature: %v", name, err)
		}
		var head, claims map[string]any
		h, _ := base64.RawURLEncoding.DecodeString(parts[0])
		c, _ := base64.RawURLEncoding.DecodeString(parts[1])
		json.Unmarshal(h, &head)
		json.Unmarshal(c, &claims)
		if head["alg"] != "RS256" || claims["iss"] != "Iv23li" || claims["iat"].(float64) != float64(now.Unix()-60) || claims["exp"].(float64) != float64(now.Unix()+540) {
			t.Fatalf("%s: %v %v", name, head, claims)
		}
	}
	if _, err := parseAppKey("not a key"); err == nil {
		t.Fatal("garbage parsed")
	}
	ek, _ := x509.MarshalPKCS8PrivateKey(mustEC(t))
	if _, err := parseAppKey(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ek}))); err == nil {
		t.Fatal("an EC key parsed as the App's")
	}
	// The cache answers the same JWT for 8 minutes, then a new one.
	var jc jwtCache
	a, _ := jc.get(k, "Iv23li", now)
	b, _ := jc.get(k, "Iv23li", now.Add(7*time.Minute))
	c, _ := jc.get(k, "Iv23li", now.Add(8*time.Minute))
	if a.Reveal() != b.Reveal() || a.Reveal() == c.Reveal() {
		t.Fatal("JWT cache: not 8 minutes")
	}
}

// Which identity each caller may use, everywhere (docs/scm.md §Identities).
func TestIdentityMatrix(t *testing.T) {
	e := newEnv(t)
	e.setup()
	tok := map[string]any{"repo": "acme/web", "access": "read"}
	with := func(as string) map[string]any {
		m := map[string]any{"as": as}
		for k, v := range tok {
			m[k] = v
		}
		return m
	}

	// Global, a tile: the bot, by default; a person is no one here.
	var h helloResp
	decode(t, e.call(e.gH, agentC, "GET", "/scm/hello?protocol=1", nil), &h)
	if h.You.Default != "bot" || strings.Join(h.You.Identities, ",") != "bot" || h.You.Person != nil {
		t.Fatalf("global tile: %+v", h.You)
	}
	ok(t, e.call(e.gH, agentC, "POST", "/scm/token", tok), 200)
	x := refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", with("person")), 403, "identity")
	if strings.Join(x.Identities, ",") != "bot" {
		t.Fatalf("identities: %v", x.Identities)
	}

	// A person's partition: the person by default — signin while not
	// signed in; the bot refused while botForPeople is off.
	u := e.user("alice").routes()
	decode(t, e.call(u, personC("alice"), "GET", "/scm/hello", nil), &h)
	if h.You.Default != "person" || strings.Join(h.You.Identities, ",") != "person" {
		t.Fatalf("partition: %+v", h.You)
	}
	x = refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", tok), 409, "signin")
	if x.Signin == nil || x.Signin.UserCode == "" || x.Signin.PollID == "" {
		t.Fatalf("no sign-in started: %+v", x)
	}
	x = refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", with("bot")), 403, "identity")
	if strings.Join(x.Identities, ",") != "person" {
		t.Fatalf("identities: %v", x.Identities)
	}
	e.signIn("alice", "octocat")
	r := e.call(u, personC("alice"), "POST", "/scm/token", tok)
	ok(t, r, 200)
	var got map[string]any
	decode(t, r, &got)
	if got["identity"].(map[string]any)["kind"] != "person" || got["identity"].(map[string]any)["login"] != "octocat" {
		t.Fatalf("token identity: %v", got["identity"])
	}

	// Legacy: bot only — for tiles; for a person's consumer only under on.
	_, lh := e.legacy()
	ok(t, e.call(lh, agentC, "POST", "/scm/token", tok), 200)
	refusal(t, e.call(lh, agentC, "POST", "/scm/token", with("person")), 403, "identity")
	refusal(t, e.call(lh, personC("alice"), "POST", "/scm/token", with("person")), 403, "identity")
	refusal(t, e.call(lh, personC("alice"), "POST", "/scm/token", tok), 403, "identity")
	ok(t, e.call(lh, caller{from: "owner", role: "admin"}, "PUT", "/api/policy", policy{BotForPeople: "own-access", BotRepos: []string{"*/*"}, AllowedAccounts: []string{"acme"}}), 200)
	refusal(t, e.call(lh, personC("alice"), "POST", "/scm/token", tok), 403, "identity")
	ok(t, e.call(lh, caller{from: "owner", role: "admin"}, "PUT", "/api/policy", policy{BotForPeople: "on", BotRepos: []string{"*/*"}, AllowedAccounts: []string{"acme"}}), 200)
	ok(t, e.call(lh, personC("alice"), "POST", "/scm/token", tok), 200)

	// No consumer role: nothing; a stranger's partition never reaches alice's.
	refusal(t, e.call(e.gH, nobodyC, "POST", "/scm/token", tok), 403, "not-allowed")
	refusal(t, e.call(u, agentC, "POST", "/scm/token", tok), 403, "not-allowed")
}

// A call carrying a person's partition is never the tile itself at global,
// even with the tile's own path and the admin role.
func TestGlobalNeverTreatsPartitionAsSelf(t *testing.T) {
	e := newEnv(t)
	e.setup()
	forged := caller{from: tilePath, role: "admin", partition: "user:alice", pid: "pid-alice"}
	for _, path := range []string{"/api/policy", "/setup/app"} {
		refusal(t, e.call(e.gH, forged, "GET", path, nil), 403, "not-allowed")
	}
	refusal(t, e.call(e.gH, forged, "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 403, "not-allowed")
	refusal(t, e.call(e.gH, forged, "POST", "/tick", nil), 403, "not-allowed")
	// With the person (an admin keeping their role): still no one — the
	// relay takes reader or writer only.
	forged.user, forged.level = "alice", "write"
	refusal(t, e.call(e.gH, forged, "GET", "/api/policy", nil), 403, "not-allowed")
	refusal(t, e.call(e.gH, forged, "POST", "/partition/identity", map[string]string{"accessToken": "x"}), 403, "not-allowed")
	// The owner token acting for a partition is no owner either.
	refusal(t, e.call(e.gH, caller{from: "owner", role: "admin", partition: "user:alice"}, "GET", "/api/policy", nil), 403, "not-allowed")
	// The person's relay is a person: a manager by their level, never the
	// tile — a reader is no manager; and /scm/* isn't theirs at global.
	ok(t, e.call(e.gH, relayC("alice", "write"), "GET", "/api/policy", nil), 200)
	refusal(t, e.call(e.gH, relayC("bob", "read"), "GET", "/api/policy", nil), 403, "not-allowed")
	refusal(t, e.call(e.gH, relayC("alice", "write"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 403, "not-allowed")
	// The tile itself and the owner are managers at global.
	ok(t, e.call(e.gH, selfC, "GET", "/api/policy", nil), 200)
	ok(t, e.call(e.gH, ownerC, "GET", "/api/policy", nil), 200)
	// A person's partition of another tile never reaches global's /scm/*.
	refusal(t, e.call(e.gH, personC("alice"), "GET", "/scm/hello", nil), 403, "not-allowed")
}

// A person's partition serves only its own person: another person's
// partition of a consumer, another person's frame, view-as — no one.
func TestPersonModeRefusesOtherPerson(t *testing.T) {
	e := newEnv(t)
	e.setup()
	u := e.signIn("alice", "octocat").routes()
	tok := map[string]any{"repo": "acme/web", "access": "read"}
	refusal(t, e.call(u, personC("bob"), "POST", "/scm/token", tok), 403, "not-allowed")
	refusal(t, e.call(u, pageC("bob"), "GET", "/scm/signin", nil), 403, "not-allowed")
	v := pageC("alice")
	v.viewedBy = "admin"
	refusal(t, e.call(u, v, "GET", "/scm/signin", nil), 403, "not-allowed")
	// Setup is global's: a person's partition has none.
	refusal(t, e.call(u, pageC("alice"), "GET", "/setup/app", nil), 404, "not-found")
	if r := e.call(u, ingress, "GET", "/setup/github?code=x&state=y", nil); r.Code != 404 {
		t.Fatalf("a partition served the manifest callback: %d", r.Code)
	}
	// The relay is global's only.
	refusal(t, e.call(u, relayC("alice", "write"), "POST", "/partition/identity", map[string]string{"accessToken": "x"}), 404, "not-found")
	ok(t, e.call(u, personC("alice"), "POST", "/scm/token", tok), 200)
}

// An unpartitioned copy keeps no one's sign-in: the bot only, and says so.
func TestLegacyBotOnly(t *testing.T) {
	e := newEnv(t)
	_, lh := e.legacy()
	var h helloResp
	decode(t, e.call(lh, agentC, "GET", "/scm/hello", nil), &h)
	if strings.Join(h.Identities, ",") != "bot" || h.You.Default != "bot" || len(h.Notes) == 0 {
		t.Fatalf("legacy hello: %+v", h)
	}
	for _, c := range h.Caps {
		if c == capPartitions || c == capRerun {
			t.Fatalf("legacy lists %s", c)
		}
	}
	x := refusal(t, e.call(lh, personC("alice"), "POST", "/scm/signin", nil), 403, "identity")
	if len(x.Identities) != 0 {
		t.Fatalf("identities: %v", x.Identities)
	}
	refusal(t, e.call(lh, adminAt, "POST", "/scm/signin", nil), 403, "identity")
	// A person in the legacy frame with write access is a manager.
	ok(t, e.call(lh, adminAt, "GET", "/api/policy", nil), 200)
	ok(t, e.call(lh, caller{from: tilePath, role: "admin", user: "bob", level: "read"}, "GET", "/api/page", nil), 200)
	refusal(t, e.call(lh, caller{from: tilePath, role: "admin", user: "bob", level: "read"}, "GET", "/api/policy", nil), 403, "not-allowed")
	refusal(t, e.call(lh, viewAs, "GET", "/api/policy", nil), 403, "not-allowed")
}

// botForPeople: off refuses a person's partition the bot; own-access lets
// a person have it for repos they can push to (read: pull); on lets them.
// Global decides, whatever the partition thinks.
func TestBotForPeoplePolicy(t *testing.T) {
	e := newEnv(t)
	e.setup()
	u := e.signIn("alice", "octocat").routes()
	write := map[string]any{"repo": "acme/web", "access": "write", "as": "bot"}
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", write), 403, "identity")
	// The person's own frame at global, straight to the relay: refused too.
	refusal(t, e.call(e.gH, relayC("alice", "write"), "POST", "/partition/bot-token", write), 403, "identity")

	p := basePolicy()
	p.BotForPeople = "own-access"
	e.setPolicy(p)
	r := e.call(u, personC("alice"), "POST", "/scm/token", write)
	ok(t, r, 200)
	var got map[string]any
	decode(t, r, &got)
	if got["identity"].(map[string]any)["kind"] != "bot" {
		t.Fatalf("not the bot: %v", got)
	}
	// octocat can only read acme/api: no write token, a read one yes.
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/api", "access": "write", "as": "bot"}), 403, "not-allowed")
	ok(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/api", "access": "read", "as": "bot"}), 200)
	// Bob never signed in: own-access has no login to check.
	refusal(t, e.call(e.gH, relayC("bob", "write"), "POST", "/partition/bot-token", write), 409, "signin")

	p.BotForPeople = "on"
	e.setPolicy(p)
	ok(t, e.call(e.gH, relayC("bob", "write"), "POST", "/partition/bot-token", map[string]any{"repo": "acme/api", "access": "write"}), 200)
	var h helloResp
	decode(t, e.call(u, personC("alice"), "GET", "/scm/hello", nil), &h)
	if strings.Join(h.You.Identities, ",") != "person,bot" {
		t.Fatalf("hello identities under on: %v", h.You.Identities)
	}
	_ = http.StatusOK
}

func mustEC(t *testing.T) *ecdsa.PrivateKey {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
