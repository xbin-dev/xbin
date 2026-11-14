package xbin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// CallerInfo.Agent comes from X-XBin-Agent: a call a person's workspace
// agent makes for them. Without the header every field reads as before. The
// call arrives as user:<id> at the person's level — never the owner, never
// admin — and the role guard judges it by that role.
func TestCallerAgent(t *testing.T) {
	r := httptest.NewRequest("POST", "/send", nil)
	r.Header.Set("X-XBin-From", "user:alice")
	r.Header.Set("X-XBin-Role", "writer")
	r.Header.Set("X-XBin-User", "alice")
	r.Header.Set("X-XBin-User-Level", "write")
	own := Caller(r)
	if own.Agent != "" || own.ByAgent() {
		t.Errorf("without the header: %+v", own)
	}
	r.Header.Set("X-XBin-Agent", "xbin/agent")
	c := Caller(r)
	if c.Agent != "xbin/agent" || !c.ByAgent() || c.Owner || c.User != "alice" || !c.UserCanWrite() {
		t.Errorf("an agent's call: %+v", c)
	}
	own.Agent = c.Agent
	if own != c {
		t.Errorf("the header changed another field: %+v vs %+v", own, c)
	}
	ran := false
	h := Role("admin", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { ran = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if ran || rec.Code != http.StatusForbidden {
		t.Errorf("an agent's writer call passed an admin guard: %d", rec.Code)
	}
}
