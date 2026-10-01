package acptest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --codex-auth signs in as codex 0.156 does: no session signed out, an API
// key through authenticate lands in auth.json AND in memory — removing the
// file leaves the running agent signed in, and a fresh start reads the file.
// The environment's CODEX_API_KEY is never read.
func TestCodexAuth(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, ".codex", "auth.json")
	o := Options{RequireLogin: true, CodexAuth: true, Persist: true, wait: quick}
	o.Getenv = func(n string) string { return map[string]string{"HOME": home, "CODEX_API_KEY": "sk-env-NOTREAD"}[n] }
	d := runServe(t, home, o)
	d.call(1, "initialize", initParams)
	d.response(1)
	d.call(2, "session/new", newParams)
	if r := d.response(2); get(r, "error", "code") != float64(-32000) {
		t.Fatalf("a session signed out: %s", r.raw)
	}
	d.call(3, "authenticate", `{"methodId":"fake-api-key","_meta":{"api-key":{"apiKey":"sk-proj-first-AAAA"}}}`)
	d.response(3)
	if b, err := os.ReadFile(file); err != nil || !strings.Contains(string(b), "sk-proj-first-AAAA") {
		t.Fatalf("auth.json after the key: %q %v", b, err)
	}
	sid := str(get(d.sessionNew(4, newParams), "result", "sessionId"))
	if err := os.Remove(file); err != nil { // the client removes it: the agent stays signed in
		t.Fatal(err)
	}
	d.call(5, "session/prompt", strings.Replace(promptParams("whoami"), "fake-1", sid, 1))
	d.chunkText("account: key …AAAA")
	d.response(5)
	d.finish()

	// a fresh start: signed out (the file is gone), until a file holds a key
	d = runServe(t, home, o)
	d.call(1, "initialize", initParams)
	d.response(1)
	d.call(2, "session/new", newParams)
	if r := d.response(2); get(r, "error", "code") != float64(-32000) {
		t.Fatalf("a fresh start without auth.json: %s", r.raw)
	}
	d.finish()
	if err := os.WriteFile(file, []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"sk-proj-file-BBBB"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d = runServe(t, home, o)
	d.call(1, "initialize", initParams)
	d.response(1)
	sid = str(get(d.sessionNew(2, newParams), "result", "sessionId"))
	d.call(3, "session/prompt", strings.Replace(promptParams("whoami"), "fake-1", sid, 1))
	d.chunkText("account: key …BBBB")
	d.response(3)
	d.finish()
}
