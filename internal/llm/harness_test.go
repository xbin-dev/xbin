package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- a fake upstream ---------------------------------------------------------

// upstreamKey is the key the tests give their upstream. No test may find
// it anywhere but in the upstream's own Authorization header.
const upstreamKey = "sk-test-UPSTREAM-SECRET-0123456789abcdef"

// fakeUpstream is an OpenAI-compatible server the tests script.
type fakeUpstream struct {
	*httptest.Server
	t *testing.T

	mu       sync.Mutex
	auth     []string                                          // every Authorization header seen
	bodies   []map[string]any                                  // every request body
	headers  []http.Header                                     // every request's headers
	canceled chan struct{}                                     // closed when a held call's client went away
	hold     chan struct{}                                     // a streamed call waits on it between its first and last chunks (nil: no wait)
	usage    map[string]any                                    // the usage a call reports (nil: 100 in, 20 out)
	fail     func(w http.ResponseWriter, r *http.Request) bool // answers instead, when it returns true
	models   []map[string]any
	modelsN  int
}

func newUpstream(t *testing.T) *fakeUpstream {
	f := &fakeUpstream{t: t, canceled: make(chan struct{}),
		models: []map[string]any{{"id": "gpt-5", "object": "model", "context_length": 400000}, {"id": "gpt-5-mini", "object": "model"}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		f.note(r, nil)
		f.mu.Lock()
		f.modelsN++
		models := f.models
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"object": "list", "data": models})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) { f.call(w, r, true) })
	mux.HandleFunc("POST /v1/responses", func(w http.ResponseWriter, r *http.Request) { f.call(w, r, false) })
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeUpstream) note(r *http.Request, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.headers = append(f.headers, r.Header.Clone())
	if body != nil {
		f.bodies = append(f.bodies, body)
	}
}

func (f *fakeUpstream) call(w http.ResponseWriter, r *http.Request, chat bool) {
	var body map[string]any
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	f.note(r, body)
	f.mu.Lock()
	fail, hold, usage := f.fail, f.hold, f.usage
	f.mu.Unlock()
	if fail != nil && fail(w, r) {
		return
	}
	if usage == nil {
		usage = map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120}
		if !chat {
			usage = map[string]any{"input_tokens": 100, "output_tokens": 20, "total_tokens": 120}
		}
	}
	if body["stream"] != true {
		if chat {
			writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ok"}, "finish_reason": "stop"}}, "usage": usage})
		} else {
			writeJSON(w, 200, map[string]any{"id": "resp_1", "status": "completed", "output": []any{}, "usage": usage})
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Set-Cookie", "upstream=1")
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		fl.Flush()
	}
	if chat {
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "first"}}}})
	} else {
		send(map[string]any{"type": "response.output_text.delta", "delta": "first"})
	}
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			close(f.canceled)
			return
		}
	}
	if chat {
		send(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": " last"}, "finish_reason": "stop"}}})
		if so, _ := body["stream_options"].(map[string]any); so["include_usage"] == true {
			send(map[string]any{"choices": []any{}, "usage": usage})
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	} else {
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_1", "status": "completed", "output": []any{}, "usage": usage}})
	}
	fl.Flush()
}

func (f *fakeUpstream) lastBody() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return nil
	}
	return f.bodies[len(f.bodies)-1]
}

func (f *fakeUpstream) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// ---- a fake vault ------------------------------------------------------------

// fakeVault stands in for the barrier: "encryption" that is visibly not the
// plaintext, and a seal switch.
type fakeVault struct {
	mu           sync.Mutex
	inited, seal bool
}

func (v *fakeVault) Initialized() bool { v.mu.Lock(); defer v.mu.Unlock(); return v.inited }
func (v *fakeVault) Sealed() bool      { v.mu.Lock(); defer v.mu.Unlock(); return v.seal || !v.inited }
func (v *fakeVault) set(inited, sealed bool) {
	v.mu.Lock()
	v.inited, v.seal = inited, sealed
	v.mu.Unlock()
}
func (v *fakeVault) EncryptFor(label string, pt []byte) ([]byte, error) {
	if v.Sealed() {
		return nil, ErrVaultSealed
	}
	out := []byte(label + ":")
	for _, b := range pt {
		out = append(out, b^0x5a)
	}
	return out, nil
}
func (v *fakeVault) DecryptFor(label string, ct []byte) ([]byte, error) {
	if v.Sealed() {
		return nil, ErrVaultSealed
	}
	rest, ok := bytes.CutPrefix(ct, []byte(label+":"))
	if !ok {
		return nil, fmt.Errorf("wrong label")
	}
	out := make([]byte, len(rest))
	for i, b := range rest {
		out[i] = b ^ 0x5a
	}
	return out, nil
}

// ---- the connector under test --------------------------------------------------

type rig struct {
	t     *testing.T
	s     *Service
	mux   *http.ServeMux
	dir   string
	vault *fakeVault
	logs  *bytes.Buffer
	clock *clock
	bound map[string]bool
	orgs  map[string][]string
	keys  *Keys
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type muxReg struct{ mux *http.ServeMux }

func (m muxReg) RegisterAPI(p string, h http.HandlerFunc) { m.mux.HandleFunc(p, h) }

type lockedBuf struct {
	mu sync.Mutex
	b  *bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// newRig starts a connector on a fresh data dir. hostFile "" = none.
func newRig(t *testing.T, hostFile string) *rig {
	t.Helper()
	return newRigAt(t, t.TempDir(), hostFile, &fakeVault{inited: true})
}

func newRigAt(t *testing.T, dir, hostFile string, v *fakeVault) *rig {
	t.Helper()
	r := &rig{t: t, dir: dir, vault: v, logs: &bytes.Buffer{}, clock: &clock{t: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)},
		bound: map[string]bool{}, orgs: map[string][]string{}}
	log := slog.New(slog.NewTextHandler(&lockedBuf{b: r.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s, err := New(Options{Dir: dir, HostFile: hostFile, Vault: v, Log: log, Now: r.clock.now,
		IsAdmin: func(p auth.Principal) bool { return p.IsAdmin() || p.Component == "apps/admin" },
		Bound:   func(c string) bool { return r.bound[c] },
		OrgsOf:  func(u string) []string { return r.orgs[u] }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	r.s, r.keys, r.mux = s, s.Keys(), newMux(s)
	return r
}

func newMux(s *Service) *http.ServeMux {
	mux := http.NewServeMux()
	s.mount(muxReg{mux})
	return mux
}

// The principals of the authz matrix.
var (
	pOwner = auth.Principal{Owner: true, Via: "bearer"}
	pAdmin = auth.Principal{UserID: "ada", User: &users.User{ID: "ada", Role: "admin"}, Via: "session"}
)

func pUser(id string) auth.Principal {
	return auth.Principal{UserID: id, User: &users.User{ID: id, Role: "user"}, Via: "session"}
}
func pFrame(tile, user string) auth.Principal {
	return auth.Principal{Component: tile, UserID: user, Via: "frame"}
}
func pInstance(tile string) auth.Principal { return auth.Principal{Component: tile, Via: "instance"} }
func pPartition(tile, user string) auth.Principal {
	return auth.Principal{Component: tile, Via: "instance", Partition: util.Partition("user:" + user)}
}

type answer struct {
	code int
	h    http.Header
	body []byte
}

func (a answer) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.body, &m); err != nil {
		t.Fatalf("answer is not JSON (%d): %s", a.code, a.body)
	}
	return m
}

// errCode is the code of an OpenAI-shaped error answer.
func (a answer) errCode(t *testing.T) string {
	t.Helper()
	e, _ := a.json(t)["error"].(map[string]any)
	if e == nil {
		t.Fatalf("answer %d is not an OpenAI-shaped error: %s", a.code, a.body)
	}
	if _, has := e["message"].(string); !has || e["type"] == nil {
		t.Fatalf("error without message or type: %s", a.body)
	}
	c, _ := e["code"].(string)
	return c
}

func (r *rig) req(p auth.Principal, method, path string, body any) *http.Request {
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req.WithContext(auth.WithPrincipal(req.Context(), p))
}

// do runs one request as p through the connector's routes.
func (r *rig) do(p auth.Principal, method, path string, body any) answer {
	r.t.Helper()
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, r.req(p, method, path, body))
	return answer{rec.Code, rec.Header(), rec.Body.Bytes()}
}

// must is do, failing the test on another status.
func (r *rig) must(want int, p auth.Principal, method, path string, body any) answer {
	r.t.Helper()
	a := r.do(p, method, path, body)
	if a.code != want {
		r.t.Fatalf("%s %s = %d, want %d: %s", method, path, a.code, want, a.body)
	}
	return a
}

// addUpstream adds an admin upstream named name in front of up.
func (r *rig) addUpstream(name string, up *fakeUpstream, extra map[string]any) {
	r.t.Helper()
	body := map[string]any{"baseURL": up.URL + "/v1", "key": upstreamKey}
	for k, v := range extra {
		body[k] = v
	}
	r.must(200, pOwner, "PUT", "/llm/upstreams/"+name, body)
}

func chatBody(model string, stream bool) map[string]any {
	b := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	if model != "" {
		b["model"] = model
	}
	if stream {
		b["stream"] = true
	}
	return b
}

// usageRows reads GET /llm/usage as the owner.
func (r *rig) usageRows(query string) []UsageRow {
	r.t.Helper()
	var out struct {
		Rows []UsageRow `json:"rows"`
	}
	a := r.must(200, pOwner, "GET", "/llm/usage"+query, nil)
	if err := json.Unmarshal(a.body, &out); err != nil {
		r.t.Fatal(err)
	}
	return out.Rows
}

// writeHost writes a deployment file (and its key file) and returns its path.
func writeHost(t *testing.T, dir, config, key string) string {
	t.Helper()
	if key != "" {
		if err := os.WriteFile(filepath.Join(dir, "upstream.key"), []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "llm.jsonc")
	if err := os.WriteFile(p, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// noSecret fails the test when the upstream key shows up in what was
// answered, logged or stored outside the sealed keys file.
func (r *rig) noSecret(what string, data []byte) {
	r.t.Helper()
	if bytes.Contains(data, []byte(upstreamKey)) {
		r.t.Errorf("the upstream key appears in %s", what)
	}
}
