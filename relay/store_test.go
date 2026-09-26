package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

var testLimits = storeLimits{maxHandles: DefaultMaxHandles, maxWorkspaces: DefaultMaxWorkspaces,
	unboundTTL: DefaultUnboundHandleTTL, idleTTL: DefaultIdleHandleTTL, unusedWorkspace: DefaultUnusedWorkspaceTTL}

// Every refusal carries a machine-readable code: xbind acts on the code,
// never on a bare status a proxy could have produced.
func TestErrorCodes(t *testing.T) {
	r := newRig(t, nil)
	k1, k2 := r.workspace(), r.workspace()
	h := r.handle(tokenOK)
	if code, _, _ := r.push(k1, h, nil); code != 200 {
		t.Fatal(code)
	}
	for _, c := range []struct {
		name   string
		status int
		code   string
		do     func() (int, map[string]any, http.Header)
	}{
		{"bad key", 401, ErrBadKey, func() (int, map[string]any, http.Header) { return r.push("xbr_nope", h, nil) }},
		{"bound", 403, ErrHandleBound, func() (int, map[string]any, http.Header) { return r.push(k2, h, nil) }},
		{"unknown", 404, ErrHandleUnknown, func() (int, map[string]any, http.Header) { return r.push(k1, "AAAAAAAAAAAAAAAAAAAAAA", nil) }},
		{"malformed", 400, ErrBadRequest, func() (int, map[string]any, http.Header) {
			return r.push(k1, h, map[string]any{"priority": 7})
		}},
		{"gone", 410, ErrHandleGone, func() (int, map[string]any, http.Header) {
			r.fake.answer[tokenOK] = []answer{{410, "Unregistered"}}
			return r.push(k1, h, nil)
		}},
	} {
		status, out, _ := c.do()
		if status != c.status || out["code"] != c.code || out["error"] == "" {
			t.Errorf("%s: %d %v, want %d code %s", c.name, status, out, c.status, c.code)
		}
	}
}

// GET /v1/workspace is how xbind checks a key an admin hands it.
func TestWorkspaceProbe(t *testing.T) {
	r := newRig(t, nil)
	code, out, _ := r.call("POST", "/v1/workspaces", "", nil)
	if code != 200 {
		t.Fatal(code)
	}
	key, id := out["key"].(string), out["workspaceId"].(string)
	if code, out, _ := r.call("GET", "/v1/workspace", key, nil); code != 200 || out["workspaceId"] != id {
		t.Fatalf("probe: %d %v", code, out)
	}
	if code, out, _ := r.call("GET", "/v1/workspace", "xbr_wrong", nil); code != 401 || out["code"] != ErrBadKey {
		t.Fatalf("wrong key: %d %v", code, out)
	}
	if code, _, _ := r.call("GET", "/v1/workspace", "", nil); code != 401 {
		t.Fatalf("no key: %d", code)
	}
}

// Registration limits count an IPv6 client per /64, an IPv4 client per
// address.
func TestRegistrationLimitsPerPrefix(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.TrustProxy = true
		c.NewHandleRate = Rate{PerHour: 1, Burst: 2}
	})
	from := func(ip string) int {
		req, _ := http.NewRequest("POST", r.srv.URL+"/v1/handles", jsonBody(map[string]string{"apnsToken": tokenOK, "topic": topic, "env": "production"}))
		req.Header.Set("X-Forwarded-For", "203.0.113.9, "+ip)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i, ip := range []string{"2001:db8:1:2::1", "2001:db8:1:2:ffff::7"} {
		if code := from(ip); code != 200 {
			t.Fatalf("call %d: %d", i, code)
		}
	}
	if code := from("2001:db8:1:2:dead:beef::1"); code != 429 {
		t.Fatalf("another address in the same /64 has its own bucket: %d", code)
	}
	if code := from("2001:db8:1:3::1"); code != 200 {
		t.Fatalf("the next /64: %d", code)
	}
	if code := from("198.51.100.1"); code != 200 {
		t.Fatalf("IPv4: %d", code)
	}
	if code := from("198.51.100.2"); code != 200 {
		t.Fatalf("another IPv4 address: %d", code)
	}
	if k := (&Server{}).clientKey(&http.Request{RemoteAddr: "[::ffff:198.51.100.1]:4000"}, 64); k != "198.51.100.1" {
		t.Fatalf("v4-mapped: %q", k)
	}
	if k := (&Server{}).clientKey(&http.Request{RemoteAddr: "[2001:db8:aa:bb::1]:4000"}, 48); k != "2001:db8:aa::/48" {
		t.Fatalf("workspaces count per /48: %q", k)
	}
}

// Workspace registrations are bounded per /48 and for everyone together.
func TestWorkspaceRegistrationLimits(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.TrustProxy = true
		c.NewWorkspaceRate = Rate{PerHour: 1, Burst: 1}
		c.AllNewWorkspacesRate = Rate{PerHour: 1, Burst: 3}
	})
	from := func(ip string) int {
		req, _ := http.NewRequest("POST", r.srv.URL+"/v1/workspaces", nil)
		req.Header.Set("X-Forwarded-For", ip)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i, c := range []struct {
		ip   string
		want int
	}{
		{"2001:db8:1::1", 200}, {"2001:db8:1:ffff::1", 429}, // one /48
		{"2001:db8:2::1", 200}, {"198.51.100.1", 200},
		{"198.51.100.2", 429}, // everyone together
	} {
		if code := from(c.ip); code != c.want {
			t.Fatalf("call %d from %s: %d, want %d", i, c.ip, code, c.want)
		}
	}
}

// Handles no workspace ever used, handles idle for long and workspace keys
// never used are deleted; a full relay refuses with 503 "full" until the
// sweep frees room.
func TestRetentionAndCapacity(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.MaxHandles, c.MaxWorkspaces = 3, 2
		c.UnboundHandleTTL, c.IdleHandleTTL, c.UnusedWorkspaceTTL = 48*time.Hour, 100*time.Hour, 48*time.Hour
	})
	key := r.workspace()
	idle := r.workspace() // never used
	_ = idle
	bound, unbound := r.handle(tokenOK), r.handle(token2)
	if code, _, _ := r.push(key, bound, nil); code != 200 {
		t.Fatal(code)
	}
	r.handle(tokenOK)
	code, out, _ := r.call("POST", "/v1/handles", "", map[string]string{"apnsToken": tokenOK, "topic": topic, "env": "production"})
	if code != 503 || out["code"] != ErrFull {
		t.Fatalf("full: %d %v", code, out)
	}
	if code, out, _ := r.call("POST", "/v1/workspaces", "", nil); code != 503 || out["code"] != ErrFull {
		t.Fatalf("workspaces full: %d %v", code, out)
	}
	r.advance(49 * time.Hour)
	if code, _, _ := r.push(key, bound, nil); code != 200 { // used: kept
		t.Fatal(code)
	}
	r.handle(tokenOK) // the sweep dropped the unbound handles and the unused key
	if code, _, _ := r.push(key, unbound, nil); code != 404 {
		t.Fatalf("an unbound handle past its TTL: %d", code)
	}
	if h, w := r.relay.Counts(); h != 2 || w != 1 {
		t.Fatalf("after the sweep: %d handles, %d workspaces", h, w)
	}
	r.advance(101 * time.Hour)
	r.handle(token2)
	if code, _, _ := r.push(key, bound, nil); code != 404 {
		t.Fatalf("an idle handle past its TTL: %d", code)
	}
	if code, _, _ := r.call("GET", "/v1/workspace", key, nil); code != 200 {
		t.Fatalf("a used workspace was dropped: %d", code)
	}
}

// With VerifyTokens a handle needs a device token APNs accepts.
func TestVerifyTokens(t *testing.T) {
	r := newRig(t, func(c *Config) { c.VerifyTokens = true })
	r.fake.answer[token2] = []answer{{400, "BadDeviceToken"}}
	code, out, _ := r.call("POST", "/v1/handles", "", map[string]string{"apnsToken": token2, "topic": topic, "env": "production"})
	if code != 400 || out["code"] != ErrBadToken {
		t.Fatalf("a made-up token: %d %v", code, out)
	}
	if n, _ := r.relay.Counts(); n != 0 {
		t.Fatal("a refused token was stored")
	}
	r.handle(tokenOK)
	got := r.fake.pushes()
	if len(got) != 1 || got[0].pushType != "background" || got[0].priority != "5" {
		t.Fatalf("the check: %+v", got)
	}
	if aps := got[0].body["aps"].(map[string]any); aps["content-available"] != float64(1) || aps["alert"] != nil {
		t.Fatalf("the check carries no alert: %v", aps)
	}
}

// The journal survives a crash (no Close), ignores a torn last line, and
// compaction folds it into the snapshot while writes keep coming.
func TestJournalReplayAndCompaction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	now := time.Unix(1_800_000_000, 0)
	st, err := openStore(path, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	ws, _, err := st.newWorkspace(now)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := st.newHandle(tokenOK, topic, "production", now)
	h2, _ := st.newHandle(token2, topic, "production", now)
	if _, err := st.target(h1, ws, "", false, now); err != nil {
		t.Fatal(err)
	}
	st.deleteHandle(h2)
	later := now.Add(25 * time.Hour)
	if _, err := st.target(h1, ws, "", false, later); err != nil { // a day on: the usage timestamp is written
		t.Fatal(err)
	}
	// a torn write at the end
	f, _ := os.OpenFile(path+".log", os.O_WRONLY|os.O_APPEND, 0)
	_, _ = f.WriteString(`{"s":99,"h":"torn`)
	f.Close()

	re, err := openStore(path, testLimits) // no close: a crash
	if err != nil {
		t.Fatal(err)
	}
	if h := re.st.Handles[h1]; h == nil || h.Workspace != ws || h.Used != later.Unix() {
		t.Fatalf("replayed handle %+v", h)
	}
	if n, w := re.counts(); n != 1 || w != 1 {
		t.Fatalf("replayed %d handles %d workspaces", n, w)
	}
	if fi, _ := os.Stat(path + ".log"); fi.Size() != 0 {
		t.Fatalf("the journal was not folded into the snapshot on open: %d bytes", fi.Size())
	}

	// compaction under concurrent writes
	old := minCompact
	minCompact = 512
	defer func() { minCompact = old }()
	re.j.compactAt = minCompact
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				tok := fmt.Sprintf("%064x", g*1000+i)
				id, err := re.newHandle(tok, topic, "production", now)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := re.target(id, ws, "", false, now); err != nil {
					t.Error(err)
				}
			}
		}(g)
	}
	wg.Wait()
	re.j.wg.Wait() // background compactions
	var mid state
	if b, err := os.ReadFile(path); err != nil || json.Unmarshal(b, &mid) != nil || len(mid.Handles) < 50 {
		t.Fatalf("no compaction ran while writing: the snapshot holds %d handles (%v)", len(mid.Handles), err)
	}
	if err := re.close(); err != nil {
		t.Fatal(err)
	}
	fin, err := openStore(path, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := fin.counts(); n != 1+8*25 {
		t.Fatalf("after compactions: %d handles, want %d", n, 1+8*25)
	}
	for id, h := range fin.st.Handles {
		if h.Workspace != ws {
			t.Fatalf("handle %s lost its binding", id)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode %v", fi.Mode().Perm())
	}
}

// A workspace key used once is not kept forever (review: a flood of keys
// each probed once filled MaxWorkspaces for good). A key unused — no push,
// no GET /v1/workspace — for IdleWorkspaceTTL goes; one used within it
// (xbind checks its key daily) stays. The operator can also close
// registration to their own token.
func TestIdleWorkspacesAndRegistrationTokens(t *testing.T) {
	r := newRig(t, func(c *Config) { c.IdleWorkspaceTTL = 100 * time.Hour })
	idle, live := r.workspace(), r.workspace()
	for _, k := range []string{idle, live} {
		if code, _, _ := r.call("GET", "/v1/workspace", k, nil); code != 200 {
			t.Fatal(code)
		}
	}
	r.advance(60 * time.Hour)
	if code, _, _ := r.call("GET", "/v1/workspace", live, nil); code != 200 { // the daily key check
		t.Fatal(code)
	}
	r.advance(60 * time.Hour)
	r.handle(tokenOK) // a registration runs the sweep
	if code, out, _ := r.call("GET", "/v1/workspace", idle, nil); code != 401 || out["code"] != ErrBadKey {
		t.Fatalf("a key idle past its TTL: %d %v", code, out)
	}
	if code, _, _ := r.call("GET", "/v1/workspace", live, nil); code != 200 {
		t.Fatalf("a key checked within its TTL: %d", code)
	}

	r2 := newRig(t, func(c *Config) { c.RegistrationTokens = []string{"op-token"} })
	for _, tok := range []string{"", "wrong"} {
		if code, out, _ := r2.call("POST", "/v1/workspaces", tok, nil); code != 401 || out["code"] != ErrRegistration {
			t.Fatalf("registration with %q: %d %v", tok, code, out)
		}
	}
	if code, out, _ := r2.call("POST", "/v1/workspaces", "op-token", nil); code != 200 || out["key"] == nil {
		t.Fatalf("registration with the operator's token: %d %v", code, out)
	}
}
