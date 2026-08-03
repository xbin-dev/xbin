package registry

import (
	"encoding/json"
	"testing"
)

// A filesystem resource can opt out of encryption-at-rest with {"plain": true}
// (D43 — container stores need real kernel POSIX semantics). The flag must
// parse on any type (the broker warns + ignores it off-filesystem) and stay
// absent from re-marshalled manifests when false.
func TestResourcePlainJSON(t *testing.T) {
	var sm ScopeManifest
	if err := json.Unmarshal([]byte(`{"resources":{
		"store":{"type":"filesystem","plain":true},
		"files":{"type":"filesystem"},
		"db":{"type":"sqlite","plain":true}}}`), &sm); err != nil {
		t.Fatal(err)
	}
	if r := sm.Resources["store"]; r.Type != "filesystem" || !r.Plain {
		t.Fatalf("plain filesystem resource: %+v", r)
	}
	if r := sm.Resources["files"]; r.Plain {
		t.Fatal("plain must default to false")
	}
	if r := sm.Resources["db"]; !r.Plain {
		// The flag parses everywhere; only the broker decides it's
		// filesystem-only (and says so) — the registry stays declarative.
		t.Fatalf("plain must parse on non-filesystem types too: %+v", r)
	}

	out, _ := json.Marshal(sm.Resources["files"])
	if string(out) != `{"type":"filesystem"}` {
		t.Fatalf("false plain must stay omitted, got %s", out)
	}
}
