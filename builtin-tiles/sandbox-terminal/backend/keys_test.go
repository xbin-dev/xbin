package main

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net"
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

// A port still held (the previous run, on its way out after a restart) is
// bound once it is free, and GET /me says why SSH is down meanwhile.
func TestListenRetries(t *testing.T) {
	t.Parallel()
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tile := newTile(self, &memKV{}, (&memVault{}).get, nil, func() []manager { return nil }, nil)
	tile.listenRetry = 20 * time.Millisecond
	got := make(chan net.Listener, 1)
	go func() { got <- tile.listen(held.Addr().String()) }()
	eventually(t, 5*time.Second, "the bind error in GET /me", func() bool {
		v := tile.sshView()
		return !v.Listening && strings.Contains(v.Error, "address already in use")
	})
	held.Close()
	select {
	case ln := <-got:
		ln.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("never bound the port once it was free")
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

// The person rules, on what a manager answers: a sandbox is this tile's own
// only when its home is this tile — one naming no home needs a share too.
func TestMayUse(t *testing.T) {
	t.Parallel()
	tile := newTile(self, &memKV{}, nil, nil, func() []manager { return nil }, nil)
	sb := func(via string, shared bool, users string, vis string, members ...string) *sandbox {
		s := &sandbox{Visibility: vis, Members: members, Shared: shared}
		s.Owner.User, s.Owner.Via = "bob", via
		if users != "" {
			s.Shares = []share{{Consumer: self, Users: json.RawMessage(users)}}
		}
		return s
	}
	for name, c := range map[string]struct {
		sb   *sandbox
		want bool
	}{
		"its own, team":              {sb(self, false, "", "team"), true},
		"its own, private":           {sb(self, false, "", "private"), false},
		"its own, a member":          {sb(self, false, "", "private", "alice"), true},
		"shared *, team":             {sb("apps/agent", true, `"*"`, "team"), true},
		"shared for bob, team":       {sb("apps/agent", true, `["bob"]`, "team"), false},
		"shared for alice, private":  {sb("apps/agent", true, `["alice"]`, "private"), false},
		"shared for alice, member":   {sb("apps/agent", true, `["alice"]`, "private", "alice"), true},
		"another home, no share":     {sb("apps/agent", false, "", "team"), false},
		"no home named, no share":    {sb("", false, "", "team"), false},
		"no home named, shared *":    {sb("", false, `"*"`, "team"), true},
		"shared, a share of garbage": {sb("apps/agent", true, `{"x":1}`, "team"), false},
	} {
		if got := tile.mayUse("alice", c.sb); got != c.want {
			t.Errorf("%s: mayUse = %v, want %v", name, got, c.want)
		}
	}
	if tile.mayUse("", sb(self, false, "", "team")) {
		t.Error("nobody may use anything")
	}
}
