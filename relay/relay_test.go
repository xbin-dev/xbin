package relay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	topic   = "dev.xbin.app"
	tokenOK = "aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11aa11"
	token2  = "bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22bb22"
)

// fakeAPNs is an HTTP/2 TLS server speaking enough of APNs: it verifies the
// provider token and records every notification.
type fakeAPNs struct {
	t      *testing.T
	srv    *httptest.Server
	pub    *ecdsa.PublicKey
	mu     sync.Mutex
	got    []gotPush
	jwts   map[string]bool
	answer map[string][]answer // per device token: queued answers (then 200)
}

type answer struct {
	status int
	reason string
}

type gotPush struct {
	token, topic, pushType, priority, collapse, expiration string
	body                                                   map[string]any
	proto                                                  int
}

func newFakeAPNs(t *testing.T, pub *ecdsa.PublicKey) *fakeAPNs {
	f := &fakeAPNs{t: t, pub: pub, jwts: map[string]bool{}, answer: map[string][]answer{}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.srv.EnableHTTP2 = true
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPNs) serve(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutPrefix(r.URL.Path, "/3/device/")
	if !ok || r.Method != http.MethodPost {
		http.Error(w, "bad path", 404)
		return
	}
	jwt, _ := strings.CutPrefix(r.Header.Get("authorization"), "bearer ")
	if !f.verifyJWT(jwt) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"reason":"InvalidProviderToken"}`))
		return
	}
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &body); err != nil {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"reason":"PayloadEmpty"}`))
		return
	}
	f.mu.Lock()
	f.jwts[jwt] = true
	var a *answer
	if q := f.answer[tok]; len(q) > 0 {
		a = &q[0]
		f.answer[tok] = q[1:]
	}
	if a == nil {
		f.got = append(f.got, gotPush{token: tok, topic: r.Header.Get("apns-topic"), pushType: r.Header.Get("apns-push-type"),
			priority: r.Header.Get("apns-priority"), collapse: r.Header.Get("apns-collapse-id"),
			expiration: r.Header.Get("apns-expiration"), body: body, proto: r.ProtoMajor})
	}
	f.mu.Unlock()
	if a != nil {
		if a.status == 429 {
			w.Header().Set("retry-after", "7")
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(`{"reason":"` + a.reason + `"}`))
		return
	}
	w.Header().Set("apns-id", "11111111-2222-3333-4444-555555555555")
	w.WriteHeader(200)
}

func (f *fakeAPNs) verifyJWT(tok string) bool {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return false
	}
	enc := base64.RawURLEncoding
	var hdr struct{ Alg, Kid string }
	var claims struct {
		Iss string
		Iat int64
	}
	hb, _ := enc.DecodeString(parts[0])
	cb, _ := enc.DecodeString(parts[1])
	sig, _ := enc.DecodeString(parts[2])
	if json.Unmarshal(hb, &hdr) != nil || json.Unmarshal(cb, &claims) != nil || len(sig) != 64 {
		return false
	}
	if hdr.Alg != "ES256" || hdr.Kid != "KEY123" || claims.Iss != "TEAM45" || claims.Iat == 0 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(f.pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func (f *fakeAPNs) pushes() []gotPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gotPush(nil), f.got...)
}

type rig struct {
	t     *testing.T
	fake  *fakeAPNs
	relay *Server
	srv   *httptest.Server
	now   time.Time
	mu    sync.Mutex
}

func (r *rig) clock() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.mu.Lock()
	r.now = r.now.Add(d)
	r.mu.Unlock()
}

func p8(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func newRig(t *testing.T, mod func(*Config)) *rig {
	t.Helper()
	k, pemBytes := p8(t)
	loaded, err := LoadP8(pemBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Equal(k) {
		t.Fatal("LoadP8 returned a different key")
	}
	r := &rig{t: t, now: time.Unix(1_800_000_000, 0)}
	r.fake = newFakeAPNs(t, &k.PublicKey)
	cfg := Config{
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Topics:    []string{topic},
		APNs: &APNs{KeyID: "KEY123", TeamID: "TEAM45", Key: loaded, Production: r.fake.srv.URL,
			Development: r.fake.srv.URL, Client: r.fake.srv.Client(), Now: r.clock},
		Now: r.clock,
	}
	if mod != nil {
		mod(&cfg)
	}
	r.relay, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.srv = httptest.NewServer(r.relay)
	t.Cleanup(r.srv.Close)
	return r
}

func (r *rig) call(method, path, key string, body any) (int, map[string]any, http.Header) {
	r.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, r.srv.URL+path, rd)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func (r *rig) workspace() string {
	r.t.Helper()
	code, out, _ := r.call("POST", "/v1/workspaces", "", nil)
	if code != 200 || out["key"] == "" || out["workspaceId"] == "" {
		r.t.Fatalf("workspace: %d %v", code, out)
	}
	return out["key"].(string)
}

func (r *rig) handle(token string) string {
	r.t.Helper()
	code, out, _ := r.call("POST", "/v1/handles", "", map[string]string{"apnsToken": token, "topic": topic, "env": "production"})
	if code != 200 {
		r.t.Fatalf("handle: %d %v", code, out)
	}
	return out["handle"].(string)
}

func envelope() map[string]any {
	enc := base64.RawURLEncoding
	return map[string]any{"v": 1, "epk": enc.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"n": enc.EncodeToString(bytes.Repeat([]byte{9}, 12)), "ct": enc.EncodeToString(bytes.Repeat([]byte{1}, 80))}
}

func (r *rig) push(key, handle string, extra map[string]any) (int, map[string]any, http.Header) {
	body := map[string]any{"handle": handle, "envelope": envelope()}
	for k, v := range extra {
		body[k] = v
	}
	return r.call("POST", "/v1/push", key, body)
}

func TestPushDeliversOverHTTP2(t *testing.T) {
	r := newRig(t, nil)
	key, h := r.workspace(), r.handle(tokenOK)
	code, out, _ := r.push(key, h, map[string]any{"collapseId": "c1", "priority": 5})
	if code != 200 || out["apnsId"] == "" {
		t.Fatalf("push: %d %v", code, out)
	}
	if code, out, _ := r.push(key, h, nil); code != 200 {
		t.Fatalf("second push: %d %v", code, out)
	}
	got := r.fake.pushes()
	if len(got) != 2 {
		t.Fatalf("fake got %d pushes", len(got))
	}
	p := got[0]
	if p.proto != 2 || p.token != tokenOK || p.topic != topic || p.pushType != "alert" || p.priority != "5" || p.collapse != "c1" {
		t.Fatalf("headers: %+v", p)
	}
	if want := r.clock().Add(24 * time.Hour).Unix(); p.expiration != fmtInt(want) {
		t.Fatalf("expiration %s, want %d", p.expiration, want)
	}
	aps := p.body["aps"].(map[string]any)
	if aps["alert"] != "New activity" || aps["mutable-content"] != float64(1) {
		t.Fatalf("aps: %v", aps)
	}
	gotEnv, _ := json.Marshal(p.body["xbin"])
	wantEnv, _ := json.Marshal(envelope())
	if !bytes.Equal(gotEnv, wantEnv) {
		t.Fatalf("envelope forwarded as %s, want %s", gotEnv, wantEnv)
	}
	if len(p.body) != 2 {
		t.Fatalf("unexpected keys in the APNs body: %v", p.body)
	}
	if got[1].priority != "10" {
		t.Fatalf("default priority %s", got[1].priority)
	}
	if len(r.fake.jwts) != 1 {
		t.Fatalf("provider token not cached: %d minted", len(r.fake.jwts))
	}
	r.advance(41 * time.Minute)
	if code, _, _ := r.push(key, h, nil); code != 200 {
		t.Fatal("push after refresh")
	}
	if len(r.fake.jwts) != 2 {
		t.Fatalf("provider token not refreshed after 41 min: %d", len(r.fake.jwts))
	}
}

func fmtInt(v int64) string { b, _ := json.Marshal(v); return string(b) }

func TestExpiredProviderTokenRetriesOnce(t *testing.T) {
	r := newRig(t, nil)
	key, h := r.workspace(), r.handle(tokenOK)
	r.fake.answer[tokenOK] = []answer{{403, "ExpiredProviderToken"}}
	if code, out, _ := r.push(key, h, nil); code != 200 {
		t.Fatalf("push: %d %v", code, out)
	}
	if len(r.fake.jwts) != 2 {
		t.Fatalf("expected a fresh token on retry, saw %d", len(r.fake.jwts))
	}
}

func TestGoneDropsHandlesOfThatToken(t *testing.T) {
	r := newRig(t, nil)
	key := r.workspace()
	h1, h2, other := r.handle(tokenOK), r.handle(tokenOK), r.handle(token2)
	r.fake.answer[tokenOK] = []answer{{410, "Unregistered"}}
	if code, _, _ := r.push(key, h1, nil); code != 410 {
		t.Fatalf("want 410, got %d", code)
	}
	for _, h := range []string{h1, h2} {
		if code, _, _ := r.push(key, h, nil); code != 404 {
			t.Fatalf("handle of a dead token still pushes: %d", code)
		}
	}
	if code, _, _ := r.push(key, other, nil); code != 200 {
		t.Fatalf("another token's handle: %d", code)
	}
	r.fake.answer[token2] = []answer{{400, "BadDeviceToken"}}
	if code, _, _ := r.push(key, other, nil); code != 410 {
		t.Fatalf("BadDeviceToken: want 410, got %d", code)
	}
}

func TestAPNsErrorsMapped(t *testing.T) {
	r := newRig(t, nil)
	key, h := r.workspace(), r.handle(tokenOK)
	r.fake.answer[tokenOK] = []answer{{429, "TooManyRequests"}, {500, "InternalServerError"}, {400, "BadCollapseId"}}
	code, _, hdr := r.push(key, h, nil)
	if code != 429 || hdr.Get("Retry-After") == "" {
		t.Fatalf("APNs 429 → %d (Retry-After %q)", code, hdr.Get("Retry-After"))
	}
	if code, _, _ := r.push(key, h, nil); code != 502 {
		t.Fatalf("APNs 500 → %d", code)
	}
	if code, _, _ := r.push(key, h, nil); code != 400 {
		t.Fatalf("APNs 400 → %d", code)
	}
}

func TestHandleBoundToFirstWorkspace(t *testing.T) {
	r := newRig(t, nil)
	k1, k2 := r.workspace(), r.workspace()
	h := r.handle(tokenOK)
	if code, _, _ := r.push(k1, h, nil); code != 200 {
		t.Fatal(code)
	}
	if code, _, _ := r.push(k2, h, nil); code != 403 {
		t.Fatalf("another workspace used a bound handle: %d", code)
	}
	if code, _, _ := r.push(k1, h, nil); code != 200 {
		t.Fatal(code)
	}
}

func TestRefusals(t *testing.T) {
	r := newRig(t, nil)
	key, h := r.workspace(), r.handle(tokenOK)
	if code, _, _ := r.push("xbr_nope", h, nil); code != 401 {
		t.Fatalf("bad key: %d", code)
	}
	if code, _, _ := r.push("", h, nil); code != 401 {
		t.Fatalf("no key: %d", code)
	}
	if code, _, _ := r.push(key, "AAAAAAAAAAAAAAAAAAAAAA", nil); code != 404 {
		t.Fatalf("unknown handle: %d", code)
	}
	bad := envelope()
	bad["n"] = "short"
	if code, _, _ := r.call("POST", "/v1/push", key, map[string]any{"handle": h, "envelope": bad}); code != 400 {
		t.Fatalf("bad nonce: %d", code)
	}
	big := envelope()
	big["ct"] = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 2500))
	if code, _, _ := r.call("POST", "/v1/push", key, map[string]any{"handle": h, "envelope": big}); code != 400 {
		t.Fatalf("oversized envelope: %d", code)
	}
	if code, _, _ := r.push(key, h, map[string]any{"priority": 7}); code != 400 {
		t.Fatalf("bad priority: %d", code)
	}
	if code, _, _ := r.push(key, h, map[string]any{"collapseId": strings.Repeat("x", 65)}); code != 400 {
		t.Fatalf("long collapse id: %d", code)
	}
	for _, q := range []map[string]string{
		{"apnsToken": "zz", "topic": topic, "env": "production"},
		{"apnsToken": tokenOK, "topic": "com.evil.app", "env": "production"},
		{"apnsToken": tokenOK, "topic": topic, "env": "staging"},
	} {
		if code, _, _ := r.call("POST", "/v1/handles", "", q); code != 400 {
			t.Fatalf("handle %v: %d", q, code)
		}
	}
	if len(r.fake.pushes()) != 0 {
		t.Fatal("a refused push reached APNs")
	}
}

func TestDevelopmentEnvAndRepointAndDelete(t *testing.T) {
	var devHits, prodHits int
	r := newRig(t, nil)
	dev := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		devHits++
		r.fake.serve(w, req)
	}))
	dev.EnableHTTP2 = true
	dev.StartTLS()
	defer dev.Close()
	r.relay.cfg.APNs.Development = dev.URL
	r.relay.cfg.APNs.Client = dev.Client() // both test servers share httptest's CA
	r.relay.cfg.APNs.Production = r.fake.srv.URL
	key := r.workspace()
	code, out, _ := r.call("POST", "/v1/handles", "", map[string]string{"apnsToken": strings.ToUpper(tokenOK), "topic": topic, "env": "sandbox"})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	h := out["handle"].(string)
	if code, _, _ := r.push(key, h, nil); code != 200 || devHits != 1 {
		t.Fatalf("development push: %d, dev hits %d", code, devHits)
	}
	if r.fake.pushes()[0].token != tokenOK {
		t.Fatal("token not normalised to lowercase")
	}
	code, _, _ = r.call("PUT", "/v1/handles/"+h, "", map[string]string{"apnsToken": token2, "topic": topic, "env": "production"})
	if code != 200 {
		t.Fatalf("repoint: %d", code)
	}
	if code, _, _ := r.push(key, h, nil); code != 200 || devHits != 1 {
		t.Fatalf("after repoint: %d dev hits %d prod %d", code, devHits, prodHits)
	}
	got := r.fake.pushes()
	if got[len(got)-1].token != token2 {
		t.Fatalf("repointed handle went to %s", got[len(got)-1].token)
	}
	if code, _, _ := r.call("DELETE", "/v1/handles/"+h, "", nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := r.push(key, h, nil); code != 404 {
		t.Fatalf("deleted handle: %d", code)
	}
	if code, _, _ := r.call("PUT", "/v1/handles/"+h, "", map[string]string{"apnsToken": token2, "topic": topic, "env": "production"}); code != 404 {
		t.Fatalf("repoint deleted: %d", code)
	}
}

func TestRateLimits(t *testing.T) {
	r := newRig(t, func(c *Config) {
		c.HandleRate = Rate{PerHour: 3600, Burst: 2}
		c.WorkspaceRate = Rate{PerHour: 3600, Burst: 3}
		c.NewWorkspaceRate = Rate{PerHour: 1, Burst: 2}
		c.NewHandleRate = Rate{PerHour: 1, Burst: 3}
	})
	key := r.workspace()
	h1, h2 := r.handle(tokenOK), r.handle(token2)
	for i := 0; i < 2; i++ {
		if code, _, _ := r.push(key, h1, nil); code != 200 {
			t.Fatalf("push %d: %d", i, code)
		}
	}
	code, _, hdr := r.push(key, h1, nil)
	if code != 429 || hdr.Get("Retry-After") != "2" {
		t.Fatalf("handle limit: %d Retry-After %q", code, hdr.Get("Retry-After"))
	}
	// the workspace has one token left (the refused push spent one)
	code, _, _ = r.push(key, h2, nil)
	if code != 429 {
		t.Fatalf("workspace limit: %d", code)
	}
	r.advance(2 * time.Second)
	if code, _, _ := r.push(key, h1, nil); code != 200 {
		t.Fatalf("after refill: %d", code)
	}
	r.workspace()
	if code, _, _ := r.call("POST", "/v1/workspaces", "", nil); code != 429 {
		t.Fatalf("workspace creation limit: %d", code)
	}
	r.handle(tokenOK)
	if code, _, _ := r.call("POST", "/v1/handles", "", map[string]string{"apnsToken": tokenOK, "topic": topic, "env": "production"}); code != 429 {
		t.Fatalf("handle creation limit: %d", code)
	}
}

func TestStatePersistsKeyHashesOnly(t *testing.T) {
	r := newRig(t, nil)
	key, h := r.workspace(), r.handle(tokenOK)
	if code, _, _ := r.push(key, h, nil); code != 200 {
		t.Fatal(code)
	}
	path := r.relay.cfg.StatePath
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(key)) {
		t.Fatal("the workspace key is stored in the clear")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("state mode %v", fi.Mode().Perm())
	}
	cfg := r.relay.cfg
	s2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.relay = s2
	r.srv.Config.Handler = s2
	if code, out, _ := r.push(key, h, nil); code != 200 {
		t.Fatalf("after reopen: %d %v", code, out)
	}
	k2 := r.workspace()
	if code, _, _ := r.push(k2, h, nil); code != 403 {
		t.Fatalf("binding lost on reopen: %d", code)
	}
}

func TestNoAPNsKey(t *testing.T) {
	r := newRig(t, func(c *Config) { c.APNs = nil })
	key, h := r.workspace(), r.handle(tokenOK)
	if code, _, _ := r.push(key, h, nil); code != 503 {
		t.Fatalf("no key: %d", code)
	}
}

func TestHandlesPerTokenCapped(t *testing.T) {
	st, _ := openStore("")
	now := time.Unix(1000, 0)
	first, _ := st.newHandle(tokenOK, topic, "production", now)
	for i := 0; i < maxHandlesPerToken; i++ {
		now = now.Add(time.Second)
		if _, err := st.newHandle(tokenOK, topic, "production", now); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := st.counts(); n != maxHandlesPerToken {
		t.Fatalf("%d handles, want %d", n, maxHandlesPerToken)
	}
	if _, err := st.target(first, "ws", now); err != errNoHandle {
		t.Fatal("the oldest handle was not evicted")
	}
}
