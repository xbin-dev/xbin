//go:build linux

package xbindtest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Resp is an answer: its status, headers and body.
type Resp struct {
	Status int
	Header http.Header
	Body   []byte
}

// Decode decodes the body as JSON into v, failing the test when it can't.
func (r Resp) Decode(t testing.TB, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("decode %q: %v", cut(r.Body), err)
	}
}

// String is the body, cut to 2 KiB.
func (r Resp) String() string { return cut(r.Body) }

func cut(b []byte) string {
	if len(b) > 2048 {
		return string(b[:2048]) + "…"
	}
	return string(b)
}

// Header is one request header: Call(t, …, H("X-XBin-Frame-Token", tok)).
type Header struct{ K, V string }

// H is a request header.
func H(k, v string) Header { return Header{k, v} }

// Call sends one request to the daemon. path is sent as written — an
// encoded "/" (%2F) or a ".." segment reaches xbind unchanged. body is nil,
// a string or []byte (sent raw), an io.Reader (streamed) or a value (sent
// as JSON).
func (d *Daemon) Call(t testing.TB, method, path string, body any, hdrs ...Header) Resp {
	t.Helper()
	st, b, h, err := d.doH(method, path, body, hdrs)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return Resp{Status: st, Header: h, Body: b}
}

// Must is Call, failing the test unless the status is want.
func (d *Daemon) Must(t testing.TB, method, path string, body any, want int, hdrs ...Header) Resp {
	t.Helper()
	r := d.Call(t, method, path, body, hdrs...)
	if r.Status != want {
		t.Fatalf("%s %s: %d %s (want %d)", method, path, r.Status, r, want)
	}
	return r
}

func (d *Daemon) do(method, path string, body any, hdrs []Header) (int, string, error) {
	st, b, _, err := d.doH(method, path, body, hdrs)
	return st, cut(b), err
}

func (d *Daemon) doH(method, path string, body any, hdrs []Header) (int, []byte, http.Header, error) {
	var rd io.Reader
	ctype := ""
	switch v := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(v)
	case []byte:
		rd = bytes.NewReader(v)
	case io.Reader:
		rd = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return 0, nil, nil, err
		}
		rd, ctype = bytes.NewReader(b), "application/json"
	}
	req, err := http.NewRequest(method, d.URL+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for _, h := range hdrs {
		req.Header.Set(h.K, h.V)
	}
	d.owner(req.Header)
	resp, err := noRedirects.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header, err
}

// noRedirects answers a redirect as itself: a test sees where xbind or a
// backend sent it, and never follows a path it didn't write.
var noRedirects = &http.Client{
	Timeout:       10 * time.Minute,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (d *Daemon) get(path string, out any) error {
	st, b, _, err := d.doH(http.MethodGet, path, nil, nil)
	if err != nil {
		return err
	}
	if st != 200 {
		return fmt.Errorf("GET %s: %d %s", path, st, cut(b))
	}
	return json.Unmarshal(b, out)
}

// Dial opens a WebSocket to path (gorilla's client, as a browser's would
// be: masked frames), with hdrs on the upgrade. A refused upgrade returns
// the answer and an error.
func (d *Daemon) Dial(t testing.TB, path string, hdrs ...Header) (*websocket.Conn, Resp, error) {
	t.Helper()
	h := http.Header{}
	for _, x := range hdrs {
		h.Set(x.K, x.V)
	}
	if !strings.Contains(path, "frame=") {
		d.owner(h)
	}
	c, resp, err := websocket.DefaultDialer.Dial("ws://"+d.Addr+path, h)
	var r Resp
	if resp != nil {
		r.Status, r.Header = resp.StatusCode, resp.Header
		if resp.Body != nil {
			r.Body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
		}
	}
	return c, r, err
}

// owner makes a request the owner's (Options.Auth: the owner token as a
// bearer) unless it carries a credential of its own.
func (d *Daemon) owner(h http.Header) {
	if d.token != "" && h.Get("Authorization") == "" && h.Get("X-XBin-Frame-Token") == "" {
		h.Set("Authorization", "Bearer "+d.token)
	}
}

// AddUser makes an account (Options.Auth; POST /api/xbin/users): role
// "user" or "admin", tiles its access entries (a path or a `prefix/*`
// pattern → read, write or terminal). extra fields (noTerminal, …) are
// merged into the request. On a remote daemon, whose workspace outlives
// the test, an earlier run's account of that id is deleted first, and
// this one when the test ends.
func (d *Daemon) AddUser(t testing.TB, id, password, role string, tiles map[string]string, extra ...map[string]any) {
	t.Helper()
	body := map[string]any{"id": id, "name": id, "role": role, "password": password, "tiles": tiles}
	for _, e := range extra {
		for k, v := range e {
			body[k] = v
		}
	}
	if d.IsRemote() {
		if r := d.Call(t, http.MethodDelete, "/api/xbin/users/"+url.PathEscape(id), nil); r.Status != 200 && r.Status != 404 {
			t.Fatalf("deleting an earlier run's %s: %d %s", id, r.Status, r)
		}
		t.Cleanup(func() { _, _, _ = d.do(http.MethodDelete, "/api/xbin/users/"+url.PathEscape(id), nil, nil) })
	}
	d.Must(t, http.MethodPost, "/api/xbin/users", body, 200)
}

// Login signs a person in with their password (POST /api/xbin/login, the
// app's sign-in) and returns their session's bearer token.
func (d *Daemon) Login(t testing.TB, id, password string) string {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"username": id, "password": password})
	resp, err := http.Post(d.URL+"/api/xbin/login", "application/json", bytes.NewReader(b)) // no credential: a sign-in
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out struct{ Token string }
	if resp.StatusCode != 200 || json.Unmarshal(body, &out) != nil || out.Token == "" {
		t.Fatalf("signing %s in: %d %s", id, resp.StatusCode, cut(body))
	}
	return out.Token
}

// Eventually polls cond every 100 ms until it holds, failing the test with
// what's last after timeout. cond returns whether it holds and what it saw.
func Eventually(t testing.TB, timeout time.Duration, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		ok, saw := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s (last: %s)", what, timeout, saw)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// --- the workspace -----------------------------------------------------------

// CopyTile copies a tile's directory (an example, a fixture) into the
// workspace at tile, as an import does, and waits for xbind to register it.
func (d *Daemon) CopyTile(t testing.TB, src, tile string) {
	t.Helper()
	if d.rem != nil {
		files, err := dirFiles(src)
		if err == nil {
			err = d.writeRemote(tile, files)
		}
		if err != nil {
			t.Fatal(err)
		}
		d.WaitComponent(t, tile)
		return
	}
	dst := d.WS + "/" + tile
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	d.WaitComponent(t, tile)
}

// WriteTile writes a tile of the given files (relative path → contents)
// into the workspace at tile and waits for xbind to register it.
func (d *Daemon) WriteTile(t testing.TB, tile string, files map[string]string) {
	t.Helper()
	if err := d.WriteFiles(tile, files); err != nil {
		t.Fatal(err)
	}
	d.WaitComponent(t, tile)
}

// WriteFiles writes files (relative path → contents) under the workspace's
// tile — a remote daemon's through XBIN_E2E_SH — without waiting or
// failing a test (a hook on another goroutine may call it).
func (d *Daemon) WriteFiles(tile string, files map[string]string) error {
	if d.rem != nil {
		raw := map[string][]byte{}
		for rel, content := range files {
			raw[rel] = []byte(content)
		}
		return d.writeRemote(tile, raw)
	}
	for rel, content := range files {
		p := d.WS + "/" + tile + "/" + rel
		if err := os.MkdirAll(p[:strings.LastIndexByte(p, '/')], 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// WaitComponent waits for xbind's registry to list tile.
func (d *Daemon) WaitComponent(t testing.TB, tile string) {
	t.Helper()
	Eventually(t, 30*time.Second, "component "+tile+" registered", func() (bool, string) {
		st, b, err := d.do(http.MethodGet, "/api/xbin/components/"+tile, nil, nil)
		return err == nil && st == 200, fmt.Sprint(st, b, err)
	})
}

// Grant approves a grant as the owner: from may use target at role
// (POST /api/xbin/grants).
func (d *Daemon) Grant(t testing.TB, from, target, role string) {
	t.Helper()
	d.Must(t, http.MethodPost, "/api/xbin/grants", map[string]string{"from": from, "target": target, "role": role}, 200)
}

// Revoke withdraws a grant (DELETE /api/xbin/grants).
func (d *Daemon) Revoke(t testing.TB, from, target, role string) {
	t.Helper()
	d.Must(t, http.MethodDelete, "/api/xbin/grants", map[string]string{"from": from, "target": target, "role": role}, 200)
}

// Bind binds component's interface slot to provider (POST
// /api/xbin/bindings): a builtin network (internet, none, lan:<cidr>, …)
// or a tile.
func (d *Daemon) Bind(t testing.TB, component, slot, provider string) {
	t.Helper()
	d.Must(t, http.MethodPost, "/api/xbin/bindings", map[string]string{"component": component, "slot": slot, "provider": provider}, 200)
}

// FrameToken is a frame token of tile: a request carrying it (the
// X-XBin-Frame-Token header, or ?frame= on a WebSocket) is the tile's page
// calling — X-XBin-From is the tile — even under --no-auth.
func (d *Daemon) FrameToken(t testing.TB, tile string) string {
	t.Helper()
	var out struct{ Token string }
	d.Must(t, http.MethodGet, "/api/xbin/frame-token?component="+url.QueryEscape(tile), nil, 200).Decode(t, &out)
	if out.Token == "" {
		t.Fatalf("no frame token for %s", tile)
	}
	return out.Token
}

// FrameHeader is the header that makes a request tile's page's.
func FrameHeader(token string) Header { return H("X-XBin-Frame-Token", token) }

// --- VMs ---------------------------------------------------------------------

// VMStatus is GET /api/xbin/vm's status.
type VMStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
	Emulated  bool   `json:"emulated"`
	Note      string `json:"note"`
}

// RequireVM skips the test unless the daemon can run VMs (KVM, or QEMU's
// emulation with XBIN_VM_ACCEL=emulate or no KVM), then turns VM tile
// sandboxes on (the VM policy's tiles, and tilesEmulated where VMs run
// emulated). It returns the accelerator: "kvm" or "emulate".
func (d *Daemon) RequireVM(t testing.TB) string {
	t.Helper()
	var out struct{ Status VMStatus }
	d.Must(t, http.MethodGet, "/api/xbin/vm", nil, 200).Decode(t, &out)
	if !out.Status.Available {
		t.Skipf("VM sandboxes unavailable here: %s", out.Status.Reason)
	}
	pol := map[string]any{"tiles": true, "tilesEmulated": out.Status.Emulated, "budgetMiB": 8192, "tilesBudgetMiB": 4096}
	if d.rem != nil { // a shared host: its own budget (XBIN_E2E_VM_MIB, XBIN_E2E_VMS)
		mib, vms := d.vmBudget()
		pol["budgetMiB"], pol["tilesBudgetMiB"], pol["maxVMs"] = mib, mib, vms
	}
	d.Must(t, http.MethodPut, "/api/xbin/vm/policy", pol, 200)
	if out.Status.Emulated {
		t.Logf("VMs run emulated: %s", out.Status.Note)
		return "emulate"
	}
	return "kvm"
}

// TileSandbox is one row of the admin's tileSandboxes.
type TileSandbox struct {
	Tile        string `json:"tile"`
	Name        string `json:"name"`
	UID         string `json:"uid"`
	State       string `json:"state"`
	StateDetail string `json:"stateDetail"`
	Mode        string `json:"mode"`
	DiskBytes   int64  `json:"diskBytes"`
	TileExists  bool   `json:"tileExists"`
}

// TileSandboxes is every tile sandbox, as an admin lists them.
func (d *Daemon) TileSandboxes(t testing.TB) []TileSandbox {
	t.Helper()
	var out struct {
		TileSandboxes []TileSandbox `json:"tileSandboxes"`
	}
	d.Must(t, http.MethodGet, "/api/xbin/sandboxes", nil, 200).Decode(t, &out)
	return out.TileSandboxes
}

// Token is the owner token a request needs ("" under --no-auth).
func (d *Daemon) Token() string { return d.token }
