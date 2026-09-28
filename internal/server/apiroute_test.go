package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The inner /api/xbin mux routes the escaped path, so the inner URL's Path
// and RawPath must be one path: an encoded "/" is part of its segment's
// value, never a separator that reaches another route.
func TestAPIKeepsEncodedSlash(t *testing.T) {
	s := &Server{}
	seen := func(tag string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			v := r.PathValue("name")
			if v == "" {
				v = r.PathValue("rest")
			}
			_, _ = w.Write([]byte(tag + "|" + v + "|" + r.URL.Path + "|" + r.URL.EscapedPath()))
		}
	}
	s.RegisterAPI("GET /probe/{name}", seen("one"))
	s.RegisterAPI("GET /probe/{name}/stop", seen("stop"))
	s.RegisterAPI("GET /kv/{rest...}", seen("kv"))
	s.RegisterAPI("GET /{$}", seen("root"))

	for _, c := range []struct{ path, want string }{
		{"/api/xbin/probe/x%2Fstop", "one|x/stop|/probe/x/stop|/probe/x%2Fstop"},
		{"/api/xbin/probe/x%2fstop", "one|x/stop|/probe/x/stop|/probe/x%2fstop"},
		{"/api/xbin/probe/x/stop", "stop|x|/probe/x/stop|/probe/x/stop"},
		{"/api/xbin/probe/a%20b", "one|a b|/probe/a b|/probe/a%20b"},
		{"/api/%78bin/probe/x%2Fstop", "one|x/stop|/probe/x/stop|/probe/x%2Fstop"}, // an encoded letter of the prefix is still the prefix
		{"/api/xbin/kv/res:a/k%2Fv", "kv|res:a/k/v|/kv/res:a/k/v|/kv/res:a/k%2Fv"}, // a {rest...} value is what it always was
		{"/api/xbin/kv/res:a/k/v", "kv|res:a/k/v|/kv/res:a/k/v|/kv/res:a/k/v"},
		{"/api/xbin", "root||/|/"},
		{"/api/xbin/", "root||/|/"},
	} {
		w := httptest.NewRecorder()
		s.handleAPI(w, httptest.NewRequest("GET", c.path, nil))
		if w.Code != http.StatusOK || w.Body.String() != c.want {
			t.Errorf("%s: %d %q, want %q", c.path, w.Code, w.Body, c.want)
		}
	}

	// A "/" encoded inside the prefix: nothing to route (as StripPrefix).
	w := httptest.NewRecorder()
	s.handleAPI(w, httptest.NewRequest("GET", "/api/xbin%2Fprobe/x", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("an encoded / in the prefix: %d, want 404", w.Code)
	}

	// An encoded dot segment is refused: no handler sees "." or "..", as
	// none did while the inner mux cleaned the decoded path into a redirect.
	for _, p := range []string{
		"/api/xbin/probe/%2E%2E",
		"/api/xbin/probe/%2e",
		"/api/xbin/probe/x%2F..%2Fstop",
		"/api/xbin/kv/res:a/..%2F..%2Fk",
	} {
		w := httptest.NewRecorder()
		s.handleAPI(w, httptest.NewRequest("GET", p, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"refusal":"invalid"`) {
			t.Errorf("%s: %d %s, want 400 invalid", p, w.Code, w.Body)
		}
	}
}
