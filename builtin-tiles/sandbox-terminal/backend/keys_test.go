package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A person registers, lists and removes their own keys; a manager lists
// everyone's and revokes any — which also ends that key's live logins.
func TestKeysAPI(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.sandbox("alice", "api-dev", shared("*"))
	k := newKey(t)
	st, b := r.do("POST", "/keys", "alice", "read", map[string]any{"publicKey": authorized(k, "alice@laptop")})
	var rec keyRec
	if st != 201 || json.Unmarshal(b, &rec) != nil || rec.User != "alice" || rec.Name != "alice@laptop" || rec.Fingerprint != ssh.FingerprintSHA256(k.PublicKey()) {
		t.Fatalf("register: %d %s", st, b)
	}
	// the same key again: the same record; someone else's: refused
	if st, _ := r.do("POST", "/keys", "alice", "read", map[string]any{"publicKey": authorized(k, "x")}); st != 200 {
		t.Fatalf("re-register: %d", st)
	}
	if st, b := r.do("POST", "/keys", "bob", "read", map[string]any{"publicKey": authorized(k, "x")}); st != 409 {
		t.Fatalf("bob registering alice's key: %d %s", st, b)
	}
	for _, bad := range []string{"", "not a key", "-----BEGIN OPENSSH PRIVATE KEY-----", `command="ls" ` + authorized(k, "")} {
		if st, b := r.do("POST", "/keys", "alice", "read", map[string]any{"publicKey": bad}); st != 400 {
			t.Fatalf("%q: %d %s", bad, st, b)
		}
	}
	weak, _ := rsa.GenerateKey(rand.Reader, 1024)
	wpk, _ := ssh.NewPublicKey(&weak.PublicKey)
	if st, b := r.do("POST", "/keys", "alice", "read", map[string]any{"publicKey": string(ssh.MarshalAuthorizedKey(wpk))}); st != 400 || !strings.Contains(string(b), "1024") {
		t.Fatalf("a 1024-bit RSA key: %d %s", st, b)
	}
	bobKey := r.register("bob")

	list := func(person, level, q string) []keyRec {
		t.Helper()
		st, b := r.do("GET", "/keys"+q, person, level, nil)
		var l struct{ Keys []keyRec }
		if st != 200 || json.Unmarshal(b, &l) != nil {
			t.Fatalf("GET /keys%s as %s: %d %s", q, person, st, b)
		}
		return l.Keys
	}
	if ks := list("alice", "read", ""); len(ks) != 1 || ks[0].ID != rec.ID {
		t.Fatalf("alice's keys: %+v", ks)
	}
	if st, _ := r.do("GET", "/keys?all=1", "alice", "read", nil); st != 403 {
		t.Fatalf("everyone's keys for a reader: %d", st)
	}
	if ks := list("carol", "write", "?all=1"); len(ks) != 2 {
		t.Fatalf("everyone's keys for a manager: %+v", ks)
	}
	if ks := list("", "", "?all=1"); len(ks) != 2 {
		t.Fatalf("everyone's keys for the owner: %+v", ks)
	}
	// alice can't remove bob's key; she can remove hers
	bobID := keyID(bobKey.PublicKey())
	if st, _ := r.do("DELETE", "/keys/"+bobID, "alice", "read", nil); st != 404 {
		t.Fatalf("alice removing bob's key: %d", st)
	}

	// a live login ends when its key is revoked
	c := r.mustDial("api-dev", k)
	live, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := live.Start("sleep 30"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the session is listed", func() bool {
		st, b := r.do("GET", "/sessions?all=1", "carol", "write", nil)
		return st == 200 && strings.Contains(string(b), `"user":"alice"`) && strings.Contains(string(b), `"kind":"command"`)
	})
	if st, b := r.do("GET", "/sessions", "bob", "read", nil); st != 200 || strings.Contains(string(b), "alice") {
		t.Fatalf("bob sees alice's sessions: %d %s", st, b)
	}
	if st, b := r.do("DELETE", "/keys/"+rec.ID, "carol", "write", nil); st != 204 {
		t.Fatalf("a manager revoking: %d %s", st, b)
	}
	if live.Wait() == nil {
		t.Fatal("a revoked key's session lives on")
	}
	if _, err := c.NewSession(); err == nil {
		t.Fatal("a revoked key's connection lives on")
	}
	if _, err := r.dial("api-dev", k); err == nil {
		t.Fatal("a revoked key logs in")
	}
	if st, _ := r.do("DELETE", "/keys/"+bobID, "bob", "read", nil); st != 204 {
		t.Fatalf("bob removing his key: %d", st)
	}
	if ks := list("", "", "?all=1"); len(ks) != 0 {
		t.Fatalf("keys left: %+v", ks)
	}
	// viewing as someone registers nothing
	if st, _ := r.doViewed("POST", "/keys", "alice", map[string]any{"publicKey": authorized(newKey(t), "")}); st != 403 {
		t.Fatalf("an admin viewing as alice registered a key: %d", st)
	}
	if st, _ := r.do("PUT", "/settings", "alice", "read", map[string]any{"sshAddress": "x"}); st != 403 {
		t.Fatalf("settings by a reader: %d", st)
	}
	if st, b := r.do("PUT", "/settings", "carol", "write", map[string]any{"sshAddress": "sbx.example.com:2222"}); st != 200 {
		t.Fatalf("settings by a manager: %d %s", st, b)
	}
	st, b = r.do("GET", "/me", "alice", "read", nil)
	var me struct {
		User    string
		Manager bool
		SSH     sshView
	}
	if st != 200 || json.Unmarshal(b, &me) != nil || me.User != "alice" || me.Manager || me.SSH.Address != "sbx.example.com:2222" ||
		me.SSH.HostKey == nil || me.SSH.HostKey.Fingerprint != ssh.FingerprintSHA256(r.hostPub) || !me.SSH.Listening {
		t.Fatalf("GET /me: %d %s", st, b)
	}
}

// The host key persists in the vault; a vault that errors (other than a
// missing key) never gets a fresh one.
func TestHostKey(t *testing.T) {
	t.Parallel()
	v := &memVault{}
	tile := newTile(self, &memKV{}, v.get, v.set, func() []manager { return nil }, nil)
	a, err := tile.loadHostKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := tile.loadHostKey()
	if err != nil || string(a.PublicKey().Marshal()) != string(b.PublicKey().Marshal()) {
		t.Fatalf("the host key changed: %v", err)
	}
	v.err = errors.New("vault: 503 Service Unavailable: sealed")
	if _, err := tile.loadHostKey(); err == nil {
		t.Fatal("a sealed vault made a key")
	}
}

// Failed logins over the rate are slowed down, per source.
func TestLimiter(t *testing.T) {
	t.Parallel()
	l := &limiter{burst: 2, every: time.Hour, buckets: map[string]*bucket{}}
	if !l.fail("a") || !l.fail("a") || l.fail("a") {
		t.Fatal("the third failure within the burst isn't slowed")
	}
	if !l.fail("b") {
		t.Fatal("another source is slowed")
	}
}

func TestManagersFromEnv(t *testing.T) {
	t.Setenv("XBIN_IFACE_SANDBOXES", `[{"provider":"apps/cs","url":"http://xbin/api/apps/cs/","service":"sandbox-manager"},
		{"provider":"apps/cs","instance":"eu","url":"http://xbin/api/apps/cs/i/eu","service":"sandbox-manager"},
		{"provider":"apps/other","url":"http://xbin/api/apps/other","service":"comm"},
		{"provider":"apps/cs","url":"http://xbin/api/apps/cs/","service":"sandbox-manager"}]`)
	ms := managersFromEnv()
	if len(ms) != 2 || ms[0].Provider != "apps/cs" || ms[0].URL != "http://xbin/api/apps/cs" || ms[1].Provider != "apps/cs#eu" {
		t.Fatalf("%+v", ms)
	}
}

func TestLoginBase(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"api-dev": "api-dev", "API dev": "api-dev", "  Ünïcode box!! ": "n-code-box", "a.b_c": "a.b_c", "!!!": ""} {
		if got := loginBase(in); got != want {
			t.Errorf("loginBase(%q) = %q, want %q", in, got, want)
		}
	}
}
