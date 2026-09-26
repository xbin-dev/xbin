package push

import (
	"bytes"
	"crypto/ecdh"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// fakeRelay models relay/README.md: minted workspace keys, GET
// /v1/workspace, and handles bound to the workspace that first pushes to
// them (403 handle_bound after), with the relay's error codes. Keys it did
// not mint name their own workspace (the environment's k1).
type fakeRelay struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	got     []relayPush
	answers []fakeAnswer      // answers to give next (then the model's)
	block   chan struct{}     // non-nil: /v1/push waits on it
	regs    int               // POST /v1/workspaces calls
	probes  int               // GET /v1/workspace calls
	keys    map[string]string // key → workspace id
	revoked map[string]bool   // keys the relay forgot (401 bad_key)
	bound   map[string]string // handle → workspace id
	unknown map[string]bool   // handles the relay does not know (404 handle_unknown)
	// proof of work (pow_test.go): bits asked (0 = none); noChallenge hides
	// the challenge endpoint (the refusal carries one); spent challenges
	pow         int
	noChallenge bool
	powSpent    map[string]bool
}

type fakeAnswer struct {
	status int
	body   string
}

// codes are bare statuses (what a proxy or a misrouted URL answers).
func codes(st ...int) []fakeAnswer {
	var out []fakeAnswer
	for _, c := range st {
		out = append(out, fakeAnswer{status: c, body: "{}"})
	}
	return out
}

type relayPush struct {
	Key        string
	Handle     string   `json:"handle"`
	Envelope   Envelope `json:"envelope"`
	CollapseID string   `json:"collapseId"`
	Priority   int      `json:"priority"`
	// Live Activity pushes (activity_test.go)
	Type     string         `json:"type"`
	Activity *activityPush  `json:"activity"`
	Raw      map[string]any `json:"-"` // the body as sent
}

func relayErr(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "code": code})
}

func newFakeRelay(t *testing.T) *fakeRelay {
	f := &fakeRelay{t: t, keys: map[string]string{}, revoked: map[string]bool{}, bound: map[string]string{}, unknown: map[string]bool{},
		powSpent: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/workspaces/challenge", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.noChallenge {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(f.challengeLocked())
	})
	mux.HandleFunc("POST /v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PoW *struct{ Challenge, Nonce string } `json:"pow"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		if f.pow > 0 {
			code := ""
			switch {
			case body.PoW == nil:
				code = "pow_required"
			case !strings.HasPrefix(body.PoW.Challenge, "fake-ch-") || f.powSpent[body.PoW.Challenge] ||
				!powSolves(body.PoW.Challenge, body.PoW.Nonce, f.pow):
				code = "pow_invalid"
			}
			if code != "" {
				ch := f.challengeLocked()
				ch["error"], ch["code"] = code, code
				f.mu.Unlock()
				w.WriteHeader(401)
				_ = json.NewEncoder(w).Encode(ch)
				return
			}
			f.powSpent[body.PoW.Challenge] = true
		}
		f.regs++
		key, id := "xbr_fresh", "wsid"
		if f.regs > 1 {
			key, id = fmt.Sprintf("xbr_fresh%d", f.regs), fmt.Sprintf("wsid%d", f.regs)
		}
		f.keys[key] = id
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"workspaceId": id, "key": key})
	})
	mux.HandleFunc("GET /v1/workspace", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		f.probes++
		id, ok := f.keys[key]
		ok = ok && !f.revoked[key]
		f.mu.Unlock()
		if !ok {
			relayErr(w, 401, "bad_key")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"workspaceId": id})
	})
	mux.HandleFunc("POST /v1/push", func(w http.ResponseWriter, r *http.Request) {
		if f.block != nil {
			<-f.block
		}
		var p relayPush
		raw, _ := io.ReadAll(r.Body)
		if json.Unmarshal(raw, &p) != nil || json.Unmarshal(raw, &p.Raw) != nil {
			w.WriteHeader(400)
			return
		}
		p.Key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.answers) > 0 {
			a := f.answers[0]
			f.answers = f.answers[1:]
			w.WriteHeader(a.status)
			_, _ = w.Write([]byte(a.body))
			return
		}
		ws, ok := f.keys[p.Key]
		if !ok {
			ws = p.Key
		}
		switch {
		case f.revoked[p.Key]:
			relayErr(w, 401, "bad_key")
		case f.unknown[p.Handle]:
			relayErr(w, 404, "handle_unknown")
		case f.bound[p.Handle] != "" && f.bound[p.Handle] != ws:
			relayErr(w, 403, "handle_bound")
		default:
			f.bound[p.Handle] = ws
			f.got = append(f.got, p)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// challengeLocked mints a fake challenge (mu held).
func (f *fakeRelay) challengeLocked() map[string]any {
	if f.pow <= 0 {
		return map[string]any{"bits": 0}
	}
	return map[string]any{"challenge": fmt.Sprintf("fake-ch-%d-%d", f.regs, len(f.powSpent)), "bits": f.pow, "expires": 0}
}

// set changes the model under its lock.
func (f *fakeRelay) set(fn func()) {
	f.mu.Lock()
	fn()
	f.mu.Unlock()
}

func (f *fakeRelay) answer(a ...fakeAnswer) {
	f.mu.Lock()
	f.answers = append(f.answers, a...)
	f.mu.Unlock()
}

func (f *fakeRelay) pushes() []relayPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]relayPush(nil), f.got...)
}

// waitPushes waits until the relay holds n pushes.
func (f *fakeRelay) waitPushes(n int) []relayPush {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.pushes(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.t.Fatalf("relay got %d pushes, want %d", len(f.pushes()), n)
	return nil
}

var (
	alice = auth.Principal{UserID: "alice", Via: "session", Gen: "s.alice"}
	bob   = auth.Principal{UserID: "bob", Via: "session", Gen: "s.bob"}
	owner = auth.Principal{Owner: true, Via: "bearer", Gen: "o.owner"}
	cal   = auth.Principal{Component: "apps/cal", Via: "instance"}
	other = auth.Principal{Component: "apps/other", Via: "instance"}
)

// readable: who may read which tile in these tests.
var readable = map[string][]string{"alice": {"apps/cal", "apps/other"}, "bob": {"apps/other"}}

type rig struct {
	t     *testing.T
	s     *Service
	relay *fakeRelay
	mux   *http.ServeMux
	keys  map[string]*ecdh.PrivateKey // deviceId → device key
}

func newRig(t *testing.T, mod func(*Options)) *rig {
	t.Helper()
	r := &rig{t: t, relay: newFakeRelay(t), keys: map[string]*ecdh.PrivateKey{}}
	o := Options{Dir: t.TempDir(), RelayURL: r.relay.srv.URL, RelayKey: "k1", Grace: -1,
		Backoff: func(int) time.Duration { return time.Millisecond },
		CanRead: func(user, tile string) bool {
			for _, x := range readable[user] {
				if x == tile {
					return true
				}
			}
			return user == OwnerUser
		}}
	if mod != nil {
		mod(&o)
	}
	s, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	r.s = s
	r.mux = http.NewServeMux()
	for pat, h := range map[string]http.HandlerFunc{
		"POST /devices/push": s.APIRegister, "GET /devices/push": s.APIList, "DELETE /devices/push/{deviceId}": s.APIUnregister,
		"GET /push/prefs": s.APIPrefs, "PUT /push/prefs": s.APISetPrefs, "POST /push/test": s.APITest,
		"GET /push/config": s.APIConfig, "PUT /push/config": s.APISetConfig, "DELETE /push/config": s.APIDeleteConfig,
		"GET /push/devices": s.APIAdminDevices, "DELETE /push/devices/{user}": s.APIAdminForget,
		"DELETE /push/devices/{user}/{deviceId}": s.APIAdminForget,
		"POST /notify":                           s.APINotify,
	} {
		r.mux.HandleFunc(pat, h)
	}
	return r
}

func (r *rig) call(p auth.Principal, method, path string, body any) (int, map[string]any, http.Header) {
	r.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

// register enrolls a device for p with a fresh X25519 key.
func (r *rig) register(p auth.Principal, deviceID, handle string, kinds ...string) {
	r.t.Helper()
	k, _ := ecdh.X25519().GenerateKey(nil)
	r.keys[handle] = k
	body := map[string]any{"deviceId": deviceID, "handle": handle, "publicKey": b64.EncodeToString(k.PublicKey().Bytes())}
	if kinds != nil {
		body["kinds"] = kinds
	}
	if code, out, _ := r.call(p, "POST", "/devices/push", body); code != 200 {
		r.t.Fatalf("register: %d %v", code, out)
	}
}

// open decrypts a push with the key registered for its handle.
func (r *rig) open(p relayPush) Payload {
	r.t.Helper()
	pt, err := Open(r.keys[p.Handle], p.Envelope)
	if err != nil {
		r.t.Fatalf("open: %v", err)
	}
	var out Payload
	if err := json.Unmarshal(pt, &out); err != nil {
		r.t.Fatal(err)
	}
	return out
}

func TestNotifyDeliversSealedToReaders(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-alice-1")
	code, out, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "Standup", "body": "in 5 min",
		"link": "#event=42", "kind": "reminder", "collapseId": "ev42"})
	if code != 202 {
		t.Fatalf("notify: %d %v", code, out)
	}
	got := r.relay.waitPushes(1)
	if got[0].Key != "k1" || got[0].Handle != "handle-alice-1" || got[0].CollapseID == "" || strings.Contains(got[0].CollapseID, "ev42") {
		t.Fatalf("relay saw %+v", got[0])
	}
	p := r.open(got[0])
	want := Payload{V: 1, WS: r.s.Workspace(), Kind: "tile.reminder", Title: "Standup", Body: "in 5 min", Link: "c/apps/cal/#event=42", CollapseID: "tile:apps/cal:ev42"}
	if p != want {
		t.Fatalf("payload %+v\nwant    %+v", p, want)
	}
	// "user:<id>" (X-XBin-From style) names the same person
	if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "user:alice", "title": "again"}); code != 202 {
		t.Fatal(code)
	}
	r.relay.waitPushes(2)
}

func TestNotifyAuthorization(t *testing.T) {
	r := newRig(t, nil)
	r.register(bob, "phone", "handle-bob-1")
	for _, c := range []struct {
		p    auth.Principal
		body map[string]any
		want int
	}{
		{cal, map[string]any{"user": "bob", "title": "x"}, 403},     // bob cannot read apps/cal
		{cal, map[string]any{"user": "mallory", "title": "x"}, 403}, // unknown user: the same answer
		{alice, map[string]any{"user": "bob", "title": "x"}, 403},   // a person is not a tile
		{owner, map[string]any{"user": "bob", "title": "x"}, 403},   // nor is the owner token
		{cal, map[string]any{"user": "alice", "title": ""}, 400},    // title required
		{cal, map[string]any{"user": "alice", "title": "x", "link": "https://evil"}, 400},
		{cal, map[string]any{"user": "alice", "title": "x", "link": "/c/apps/other/"}, 400},
		{cal, map[string]any{"user": "alice", "title": "x", "link": "a/../../other"}, 400},
		{cal, map[string]any{"user": "alice", "title": "x", "link": "%2e%2e/%2e%2e/login"}, 400},
		{cal, map[string]any{"user": "alice", "title": "x", "link": ".%2E/other/"}, 400},
		{cal, map[string]any{"user": "alice", "title": "x", "kind": "Bad Kind"}, 400},
		{cal, map[string]any{"user": "alice", "title": "x", "collapseId": strings.Repeat("c", 65)}, 400},
		{other, map[string]any{"user": "bob", "title": "x"}, 202}, // bob reads apps/other
		{cal, map[string]any{"user": OwnerUser, "title": "x"}, 202},
	} {
		if code, out, _ := r.call(c.p, "POST", "/notify", c.body); code != c.want {
			t.Errorf("%s → %v: %d %v, want %d", c.p.From(), c.body, code, out, c.want)
		}
	}
	got := r.relay.waitPushes(1)
	time.Sleep(20 * time.Millisecond)
	if got = r.relay.pushes(); len(got) != 1 || r.open(got[0]).Link != "c/apps/other/" {
		t.Fatalf("only the authorised notification may reach bob: %d pushes", len(got))
	}
}

func TestNotifyRateLimits(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.Limits = Limits{Tile: Rate{PerHour: 3600, Burst: 2}, User: Rate{PerHour: 3600, Burst: 3}, Session: Rate{PerHour: 1, Burst: 1}}
	})
	r.register(alice, "phone", "handle-phone")
	send := func(p auth.Principal) (int, http.Header) {
		code, _, h := r.call(p, "POST", "/notify", map[string]any{"user": "alice", "title": "x"})
		return code, h
	}
	for i := 0; i < 2; i++ {
		if code, _ := send(cal); code != 202 {
			t.Fatalf("call %d: %d", i, code)
		}
	}
	code, h := send(cal)
	if code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("tile limit: %d, Retry-After %q", code, h.Get("Retry-After"))
	}
	if code, _ := send(other); code != 202 { // another tile: its own bucket; alice's third
		t.Fatal(code)
	}
	// alice's budget for tiles is spent: accepted, and dropped quietly — a
	// 429 here would tell apps/other how much apps/cal sends her
	if code, _ := send(other); code != 202 {
		t.Fatalf("over the per-user limit: %d", code)
	}
	r.relay.waitPushes(3)
	time.Sleep(20 * time.Millisecond)
	if n, st := len(r.relay.pushes()), r.s.snd.stats(); n != 3 || st.Limited != 1 {
		t.Fatalf("%d pushes, stats %+v", n, st)
	}
}

func TestMuteAndKinds(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-phone", "agent")
	r.register(alice, "ipad", "handle-ipad")
	code, out, _ := r.call(alice, "PUT", "/push/prefs", map[string]any{"mutedTiles": []string{"apps/cal/", "apps/cal"}})
	if code != 200 || len(out["mutedTiles"].([]any)) != 1 {
		t.Fatalf("prefs: %d %v", code, out)
	}
	if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "muted"}); code != 202 {
		t.Fatalf("a muted tile's notify still answers 202, got %d", code)
	}
	if code, _, _ := r.call(other, "POST", "/notify", map[string]any{"user": "alice", "title": "heard"}); code != 202 {
		t.Fatal(code)
	}
	got := r.relay.waitPushes(1)
	time.Sleep(20 * time.Millisecond)
	got = r.relay.pushes()
	if len(got) != 1 || got[0].Handle != "handle-ipad" || r.open(got[0]).Title != "heard" {
		t.Fatalf("want only the unmuted tile, to the ipad (the phone takes agent only): %+v", got)
	}
	r.s.AgentEvent("alice", "s1", "apps/cal", agent.New(agent.EvTurnEnd, map[string]any{"turn": 1, "stopReason": "end_turn"}))
	got = r.relay.waitPushes(3)
	if got[1].Handle == got[2].Handle {
		t.Fatal("an agent push went to one device twice")
	}
	for _, p := range got[1:] {
		if pl := r.open(p); pl.Kind != KindAgentTurn || pl.Link != "agent/s1" {
			t.Fatalf("agent push %+v", pl)
		}
	}
	if code, out, _ := r.call(alice, "GET", "/push/prefs", nil); code != 200 || out["mutedTiles"].([]any)[0] != "apps/cal" {
		t.Fatalf("prefs read back: %v", out)
	}
}

func TestAgentEvents(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Grace = 80 * time.Millisecond })
	r.register(alice, "phone", "handle-phone")
	ev := func(typ string, d map[string]any) agent.Event { return agent.New(typ, d) }
	// answered within the grace period (a session rule, or someone at the desk): nothing
	r.s.AgentEvent("alice", "s1", "apps/cal", ev(agent.EvPermissionRequest, map[string]any{"pid": "p1", "toolCall": map[string]any{"title": "rm -rf build"}}))
	r.s.AgentEvent("alice", "s1", "apps/cal", ev(agent.EvPermissionResolved, map[string]any{"pid": "p1", "by": "auto"}))
	// unanswered: pushed after the grace period
	r.s.AgentEvent("alice", "s1", "apps/cal", ev(agent.EvPermissionRequest, map[string]any{"pid": "p2", "toolCall": map[string]any{"title": "go test ./..."}}))
	r.s.AgentEvent("alice", "s1", "apps/cal", ev(agent.EvElicitRequest, map[string]any{"eid": "e1", "message": "Which branch?"}))
	r.s.AgentEvent("alice", "s1", "apps/cal", ev(agent.EvTurnEnd, map[string]any{"turn": 1, "stopReason": "cancelled"}))
	r.s.AgentEvent("alice", "s1", "apps/cal", ev(agent.EvTurnEnd, map[string]any{"turn": 2, "stopReason": "max_tokens"}))
	r.s.AgentEvent("alice", "s1", "apps/cal", ev(agent.EvMessageDelta, map[string]any{"text": "hi"}))
	got := r.relay.waitPushes(3)
	time.Sleep(150 * time.Millisecond)
	got = r.relay.pushes()
	if len(got) != 3 {
		t.Fatalf("want 3 pushes (p2, e1, the max_tokens turn), got %d", len(got))
	}
	byKind := map[string]Payload{}
	for _, p := range got {
		pl := r.open(p)
		byKind[pl.Kind] = pl
	}
	if p := byKind[KindAgentPermission]; p.Body != "go test ./..." || p.CollapseID != "agent:s1:perm:p2" || p.Link != "agent/s1" || !strings.Contains(p.Title, "cal") {
		t.Fatalf("permission push %+v", p)
	}
	if p := byKind[KindAgentQuestion]; p.Body != "Which branch?" || p.CollapseID != "agent:s1:q:e1" {
		t.Fatalf("question push %+v", p)
	}
	if p := byKind[KindAgentTurn]; !strings.HasPrefix(p.Title, "Agent stopped") || !strings.Contains(p.Body, "max_tokens") {
		t.Fatalf("turn push %+v", p)
	}
}

func TestRelayAnswers(t *testing.T) {
	r := newRig(t, nil)
	r.register(alice, "phone", "handle-phone")
	r.register(alice, "ipad", "handle-ipad")
	// 503, 429 retry; then delivered
	r.relay.answer(codes(503, 429)...)
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "one"})
	r.relay.waitPushes(2)
	st := r.s.snd.stats()
	if st.Retried != 2 || st.Sent != 2 {
		t.Fatalf("stats after retries: %+v", st)
	}
	// 410: the registration goes, the other device stays
	r.relay.answer(codes(410)...)
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "two"})
	r.relay.waitPushes(3)
	deadline := time.Now().Add(2 * time.Second)
	for len(r.s.st.devices("alice")) != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(r.s.st.devices("alice")); n != 1 {
		t.Fatalf("a dead handle's registration stayed: %d devices", n)
	}
	// retries end
	r.relay.answer(codes(502, 502, 502, 502, 502)...)
	r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "three"})
	deadline = time.Now().Add(2 * time.Second)
	for r.s.snd.stats().Failed < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if st := r.s.snd.stats(); st.Failed != 2 || st.LastError == "" {
		t.Fatalf("after giving up: %+v", st)
	}
}

func TestQueueNeverBlocksCallers(t *testing.T) {
	r := newRig(t, func(o *Options) {
		o.QueueSize, o.Workers = 2, 1
		o.Limits = Limits{Tile: Rate{PerHour: 1e6, Burst: 1000}, User: Rate{PerHour: 1e6, Burst: 1000}, Session: Rate{PerHour: 1, Burst: 1}}
	})
	r.relay.block = make(chan struct{})
	r.register(alice, "phone", "handle-phone")
	start := time.Now()
	for i := 0; i < 50; i++ {
		if code, _, _ := r.call(cal, "POST", "/notify", map[string]any{"user": "alice", "title": "x"}); code != 202 {
			t.Fatalf("call %d: %d", i, code)
		}
		r.s.AgentEvent("alice", "s1", "apps/cal", agent.New(agent.EvTurnEnd, map[string]any{"stopReason": "end_turn"}))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("callers waited on a stuck relay: %v", d)
	}
	if st := r.s.snd.stats(); st.Dropped == 0 {
		t.Fatalf("a full queue should drop: %+v", st)
	}
	close(r.relay.block)
}

func TestRegistrations(t *testing.T) {
	r := newRig(t, func(o *Options) { o.Limits = DefaultLimits; o.Limits.Register = Rate{} }) // TestRegisterLimit

	k, _ := ecdh.X25519().GenerateKey(nil)
	pub := b64.EncodeToString(k.PublicKey().Bytes())
	for _, c := range []struct {
		p    auth.Principal
		body map[string]any
		want int
	}{
		{cal, map[string]any{"deviceId": "d", "handle": "handle-123", "publicKey": pub}, 403},
		{alice, map[string]any{"deviceId": "", "handle": "handle-123", "publicKey": pub}, 400},
		{alice, map[string]any{"deviceId": "d", "handle": "h", "publicKey": pub}, 400},
		{alice, map[string]any{"deviceId": "d", "handle": "handle-123", "publicKey": "AAAA"}, 400},
		{alice, map[string]any{"deviceId": "d", "handle": "handle-123", "publicKey": b64.EncodeToString(make([]byte, 32))}, 400},
		{alice, map[string]any{"deviceId": "d", "handle": "handle-123", "publicKey": pub, "kinds": []string{"Agent!"}}, 400},
		{alice, map[string]any{"deviceId": "d", "handle": "handle-123", "publicKey": pub, "kinds": []string{"agent", "agent"}, "future": true}, 200},
	} {
		if code, out, _ := r.call(c.p, "POST", "/devices/push", c.body); code != c.want {
			t.Errorf("%v: %d %v, want %d", c.body, code, out, c.want)
		}
	}
	code, out, _ := r.call(alice, "GET", "/devices/push", nil)
	devs := out["devices"].([]any)
	if code != 200 || len(devs) != 1 || out["workspace"] != r.s.Workspace() || out["enabled"] != true {
		t.Fatalf("list: %d %v", code, out)
	}
	if d := devs[0].(map[string]any); len(d["kinds"].([]any)) != 1 || d["publicKey"] != nil || d["handle"] != nil {
		t.Fatalf("device view %v", d)
	}
	// the same device again: updated in place
	r.call(alice, "POST", "/devices/push", map[string]any{"deviceId": "d", "handle": "handle-456", "publicKey": pub})
	if ds := r.s.st.devices("alice"); len(ds) != 1 || ds[0].Handle != "handle-456" || ds[0].Kinds != nil {
		t.Fatalf("upsert: %+v", ds)
	}
	// the handle now signed in as bob: alice's registration goes
	r.call(bob, "POST", "/devices/push", map[string]any{"deviceId": "d", "handle": "handle-456", "publicKey": pub})
	if len(r.s.st.devices("alice")) != 0 || len(r.s.st.devices("bob")) != 1 {
		t.Fatal("a handle held by two registrations")
	}
	// the cap evicts the stalest
	for i := 0; i < maxDevicesPerUser+3; i++ {
		r.call(alice, "POST", "/devices/push", map[string]any{"deviceId": "dev" + string(rune('a'+i)), "handle": "handle-x" + string(rune('a'+i)), "publicKey": pub})
	}
	if n := len(r.s.st.devices("alice")); n != maxDevicesPerUser {
		t.Fatalf("%d devices, want the cap %d", n, maxDevicesPerUser)
	}
	if code, _, _ := r.call(bob, "DELETE", "/devices/push/d", nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := r.call(bob, "DELETE", "/devices/push/d", nil); code != 404 {
		t.Fatalf("delete again: %d", code)
	}
	if r.s.ForgetDevice("alice", "deva") { // the stalest: evicted by the cap
		t.Fatal("the cap kept the oldest registration")
	}
	last := "dev" + string(rune('a'+maxDevicesPerUser+2))
	if !r.s.ForgetDevice("alice", last) || r.s.ForgetDevice("alice", last) {
		t.Fatal("ForgetDevice")
	}
	// the store survives a reopen, private
	raw, err := os.ReadFile(filepath.Join(r.s.o.Dir, "push.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(r.s.o.Dir, "push.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("push.json mode %v", fi.Mode().Perm())
	}
	s2, err := New(Options{Dir: r.s.o.Dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if s2.Workspace() != r.s.Workspace() || len(s2.st.devices("alice")) != maxDevicesPerUser-1 {
		t.Fatalf("reopen lost state: %s", raw)
	}
}

func TestPushTestRoute(t *testing.T) {
	r := newRig(t, nil)
	if code, _, _ := r.call(alice, "POST", "/push/test", nil); code != 409 {
		t.Fatalf("no devices: %d", code)
	}
	r.register(alice, "phone", "handle-phone", "tile")
	if code, out, _ := r.call(alice, "POST", "/push/test", nil); code != 202 || out["devices"] != float64(1) {
		t.Fatalf("test: %d %v", code, out)
	}
	if p := r.open(r.relay.waitPushes(1)[0]); p.Kind != KindTest {
		t.Fatalf("test push %+v (kinds never filter a test)", p)
	}
}

func TestFitAndLinks(t *testing.T) {
	huge := strings.Repeat("字", 5000)
	pt, err := fit(Payload{V: 1, WS: "w", Kind: KindTile, Title: huge, Body: huge, Link: "c/apps/x/" + strings.Repeat("a", 600)})
	if err != nil || len(pt) > maxPlain {
		t.Fatalf("fit: %d bytes, %v", len(pt), err)
	}
	env, _ := Seal(mustEphemeral().PublicKey().Bytes(), pt)
	if len(env.CT) > 3200 {
		t.Fatalf("sealed ct %d characters — over the relay's limit", len(env.CT))
	}
	var p Payload
	_ = json.Unmarshal(pt, &p)
	if p.Link != "" || !strings.HasSuffix(p.Title, "…") {
		t.Fatalf("fit kept an oversized link or did not mark the cut: %q", p.Link)
	}
	for l, ok := range map[string]bool{"": true, "#x": true, "?a=b#c": true, "sub/page.html": true, "a:b": false,
		"javascript:alert(1)": false, "//evil": false, "/abs": false, "../up": false, "a/./b": false, "sp ace": false,
		"x\ny": false, "ok/a:b": true, "#frag:with:colons": true,
		// percent-encoded dot segments: a URL parser resolves them like ".."
		"%2e%2e/%2e%2e/login": false, ".%2E/other/": false, "%2E%2E/%2E%2E/%2E%2E/api/xbin/users": false,
		"a/%2e/b": false, "a/%2E%2e": false, "a%2fb": false, "a%5Cb": false, "bad%zz": false,
		"%252e%252e/x": true, "a%20b/c.d": true, "..x/y": true} {
		if _, got := tileLink(l); got != ok {
			t.Errorf("tileLink(%q) = %v, want %v", l, got, ok)
		}
	}
	for k, want := range map[string]bool{"agent.turn": true, "agent": true, "tile.alert": false, "test": true} {
		if got := kindAllowed([]string{"agent"}, k); got != want {
			t.Errorf("kindAllowed([agent], %q) = %v", k, got)
		}
	}
	if kindAllowed([]string{"agent.turn"}, "agent.turnover") {
		t.Error("prefix match must stop at a dot")
	}
}
