package main

// helpers_test.go — a tile wired to the reference manager (fsb_fake_test.go,
// a byte-identical copy of hack/fakesandbox/fsb.go) on an httptest server,
// its SSH server on a loopback port, and an x/crypto/ssh client.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/sandboxcontract"
	"golang.org/x/crypto/ssh"
)

const self = "apps/sandbox-terminal"

// memKV is the tile's kv.
type memKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (k *memKV) Get(key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	b, ok := k.m[key]
	if !ok {
		return nil, errNotFound
	}
	return append([]byte(nil), b...), nil
}

func (k *memKV) Put(key string, val []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[string][]byte{}
	}
	k.m[key] = append([]byte(nil), val...)
	return nil
}

// memVault is the tile's vault; err, when set, is every read's answer.
type memVault struct {
	mu  sync.Mutex
	m   map[string]string
	err error
}

func (v *memVault) get(name string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.err != nil {
		return "", v.err
	}
	s, ok := v.m[name]
	if !ok {
		return "", errors.New(`vault: 404 Not Found: {"error":"no such key"}`)
	}
	return s, nil
}

func (v *memVault) set(name, val string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = map[string]string{}
	}
	v.m[name] = val
	return nil
}

// fromTransport sets X-XBin-From as xbind's gateway does for this tile.
type fromTransport struct{ from string }

func (f fromTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("X-XBin-From", f.from)
	return http.DefaultTransport.RoundTrip(r)
}

// rig is a tile with its manager, SSH server and HTTP routes.
type rig struct {
	t       *testing.T
	tile    *Tile
	mgr     *fsbManager
	tg      sandboxcontract.Target
	sshAddr string
	hostPub ssh.PublicKey
	api     *httptest.Server
}

func newRig(t *testing.T) *rig {
	t.Helper()
	m := &fsbManager{Root: t.TempDir(), DefaultFrom: "apps/nobody", Grace: 200 * time.Millisecond}
	srv := httptest.NewServer(m)
	t.Cleanup(func() { srv.Close(); m.Close() })
	vault := &memVault{}
	tile := newTile(self, &memKV{}, vault.get, vault.set,
		func() []manager { return []manager{{Provider: "apps/fsb", URL: srv.URL}} },
		&http.Client{Transport: fromTransport{from: self}})
	tile.hupGrace = 300 * time.Millisecond
	if err := tile.loadKeys(); err != nil {
		t.Fatal(err)
	}
	signer, err := tile.loadHostKey()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = tile.serveSSH(ln, signer) }()
	t.Cleanup(func() { ln.Close() })
	api := httptest.NewServer(tile.routes())
	t.Cleanup(api.Close)
	return &rig{t: t, tile: tile, mgr: m, tg: sandboxcontract.Target{URL: srv.URL, Grace: m.Grace},
		sshAddr: ln.Addr().String(), hostPub: signer.PublicKey(), api: api}
}

// agent is another consumer of the manager (the agent tile), acting for user.
func (r *rig) agent(user string) sandboxcontract.Caller {
	return r.tg.As(r.t, "apps/agent").Asserting(user)
}

// sandbox creates a sandbox at the agent for owner, patched with fields
// (shares, visibility, members).
func (r *rig) sandbox(owner, name string, patch map[string]any) sandboxcontract.Sandbox {
	r.t.Helper()
	a := r.agent(owner)
	sb := a.Create(map[string]any{"name": name})
	if len(patch) > 0 {
		a.Call("PATCH", "/sandboxes/"+sb.ID, patch, 200, &sb)
	}
	return sb
}

// shared is a patch sharing a sandbox with this tile for users ("*" or a list).
func shared(users any) map[string]any {
	return map[string]any{"shares": []map[string]any{{"consumer": self, "users": users}}}
}

// do calls the tile's API as person (level: read | write | terminal; ""
// person: the owner token).
func (r *rig) do(method, path, person, level string, body any) (int, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, r.api.URL+path, rd)
	if person == "" {
		req.Header.Set("X-XBin-From", "owner")
	} else {
		req.Header.Set("X-XBin-From", self)
		req.Header.Set("X-XBin-User", person)
		req.Header.Set("X-XBin-User-Level", level)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// doViewed calls as an admin viewing the workspace as person (D64).
func (r *rig) doViewed(method, path, person string, body any) (int, []byte) {
	r.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, r.api.URL+path, bytes.NewReader(b))
	req.Header.Set("X-XBin-From", self)
	req.Header.Set("X-XBin-User", person)
	req.Header.Set("X-XBin-User-Level", "read")
	req.Header.Set("X-XBin-Viewed-By", "admin")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// newKey makes an SSH key pair.
func newKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func authorized(s ssh.Signer, comment string) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))) + " " + comment
}

// register registers a new key for person and returns it.
func (r *rig) register(person string) ssh.Signer {
	r.t.Helper()
	k := newKey(r.t)
	if st, b := r.do("POST", "/keys", person, "read", map[string]any{"publicKey": authorized(k, person+"@laptop")}); st != 201 {
		r.t.Fatalf("register: %d %s", st, b)
	}
	return k
}

// dial logs in as login with key.
func (r *rig) dial(login string, key ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", r.sshAddr, &ssh.ClientConfig{User: login, Auth: []ssh.AuthMethod{ssh.PublicKeys(key)},
		HostKeyCallback: ssh.FixedHostKey(r.hostPub), Timeout: 10 * time.Second})
}

func (r *rig) mustDial(login string, key ssh.Signer) *ssh.Client {
	r.t.Helper()
	c, err := r.dial(login, key)
	if err != nil {
		r.t.Fatalf("ssh %s: %v", login, err)
	}
	r.t.Cleanup(func() { c.Close() })
	return c
}

// run runs cmd in a session of c (a terminal when pty): stdout, stderr and
// the exit status (-1: no status).
func run(t *testing.T, c *ssh.Client, cmd string, pty bool, stdin string) (string, string, int) {
	t.Helper()
	s, err := c.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if pty {
		if err := s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
			t.Fatal(err)
		}
	}
	var out, errb bytes.Buffer
	s.Stdout, s.Stderr = &out, &errb
	if stdin != "" {
		s.Stdin = strings.NewReader(stdin)
	}
	err = s.Run(cmd)
	return out.String(), errb.String(), exitCode(t, err)
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var ee *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitStatus()
	case errors.As(err, &missing):
		return -1
	}
	t.Fatalf("session: %v", err)
	return 0
}

// eventually waits for cond.
func eventually(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
