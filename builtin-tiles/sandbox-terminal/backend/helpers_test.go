package main

// helpers_test.go — a tile wired to the reference manager (fsb_fake_test.go,
// a byte-identical copy of hack/fakesandbox/fsb.go) on an httptest server,
// its SSH server on a loopback port, and an x/crypto/ssh client.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
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
	front   *front
	tg      sandboxcontract.Target
	sshAddr string
	hostPub ssh.PublicKey
	api     *httptest.Server
	xbind   *fakeAccess
}

// front serves the manager's requests: a test may wrap it to step in
// (see a call answered, hold an answer back).
type front struct {
	mu sync.Mutex
	h  http.Handler
}

func (f *front) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	h := f.h
	f.mu.Unlock()
	h.ServeHTTP(w, r)
}

// use wraps what serves the manager's requests from now on.
func (f *front) use(mw func(next http.Handler) http.Handler) {
	f.mu.Lock()
	f.h = mw(f.h)
	f.mu.Unlock()
}

// deletes says which of sandbox sb's execs the manager was told to DELETE,
// each once that is answered.
func (r *rig) deletes(sb string) <-chan string {
	prefix := "/sbx/sandboxes/" + sb + "/execs/"
	ch := make(chan string, 16)
	r.front.use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			next.ServeHTTP(w, q)
			if eid, ok := strings.CutPrefix(q.URL.Path, prefix); ok && q.Method == http.MethodDelete && !strings.Contains(eid, "/") {
				ch <- eid
			}
		})
	})
	return ch
}

// holdStarts holds back the manager's answers to exec starts in sandbox
// sb: each command starts, started says its id, and its answer goes out
// once answer is called (at the latest when the test ends).
func (r *rig) holdStarts(sb string) (started <-chan string, answer func()) {
	route := "/sbx/sandboxes/" + sb + "/execs"
	ids := make(chan string, 16)
	hold := make(chan struct{})
	answer = sync.OnceFunc(func() { close(hold) })
	r.t.Cleanup(answer) // before the manager's server closes: it waits for its handlers
	r.front.use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
			if q.Method != http.MethodPost || q.URL.Path != route {
				next.ServeHTTP(w, q)
				return
			}
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, q)
			var x struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &x)
			ids <- x.ID
			<-hold
			maps.Copy(w.Header(), rec.Header())
			w.WriteHeader(rec.Code)
			_, _ = w.Write(rec.Body.Bytes())
		})
	})
	return ids, answer
}

// execs lists sandbox sb's execs at the manager, as person.
func (r *rig) execs(person, sb string) []sandboxcontract.Exec {
	r.t.Helper()
	var l struct {
		Execs []sandboxcontract.Exec `json:"execs"`
	}
	r.tg.As(r.t, self).Asserting(person).Call("GET", "/sandboxes/"+sb+"/execs", nil, 200, &l)
	return l.Execs
}

// awaitOutput reads exec eid's output at the manager, as person, until it
// holds want (the exec still running).
func (r *rig) awaitOutput(person, sb, eid, want string) {
	r.t.Helper()
	me := r.tg.As(r.t, self).Asserting(person)
	out := ""
	for since, deadline := int64(0), time.Now().Add(hangGuard); ; {
		c := me.Chunk(sb, eid, fmt.Sprintf("since=%d&waitMs=10000", since))
		out, since = out+c.Data, c.End
		if strings.Contains(out, want) {
			return
		}
		if c.State != "running" || time.Now().After(deadline) {
			r.t.Fatalf("exec %s is %s, its output %q without %q", eid, c.State, out, want)
		}
	}
}

// endedByDelete: the tile sent exec eid a HUP and then — the command deaf
// to it — a DELETE; it runs no more.
func (r *rig) endedByDelete(person, sb, eid string) {
	r.t.Helper()
	route := "/sbx/sandboxes/" + sb + "/execs/" + eid
	hup, del := -1, -1
	for i, c := range r.mgr.Calls() {
		switch {
		case c.Method == http.MethodPost && c.Path == route+"/signal" && strings.Contains(c.Body, `"HUP"`) && hup < 0:
			hup = i
		case c.Method == http.MethodDelete && c.Path == route:
			del = i
		}
	}
	if hup < 0 || del < hup {
		r.t.Fatalf("exec %s: not a HUP and then a DELETE (calls #%d, #%d)", eid, hup, del)
	}
	for _, x := range r.execs(person, sb) {
		if x.State == "running" {
			r.t.Fatalf("exec %s still runs", x.ID)
		}
	}
}

// fakeAccess is xbind's GET /access/<user> for the tile: everyone reads it
// unless set otherwise; err, when set, is every answer.
type fakeAccess struct {
	mu    sync.Mutex
	m     map[string]xbin.UserAccess
	err   error
	calls int
}

func (f *fakeAccess) of(_ context.Context, user string) (xbin.UserAccess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return xbin.UserAccess{}, f.err
	}
	if a, ok := f.m[user]; ok {
		return a, nil
	}
	return xbin.UserAccess{User: user, Level: "read", Active: true}, nil
}

// set says what user may do from now on (level "none"; active false: a
// disabled account) — and forgets what the tile cached.
func (r *rig) setAccess(user, level string, active bool) {
	r.xbind.mu.Lock()
	if r.xbind.m == nil {
		r.xbind.m = map[string]xbin.UserAccess{}
	}
	r.xbind.m[user] = xbin.UserAccess{User: user, Level: level, Active: active}
	r.xbind.mu.Unlock()
	r.forget()
}

// forget drops the tile's cached answers (as if 30 s went by).
func (r *rig) forget() {
	r.tile.accMu.Lock()
	clear(r.tile.accCache)
	r.tile.accMu.Unlock()
}

// newRig: opts adjust the tile before its SSH server starts.
func newRig(t *testing.T, opts ...func(*Tile)) *rig {
	t.Helper()
	m := &fsbManager{Root: t.TempDir(), DefaultFrom: "apps/nobody", Grace: 200 * time.Millisecond}
	fr := &front{h: m}
	srv := httptest.NewServer(fr)
	t.Cleanup(func() { srv.Close(); m.Close() })
	vault := &memVault{}
	tile := newTile(self, &memKV{}, vault.get, vault.set,
		func() []manager { return []manager{{Provider: "apps/fsb", URL: srv.URL}} },
		&http.Client{Transport: fromTransport{from: self}})
	tile.hupGrace = 300 * time.Millisecond
	xb := &fakeAccess{}
	tile.accessOf = xb.of
	for _, o := range opts {
		o(tile)
	}
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
	return &rig{t: t, tile: tile, mgr: m, front: fr, tg: sandboxcontract.Target{URL: srv.URL, Grace: m.Grace},
		sshAddr: ln.Addr().String(), hostPub: signer.PublicKey(), api: api, xbind: xb}
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

// hangGuard bounds a wait for something that happens whatever the load:
// only a hang (a bug) runs into it.
const hangGuard = 2 * time.Minute

// recv waits for a value on ch.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(hangGuard):
		t.Fatalf("hung waiting: %s", what)
	}
	var zero T
	return zero
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
