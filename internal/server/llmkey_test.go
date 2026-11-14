package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
)

// covers D226 — an LLM key is good for the connector's OpenAI-compatible
// routes and nothing else: there it runs the request as a marker principal
// (Via "llm"), everywhere else it is a 401 — beside a valid credential too
// — and a dead key, or no credential at all, is answered on those routes
// in OpenAI's error shape.
func TestLLMKeyGate(t *testing.T) {
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Auth: a, Hub: events.NewHub()}
	s.LLMKey = func(tok string) (auth.Principal, bool) {
		// a resolver that answers more than a marker: the gate keeps the marker only
		return auth.Principal{UserID: "kim", Owner: true, Component: "apps/x", Via: "bearer"}, tok == "xbl_live"
	}
	var seen []auth.Principal
	probe := func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, auth.PrincipalOf(r))
		w.WriteHeader(http.StatusNoContent)
	}
	s.RegisterAPI("POST /llm/v1/chat/completions", probe)
	s.RegisterAPI("GET /llm/v1/models", probe)
	s.RegisterAPI("GET /llm/config", probe)
	h := s.Handler()
	do := func(method, path, bearer string, cookie bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie {
			r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: a.OwnerTokenValue()})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	openAIError := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		var body struct {
			Error struct{ Message, Type, Code string } `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 401 || body.Error.Message == "" || body.Error.Type != "authentication_error" || body.Error.Code != "invalid_api_key" {
			t.Errorf("%s: %d %s", what, w.Code, w.Body)
		}
	}

	if w := do("POST", "/api/xbin/llm/v1/chat/completions", "xbl_live", false); w.Code != http.StatusNoContent {
		t.Fatalf("a live key on the LLM plane: %d %s", w.Code, w.Body)
	}
	if len(seen) != 1 || seen[0].Via != "llm" || seen[0].UserID != "kim" || seen[0].Owner || seen[0].Component != "" || seen[0].IsAdmin() {
		t.Fatalf("the principal a key runs as: %+v", seen)
	}
	openAIError("a dead key", do("GET", "/api/xbin/llm/v1/models", "xbl_dead", false))
	openAIError("a dead key beside a valid cookie", do("GET", "/api/xbin/llm/v1/models", "xbl_dead", true))
	openAIError("no credential at all", do("POST", "/api/xbin/llm/v1/chat/completions", "", false))
	// everywhere else: 401, in xbind's own shape, whatever rides beside it
	for _, path := range []string{"/api/xbin/llm/config", "/api/xbin/whoami", "/api/xbin/status", "/api/apps/x/thing", "/api/xbin/llm/v1%2Fmodels", "/api/xbin/llm/v1/../config"} {
		for _, cookie := range []bool{false, true} {
			w := do("GET", path, "xbl_live", cookie)
			if w.Code != 401 && w.Code != 307 { // the mux redirects a plain dot segment before any handler
				t.Errorf("a live key on %s (cookie %v): %d %s", path, cookie, w.Code, w.Body)
			}
		}
	}
	if len(seen) != 1 {
		t.Errorf("a key reached a handler outside the LLM plane: %+v", seen)
	}
	// the owner's own credentials reach the plane as themselves
	if w := do("GET", "/api/xbin/llm/v1/models", a.OwnerTokenValue(), false); w.Code != http.StatusNoContent || !seen[len(seen)-1].Owner {
		t.Errorf("the owner token on the LLM plane: %d, %+v", w.Code, seen[len(seen)-1])
	}
	// no resolver installed: no key is valid
	s.LLMKey = nil
	openAIError("a key with no connector", do("GET", "/api/xbin/llm/v1/models", "xbl_live", false))
	// a model call is the data plane: metered, not audited — and its writer still flushes
	if auditable("POST", "/llm/v1/chat/completions") || auditable("POST", "/llm/v1/responses") || !auditable("PUT", "/llm/config") || !auditable("PUT", "/llm/upstreams/x") {
		t.Error("audit: model calls are not audited; the connector's settings are")
	}
}

// deadlineWriter records the write deadline it is given.
type deadlineWriter struct {
	http.ResponseWriter
	set bool
}

func (d *deadlineWriter) SetWriteDeadline(time.Time) error { d.set = true; return nil }

// covers D226 — the request counter every response passes through lets a
// streaming handler set write deadlines on the connection beneath it.
func TestCountingWriterPassesWriteDeadlines(t *testing.T) {
	inner := &deadlineWriter{ResponseWriter: httptest.NewRecorder()}
	rc := http.NewResponseController(&countingWriter{inner})
	if err := rc.SetWriteDeadline(time.Now().Add(time.Second)); err != nil || !inner.set {
		t.Errorf("SetWriteDeadline through the counter: %v (reached the connection: %v)", err, inner.set)
	}
	if err := rc.Flush(); err != nil {
		t.Errorf("Flush through the counter: %v", err)
	}
}
